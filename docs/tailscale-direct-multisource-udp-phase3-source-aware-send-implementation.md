# Tailscale Direct Multisource UDP Phase 3 Source-Aware Send Implementation

Date: 2026-04-29

This document records the Phase 3 implementation against
`tailscale_direct_multisource_udp_final_implementation_v02.md`.

## Scope

Phase 3 adds source-aware send primitives and wires them into the direct UDP
WireGuard data-send path behind a manual debug gate. It does not enable an
automatic scorer yet.

Implemented for both IP families:

- IPv4 auxiliary data send uses the bound Linux auxiliary IPv4 socket.
- IPv6 auxiliary data send uses the bound Linux auxiliary IPv6 socket.
- Family-specific forcing can restrict testing to only IPv4 or only IPv6.

Out of scope for this phase:

- automatic source scoring
- auxiliary receive path for WireGuard data packets
- changing DERP behavior
- changing peer endpoint maps to include local source socket identity
- changing `lazyEndpoint` cookie or handshake send behavior

## Control Gate

The existing source-selection experiment remains disabled unless:

```text
TS_EXPERIMENTAL_SRCSEL_ENABLE=true
TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
```

Phase 3 adds one data-send forcing knob:

```text
TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE
```

Accepted values:

- `aux`: force matching auxiliary sockets for both IPv4 and IPv6 direct UDP
  data sends.
- `aux4`, `ipv4`, `v4`: force only IPv4 direct UDP data sends through the IPv4
  auxiliary socket; IPv6 stays primary.
- `aux6`, `ipv6`, `v6`: force only IPv6 direct UDP data sends through the IPv6
  auxiliary socket; IPv4 stays primary.
- unset or any other value: keep all data sends on the primary socket.

This keeps Phase 3 manually testable without changing normal production path
selection.

## Code Changes

`wgengine/magicsock/sourcepath_linux.go`

- Adds `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE`.
- Adds `sourcePathDataSendSource`, which returns the matching auxiliary
  `sourceRxMeta` only when:
  - source selection is enabled,
  - at least one auxiliary socket is configured,
  - the destination is a direct UDP endpoint,
  - the forcing knob allows the destination IP family, and
  - the matching auxiliary socket is currently bound.
- Adds `sourcePathWriteWireGuardBatchTo`, which sends a WireGuard batch through
  the requested auxiliary `RebindingUDPConn` only when the requested source
  metadata still matches the current bound socket.

`wgengine/magicsock/sourcepath_default.go`

- Keeps non-Linux platforms on primary source selection.
- Returns `errSourcePathUnavailable` for source-aware WireGuard batch writes.

`wgengine/magicsock/magicsock.go`

- Adds `sendUDPBatchFromSource`.
- Adds `sendUDPStdFromSource`.
- Adds `sendAddrFromSource`.
- Adds `sendDiscoMessageFromSource`.
- Existing `sendDiscoMessage` now delegates to the primary source, preserving
  current caller behavior.
- Auxiliary send helpers never call `maybeRebindOnError`; only the existing
  primary send path may trigger primary socket rebind handling.

`wgengine/magicsock/endpoint.go`

- The direct UDP branch in `endpoint.send` asks `sourcePathDataSendSource` for a
  source before calling `sendUDPBatchFromSource`.
- If auxiliary sending fails, it retries the same batch through the primary
  socket.
- A bad endpoint is recorded only when the primary send attempt fails with a
  bad-endpoint error. An auxiliary-only failure cannot mark the peer endpoint
  bad.

`wgengine/magicsock/sourcepath_linux_test.go`

- Adds a Linux-only dual-stack source-selection test for:
  - `aux` selecting IPv4 and IPv6 auxiliary sockets,
  - `aux4` selecting only IPv4 auxiliary sends,
  - `aux6` selecting only IPv6 auxiliary sends,
  - unset forcing preserving primary sends for both families.

## Safety Properties

Normal behavior is unchanged when the new forcing knob is unset.

`lazyEndpoint` stays on the existing `pconn4` / `pconn6` send path in
`Conn.Send`; Phase 3 does not route that path through source-aware helpers.

Auxiliary WireGuard data receive remains intentionally unsupported. Phase 3
only proves an auxiliary egress path under manual forcing, and the Phase 2 disco
probe isolation still prevents auxiliary probe packets from advertising a data
endpoint candidate.

Auxiliary send errors do not call `maybeRebindOnError`. This prevents a failed
experimental source socket send from causing primary socket rebind behavior.

Auxiliary send errors also do not directly call `endpoint.noteBadEndpoint`. The
endpoint can be marked bad only after fallback to the primary send path fails
with a bad-endpoint error.

## Validation

Completed locally on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\endpoint.go wgengine\magicsock\magicsock.go wgengine\magicsock\sourcepath_linux.go wgengine\magicsock\sourcepath_default.go wgengine\magicsock\sourcepath_linux_test.go
go test ./wgengine/magicsock
$env:GOOS='linux'; $env:GOARCH='amd64'; go test -c -o "$env:TEMP\magicsock_linux_phase3.test" ./wgengine/magicsock
```

Results:

- `go test ./wgengine/magicsock`: passed.
- `GOOS=linux GOARCH=amd64 go test -c ./wgengine/magicsock`: passed.

Runtime validation still required in a Linux dual-node testbed:

- packet capture proving IPv4 WireGuard data can leave the IPv4 auxiliary
  socket under `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` or `aux4`;
- packet capture proving IPv6 WireGuard data can leave the IPv6 auxiliary
  socket under `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` or `aux6`;
- peer decrypt/response confirmation for both families;
- primary fallback confirmation after forced auxiliary send failure;
- confirmation that primary rebind counters/logs do not change because of an
  auxiliary-only send error;
- confirmation that `lazyEndpoint` remains on the original primary send path.

## Codex Review

Requested on PR #1:

- Phase 3 code review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4338044687`
- Requested focus:
  - IPv4 and IPv6 forced auxiliary data sends use only the matching auxiliary
    socket.
  - Auxiliary send errors fall back to primary without calling
    `maybeRebindOnError` or marking the endpoint bad unless the primary retry
    fails.
  - `lazyEndpoint` remains on the original `pconn4` / `pconn6` send path.
  - `sendDiscoMessage` still defaults to primary and the new source-aware
    helper does not affect current callers.
  - `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE` does not change normal behavior
    when unset.
- Initial local polling after the request did not observe a response.
- Later PR refresh observed Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4338083749`
- Phase 3 Codex response result: no major issues found for the source-aware
  data send implementation.

PR startup check on 2026-04-29:

- Two older Phase 2 inline review threads still appear unresolved in the GitHub
  UI:
  - `PRRT_kwDOSPBZuM5-NLPH`: auxiliary probes must not advertise non-primary
    endpoints.
  - `PRRT_kwDOSPBZuM5-NLPK`: unsatisfied source-path probe TxIDs need expiry.
- Both findings were addressed by the Phase 2 feedback fix and recorded in
  `docs/tailscale-direct-multisource-udp-phase2-dualstack-implementation.md`.
- The follow-up Codex review for that fix reported no major issues:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337667402`
- No new actionable Phase 3 review finding is present at this checkpoint.

## Current Status

Phase 3 source-aware data-send plumbing is implemented for IPv4 and IPv6 behind
a manual debug forcing gate. Codex review found no major Phase 3 code issues.
It is not yet an automatic path-selection feature.
