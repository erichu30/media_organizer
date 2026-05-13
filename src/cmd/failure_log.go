package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// FailureRecord is one NDJSON line written for each file that fails processing.
type FailureRecord struct {
	Timestamp  string `json:"ts"`
	Path       string `json:"path"`
	Filename   string `json:"filename"`
	SizeBytes  int64  `json:"size_bytes"`
	Reason     string `json:"reason"`
	Error      string `json:"error"`
	DurationMs int64  `json:"duration_ms"`
}

// FailureLogger writes FailureRecords as NDJSON (one JSON object per line) to a file.
// It is safe for concurrent use.
type FailureLogger struct {
	mu  sync.Mutex
	f   *os.File
	bw  *bufio.Writer
	enc *json.Encoder
}

// NewFailureLogger opens (or creates) path in truncate mode and returns a ready logger.
func NewFailureLogger(path string) (*FailureLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("open failure log %s: %w", path, err)
	}
	bw := bufio.NewWriterSize(f, 64<<10) // 64 KiB buffer
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	return &FailureLogger{f: f, bw: bw, enc: enc}, nil
}

// Write appends a FailureRecord as a single JSON line. Safe for concurrent callers.
func (fl *FailureLogger) Write(r FailureRecord) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return fl.enc.Encode(r)
}

// Close flushes the buffer and closes the underlying file.
func (fl *FailureLogger) Close() error {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if err := fl.bw.Flush(); err != nil {
		fl.f.Close()
		return err
	}
	return fl.f.Close()
}

// FailureLogPath returns the auto-generated filename for a run started at t.
// Example: failures-20260510-030000.ndjson
func FailureLogPath(t time.Time) string {
	return fmt.Sprintf("failures-%s.ndjson", t.Format("20060102-150405"))
}
