package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	Retries              int  // rclone --retries value; 0 lets rclone use its own default
	RemoteFailThreshold  int  // circuit-breaker: abort after this many consecutive remote failures (0 = disabled)
	EstimateTransfer     bool // -estimate: probe destination before run to display a time estimate (remote only; skipped in dry-run)
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

// validatePaths ensures input and output paths are usable before the run starts.
func validatePaths(config *Config) error {
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
		if err := os.MkdirAll(config.OutputPath, 0755); err != nil {
			return fmt.Errorf("failed to create or access output directory: %w", err)
		}
	}
	return nil
}
