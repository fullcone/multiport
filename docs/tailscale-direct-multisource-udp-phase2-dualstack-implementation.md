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
- Local tree: `C:\other_project\fullcone`
- Branch: `phase1-srcsel-source-metadata`
- PR: `https://github.com/fullcone/multiport/pull/1`
- Phase 2 commit: pending

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
  - Adds `sendSourcePathDiscoPing`, which creates an auxiliary disco Ping with
    a TxID that is not inserted into `endpoint.sentPing`.

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

## Behavior Guarantees

Phase 2 intentionally does not make source selection decisions. It only sends
isolated discovery probes and records the matching Pong path.

Safety invariants:

- Auxiliary probe TxIDs are never inserted into `endpoint.sentPing`.
- Auxiliary Pong responses are consumed by `sourcePathProbeManager` before
  `endpoint.handlePongConnLocked`.
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

## Codex Review

Pending. The review request should focus on whether auxiliary IPv4/IPv6 disco
probe TxIDs can mutate endpoint state, peer endpoint maps, or preferred-address
selection.
