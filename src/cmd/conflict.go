package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Conflict policies accepted by -on-conflict. Two files that share a basename and
// land in the same YYYY/MM directory are the normal case, not an edge case: cameras
// restart their counters, and IMG_0001.jpg recurs across imports.
const (
	ConflictRename    = "rename"    // give the incoming file a numbered suffix
	ConflictSkip      = "skip"      // leave the destination alone, count the file as skipped
	ConflictOverwrite = "overwrite" // replace the destination
	ConflictFail      = "fail"      // treat the collision as a per-file failure
)

// conflictPolicies lists the accepted -on-conflict values in help order.
var conflictPolicies = []string{ConflictRename, ConflictSkip, ConflictOverwrite, ConflictFail}

// maxRenameAttempts bounds the "_1", "_2", … search so a pathological directory
// cannot spin forever.
const maxRenameAttempts = 1000

// rcloneExitDirNotFound is rclone's exit code for "directory not found". For a
// listing that just means the destination is empty, not that anything went wrong.
const rcloneExitDirNotFound = 3

// errListDest reports that a destination directory could not be listed, so whether
// a transfer would overwrite an existing file is unknown.
var errListDest = errors.New("cannot list destination directory")

// targetAction is what resolveTarget decided should happen to a file.
type targetAction int

const (
	actionTransfer      targetAction = iota // transfer to the returned path
	actionSkipIdentical                     // destination already holds a same-sized file
	actionSkipConflict                      // -on-conflict=skip and the destination differs
)

// destIndex is a lazily-populated listing of one remote destination directory,
// mapping filename to size in bytes. Local destinations are probed with os.Lstat
// instead, which is cheap enough not to need caching.
type destIndex struct {
	once  sync.Once
	names map[string]int64
	err   error
}

// resolveTarget applies the -on-conflict policy to a planned destination and
// returns the path the file should actually be written to.
//
// A destination already holding a file of the same size is treated as "transferred
// by an earlier run" whatever the policy says, and skipped. Without that check,
// re-running the same input under -copy would pile up photo_1.jpg, photo_2.jpg, …
// on every pass.
//
// The returned path is reserved for the caller, so two workers racing on the same
// basename cannot settle on the same numbered suffix.
func (app *App) resolveTarget(ctx context.Context, src, targetDir, targetPath string) (string, targetAction, string, error) {
	if app.Config.OnConflict == ConflictOverwrite {
		return targetPath, actionTransfer, "", nil
	}

	// Size is the only cross-destination identity signal available cheaply: rclone
	// reports it for every backend, and os.Lstat gives it locally.
	srcSize := int64(-1)
	if fi, err := os.Lstat(src); err == nil {
		srcSize = fi.Size()
	}

	name := app.destBase(targetPath)
	free, size, exists, err := app.claimIfFree(ctx, targetDir, name)
	if err != nil {
		return "", actionTransfer, "destination listing failed", err
	}
	if free {
		return targetPath, actionTransfer, "", nil
	}
	if exists && srcSize >= 0 && size == srcSize {
		return targetPath, actionSkipIdentical, "", nil
	}

	switch app.Config.OnConflict {
	case ConflictSkip:
		return targetPath, actionSkipConflict, "", nil
	case ConflictFail:
		return "", actionTransfer, "destination already exists",
			fmt.Errorf("destination already exists: %s", targetPath)
	}

	// ConflictRename (also the zero value): walk _1, _2, … until a name is free.
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i <= maxRenameAttempts; i++ {
		candidate := fmt.Sprintf("%s_%d%s", stem, i, ext)
		free, size, exists, err := app.claimIfFree(ctx, targetDir, candidate)
		if err != nil {
			return "", actionTransfer, "destination listing failed", err
		}
		if free {
			return app.destJoin(targetDir, candidate), actionTransfer, "", nil
		}
		if exists && srcSize >= 0 && size == srcSize {
			return app.destJoin(targetDir, candidate), actionSkipIdentical, "", nil
		}
	}

	return "", actionTransfer, "too many name conflicts",
		fmt.Errorf("gave up after %d name conflicts for %s", maxRenameAttempts, targetPath)
}

