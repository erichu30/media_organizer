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
	success         atomic.Int64
	renamed         atomic.Int64
	alreadyPresent  atomic.Int64
	skippedConflict atomic.Int64
	cancelled       atomic.Int64
	mu              sync.Mutex
	failed          map[string]int64
}

func (s *Stats) addSuccess()         { s.success.Add(1) }
func (s *Stats) addRenamed()         { s.renamed.Add(1) }
func (s *Stats) addAlreadyPresent()  { s.alreadyPresent.Add(1) }
func (s *Stats) addSkippedConflict() { s.skippedConflict.Add(1) }
func (s *Stats) addCancelled()       { s.cancelled.Add(1) }

func (s *Stats) addFailure(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed == nil {
		s.failed = make(map[string]int64)
	}
	s.failed[reason]++
}

// totalFailed returns the number of files that failed for any reason.
func (s *Stats) totalFailed() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, c := range s.failed {
		n += c
	}
	return n
}

// failureCount returns how many files failed for one specific reason.
func (s *Stats) failureCount(reason string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed[reason]
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
	success := s.success.Load()
	alreadyPresent := s.alreadyPresent.Load()
	skippedConflict := s.skippedConflict.Load()

	fmt.Printf("  Media files : %d\n", total)
	fmt.Printf("  Success     : %d\n", success)
	fmt.Printf("  Failed      : %d\n", totalFailed)
	for _, r := range reasons {
		fmt.Printf("    %-30s %d\n", r+":", counts[r])
	}
	if n := s.renamed.Load(); n > 0 {
		fmt.Printf("  Renamed     : %d (name already taken at destination)\n", n)
	}
	if alreadyPresent > 0 {
		fmt.Printf("  Already there : %d (same-sized file at destination; source left in place)\n", alreadyPresent)
	}
	if skippedConflict > 0 {
		fmt.Printf("  Conflicts skipped : %d (-on-conflict=skip)\n", skippedConflict)
	}

	// An interrupted run stops the feeder mid-queue, so most files are never
	// dispatched at all and appear in none of the counters above. Reporting the
	// remainder explicitly keeps the summary from implying they were handled.
	if remainder := int64(total) - (success + totalFailed + alreadyPresent + skippedConflict); remainder > 0 {
		fmt.Printf("  Not attempted : %d (run ended before these files were reached)\n", remainder)
	}
	if skipped > 0 {
		fmt.Printf("  Skipped     : %d (non-media files)\n", skipped)
	}
}

// App holds the application state, including configuration and services.
type App struct {
	Config             *Config
	ExifService        ExifService
	dirCache           sync.Map // caches local directories already created; avoids redundant MkdirAll calls
	claimed            sync.Map // destination paths reserved by a worker this run; keeps two workers off one name
	destDirs           sync.Map // remote destination dir → *destIndex, so each dir is listed at most once
	stats              Stats
	failureLog         *FailureLogger
	failureLogPath     string
	consRemoteFailures atomic.Int64       // consecutive remote-transfer failures; reset to 0 on success
	breakerFired       atomic.Bool        // true once the circuit breaker has announced itself
	remoteErrShown     atomic.Bool        // true once a full rclone error has been shown on stderr
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
	bar.RenderBlank()

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
	app.printHints()

	return interrupted
}

// printHints turns the run's outcome into the next thing worth trying. A count of
// failed files says what happened; it does not say that the flag which would have
// rescued most of them is one word away.
func (app *App) printHints() {
	var hints []string

	if n := app.stats.failureCount("no EXIF date"); n > 0 && !app.Config.UseFileModifyDate {
		hints = append(hints, fmt.Sprintf(
			"%d file(s) had no EXIF date. Screenshots and downloaded images usually have none —\n"+
				"    re-run with -use-file-modify-date to fall back to the file's modification time.", n))
	}
	if app.stats.renamed.Load() > 0 {
		hints = append(hints, "Renamed files kept a \"_1\"-style suffix. Use -on-conflict skip to leave\n"+
			"    duplicates in the input instead, or -on-conflict fail to stop on them.")
	}
	if app.stats.totalFailed() > 0 {
		hints = append(hints, fmt.Sprintf(
			"Per-file errors are in %s. Add -failure-log auto to get them as NDJSON.", app.Config.LogPath))
	}

	if len(hints) == 0 {
		return
	}
	fmt.Printf("\n--- Hints ---\n")
	for _, h := range hints {
		fmt.Printf("  • %s\n", h)
	}
}

// worker processes files from the jobs channel.
func (app *App) worker(ctx context.Context, id int, jobs <-chan string, wg *sync.WaitGroup, bar *progressbar.ProgressBar) {
	defer wg.Done()
	for path := range jobs {
		// The feeder stops queueing on cancellation, but whatever is already in the
		// channel would otherwise still be attempted. Every one of those transfers
		// fails instantly against a cancelled context and then pays for a remote
		// cleanup, so a Ctrl-C would take minutes and report a queue's worth of
		// failures for files that were never touched.
		if ctx.Err() != nil {
			app.stats.addCancelled()
			bar.Add(1)
			continue
		}
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
		} else if reason != reasonSkipped {
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
		if d.IsDir() && isExcludedDir(base) {
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
