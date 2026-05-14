package main

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

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
