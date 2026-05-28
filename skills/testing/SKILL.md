---
name: testing
description: Test strategy, patterns, and gotchas for media_organizer — testify suites, fake injection, external binary skipping.
skills:
  - fullstack-dev-skills:test-master
---

# Testing

## When to Invoke

| Skill | Invoke When |
|---|---|
| `/test-master` | Table-driven tests, test isolation, coverage gaps, testify suite patterns, benchmarks |

## Project Context

**Test suite** (`src/cmd/app_test.go`)
- `TestAppSuite` uses `testify/suite` — per-case setup/teardown lives in `SetupTest` / `TearDownTest`
- Run a single case: `go test -v ./src/cmd/ -run "TestAppSuite/TestProcessFile_DryRun"`

**Fake EXDEV injection** (`fileops.go`)
- `var osRename = os.Rename` — override at test start, restore with `defer`
- Lets you trigger the cross-device fallback without needing two real filesystems

**External binary tests** (`src/internal/exiftool_pool_test.go`)
- `TestExtractDate` calls real `exiftool` subprocess — auto-skips with `t.Skip` if not installed
- CI must have `exiftool` installed or these tests are silently skipped (not failing)

**Temp directories**
- Always `t.TempDir()` — automatic cleanup even on failure, no manual `defer os.RemoveAll`

**Darwin-only tests** (`fileutil_darwin_test.go`)
- Uses `//go:build darwin` — runs only on macOS
- Tests birth-time round-trip via `setFileBirthTime` / `getFileBirthTime` helpers

## Run Commands

```bash
go test -v ./...                                          # all tests
go test -v ./src/internal/ -run TestParseExifDate         # unit
go test -v ./src/cmd/ -run TestNewConfig                  # config
go test -v ./src/cmd/ -run TestAppSuite                   # full suite
go test -v ./src/cmd/ -run "TestAppSuite/TestProcessFile_DryRun"  # single case
go test -bench=. ./src/cmd/                               # benchmarks
```
