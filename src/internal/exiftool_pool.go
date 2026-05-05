package internal

import (
	"fmt"
	"time"
)

// ExifToolPool holds a fixed number of ExifToolService instances in a buffered channel.
// Each call to ExtractDate acquires one instance exclusively, runs the extraction, then
// returns the instance — giving each worker its own exiftool process and eliminating the
// per-service mutex as a shared bottleneck.
type ExifToolPool struct {
	pool chan *ExifToolService
}

// NewExifToolPool creates n independent ExifToolService instances. n should equal the
// number of workers so every worker can extract EXIF data without waiting.
func NewExifToolPool(n int) (*ExifToolPool, error) {
	if n <= 0 {
		n = 1
	}
	ch := make(chan *ExifToolService, n)
	for i := 0; i < n; i++ {
		svc, err := NewExifToolService()
		if err != nil {
			close(ch)
			for s := range ch {
				s.Close()
			}
			return nil, fmt.Errorf("failed to create exiftool instance %d/%d: %w", i+1, n, err)
		}
		ch <- svc
	}
	return &ExifToolPool{pool: ch}, nil
}

// ExtractDate acquires an idle ExifToolService, delegates the call, then returns it to the pool.
func (p *ExifToolPool) ExtractDate(path string, debug bool, useFileModifyDate bool) (time.Time, string, error) {
	svc := <-p.pool
	t, tag, err := svc.ExtractDate(path, debug, useFileModifyDate)
	p.pool <- svc
	return t, tag, err
}

// Close shuts down all instances. Must only be called after all workers have finished.
func (p *ExifToolPool) Close() {
	close(p.pool)
	for svc := range p.pool {
		svc.Close()
	}
}
