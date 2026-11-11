# Conversation log — 2026-11-12

## Session 1 (continued from 2026-11-11)

1. **User** answered the multi-select questions (asked on 2026-11-11): select with Ctrl+click and a Shift+drag rubber band; g asks a name; c, e and Delete act on all selected.
   **Claude** built the two queued requests:
   - **`feature/anchored-expand`** (merged): the API move takes `contents_only`. The GUI keeps a group's top-left corner on collapse (icon to the frame's corner) and on expand (contents shifted first); empty frames start at the icon's corner. Checked under Xvfb, also after dragging the collapsed icon elsewhere. On the way, it fixed a racy test (`TestDiscoveryDialog`, 2 of 10 runs): it released the probes before the cancel had reached the server.
   - **`feature/multi-select`** (merged): a selection set in the map widget (modifier keys from the mouse press); the rubber band; multi-drag and drop; keys g (group the selection: name dialog, common parent, positions kept, corner rule), c/e, Delete (one question), Esc. Checked under Xvfb: Ctrl+click, g, c, the rubber band, Delete.
   - Spec and Plan v0.29 (SPEC-507, 703, 724), TC-725/726. `make all`, staticcheck and govulncheck pass.
2. **User**: pushed; do a crosscheck.
   **Claude** ran crosscheck `Crosscheck-2026-11-12-01.md` (the tenth) on everything since 0.1.0:
   - 219 tests pass, and CI on `main` is green.
   - **Probes:**
     - Wrapping costs about 7 ms per layout of 5,000 long names, and a 1,000-event burst stays at 0.15 s.
     - Keyboard focus is fine: Delete after editing the note asks to delete the host; the note is untouched.
     - Reading the code showed that the Rechecker closes the old prober at once, so a ping round in flight can fail falsely.
   - **Findings:** 0 H, 1 M (C1: delay closing the old prober), 4 L.
   - **Decisions:** D1 (what the right-click menu does with a multi-selection) and D2 (0.2.0 or 0.1.1 next).
   - **Process note:** display `:93` belonged to another Xvfb; the probe now checks for a free display first.
   - The fixes wait for the user's OK.
3. **User**: pushed; OK, all.
   **Claude** applied the crosscheck fixes on `fix/crosscheck-10` (merged):
   - **C1:** a replaced prober is closed two minutes later, not at once.
   - **C2 / D1 (b):** a right-click on a selected item keeps the selection; "Move N items to group…" and "Delete N items…" act on all selected.
   - **S1:** "Cmd+click on macOS" in SPEC-724.
   - **E1:** examples for arrange, `inedctl view arrange` and `contents_only`; on the way, "arranged 1 member".
   - Spec v0.30 (SPEC-304, 724), TC-727, the crosscheck's resolution. `make all` and `make lint` pass.
   - Then prepared release 0.2.0 (D2 a): see the next entry.
4. **Claude** prepared release 0.2.0 (D2 a), on `main`:
   - `Changes.md`: `[Unreleased]` became `[0.2.0] - 2026-11-12` (an empty `[Unreleased]` stays); the follow-up fixes were folded into the new features they belong to.
   - OpenAPI `info.version` 0.2.0; README status (M1–7 done, latest release 0.2.0).
   - `make all` passes; a local `build-release.sh v0.2.0` gave 8 files, the checksums match and the binaries print `v0.2.0`.
   - Annotated tag `v0.2.0` made locally. The user pushes; then Claude checks the release run and its assets.
