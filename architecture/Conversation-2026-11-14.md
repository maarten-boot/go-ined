# Conversation log — 2026-11-14

1. **User**: we currently have a top bar on the group; moving an item to the top bar of a group makes the item enter the group, that works fine. Now give the group also a bottom bar (can be half the size of the top bar); moving an item to the bottom bar makes the item leave the group. Allow the user to select a colour for the top bar of the group: a fixed palette of 16 or so colours and a custom colour selector.
   (Spelling corrected: "hald" → "half", "pallete" → "palette".)
   **Claude** checked the push of the GUI-platform work: not arrived yet (GitHub still at `433fcde`); the tag `v0.4.1` waits for CI's gui job.
   - Wrote `Design-group-bars.md`: a 12-unit bottom bar on every expanded frame that moves an item inside the group out to the group's parent, landing just below the frame; a "Bar colour ▸" menu with "None", 16 colours and "Custom…" (Fyne's colour picker), stored on the server; label black or white for contrast, AAA adjusts the bar, Max drops the colour. Decisions B1–B8 wait for the user's OK.
2. **User**: pushed; OK, all (B1–B8).
   **Claude** checked the push: the three branches arrived at `2d21de0` (without the design note `0f656b4`). CI on `main` (43589541992): every job passed except `gui (macos-15)`: `realpath: illegal option -- m`.
   - Fixed on `fix/macos-release-build` (merged): the output folder is resolved with `cd … && pwd`, and `shasum -a 256` is used where `sha256sum` is missing (script and CI check). The unpushed tag `v0.4.1` moved to the new `main`.
   - Built the bottom bar and the bar colour on `feature/group-bars` (merged), B1–B8 as recommended:
     - server: `color` on groups (migration 5), validated, upper case, in `/map`, OpenAPI and the API examples; `inedctl group color` and `group add --color`;
     - GUI: a 12-unit bottom bar on every frame; dropping items from inside onto it moves them to the group's parent just below the frame, marked "out" while dragging; "Bar colour ▸" with None, 16 swatches and Custom… (Fyne's colour picker); black or white label, AAA adjusts the bar, Max draws none; a "Bar colour" row in the details panel.
   - Tests TC-735, 736; SPEC-704 v0.42, SPEC-727; `Changes.md`, README.
   - Rendered a map with coloured bars to PNG (a throwaway test, deleted) for AA and Max: as designed.
   - Found: TestDiscoveryDialog fails 6–7 of 10 runs under `-race`, on `main` before this feature too (checked in a temporary worktree, removed). Put in the Plan; not fixed yet.
3. **User**: pushed.
   **Claude** checked: `main`, `docs/architecture`, `fix/macos-release-build` and `feature/group-bars` arrived. CI on `main` (43604749905) green, including the gui jobs for macOS (arm64, amd64), Windows amd64 and Linux arm64.
4. **User**: pushed the tag.
   **Claude** checked: `v0.4.1` on GitHub at `f4d4146`. Release run 43618686176 green; the release has 11 archives and SHA256SUMS, every checksum OK, `inedctl` prints `v0.4.1`, the GUI binaries are of the right architecture (TC-011). Downloads deleted.
5. **User**: were the files in bin rebuilt?
   (Spelling corrected: "rebuild" → "rebuilt".)
   **Claude**: no, `bin/` still held `v0.2.0-5-g812912d-dirty` from 2026-11-12; tests, CI and releases do not write `bin/`. None was running; rebuilt with `make build`: `v0.4.1-4-g217c949`.
6. **User**: rebuild bin after every merge to main.
   **Claude** saved it as a working rule (memory) and in the Plan.
7. **User**: pushed; prepare 0.5.0; after selecting a bar colour the bar does not show the colour and the selector check stays on None.
   (Spelling corrected: "seletor" → "selector", "stayes" → "stays".)
   **Claude** checked the push: not arrived (GitHub still at `217c949`).
   - Bar colour: with the current `bin/` server and GUI (scratch database, Xvfb :97, xdotool) choosing Blue works: the bar turns blue and the details panel says "Blue". With a server built from `v0.4.1` the same click gives `group 1: invalid_argument (HTTP 400): invalid JSON body: json: unknown field "color"` in the status bar and nothing changes, which matches the report. Most likely the running `ined-server` is older than 0.5.0. Test processes stopped, files deleted.
   - Release 0.5.0 prepared: `[0.5.0]` in `Changes.md` with a note to update server and GUI together; OpenAPI 0.5.0; README; annotated tag `v0.5.0`.
8. **User**: when moving a group (now with the bottom bar) the line from the bottom bar is outside the box (but on the same horizontal position); best simply don't show the line when moving.
   (Spelling corrected: "boottom" → "bottom", "dont" → "don't".)
   **Claude**: the drag copy of a frame moved its outline but not its bottom bar, so the line stayed at the old height. Fixed on `fix/drag-foot-line` (merged): no bottom bar line while dragging, back after the drop (SPEC-704 v0.43; TC-736 extended, fails without the fix). All tests, lint, fmt-check and nogui-check pass. The unpushed tag `v0.5.0` moved to include the fix.
9. **User**: best move all the zoom settings in a single top menu and move the group selector to the top menu bar.
   (Read as: "tom menu" → "top menu"; "negroup selector" → "group selector", the "Show" list. To confirm.)
   **Claude** wrote `Design-toolbar.md`: one "Zoom 100 % ▾" menu button (zoom in, zoom out, 100 %, fit all; shortcuts stay), the "Show" list in the toolbar after it, the trail row only while a group is shown, no window menu bar. Decisions T1–T4 wait for the user's OK.
