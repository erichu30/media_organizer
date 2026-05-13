// main is the entry point of the media organizer command-line tool.
// It organizes media files from an input directory into an output directory
// based on the creation date extracted from the file's EXIF metadata.
//
// The tool supports:
// - Concurrent processing using a worker pool to speed up operations.
// - Copying or moving files.
// - Dry-run mode to preview changes without modifying files.
// - Fallback to file modification date if EXIF date is not available.
// - Filtering files that only have a "DateTimeOriginal" EXIF tag.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"media_organizer/src/internal"

	"github.com/schollz/progressbar/v3"
	"github.com/sirupsen/logrus"
)

// Config holds the application configuration, populated from command-line flags.
type Config struct {
	InputPath            string
	OutputPath           string
	Workers              int
	Buffer               int
	Debug                bool
	CopyMode             bool
	DryRun               bool
	OnlyDateTimeOriginal bool
	UseFileModifyDate    bool
	IsRemote             bool
	FailureLog           string
	SSHKey               string
	Retries              int // rclone --retries value; 0 lets rclone use its own default
	RemoteFailThreshold  int  // circuit-breaker: abort after this many consecutive remote failures (0 = disabled)
	EstimateTransfer     bool // -estimate: probe destination before run to display a time estimate (remote only; skipped in dry-run)
}

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

// App represents the application state, including configuration and services.
type App struct {
	Config              *Config
	ExifService         ExifService
	dirCache            sync.Map // caches local directories already created; avoids redundant MkdirAll calls
	stats               Stats
	failureLog          *FailureLogger
	failureLogPath      string
	consRemoteFailures  atomic.Int64    // consecutive remote-transfer failures; reset to 0 on success
	runCancel           context.CancelFunc // cancels the run-level context; set in Run before workers start
}

// isSFTPPath returns true for SSH-style user@host:path notation.
func isSFTPPath(p string) bool {
	atIdx := strings.IndexByte(p, '@')
	if atIdx <= 0 {
		return false
	}
	return strings.ContainsRune(p[atIdx:], ':')
}

// toRcloneSFTPPath converts user@host:/path to rclone on-the-fly SFTP syntax.
// e.g. root@192.168.0.83:/mnt/photos → :sftp,host=192.168.0.83,user=root:/mnt/photos
// If keyFile is non-empty, key_file=<path> is appended to the options.
// Non-SFTP paths are returned unchanged.
func toRcloneSFTPPath(p, keyFile string) string {
	atIdx := strings.IndexByte(p, '@')
	if atIdx <= 0 {
		return p
	}
	rest := p[atIdx+1:]
	colonIdx := strings.IndexByte(rest, ':')
	if colonIdx < 0 {
		return p
	}
	user := p[:atIdx]
	host := rest[:colonIdx]
	path := rest[colonIdx+1:]
	opts := fmt.Sprintf("host=%s,user=%s", host, user)
	if keyFile != "" {
		opts += ",key_file=" + keyFile
	}
	return fmt.Sprintf(":sftp,%s:%s", opts, path)
}

// expandTilde replaces a leading ~ with the current user's home directory.
func expandTilde(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p, err
	}
	return filepath.Join(home, p[1:]), nil
}

// isRclonePath returns true when p uses rclone remote syntax (remotename:path)
// or SSH-style user@host:/path (which is converted to rclone SFTP on-the-fly syntax).
func isRclonePath(p string) bool {
	if isSFTPPath(p) {
		return true
	}
	idx := strings.IndexByte(p, ':')
	return idx > 0 && !strings.ContainsRune(p[:idx], '@')
}

