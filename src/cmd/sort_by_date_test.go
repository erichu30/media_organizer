package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConfig_Defaults(t *testing.T) {
	resetFlags(t, []string{"-i", "/input", "-o", "/output"})
	cfg := NewConfig()

	assert.Equal(t, "/input", cfg.InputPath)
	assert.Equal(t, "/output", cfg.OutputPath)
	assert.Equal(t, 8, cfg.Workers)
	assert.Equal(t, 100, cfg.Buffer)
	assert.False(t, cfg.Debug)
	assert.False(t, cfg.CopyMode)
	assert.False(t, cfg.DryRun)
	assert.False(t, cfg.OnlyDateTimeOriginal)
	assert.False(t, cfg.UseFileModifyDate)
	assert.False(t, cfg.IsRemote)
}

func TestNewConfig_AllFlags(t *testing.T) {
	resetFlags(t, []string{
		"-i", "/input",
		"-o", "/output",
		"-workers", "16",
		"-buffer", "200",
		"-debug",
		"-copy",
		"-dry-run",
		"-only-datetimeoriginal",
		"-use-file-modify-date",
	})
	cfg := NewConfig()

	require.Equal(t, 16, cfg.Workers)
	require.Equal(t, 200, cfg.Buffer)
	assert.True(t, cfg.Debug)
	assert.True(t, cfg.CopyMode)
	assert.True(t, cfg.DryRun)
	assert.True(t, cfg.OnlyDateTimeOriginal)
	assert.True(t, cfg.UseFileModifyDate)
}

func TestNewConfig_RemoteOutput(t *testing.T) {
	resetFlags(t, []string{"-i", "/input", "-o", "myremote:/remote/path"})
	cfg := NewConfig()

	assert.True(t, cfg.IsRemote)
	assert.Equal(t, "myremote:/remote/path", cfg.OutputPath)
}

func TestNewConfig_SSHPathIsRemote(t *testing.T) {
	// user@host:/path is auto-converted to rclone SFTP on-the-fly syntax and treated as remote.
	resetFlags(t, []string{"-i", "/input", "-o", "user@host:/remote/path"})
	cfg := NewConfig()

	assert.True(t, cfg.IsRemote)
	assert.Equal(t, ":sftp,host=host,user=user:/remote/path", cfg.OutputPath)
}

func TestNewConfig_SSHKeyEmbeddedInOutputPath(t *testing.T) {
	resetFlags(t, []string{"-i", "/input", "-o", "user@host:/remote/path", "-ssh-key", "/home/user/.ssh/id_ed25519"})
	cfg := NewConfig()

	assert.True(t, cfg.IsRemote)
	assert.Equal(t, ":sftp,host=host,user=user,key_file=/home/user/.ssh/id_ed25519:/remote/path", cfg.OutputPath)
}

func TestNewConfig_LocalOutputNotRemote(t *testing.T) {
	resetFlags(t, []string{"-i", "/input", "-o", "/local/path"})
	cfg := NewConfig()

	assert.False(t, cfg.IsRemote)
}

// resetFlags resets the flag.CommandLine and os.Args for isolated flag parsing per test.
func resetFlags(t *testing.T, args []string) {
	t.Helper()
	orig := os.Args
	origFlags := flag.CommandLine
	t.Cleanup(func() {
		os.Args = orig
		flag.CommandLine = origFlags
	})
	flag.CommandLine = flag.NewFlagSet(t.Name(), flag.ExitOnError)
	os.Args = append([]string{t.Name()}, args...)
}

func TestValidatePaths_LocalSuccess(t *testing.T) {
	tmp := t.TempDir()
	inDir := filepath.Join(tmp, "input")
	outDir := filepath.Join(tmp, "output")
	require.NoError(t, os.Mkdir(inDir, 0755))

	cfg := &Config{
		InputPath:  inDir,
		OutputPath: outDir,
		IsRemote:   false,
	}
	err := validatePaths(cfg)
	assert.NoError(t, err)
	assert.DirExists(t, outDir)
}

