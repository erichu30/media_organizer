---
name: code-review
description: Code review and debugging guidance for media_organizer — concurrency bugs, subprocess lifecycle, rclone failure patterns.
skills:
  - fullstack-dev-skills:code-reviewer
  - fullstack-dev-skills:debugging-wizard
---

# Code Review

## When to Invoke

| Skill | Invoke When |
|---|---|
| `/code-reviewer` | PR review, logic errors, refactor suggestions |
| `/debugging-wizard` | Concurrency bugs, rclone subprocess failures, exiftool lifecycle issues |

## Review Checklist for This Project

**Concurrency**
- [ ] New code that reads/writes shared state uses `sync.Map`, `atomic`, or a mutex — not plain maps
- [ ] Any channel send in the feeder uses `select { case jobs <- path: case <-ctx.Done(): }` — never a bare send
- [ ] `ExifToolPool` instances are always returned via `defer checkin` — never stored outside the pool
- [ ] `ExifToolPool.Close()` is deferred immediately after creation in `run()`

**Error handling**
- [ ] `processFile` returns `(reason string, err error)` — reason is a short bucket string, not a formatted message
- [ ] No `log.Fatal` or `os.Exit` inside workers — always propagate via return values
- [ ] EXDEV handling: any new local move path catches `syscall.EXDEV` and falls back to copy+delete

**Context propagation**
- [ ] New functions that do I/O accept a `ctx context.Context` parameter
- [ ] No `context.Background()` created inside a function that already has a `ctx`
- [ ] `cleanupRemoteFile` intentionally uses a fresh context — this is correct, not a bug

**Platform code**
- [ ] New Darwin-specific APIs have both `//go:build darwin` tag and `_darwin.go` filename suffix
- [ ] Corresponding `_other.go` fallback exists for every platform-split function

## Common Bugs to Watch For

| Symptom | Likely Cause |
|---|---|
| Dates wrong or garbled | Concurrent use of a single `ExifToolService` (missing pool checkout) |
| Files not moved, no error | `os.Rename` EXDEV not caught — missing cross-device fallback |
| Failure log truncated | Early `os.Exit` bypassed `FailureLogger.Close()` defer |
| Workers hang on shutdown | Bare channel send instead of `select` with `ctx.Done()` |
| rclone "remote not found" | SSH path not converted — `toRcloneSFTPPath` not called |

See `ERRORS.md` for the full pitfall list.