// NewConfig creates a new Config object from command-line flags.
func NewConfig() *Config {
	config := &Config{}
	flag.StringVar(&config.InputPath, "i", "", "Input directory")
	flag.StringVar(&config.OutputPath, "o", "", "Output directory")
	flag.IntVar(&config.Workers, "workers", 8, "Number of concurrent workers")
	flag.IntVar(&config.Buffer, "buffer", 100, "Channel buffer size")
	flag.BoolVar(&config.Debug, "debug", false, "Enable debug logging")
	flag.BoolVar(&config.CopyMode, "copy", false, "Copy instead of move (keep original files)")
	flag.BoolVar(&config.DryRun, "dry-run", false, "Show what would be done, without moving/copying files")
	flag.BoolVar(&config.OnlyDateTimeOriginal, "only-datetimeoriginal", false, "Only process files with DateTimeOriginal tag")
	flag.BoolVar(&config.UseFileModifyDate, "use-file-modify-date", false, "Use file modify date as a fallback")
	flag.StringVar(&config.FailureLog, "failure-log", "", `Write failed-file records as NDJSON to this path ("auto" = timestamp-based filename)`)
	flag.StringVar(&config.SSHKey, "ssh-key", "", "Path to SSH private key for user@host:/path destinations (e.g. ~/.ssh/id_ed25519)")
	flag.IntVar(&config.Retries, "retries", 3, "rclone retry count for transient remote errors (SSH disconnect, timeouts)")
	flag.IntVar(&config.RemoteFailThreshold, "remote-fail-threshold", 5, "abort after this many consecutive remote failures, 0 to disable")
	flag.BoolVar(&config.EstimateTransfer, "estimate", false, "Sample two files to measure destination speed and show a time estimate (remote only; skipped in dry-run)")
	// Use custom usage/help function
	flag.Usage = showHelp

	// If user passed --help or -h explicitly, print help and exit early.
	for _, a := range os.Args[1:] {
		if a == "-h" || a == "--help" {
			showHelp()
			os.Exit(0)
		}
	}

	flag.Parse()

	if config.SSHKey != "" {
		if expanded, err := expandTilde(config.SSHKey); err == nil {
			config.SSHKey = expanded
		}
	}
	config.IsRemote = isRclonePath(config.OutputPath)
	if isSFTPPath(config.OutputPath) {
		config.OutputPath = toRcloneSFTPPath(config.OutputPath, config.SSHKey)
	}

	return config
}

// setupLogging configures the logging settings for the application.
func setupLogging(debug bool) {
	logFile, err := os.OpenFile("sortbydate.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		logrus.Fatalf("Failed to open log file: %v", err)
	}
	logrus.SetOutput(logFile)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		ForceQuote:      false,
		PadLevelText:    true,
	})
	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}
}

// main is a thin wrapper so that deferred cleanup in run() executes before the
// process exits. os.Exit skips defers; keeping all logic in run() avoids leaks.
func main() {
	os.Exit(run())
}

// run contains the real entry-point logic and returns an OS exit code:
// 0 on success, 1 on error or when the run is interrupted by a signal.
// All deferred cleanup (pool, failure log) runs before the exit code is returned.
func run() int {
	// Cancel the context when SIGINT, SIGTERM, or SIGHUP arrives.
	// After the first signal the handler is unregistered, so a second signal
	// (e.g. two Ctrl-C presses) terminates the process immediately — the
	// expected "force-quit" behaviour.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt,   // SIGINT  (Ctrl-C)
		syscall.SIGTERM, // graceful shutdown from init/systemd/docker stop
		syscall.SIGHUP,  // terminal closed / daemon reload
	)
	defer stop() // release signal resources; also re-enables default signal behaviour

	config := NewConfig()
	if config.InputPath == "" || config.OutputPath == "" {
		logrus.Fatal("Input (-i) and output (-o) directories are required")
	}

	setupLogging(config.Debug)

	if err := validatePaths(config); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		logrus.Fatal(err)
	}

	pool, err := internal.NewExifToolPool(config.Workers)
	if err != nil {
		logrus.Fatalf("Failed to initialize ExifToolPool: %v", err)
	}
	defer pool.Close() // always reached; shuts down exiftool subprocess pool

	app := &App{
		Config:      config,
		ExifService: pool,
	}

	if config.FailureLog != "" {
		logPath := config.FailureLog
		if logPath == "auto" {
			logPath = FailureLogPath(time.Now())
		}
		fl, err := NewFailureLogger(logPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Warning: could not open failure log:", err)
		} else {
			app.failureLog = fl
			app.failureLogPath = logPath
			defer func() {
				// Flush the 64 KiB buffer and sync to disk even on interrupt,
				// so partial failure records are not lost.
				if err := fl.Close(); err != nil {
					logrus.Warnf("closing failure log: %v", err)
				}
			}()
		}
	}

	if app.Run(ctx) {
		return 1
	}
	return 0
}