func TestValidatePaths_InputNotExist(t *testing.T) {
	cfg := &Config{
		InputPath: "/does/not/exist/12345",
	}
	err := validatePaths(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestValidatePaths_InputIsFile(t *testing.T) {
	tmp := t.TempDir()
	inFile := filepath.Join(tmp, "file.txt")
	require.NoError(t, os.WriteFile(inFile, []byte("test"), 0644))

	cfg := &Config{
		InputPath: inFile,
	}
	err := validatePaths(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be a directory")
}

func TestValidatePaths_LocalOutputCreationFails(t *testing.T) {
	tmp := t.TempDir()
	inDir := filepath.Join(tmp, "input")
	require.NoError(t, os.Mkdir(inDir, 0755))

	// Create a file where the output directory is supposed to be,
	// which will cause MkdirAll to fail.
	outFile := filepath.Join(tmp, "output")
	require.NoError(t, os.WriteFile(outFile, []byte("blocking file"), 0644))

	cfg := &Config{
		InputPath:  inDir,
		OutputPath: outFile,
		IsRemote:   false,
	}

	err := validatePaths(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create or access output directory")
}

func TestValidatePaths_RemoteMissingTools(t *testing.T) {
	tmp := t.TempDir()
	inDir := filepath.Join(tmp, "input")
	require.NoError(t, os.Mkdir(inDir, 0755))

	cfg := &Config{
		InputPath:  inDir,
		OutputPath: "myremote:/path",
		IsRemote:   true,
	}

	// Clear PATH temporarily to simulate missing rclone
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", "")

	err := validatePaths(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command not found")
}

func TestValidatePaths_RemoteSuccess(t *testing.T) {
	tmp := t.TempDir()
	inDir := filepath.Join(tmp, "input")
	require.NoError(t, os.Mkdir(inDir, 0755))

	cfg := &Config{
		InputPath:  inDir,
		OutputPath: "myremote:/path",
		IsRemote:   true,
	}

	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not found in PATH, skipping remote success test")
	}

	err := validatePaths(cfg)
	assert.NoError(t, err)
}

// ---- isSFTPPath / toRcloneSFTPPath / isRclonePath ----

func TestIsSFTPPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"root@192.168.0.83:/mnt/photos", true},
		{"user@host:path", true},
		{"remotename:/path", false},  // rclone remote, no @
		{"/local/path", false},       // plain local
		{"C:\\Windows\\path", false}, // Windows-style local
		{"@missinguser:path", false}, // @ at index 0
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, isSFTPPath(tc.in))
		})
	}
}

func TestToRcloneSFTPPath(t *testing.T) {
	cases := []struct {
		in      string
		keyFile string
		want    string
	}{
		{
			"root@192.168.0.83:/mnt/naspool/photo", "",
			":sftp,host=192.168.0.83,user=root:/mnt/naspool/photo",
		},
		{
			"alice@nas:backup", "",
			":sftp,host=nas,user=alice:backup",
		},
		{
			"root@192.168.0.83:/mnt/photos", "/home/user/.ssh/id_ed25519",
			":sftp,host=192.168.0.83,user=root,key_file=/home/user/.ssh/id_ed25519:/mnt/photos",
		},
		{"remotename:/path", "", "remotename:/path"},
		{"/local/path", "", "/local/path"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, toRcloneSFTPPath(tc.in, tc.keyFile))
		})
	}
}

func TestIsRclonePath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"myremote:/photos", true},
		{"root@192.168.0.83:/mnt/p", true},
		{"user@host:path", true},
		{"/local/path", false},
		{"relative/path", false},
		// A colon inside a local path does not make it a remote. Treating these as
		// rclone destinations sent the whole run to a remote name that never existed.
		{"/Volumes/Trips/2024:Japan", false},
		{"./out:staging", false},
		{"~/Pictures/2024:trip", false},
		{"photos:2024/sorted", true}, // bare token before the colon — a real remote
		{"C:\\Users\\me\\Photos", false},
		{"D:/Photos", false},
		{":memory", false}, // empty remote name
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, isRclonePath(tc.in))
		})
	}
}

// ---- validateConfig ----

// TestValidateConfig_Defaults confirms an untouched config passes, so the checks
// below are rejecting bad input rather than the defaults.
func TestValidateConfig_Defaults(t *testing.T) {
	resetFlags(t, []string{"-i", "/input", "-o", "/output"})
	assert.NoError(t, validateConfig(NewConfig()))
}

