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
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

func (s *Stats) print(total, skipped int) {
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
	Config         *Config
	ExifService    ExifService
	dirCache       sync.Map // caches local directories already created; avoids redundant MkdirAll calls
	stats          Stats
	failureLog     *FailureLogger
	failureLogPath string
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

func main() {
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
	defer pool.Close()

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
				if err := fl.Close(); err != nil {
					logrus.Warnf("closing failure log: %v", err)
				}
			}()
		}
	}

	app.Run()
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
	%s -i /path/to/input -o /path/to/output -dry-run
	%s -i /path/to/input -o /path/to/output -failure-log auto
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])
}

// Run starts the file organization process.
func (app *App) Run() {
	startTime := time.Now()

	// Step 1: Walk the input directory to count files and collect paths.
	paths, total, skipped := app.collectFiles()
	logrus.Infof("Estimated total files: %d (skipped non-media: %d)", total, skipped)

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
		go app.worker(w, jobs, &wg, bar)
	}

	// Step 3: Push file paths to the jobs channel.
	for _, path := range paths {
		jobs <- path
	}
	close(jobs)

	// Step 4: Wait for all workers to finish.
	wg.Wait()

	elapsed := time.Since(startTime)
	logrus.Infof("Processing finished. Total files: %d, Elapsed time: %s", total, elapsed)

	app.stats.print(total, skipped)
	if app.failureLogPath != "" {
		fmt.Printf("  Failure log: %s\n", app.failureLogPath)
	}
}

// collectFiles walks the input directory and returns media file paths, a media count, and a skipped-non-media count.
func (app *App) collectFiles() ([]string, int, int) {
	var paths []string
	var count, skipped int
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
			paths = append(paths, path)
			count++
		}
		return nil
	})
	return paths, count, skipped
}

// worker is a routine that processes files from the jobs channel.
func (app *App) worker(id int, jobs <-chan string, wg *sync.WaitGroup, bar *progressbar.ProgressBar) {
	defer wg.Done()
	for path := range jobs {
		if app.Config.Debug {
			logrus.Debugf("Worker %d handling %s", id, path)
		}
		start := time.Now()
		reason, err := app.processFile(path)
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
func (app *App) processFile(path string) (string, error) {
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
		subCmd := "moveto"
		if app.Config.CopyMode {
			subCmd = "copyto"
		}
		rcloneCmd := exec.Command("rclone", subCmd, path, targetPath)
		if app.Config.Debug {
			logrus.Debugf("Executing: %s", rcloneCmd.String())
		}
		if output, err := rcloneCmd.CombinedOutput(); err != nil {
			return "file operation failed", fmt.Errorf("rclone %s %s: %w, output: %s", subCmd, path, err, string(output))
		}
	} else {
		if app.Config.CopyMode {
			if err := copyFile(path, targetPath); err != nil {
				return "file operation failed", err
			}
			return "", nil
		}
		if err := os.Rename(path, targetPath); err != nil {
			var linkErr *os.LinkError
			if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
				// Source and destination are on different filesystems (e.g. local → SMB/NFS mount).
				// os.Rename only works within a single device; fall back to copy + delete.
				if copyErr := copyFile(path, targetPath); copyErr != nil {
					return "file operation failed", copyErr
				}
				if removeErr := os.Remove(path); removeErr != nil {
					return "file operation failed", removeErr
				}
			} else {
				return "file operation failed", err
			}
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
