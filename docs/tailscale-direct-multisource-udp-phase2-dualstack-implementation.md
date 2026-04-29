# Tailscale Direct Multi-Source UDP Phase 2 Dual-Stack Implementation

This document records the Phase 2 implementation state for later audit.

## Scope

Phase 2 adds gated Linux auxiliary UDP source-path discovery probes for both
IPv4 and IPv6. The auxiliary sockets only carry direct disco probes and do not
become a WireGuard packet receive path.

IPv4 and IPv6 are both in scope:

- IPv4 direct endpoints may be probed through one auxiliary `udp4` socket.
- IPv6 direct endpoints may be probed through one auxiliary `udp6` socket.
- DERP, relay Geneve paths, raw disco, and CLI ping behavior remain on the
  primary path.

## Repository State

- Repository: `https://github.com/fullcone/multiport`
- Local tree: `C:\other_project\zerotier-client\multiport`
- Branch: `phase1-srcsel-source-metadata`
- PR: `https://github.com/fullcone/multiport/pull/1`
- Phase 2 implementation commit: `faed25451`
- Phase 2 Codex-feedback fix commit: `0918a9300`

## Feature Gate

The Linux auxiliary source-path sockets are disabled by default.

To enable Phase 2:

```powershell
$env:TS_EXPERIMENTAL_SRCSEL_ENABLE='1'
$env:TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS='1'
```

With `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1`, magicsock creates one auxiliary
IPv4 socket and one auxiliary IPv6 socket. Each auxiliary socket binds to an
ephemeral local port on every rebind. If either family fails to bind, the
failure is logged and the other family may still operate.

## Implementation

- `wgengine/magicsock/sourcepath.go`
  - Adds `sourceGeneration`, IPv4/IPv6 auxiliary `SourceSocketID` values, and
    common source-path probe bookkeeping.
  - Adds `sourcePathProbeManager`, which owns auxiliary probe TxIDs and samples.
  - Adds timeout pruning for unsatisfied auxiliary probe TxIDs using
    `pingTimeoutDuration`.
  - Adds `sendSourcePathDiscoPing`, which creates an auxiliary
    `disco.SourcePathProbe` with a TxID that is not inserted into
    `endpoint.sentPing`.

- `disco/disco.go`
  - Adds `disco.SourcePathProbe`, a Ping-shaped disco message with a distinct
    message type.
  - The peer replies with a normal Pong, but the receiver does not treat the
    source address as a candidate endpoint.

- `wgengine/magicsock/sourcepath_linux.go`
  - Adds Linux-only auxiliary `udp4` and `udp6` sockets behind the feature gate.
  - Rebinds both auxiliary sockets on the normal magicsock rebind path.
  - Provides auxiliary receive functions for both families.
  - Routes auxiliary writes by address family and source generation.

- `wgengine/magicsock/sourcepath_default.go`
  - Keeps non-Linux platforms as no-op for Phase 2.

- `wgengine/magicsock/magicsock.go`
  - Threads dynamic `sourceRxMeta` through receive handling.
  - Drops WireGuard-looking packets received on auxiliary source sockets.
  - Replies to `disco.SourcePathProbe` without mutating peer endpoint maps,
    candidate endpoints, or best-address state.
  - Intercepts auxiliary Pong responses before endpoint Pong handling.
  - Closes auxiliary sockets through `connBind.Close` and `Conn.Close`.

- `wgengine/magicsock/endpoint.go`
  - Schedules auxiliary probes only for non-CLI direct endpoints.
  - Keeps primary probe TxIDs in `endpoint.sentPing`.
  - Keeps auxiliary probe TxIDs isolated in `sourcePathProbeManager`.

- `wgengine/magicsock/sourcepath_test.go`
  - Covers primary vs auxiliary source metadata.
  - Covers matching auxiliary Pong consumption.
  - Covers rejection of primary and stale-generation Pong handling.
  - Covers pruning of expired pending auxiliary probe TxIDs.
  - Covers consuming expired auxiliary Pong responses without recording samples.

- `disco/disco_test.go`
  - Covers marshal and parse behavior for `disco.SourcePathProbe`.

## Behavior Guarantees

Phase 2 intentionally does not make source selection decisions. It only sends
isolated discovery probes and records the matching Pong path.

Safety invariants:

- Auxiliary IPv4 probes use the auxiliary `udp4` socket.
- Auxiliary IPv6 probes use the auxiliary `udp6` socket.
- Auxiliary probe TxIDs are never inserted into `endpoint.sentPing`.
- Auxiliary probes use `disco.SourcePathProbe`, not normal `disco.Ping`, so
  their source port is not advertised as a data-capable endpoint.
