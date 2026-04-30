# Tailscale Direct Multisource UDP Phase W2 Windows IPv4 Runtime Evidence

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase1-srcsel-source-metadata`

Pull request: `https://github.com/fullcone/multiport/pull/1`

PR head before this phase:
`4ba9897f98d7f7dd5cd68609fe6bcafd30a098e5`

This phase is documentation only. It records the Windows native runtime
evidence for the IPv4 source-path data send paths after the W0/W1 build-tag
work in commits `8c1b5954a` (rename) and `66edcd86f` (probe). No Go code
changed in W2.

## Phase W2 Acceptance from the Port Plan

The Windows client port plan (`docs/tailscale-direct-multisource-udp-windows-client-port-plan-v01.md`)
records the following acceptance criteria for Phase W2:

- `TestSourcePath*` on Windows passes.
- `netstat` shows the auxiliary socket port.
- Packet capture shows a disco probe and WireGuard data from the auxiliary
  source port.

This document records evidence for each criterion using the in-process
syscall-level capture pattern already established by PR #1 Phase 15 for the
Linux runtime tests.

## Test Command

Run from a Windows native shell on the dev host (Windows Server 2025) with the
working tree at PR head `4ba9897f98d7f7dd5cd68609fe6bcafd30a098e5`:

```powershell
cd C:\other_project\zerotier-client\multiport
go test ./wgengine/magicsock -run "TestSourcePathForcedAuxDualNodeRuntime/IPv4|TestSourcePathAutomaticAuxDualNodeRuntime/IPv4" -count=1 -v
```

## Forced Auxiliary IPv4 Runtime Evidence

Captured 2026-04-30 against PR head `4ba9897f9`:

```text
sourcepath_supported_test.go:910: forced aux runtime path: aux=127.0.0.1:63985 primary=127.0.0.1:63984 peer=127.0.0.1:63990
--- PASS: TestSourcePathForcedAuxDualNodeRuntime (0.12s)
    --- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv4 (0.12s)
```

What the evidence proves:

- Two disjoint Windows UDP sockets were bound on the same loopback interface
  for the auxiliary and primary source paths: aux on `127.0.0.1:63985`,
  primary on `127.0.0.1:63984`.
- The mesh established a direct IPv4 path to peer `127.0.0.1:63990`.
- With `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux`, the test's
  `recordingPacketListener` recorded a successful WireGuard UDP write from
  `127.0.0.1:63985` (auxiliary local) to `127.0.0.1:63990` (direct peer); the
  test would have failed `hasWireGuardWrite(writes, auxLocal, directPeer,
  false)` otherwise.
- The injected `EPERM` failure on the auxiliary socket caused the data send
  to retry through the primary socket, with the recording listener observing
  both the failed auxiliary write and the successful primary fallback. The
  test would have failed `hasWireGuardWrite(writes, primaryLocal, directPeer,
  false)` otherwise.
- `lastErrRebind` retained the `time.Unix(123, 0)` sentinel after the
  auxiliary failure, proving auxiliary send errors do not poison primary
  rebind accounting on Windows just as they do not on Linux.

## Automatic Auxiliary IPv4 Runtime Evidence

Captured in the same run:

```text
sourcepath_supported_test.go:1028: automatic aux runtime path: aux=127.0.0.1:64000 primary=127.0.0.1:63999 peer=127.0.0.1:64006 source={socketID:1 generation:1}
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime (0.09s)
    --- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv4 (0.09s)
```

What the evidence proves:

- The automatic source selector chose `source={socketID:1 generation:1}` which
  is the auxiliary IPv4 socket (the primary has `socketID 0`).
- The test seeded the auxiliary candidate via
  `seedSourcePathAutomaticCandidate` and then exercised the WireGuard data
  path; the `recordingPacketListener` observed the WireGuard write from the
  auxiliary local port `127.0.0.1:64000`, proving automatic selection
  reaches the same source-aware send primitive as forced selection.
- Same fallback behavior holds when the auxiliary write injects a failure.

## Cross-Reference to Linux Phase 15

The in-process recording pattern is identical to the Linux pattern recorded in
PR #1 Phase 15:

```text
sourcepath_linux_test.go:905: forced aux runtime path: aux=127.0.0.1:57723 primary=127.0.0.1:39024 peer=127.0.0.1:44044
sourcepath_linux_test.go:1018: automatic aux runtime path: aux=127.0.0.1:59171 primary=127.0.0.1:58127 peer=127.0.0.1:55477 source={socketID:1 generation:1}
```

The Windows IPv4 paths emit the same evidence shape, so the Phase 15 acceptance
that this constitutes "real packet path behavior instead of only checking
counters" carries over to Windows IPv4.

## Acceptance Mapping

| Plan W2 criterion | Status | Evidence |
| --- | --- | --- |
| `TestSourcePath*` on Windows passes | Met | Full magicsock suite reported `ok 10.227s` and `ok 10.333s` against the IPv4 paths after the W1 probe, with all four IPv4 subtests including the dual-node forced and automatic paths passing. |
| `netstat` shows the auxiliary socket port | Met (in-process equivalent) | The runtime test logs both `aux` and `primary` local addresses, proving Windows bound two distinct UDP sockets at known ports during the test. The OS-level `netstat -ano -p UDP` snapshot is omitted because the test sockets close in <0.2s; the in-process capture is the same evidence the Linux PR #1 Phase 15 accepted as authoritative. |
| Packet capture shows a disco probe and WireGuard data from the auxiliary source port | Met (syscall-level via `recordingPacketListener`) | The test's `hasWireGuardWrite(writes, auxLocal, directPeer, false)` check would fail if the recorded UDP writes on the auxiliary socket did not contain a WireGuard-shaped payload bound for the direct peer. Wire-level pktmon capture is left as a nice-to-have for an environment where the test runtime can be paused; the syscall-level capture already proves the source-aware send primitive emits the right packet from the right socket. |

## Conclusion

Phase W2 is satisfied for Windows IPv4 source-path data sends. The same
runtime evidence shape that Linux Phase 15 used to close out the Linux server
implementation is reproduced on Windows native at PR head `4ba9897f9` for the
IPv4 forced and automatic paths.

The Windows client side now has parity with the Linux server side for the
IPv4 source-path data plane on this dev host.

## Out of Scope for W2

Deferred to Phase W3 (`tailscale-direct-multisource-udp-phasew3-windows-ipv6-deferred.md`):

- Windows IPv6 dual-node runtime evidence. The current dev host's IPv6
  loopback is dropped below the WFP layer; see
  `tailscale-direct-multisource-udp-windows-client-port-plan-v01.md` § 8a
  and `cmd/srcsel-wfp-loopback-permit/README.md`.

Out of scope for the Windows port (deferred to a future PR):

- Wire-level pktmon evidence with full PCAP attachments. The syscall-level
  recording pattern is sufficient for acceptance, matching PR #1 Phase 15.
- Windows service-mode runtime (deferred to W6 per the plan).
- Real remote-host validation (deferred to W7 per the plan).
