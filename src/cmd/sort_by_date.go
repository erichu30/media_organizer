// Package main is the entry point for the media organizer CLI.
// It organizes photo and video files into a YYYY/MM directory structure
// by reading EXIF metadata, with optional remote transfer via rclone.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"media_organizer/src/internal"

	"github.com/sirupsen/logrus"
)

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
	// (e.g. two Ctrl-C presses) terminates the process immediately.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt,    // SIGINT  (Ctrl-C)
		syscall.SIGTERM, // graceful shutdown from init/systemd/docker stop
		syscall.SIGHUP,  // terminal closed / daemon reload
	)
	defer stop()

	config := NewConfig()
	if config.InputPath == "" || config.OutputPath == "" {
		fmt.Fprintf(os.Stderr, "Error: both -i (input) and -o (output) are required\n\n")
		showHelp()
		return 2
	}
	if err := validateConfig(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n\nRun %s -h for usage.\n", err, os.Args[0])
		return 2
	}

	if err := setupLogging(config); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}

	// Past this point logrus writes to the log file, so every user-facing failure
	// has to be printed to stderr as well or the terminal shows nothing at all.
	if err := validatePaths(config); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		logrus.Error(err)
		return 1
	}

	pool, err := internal.NewExifToolPool(config.Workers)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: failed to start exiftool:", err)
		logrus.Errorf("Failed to initialize ExifToolPool: %v", err)
		return 1
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

	interrupted := app.Run(ctx)

	// Anything short of "every file arrived" is a non-zero exit. A cron job or shell
	// script has no other way to notice that a run moved nothing at all.
	if interrupted || app.stats.totalFailed() > 0 {
		return 1
	}
	return 0
}

// maxLogBytes is the size at which the log file is rotated. Without a cap the log
// grows for the life of the install — it is append-only and one line per file.
const maxLogBytes = 50 << 20

// setupLogging points logrus at the configured log file, rotating it first if it
// has grown past maxLogBytes.
func setupLogging(config *Config) error {
	rotateLogIfLarge(config.LogPath)

	logFile, err := os.OpenFile(config.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", config.LogPath, err)
	}
	logrus.SetOutput(logFile)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		ForceQuote:      false,
		PadLevelText:    true,
	})
	if config.Debug {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}
	return nil
}

// rotateLogIfLarge renames an oversized log to "<path>.1", replacing any previous
// rotation. Failures are ignored: a log that cannot be rotated is not a reason to
// refuse to run.
func rotateLogIfLarge(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxLogBytes {
		return
	}
	_ = os.Rename(path, path+".1")
}
