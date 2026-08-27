package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newConflictApp builds an App configured for a local destination under dir.
func newConflictApp(dir, policy string) *App {
	return &App{Config: &Config{OutputPath: dir, OnConflict: policy}}
}

// writeSized creates a file of exactly n bytes and returns its path.
func writeSized(t *testing.T, dir, name string, n int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, make([]byte, n), 0644))
	return path
}

func TestResolveTarget_FreeNamePassesThrough(t *testing.T) {
	tmp := t.TempDir()
	src := writeSized(t, tmp, "src/photo.jpg", 10)
	dstDir := filepath.Join(tmp, "out", "2023", "05")
	want := filepath.Join(dstDir, "photo.jpg")

	got, action, _, err := newConflictApp(tmp, ConflictRename).
		resolveTarget(context.Background(), src, dstDir, want)

	require.NoError(t, err)
	assert.Equal(t, actionTransfer, action)
	assert.Equal(t, want, got)
}

// TestResolveTarget_RenamesOnCollision is the regression test for silent data loss:
// two different photos sharing a basename must both survive.
func TestResolveTarget_RenamesOnCollision(t *testing.T) {
	tmp := t.TempDir()
	src := writeSized(t, tmp, "src/photo.jpg", 10)
	dstDir := filepath.Join(tmp, "out")
	writeSized(t, dstDir, "photo.jpg", 999) // a different file, same name

	got, action, _, err := newConflictApp(tmp, ConflictRename).
		resolveTarget(context.Background(), src, dstDir, filepath.Join(dstDir, "photo.jpg"))

	require.NoError(t, err)
	assert.Equal(t, actionTransfer, action)
	assert.Equal(t, filepath.Join(dstDir, "photo_1.jpg"), got)
}

func TestResolveTarget_RenameWalksPastTakenSuffixes(t *testing.T) {
	tmp := t.TempDir()
	src := writeSized(t, tmp, "src/photo.jpg", 10)
	dstDir := filepath.Join(tmp, "out")
	for _, name := range []string{"photo.jpg", "photo_1.jpg", "photo_2.jpg"} {
		writeSized(t, dstDir, name, 999)
	}

	got, _, _, err := newConflictApp(tmp, ConflictRename).
		resolveTarget(context.Background(), src, dstDir, filepath.Join(dstDir, "photo.jpg"))

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dstDir, "photo_3.jpg"), got)
}

// TestResolveTarget_SameSizeIsAlreadyPresent keeps re-runs idempotent: without it a
// second -copy pass over the same input would create photo_1.jpg, then photo_2.jpg.
func TestResolveTarget_SameSizeIsAlreadyPresent(t *testing.T) {
	tmp := t.TempDir()
	src := writeSized(t, tmp, "src/photo.jpg", 42)
	dstDir := filepath.Join(tmp, "out")
	writeSized(t, dstDir, "photo.jpg", 42)

	for _, policy := range []string{ConflictRename, ConflictSkip, ConflictFail} {
		t.Run(policy, func(t *testing.T) {
			_, action, _, err := newConflictApp(tmp, policy).
				resolveTarget(context.Background(), src, dstDir, filepath.Join(dstDir, "photo.jpg"))

			require.NoError(t, err)
			assert.Equal(t, actionSkipIdentical, action)
		})
	}
}

func TestResolveTarget_SkipPolicy(t *testing.T) {
	tmp := t.TempDir()
	src := writeSized(t, tmp, "src/photo.jpg", 10)
	dstDir := filepath.Join(tmp, "out")
	writeSized(t, dstDir, "photo.jpg", 999)

	_, action, _, err := newConflictApp(tmp, ConflictSkip).
		resolveTarget(context.Background(), src, dstDir, filepath.Join(dstDir, "photo.jpg"))

	require.NoError(t, err)
	assert.Equal(t, actionSkipConflict, action)
}

