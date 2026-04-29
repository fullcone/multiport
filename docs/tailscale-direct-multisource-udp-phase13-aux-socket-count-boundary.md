# Tailscale Direct Multisource UDP Phase 13 Aux Socket Count Boundary

Date: 2026-04-29

This document records the Phase 13 boundary hardening for the Linux
source-selection auxiliary socket count in `fullcone/multiport`.

## PR Review Gate

Current PR feedback state before this implementation:

- PR: `https://github.com/fullcone/multiport/pull/1`
- PR head before this phase: `f0ed52dce`
- All known inline review threads were resolved.
- The latest Phase 12 doc-only review request
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343420548`
  received Codex response
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343429707`.
- Result: no major issues.
- No current blocking review thread was present, so implementation continued
  under the automatic flow rule.

## Scope

The original implementation plan defines the first Linux version as a
single-auxiliary-socket design, with `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS`
hard-limited to one enabled auxiliary source set.

The current implementation has exactly one IPv4 auxiliary socket and one IPv6
auxiliary socket:

- `sourcePathState.aux4`
- `sourcePathState.aux6`

Those two sockets form one dual-stack auxiliary source set. Values greater
than one for `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS` must not imply multiple
auxiliary sockets per address family.

This phase does not add true multi-auxiliary socket support. That would require
a separate design for source IDs, probe state, scoring, metrics, debug
snapshots, runtime cleanup, and data-send selection over more than one
candidate auxiliary socket per IP family.

## Code Changes

`wgengine/magicsock/sourcepath_linux_test.go`

- Adds `TestSourcePathAuxSocketCountBoundaryDualStack`.
- Verifies that source selection disabled by `TS_EXPERIMENTAL_SRCSEL_ENABLE`
  always produces zero auxiliary sockets.
- Verifies that `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=0` produces zero auxiliary
  sockets.
- Verifies that negative values produce zero auxiliary sockets.
- Verifies that `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1` produces one auxiliary
  source set.
- Verifies that `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=2` clamps to one auxiliary
  source set.
- Verifies that the clamped `AUX_SOCKETS=2` configuration still exposes only
  one IPv4 auxiliary probe source and one IPv6 auxiliary probe source.

No production code behavior is changed in this phase.

## Safety Properties

This pins the intended first-version boundary:

- `TS_EXPERIMENTAL_SRCSEL_ENABLE=0` creates no auxiliary source set even if
  `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS` is nonzero.
- `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS<=0` creates no auxiliary source set.
- `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1` enables the one supported dual-stack
  auxiliary source set.
- `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS>1` is accepted but clamped to the same
  single supported dual-stack auxiliary source set.

The feature remains conservative while the rest of the Phase 1 source metadata,
Linux probe, source-aware send, scoring, observability, runtime validation, and
rollback behavior continue to use the same single auxiliary IPv4/IPv6 pair.

## Validation

Completed local validation on 2026-04-29:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'gofmt -w wgengine/magicsock/sourcepath_linux_test.go'
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePathAuxSocketCountBoundaryDualStack|TestSourcePathDataSendSourceForcedAuxDualStack|TestSourcePathDataSendSourceNonDirectGuardDualStack" -count=1 -v'
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run TestSourcePath -count=1'
```

Validation results:

- `gofmt` completed successfully.
- `TestSourcePathAuxSocketCountBoundaryDualStack` passed.
- Existing forced auxiliary dual-stack source selection test passed.
- Existing non-direct IPv4/IPv6 guard test passed.
- Existing `TestSourcePath` focused group passed.
- The WSL command printed the known localhost/NAT warning after the Go test
  result; both Go test commands still exited successfully.

Focused test output:

```text
--- PASS: TestSourcePathAuxSocketCountBoundaryDualStack
--- PASS: TestSourcePathDataSendSourceForcedAuxDualStack
--- PASS: TestSourcePathDataSendSourceNonDirectGuardDualStack
PASS
ok  	tailscale.com/wgengine/magicsock	0.020s
```

Full focused group output:

```text
ok  	tailscale.com/wgengine/magicsock	0.220s
```
