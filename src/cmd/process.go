package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

// reasonSkipped is returned with a nil error when processFile deliberately did not
// transfer a file — it was already at the destination, or -on-conflict=skip left it
// alone. The worker must not count these as successes: processFile has already put
// them in their own counter, and counting them twice makes the summary add up to
// more files than the run found.
const reasonSkipped = "skipped"

// processFile handles a single file: extract date, determine destination, move/copy.
// Returns a short failure-reason string (empty on success) alongside any error.
func (app *App) processFile(ctx context.Context, path string) (string, error) {
	t, reason, err := app.extractDate(path)
	if err != nil {
		logrus.Warnf("Cannot extract date for %s: %v", path, err)
		return reason, err
	}

	year := fmt.Sprintf("%04d", t.Year())
	month := fmt.Sprintf("%02d", int(t.Month()))

	var targetDir, targetPath string
	if app.Config.IsRemote {
		// rclone creates destination directories automatically; no mkdir step needed.
		targetDir = app.Config.OutputPath + "/" + year + "/" + month
		targetPath = targetDir + "/" + filepath.Base(path)
	} else {
		targetDir = filepath.Join(app.Config.OutputPath, year, month)
		targetPath = filepath.Join(targetDir, filepath.Base(path))
	}

	finalPath, action, reason, err := app.resolveTarget(ctx, path, targetDir, targetPath)
	if err != nil {
		// A dry run is a preview, not a transfer: an unreachable destination should
		// not stop it from reporting what it would have done.
		if !app.Config.DryRun {
			if errors.Is(err, errListDest) {
				app.reportDestListError(targetDir, err)
				// A destination that cannot be listed will not accept transfers
				// either, so let the circuit breaker end the run early.
				app.recordRemoteFailure()
			}
			return reason, err
		}
		logrus.Warnf("[DRY-RUN] cannot check destination for %s: %v", path, err)
		finalPath, action = targetPath, actionTransfer
	}

	switch action {
	case actionSkipIdentical:
		logrus.Infof("Already at destination, skipping: %s (matches %s)", path, finalPath)
		app.stats.addAlreadyPresent()
		return reasonSkipped, nil
	case actionSkipConflict:
		logrus.Infof("Destination exists, skipping (-on-conflict=skip): %s → %s", path, finalPath)
		app.stats.addSkippedConflict()
		return reasonSkipped, nil
	}
	if finalPath != targetPath {
		logrus.Infof("Name conflict: %s renamed to %s", targetPath, app.destBase(finalPath))
		app.stats.addRenamed()
	}

	if app.Config.DryRun {
		logrus.Infof("[DRY-RUN] Move: %s → %s (copy=%v)", path, finalPath, app.Config.CopyMode)
		return "", nil
	}

	// Created only once the transfer is actually going to happen, so a dry run
	// leaves no empty YYYY/MM directories behind.
	if !app.Config.IsRemote {
		if _, loaded := app.dirCache.Load(targetDir); !loaded {
			if err := os.MkdirAll(targetDir, os.ModePerm); err != nil {
				return "directory creation failed", fmt.Errorf("failed to create dir %s: %w", targetDir, err)
			}
			app.dirCache.Store(targetDir, struct{}{})
		}
	}

	logrus.Infof("Move: %s → %s (copy=%v)", path, finalPath, app.Config.CopyMode)
	if app.Config.Debug {
		logrus.Debugf("%s → %s (copy=%v)", path, finalPath, app.Config.CopyMode)
	}

	if app.Config.IsRemote {
		return app.transferRemote(ctx, path, finalPath)
	}
	return app.transferLocal(ctx, path, finalPath)
}

// extractDate extracts the creation date from a file's EXIF metadata.
// Returns a short failure-reason string (empty on success) alongside any error.
func (app *App) extractDate(path string) (time.Time, string, error) {
	t, tag, err := app.ExifService.ExtractDate(path, app.Config.Debug, app.Config.UseFileModifyDate)
	if err != nil {
		logrus.Errorf("Failed to extract date for %s: %v", path, err)
		return time.Time{}, "no EXIF date", err
	}

	hasDateTimeOriginal := tag == "DateTimeOriginal"
	if app.Config.OnlyDateTimeOriginal && !hasDateTimeOriginal {
		logrus.Infof("Skipping %s because it does not have DateTimeOriginal tag", path)
		return time.Time{}, "DateTimeOriginal not found", fmt.Errorf("DateTimeOriginal not found")
	}

	if t.IsZero() {
		logrus.Warnf("No valid date found for %s", path)
		return time.Time{}, "no EXIF date", fmt.Errorf("no valid date found in EXIF or file system")
	}
	return t, "", nil
}

// transferRemote runs rclone moveto/copyto with retry and circuit-breaker support.
//
// On any failure (signal, SSH disconnect, timeout, …):
//  1. A best-effort rclone deletefile removes any partial remote file so re-runs start clean.
//  2. The consecutive-failure counter is incremented; at the threshold the circuit breaker
//     fires, cancelling the run context so the feeder stops dispatching new work.
//
// On success the counter is reset to zero.
func (app *App) transferRemote(ctx context.Context, src, dst string) (string, error) {
	subCmd := "moveto"
	if app.Config.CopyMode {
		subCmd = "copyto"
	}

	args := []string{subCmd, src, dst}
	if app.Config.Retries > 0 {
		args = append(args, "--retries", strconv.Itoa(app.Config.Retries))
	}
	rcloneCmd := exec.CommandContext(ctx, "rclone", args...)
	if app.Config.Debug {
		logrus.Debugf("Executing: %s", rcloneCmd.String())
	}

	output, err := rcloneCmd.CombinedOutput()
	if err == nil {
		app.resetRemoteFailures()
		return "", nil
	}

	// On any rclone failure, attempt to remove the partial remote file so re-runs start clean.
	logrus.Warnf("rclone %s failed for %s; attempting remote cleanup of %s", subCmd, src, dst)
	app.cleanupRemoteFile(dst)

	if ctx.Err() != nil {
		app.recordRemoteFailure()
		return "file operation failed", fmt.Errorf("rclone %s interrupted: %w", subCmd, ctx.Err())
	}

	app.recordRemoteFailure()
	app.reportRemoteError(src, err, string(output))
	return "file operation failed", fmt.Errorf("rclone %s %s: %w, output: %s", subCmd, src, err, string(output))
}

