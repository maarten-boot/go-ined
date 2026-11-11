# Conversation log — 2026-11-11

## Session 1 (continued from 2026-11-10)

1. **User**: status.
   **Claude**:
   - M4b is merged into `main` (`0bb3f08`); `make all` passes; 86 of 105 TCs pass.
   - Not yet pushed: `main` (8 commits ahead), `docs/architecture` (1 ahead), and `feature/m4b-views`, which isn't on GitHub yet. Gave the push command.
   - The merged local branches can be deleted.
   - Recommended crosscheck 06 before M5.
2. **User**: pushed; do a crosscheck.
   **Claude** ran crosscheck `Crosscheck-2026-11-11-01.md` (the sixth) on M4b:
   - 149/149 tests pass, and the tracing is complete.
   - A database made by the pre-M4b server upgrades with every parent and position unchanged.
   - Findings: 0 H, 1 M, 8 L, plus one decision.
     - **M (C1):** in a PATCH, `null` on a field that can't be null silently sets its zero value; for example, `{"monitored":null}` turns monitoring off. This dates from M1.
     - **L:** `view show` exits 0 when its second request fails; `group list` and `group show` don't show a group's view; the Spec's GUI, API and CLI sections need cross-references to views; Plan M5 has no item for view tabs.
     - **Decision (C4):** `view place` without `--parent` moves the host to the view's root. Should this just be documented (recommended), or should `inedctl` keep the current parent?
   - The fixes wait for the user's OK.
