# Tailscale Direct Multisource UDP Phase 20 Primary-Baseline Scorer

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase20-srcsel-primary-baseline`

Pull request: not yet opened

Phase 20 commits (on top of `00f24e43b`, the PR #1 merge):

- `30bc41867` magicsock: gate srcsel automatic selection on primary-baseline RTT

## Why Phase 20

PR #1's Phase 19 closed the post-Phase-16 audit findings but left one
out-of-scope item explicit:

> Primary-baseline RTT comparison in the scorer. The current scorer is
> absolute (it does not compare auxiliary mean latency to primary). A
> later phase may add a primary-baseline gate if real-network data
> shows automatic selection picking auxiliary when primary is in fact
> better.

Phase 20 adds that gate so automatic selection only steers data onto an
auxiliary source when the auxiliary is meaningfully faster than the
primary path being used. This matters most on networks where the primary
already has low RTT — without a gate, a probe sample that lands a few
hundred microseconds below the primary mean is enough to flip the
data plane for that destination.

## Behavior

### New scorer parameter

`bestCandidateLocked` now takes a `primaryRTT time.Duration` argument:

- `primaryRTT == 0` — gate disabled (Phase 19 behavior). Auxiliary
  candidates are accepted on the absolute TTL + minimum-sample rules
  alone. Used when the caller has no primary measurement yet.
- `primaryRTT > 0` — gate enabled. An auxiliary candidate's mean
  latency must satisfy `mean < primaryRTT × (1 - threshold/100)` to be
  eligible. Failures increment the new
  `magicsock_srcsel_primary_beat_rejected` counter and skip that
  candidate; other auxiliary candidates may still win.

`Conn.sourcePathBestCandidate(dst)` now looks up the endpoint that owns
`dst` and reads its observed primary RTT before invoking the scorer.
Lookup acquires `c.mu` to find the endpoint, releases it, then takes
`de.mu` to read RTT — keeping the conventional lock order.

### New endpoint helper

`endpoint.primaryRTTForLocked(dst epAddr) time.Duration` returns the
most recently observed primary-path RTT for `dst`, or `0` if no usable
measurement exists. Resolution order:

1. `endpoint.endpointState[dst.ap].latencyLocked()` — most recent pong
   RTT for that exact address (preferred; freshest data).
2. `endpoint.bestAddr.latency` — when `dst` is the current bestAddr
   (fallback; older but always present once a direct path is up).

Returning `0` deliberately disables the gate at the call site so
endpoints with no telemetry yet (e.g., right after a netmap add) do
not get penalized.

### Threshold defaults and override

| Source                                              | Value                                |
| --------------------------------------------------- | ------------------------------------ |
| `sourcePathAuxBeatThresholdPercent` (constant)      | 10                                   |
| `TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT < 0` | 0 (gate disabled)                    |
| `TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT == 0` | constant default                    |
| `TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT > 0`  | clamped to `[1, 100]`               |

`100` makes the gate impossible to satisfy (auxiliary never selected);
`1` makes a one-percent improvement enough; `-1` opts out of the gate
without removing the metric or the env knob.

## Tests

New unit tests in `sourcepath_test.go`:

- `TestSourcePathProbeManagerPrimaryBaselineRejectsClose` — aux mean of
  19 ms with primary 20 ms (5 % improvement) is rejected at the
  default 10 % threshold; the same samples without a primary RTT are
  still accepted (Phase 19 backward compat).
- `TestSourcePathProbeManagerPrimaryBaselineAcceptsClearWin` — aux mean
  of 5 ms with primary 20 ms (75 % improvement) is accepted.
- `TestSourcePathProbeManagerPrimaryBaselineThresholdEnvOverride` —
  setting the threshold env to `1` lets a 5 % improvement qualify.
- `TestSourcePathProbeManagerPrimaryBaselineThresholdEnvClampedTo100`
  — env value `200` clamps to 100 and rejects even a 1 ms aux against
  20 ms primary.
- `TestEndpointPrimaryRTTForLockedFallsBackToBestAddr` — exercises the
  pong-history → bestAddr fallback chain on the endpoint helper.

The existing `TestSourcePathAutomaticAuxDualNodeRuntime` was updated
to set the env knob to `-1`. Real loopback primary RTT is
sub-millisecond, far below the seeded 1 ms aux samples, so leaving the
gate enabled would cause a deterministic test failure that has nothing
to do with automatic-mode selection logic. The runtime evidence the
test produces (auxiliary writes captured by the recording listener,
metrics counters) is unchanged.

## Validation

`wsl.exe -d Ubuntu-24.04 -- bash -lc 'cd /mnt/c/other_project/zerotier-client/multiport && go test ./wgengine/magicsock -count=1 -timeout 300s'`
passes in ~10.5 s on Go 1.26.2.

## Out Of Scope

- Per-source primary-baseline tracking (current code uses one
  `primaryRTT` per `dst` regardless of which auxiliary source the
  candidate represents). Multi-source-set deployments may eventually
  want per (source, dst) primary baselines; not needed for the current
  one-aux-set design.
- Adaptive thresholds. The 10 % default is a single global knob; future
  phases may explore per-peer or per-tier (DERP-fallback / cellular /
  ethernet) thresholds.
- Backporting the gate to forced auxiliary mode. `FORCE_DATA_SOURCE`
  remains an explicit operator override and is intentionally untouched
  by Phase 20.