- `disco.SourcePathProbe` handling does not call
  `peerMap.setNodeKeyForEpAddr`.
- `disco.SourcePathProbe` handling does not call `ep.addCandidateEndpoint`.
- Auxiliary Pong responses are consumed by `sourcePathProbeManager` before
  `endpoint.handlePongConnLocked`.
- Expired auxiliary Pong responses are consumed and discarded without producing
  a source-path sample.
- Auxiliary Pong responses do not mutate `endpointState.recentPongs`.
- Auxiliary Pong responses do not mutate `peerMap.byEpAddr`.
- Auxiliary Pong responses do not mutate `bestAddr` or `trustBestAddrUntil`.
- Auxiliary receive sockets drop WireGuard-looking packets instead of reporting
  them to wireguard-go.
- Linux raw disco remains tagged as primary metadata.

## Validation

Completed locally on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\sourcepath.go wgengine\magicsock\sourcepath_linux.go wgengine\magicsock\sourcepath_default.go wgengine\magicsock\magicsock.go wgengine\magicsock\endpoint.go wgengine\magicsock\sourcepath_test.go
go test ./wgengine/magicsock
$env:GOOS='linux'; $env:GOARCH='amd64'; go test -c -o "$env:TEMP\magicsock_linux_phase2.test" ./wgengine/magicsock
git diff --check
```

Results:

- `go test ./wgengine/magicsock`: passed.
- `GOOS=linux GOARCH=amd64 go test -c ./wgengine/magicsock`: passed.
- `git diff --check`: passed; Git printed CRLF conversion warnings for existing
  Windows checkout behavior, but no whitespace errors.

Codex-feedback fix validation completed locally on 2026-04-29:

```powershell
gofmt -w disco\disco.go disco\disco_test.go wgengine\magicsock\magicsock.go wgengine\magicsock\sourcepath.go wgengine\magicsock\sourcepath_test.go wgengine\magicsock\peermtu.go
go test ./disco
go test ./wgengine/magicsock
$env:GOOS='linux'; $env:GOARCH='amd64'; go test -c -o "$env:TEMP\magicsock_linux_phase2_feedback.test" ./wgengine/magicsock
git diff --check
```

Results:

- `go test ./disco`: passed.
- `go test ./wgengine/magicsock`: passed.
- `GOOS=linux GOARCH=amd64 go test -c ./wgengine/magicsock`: passed.
- `git diff --check`: passed; Git printed CRLF conversion warnings for existing
  Windows checkout behavior, but no whitespace errors.

## Codex Review

Requested on PR #1:

- Request comment:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337377316`
- Requested focus: whether auxiliary IPv4/IPv6 disco probe TxIDs can mutate
  endpoint state, peer endpoint maps, or preferred-address selection.
- Initial 60 second polls did not observe a response.
- Codex later reported:
  - P1: auxiliary probes used normal `disco.Ping`, so the peer could advertise
    the auxiliary source port as a candidate endpoint even though auxiliary
    receive drops WireGuard data packets.
  - P2: unsatisfied auxiliary probe TxIDs had no timeout-based eviction.
- This fix addresses both findings by adding `disco.SourcePathProbe`, replying
  to it without endpoint-discovery side effects, and pruning expired pending
  auxiliary probe TxIDs.
- Follow-up request after the fix:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337646399`
- Follow-up request focus: verify IPv4 and IPv6 auxiliary probes use
  `disco.SourcePathProbe`, do not add candidate endpoints or best-address
  state, and prune expired pending TxIDs.
- First 60 second follow-up poll: no new Codex response observed.
- Later follow-up poll observed Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337667402`
- Follow-up Codex response result: no major issues found for the Phase 2
  feedback fix.
- Final docs-only review request after recording the follow-up result:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337699549`
- Final docs-only Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337716318`
- Final docs-only Codex response result: no major issues found for the Phase 2
  implementation document update.
- Final follow-up docs-only review request after recording that response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337826730`
- Final follow-up docs-only Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337840584`
- Final follow-up docs-only Codex response result: no major issues found for the
  Phase 2 implementation document update.
- Final response-recording docs-only review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337853101`
- Final response-recording docs-only Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337869452`
- Final response-recording docs-only Codex response result: no major issues
  found for the Phase 2 implementation document update.
- Final response-recording follow-up docs-only review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337912719`
- Final response-recording follow-up docs-only Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337923136`
- Final response-recording follow-up docs-only Codex response result: no major
  issues found for the Phase 2 implementation document update.