// validatePaths ensures the input and output directories are correctly set up.
// It handles both local (Mac/Linux) paths and remote (rsync via SSH) connections.
func validatePaths(config *Config) error {
	// Validate input path exists and is a directory
	info, err := os.Stat(config.InputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("input path does not exist: %s", config.InputPath)
		}
		return fmt.Errorf("failed to access input path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("input path must be a directory: %s", config.InputPath)
	}

	if config.IsRemote {
		if _, err := exec.LookPath("rclone"); err != nil {
			return fmt.Errorf("rclone command not found, required for remote output sync")
		}
	} else {
		// For local Mac/Linux paths, ensure output directory exists or can be created with safe default permissions (0755)
		if err := os.MkdirAll(config.OutputPath, 0755); err != nil {
			return fmt.Errorf("failed to create or access output directory: %w", err)
		}
	}
	return nil
}

// showHelp prints a concise usage message and examples.
func showHelp() {
	fmt.Fprintf(os.Stderr, `Usage: %s [OPTIONS]

Organize media files by date (YYYY/MM) using EXIF data, with optional remote transfer via rclone.

Required:
	-i <dir>        Input directory
	-o <dir|dest>   Output: local directory, rclone remote (remotename:path),
	                or SSH destination (user@host:/path — auto-converted to rclone SFTP)

Options:
`, os.Args[0])
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Examples:
	%s -i /path/to/input -o /path/to/output
	%s -i /path/to/input -o myremote:/photos -copy
	%s -i /path/to/input -o root@192.168.1.10:/mnt/nas/photos -ssh-key ~/.ssh/id_ed25519
	%s -i /path/to/input -o root@192.168.1.10:/mnt/nas/photos -ssh-key ~/.ssh/id_ed25519 -estimate
	%s -i /path/to/input -o /path/to/output -dry-run
	%s -i /path/to/input -o /path/to/output -failure-log auto
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

// Run starts the file organization process and returns true if the run was
// cut short — either by a signal or by the circuit breaker.
// Workers always finish their current file before exiting — no partial writes.
func (app *App) Run(ctx context.Context) (interrupted bool) {
	startTime := time.Now()

	// Wrap with a child context so the circuit breaker can abort the run
	// independently of the parent signal context. Both signal and circuit
	// breaker cancel runCtx, which stops the feeder and sets interrupted=true.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	app.runCancel = runCancel // stored so transferRemote can call it

	// Step 1: Walk the input directory to count files and collect paths.
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

	// Step 2: Set up a worker pool to process files concurrently.
	jobs := make(chan string, app.Config.Buffer)
	var wg sync.WaitGroup

	for w := 1; w <= app.Config.Workers; w++ {
		wg.Add(1)
		go app.worker(runCtx, w, jobs, &wg, bar)
	}

	// Step 3: Feed file paths into the jobs channel.
	// On signal or circuit-breaker abort, stop feeding; workers drain the
	// channel, finish their current file, then exit when the channel is closed.
	// Closing jobs here (not inside the select) guarantees exactly one close.
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

	// Step 4: Wait for all in-flight workers to finish their current file.
	// This is always reached — signal only stops the feeder, not the workers.
	wg.Wait()

	// Clear the progress bar before printing; it may be mid-render if interrupted.
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

// worker is a routine that processes files from the jobs channel.
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

// processFile handles the logic for a single file: extracting the date, determining the destination, and moving/copying.
// Returns a short failure-reason string (empty on success) alongside any error.
// ctx is used to cancel in-progress rclone subprocesses on signal; local operations
// complete their current step (copy or rename) before honouring cancellation.
func (app *App) processFile(ctx context.Context, path string) (string, error) {
	t, reason, err := app.extractDate(path)
	if err != nil {
		logrus.Warnf("Cannot extract date for %s: %v", path, err)
		return reason, err
	}

	year := fmt.Sprintf("%04d", t.Year())
	month := fmt.Sprintf("%02d", int(t.Month()))
	var targetDir string
	if app.Config.IsRemote {
		// rclone creates destination directories automatically; no mkdir step needed
		targetDir = app.Config.OutputPath + "/" + year + "/" + month
	} else {
		targetDir = filepath.Join(app.Config.OutputPath, year, month)
		if _, loaded := app.dirCache.Load(targetDir); !loaded {
			if err := os.MkdirAll(targetDir, os.ModePerm); err != nil {
				return "directory creation failed", fmt.Errorf("failed to create dir %s: %w", targetDir, err)
			}
			app.dirCache.Store(targetDir, struct{}{})
		}
	}

	var targetPath string
	if app.Config.IsRemote {
		targetPath = app.Config.OutputPath + "/" + year + "/" + month + "/" + filepath.Base(path)
	} else {
		targetPath = filepath.Join(targetDir, filepath.Base(path))
	}

	if app.Config.DryRun {
		logrus.Infof("[DRY-RUN] Move: %s → %s (copy=%v)", path, targetPath, app.Config.CopyMode)
		return "", nil
	}

	logrus.Infof("Move: %s → %s (copy=%v)", path, targetPath, app.Config.CopyMode)

	if app.Config.Debug {
		logrus.Debugf("%s → %s (copy=%v)", path, targetPath, app.Config.CopyMode)
	}

	if app.Config.IsRemote {
		return app.transferRemote(ctx, path, targetPath)
	}
	return app.transferLocal(ctx, path, targetPath)
}

// transferRemote runs rclone moveto/copyto with retry and circuit-breaker support.
//
// On any failure (signal, SSH disconnect, timeout, …):
//  1. A best-effort rclone deletefile removes any partial remote file so re-runs
//     start clean. The local source is always intact — rclone never deletes it
//     until the transfer fully succeeds.
//  2. The consecutive-failure counter is incremented; if it reaches the configured
//     threshold the circuit breaker fires, cancelling the run context so the feeder
//     stops dispatching new work.
//
// On success the counter is reset to zero.
func (app *App) transferRemote(ctx context.Context, src, dst string) (string, error) {
	subCmd := "moveto"
	if app.Config.CopyMode {
		subCmd = "copyto"
	}

	args := []string{subCmd, src, dst}
	if app.Config.Retries > 0 {
		args = append(args, "--retries", strconv.Itoa(app.Config.Retries))
	}
	rcloneCmd := exec.CommandContext(ctx, "rclone", args...)
	if app.Config.Debug {
		logrus.Debugf("Executing: %s", rcloneCmd.String())
	}

	output, err := rcloneCmd.CombinedOutput()
	if err == nil {
		app.resetRemoteFailures()
		return "", nil
	}

	// On any rclone failure, attempt to remove the partial remote file.
	// Uses a fresh context so cleanup can run even when ctx is already cancelled
	// (signal or circuit breaker). Best-effort: if SSH is down this also fails;
	// the warning gives the user enough information to clean up manually.
	logrus.Warnf("rclone %s failed for %s; attempting remote cleanup of %s", subCmd, src, dst)
	app.cleanupRemoteFile(dst)

	if ctx.Err() != nil {
		app.recordRemoteFailure()
		return "file operation failed", fmt.Errorf("rclone %s interrupted: %w", subCmd, ctx.Err())
	}

	app.recordRemoteFailure()
	return "file operation failed", fmt.Errorf("rclone %s %s: %w, output: %s", subCmd, src, err, string(output))
}

// cleanupRemoteFile attempts rclone deletefile on dst using a fresh 30-second context.
// Errors are logged as warnings — this is always best-effort.
func (app *App) cleanupRemoteFile(dst string) {
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanCancel()
	// Pass --retries 1 so cleanup fails fast rather than spending time retrying
	// (if SSH is down, retrying won't help).
	if cleanErr := exec.CommandContext(cleanCtx, "rclone", "deletefile", "--retries", "1", dst).Run(); cleanErr != nil {
		logrus.Warnf("remote cleanup of %s failed (manual removal may be needed): %v", dst, cleanErr)
	} else {
		logrus.Infof("remote cleanup succeeded: removed partial %s", dst)
	}
}

// recordRemoteFailure increments the consecutive failure counter and fires the
// circuit breaker when the threshold is reached. Safe for concurrent callers.
// Returns true if the circuit breaker fired on this call.
func (app *App) recordRemoteFailure() bool {
	n := app.consRemoteFailures.Add(1)
	threshold := int64(app.Config.RemoteFailThreshold)
	if threshold <= 0 || n < threshold {
		return false
	}
	// Fire exactly once — context cancellation is idempotent so repeated calls
	// from other goroutines are harmless.
	msg := fmt.Sprintf("circuit breaker: %d consecutive remote failures — aborting remaining transfers", n)
	logrus.Error(msg)
	fmt.Fprintf(os.Stderr, "\n%s\n", msg)
	if app.runCancel != nil {
		app.runCancel()
	}
	return true
}

// resetRemoteFailures resets the consecutive failure counter after a successful transfer.
func (app *App) resetRemoteFailures() {
	app.consRemoteFailures.Store(0)
}

// printPreRunInfo prints file count and total size before the transfer starts.
// For remote destinations, if -estimate is set (and not dry-run), it also runs a
// two-file probe to measure per-file overhead and bandwidth, then shows a time estimate.
func (app *App) printPreRunInfo(ctx context.Context, paths []string, sizes []int64, total, skipped int) {
	var totalBytes int64
	for _, s := range sizes {
		totalBytes += s
	}
	fmt.Printf("--- Pre-transfer ---\n")
	fmt.Printf("  Files      : %d\n", total)
	fmt.Printf("  Total size : %s\n", formatBytes(totalBytes))
	if skipped > 0 {
		fmt.Printf("  Skipped    : %d non-media files\n", skipped)
	}
	if app.Config.IsRemote && app.Config.EstimateTransfer && !app.Config.DryRun && len(paths) > 0 {
		app.probeAndEstimate(ctx, paths, sizes, totalBytes)
	} else if app.Config.IsRemote && !app.Config.EstimateTransfer {
		fmt.Printf("  (add -estimate to measure destination speed)\n")
	}
	fmt.Println()
}

// probeAndEstimate transfers two sample files (10th and 90th percentile by size) to the
// destination under temporary names, measures elapsed time, fits a linear model
// time = alpha + beta*bytes, and prints the estimated total transfer time.
// Probe files are deleted immediately after measurement; the main run is unaffected.
func (app *App) probeAndEstimate(ctx context.Context, paths []string, sizes []int64, totalBytes int64) {
	n := len(paths)

	// Sort indices by file size to pick representative samples.
	idxs := make([]int, n)
	for i := range idxs {
		idxs[i] = i
	}
	sort.Slice(idxs, func(a, b int) bool {
		return sizes[idxs[a]] < sizes[idxs[b]]
	})

	lo := idxs[n/10]
	hi := idxs[n*9/10]
	if lo == hi && n > 1 {
		hi = idxs[n-1]
	}

	tmpBase := app.Config.OutputPath + "/.media-organizer-probe-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	fmt.Printf("  Probing    : transferring sample file(s)...")

	t1, err := app.probeFile(ctx, paths[lo], tmpBase+"-1")
	if err != nil {
		fmt.Printf(" failed (%v)\n", err)
		return
	}
	s1 := sizes[lo]

	var alpha, beta float64
	probe2Desc := ""

	if lo != hi {
		t2, err2 := app.probeFile(ctx, paths[hi], tmpBase+"-2")
		s2 := sizes[hi]
		if err2 != nil {
			// One probe failed — fall back to single-point (no per-file overhead term).
			beta = t1.Seconds() / float64(max(s1, 1))
		} else {
			alpha, beta = fitModel(s1, s2, t1.Seconds(), t2.Seconds())
			probe2Desc = fmt.Sprintf(" + %s", formatBytes(s2))
		}
	} else {
		beta = t1.Seconds() / float64(max(s1, 1))
	}

	fmt.Printf(" done\n")
	fmt.Printf("  Probe      : %s%s sampled\n", formatBytes(s1), probe2Desc)
	if alpha > 0.01 {
		fmt.Printf("  Per-file   : ~%d ms overhead\n", int64(alpha*1000))
	}
	if beta > 0 {
		fmt.Printf("  Bandwidth  : ~%s/s\n", formatBytes(int64(1.0/beta)))
	}
	estSec := alpha*float64(n) + beta*float64(totalBytes)
	if estSec > 0 {
		fmt.Printf("  Estimated  : ~%s\n", formatDuration(time.Duration(float64(time.Second)*estSec)))
	}
}

// probeFile copies src to tmpDst using rclone copyto (--retries 1 for fast failure),
// returns the elapsed time, and deletes tmpDst. Used only for pre-transfer estimation.
func (app *App) probeFile(ctx context.Context, src, tmpDst string) (time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(probeCtx, "rclone", "copyto", src, tmpDst, "--retries", "1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	elapsed := time.Since(start)

	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanCancel()
	if cleanErr := exec.CommandContext(cleanCtx, "rclone", "deletefile", "--retries", "1", tmpDst).Run(); cleanErr != nil {
		logrus.Warnf("probe cleanup of %s failed: %v", tmpDst, cleanErr)
	}

	return elapsed, nil
}

// fitModel fits a linear model time = alpha + beta*bytes to two (size, time) data points.
// alpha is per-file fixed overhead in seconds; beta is seconds per byte (inverse bandwidth).
// Both are clamped to 0 if the data is noisy enough to produce negative values.
func fitModel(s1, s2 int64, t1, t2 float64) (alpha, beta float64) {
	ds := float64(s2 - s1)
	if ds <= 0 {
		// Same size — average the two measurements; no overhead term.
		beta = (t1 + t2) / 2.0 / float64(max(s1, 1))
		return 0, beta
	}
	beta = (t2 - t1) / ds
	if beta < 0 {
		beta = 0
	}
	alpha = t1 - beta*float64(s1)
	if alpha < 0 {
		alpha = 0
	}
	return alpha, beta
}

// formatBytes returns a human-readable representation of a byte count (e.g. "4.2 GB").
func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// formatDuration returns a human-readable duration rounded to the nearest second.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "< 1 sec"
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d h %d min %d sec", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%d min %d sec", m, s)
	}
	return fmt.Sprintf("%d sec", s)
}

