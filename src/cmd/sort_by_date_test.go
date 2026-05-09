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

func TestNewConfig_SSHPathNotRemote(t *testing.T) {
	// Legacy user@host:/path syntax is no longer treated as remote; users must configure an rclone remote.
	resetFlags(t, []string{"-i", "/input", "-o", "user@host:/remote/path"})
	cfg := NewConfig()

	assert.False(t, cfg.IsRemote)
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
