# Tailscale Direct Multisource UDP Phase 18 Worktree Path Normalization

Date: 2026-04-29

Repository: `https://github.com/fullcone/multiport`

Current local checkout: `C:\other_project\zerotier-client\multiport`

Current WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase1-srcsel-source-metadata`

Pull request: `https://github.com/fullcone/multiport/pull/1`

This document records a documentation-only workspace normalization after the
Phase 17 closeout. The implementation checkout was moved under the planning
workspace so future audit, validation, and rerun commands all point at the same
tree.

## Scope

- Retired standalone local checkout path:
  `C:\other_project\fullcone`
- Retired standalone WSL checkout path:
  `/mnt/c/other_project/fullcone`
- Canonical local checkout path:
  `C:\other_project\zerotier-client\multiport`
- Canonical WSL checkout path:
  `/mnt/c/other_project/zerotier-client/multiport`

The GitHub repository identity remains `fullcone/multiport`. Repository URLs,
PR links, issue links, and review links were not renamed.

## Documentation Update

Path references in the Phase 1 through Phase 17 documents were normalized from
the retired standalone checkout path to the canonical checkout path. This makes
copy-paste validation commands runnable from the current workspace layout.

The path normalization does not change production code, test fixtures, runtime
behavior, or the review conclusions recorded in earlier phase documents. The
commit SHAs and PR links remain the audit authority for which source revision
was reviewed or tested.

## Validation

Local documentation sanity checks for this phase:

```powershell
rg -n "C:\\other_project\\fullcone|/mnt/c/other_project/fullcone" docs
git diff --check
```

Expected result:

- Only this relocation record may mention the retired checkout path.
- `git diff --check` passes, allowing normal LF-to-CRLF working-copy warnings
  if Git reports them on Windows.