// TestValidateConfig_RejectsUnusableValues covers the flag values that used to fail
// only at run time: -workers 0 hung forever with an empty screen, and a negative
// -buffer panicked inside make(chan).
func TestValidateConfig_RejectsUnusableValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantMsg string
	}{
		{"zero workers", func(c *Config) { c.Workers = 0 }, "-workers"},
		{"negative workers", func(c *Config) { c.Workers = -3 }, "-workers"},
		{"too many workers", func(c *Config) { c.Workers = maxWorkers + 1 }, "-workers"},
		{"negative buffer", func(c *Config) { c.Buffer = -1 }, "-buffer"},
		{"negative retries", func(c *Config) { c.Retries = -1 }, "-retries"},
		{"negative threshold", func(c *Config) { c.RemoteFailThreshold = -1 }, "-remote-fail-threshold"},
		{"unknown conflict policy", func(c *Config) { c.OnConflict = "clobber" }, "-on-conflict"},
		{"empty log path", func(c *Config) { c.LogPath = "" }, "-log"},
		{"estimate on local dest", func(c *Config) { c.EstimateTransfer = true }, "-estimate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				InputPath: "/input", OutputPath: "/output",
				Workers: 8, Buffer: 100, Retries: 3,
				RemoteFailThreshold: 5, OnConflict: ConflictRename, LogPath: defaultLogPath,
			}
			tc.mutate(cfg)

			err := validateConfig(cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestValidateConfig_EstimateAllowedOnRemote(t *testing.T) {
	cfg := &Config{
		InputPath: "/input", OutputPath: "remote:/photos", IsRemote: true,
		Workers: 8, Buffer: 100, Retries: 3,
		RemoteFailThreshold: 5, OnConflict: ConflictRename, LogPath: defaultLogPath,
		EstimateTransfer: true,
	}
	assert.NoError(t, validateConfig(cfg))
}

// ---- new flags ----

func TestNewConfig_ConflictAndLogFlags(t *testing.T) {
	resetFlags(t, []string{"-i", "/in", "-o", "/out", "-on-conflict", "skip", "-log", "/tmp/run.log"})
	cfg := NewConfig()

	assert.Equal(t, ConflictSkip, cfg.OnConflict)
	assert.Equal(t, "/tmp/run.log", cfg.LogPath)
}

func TestNewConfig_ConflictDefaultsToRename(t *testing.T) {
	resetFlags(t, []string{"-i", "/in", "-o", "/out"})
	cfg := NewConfig()

	assert.Equal(t, ConflictRename, cfg.OnConflict, "the default must never lose a file")
	assert.Equal(t, defaultLogPath, cfg.LogPath)
}

// ---- log rotation ----

func TestRotateLogIfLarge(t *testing.T) {
	t.Run("small log is left alone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "run.log")
		require.NoError(t, os.WriteFile(path, []byte("short"), 0644))

		rotateLogIfLarge(path)

		assert.FileExists(t, path)
		assert.NoFileExists(t, path+".1")
	})

	t.Run("oversized log is rotated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "run.log")
		require.NoError(t, os.WriteFile(path, make([]byte, maxLogBytes+1), 0644))

		rotateLogIfLarge(path)

		assert.NoFileExists(t, path)
		assert.FileExists(t, path+".1")
	})

	t.Run("missing log is not an error", func(t *testing.T) {
		assert.NotPanics(t, func() { rotateLogIfLarge(filepath.Join(t.TempDir(), "absent.log")) })
	})
}

// ---- excluded directories ----

// TestIsExcludedDir covers the directories that are full of media-extension files
// that are not the user's photos — a Synology @eaDir holds one SYNOPHOTO_THUMB_*.jpg
// per real photo, and a Photos library bundle comes apart if it is reorganized.
func TestIsExcludedDir(t *testing.T) {
	excluded := []string{
		".Spotlight-V100", ".fseventsd", ".DocumentRevisions-V100", ".Trashes",
		"@eaDir", "#recycle", "$RECYCLE.BIN", ".git",
		"Photos Library.photoslibrary", "Trip.LRDATA",
	}
	for _, name := range excluded {
		t.Run(name, func(t *testing.T) { assert.True(t, isExcludedDir(name)) })
	}

	kept := []string{"2023", "05", "Holidays", "photos", "eaDir", "library"}
	for _, name := range kept {
		t.Run("keeps "+name, func(t *testing.T) { assert.False(t, isExcludedDir(name)) })
	}
}
