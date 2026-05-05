package internal

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var benchDates = []string{
	"2023:01:01 12:00:00-05:00",
	"2023:01:01 12:00:00",
	"2023:01:01",
}

func BenchmarkParseExifDate(b *testing.B) {
	for i := 0; i < b.N; i++ {
		for _, d := range benchDates {
			ParseExifDate(d) //nolint:errcheck
		}
	}
}

// ---- ExifTool pool throughput: pool-size 1 vs 4 vs 8 ----
// These benchmarks require the exiftool binary and are skipped automatically when absent.

// makeTaggedJPEG writes a minimal JPEG with DateTimeOriginal set via exiftool.
func makeTaggedJPEG(b *testing.B, dir, name string) string {
	b.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, minimalJPEG, 0644); err != nil {
		b.Fatal(err)
	}
	out, err := exec.Command("exiftool", "-overwrite_original", "-DateTimeOriginal=2023:06:01 12:00:00", p).CombinedOutput()
	if err != nil {
		b.Skipf("exiftool unavailable: %v — %s", err, out)
	}
	return p
}

func benchmarkExifPool(b *testing.B, poolSize int) {
	b.Helper()
	if _, err := exec.LookPath("exiftool"); err != nil {
		b.Skip("exiftool not found in PATH")
	}

	dir := b.TempDir()
	// Create 64 tagged files so b.N can run many iterations without path re-use issues.
	const nFiles = 64
	files := make([]string, nFiles)
	for i := 0; i < nFiles; i++ {
		files[i] = makeTaggedJPEG(b, dir, filepath.Base(b.Name())+".jpg")
		// give each a unique name
		dst := filepath.Join(dir, filepath.Base(b.Name())+"."+string(rune('A'+i))+".jpg")
		os.Rename(files[i], dst)
		files[i] = dst
	}

	pool, err := NewExifToolPool(poolSize)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()

	b.ResetTimer()
	b.SetParallelism(poolSize)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			pool.ExtractDate(files[i%nFiles], false, false) //nolint:errcheck
			i++
		}
	})
}

func BenchmarkExifPool_Size1(b *testing.B)  { benchmarkExifPool(b, 1) }
func BenchmarkExifPool_Size4(b *testing.B)  { benchmarkExifPool(b, 4) }
func BenchmarkExifPool_Size8(b *testing.B)  { benchmarkExifPool(b, 8) }
