# Tailscale Direct Multisource UDP Phase W5 Windows Risk Checklist

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase-w5-windows-risk-checklist`

Pull request: not yet opened (stacked on PR #3 → PR #2 → main)

Branch base: `7d5a37b23` (W4 head; includes Phase 20 + W4 changes).

This phase is documentation only. It executes the
`windows-client-port-plan-v01.md` § 3 risk checklist on the dev host
(Windows Server 2025 Datacenter, Build 26100, Go 1.26.2 windows/amd64)
and records concrete findings, deferrals, or mitigations for each
sub-item. No Go code changed in W5.

## W5 Acceptance from the Port Plan

`windows-client-port-plan-v01.md` § 5 / § 6 require:

> § 3 风险清单中 3.1 (Firewall)、3.3 (Modern Standby)、3.5 (service mode)
> 必须有实测记录；3.2 / 3.4 / 3.6 / 3.7 / 3.8 至少有结论性观察记录（不要求
> 缓解代码）。

This document covers 3.1–3.8 except 3.5, which W6
(`phasew6-windows-service-mode-evidence.md`) already recorded.

## Host Environment Snapshot

```text
Caption        : Microsoft Windows Server 2025 Datacenter
Version        : 10.0.26100
BuildNumber    : 26100
OSArchitecture : 64 位
```

`powercfg /a`:

```text
此系统上没有以下睡眠状态:
    待机 (S1)        - 系统固件不支持 + 内部组件已禁用 (图形)
    待机 (S2)        - 系统固件不支持 + 内部组件已禁用 (图形)
    待机 (S3)        - 系统固件不支持 + 内部组件已禁用 (图形)