func TestResolveTarget_FailPolicy(t *testing.T) {
	tmp := t.TempDir()
	src := writeSized(t, tmp, "src/photo.jpg", 10)
	dstDir := filepath.Join(tmp, "out")
	writeSized(t, dstDir, "photo.jpg", 999)

	_, _, reason, err := newConflictApp(tmp, ConflictFail).
		resolveTarget(context.Background(), src, dstDir, filepath.Join(dstDir, "photo.jpg"))

	require.Error(t, err)
	assert.Equal(t, "destination already exists", reason)
}

// TestResolveTarget_OverwritePolicySkipsAllChecks confirms the escape hatch still
// returns the plain destination without probing it.
func TestResolveTarget_OverwritePolicySkipsAllChecks(t *testing.T) {
	tmp := t.TempDir()
	src := writeSized(t, tmp, "src/photo.jpg", 10)
	dstDir := filepath.Join(tmp, "out")
	writeSized(t, dstDir, "photo.jpg", 999)
	want := filepath.Join(dstDir, "photo.jpg")

	got, action, _, err := newConflictApp(tmp, ConflictOverwrite).
		resolveTarget(context.Background(), src, dstDir, want)

	require.NoError(t, err)
	assert.Equal(t, actionTransfer, action)
	assert.Equal(t, want, got)
}

// TestResolveTarget_ConcurrentClaimsAreUnique covers the in-run race: two workers
// resolving the same basename at the same moment must not be handed the same path,
// because neither file exists at the destination yet for the other one to see.
func TestResolveTarget_ConcurrentClaimsAreUnique(t *testing.T) {
	tmp := t.TempDir()
	dstDir := filepath.Join(tmp, "out")
	require.NoError(t, os.MkdirAll(dstDir, 0755))
	app := newConflictApp(tmp, ConflictRename)

	const workers = 16
	results := make([]string, workers)
	var wg sync.WaitGroup
	for i := range workers {
		// Distinct sizes so the already-present shortcut never applies.
		src := writeSized(t, tmp, fmt.Sprintf("src/in%02d/photo.jpg", i), i+1)
		wg.Add(1)
		go func(i int, src string) {
			defer wg.Done()
			got, _, _, err := app.resolveTarget(context.Background(), src, dstDir,
				filepath.Join(dstDir, "photo.jpg"))
			assert.NoError(t, err)
			results[i] = got
		}(i, src)
	}
	wg.Wait()

	seen := make(map[string]int, workers)
	for _, r := range results {
		seen[r]++
	}
	assert.Len(t, seen, workers, "every worker must get its own destination path")
}

func TestResolveTarget_RemoteUsesCachedListing(t *testing.T) {
	var calls int
	orig := rcloneListDir
	t.Cleanup(func() { rcloneListDir = orig })
	rcloneListDir = func(_ context.Context, _ string) (map[string]int64, error) {
		calls++
		return map[string]int64{"photo.jpg": 999}, nil
	}

	app := &App{Config: &Config{OutputPath: "remote:/photos", OnConflict: ConflictRename, IsRemote: true}}
	src := writeSized(t, t.TempDir(), "photo.jpg", 10)

	for range 5 {
		got, _, _, err := app.resolveTarget(context.Background(), src,
			"remote:/photos/2023/05", "remote:/photos/2023/05/photo.jpg")
		require.NoError(t, err)
		assert.Contains(t, got, "photo_")
	}
	assert.Equal(t, 1, calls, "a remote directory must be listed once per run, not once per file")
}

func TestResolveTarget_RemoteListingFailureIsReported(t *testing.T) {
	orig := rcloneListDir
	t.Cleanup(func() { rcloneListDir = orig })
	rcloneListDir = func(_ context.Context, _ string) (map[string]int64, error) {
		return nil, errListDest
	}

	app := &App{Config: &Config{OutputPath: "remote:/p", OnConflict: ConflictRename, IsRemote: true}}
	src := writeSized(t, t.TempDir(), "photo.jpg", 10)

	_, _, reason, err := app.resolveTarget(context.Background(), src,
		"remote:/p/2023/05", "remote:/p/2023/05/photo.jpg")

	require.Error(t, err)
	assert.Equal(t, "destination listing failed", reason)
}

