---
name: security
description: Security concerns specific to media_organizer — subprocess argument construction, SSH key handling, path traversal in output destinations.
skills:
  - fullstack-dev-skills:security-reviewer
  - fullstack-dev-skills:secure-code-guardian
---

# Security

## When to Invoke

| Skill | Invoke When |
|---|---|
| `/security-reviewer` | Audit subprocess calls, SSH key handling, output path validation |
| `/secure-code-guardian` | Writing new transfer code, adding new CLI flags that accept paths |

## Project-Specific Risks

### 1. `exec.Command` argument construction (`fileops.go`)

rclone is invoked with user-supplied paths directly as arguments:
```go
exec.CommandContext(ctx, "rclone", "moveto", src, dst, ...)
```
**Rule:** Arguments must be passed as separate strings to `exec.Command` — never concatenated into a shell string. The current code is safe. Never refactor to `exec.Command("sh", "-c", "rclone moveto "+src+" "+dst)`.

### 2. SSH key path expansion (`config.go:expandTilde`)

`-ssh-key` accepts a path with `~`. The expansion uses `os.UserHomeDir()`, not shell interpolation. Do not replace with `os.ExpandEnv` or shell execution.

### 3. Output path traversal

`-o` accepts arbitrary paths including SSH destinations. The path is used verbatim as a rclone destination. No sanitization is done beyond SSH-to-SFTP normalization.
- **Risk:** A crafted `-o` could write to unexpected remote locations if this tool is ever wrapped in a web API or automation layer.
- **Mitigation (current):** Tool is CLI-only; path comes directly from the invoking user.

### 4. `failure-log` path

`-failure-log auto` generates a timestamped filename. A literal path is used as-is with `O_TRUNC`. No directory traversal protection — assumes trusted input (CLI user).

## Key Files

- `src/cmd/config.go` — `expandTilde`, `toRcloneSFTPPath`, `isSFTPPath`
- `src/cmd/fileops.go` — `transferRemote` (exec.Command construction)
- `src/cmd/failure_log.go` — file open with user-supplied path
