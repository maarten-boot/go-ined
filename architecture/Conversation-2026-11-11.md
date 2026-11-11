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
3. **User**: pushed; OK, all.
   **Claude** applied the crosscheck fixes on `fix/crosscheck-06`:
   - **C1:** a `null` in a PATCH is rejected with 400, except for `parent_id`.
   - **C3:** `group list` has a VIEW column, and `group show` shows the group's view.
   - **C2:** `view show` exits non-zero when it can't read the view's hosts.
   - **C4:** the usage text says that `view place` without `--parent` puts the host at the view's root.
   - **Docs:** Spec and Plan v0.17 (SPEC-122/124 clarified; SPEC-501/502/504/702/901/902 updated; Plan M5 has the view-tabs and benchmark items).
   - Two new test checks and `make all` passes.
4. **User**: pushed; start M5.
   **Claude** checked the build environment for Fyne: gcc, pkg-config and the X11/OpenGL/Wayland dev packages are present, Xvfb is available, and the latest Fyne is v2.8.2 (2026-10-07). It wrote the M5 draft, Spec and Plan v0.18:
   - **New Spec items:** SPEC-715 (view tabs), SPEC-716 (how the GUI keeps up with the server), SPEC-717 (window layout and status bar), SPEC-718 (what the GUI may change in M5: expand/collapse, note, contrast).
   - **Amended:** SPEC-708 (the parent shown is the one in the current view), SPEC-713 (zoom and pan per tab), SPEC-714 (find over all hosts).
   - **Tests:** TC-710..713 added, and TC-709 extended.
   - **Decisions:** proposed M5-1..10 (D15) with defaults. The code waits for the decisions.
5. **User**: pushed; OK, defaults (M5 decisions 1–10).
   **Claude** built M5 on `feature/m5-gui`:
   - **`internal/theme`:** WCAG contrast math, the presets and derived palettes. Every colour pair is tested for every preset, and Max is black and white only.
   - **`internal/gui/mapmodel` (pure Go):**
     - the views and the loaded maps;
     - events: status and names in place, other changes reload the view at most every 250 ms, `resync` reloads everything;
     - layout with frames and counts, coordinates, find and locate.
   - **`internal/gui` (Fyne v2.8.2):**
     - a map widget that draws only what is near the visible area, with status as shape plus word, folder icons, dashed network frames and note corners;
     - pan, wheel zoom, scroll bars, double-click expand/collapse and a context menu;
     - view tabs, a toolbar, a details panel with note editing, a status bar, the find dialog and the contrast selector.
   - **`cmd/ined-gui`:** flags, `gui.toml` and `-version`. The contrast setting is saved line by line, so comments stay.
   - **Small additions:**
     - API map items carry `monitored` (needed for the "· n off" count).
     - New group members start 120 units below their group, so expanded frames don't cover neighbouring labels; seen in a screenshot.
     - `make build-nogui` and `nogui-check`.
     - README GUI section with a screenshot.
   - **Tests:**
     - GUI tests run headless against the real test server with `-race`, through a serial UI queue, because Fyne's test driver runs `fyne.Do` at once.
     - Smoke test of the real binary under Xvfb with screenshots.
     - Benchmark: about 10 ms to lay out and draw 5,000 hosts.
   - **Docs:** Spec and Plan v0.19, D15 recorded. Test cases: 103 passing, 4 partial (visual checks by the user, M6 parts), 2 planned (CI, OpenAPI). `make all` passes.
6. **User**: pushed; do a crosscheck.
   **Claude** ran crosscheck `Crosscheck-2026-11-11-02.md` (the seventh) on M5:
   - 175 tests pass (1 skipped: the screenshot test), and the tracing is complete.
   - **Load probes:**
     - 500 status events with 5,000 hosts loaded take 2.3 s of UI-thread work, because every event re-lays out and redraws the whole view.
     - The real GUI with 2,046 hosts and about 1,600 changes went from 236 MB to 947 MB, stayed busy for about 20 s after the changes stopped, and didn't release the memory.
   - **Findings:** 1 H (C1: coalesce redraws and reuse canvas objects), 2 M (C2: the view list can miss an event while reloading; P1: no burst test), 6 L, plus three decisions:
     - D1: a button border in Max contrast;
     - D2: dragging enough for overlaps, or also a tidy-up action;
     - D3: wider rows for large groups.
   - The fixes wait for the user's OK.
7. **User**: pushed; OK, all.
   **Claude** applied the crosscheck 2026-11-11-02 fixes on `fix/crosscheck-07`:
   - **C1, redraws:** combined (at most every 100 ms per view), and the canvas objects are reused.
     - Test: a burst of 1,000 events on 5,000 hosts went from about 4.6 s to about 15 ms.
     - Repeated real-binary probe: memory +69 MB instead of +710 MB, and no backlog.
   - **C2:** the view list is fetched again if a view event arrives during the fetch.
   - **D3:** rows grow with a group, so 2,046 hosts form a block that fits on screen.
   - **D1, I1:** black button borders in Max; the toolbar shows the zoom.
   - **C5:** new tests for find, the context menu, toolbar zoom, the details panel, and the custom-contrast dialog. `internal/gui` coverage is now 91 %.
   - **Docs:** Spec and Plan v0.20, D16 recorded, TC-714 added. `make all` passes.
