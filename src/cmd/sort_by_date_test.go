package main

import (
	"flag"
	"os"
	"testing"
)

func TestNewConfig(t *testing.T) {
	// Save original os.Args
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	originalFlagCommand := flag.CommandLine
	defer func() { flag.CommandLine = originalFlagCommand }()

	testCases := []struct {
		name           string
		args           []string
		expectedConfig *Config
	}{
		{
			name: "Default values",
			args: []string{"-i", "/input", "-o", "/output"},
			expectedConfig: &Config{
				InputPath:            "/input",
				OutputPath:           "/output",
				Workers:              8,
				Buffer:               100,
				Debug:                false,
				CopyMode:             false,
				DryRun:               false,
				OnlyDateTimeOriginal: false,
				UseFileModifyDate:    false,
				IsRemote:             false,
			},
		},
		{
			name: "All flags set",
			args: []string{
				"-i", "/input",
				"-o", "/output",
				"-workers", "16",
				"-buffer", "200",
				"-debug",
				"-copy",
				"-dry-run",
				"-only-datetimeoriginal",
				"-use-file-modify-date",
			},
			expectedConfig: &Config{
				InputPath:            "/input",
				OutputPath:           "/output",
				Workers:              16,
				Buffer:               200,
				Debug:                true,
				CopyMode:             true,
				DryRun:               true,
				OnlyDateTimeOriginal: true,
				UseFileModifyDate:    true,
				IsRemote:             false,
			},
		},
		{
			name: "Remote output",
			args: []string{"-i", "/input", "-o", "user@host:/remote/path"},
			expectedConfig: &Config{
				InputPath:            "/input",
				OutputPath:           "user@host:/remote/path",
				Workers:              8,
				Buffer:               100,
				Debug:                false,
				CopyMode:             false,
				DryRun:               false,
				OnlyDateTimeOriginal: false,
				UseFileModifyDate:    false,
				IsRemote:             true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// We need to reset the flag set for each test case
			flag.CommandLine = flag.NewFlagSet(tc.name, flag.ExitOnError)
			os.Args = append([]string{tc.name}, tc.args...)
			config := NewConfig()

			if config.InputPath != tc.expectedConfig.InputPath {
				t.Errorf("Expected InputPath %v, but got %v", tc.expectedConfig.InputPath, config.InputPath)
			}
			if config.OutputPath != tc.expectedConfig.OutputPath {
				t.Errorf("Expected OutputPath %v, but got %v", tc.expectedConfig.OutputPath, config.OutputPath)
			}
			if config.Workers != tc.expectedConfig.Workers {
				t.Errorf("Expected Workers %v, but got %v", tc.expectedConfig.Workers, config.Workers)
			}
			if config.Buffer != tc.expectedConfig.Buffer {
				t.Errorf("Expected Buffer %v, but got %v", tc.expectedConfig.Buffer, config.Buffer)
			}
			if config.Debug != tc.expectedConfig.Debug {
				t.Errorf("Expected Debug %v, but got %v", tc.expectedConfig.Debug, config.Debug)
			}
			if config.CopyMode != tc.expectedConfig.CopyMode {
				t.Errorf("Expected CopyMode %v, but got %v", tc.expectedConfig.CopyMode, config.CopyMode)
			}
			if config.DryRun != tc.expectedConfig.DryRun {
				t.Errorf("Expected DryRun %v, but got %v", tc.expectedConfig.DryRun, config.DryRun)
			}
			if config.OnlyDateTimeOriginal != tc.expectedConfig.OnlyDateTimeOriginal {
				t.Errorf("Expected OnlyDateTimeOriginal %v, but got %v", tc.expectedConfig.OnlyDateTimeOriginal, config.OnlyDateTimeOriginal)
			}
			if config.UseFileModifyDate != tc.expectedConfig.UseFileModifyDate {
				t.Errorf("Expected UseFileModifyDate %v, but got %v", tc.expectedConfig.UseFileModifyDate, config.UseFileModifyDate)
			}
			if config.IsRemote != tc.expectedConfig.IsRemote {
				t.Errorf("Expected IsRemote %v, but got %v", tc.expectedConfig.IsRemote, config.IsRemote)
			}
		})
	}
}