// claimIfFree reserves name inside targetDir when nothing else holds it.
//
// free=true means the caller now owns the name. Otherwise exists and size describe
// the file already sitting at the destination; exists=false with free=false means
// another worker in this run claimed the name first.
func (app *App) claimIfFree(ctx context.Context, targetDir, name string) (free bool, size int64, exists bool, err error) {
	full := app.destJoin(targetDir, name)
	if _, taken := app.claimed.LoadOrStore(full, struct{}{}); taken {
		return false, 0, false, nil
	}

	size, exists, err = app.destHas(ctx, targetDir, name)
	if err != nil || exists {
		app.claimed.Delete(full)
		return false, size, exists, err
	}
	return true, 0, false, nil
}

// destHas reports whether name already exists in targetDir and, if so, its size.
func (app *App) destHas(ctx context.Context, targetDir, name string) (int64, bool, error) {
	if !app.Config.IsRemote {
		fi, err := os.Lstat(filepath.Join(targetDir, name))
		if err != nil {
			if os.IsNotExist(err) {
				return 0, false, nil
			}
			return 0, false, err
		}
		return fi.Size(), true, nil
	}

	names, err := app.remoteDirIndex(ctx, targetDir)
	if err != nil {
		return 0, false, err
	}
	size, ok := names[name]
	return size, ok, nil
}

// remoteDirIndex lists a remote destination directory once per run and caches it.
// One rclone call per YYYY/MM directory is far cheaper than one probe per file, and
// the cache stays correct because every name this run writes also goes into
// app.claimed.
func (app *App) remoteDirIndex(ctx context.Context, dir string) (map[string]int64, error) {
	v, _ := app.destDirs.LoadOrStore(dir, &destIndex{})
	idx := v.(*destIndex)
	idx.once.Do(func() {
		idx.names, idx.err = rcloneListDir(ctx, dir)
	})
	return idx.names, idx.err
}

// rcloneListDir returns filename → size for one remote directory. A directory that
// does not exist yet is reported as an empty listing rather than an error.
//
// Declared as a variable so tests can substitute a listing without a real remote.
var rcloneListDir = func(ctx context.Context, dir string) (map[string]int64, error) {
	listCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(listCtx, "rclone", "lsf", "--files-only",
		"--format", "sp", "--separator", ";", "--retries", "1", dir)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == rcloneExitDirNotFound {
				return map[string]int64{}, nil
			}
			// rclone explains itself on stderr — "didn't find section in config
			// file", a rejected key, an unreachable host. Losing that turns every
			// remote misconfiguration into an unexplained failure count.
			if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
				return nil, fmt.Errorf("%w %s: %w: %s", errListDest, dir, err, stderr)
			}
		}
		return nil, fmt.Errorf("%w %s: %w", errListDest, dir, err)
	}

	names := make(map[string]int64)
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	// Deep directories can exceed the 64 KiB default line budget.
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		// The first ";" separates size from name; a name may contain further ones.
		sep := strings.IndexByte(line, ';')
		if sep < 0 {
			continue
		}
		size, convErr := strconv.ParseInt(line[:sep], 10, 64)
		if convErr != nil {
			size = -1
		}
		names[line[sep+1:]] = size
	}
	return names, sc.Err()
}

// destBase returns the final element of a destination path. Remote paths always use
// "/" regardless of host OS, so they are split explicitly rather than via filepath.
func (app *App) destBase(p string) string {
	if app.Config.IsRemote {
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			return p[i+1:]
		}
		return p
	}
	return filepath.Base(p)
}

// destJoin joins a destination directory and filename with the right separator.
func (app *App) destJoin(dir, name string) string {
	if app.Config.IsRemote {
		return dir + "/" + name
	}
	return filepath.Join(dir, name)
}
