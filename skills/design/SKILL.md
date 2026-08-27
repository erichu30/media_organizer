---
name: design
description: Architecture and design guidance for media_organizer — evaluating new features, pipeline redesigns, and transfer backend trade-offs.
skills:
  - fullstack-dev-skills:architecture-designer
---

# Design / Architecture

## When to Invoke

| Skill | Invoke When |
|---|---|
| `/architecture-designer` | Evaluating new features, pipeline redesigns, adding transfer backends, ADR drafts |

## Open Design Problems

See `TASKS.md` for full analysis. Summary:

### Batch rclone optimization (SSH connection reuse)
- **Problem:** One rclone subprocess per file = one SSH handshake per file (~300–500 ms each)
- **Proposed:** Group files by `YYYY/MM` destination, run one `rclone --files-from` per group
- **Blocker:** Per-file failure tracking becomes lossy with batch mode; circuit breaker needs adaptation
- **Impact:** 142 files → ~60 s overhead drops to < 5 s for typical runs

### Source disk disconnect resilience
- **Problem:** I/O errors from a disconnected source disk are bucketed as `"no EXIF date"` or `"file operation failed"` — indistinguishable from normal failures
- **Proposed:** `isSourceIOError` classifier + separate source circuit breaker + feeder-loop `Lstat` validation
- **Impact:** Clearer diagnostics, faster abort when source disk is gone

## Architecture Constraints

Any redesign must preserve:
1. **Signal safety** — `SIGINT`/`SIGTERM` stops the feeder; workers drain; cleanup always runs
2. **Atomic local moves** — `os.Rename` on same filesystem; EXDEV falls back to copy+delete
3. **Partial remote file cleanup** — `cleanupRemoteFile` must run after any rclone failure
4. **Context propagation** — all I/O operations must respect `ctx` cancellation
5. **`main()` as thin wrapper** — deferred cleanup must always execute

## Key Reference

- `ARCHITECTURE.md` — full package layout, data-flow diagram, decision rationale
- `TASKS.md` — open tasks with option analysis and trade-offs
