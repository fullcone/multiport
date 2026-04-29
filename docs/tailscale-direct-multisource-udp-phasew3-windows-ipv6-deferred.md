# Tailscale Direct Multisource UDP Phase W3 Windows IPv6 — Deferred

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase1-srcsel-source-metadata`

Pull request: `https://github.com/fullcone/multiport/pull/1`

PR head before this phase:
`4ba9897f98d7f7dd5cd68609fe6bcafd30a098e5`

This phase is documentation only. It records that Phase W3 (Windows IPv6
runtime evidence) is deferred from the current dev host and lists what an
environment that can unblock W3 must look like. No Go code changed in W3.

## Phase W3 Acceptance from the Port Plan

The Windows client port plan
(`docs/tailscale-direct-multisource-udp-windows-client-port-plan-v01.md`)
records this acceptance for Phase W3:

> Dual-stack runtime test 通过.

Concretely, the same forced and automatic auxiliary runtime tests that W2
satisfied for IPv4 must also pass for the IPv6 subtests:

- `TestSendUDPBatchFromSourceAuxDualStackLoopback/ipv6`
- `TestLazyEndpointSendIgnoresForcedAuxDataSourceDualStack/ipv6`
- `TestSourcePathForcedAuxDualNodeRuntime/IPv6`
- `TestSourcePathAutomaticAuxDualNodeRuntime/IPv6`

## Why W3 Cannot Run on the Current Dev Host

Documented in detail in
`docs/tailscale-direct-multisource-udp-windows-client-port-plan-v01.md` § 8a
and `cmd/srcsel-wfp-loopback-permit/README.md`. Summary:

- The current dev host is Windows Server 2025 with WSL2 + sing-box +
  v2rayN + Wintun installed and active.
- IPv4 loopback (`127.0.0.1`) works normally; W2 already validated the
  IPv4 source-path data plane on this host.
- IPv6 loopback (`::1`) is fully dropped at a kernel layer below the
  Windows Filtering Platform. Defender Firewall, Hyper-V firewall,
  Hyper-V Compute Service, and WSL2 were each excluded as the cause; an
  in-house WFP helper that installs max-weight permit filters at all
  eight relevant ALE_AUTH and TRANSPORT V4/V6 layers was verified to
  install correctly via `netsh wfp show filters` and still leaves
  `ping -6 ::1` and TCP/UDP roundtrips on `::1` failing.
- The blocker is therefore at NDIS LWF, TDI, Server 2025 hardening, or an
  EDR network module level. The Windows kernel security model
  intentionally does not let a user-mode permit reach below WFP. No
  user-space workaround exists on this host without changing the upstream
  filter source.

The W1 runtime probe `ipv6LoopbackUDPRoundtripProbe` in
`wgengine/magicsock/sourcepath_supported_test.go` therefore skips the four
IPv6 subtests with an explanatory message instead of failing them.

## Environments That Can Unblock W3

A clean Windows environment without an in-kernel IPv6 loopback blocker. The
probe in `sourcepath_supported_test.go` is a runtime capability check, so
W3 is unblocked automatically the moment a host returns success from the
probe.

Candidate environments, in order of cost:

1. A Windows 10 / 11 client virtual machine without WSL2, without sing-box /
   clash / v2rayN, without an EDR network module. Run the same
   `go test ./wgengine/magicsock` command; the probe should succeed and the
   four IPv6 subtests should run and pass.
2. A clean dedicated Windows test machine (a build agent, a Windows CI
   runner with default firewall) running the full magicsock suite.
3. The current dev host with sing-box and v2rayN stopped during the test.
   The user has explicitly excluded this option for ergonomic reasons; the
   probe-and-skip mechanism keeps the dev workflow unaffected.
4. Real remote-host IPv6 dual-stack validation, where the tests are
   replaced or supplemented by a non-loopback IPv6 path between two real
   machines. This is W7 in the port plan and is the strongest evidence
   form.

## What Counts as W3 Acceptance When the Environment Is Available

When run on an unblocked Windows host:

- `go test ./wgengine/magicsock -count=1` reports `ok` and the test log
  does NOT contain the message "IPv6 loopback UDP roundtrip not delivered".
- `go test ./wgengine/magicsock -run TestSourcePathForcedAuxDualNodeRuntime/IPv6 -v`
  emits `--- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv6 (...)` plus
  the runtime evidence line `forced aux runtime path: aux=[::1]:port
  primary=[::1]:port peer=[::1]:port`, mirroring the IPv4 evidence form
  recorded in
  `docs/tailscale-direct-multisource-udp-phasew2-windows-ipv4-runtime-evidence.md`.
- `go test ./wgengine/magicsock -run TestSourcePathAutomaticAuxDualNodeRuntime/IPv6 -v`
  emits `automatic aux runtime path: aux=[::1]:port primary=[::1]:port
  peer=[::1]:port source={socketID:2 generation:1}` (`socketID:2` because
  the IPv6 auxiliary slot uses socket id 2 on the Windows port, matching
  the Linux `sourceIPv6SocketID` value).

When that evidence is captured, append it to
`docs/tailscale-direct-multisource-udp-phasew3-windows-ipv6-deferred.md`
or supersede the file with `phasew3-windows-ipv6-runtime-evidence.md` so
the deferral record stays linked to the resolution.

## Status

Phase W3 deferred. The W1 probe-and-skip pattern keeps the test suite green
on the current dev host and on any future host where the probe finds IPv6
loopback unusable, so this deferral does not block any Linux server work or
any Windows IPv4 work. The Windows client implementation is unchanged from
the Linux server implementation in the IPv6 paths; the deferral is about
host-level test environment, not about srcsel correctness.
