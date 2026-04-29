# srcsel-wfp-loopback-permit

Throwaway diagnostic helper that installs a high-priority Windows Filtering
Platform (WFP) permit for loopback traffic, intended for dev environments where
a third-party WFP filter (typically a TUN-based proxy) silently drops IPv6
loopback UDP and breaks the magicsock source-path tests.

The session is dynamic: when the helper exits the kernel removes every WFP
object the helper created, so the dev machine returns to its pre-helper state
even after a crash.

## When this helps

When the upstream blocker is itself a WFP callout / filter from a user-mode
process. The high-weight permit at all eight relevant V4 and V6 ALE and
TRANSPORT layers should override that filter in WFP arbitration.

## When this does NOT help

When the blocker is below WFP, for example:

- An NDIS Lightweight Filter (LWF) installed by a kernel-level proxy or EDR.
- A TDI filter from legacy security software.
- A boot-time hardening setting in Windows Server SKUs that disables IPv6
  loopback regardless of WFP state.

The investigation that produced this helper (Windows Server 2025, sing-box +
v2rayN running, Wintun driver active) verified the helper installed all eight
permit filters successfully (visible in `netsh wfp show filters`) yet
`ping -6 ::1` and TCP / UDP roundtrips on `::1` still failed. On that host the
blocker is below the WFP layer and the helper provides no benefit. The
magicsock source-path test file therefore probes IPv6 loopback at runtime via
`ipv6LoopbackUDPRoundtripProbe` and skips IPv6 subtests with an explanatory
message instead of failing.

## Usage

Requires Administrator privileges (WFP filter manipulation needs elevated
rights).

```powershell
# Build once.
go build -o C:\temp\srcsel-wfp-loopback-permit.exe .\cmd\srcsel-wfp-loopback-permit

# Run from an elevated shell. The helper prints "READY pid=<n> ..." when
# filters are installed, then blocks until stdin reaches EOF.
C:\temp\srcsel-wfp-loopback-permit.exe
```

To verify whether the helper actually unblocks IPv6 loopback on your machine:

```powershell
# In one elevated shell:
C:\temp\srcsel-wfp-loopback-permit.exe

# In another shell, while the helper is running:
ping -6 ::1 -n 2
```

If `ping -6 ::1` still reports `General failure` while the helper is running,
the blocker on your host is below WFP and this helper cannot solve the problem
without changing the upstream filter source (proxy, EDR, or platform setting).
