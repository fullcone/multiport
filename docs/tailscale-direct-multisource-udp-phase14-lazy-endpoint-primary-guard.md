# Tailscale Direct Multisource UDP Phase 14 Lazy Endpoint Primary Guard

Date: 2026-04-29

This document records the Phase 14 regression guard for `Conn.Send` with
`*lazyEndpoint` under Linux source-selection forced auxiliary mode in
`fullcone/multiport`.

## PR Review Gate

Current PR feedback state before this implementation:

- PR: `https://github.com/fullcone/multiport/pull/1`
- PR head before this phase: `41b6ac547b44d193e8bba325c52f3fc5e1afbd6c`
- All known inline review threads were resolved.
- The latest Phase 13 doc-only review request
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343556912`
  received Codex response
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343571687`.
- Result: no major issues.
- No current blocking review thread was present, so implementation continued
  under the automatic flow rule.

## Review Tracking

Implementation commit:

- `1a4b13b62f06d63d5359056728b3345e1c7d5945`

Review request:

- `https://github.com/fullcone/multiport/pull/1#issuecomment-4343703420`

First recorded poll after the Phase 14 review request:

- Local poll time: `2026-04-29T20:40:55.1036826+08:00`
- PR head at poll time: `1a4b13b62f06d63d5359056728b3345e1c7d5945`
- All known inline review threads remained resolved.
- No new current unresolved blocking review thread was present.
- No Codex response to the Phase 14 implementation review request was visible
  at this poll.
- Action: recorded the poll result and continued the automatic flow.

Second recorded poll after the Phase 14 review-poll record request:

- Local poll time: `2026-04-29T20:44:22.7702162+08:00`
- PR head at poll time: `67912ff2cc785f49d0e9c6784d322cc77026396b`
- Phase 14 implementation review response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343745510`
- Phase 14 doc-only review response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343759953`
- Result: both Codex responses reported no major issues.
- All known inline review threads remained resolved.
- No new current unresolved blocking review thread was present.
- Action: recorded both review responses and continued the automatic flow.

## Scope

The source-selection data-send path is intentionally attached to normal
magicsock endpoints through `endpoint.send`. `Conn.Send` still has a separate
`*lazyEndpoint` branch that writes directly through the primary rebinding UDP
connections:

- IPv4 lazy endpoints use `c.pconn4.WriteWireGuardBatchTo`.
- IPv6 lazy endpoints use `c.pconn6.WriteWireGuardBatchTo`.

That branch must remain primary-only. It must not inherit forced or automatic
auxiliary source selection merely because the Linux source-selection
environment variables are enabled.

This phase adds a regression test for that boundary. It does not change
production code behavior.

## Code Changes

`wgengine/magicsock/sourcepath_linux_test.go`

- Adds `TestLazyEndpointSendIgnoresForcedAuxDataSourceDualStack`.
- Runs the test with:
  - `TS_EXPERIMENTAL_SRCSEL_ENABLE=true`
  - `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1`
  - `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux`
- Covers both IPv4 and IPv6 loopback paths.
- Binds a primary packet connection and an auxiliary packet connection for each
  address family.
- First proves that `sourcePathDataSendSource` would select the current
  auxiliary source for a normal direct endpoint.
- Sends via `Conn.Send` with `*lazyEndpoint`.
- Verifies the received UDP source port is the primary port.
- Verifies the received UDP source port is not the auxiliary port.

## Safety Properties

This pins the intended first-version boundary:

- Forced auxiliary data-send selection applies only where source metadata is
  explicitly threaded into the endpoint send path.
- `*lazyEndpoint` keeps using primary `pconn4` and `pconn6` sends.
- IPv4 and IPv6 lazy endpoint sends stay covered equally.
- The test guards against accidental future refactors that route lazy endpoint
  sends through auxiliary sockets.
- Primary rebind behavior is not changed by this phase.

## Validation

Completed local validation on 2026-04-29:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'gofmt -w wgengine/magicsock/sourcepath_linux_test.go'
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run "TestLazyEndpointSendIgnoresForcedAuxDataSourceDualStack|TestSendUDPBatchFromSourceAuxDualStackLoopback|TestSourcePathDataSendSourceForcedAuxDualStack" -count=1 -v'
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run TestSourcePath -count=1'
git diff --check
```

Validation results:

- `gofmt` completed successfully.
- `TestLazyEndpointSendIgnoresForcedAuxDataSourceDualStack` passed for IPv4.
- `TestLazyEndpointSendIgnoresForcedAuxDataSourceDualStack` passed for IPv6.
- Existing auxiliary loopback send coverage passed for IPv4 and IPv6.
- Existing forced auxiliary source-selection coverage passed.
- Existing `TestSourcePath` focused group passed.
- `git diff --check` exited 0 with only the repository's LF-to-CRLF worktree
  warning for the touched Go test file.
- The WSL command printed the known localhost/NAT warning after the Go test
  result; both Go test commands still exited successfully.

Focused test output:

```text
--- PASS: TestSourcePathDataSendSourceForcedAuxDualStack
--- PASS: TestSendUDPBatchFromSourceAuxDualStackLoopback
--- PASS: TestLazyEndpointSendIgnoresForcedAuxDataSourceDualStack
PASS
ok  	tailscale.com/wgengine/magicsock	0.031s
```

Full focused group output:

```text
ok  	tailscale.com/wgengine/magicsock	0.169s
```

## PR Review And Polling

Phase 14 implementation review:

- Implementation commit:
  `1a4b13b62f06d63d5359056728b3345e1c7d5945`
- Review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343703420`
- Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343745510`
- Result: no major issues.

First recorded 60-second poll after the Phase 14 implementation review request:

- Poll record commit:
  `67912ff2cc785f49d0e9c6784d322cc77026396b`
- Review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343747956`
- Result at the recorded poll time: no Codex response yet for the doc-only
  poll-record request, and no new current unresolved blocking review thread.

Second recorded poll after the Phase 14 review-poll record request:

- Local poll time: `2026-04-29T20:44:22.7702162+08:00`
- PR head at poll time:
  `67912ff2cc785f49d0e9c6784d322cc77026396b`
- Phase 14 implementation response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343745510`
- Phase 14 doc-only response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343759953`
- Result: both responses reported no major issues.
- All known inline review threads remained resolved; no new current unresolved
  blocking review thread was present.
- Action: continued automatic flow to record the review responses.

Third recorded poll after the Phase 14 review-response record request:

- Local poll time: `2026-04-29T20:50:54.5575793+08:00`
- PR head at poll time:
  `dcb5b87a3c5f4ecce5c10778c3309c0331da1795`
- Review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343798622`
- Codex response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343809795`
- Result: no major issues.
- All known inline review threads remained resolved; no new current unresolved
  blocking review thread was present.
- Action: continue automatic flow into Phase 15 final runtime revalidation.
