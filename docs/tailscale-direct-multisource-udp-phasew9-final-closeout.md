# Tailscale Direct Multisource UDP Phase W9 Final Closeout

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase-w9-final-closeout`

PR chain stacked on `main`:

```
main
 └── PR #2  phase20-srcsel-primary-baseline    (Phase 20)
      └── PR #3  phase-w4-windows-runtime-evidence    (W4)
           └── PR #4  phase-w5-windows-risk-checklist (W5)
                └── PR #5  phase-w9-final-closeout    (this)
```

This phase is documentation only. It records the closeout state for the
Windows client port plan (`windows-client-port-plan-v01.md`), summarizes
what each W-phase delivered, names the deferrals that remain after the
PR #2 → PR #5 stack lands, and points to the next milestone (W7 real-
network bilateral validation).

## Phase Status Summary

| Phase | Status        | Where it lives                                |
|-------|---------------|-----------------------------------------------|
| W0    | Done          | PR #1 docs `phaseW0/W1` (combined)            |
| W1    | Done          | PR #1 commit `8c1b5954a` + `66edcd86f`        |
| W2    | Done          | PR #1 docs `phasew2-windows-ipv4-runtime-evidence.md` |
| W3    | **Deferred**  | PR #1 docs `phasew3-windows-ipv6-deferred.md` (this dev host has IPv6 loopback intercepted below the WFP layer; needs a different machine or a real-network IPv6 link) |
| W4    | Done          | PR #3 docs `phasew4-windows-runtime-evidence.md` |
| W5    | Done          | PR #4 docs `phasew5-windows-risk-checklist.md`  |
| W6    | Done          | PR #1 docs `phasew6-windows-service-mode-evidence.md` |
| W7    | **Deferred**  | not yet documented; needs a real Linux remote and a Windows client across an actual Internet path |
| W8    | Done          | this stack — Codex review fixes landed in `794c1bfaf` (PR #2 P1) and `0d20a4c30` (PR #3 P2) |
| W9    | This document | PR #5                                         |

The "**Deferred**" rows are not failures of W4/W5 — they are explicit
environment dependencies that the dev host cannot satisfy.

## Behavior Now Guaranteed Across PR #1 + Stack

This consolidates Phase 16's "Behavior Now Guaranteed In Scope", Phase 19's
amendment for the bidirectional auxiliary data plane, Phase 20's
primary-baseline gate, and the W-series Windows port.

### Core srcsel data plane (PR #1 + Phase 19, Linux server)

- `Conn.receiveIPWithSource` accepts WireGuard frames on auxiliary
  sockets and routes them through the standard `lazyEndpoint` /
  `peerMap` path. The Phase 19 fix removed the unconditional drop;
  `magicsock_srcsel_aux_wireguard_rx` counts admitted frames.
- `Conn.endpoint.send` may steer real WireGuard data through an
  auxiliary source socket only when (a) source selection is enabled
  via `TS_EXPERIMENTAL_SRCSEL_ENABLE`, (b) `dst.isDirect()`, and
  (c) the candidate satisfies the scorer's TTL, sample-count, and
  primary-baseline gates.
- The probe manager retains samples for at most
  `sourcePathSampleTTL = 60s` and rejects auxiliary candidates with
  fewer than `sourcePathMinSamplesForUse = 3` fresh samples. Samples
  are evicted by both TTL pruning on every accepted Pong and by the
  `sourcePathProbeHistoryLimit = 100000` memory hard cap.
- A real-data send failure on an auxiliary source clears that
  (dst, source) pair's samples via `noteSourcePathSendFailure` so
  the next selection cycle waits for fresh probe evidence.

### Primary-baseline gate (PR #2, Phase 20)

- `bestCandidateLocked` rejects an auxiliary candidate whose mean
  latency does not satisfy `mean < primaryRTT × (1 - threshold/100)`,
  where `primaryRTT` comes from the endpoint's
  `primaryRTTForLocked(dst)` helper (per-address pong history,
  fallback to `bestAddr.latency`).
- Default threshold 10 % via `sourcePathAuxBeatThresholdPercent`.
  Override via `TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT`:
  `< 0` disables the gate, `0` keeps the default, `> 0` clamps to
  `[1, 100]`.
- `magicsock_srcsel_primary_beat_rejected` counts how often the gate
  fires.

### Windows client (PR #3 + PR #4, W-series)

- `sourcepath_supported.go` carries the same source-aware send /
  receive plumbing as Linux; `linux || windows` build tag.
- IPv4 dual-node forced + automatic auxiliary tests pass on Windows
  Server 2025 (Go 1.26.2 windows/amd64) — verified by `magicsock.test`
  binding clusters of four loopback UDP sockets in the documented
  order (primary v6, primary v4, aux v4, aux v6) and
  `recordingPacketListener` capturing the WireGuard write from
  auxiliary local to direct peer.
- IPv6 dual-node tests SKIP via the W1
  `ipv6LoopbackUDPRoundtripProbe` on this dev host because IPv6
  loopback delivery is intercepted below the WFP layer; the skip is
  capability-based, not platform-blacklist, and machines with healthy
  IPv6 loopback run the same tests.
- The W5 risk checklist confirmed: stock Defender Firewall app-scoped
  rules do not constrain UDP local port; Server SKU does not advertise
  Modern Standby; IOCP wallclock is comparable to (and lower-jitter
  than) WSL Linux at this scale; multi-NIC enumeration shows aux
  shares primary's listenPacket / rebind path; `sourcePathBindError`
  already tolerates single-stack failure for v6-only environments;
  Defender RTP-off period saw no block events.

### Test posture

- `go test ./wgengine/magicsock -count=1 -timeout 300s` passes in
  ~10.5 s on WSL Linux Go 1.26.2 and ~2.2 s on Windows native Go
  1.26.2 (W4 figures).
- Codex automated review on the stack: PR #2 had one P1 (env-knob
  tests on `!linux && !windows`), PR #3 had one P2 (W4 doc bind-order
  attribution), both fixed in `794c1bfaf` and `0d20a4c30`. PR #4
  trigger had not yet drawn a Codex response at the time of this
  closeout draft.

## Known Deferrals After This Stack Lands

These are not gaps in PR #2–#5; they are explicit environment
dependencies recorded for the next milestone.

1. **W3 — Windows IPv6 real-environment validation.** This dev host
   has IPv6 loopback intercepted below the WFP layer. The probe
   skip keeps tests green on healthy machines without falsely
   passing on broken ones; an actual run on a clean Windows VM or a
   real IPv6 access link is still required before claiming Windows
   IPv6 srcsel parity with Linux.

2. **W7 — Windows ↔ Linux bilateral real-network validation.**
   Required matrix from `windows-client-port-plan-v01.md` § 4.4
   includes:
   - Public both ends, no NAT
   - Single-side hard NAT (Windows behind home NAT)
   - Both-side NAT
   - Wi-Fi / 4G switching on the Windows side
   - Modern Standby suspend / resume on a Windows client SKU
   - Antivirus / EDR enabled environment
   At least four of the six rows must complete with both ends
   exercising real auxiliary source-aware sends and reverse-path
   delivery (the Phase 19 `magicsock_srcsel_aux_wireguard_rx`
   counter is the basis for confirming the reverse path is not
   silently black-holed). This needs a real Linux remote — not WSL
   loopback — and the Windows client laptop categories the matrix
   names.

3. **macOS / BSD source-aware send.** Out of scope for this PR
   chain; still gated by the `sourcepath_default.go` stub. A future
   PR can lift the build tag once a similar verification pass on a
   macOS host is performed.

4. **Per-NIC auxiliary source selection.** The Asymmetric ECMP plan
   from the original v04 design names this as the second half of
   the multi-source story (separate aux sockets bound to specific
   NICs). Out of scope here; needs its own design PR before
   implementation.

5. **Adaptive primary-baseline threshold.** Phase 20 ships a single
   global percent. Real W7 telemetry may motivate per-peer or
   per-tier defaults.

6. **Production rollout playbook.** PR #2–#5 deliver the
   experimental knobs and the verification matrix; staged rollout
   strategy (which tailnets, which percentage, what trigger to roll
   back) belongs in a follow-on operational PR.

## Out Of Scope For W9

W9 is documentation only. Real environment evidence collection,
bilateral W7 runs, macOS / BSD ports, and rollout planning are all
captured in the **Known Deferrals** section above and are explicitly
out of scope for this closeout.

## Recommended Next Step

Once PR #2 → PR #5 merge, the next milestone is W7 real-network
bilateral validation against a real Linux remote. A new branch
`phase-w7-real-network-validation` would add:

- A short helper or operational runbook for setting up the Linux
  remote and the Windows client.
- Per-row evidence captures (logs, metric snapshots, brief packet
  captures) for at least four of the six § 4.4 matrix rows.
- A short doc-only PR mirroring W4 / W5's evidence pattern.

W7 does not need any code change in `wgengine/magicsock`; the
runtime contract has already been validated end-to-end on Linux
(PR #1 + Phase 19) and locally on Windows (W2 / W4 / W5 / W6).
