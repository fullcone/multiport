# Tailscale Direct Multisource UDP Phase 5 Auto Source Selection

Date: 2026-04-29

This document records the Phase 5 implementation against
`tailscale_direct_multisource_udp_final_implementation_v02.md`.

## Scope

Phase 5 adds the first opt-in automatic data-path consumer of the Phase 4B
source-path candidate boundary.

Implemented for both IP families:

- IPv4 automatic data sends can select a current IPv4 auxiliary source.
- IPv6 automatic data sends can select a current IPv6 auxiliary source.
- Selection only applies to direct UDP endpoints.
- Selection only uses current auxiliary source metadata that has matching
  probe samples for the same destination.
- Primary fallback remains the send-path recovery behavior when auxiliary
  transmission fails.

Out of scope for this phase:

- enabling automatic source selection by default
- selecting an auxiliary source for DERP or non-direct endpoints
- promoting auxiliary probe endpoints into endpoint maps
- rebinding primary sockets after auxiliary send failure
- changing the existing forced auxiliary debug mode semantics except making
  its precedence explicit

## Feature Gates

Automatic selection is disabled unless all of these are true:

```text
TS_EXPERIMENTAL_SRCSEL_ENABLE=true
TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true
```

The existing forced data-source knob has strict precedence:

```text
TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE
```

If the forced knob is non-empty, it owns source selection. For example,
`aux4` on an IPv6 destination returns the primary source; it does not fall
through to automatic IPv6 selection even when
`TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true`.

## Behavior

The new automatic path in `Conn.sourcePathDataSendSource(dst)` is:

1. Return primary when auxiliary sockets are disabled or the destination is not
   direct UDP.
2. If the forced data-source knob is set, apply the forced policy and return
   primary when the forced policy does not allow the destination IP family.
3. Return primary when `TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE` is disabled.
4. Call `Conn.sourcePathBestCandidate(dst)` and return the candidate source
   when a current auxiliary source has matching probe samples.
5. Return primary when no current candidate exists.

`Conn.sourcePathBestCandidate(dst)` snapshots source-path auxiliary sources
before locking `Conn.mu`, so automatic selection does not introduce nested
`sourcePath.mu` and `Conn.mu` locking.

## Code Changes

`wgengine/magicsock/sourcepath_linux.go`

- Adds `TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE`.
- Splits forced selection into `sourcePathForcedDataSendSource`.
- Makes forced data-source mode parsing reusable through
  `sourcePathForcedDataSourceModeAllowsAddr`.
- Wires automatic data-source selection to `Conn.sourcePathBestCandidate`.
- Keeps non-Linux behavior unchanged through `sourcepath_default.go`.

`wgengine/magicsock/sourcepath_linux_test.go`

- Adds dual-stack unit coverage for automatic IPv4 and IPv6 candidate
  selection.
- Proves stale auxiliary metadata, primary metadata, wrong-destination samples,
  and no-sample destinations do not select an auxiliary data source.
- Proves forced source mode has strict precedence over automatic selection.
- Adds a Linux dual-node runtime test for automatic IPv4 and IPv6 auxiliary
  WireGuard egress.
- Proves auxiliary `EPERM` fallback retries through the primary socket without
  changing `lastErrRebind`.

## Safety Properties

Automatic selection is Linux-only and disabled by default.

The automatic path does not mutate:

- pending source probes
- source probe samples
- endpoint maps
- primary rebind state
- auxiliary socket binding state

The selected source is still revalidated at send time by
`sourcePathWriteWireGuardBatchTo`. If an auxiliary socket is no longer bound or
its source metadata changed, the send path returns `errSourcePathUnavailable`
and the existing primary fallback path handles retry.

## Validation

Completed locally on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\sourcepath_linux.go wgengine\magicsock\sourcepath_linux_test.go
go test ./wgengine/magicsock -run "TestSourcePath" -count=1
go test ./wgengine/magicsock ./envknob -count=1
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run TestSourcePathAutomaticAuxDualNodeRuntime -count=1 -v'
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock ./envknob -count=1'
git diff --check
```

Results:

- Windows `go test ./wgengine/magicsock -run "TestSourcePath" -count=1`:
  passed.
- Windows `go test ./wgengine/magicsock ./envknob -count=1`: passed.
- WSL Ubuntu-24.04
  `go test ./wgengine/magicsock -run TestSourcePathAutomaticAuxDualNodeRuntime -count=1 -v`:
  passed.
- WSL Ubuntu-24.04 `go test ./wgengine/magicsock ./envknob -count=1`:
  passed.

Runtime proof lines:

- IPv4 automatic selection:
  `automatic aux runtime path: aux=127.0.0.1:35006 primary=127.0.0.1:45553 peer=127.0.0.1:34498 source={socketID:1 generation:1}`.
- IPv4 fallback after injected auxiliary `EPERM`:
  `srcsel: data send from source 1 to 127.0.0.1:34498 failed, retrying primary: write: operation not permitted`.
- IPv6 automatic selection:
  `automatic aux runtime path: aux=[::1]:44969 primary=[::1]:52172 peer=[::1]:54964 source={socketID:2 generation:1}`.
- IPv6 fallback after injected auxiliary `EPERM`:
  `srcsel: data send from source 2 to [::1]:54964 failed, retrying primary: write: operation not permitted`.

## PR Review Record

Pending commit and `@codex review` request for PR #1.