// transferLocal moves or copies a file within the local filesystem.
// For cross-device moves (EXDEV), it falls back to copy+delete. If a signal arrives
// after the copy succeeds but before the source is deleted, the delete is skipped so
// the source is preserved — the copy at the destination is the authoritative copy.
func (app *App) transferLocal(ctx context.Context, src, dst string) (string, error) {
	if app.Config.CopyMode {
		if err := copyFile(src, dst); err != nil {
			return "file operation failed", err
		}
		return "", nil
	}

	if err := osRename(src, dst); err != nil {
		var linkErr *os.LinkError
		if !errors.As(err, &linkErr) || !errors.Is(linkErr.Err, syscall.EXDEV) {
			return "file operation failed", err
		}
		// Source and destination are on different filesystems (e.g. local → SMB/NFS mount).
		// os.Rename only works within a single device; fall back to copy + delete.
		if copyErr := copyFile(src, dst); copyErr != nil {
			return "file operation failed", copyErr
		}
		// If a signal arrived after the copy completed, skip removing the source.
		// The destination holds a complete, timestamp-preserved copy; the original
		// is left in place as a safety net for the user to inspect.
		if ctx.Err() != nil {
			logrus.Warnf("interrupted after copy; source preserved: %s (copy at: %s)", src, dst)
			return "", nil
		}
		if removeErr := os.Remove(src); removeErr != nil {
			return "file operation failed", removeErr
		}
	}

	return "", nil
}