func TestDestBaseAndJoin(t *testing.T) {
	remote := &App{Config: &Config{IsRemote: true}}
	local := &App{Config: &Config{IsRemote: false}}

	// Remote paths always use "/", whatever the host OS separator is.
	assert.Equal(t, "photo.jpg", remote.destBase("remote:/photos/2023/05/photo.jpg"))
	assert.Equal(t, "remote:/photos/2023/05/photo.jpg",
		remote.destJoin("remote:/photos/2023/05", "photo.jpg"))

	assert.Equal(t, "photo.jpg", local.destBase(filepath.Join("out", "2023", "photo.jpg")))
	assert.Equal(t, filepath.Join("out", "2023", "photo.jpg"),
		local.destJoin(filepath.Join("out", "2023"), "photo.jpg"))
}

// ---- end-to-end: the collision must not lose a file ----

// TestProcessFile_CollidingMovesKeepBothFiles is the whole point of the conflict
// policy: before it, the second move overwrote the first and the summary still
// counted two successes.
func TestProcessFile_CollidingMovesKeepBothFiles(t *testing.T) {
	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "out")
	fixed := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)

	first := writeSized(t, tmp, "roll-a/IMG_0001.jpg", 100)
	second := writeSized(t, tmp, "roll-b/IMG_0001.jpg", 200)

	svc := new(MockExifService)
	svc.On("ExtractDate", first, false, false).Return(fixed, "DateTimeOriginal", nil)
	svc.On("ExtractDate", second, false, false).Return(fixed, "DateTimeOriginal", nil)

	app := &App{Config: &Config{OutputPath: outDir, OnConflict: ConflictRename}, ExifService: svc}

	_, err := app.processFile(context.Background(), first)
	require.NoError(t, err)
	_, err = app.processFile(context.Background(), second)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(outDir, "2023", "05", "IMG_0001.jpg"))
	assert.FileExists(t, filepath.Join(outDir, "2023", "05", "IMG_0001_1.jpg"))
	assert.Equal(t, int64(1), app.stats.renamed.Load())
	svc.AssertExpectations(t)
}

// TestProcessFile_DryRunCreatesNoDirectories guards the promise -dry-run makes:
// the destination tree must be untouched, empty YYYY/MM directories included.
func TestProcessFile_DryRunCreatesNoDirectories(t *testing.T) {
	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "out")
	src := writeSized(t, tmp, "photo.jpg", 10)

	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).
		Return(time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC), "DateTimeOriginal", nil)

	app := &App{Config: &Config{OutputPath: outDir, DryRun: true, OnConflict: ConflictRename}, ExifService: svc}
	_, err := app.processFile(context.Background(), src)

	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(outDir, "2023"))
	assert.True(t, os.IsNotExist(statErr), "dry-run must not create destination directories")
}

// TestProcessFile_SkippedFilesAreNotAlsoSuccesses guards the summary arithmetic: a
// file that was skipped belongs in exactly one counter, so the totals still add up
// to the number of media files found.
func TestProcessFile_SkippedFilesAreNotAlsoSuccesses(t *testing.T) {
	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "out")
	fixed := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)

	src := writeSized(t, tmp, "photo.jpg", 100)
	writeSized(t, outDir, filepath.Join("2023", "05", "photo.jpg"), 100) // same size

	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(fixed, "DateTimeOriginal", nil)

	app := &App{Config: &Config{OutputPath: outDir, OnConflict: ConflictRename, CopyMode: true}, ExifService: svc}
	reason, err := app.processFile(context.Background(), src)

	require.NoError(t, err)
	assert.Equal(t, reasonSkipped, reason, "the worker relies on this to skip addSuccess")
	assert.Equal(t, int64(1), app.stats.alreadyPresent.Load())
	assert.Zero(t, app.stats.success.Load())
}
