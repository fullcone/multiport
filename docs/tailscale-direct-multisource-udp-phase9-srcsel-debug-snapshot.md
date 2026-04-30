# Tailscale Direct Multisource UDP Phase 9 Source Selection Debug Snapshot

Date: 2026-04-29

This document records the Phase 9 implementation against
`tailscale_direct_multisource_udp_final_implementation_v02.md`.

## Scope

Phase 9 exposes the experimental source-selection state in the existing
`/debug/magicsock` page. This is an observability-only change intended to make
runtime audit easier before and during Linux dual-node validation.

The debug page now reports:

- source-path generation
- configured auxiliary socket enablement count
- pending auxiliary source-path probe count
- retained source-path sample count
- configured probe peer budget
- configured probe burst budget
- auxiliary IPv4 socket bound state, socket ID, generation, and local address
- auxiliary IPv6 socket bound state, socket ID, generation, and local address

## Code Changes

`wgengine/magicsock/debughttp.go`

- Adds a `Source selection` section immediately after the magicsock page
  heading.
- Takes a small source-selection snapshot under the existing debug-page
  `Conn.mu` lock.
- Reads auxiliary IPv4 and IPv6 socket metadata under `sourcePath.mu`.
- Reads auxiliary socket local addresses under each `RebindingUDPConn` lock and
  tolerates uninitialized sockets.
- Escapes rendered labels, socket IDs, and local addresses before writing HTML.

`wgengine/magicsock/sourcepath_default.go`

- Adds a non-Linux `sourcePathAuxSocketCount` stub so the common debug page can
  report `0` auxiliary sockets outside Linux builds.

`wgengine/magicsock/debughttp_test.go`

- Covers the generated source-selection debug HTML for generation, pending
  probe count, sample count, budget values, and both IPv4/IPv6 auxiliary socket
  rows.

## Safety Properties

This phase does not change:

- packet receive classification
- auxiliary source-path probing
- candidate scoring
- forced or automatic data-source selection
- UDP send routing
- primary socket rebind logic
- auxiliary socket bind or close behavior

The debug snapshot only reads existing state. It does not prune pending probes,
add samples, select a candidate, or write to any socket.

The lock order in the debug snapshot is:

- existing debug page `Conn.mu`
- `sourcePath.mu`
- individual auxiliary `RebindingUDPConn.mu`

The source-path send and rebind paths do not hold `RebindingUDPConn.mu` while
entering `Conn.mu`, so this keeps the debug-only read path separated from the
runtime send path.

## Validation

Completed local validation on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\debughttp.go wgengine\magicsock\debughttp_test.go wgengine\magicsock\sourcepath_default.go
go test ./wgengine/magicsock -run TestPrintSourcePathDebugHTML -count=1
go test ./wgengine/magicsock -run "TestSourcePathProbe|TestPrintSourcePathDebugHTML" -count=1
go test ./wgengine/magicsock ./envknob -count=1
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run TestPrintSourcePathDebugHTML -count=1'
git diff --check
```

Validation results:

- `gofmt` completed successfully.
- Focused debug HTTP test passed:
  `ok tailscale.com/wgengine/magicsock 0.049s`.
- Focused source-path plus debug HTTP tests passed:
  `ok tailscale.com/wgengine/magicsock 0.048s`.
- Windows package validation passed:
  `ok tailscale.com/wgengine/magicsock 9.815s` and
  `ok tailscale.com/envknob 0.032s`.
- WSL Ubuntu-24.04 focused debug HTTP test passed:
  `ok tailscale.com/wgengine/magicsock 0.011s`.
- The WSL command printed the host localhost/NAT warning before running tests;
  the Go test command still exited successfully.
- `git diff --check` passed with only CRLF worktree conversion warnings for
  touched Go files.

## PR Review Record

Current PR feedback state before this implementation:

- Latest PR head before Phase 9 was
  `4c174da62e71cdee00cd5df7c80ad28984f31aa9`.
- Latest Codex response before Phase 9 was a no-major-issues response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342957754`.
- Earlier actionable review threads are resolved or outdated.
- The only unresolved thread remained the outdated Phase 4B P2
  `sourcePathSocket.rxMeta` synchronization finding
  (`PRRT_kwDOSPBZuM5-U8eD`), already fixed by commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a` and followed by a no-major-issues
  Codex review.
- No new actionable review thread blocked Phase 9.

Phase 9 implementation review tracking:

- Implementation commit:
  `62b307da746f7d5f4c0c6f7b78a5f79b64f66ec0`.
- Review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343087512`.
- Codex implementation review response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343111677`.
- The implementation review response reported no major issues.
- First 60-second poll after the Phase 9 review request initially found no
  Codex response yet and no new actionable review thread.
- The follow-up PR thread check later found the Codex implementation response
  above and still found no new actionable current review thread.
- Doc-only review request for the first poll record:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343121651`.
- Codex doc-only review response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343132051`.
- The doc-only review response reported no major issues.
- The only unresolved thread remained the outdated Phase 4B P2
  `sourcePathSocket.rxMeta` synchronization finding
  (`PRRT_kwDOSPBZuM5-U8eD`), already fixed by commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a`.
