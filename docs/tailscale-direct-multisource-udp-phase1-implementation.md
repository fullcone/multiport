# Tailscale Direct Multi-Source UDP Phase 1 Implementation

This document records the Phase 1 implementation state for later audit.

## Scope

Phase 1 only adds receive-side source metadata plumbing. It does not add
additional UDP sockets, probe ordering, ECMP scoring, source selection, or send
path behavior changes.

## Repository State

- Repository: `https://github.com/fullcone/multiport`
- Local tree: `C:\other_project\fullcone`
- Branch: `phase1-srcsel-source-metadata`
- PR: `https://github.com/fullcone/multiport/pull/1`
- Code commit: `e9f49eff3 magicsock: thread receive source metadata`

## Implementation

The Phase 1 code change introduces a metadata object that can later carry the
local receive socket identity through disco handling without changing existing
packet behavior.

- `wgengine/magicsock/magicsock.go`
  - Added `SourceSocketID`.
  - Added `sourceRxMeta`.
  - Added `primarySourceSocketID` and `primarySourceRxMeta`.
  - Kept the existing `handleDiscoMessage` API as a wrapper.
  - Added `handleDiscoMessageWithSource`.
  - Routed the normal UDP receive path through `handleDiscoMessageWithSource`
    with primary source metadata.

- `wgengine/magicsock/derp.go`
  - Routed DERP disco handling through `handleDiscoMessageWithSource` with
    primary source metadata.

- `wgengine/magicsock/magicsock_linux.go`
  - Routed Linux raw disco handling through `handleDiscoMessageWithSource` with
    primary source metadata.

- `wgengine/magicsock/endpoint_test.go`
  - Added coverage for unknown disco Pong TxID behavior.
  - The test verifies that an unknown Pong TxID remains a no-op and does not
    mutate `sentPing`, `bestAddr`, or `trustBestAddrUntil`.

- `wgengine/magicsock/sourcepath_test.go`
  - Added focused coverage that `primarySourceRxMeta` uses
    `primarySourceSocketID`.

## Behavior Guarantees

The current metadata value is intentionally unused by path selection. The new
`handleDiscoMessageWithSource` accepts `sourceRxMeta`, assigns it to `_`, and
then follows the existing disco handling flow. This preserves current behavior
while creating a stable receive-path seam for later phases.

Known behavior preserved in Phase 1:

- Existing `handleDiscoMessage` callers keep compiling through the wrapper.
- Normal UDP, DERP, and Linux raw disco all map to the primary source socket.
- Unknown disco Pong TxID remains a no-op.
- No send path uses `SourceSocketID`.
- No source preference, ranking, probing, or scoring is introduced.

## Validation

Validation run for the code commit:

```powershell
go test ./wgengine/magicsock
```

Result: passed.

Linux raw disco compile check:

```powershell
$env:GOOS='linux'
$env:GOARCH='amd64'
go test -c -o "$env:TEMP\magicsock_linux.test" ./wgengine/magicsock
```

Result: passed.

Whitespace check:

```powershell
git diff --check
```

Result: no whitespace errors.

## Review Process

The PR follows the `C:\netdisk_work_ascii` workflow pattern:

1. Push each completed commit to the PR branch.
2. Add a PR comment requesting Codex review for the latest head.
3. Treat review comments as blocking until inspected and either fixed or
   explicitly documented.

Initial review request for the code commit:

- `https://github.com/fullcone/multiport/pull/1#issuecomment-4336864938`

For each later commit, request a new review with:

```text
@codex review this latest head.

Please review the diff first, then the affected subsystem under `wgengine/magicsock`.

Prioritize:
- disco receive-path behavior preservation
- Linux raw disco path correctness
- unknown TxID handling
- keeping Phase 1 metadata plumbing free of send-path or path-selection behavior changes
```

## Next Phase Gate

Do not start Phase 2 until Phase 1 review feedback has been checked. If Codex
reports a real issue, fix it in this PR before adding any new source socket or
probe behavior.
