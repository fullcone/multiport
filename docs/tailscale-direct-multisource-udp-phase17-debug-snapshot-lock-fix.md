# Tailscale Direct Multisource UDP Phase 17 Debug Snapshot Lock Fix

Date: 2026-04-29

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\fullcone`

WSL checkout: `/mnt/c/other_project/fullcone`

Branch: `phase1-srcsel-source-metadata`

Pull request: `https://github.com/fullcone/multiport/pull/1`

PR head before this phase:
`e03a1df78c2e2ac86b59e53fa3f69feb2f4e7e44`

## PR Review Gate

Automatic flow review polling found one new current unresolved Codex thread:

- `PRRT_kwDOSPBZuM5-c5c4`
- File: `wgengine/magicsock/debughttp.go`
- Anchor: line 222
- Severity: P1
- Request: lock source probe debug counters before reading them.

All earlier known threads were either resolved or outdated before this fix.

## Problem

`sourcePathDebugSnapshotLocked` read `c.sourceProbes.pending` and
`c.sourceProbes.samples` through `pendingLenLocked` and `samplesLenLocked`.
Those fields are mutated under `c.mu` by source probe send, receive, timeout,
and cleanup paths.

Before this phase, the debug HTML caller happened to hold `c.mu` around the
source-selection section. That made the current handler safe but left the
source-selection debug helper dependent on an external lock contract that was
not enforced at the helper boundary. A future caller could reuse
`printSourcePathDebugHTML` or `sourcePathDebugSnapshotLocked` without holding
`c.mu` and race with source probe mutation.

## Fix

`wgengine/magicsock/debughttp.go`

- Adds a safe `sourcePathDebugSnapshot` wrapper that acquires `c.mu` before
  reading `c.sourceProbes`.
- Keeps `sourcePathDebugSnapshotLocked` as the internal locked helper and
  documents that it requires `c.mu`.
- Makes `printSourcePathDebugHTML` call the safe wrapper.
- Moves the outer `ServeHTTPDebug` `c.mu` acquisition to after the
  source-selection section so the safe wrapper does not re-enter `c.mu`.

`wgengine/magicsock/debughttp_test.go`

- Updates `TestPrintSourcePathDebugHTML` so the test calls
  `printSourcePathDebugHTML` without externally locking `c.mu`.
- This confirms the debug helper is safe at its public helper boundary and
  still reports source probe counters.

## Validation

Command run from WSL:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run TestPrintSourcePathDebugHTML -count=1'
```

Result:

```text
ok  	tailscale.com/wgengine/magicsock	0.013s
```

Command run from WSL:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -count=1'
```

Result:

```text
ok  	tailscale.com/wgengine/magicsock	12.368s
```

The WSL commands printed the host's usual localhost/NAT warning after the
successful Go test result. That warning was outside the Go test process and did
not change the test exit status.

Command run locally:

```powershell
git diff --check
```

Result: passed. Git printed only LF-to-CRLF working-copy warnings for the Go
files.

## Expected Review Resolution

This phase directly addresses `PRRT_kwDOSPBZuM5-c5c4` by ensuring source probe
debug counters are read while `c.mu` is held, independent of the debug helper's
caller.
