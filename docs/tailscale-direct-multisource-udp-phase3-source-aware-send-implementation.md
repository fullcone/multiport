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
- Adds explicit front-loaded assertions that the source-selection boolean knob,
  auxiliary socket-count integer knob, source-path auxiliary socket count, and
  IPv4/IPv6 forcing policy are active before validating selected sockets.
- Adds `TestSendUDPBatchFromSourceAuxDualStackLoopback`, a Linux-only loopback
  test that binds real IPv4 and IPv6 UDP destination sockets plus matching
  auxiliary sockets, sends through `sendUDPBatchFromSource`, and verifies the
  received packet source port is the selected auxiliary socket port.
- Adds `TestSourcePathWriteWireGuardBatchToRejectsStaleAuxSource`, proving
  stale auxiliary source metadata is rejected after a generation mismatch.
- Adds `TestSendUDPBatchFromSourceAuxErrorDoesNotRebind`, proving IPv4 and
  IPv6 auxiliary-source send errors return locally and leave the primary
  rebind throttle state unchanged.
- Adds `TestSourcePathForcedAuxDualNodeRuntime`, a Linux-only dual-node runtime
  test using real `magicStack` nodes, TUN pings, and recorded `PacketConn`
  writes. It proves forced auxiliary WireGuard data egress for IPv4 and IPv6,
  primary fallback after injected auxiliary `EPERM`, and no `lastErrRebind`
  pollution from successful or failed auxiliary sends.
- The runtime test injects loopback IPv6 endpoints directly into the test
  netmap because the local WSL netcheck path does not advertise IPv6 STUN
  endpoints. The actual proof still uses bound `::1` UDP sockets and recorded
  real packet writes.

`envknob/envknob.go`

- Updates `Setenv` to refresh registered integer knobs in `regInt`.
- This is required for tests that mutate an already-registered integer knob
  after package initialization, such as
  `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS`.

`envknob/envknob_test.go`

- Adds `TestSetenvUpdatesRegisteredInt` to prove `RegisterInt` observers see
  later `envknob.Setenv` updates and clear operations.

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

Additional runtime/unit validation on 2026-04-29:

```powershell
gofmt -w envknob\envknob.go envknob\envknob_test.go wgengine\magicsock\sourcepath_linux_test.go
go test ./envknob
wsl -d Ubuntu-24.04 -- bash -lc 'cd /mnt/c/other_project/fullcone && go test ./wgengine/magicsock -run TestSourcePathDataSendSourceForcedAuxDualStack -count=1 -v'
go test ./wgengine/magicsock ./envknob
wsl -d Ubuntu-24.04 -- bash -lc 'cd /mnt/c/other_project/fullcone && go test ./wgengine/magicsock ./envknob'
```

Results:

- `go test ./envknob`: passed.
- WSL Ubuntu-24.04 targeted
  `TestSourcePathDataSendSourceForcedAuxDualStack`: passed.
- Windows `go test ./wgengine/magicsock ./envknob`: passed.
- WSL Ubuntu-24.04 `go test ./wgengine/magicsock ./envknob`: passed.

The first WSL Linux targeted run exposed that `envknob.Setenv` refreshed
registered string, boolean, optional-boolean, and duration knobs, but not
registered integer knobs. Because `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS` is a
registered integer knob, the Linux dual-stack test saw an auxiliary socket
count of `0` after setting the knob to `1`. The `envknob.Setenv` `regInt` fix
and `TestSetenvUpdatesRegisteredInt` cover this support bug.

Additional Linux loopback egress validation on 2026-04-29:

```powershell
wsl -d Ubuntu-24.04 -- bash -lc 'cd /mnt/c/other_project/fullcone && go test ./wgengine/magicsock -run "Test(SendUDPBatchFromSourceAuxDualStackLoopback|SourcePathWriteWireGuardBatchToRejectsStaleAuxSource|SourcePathDataSendSourceForcedAuxDualStack)" -count=1 -v'
wsl -d Ubuntu-24.04 -- bash -lc 'cd /mnt/c/other_project/fullcone && go test ./wgengine/magicsock ./envknob'
go test ./wgengine/magicsock ./envknob
```

Results:

- `TestSendUDPBatchFromSourceAuxDualStackLoopback`: passed for IPv4 and IPv6.
  The destination sockets received the payload from the selected auxiliary UDP
  socket ports, proving the source-aware Linux data-send helper can use the
  auxiliary socket for actual local UDP egress.
