package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/sirupsen/logrus"
)

// ExifService is the interface for extracting dates from media files.
// Defined at the consumer site so App can be tested without a real exiftool process.
type ExifService interface {
	ExtractDate(path string, debug bool, useFileModifyDate bool) (time.Time, string, error)
}

// Stats tracks per-run outcomes for the final summary.
type Stats struct {
	success atomic.Int64
	mu      sync.Mutex
	failed  map[string]int64
}

func (s *Stats) addSuccess() { s.success.Add(1) }

func (s *Stats) addFailure(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed == nil {
		s.failed = make(map[string]int64)
	}
	s.failed[reason]++
}

func (s *Stats) print(total, skipped int, interrupted bool) {
	s.mu.Lock()
	reasons := make([]string, 0, len(s.failed))
	counts := make(map[string]int64, len(s.failed))
	for r, n := range s.failed {
		reasons = append(reasons, r)
		counts[r] = n
	}
	s.mu.Unlock()

	sort.Strings(reasons)
	var totalFailed int64
	for _, n := range counts {
		totalFailed += n
	}

	fmt.Printf("\n--- Summary ---\n")
	if interrupted {
		fmt.Printf("  WARNING: interrupted — results below are partial\n")
	}
	fmt.Printf("  Processed : %d\n", total)
	fmt.Printf("  Success   : %d\n", s.success.Load())
	fmt.Printf("  Failed    : %d\n", totalFailed)
	for _, r := range reasons {
		fmt.Printf("    %-32s %d\n", r+":", counts[r])
	}
	if skipped > 0 {
		fmt.Printf("  Skipped   : %d (non-media files)\n", skipped)
	}
}

// App holds the application state, including configuration and services.
type App struct {
	Config             *Config
	ExifService        ExifService
	dirCache           sync.Map       // caches local directories already created; avoids redundant MkdirAll calls
	stats              Stats
	failureLog         *FailureLogger
	failureLogPath     string
	consRemoteFailures atomic.Int64   // consecutive remote-transfer failures; reset to 0 on success
	runCancel          context.CancelFunc // cancels the run-level context; set in Run before workers start
}

// Run starts the file organisation process and returns true if the run was cut short —
// either by a signal or by the circuit breaker.
func (app *App) Run(ctx context.Context) (interrupted bool) {
	startTime := time.Now()

	// Wrap with a child context so the circuit breaker can abort the run
	// independently of the parent signal context.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	app.runCancel = runCancel

	paths, sizes, total, skipped := app.collectFiles()
	logrus.Infof("Estimated total files: %d (skipped non-media: %d)", total, skipped)

	app.printPreRunInfo(ctx, paths, sizes, total, skipped)

	bar := progressbar.NewOptions(total,
		progressbar.OptionSetDescription("Processing"),
		progressbar.OptionSetWidth(20),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionClearOnFinish(),
	)

	jobs := make(chan string, app.Config.Buffer)
	var wg sync.WaitGroup

	for w := 1; w <= app.Config.Workers; w++ {
		wg.Add(1)
		go app.worker(runCtx, w, jobs, &wg, bar)
	}

feederLoop:
	for _, path := range paths {
		select {
		case jobs <- path:
		case <-runCtx.Done():
			interrupted = true
			break feederLoop
		}
	}
	close(jobs)

	wg.Wait()
	bar.Clear()

	elapsed := time.Since(startTime)
	logrus.Infof("Processing finished (interrupted=%v). Total files: %d, Elapsed: %s",
		interrupted, total, elapsed)

	app.stats.print(total, skipped, interrupted)
	if app.failureLogPath != "" {
		fmt.Printf("  Failure log: %s\n", app.failureLogPath)
	}

	return interrupted
}

// worker processes files from the jobs channel.
func (app *App) worker(ctx context.Context, id int, jobs <-chan string, wg *sync.WaitGroup, bar *progressbar.ProgressBar) {
	defer wg.Done()
	for path := range jobs {
		if app.Config.Debug {
			logrus.Debugf("Worker %d handling %s", id, path)
		}
		start := time.Now()
		reason, err := app.processFile(ctx, path)
		elapsed := time.Since(start)
		if err != nil {
			logrus.Errorf("Failed processing %s: %v", path, err)
			app.stats.addFailure(reason)
			if app.failureLog != nil {
				var sizeBytes int64
				if fi, statErr := os.Lstat(path); statErr == nil {
					sizeBytes = fi.Size()
				}
				rec := FailureRecord{
					Timestamp:  time.Now().UTC().Format(time.RFC3339),
					Path:       path,
					Filename:   filepath.Base(path),
					SizeBytes:  sizeBytes,
					Reason:     reason,
					Error:      err.Error(),
					DurationMs: elapsed.Milliseconds(),
				}
				if writeErr := app.failureLog.Write(rec); writeErr != nil {
					logrus.Warnf("failure log write error: %v", writeErr)
				}
			}
		} else {
			app.stats.addSuccess()
		}
		bar.Add(1)
	}
}

// collectFiles walks the input directory and returns media file paths, per-file sizes,
// a media count, and a skipped-non-media count.
func (app *App) collectFiles() (paths []string, sizes []int64, total int, skipped int) {
	filepath.WalkDir(app.Config.InputPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				logrus.Warnf("⚠️ Skipping directory due to permission error: %s", path)
				return fs.SkipDir
			}
			logrus.Warnf("⚠️ Ignoring walk error for %s: %v", path, err)
			return nil
		}

		base := d.Name()
		if d.IsDir() && (base == ".DocumentRevisions-V100" || base == ".Spotlight-V100" || base == ".fseventsd") {
			logrus.Warnf("ℹ️ Skipping system folder: %s", path)
			return fs.SkipDir
		}

		if !d.IsDir() {
			if !isMediaFile(path) {
				logrus.Debugf("Skipping non-media file: %s", path)
				skipped++
				return nil
			}
			var sz int64
			if info, ierr := d.Info(); ierr == nil {
				sz = info.Size()
			}
			paths = append(paths, path)
			sizes = append(sizes, sz)
			total++
		}
		return nil
	})
	return
}
