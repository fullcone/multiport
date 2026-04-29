# Tailscale Direct Multisource UDP Phase 16 Final Closeout

Date: 2026-04-29

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\fullcone`

WSL checkout: `/mnt/c/other_project/fullcone`

Branch: `phase1-srcsel-source-metadata`

Pull request: `https://github.com/fullcone/multiport/pull/1`

Last production runtime-changing implementation commit:
`cb3a212859ff647ecef95bf399b940e298b321ac`

Final validation checkout before this doc-only closeout:
`29af3d194e85d52174649e41e8a77835d6f85992`

This document is the final closeout record for the Linux direct multi-source UDP
implementation work. It does not introduce runtime behavior by itself.

## Current PR Review State

Known inline Codex review threads were checked before starting this closeout.
All known threads were resolved at that point, including the Phase 15 checkout
identity thread:

- `PRRT_kwDOSPBZuM5-NLPH`: resolved.
- `PRRT_kwDOSPBZuM5-NLPK`: resolved and outdated.
- `PRRT_kwDOSPBZuM5-UOSx`: resolved and outdated.
- `PRRT_kwDOSPBZuM5-UOS2`: resolved and outdated.
- `PRRT_kwDOSPBZuM5-U8eD`: resolved and outdated.
- `PRRT_kwDOSPBZuM5-cKZI`: resolved and outdated.

Latest Phase 15 feedback-fix review request:
`https://github.com/fullcone/multiport/pull/1#issuecomment-4344062888`

Latest Phase 15 Codex response:
`https://github.com/fullcone/multiport/pull/1#issuecomment-4344083622`

Result: Codex reported no major issues for the Phase 15 checkout identity fix.

Phase 16 Codex review thread addressed by this follow-up:

- `PRRT_kwDOSPBZuM5-cudx`: clarified that the production runtime-changing
  implementation commit is `cb3a212859ff647ecef95bf399b940e298b321ac`, while
  `29af3d194e85d52174649e41e8a77835d6f85992` is the validation checkout.

## Final Package Validation

Command run from the Windows host against the WSL checkout:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'git rev-parse HEAD && go test ./wgengine/magicsock -count=1'
```

Result:

```text
29af3d194e85d52174649e41e8a77835d6f85992
ok  	tailscale.com/wgengine/magicsock	11.166s
```

The WSL command printed the host's usual localhost/NAT warning after the
successful `go test` result. That warning was outside the Go test process and
did not change the test exit status.

The last production runtime-changing implementation commit is
`cb3a212859ff647ecef95bf399b940e298b321ac`. The complete PR checkout validated
by the command above was `29af3d194e85d52174649e41e8a77835d6f85992`, which is a
documentation/provenance layer on top of that runtime implementation. This
Phase 16 closeout file is documentation-only.

## Completed Implementation Phases

Phase 1: receive source metadata seam.

- Threaded source metadata through magicsock receive paths without changing
  data-plane behavior.
- Preserved unknown TxID and Linux raw disco behavior.

Phase 2: dual-stack auxiliary source path probes.

- Added Linux IPv4 and IPv6 auxiliary source sockets.
- Isolated auxiliary probe TxIDs from primary endpoint discovery.
- Added timeout cleanup for unsatisfied auxiliary probe TxIDs.

Phase 3: source-aware data send primitive.

- Added a controlled send path that can force a specific auxiliary source
  socket for data packets.
- Covered IPv4 and IPv6 forced auxiliary sends.
- Verified fallback to primary send when an auxiliary send fails.
- Verified auxiliary send failures do not pollute primary rebind accounting.

Phase 4A: observe-only source scorer.

- Added observation state for auxiliary path quality without automatic data
  send switching.

Phase 4B: observe selection boundary.

- Added explicit boundary tests around when observed auxiliary paths may be
  considered selectable.

Phase 5: automatic source selection.

- Enabled automatic direct-path data sends through selected auxiliary source
  sockets.
- Kept selection constrained to direct peer paths.
- Covered IPv4 and IPv6 automatic auxiliary runtime paths.

Phase 6: source selection observability.

- Added data-send metrics and debug visibility for source selection behavior.

Phase 7: source probe observability.

- Added auxiliary probe metrics so probe attempts, replies, expiry, and
  selection input can be audited.

Phase 8: source probe safety budget.

- Added safety limits around auxiliary source probing.

Phase 9: debug snapshot.

- Exposed source-selection debug snapshot state for operational auditing.

Phase 10: final runtime revalidation.

- Revalidated the full source-selection runtime path after the accumulated
  implementation phases.

Phase 11: runtime disable cleanup.

- Cleared source-selection state when the feature is disabled at runtime.

Phase 12: non-direct path guard.

- Prevented source selection from affecting DERP, lazy, and other non-direct
  send paths.

Phase 13: auxiliary socket count boundary.

- Pinned the auxiliary socket count and protected against accidental extra
  socket expansion.

Phase 14: lazy endpoint primary-send guard.

- Guarded lazy endpoint sends so they continue to use the primary path rather
  than implicit auxiliary source selection.

Phase 15: final dual-node runtime revalidation.

- Revalidated forced auxiliary sends under IPv4 and IPv6.
- Revalidated automatic auxiliary sends under IPv4 and IPv6.
- Revalidated fallback from forced auxiliary send failure to primary send.
- Revalidated that auxiliary send errors do not update primary rebind state.
- Recorded the tested checkout SHA after Codex requested stronger provenance.

## Behavior Now Guaranteed In Scope

The completed Linux implementation guarantees the following within the current
scope:

- Direct peer data traffic may use a selected auxiliary source socket only when
  source selection is enabled and the path is direct.
- IPv4 and IPv6 auxiliary source sockets are both implemented.
- Forced auxiliary data sends are covered for IPv4 and IPv6.
- Automatic auxiliary data selection is covered for IPv4 and IPv6.
- Auxiliary send failure falls back to primary send.
- Auxiliary send errors do not update or contaminate primary rebind accounting.
- Auxiliary disco probes do not advertise auxiliary endpoints as normal peer
  candidates.
- Stale auxiliary probe TxIDs expire instead of growing without bound.
- Non-direct, lazy, DERP, and guarded endpoint paths remain on the primary send
  path.
- Disabling source selection clears runtime source-selection state.
- Auxiliary socket count is bounded and tested.
- Source-selection behavior has debug and metric visibility for audit.

## Out Of Scope For This PR

The following items are intentionally not implemented in this PR:

- Non-Linux platform-specific source-aware sends.
- Production rollout on real remote hosts outside the WSL/local validation
  environment.
- A separate lazy-endpoint auxiliary-send design. Lazy endpoint sends are
  intentionally guarded to primary sends in this implementation.
- Policy beyond the current direct-peer source-selection scorer and safety
  budget.

## Final Assessment

The requested Linux direct multi-source UDP implementation is complete for the
current PR scope, including both IPv4 and IPv6. The last production
runtime-changing implementation commit is
`cb3a212859ff647ecef95bf399b940e298b321ac`; the checkout validated by the
recorded command was
`29af3d194e85d52174649e41e8a77835d6f85992`.