// extractDate extracts the date from a file's metadata.
// Returns a short failure-reason string (empty on success) alongside any error.
func (app *App) extractDate(path string) (time.Time, string, error) {
	t, tag, err := app.ExifService.ExtractDate(path, app.Config.Debug, app.Config.UseFileModifyDate)
	if err != nil {
		logrus.Errorf("Failed to extract date for %s: %v", path, err)
		return time.Time{}, "no EXIF date", err
	}

	hasDateTimeOriginal := tag == "DateTimeOriginal"
	if app.Config.OnlyDateTimeOriginal && !hasDateTimeOriginal {
		logrus.Infof("Skipping %s because it does not have DateTimeOriginal tag", path)
		return time.Time{}, "DateTimeOriginal not found", fmt.Errorf("DateTimeOriginal not found")
	}

	if t.IsZero() {
		logrus.Warnf("No valid date found for %s", path)
		return time.Time{}, "no EXIF date", fmt.Errorf("no valid date found in EXIF or file system")
	}
	return t, "", nil
}

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
// 1 MiB is chosen to amortise syscall overhead for large video files while staying CPU-cache friendly.
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

	// copyErr or syncErr mean the data on disk is incomplete or unsafe.
	// Remove dst so a re-run can start fresh rather than finding a corrupt file.
	if copyErr != nil || syncErr != nil {
		os.Remove(dst) // best-effort; ignore remove error — we already have a more important one to return
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
