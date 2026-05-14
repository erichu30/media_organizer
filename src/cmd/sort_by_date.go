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

	if app.Run(ctx) {
		return 1
	}
	return 0
}

// setupLogging configures logrus to write to sortbydate.log in the working directory.
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
