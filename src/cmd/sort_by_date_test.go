package main

import (
	"flag"
	"os"
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
	resetFlags(t, []string{"-i", "/input", "-o", "user@host:/remote/path"})
	cfg := NewConfig()

	assert.True(t, cfg.IsRemote)
	assert.Equal(t, "user@host:/remote/path", cfg.OutputPath)
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
