package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// copyFileOld is the original implementation using io.Copy (32 KB kernel buffer).
// Kept here solely for before/after comparison benchmarks.
func copyFileOld(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// ---- copyFile: new (1 MiB pool) vs old (32 KB) ----

func benchmarkCopyNew(b *testing.B, size int) {
	b.Helper()
	src := filepath.Join(b.TempDir(), "src.bin")
	dst := filepath.Join(b.TempDir(), "dst.bin")
	if err := os.WriteFile(src, make([]byte, size), 0644); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(size))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := copyFile(src, dst); err != nil {
			b.Fatal(err)
		}
		_ = os.Remove(dst)
	}
}

func benchmarkCopyOld(b *testing.B, size int) {
	b.Helper()
	src := filepath.Join(b.TempDir(), "src.bin")
	dst := filepath.Join(b.TempDir(), "dst.bin")
	if err := os.WriteFile(src, make([]byte, size), 0644); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(size))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := copyFileOld(src, dst); err != nil {
			b.Fatal(err)
		}
		_ = os.Remove(dst)
	}
}

func BenchmarkCopyNew_1KB(b *testing.B)   { benchmarkCopyNew(b, 1<<10) }
func BenchmarkCopyNew_10KB(b *testing.B)  { benchmarkCopyNew(b, 10<<10) }
func BenchmarkCopyNew_100KB(b *testing.B) { benchmarkCopyNew(b, 100<<10) }
func BenchmarkCopyNew_1MB(b *testing.B)   { benchmarkCopyNew(b, 1<<20) }
func BenchmarkCopyNew_10MB(b *testing.B)  { benchmarkCopyNew(b, 10<<20) }
func BenchmarkCopyNew_100MB(b *testing.B) { benchmarkCopyNew(b, 100<<20) }

func BenchmarkCopyOld_1KB(b *testing.B)   { benchmarkCopyOld(b, 1<<10) }
func BenchmarkCopyOld_10KB(b *testing.B)  { benchmarkCopyOld(b, 10<<10) }
func BenchmarkCopyOld_100KB(b *testing.B) { benchmarkCopyOld(b, 100<<10) }
func BenchmarkCopyOld_1MB(b *testing.B)   { benchmarkCopyOld(b, 1<<20) }
func BenchmarkCopyOld_10MB(b *testing.B)  { benchmarkCopyOld(b, 10<<20) }
func BenchmarkCopyOld_100MB(b *testing.B) { benchmarkCopyOld(b, 100<<20) }

// ---- collectFiles: walk overhead by collection size ----

func benchmarkCollectFiles(b *testing.B, n int) {
	b.Helper()
	dir := b.TempDir()
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("file_%06d.jpg", i))
		if err := os.WriteFile(p, []byte{}, 0644); err != nil {
			b.Fatal(err)
		}
	}
	app := &App{Config: &Config{InputPath: dir}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.collectFiles()
	}
}

func BenchmarkCollectFiles_100(b *testing.B)   { benchmarkCollectFiles(b, 100) }
func BenchmarkCollectFiles_1000(b *testing.B)  { benchmarkCollectFiles(b, 1_000) }
func BenchmarkCollectFiles_10000(b *testing.B) { benchmarkCollectFiles(b, 10_000) }
func BenchmarkCollectFiles_50000(b *testing.B) { benchmarkCollectFiles(b, 50_000) }

// ---- dirCache: MkdirAll every call vs sync.Map hit ----

func BenchmarkDirCreate_NoCache(b *testing.B) {
	dir := b.TempDir()
	target := filepath.Join(dir, "2023", "05")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := os.MkdirAll(target, os.ModePerm); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDirCreate_WithCache(b *testing.B) {
	dir := b.TempDir()
	target := filepath.Join(dir, "2023", "05")
	app := &App{Config: &Config{}}
	_ = os.MkdirAll(target, os.ModePerm)
	app.dirCache.Store(target, struct{}{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, loaded := app.dirCache.Load(target); !loaded {
			if err := os.MkdirAll(target, os.ModePerm); err != nil {
				b.Fatal(err)
			}
			app.dirCache.Store(target, struct{}{})
		}
	}
}