- `TestSourcePathWriteWireGuardBatchToRejectsStaleAuxSource`: passed, proving
  stale auxiliary source metadata is rejected instead of sending through an old
  socket identity.
- `TestSourcePathDataSendSourceForcedAuxDualStack`: passed.
- WSL Ubuntu-24.04 `go test ./wgengine/magicsock ./envknob`: passed.
- Windows `go test ./wgengine/magicsock ./envknob`: passed.

Additional auxiliary-send error isolation validation on 2026-04-29:

```powershell
wsl -d Ubuntu-24.04 -- bash -lc 'cd /mnt/c/other_project/fullcone && go test ./wgengine/magicsock -run "Test(SendUDPBatchFromSourceAuxErrorDoesNotRebind|SendUDPBatchFromSourceAuxDualStackLoopback|SourcePathWriteWireGuardBatchToRejectsStaleAuxSource|SourcePathDataSendSourceForcedAuxDualStack)" -count=1 -v'
```

Results:

- `TestSendUDPBatchFromSourceAuxErrorDoesNotRebind`: passed for IPv4 and IPv6.
  A failing auxiliary packet conn returned `EPERM` through
  `sendUDPBatchFromSource`, and `lastErrRebind` stayed at its pre-call
  sentinel value. This directly covers the Phase 3 property that auxiliary-only
  send errors do not invoke primary socket rebind handling.

Linux dual-node runtime validation on 2026-04-29:

```powershell
wsl -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run TestSourcePathForcedAuxDualNodeRuntime -count=1 -v'
wsl -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run "Test(SourcePath|SendUDPBatchFromSourceAux)" -count=1'
```

Results:

- `TestSourcePathForcedAuxDualNodeRuntime`: passed for IPv4 and IPv6.
- IPv4 runtime proof observed forced auxiliary, primary, and peer loopback
  paths in the same real direct peer session. The test recorded a WireGuard UDP
  packet leaving the IPv4 auxiliary socket, then injected `EPERM` on that
  auxiliary source and observed fallback to the IPv4 primary socket.
- IPv6 runtime proof observed forced auxiliary, primary, and peer `::1`
  loopback paths in the same real direct peer session. The test recorded a
  WireGuard UDP packet leaving the IPv6 auxiliary socket, then injected `EPERM`
  on that auxiliary source and observed fallback to the IPv6 primary socket.
- For both families, `lastErrRebind` stayed at the sentinel value after the
  successful auxiliary send and after the auxiliary-failure primary fallback.
  This proves forced auxiliary send failure does not pollute primary rebind
  state.
- The related Linux source-path test subset also passed after adding the
  runtime test.

Codex review follow-up fix validation on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\sourcepath_linux_test.go
wsl -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run TestSourcePathForcedAuxDualNodeRuntime -count=1 -v'
wsl -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock ./envknob -count=1'
go test ./wgengine/magicsock ./envknob -count=1
```

Results:

- `TestSourcePathForcedAuxDualNodeRuntime`: passed again for IPv4 and IPv6.
  The rerun still recorded a WireGuard UDP packet from the forced auxiliary
  source, then recorded the injected auxiliary `EPERM` followed by primary
  fallback for each address family.
- The runtime test now registers cleanup for the DERP/STUN fixture, both
  `magicStack` instances, and the `meshStacks` goroutine before traffic starts.
  The verbose rerun showed both nodes closing and STUN server shutdown after
  each subtest.
- IPv6 primary endpoint availability is checked before test netmap endpoint
  injection. A host without usable `udp6` primary bind now skips the IPv6
  runtime subtest before touching `pconn6.LocalAddr()` as an endpoint.
- WSL Ubuntu-24.04 `go test ./wgengine/magicsock ./envknob -count=1`: passed.
- Windows `go test ./wgengine/magicsock ./envknob -count=1`: passed.

External packet-capture validation remains optional if out-of-process evidence
is needed:

- packet capture proving IPv4 WireGuard data can leave the IPv4 auxiliary
  socket under `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` or `aux4` in a
  real peer session;
- packet capture proving IPv6 WireGuard data can leave the IPv6 auxiliary
  socket under `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` or `aux6` in a
  real peer session;
- peer decrypt/response confirmation for both families;
- primary fallback confirmation after forced auxiliary send failure;
- optional runtime log/counter confirmation of the same no-primary-rebind
  property already covered by `TestSendUDPBatchFromSourceAuxErrorDoesNotRebind`;
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

- Two older Phase 2 inline review threads were rechecked against the current
  source and marked resolved after verifying the corresponding fixes:
  - `PRRT_kwDOSPBZuM5-NLPH`: auxiliary probes now use the source-path probe
    request path instead of advertising auxiliary sockets as ordinary peer
    endpoints.
  - `PRRT_kwDOSPBZuM5-NLPK`: unsatisfied source-path probe TxIDs are pruned and
    expired Pongs are consumed without producing samples.
- Both findings were addressed by the Phase 2 feedback fix and recorded in
  `docs/tailscale-direct-multisource-udp-phase2-dualstack-implementation.md`.
- The follow-up Codex review for that fix reported no major issues:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4337667402`
- No new actionable Phase 3 review finding is present at this checkpoint.

