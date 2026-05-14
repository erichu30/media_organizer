package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

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
	var targetDir string
	if app.Config.IsRemote {
		// rclone creates destination directories automatically; no mkdir step needed.
		targetDir = app.Config.OutputPath + "/" + year + "/" + month
	} else {
		targetDir = filepath.Join(app.Config.OutputPath, year, month)
		if _, loaded := app.dirCache.Load(targetDir); !loaded {
			if err := os.MkdirAll(targetDir, os.ModePerm); err != nil {
				return "directory creation failed", fmt.Errorf("failed to create dir %s: %w", targetDir, err)
			}
			app.dirCache.Store(targetDir, struct{}{})
		}
	}

	var targetPath string
	if app.Config.IsRemote {
		targetPath = app.Config.OutputPath + "/" + year + "/" + month + "/" + filepath.Base(path)
	} else {
		targetPath = filepath.Join(targetDir, filepath.Base(path))
	}

	if app.Config.DryRun {
		logrus.Infof("[DRY-RUN] Move: %s → %s (copy=%v)", path, targetPath, app.Config.CopyMode)
		return "", nil
	}

	logrus.Infof("Move: %s → %s (copy=%v)", path, targetPath, app.Config.CopyMode)
	if app.Config.Debug {
		logrus.Debugf("%s → %s (copy=%v)", path, targetPath, app.Config.CopyMode)
	}

	if app.Config.IsRemote {
		return app.transferRemote(ctx, path, targetPath)
	}
	return app.transferLocal(ctx, path, targetPath)
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
	return "file operation failed", fmt.Errorf("rclone %s %s: %w, output: %s", subCmd, src, err, string(output))
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
