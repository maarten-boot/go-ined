# Conversation log — 2026-11-13

## Session 1 (continued from 2026-11-12)

1. **User**: status?
   **Claude**: all refs pushed (checked with `git ls-remote`), CI green on `main`, `docs/architecture` and `fix/crosscheck-14`. v0.3.1 is the latest release (verified). `[Unreleased]` has the DNS log fix. Dependabot PR #1 is closed. Open: the formal OpenAPI validation, and a visual check of the new GUI features.
2. **User**: do a crosscheck.
   **Claude** ran crosscheck `Crosscheck-2026-11-13-01.md` (the fifteenth) on the crosscheck-14 fix, with a probe of the real binaries under Xvfb `:97` (a free display):
   - The menu on two groups, the Host labels submenu, the details row and "Expand 2 groups" all work as specified; an already expanded group's host didn't move.
   - **C1 (L):** frame labels can run past the frame's right edge: the width leaves out the status glyph, and text is measured regular but drawn bold (since M5).
   - Only this session's processes were stopped, and the scratchpad was emptied.
   - The fix waits for the user's OK.
3. **User**: pushed; OK, all.
   **Claude** checked the push (`git ls-remote`: `main` and `docs/architecture` arrived).
   - Applied C1 on `fix/crosscheck-15` (merged): frames count the status glyph, and map text is measured bold. TestFrameLabelWidth (TC-725); SPEC-703 v0.36.
   - Checked under Xvfb `:97`: the label fits.
   - A test comment first named TC-703, the reconnect test; it is corrected to TC-725.
   - Kept in `[Unreleased]`.

4. **User**: pushed; do a crosscheck.
   **Claude** checked the push (`git ls-remote`): only `main` arrived; `docs/architecture` is still at `2715e4b` and `fix/crosscheck-15` is missing.
   - Ran crosscheck `Crosscheck-2026-11-13-02.md` (the sixteenth) on the crosscheck-15 fix. The fix is correct; all tests pass, and lint is clean.
   - **C1 (M, older):** "Reflow…" on a collapsed group sizes an expanded subgroup as an icon (probe: an 80 × 92 cell instead of 480 × 428), so its frame overlaps a neighbour after expanding (SPEC-723).
   - Dependabot PR #1 turned out to be closed already.
   - The fix waits for the user's OK.
5. **User**: pushed; OK, all.
   **Claude** checked the push (`git ls-remote`: all three branches arrived; CI on `b7dc464` passed).
   - Applied C1 on `fix/crosscheck-16` (merged): Reflow on a collapsed group measures it as expanded, so expanded subgroups count with their whole frame. TestReflowCellCollapsed (TC-724); SPEC-723 v0.37.
   - D2: no release; `[Unreleased]` holds three fixes.