Phase 3 documentation follow-up review checkpoint on 2026-04-29:

- Later PR refresh observed Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4338125251`
- Codex reported no major issues for the Phase 3 documentation update.
- The two older Phase 2 inline threads are now resolved after the startup
  source check described above.

Phase 3 Linux runtime review checkpoint on 2026-04-29:

- Codex reviewed commit `8f62f51a0` and found two P2 issues in
  `TestSourcePathForcedAuxDualNodeRuntime`:
  - the dual-node subtests created DERP/STUN, mesh, and magicStack resources
    without per-subtest cleanup;
  - the IPv6 path read `pconn6.LocalAddr()` during endpoint injection before
    the IPv6 skip gate, which would fatal instead of skip on hosts without a
    usable IPv6 UDP primary socket.
- The follow-up patch registers the DERP/STUN cleanup closure, both
  `magicStack.Close` calls, and `meshStacks` cleanup with `t.Cleanup` so the
  mesh goroutine exits before stacks and DERP/STUN are closed.
- The follow-up patch computes both primary endpoints before netmap injection
  and skips the IPv6 subtest immediately if either node lacks a valid IPv6
  primary UDP endpoint. IPv4 primary absence remains a test failure.

Phase 3 forced-aux runtime final verification on 2026-04-29:

- Codex reviewed follow-up commit `1713f3e390bfde01a58bb48923c3d20c4cfe9d4e`
  and reported no major issues:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4340622840`
- The latest Linux dual-node runtime proof was rerun under WSL Ubuntu-24.04:
  `go test ./wgengine/magicsock -run TestSourcePathForcedAuxDualNodeRuntime -count=1 -v`.
- IPv4 proof line:
  `forced aux runtime path: aux=127.0.0.1:35699 primary=127.0.0.1:46661 peer=127.0.0.1:41424`.
  The same run logged the injected auxiliary write failure and primary retry:
  `srcsel: data send from source 1 to 127.0.0.1:41424 failed, retrying primary: write: operation not permitted`.
- IPv6 proof line:
  `forced aux runtime path: aux=[::1]:53258 primary=[::1]:54716 peer=[::1]:56119`.
  The same run logged the injected auxiliary write failure and primary retry:
  `srcsel: data send from source 2 to [::1]:56119 failed, retrying primary: write: operation not permitted`.
- The test assertions require a WireGuard UDP write from the forced auxiliary
  socket to the direct peer endpoint, then require primary fallback after the
  injected auxiliary failure. Both the successful forced auxiliary send and the
  fallback path keep `lastErrRebind` equal to the sentinel value.
- Linux package regression also passed after the runtime proof:
  `go test ./wgengine/magicsock ./envknob -count=1`.

## Current Status

Phase 3 source-aware data-send plumbing is implemented for IPv4 and IPv6 behind
a manual debug forcing gate. The latest Codex runtime-test review issues have
been fixed and the follow-up review reported no major issues. The Linux
dual-stack source-selection unit path and local loopback egress path now pass
under WSL Ubuntu-24.04, including IPv4 and IPv6 auxiliary source-port
verification, stale-generation rejection, auxiliary-send error isolation from
primary rebind handling, and real dual-node forced auxiliary WireGuard data
egress with primary fallback for both families. It is not yet an automatic
path-selection feature, and external packet-capture validation remains optional
future evidence.
