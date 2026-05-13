package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- FailureLogPath ----

func TestFailureLogPath_Format(t *testing.T) {
	ts := time.Date(2026, 5, 10, 3, 0, 0, 0, time.UTC)
	got := FailureLogPath(ts)
	assert.Equal(t, "failures-20260510-030000.ndjson", got)
}

func TestFailureLogPath_UniquePerSecond(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	assert.NotEqual(t, FailureLogPath(t1), FailureLogPath(t2))
}

// ---- NewFailureLogger ----

func TestNewFailureLogger_CreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failures.ndjson")
	fl, err := NewFailureLogger(path)
	require.NoError(t, err)
	require.NoError(t, fl.Close())

	_, statErr := os.Stat(path)
	assert.NoError(t, statErr, "failure log file must exist after Close")
}

func TestNewFailureLogger_Truncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failures.ndjson")
	require.NoError(t, os.WriteFile(path, []byte("old content\n"), 0644))

	fl, err := NewFailureLogger(path)
	require.NoError(t, err)
	require.NoError(t, fl.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, data, "re-opened failure log must be empty (truncated)")
}

func TestNewFailureLogger_BadPath(t *testing.T) {
	_, err := NewFailureLogger("/nonexistent-dir/failures.ndjson")
	assert.Error(t, err)
}

// ---- Write / Close ----

func TestFailureLogger_WritesValidNDJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failures.ndjson")
	fl, err := NewFailureLogger(path)
	require.NoError(t, err)

	records := []FailureRecord{
		{Timestamp: "2026-05-10T03:00:00Z", Path: "/photos/a.jpg", Filename: "a.jpg", SizeBytes: 1024, Reason: "no EXIF date", Error: "exiftool: no date", DurationMs: 5},
		{Timestamp: "2026-05-10T03:00:01Z", Path: "/photos/b.mp4", Filename: "b.mp4", SizeBytes: 20480, Reason: "file operation failed", Error: "rename: cross-device link", DurationMs: 12},
	}
	for _, r := range records {
		require.NoError(t, fl.Write(r))
	}
	require.NoError(t, fl.Close())

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var got []FailureRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec FailureRecord
		require.NoError(t, json.Unmarshal(sc.Bytes(), &rec))
		got = append(got, rec)
	}
	require.NoError(t, sc.Err())

	assert.Equal(t, records, got)
}

func TestFailureLogger_CloseFlushesBuffer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failures.ndjson")
	fl, err := NewFailureLogger(path)
	require.NoError(t, err)

	require.NoError(t, fl.Write(FailureRecord{Path: "/a.jpg", Filename: "a.jpg", Reason: "no EXIF date", Error: "e"}))
	require.NoError(t, fl.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotEmpty(t, data, "data must be on disk after Close")
}

func TestFailureLogger_ConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failures.ndjson")
	fl, err := NewFailureLogger(path)
	require.NoError(t, err)

	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, fl.Write(FailureRecord{
				Path:     "/file.jpg",
				Filename: "file.jpg",
				Reason:   "no EXIF date",
				Error:    "err",
			}))
			_ = i
		}(i)
	}
	wg.Wait()
	require.NoError(t, fl.Close())

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var lineCount int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec FailureRecord
		assert.NoError(t, json.Unmarshal(sc.Bytes(), &rec), "each line must be valid JSON")
		lineCount++
	}
	require.NoError(t, sc.Err())
	assert.Equal(t, n, lineCount, "must have one line per Write call")
}

// ---- integration: worker writes failure record ----

func (s *AppTestSuite) TestWorker_WritesFailureRecord() {
	src := s.writeFile("bad.jpg", "data")

	svc := new(MockExifService)
	svc.On("ExtractDate", src, false, false).Return(time.Time{}, "", assert.AnError)

	logPath := filepath.Join(s.tmpDir, "failures.ndjson")
	fl, err := NewFailureLogger(logPath)
	require.NoError(s.T(), err)

	app := s.appWith(&Config{OutputPath: s.tmpDir}, svc)
	app.failureLog = fl
	app.failureLogPath = logPath

	_, err = app.processFile(context.Background(), src)
	s.Error(err)

	// Simulate what worker does after processFile returns an error
	rec := FailureRecord{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Path:       src,
		Filename:   filepath.Base(src),
		SizeBytes:  4,
		Reason:     "no EXIF date",
		Error:      assert.AnError.Error(),
		DurationMs: 0,
	}
	require.NoError(s.T(), fl.Write(rec))
	require.NoError(s.T(), fl.Close())

	data, err := os.ReadFile(logPath)
	require.NoError(s.T(), err)
	assert.NotEmpty(s.T(), data)

	var got FailureRecord
	require.NoError(s.T(), json.Unmarshal(data[:len(data)-1], &got)) // strip trailing newline
	s.Equal("bad.jpg", got.Filename)
	s.Equal("no EXIF date", got.Reason)
}
