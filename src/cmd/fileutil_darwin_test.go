//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// setFileBirthTime sets the birth time (FileCreatedDate) on path via setattrlist(2).
func setFileBirthTime(t *testing.T, path string, btime time.Time) {
	t.Helper()
	al := unix.Attrlist{
		Bitmapcount: 5, // ATTR_BIT_MAP_COUNT
		Commonattr:  attrCmnCrtime,
	}
	ts := unix.Timespec{Sec: btime.Unix(), Nsec: int64(btime.Nanosecond())}
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&ts)), unsafe.Sizeof(ts))
	require.NoError(t, unix.Setattrlist(path, &al, buf, 0))
}

// getFileBirthTime reads the birth time (FileCreatedDate) from path via Lstat.
func getFileBirthTime(t *testing.T, path string) time.Time {
	t.Helper()
	var st syscall.Stat_t
	require.NoError(t, syscall.Lstat(path, &st))
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec).UTC()
}

// TestCopyFile_PreservesBirthTime verifies that the destination file receives the same
// birth time (FileCreatedDate) as the source, not the time of the copy operation.
func TestCopyFile_PreservesBirthTime(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.jpg")
	dst := filepath.Join(tmp, "dst.jpg")
	require.NoError(t, os.WriteFile(src, []byte("media data"), 0644))

	// Set birth time to a fixed past date so it is distinguishable from "now".
	wantBirth := time.Date(2015, 3, 10, 8, 0, 0, 0, time.UTC)
	setFileBirthTime(t, src, wantBirth)

	require.NoError(t, copyFile(src, dst))

	gotBirth := getFileBirthTime(t, dst)
	assert.WithinDuration(t, wantBirth, gotBirth, time.Second,
		"dst FileCreatedDate must match src, not the copy time")
}

// TestCopyFile_SourceBirthTimeUnchanged guards against accidentally mutating the
// source file's birth time during the copy.
func TestCopyFile_SourceBirthTimeUnchanged(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.jpg")
	require.NoError(t, os.WriteFile(src, []byte("data"), 0644))

	wantBirth := time.Date(2012, 6, 20, 12, 0, 0, 0, time.UTC)
	setFileBirthTime(t, src, wantBirth)

	require.NoError(t, copyFile(src, filepath.Join(tmp, "dst.jpg")))

	srcBirth := getFileBirthTime(t, src)
	assert.WithinDuration(t, wantBirth, srcBirth, time.Second,
		"source FileCreatedDate must not be changed by copyFile")
}

// TestPreserveTimestamps_AllThreeTimestamps verifies that mtime, atime, and birth time
// are all written to the destination by a single preserveTimestamps call.
func TestPreserveTimestamps_AllThreeTimestamps(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.jpg")
	dst := filepath.Join(tmp, "dst.jpg")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0644))
	require.NoError(t, os.WriteFile(dst, []byte("x"), 0644))

	wantTime := time.Date(2017, 11, 5, 9, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(src, wantTime, wantTime))
	setFileBirthTime(t, src, wantTime)

	require.NoError(t, preserveTimestamps(src, dst))

	dstInfo, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.WithinDuration(t, wantTime, dstInfo.ModTime(), time.Second, "mtime (FileModifyDate)")

	dstBirth := getFileBirthTime(t, dst)
	assert.WithinDuration(t, wantTime, dstBirth, time.Second, "birth time (FileCreatedDate)")
}
