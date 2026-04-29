# Tailscale Direct Multisource UDP Phase 17 Debug Snapshot Lock Fix

Date: 2026-04-29

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase1-srcsel-source-metadata`

Pull request: `https://github.com/fullcone/multiport/pull/1`

PR head before this phase:
`e03a1df78c2e2ac86b59e53fa3f69feb2f4e7e44`

Phase 17 runtime-changing commits:

- `d1d93651d61c23db143af04be6d968504614094d`
- `202f878ef402de238dae3f3258a0c85f76a1333d`

## PR Review Gate

Automatic flow review polling found one new current unresolved Codex thread:

- `PRRT_kwDOSPBZuM5-c5c4`
- File: `wgengine/magicsock/debughttp.go`
- Anchor: line 222
- Severity: P1
- Request: lock source probe debug counters before reading them.

All earlier known threads were either resolved or outdated before this fix.

## Problem

The source-selection debug snapshot read `c.sourceProbes.pending` and
`c.sourceProbes.samples` through `pendingLenLocked` and `samplesLenLocked`.
Those fields are mutated under `c.mu` by source probe send, receive, timeout,
and cleanup paths.

Before this phase, the debug HTML caller happened to hold `c.mu` around the
source-selection section. That made the current handler safe but left the
source-selection debug helper dependent on an external lock contract that was
not enforced at the helper boundary. A future caller could reuse
`printSourcePathDebugHTML` without holding `c.mu` and race with source probe
mutation.

## Fix

`wgengine/magicsock/debughttp.go`

- Replaces the caller-dependent source-path debug snapshot helper with
  `sourcePathDebugSnapshot`, which acquires `c.mu` before reading
  `c.sourceProbes`.
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
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run TestPrintSourcePathDebugHTML -count=1'
```

Result:

```text
ok  	tailscale.com/wgengine/magicsock	0.010s
```

Command run from WSL:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -count=1'
```

Result:

```text
ok  	tailscale.com/wgengine/magicsock	11.377s
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

## Review Result

Thread `PRRT_kwDOSPBZuM5-c5c4` was resolved after the safe snapshot helper was
updated and the unsafe caller-dependent helper was removed.

Phase 17 follow-up review request:
`https://github.com/fullcone/multiport/pull/1#issuecomment-4344485699`

Phase 17 Codex response:
`https://github.com/fullcone/multiport/pull/1#issuecomment-4344515382`

Result: Codex reported no major issues for the Phase 17 debug snapshot lock
fix. Review polling after the response showed all known inline Codex threads
resolved.

## Post-Closeout Path Note

After this phase, the local checkout was normalized under
`C:\other_project\zerotier-client\multiport`. The documentation-only relocation
record is:

`docs/tailscale-direct-multisource-udp-phase18-worktree-path-normalization.md`
