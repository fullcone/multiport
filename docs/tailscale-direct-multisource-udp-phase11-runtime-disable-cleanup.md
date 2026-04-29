# Tailscale Direct Multisource UDP Phase 11 Runtime Disable Cleanup

Date: 2026-04-29

This document records the Phase 11 cleanup for runtime disable and rollback
behavior in `fullcone/multiport`.

## PR Review Gate

Current PR feedback state before this implementation:

- PR: `https://github.com/fullcone/multiport/pull/1`
- PR head before this phase: `d1911158efb6411ce3c272685c4744b5b21f35f5`
- All earlier actionable review threads were resolved or outdated.
- The stale `sourcePathSocket.rxMeta` synchronization thread
  `PRRT_kwDOSPBZuM5-U8eD` had already been fixed by atomic socket metadata and
  was resolved before this phase.
- No current blocking review thread was present, so implementation continued
  under the automatic flow rule.

## Scope

The original implementation plan requires `TS_EXPERIMENTAL_SRCSEL_ENABLE=0` to
restore native magicsock behavior. In addition to closing auxiliary sockets,
runtime disable must clear source-selection state that could otherwise survive
from an enabled period:

- pending auxiliary source-path probes
- retained source-path probe samples
- automatic source candidate evidence derived from those samples

Primary send, receive, and rebind behavior must continue to work without
depending on source-selection state.

## Code Changes

`wgengine/magicsock/sourcepath.go`

- Adds `sourcePathProbeManager.clearLocked`.
- The method clears the pending TxID map and drops retained samples.
- Dropping samples also drops any automatic candidate evidence, because
  candidates are computed from retained samples.

`wgengine/magicsock/sourcepath_linux.go`

- When `sourcePathAuxSocketCount() == 0`, `rebindSourcePathSockets` now:
  - closes auxiliary IPv4 and IPv6 sockets
  - clears pending probe state
  - clears retained source-path samples
- The auxiliary socket close still happens under `sourcePath.mu`.
- Probe-state cleanup happens under `Conn.mu`, after auxiliary sockets have
  been closed.

`wgengine/magicsock/magicsock.go`

- `Conn.Close` now clears source-path probe state after closing auxiliary
  sockets.
- This keeps full connection teardown aligned with runtime disable cleanup.

`wgengine/magicsock/sourcepath_linux_test.go`

- Adds `TestSourcePathRebindDisabledClosesAuxAndClearsState`.
- The test seeds both IPv4 and IPv6 auxiliary sockets, pending probes, and
  retained samples, while srcsel itself is disabled.
- It verifies:
  - disabled rebind closes both auxiliary sockets
  - auxiliary bound flags are cleared
  - pending probes are cleared
  - retained samples are cleared
  - forced aux data-source mode still falls back to primary when srcsel is
    disabled
- The test uses fake packet conns, so it does not depend on host IPv6 socket
  availability.

## Safety Properties

This phase does not change:

- source-path probe packet format
- source-path candidate scoring rules while srcsel is enabled
- forced auxiliary source selection while srcsel is enabled
- automatic auxiliary source selection while srcsel is enabled
- primary socket send and receive paths
- primary rebind error accounting

The disabled path now removes stale source-selection state before any later
enable or close path can observe it. With `TS_EXPERIMENTAL_SRCSEL_ENABLE=0`,
`sourcePathDataSendSource` still returns `primarySourceRxMeta` before forced or
automatic source selection is considered.

## Validation

Completed local validation on 2026-04-29:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'gofmt -w wgengine/magicsock/sourcepath.go wgengine/magicsock/sourcepath_linux.go wgengine/magicsock/magicsock.go wgengine/magicsock/sourcepath_linux_test.go'
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePath(RebindDisabledClosesAuxAndClearsState|DataSendSourceForcedAuxDualStack|DataSendSourceAutomaticCandidateDualStack|ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v'
```

Validation results:

- `gofmt` completed successfully.
- `TestSourcePathRebindDisabledClosesAuxAndClearsState` passed.
- Existing forced source-selection dual-stack unit test passed.
- Existing automatic source-selection dual-stack unit test passed.
- Existing forced auxiliary dual-node runtime test passed for IPv4 and IPv6.
- Existing automatic auxiliary dual-node runtime test passed for IPv4 and IPv6.
- The WSL command printed the known localhost/NAT warning after the Go test
  result; the Go test command still exited successfully.

The focused test run ended with:

```text
PASS
ok  	tailscale.com/wgengine/magicsock	0.217s
```
