//go:build !darwin

package main

import "os"

// preserveTimestamps copies mtime from src to dst.
// Birth time is not available on non-macOS platforms; atime is set to mtime as a fallback.
func preserveTimestamps(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	mtime := info.ModTime()
	return os.Chtimes(dst, mtime, mtime)
}
