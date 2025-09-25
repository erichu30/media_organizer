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
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/sirupsen/logrus"
	"media_organizer/src/internal"
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
}

// App represents the application state, including configuration and services.
type App struct {
	Config      *Config
	ExifService *internal.ExifToolService
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

	config.IsRemote = strings.Contains(config.OutputPath, "@") && strings.Contains(config.OutputPath, ":")

	return config
}

// setupLogging configures the logging settings for the application.
func setupLogging(debug bool) {
	logFile, err := os.OpenFile("sortbydate.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		logrus.Fatalf("Failed to open log file: %v", err)
	}
	logrus.SetOutput(logFile)
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

	exifService, err := internal.NewExifToolService()
	if err != nil {
		logrus.Fatalf("Failed to initialize ExifToolService: %v", err)
	}
	defer exifService.Close()

	app := &App{
		Config:      config,
		ExifService: exifService,
	}

	app.Run()
}

// showHelp prints a concise usage message and examples.
func showHelp() {
		fmt.Fprintf(os.Stderr, `Usage: %s [OPTIONS]

Organize media files by date (YYYY/MM) using EXIF data, with optional remote rsync transfer.

Required:
	-i <dir>        Input directory
	-o <dir|dest>   Output: local directory or
							remote destination formatted user@host:/remote/path with rsync module

Options:
`, os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
	%s -i /path/to/input -o /path/to/output
	%s -i /path/to/input -o user@host:/remote/path --copy
	%s -i /path/to/input -o /path/to/output --dry-run
`, os.Args[0], os.Args[0], os.Args[0])
}

// Run starts the file organization process.
func (app *App) Run() {
	startTime := time.Now()

	// Step 1: Walk the input directory to count files and collect paths.
	paths, total := app.collectFiles()
	logrus.Infof("Estimated total files: %d", total)

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
}

// collectFiles walks the input directory, counts the files, and returns a slice of file paths.
func (app *App) collectFiles() ([]string, int) {
	var paths []string
	var count int
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
			paths = append(paths, path)
			count++
		}
		return nil
	})
	return paths, count
}

// worker is a routine that processes files from the jobs channel.
func (app *App) worker(id int, jobs <-chan string, wg *sync.WaitGroup, bar *progressbar.ProgressBar) {
	defer wg.Done()
	for path := range jobs {
		if app.Config.Debug {
			logrus.Debugf("Worker %d handling %s", id, path)
		}
		if err := app.processFile(path); err != nil {
			logrus.Errorf("Failed processing %s: %v", path, err)
		}
		bar.Add(1)
	}
}

// processFile handles the logic for a single file: extracting the date, determining the destination, and moving/copying.
func (app *App) processFile(path string) error {
	t, err := app.extractDate(path)
	if err != nil {
		logrus.Warnf("Cannot extract date for %s: %v", path, err)
		return err
	}

	year := fmt.Sprintf("%04d", t.Year())
	month := fmt.Sprintf("%02d", int(t.Month()))
	var targetDir string
	if app.Config.IsRemote {
		remoteParts := strings.Split(app.Config.OutputPath, ":")
		remoteHost := remoteParts[0]
		remoteBaseDir := remoteParts[1]
		targetDir = filepath.Join(remoteBaseDir, year, month)
		sshCmd := exec.Command("ssh", remoteHost, "mkdir", "-p", targetDir)
		if app.Config.Debug {
			logrus.Debugf("Executing: %s", sshCmd.String())
		}
		if err := sshCmd.Run(); err != nil {
			return fmt.Errorf("failed to create remote dir %s: %w", targetDir, err)
		}
	} else {
		targetDir = filepath.Join(app.Config.OutputPath, year, month)
		if err := os.MkdirAll(targetDir, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create dir %s: %w", targetDir, err)
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
		return nil
	}

	logrus.Infof("Move: %s → %s (copy=%v)", path, targetPath, app.Config.CopyMode)

	if app.Config.Debug {
		logrus.Debugf("%s → %s (copy=%v)", path, targetPath, app.Config.CopyMode)
	}

	if app.Config.IsRemote {
		args := []string{"-aHAXv"}
		if !app.Config.CopyMode {
			args = append(args, "--remove-source-files")
		}
		args = append(args, path, targetPath)
		rsyncCmd := exec.Command("rsync", args...)
		if app.Config.Debug {
			logrus.Debugf("Executing: %s", rsyncCmd.String())
		}
		if output, err := rsyncCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to rsync %s: %w, output: %s", path, err, string(output))
		}
	} else {
		if app.Config.CopyMode {
			return copyFile(path, targetPath)
		}
		return os.Rename(path, targetPath)
	}

	return nil
}

// extractDate extracts the date from a file's metadata.
func (app *App) extractDate(path string) (time.Time, error) {
	t, tag, err := app.ExifService.ExtractDate(path, app.Config.Debug, app.Config.UseFileModifyDate)
	if err != nil {
		logrus.Errorf("Failed to extract date for %s: %v", path, err)
		return time.Time{}, err
	}

	hasDateTimeOriginal := tag == "DateTimeOriginal"
	if app.Config.OnlyDateTimeOriginal && !hasDateTimeOriginal {
		logrus.Infof("Skipping %s because it does not have DateTimeOriginal tag", path)
		return time.Time{}, fmt.Errorf("DateTimeOriginal not found")
	}

	if t.IsZero() {
		logrus.Warnf("No valid date found for %s", path)
		return time.Time{}, fmt.Errorf("no valid date found in EXIF or file system")
	}
	return t, nil
}

// copyFile copies a file from a source to a destination.
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
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}





