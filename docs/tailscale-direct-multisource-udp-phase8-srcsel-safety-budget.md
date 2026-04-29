# Tailscale Direct Multisource UDP Phase 8 Source Probe Safety Budget

Date: 2026-04-29

This document records the Phase 8 implementation against
`tailscale_direct_multisource_udp_final_implementation_v02.md`.

## Scope

Phase 8 adds bounded source-path probe admission for auxiliary IPv4 and IPv6
probe traffic. The goal is to keep the experimental source selection feature
observable and bounded while preserving the existing primary path behavior.

Implemented for both IP families:

- maximum tracked destination disco peers with pending source-path probes
- maximum pending source-path probe burst per destination disco peer
- counters for probe admission drops caused by either budget

Out of scope for this phase:

- changing source-path probe packet format
- changing endpoint candidate discovery
- changing candidate scoring
- changing forced or automatic data-source selection
- changing primary socket rebind behavior
- adding Windows, macOS, or BSD source-path sockets

## Knobs

`TS_EXPERIMENTAL_SRCSEL_MAX_PEERS`

- Linux-only source selection knob.
- Limits the number of distinct destination disco peers that can have pending
  auxiliary source-path probes.
- Defaults to `32` when unset or set to a non-positive value.

`TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST`

- Linux-only source selection knob.
- Limits the number of pending auxiliary source-path probes for one
  destination disco peer across IPv4 and IPv6 auxiliary sources.
- Defaults to `1` when unset or set to a non-positive value.

The existing `TS_EXPERIMENTAL_SRCSEL_ENABLE` kill switch remains the outer
gate. When it is disabled, no auxiliary source-path sockets, probes, or
source-aware data sends are enabled.

## Metrics

`magicsock_srcsel_probe_peer_budget_dropped`

- Increments once when a new auxiliary source-path probe is rejected because
  adding its destination disco peer would exceed the tracked peer budget.

`magicsock_srcsel_probe_burst_budget_dropped`

- Increments once when a new auxiliary source-path probe is rejected because
  that destination disco peer already has the allowed number of pending
  auxiliary probes.

These counters are admission-drop counters. They do not count packets sent on
the network.

## Code Changes

`wgengine/magicsock/sourcepath.go`

- Adds default peer and burst budget constants.
- Adds budget-aware `sourcePathProbeManager.addLocked` admission.
- Prunes expired pending probes before evaluating budgets.
- Rejects over-budget probes before `sendSourcePathDiscoPing` writes a packet.
- Keeps budget state scoped to auxiliary source-path probe pending TxIDs.

`wgengine/magicsock/sourcepath_linux.go`

- Adds Linux env knobs for peer and burst budgets.
- Sanitizes non-positive knob values back to conservative defaults.

`wgengine/magicsock/sourcepath_default.go`

- Keeps non-Linux builds compiling with default budget helpers.

`wgengine/magicsock/magicsock.go`

- Adds peer-budget and burst-budget admission-drop counters.

`wgengine/magicsock/sourcepath_test.go`

- Covers peer-budget rejection without adding new pending entries.
- Covers per-peer burst-budget rejection across IPv4 and IPv6 auxiliary
  sources.
- Covers metric deltas for both rejection reasons.

## Safety Properties

Budget checks happen after stale pending probes are pruned and before any
auxiliary source-path probe packet is sent.

Rejected source-path probes:

- do not insert a pending TxID
- do not send a disco packet
- do not change endpoint candidate state
- do not change candidate scoring samples
- do not change primary socket rebind logic
- do not change data-source fallback behavior

The burst budget is keyed by destination disco public key, so IPv4 and IPv6
auxiliary probes for the same peer share the same conservative outstanding
probe budget.

## Validation

Completed local validation on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\magicsock.go wgengine\magicsock\sourcepath.go wgengine\magicsock\sourcepath_linux.go wgengine\magicsock\sourcepath_default.go wgengine\magicsock\sourcepath_test.go
go test ./wgengine/magicsock -run "TestSourcePathProbeManager.*Budget|TestSourcePathProbe" -count=1
go test ./wgengine/magicsock ./envknob -count=1
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePathProbeManager.*Budget|TestSourcePathProbe" -count=1'
git diff --check
```

Validation results:

- `gofmt` completed successfully.
- Windows focused source-path probe budget tests passed:
  `ok tailscale.com/wgengine/magicsock 0.052s`.
- Windows package validation passed:
  `ok tailscale.com/wgengine/magicsock 9.581s` and
  `ok tailscale.com/envknob 0.031s`.
- WSL Ubuntu-24.04 focused source-path probe budget tests passed:
  `ok tailscale.com/wgengine/magicsock 0.017s`.
- The WSL command printed the host localhost/NAT warning before running tests;
  the Go test command still exited successfully.
- `git diff --check` passed with only CRLF worktree conversion warnings for
  touched Go files.

## PR Review Record

Current PR feedback state before this implementation:

- Latest PR head before Phase 8 was
  `91ce572d8a95d8ad307d3d8e42d24cc27003be1c`.
- Latest Codex response before Phase 8 was a no-major-issues response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342785059`.
- Earlier Phase 2 review threads are resolved.
- Earlier Phase 3 test-fixture review threads are resolved and outdated.
- The only unresolved thread is the outdated Phase 4B P2
  `sourcePathSocket.rxMeta` synchronization finding
  (`PRRT_kwDOSPBZuM5-U8eD`), already fixed by commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a` and followed by a no-major-issues
  Codex review.
- No new actionable review thread blocked Phase 8.

Phase 8 implementation review tracking:

- Implementation commit:
  `6b07889052f18d956a164aff379ce44fed7d4dc8`.
- Review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342904876`.
- Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342921179`.
- Codex reported no major issues for the Phase 8 implementation review.
- The follow-up PR thread check found no new actionable review thread.
- The only unresolved thread remained the outdated Phase 4B P2
  `sourcePathSocket.rxMeta` synchronization finding
  (`PRRT_kwDOSPBZuM5-U8eD`), already fixed by commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a` and followed by a no-major-issues
  Codex review.