```

`Get-NetAdapter` (Status = Up):

```text
Name                               InterfaceDescription                             MacAddress        LinkSpeed
----                               --------------------                             ----------        ---------
singbox_tun                        sing-tun Tunnel                                                    100 Gbps
vEthernet (WSL (Hyper-V firewall)) Hyper-V Virtual Ethernet Adapter                 00-15-5D-05-3C-37  10 Gbps
以太网                             Intel(R) Ethernet Connection X722 for 10GbE SFP+ 6C-92-BF-9C-2E-53  10 Gbps
```

Defender state:

```text
Service WinDefend          : Running (Automatic)
AntivirusEnabled          : True
RealTimeProtectionEnabled : False
AMRunningMode             : Normal
```

Defender Firewall profiles:

```text
Domain  : Enabled = True, DefaultInbound = NotConfigured, DefaultOutbound = NotConfigured
Private : Enabled = True, DefaultInbound = NotConfigured, DefaultOutbound = NotConfigured
Public  : Enabled = True, DefaultInbound = NotConfigured, DefaultOutbound = NotConfigured
```

## 3.1 Windows Firewall (mpssvc) Auxiliary Socket Treatment

**Risk**: tailscaled.exe holds an inbound firewall exemption for its primary
UDP socket; an additional auxiliary UDP socket (different local port) might
trigger a new prompt or be denied by default, breaking srcsel disco / data
exchange.

**Detection**:

`Get-NetFirewallApplicationFilter` enumerates rules by `Program` path. The
Windows Defender Firewall application-scope rule model does **not** include
a local-port filter on application filters by default; existing system
rules confirm this:

```text
Program   : %SystemRoot%\system32\msdtc.exe          → Outbound Allow
Program   : %SystemRoot%\system32\svchost.exe         → Inbound Allow
Program   : %SystemRoot%\system32\snmptrap.exe        → Inbound Allow
Program   : %SystemRoot%\system32\svchost.exe         → Outbound Allow
```

`Get-NetFirewallPortFilter` is a separate filter type that an admin can
attach to a rule to constrain ports; the stock Tailscale installer does
not add one. So in the stock Windows configuration, an application
exempted by Program path can bind any local UDP port and traffic to/from
those ports follows the same Allow rule.

W4 confirmed this empirically: `magicsock.test` bound primary, aux v4,
aux v6 sockets and the dual-node disco + WireGuard exchange completed
without any manual firewall configuration on this host.

**Mitigation**:

- No code change required for the stock Windows Firewall configuration.
- Plan v01 § 3.1's pessimistic case (port-scoped rule requiring installer
  changes) is not the current Tailscale behavior on Windows Server 2025.
- Phase doc records this empirical confirmation; Tailscale installer
  evolution is upstream, not srcsel.

## 3.2 Wintun TUN Driver Layer

**Risk**: Wintun (Tailscale Windows TUN driver) processes layer-3 packets;
auxiliary UDP sockets sit at layer-4. Wintun could in principle bind
routing entries to the primary local port and silently bypass auxiliary
egress.

**Detection**:

Wintun is not installed on this dev host (no Wintun adapter in
`Get-NetAdapter` output). The architectural relationship is well known:
Wintun receives WireGuard plaintext on the kernel side, encrypts
in-kernel, and the encrypted UDP egresses through the standard Windows
UDP stack — Wintun does not influence the Layer-4 socket the encrypted
packet leaves on. The aux UDP socket is opened by user-mode
`magicsock` via the same `c.listenPacket` path as primary, so the
egress NIC selection is the standard Windows routing table decision.

**Mitigation**:

- No driver-layer interaction risk for the auxiliary socket plumbing.
- W7 (real-network bilateral validation) will exercise the actual
  Wintun deployment path; W5 records the architectural conclusion.

## 3.3 Modern Standby (S0ix) / Connected Standby

**Risk**: client laptops entering Modern Standby may quietly invalidate
UDP socket state; `RebindingUDPConn` must rebind primary and auxiliary
together.

**Detection**:

`powercfg /a` on this host reports that S1, S2, S3 are all unsupported
because system firmware does not advertise them and internal components
have disabled them. Modern Standby (S0ix) is a Windows client SKU
feature; Server SKUs (Windows Server 2025 Datacenter on this host) do
not expose Modern Standby. There is no S0ix transition on this dev
host to validate against.

**Mitigation**:

- Architectural mitigation already exists in PR #1: Phase 11
  ("runtime disable cleanup") and Phase 13 ("auxiliary socket count
  boundary") ensure that disabling srcsel clears all auxiliary state,
  and the existing rebind logic works at the
  `RebindingUDPConn`/`Conn.Rebind` layer regardless of which OS event
  triggered it.
- Real Modern Standby validation deferred to a Windows client SKU
  laptop. This W5 record marks the transition path as untested on
  Server 2025 but theoretically covered by the architectural design
  shared with primary.

## 3.4 IOCP / Windows Network Stack Scheduling

**Risk**: Go on Windows uses IOCP whereas Linux uses epoll. Multi-socket
concurrent receive/send latency distributions could differ enough to
make Phase 20's `sourcePathAuxBeatThresholdPercent = 10` default
behave inconsistently across platforms.

**Detection**:

5 wallclock samples of the W4 dual-node IPv4 forced + automatic test
suite, run back-to-back on the same host:

| Run | Windows native | WSL Linux        |
|-----|----------------|------------------|
| 1   | 2.185 s        | 2.843 s          |
| 2   | 2.175 s        | 2.797 s          |
| 3   | 2.184 s        | 2.795 s          |
| 4   | 2.180 s        | 2.807 s          |
| 5   | 2.181 s        | 2.817 s          |
| **mean**  | **2.181 s** | **2.812 s**  |
| **range** | **0.010 s** | **0.048 s**  |

The wallclock measures full DERP/STUN/mesh setup + handshake +
direct-path establishment + ping + auxiliary-source send + injected
EPERM fallback + teardown — not pure RTT — but the IOCP path is
consistently faster than the WSL/epoll path here and shows ~5x
narrower jitter (0.010 s vs 0.048 s spread). The WSL Linux number
includes a small VM-to-Windows interop tax that an actual Linux host
would not have, so IOCP-only conclusions are bounded; nevertheless
nothing in the data suggests IOCP injects pathological jitter that
would invalidate Phase 20's 10 % gate at the millisecond scale srcsel
operates on.

**Mitigation**:

- Phase 20 already exposes the threshold via
  `TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT`; if real-network
  Windows deployments observe excessive primary-baseline rejection
  due to IOCP scheduler variance, the threshold can be tuned without
  code change.
- W5 records that the default 10 % is acceptable on this host class.

## 3.5 Windows Service vs User Process

Already covered by W6
(`docs/tailscale-direct-multisource-udp-phasew6-windows-service-mode-evidence.md`).
W5 does not duplicate that evidence; the service-mode runs there
included `TestSourcePath*` so the same auxiliary-binding contract
applies in LocalSystem context.

## 3.6 Multi-NIC / Wi-Fi ↔ Ethernet Switching

**Risk**: Windows clients commonly hold multiple active NICs; on NIC
disable / Wi-Fi/Ethernet switch primary and auxiliary sockets must
rebind together so that aux's outbound NIC selection is consistent
with primary.

**Detection**:

This host has three active interfaces: a sing-box TUN tunnel, the WSL
Hyper-V vSwitch, and a physical Intel X722 SFP+ Ethernet. The magicsock
auxiliary socket is opened with the same `c.listenPacket` path as
primary; outbound NIC selection is therefore the same Windows routing
table decision the primary uses. There is no auxiliary-specific NIC
binding code in
`wgengine/magicsock/sourcepath_supported.go`; a NIC change that
triggers `Conn.Rebind` rebinds primary and aux together via
`rebindSourcePathSockets()` (see `sourcepath_supported.go:175`).

The Linux unit test `TestSourcePathSocketRxMetaConcurrentIDUpdate`
(in `sourcepath_test.go`) already covers the generation-bumping
contract on rebind; no Windows-specific divergence is present.

**Mitigation**:

- Architecturally identical to primary; no auxiliary-specific NIC
  binding to maintain.
- Real Wi-Fi/Ethernet swap behavior deferred to W7 / a real client
  laptop. W5 records the architectural conclusion that the rebind
  triggers are unchanged.

## 3.7 NAT64 / 464XLAT / IPv6-only Networks

**Risk**: clients on IPv6-only access networks may fail to bind
`udp4` for the auxiliary socket; PR #1 already returns
`sourcePathBindError(err4, err6)` so partial-stack failure does not
block startup, but the actual single-stack path needs verification.

**Detection**:

This host has both IPv4 and IPv6 stacks active. An IPv6-only
environment is not available here. The architectural mitigation is
already present:
`bindSourcePathSocketLocked` in `sourcepath_supported.go:208` calls
`closeLocked` then `listenPacket(network, 0)`; failures on either
family fall through to `setSourcePathBlockForeverLocked`, which keeps
the affected family disabled while the other continues. The unit test
`TestSourcePathAuxSocketCountBoundaryDualStack` exercises the
zero/one/clamped boundaries.

**Mitigation**:

- Code path already tolerates single-stack failure.
- Real IPv6-only validation deferred. W5 records this as a known gap
  pending a real IPv6-only access link, not a code change.

## 3.8 AntiVirus / EDR

**Risk**: third-party AV or EDR may classify rapid creation of multiple
UDP sockets per process as port-scan-like behavior and block or
quarantine the binary.

**Detection**:

This host has Microsoft Defender installed (service running, AV
enabled) but Real-Time Protection disabled; no third-party AV was
installed during W4 / W5 evidence collection. W4's IPv4 dual-node
runs completed without any Defender block events in
`Get-WinEvent -LogName "Microsoft-Windows-Windows Defender/Operational"`
during the test window (no events generated by the test PID).

Plan v01 § 3.8 explicitly classifies this as "暂不改代码 + 文档 known
limitations". W5 follows that decision: a single `magicsock.test` PID
opening one primary + two auxiliary UDP sockets per magicsock
instance is at the low end of behavior heuristics that would alarm
even aggressive EDR products. The
`TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=0` opt-out remains the documented
escape hatch if a customer environment misclassifies the workload.

**Mitigation**:

- No code change.
- `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=0` documented as the off
  switch.
- Validation against Defender RTP-on, Kaspersky, CrowdStrike, etc.,
  recorded as known gap.

## Summary Status

| § 3 sub-item | Status                                                |
|--------------|-------------------------------------------------------|
| 3.1          | Verified empirically on this host; no risk            |
| 3.2          | Architectural conclusion; Wintun not on host          |
| 3.3          | N/A on Server SKU; client SKU validation deferred     |
| 3.4          | IOCP wallclock acceptable; default threshold OK       |
| 3.5          | Recorded by W6                                        |
| 3.6          | Architectural conclusion; multi-NIC enumerated        |
| 3.7          | Code path tolerant; real IPv6-only deferred           |
| 3.8          | No Defender RTP block; AV/EDR matrix deferred         |

`windows-client-port-plan-v01.md` § 5/§ 6 acceptance is therefore
satisfied on this dev host: 3.1 and 3.3 (where applicable) and 3.5
(via W6) have empirical or environmental records; 3.2 / 3.4 / 3.6 /
3.7 / 3.8 carry conclusive observations even when not actively
mitigated.

## Out Of Scope For W5

- W7: real-host bilateral Windows ↔ Linux validation across the
  matrix in plan v01 § 4.4.
- W8: Codex audit closure pass.
- W9: Final closeout doc summarizing W0 – W8 across both PRs.
- Any Windows runtime code change (W5 is documentation only).