// reportRemoteError shows the first rclone failure of a run on stderr, with the
// command's own output. Later failures go only to the log.
//
// Without this the terminal shows "file operation failed: 300" and nothing else,
// while the one line that explains it — a remote name that is not in the config, a
// rejected SSH key — sits in the log file the user has no reason to open yet. One
// worked example is enough to diagnose it; 300 copies would bury the summary.
func (app *App) reportRemoteError(src string, err error, output string) {
	if !app.remoteErrShown.CompareAndSwap(false, true) {
		return
	}
	fmt.Fprintf(os.Stderr, "\nrclone failed on %s: %v\n", src, err)
	for _, line := range firstLines(output, 3) {
		fmt.Fprintf(os.Stderr, "  %s\n", line)
	}
	fmt.Fprintf(os.Stderr, "  (further rclone errors go to %s)\n\n", app.Config.LogPath)
}

// reportDestListError shows the first destination-listing failure of a run. A bad
// remote now fails here rather than at transfer time, so this path needs the same
// treatment as reportRemoteError or the cause disappears again.
func (app *App) reportDestListError(dir string, err error) {
	if !app.remoteErrShown.CompareAndSwap(false, true) {
		return
	}
	fmt.Fprintf(os.Stderr, "\nCould not read the destination %s:\n", dir)
	for _, line := range firstLines(err.Error(), 3) {
		fmt.Fprintf(os.Stderr, "  %s\n", line)
	}
	fmt.Fprintf(os.Stderr, "  Check the remote name with: rclone listremotes\n")
	fmt.Fprintf(os.Stderr, "  (further errors go to %s)\n\n", app.Config.LogPath)
}

// firstLines returns up to n non-blank lines of s.
func firstLines(s string, n int) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		out = append(out, line)
		if len(out) == n {
			break
		}
	}
	return out
}

// cleanupRemoteFile attempts rclone deletefile on dst using a fresh 30-second context.
// Errors are logged as warnings — this is always best-effort.
func (app *App) cleanupRemoteFile(dst string) {
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanCancel()
	// --retries 1: fail fast; if SSH is down, retrying the cleanup won't help.
	if cleanErr := exec.CommandContext(cleanCtx, "rclone", "deletefile", "--retries", "1", dst).Run(); cleanErr != nil {
		logrus.Warnf("remote cleanup of %s failed (manual removal may be needed): %v", dst, cleanErr)
	} else {
		logrus.Infof("remote cleanup succeeded: removed partial %s", dst)
	}
}

// recordRemoteFailure increments the consecutive failure counter and fires the
// circuit breaker when the threshold is reached. Safe for concurrent callers.
// Returns true if the circuit breaker fired on this call.
func (app *App) recordRemoteFailure() bool {
	n := app.consRemoteFailures.Add(1)
	threshold := int64(app.Config.RemoteFailThreshold)
	if threshold <= 0 || n < threshold {
		return false
	}
	// Only the first crossing announces itself. Workers still finishing their
	// in-flight transfers keep incrementing the counter, and each one used to repeat
	// this line — a hundred near-identical warnings scrolling past the summary.
	if !app.breakerFired.CompareAndSwap(false, true) {
		return false
	}
	msg := fmt.Sprintf("circuit breaker: %d consecutive remote failures — aborting remaining transfers", n)
	logrus.Error(msg)
	fmt.Fprintf(os.Stderr, "\n%s\n", msg)
	if app.runCancel != nil {
		app.runCancel()
	}
	return true
}

// resetRemoteFailures resets the consecutive failure counter after a successful transfer.
func (app *App) resetRemoteFailures() {
	app.consRemoteFailures.Store(0)
}

// transferLocal moves or copies a file within the local filesystem.
// For cross-device moves (EXDEV), it falls back to copy+delete. If a signal arrives
// after the copy succeeds but before the source is deleted, the delete is skipped so
// the source is preserved — the copy at the destination is the authoritative copy.
func (app *App) transferLocal(ctx context.Context, src, dst string) (string, error) {
	if app.Config.CopyMode {
		if err := copyFile(src, dst); err != nil {
			return "file operation failed", err
		}
		return "", nil
	}

	if err := osRename(src, dst); err != nil {
		var linkErr *os.LinkError
		if !errors.As(err, &linkErr) || !errors.Is(linkErr.Err, syscall.EXDEV) {
			return "file operation failed", err
		}
		// Source and destination are on different filesystems (e.g. local → SMB/NFS mount).
		if copyErr := copyFile(src, dst); copyErr != nil {
			return "file operation failed", copyErr
		}
		// If a signal arrived after the copy completed, skip removing the source.
		if ctx.Err() != nil {
			logrus.Warnf("interrupted after copy; source preserved: %s (copy at: %s)", src, dst)
			return "", nil
		}
		if removeErr := os.Remove(src); removeErr != nil {
			return "file operation failed", removeErr
		}
	}

	return "", nil
}
