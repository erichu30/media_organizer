package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

// mediaExtensions is the set of file extensions recognised as media.
// Keys are lowercase; matching is case-insensitive via strings.ToLower.
var mediaExtensions = map[string]struct{}{
	// Images
	".jpg": {}, ".jpeg": {}, ".png": {}, ".gif": {}, ".bmp": {}, ".webp": {},
	".heic": {}, ".heif": {},
	".tiff": {}, ".tif": {},
	".raw": {}, ".cr2": {}, ".cr3": {}, ".nef": {}, ".arw": {}, ".dng": {},
	".orf": {}, ".rw2": {}, ".pef": {}, ".srw": {},
	// Videos
	".mp4": {}, ".mov": {}, ".m4v": {}, ".avi": {}, ".mkv": {},
	".mts": {}, ".m2ts": {}, ".3gp": {}, ".3g2": {},
	".wmv": {}, ".flv": {}, ".ts": {}, ".mpg": {}, ".mpeg": {},
}

func isMediaFile(path string) bool {
	_, ok := mediaExtensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

// osRename is a variable so tests can inject a fake EXDEV error without needing two real filesystems.
var osRename = os.Rename

// copyBufPool recycles 1 MiB buffers across copyFile calls to reduce allocations and GC pressure.
var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 1<<20)
		return &b
	},
}

// copyFile copies a file from src to dst using a pooled 1 MiB buffer.
// If the copy or sync fails, dst is removed so no partial file is left behind.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	bufp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bufp)

	_, copyErr := io.CopyBuffer(out, in, *bufp)
	syncErr := out.Sync()
	closeErr := out.Close()

	if copyErr != nil || syncErr != nil {
		os.Remove(dst)
		if copyErr != nil {
			return copyErr
		}
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}

	// Restore original timestamps. Failure is non-fatal: some destination filesystems
	// (e.g. SMB shares) may not support birth-time writes, but the data copy succeeded.
	if err := preserveTimestamps(src, dst); err != nil {
		logrus.Warnf("preserving timestamps on %s: %v", dst, err)
	}
	return nil
}
