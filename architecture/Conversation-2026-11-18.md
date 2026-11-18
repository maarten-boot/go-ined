# Conversation 2026-11-18

1. **User**: status?
   **Claude**: all pushed (`main` and `docs/architecture` at `36a194c`), CI green on both, v0.6.0 published and checked, nothing waiting. Open: the user's visual checks, the flaky TestDiscoveryDialog, the SNMP items.
2. **User**: the little red triangle on the bottom right of a host means what? it would be nice to have a explanation in the right details pane when we select the host or a hover ?
   **Claude** explained the service marker (a watched service is down; the glyph still shows the ping status) and wrote `Design-marker-explained.md`: E1 a line in the details pane ("▲ 1 watched service down: https (443) since 14:07"), E2 a hover box on the map (E2a every host, recommended; E2b the triangle only), E3 a Help menu with "Map symbols…", E4 docs. Waiting for the user's OK.
3. **User**: when doing a export the file dialog is not movable or resizable , can we fix that ? and the folders as black is a bit massive, the outline is ok as the symbol is now 40 years part of our culture.
   **Claude**: read as "keep the folder symbol, but outlined instead of filled black". Cause: Fyne's file dialog is drawn inside the main window at a fixed size; Fyne uses the desktop's dialog only in Flatpak builds. Wrote `Design-file-dialogs.md`: F1 the desktop's own dialog (F1a XDG portal on Linux and `zenity` on Windows/macOS with Fyne's dialog as fallback, recommended; F1b Linux only; F1c a larger Fyne dialog only), F2 the fallback at 90 % of the window, F3 outlined folder icons in the text colour. Waiting for the user's OK; the marker design (E1–E4) is waiting too.
4. **User**: i see that in the export the x, y coordinaties are float , is more then one digit after the comma helpful at all as coordinate?
   **Claude**: no: one map unit is one pixel at 100 % and 4 pixels at the maximum zoom (400 %), so 0.1 is under half a pixel. The long fractions come from drags at other zooms (screen movement divided by the zoom), stored as they come. Proposed: the server rounds every saved position to one decimal, and the export rounds too.
5. **User**: yes, add it; OK, all three designs.
   **Claude** wrote `Design-positions.md` (P1–P3, accepted) and marked `Design-marker-explained.md` (E1, E2a, E3, E4) and `Design-file-dialogs.md` (F1a, F2, F3) accepted. Build order: positions, file dialogs, marker help, each on its own branch.
   - Positions built on `feature/round-positions` (merged): every saved position and move rounded to one decimal, the export too (Spec v0.55: SPEC-106, 125; TC-135). A move by 1.7e308 first failed: ×10 overflowed to infinity, so values from 1e15 up are left alone. Two GUI tests compared exact positions (the corner kept on collapse and expand, the drop-out position); they now allow 0.05. API examples regenerated; only ids and times changed.
   - File dialogs built on `feature/file-dialogs` (merged): the XDG portal on Linux, `zenity` on Windows and macOS, Fyne's dialog at 90 % as fallback; outlined folders (SPEC-733 v0.55; TC-752, 753). Rendered to PNG: Fyne paints the symbols of special folders in white over the folder's middle, which cut a line under the tab; the line was dropped. The Windows and macOS code was typechecked against zenity here; CI builds it natively.
   - Marker help built on `feature/marker-help` (merged): the line of services down in the details, a hover box on hosts, "Help ▾ › Map symbols…" (SPEC-734 v0.55; TC-754, 755). Rendered to PNG: the first details render overlapped the title and rows, since the panel wasn't laid out again when the line appeared; fixed. A race under `-race`: the test driver ran the hover's `fyne.Do` on the timer goroutine; the map now uses the GUI's own `do`.
6. **User**: pushed; run the crosscheck.
   **Claude** checked the push (all five refs arrived) and wrote `Crosscheck-2026-11-18-01.md`: two reviewers with read-only access (positions and the 2026-11-17-01 fixes; file dialogs and marker help), Claude the trace, docs and CI, repeating the H and M findings. Findings: 3 H, 8 M, 11 L in the code, 3 L in the docs.
   - H: C1 file names with `+`, `%`, `#`, `?` give another file (the portal package decodes the URI, `firstPath` decodes it again; repeated); C2 a portal answer "ended" panics the GUI (read in the package); C3 export empties an earlier file when the server then fails (since 0.6.0).
   - M: C4 a late error also opens Fyne's dialog; C5 two dialogs at once can swap answers, a hanging portal hangs; C6 Chmod on existing files; C7 a click with the hover box open is lost; C8 the box isn't kept inside the window; C9 reflow saves groups unrounded; C10 a move can reach +Inf (repeated); C11 a service alert stays open with repeats when the service becomes `unknown` (repeated).
   - CI on `main` green (Windows and macOS builds with `zenity` included). `feature/marker-help` failed on a race in `TestFileDialogs` (the test swaps `nativeSave` while a goroutine reads it; L11); `feature/round-positions` on the known flaky TestDiscoveryDialog.
   - Decisions asked: D1 fix C1–C11; D2 fix L1–L8, L11, P1–P3; D3 release 0.7.0 after them.
7. **User**: pushed; OK, all.
   **Claude** checked the push (`main` and `docs/architecture` at `730cfa7`) and fixed the crosscheck on `fix/crosscheck-18` (merged as `67e6006`; `bin/` at v0.6.0-16-g67e6006):
   - File dialogs: the XDG portal called directly over D-Bus, so names with `+%#?` stay as chosen, "ended" is an error and only the own request's answer counts (C1, C2, C5); export reads the server first and replaces the file only when complete, mode 0600 (C3, C6, L2); only a dialog that can't be opened falls back to Fyne's (C4); one file dialog at a time (C5); "larger than 64 MiB" (L1); the race in the test (L11). The portal test runs against a fake portal on a private `dbus-daemon`. A first try with `--session` let that bus start the real `xdg-desktop-portal`; it had exited when checked, no dialog was seen, and the test now uses a config that starts no services.
   - Hover box: a layer of the window without input (C7), kept inside the window (C8), closed when the host changes or on another tab (L3; the map's nodes point into the model, so a copy of the item is compared), its timer stopped (L4).
   - Positions: reflow rounds groups (C9); beyond ±1e9 refused, also as the result of a move, reparent, reflow, copy or import (C10).
   - `ined-alert`: `unknown` ends a service alert without mail (C11); `_renamed` clears `svc_held` (L5). `inedctl`: `--add` by port (L6), export to a device (L7). Watcher: a generation per watching of a service (L8; the test fails with the check off).
   - Docs: Spec v0.56 (SPEC-106, 310, 733, 734, 904, 905, 952); Tests.md (TC-135, 319, 752, 754, 907, 908, 957); `Changes.md` (P2, Fixed); the three designs (P1); crosscheck resolution; Plan.
   - All Go tests (with `-race`) and the 50 Python tests pass; the new GUI tests passed 5 runs in a row.
8. **User**: pushed (then "rerun started" twice, "pushed the tag").
   **Claude** checked the push (`main` and `docs/architecture` at `cf47234`). CI on `main` failed once on the Windows GUI build: the Go module proxy broke off the download of `zenity` (`stream error … INTERNAL_ERROR`), not the code; the user's first rerun didn't register (still attempt 1), the second passed (attempt 2, all 8 jobs). Tag `v0.7.0` (annotated, on `cf47234`) made and pushed by the user. The release workflow built everything but the macOS GUI: GitHub found no macOS arm64 runner within 15 min ("not acquired by Runner of type hosted"), so `publish` was skipped; the user is to rerun the failed jobs.
9. **User**: do we automatically discover new hosts in a network at regular intervals?
   **Claude**: no; scans run only when started (SPEC-201, 707). Repeating a scan is safe (SPEC-204, 205), so cron with `inedctl discover start --wait` works today.
10. **User**: yes, write the design note.
   **Claude** wrote `Design-rescan.md`: R1 the schedule as a setting on the network group (R1a, recommended) or a list in `server.toml` (R1b); R2 due times kept over a restart, one scheduled scan at a time, a manual scan counts and wins, `discovery.scheduled` switch; R3 the same as a manual scan, nothing removed; R4 a "Rescan ▸" menu, a details row, `inedctl group rescan`, not exported; R5 an optional new-host mail in `ined-alert` (R5a, recommended); R6 docs and tests. Waiting for the user's OK.
11. **User**: pushed; OK, all as recommended.
   **Claude** checked the push (`main` and `docs/architecture` at `871e450`); the user's rerun of the 0.7.0 release run (attempt 2) was queued. Built `Design-rescan.md` R1a–R5a on `feature/rescan` (merged; `bin/` at v0.7.0-3-g52b7549):
   - Server: `rescan`, `rescan_last` and `rescan_found` on network groups (schema 8); a scheduler in the discovery manager (every 10 s, one scheduled scan at a time, the one due longest first); every job that ends `done` records the scan for the groups whose whole range it covered; a manual job cancels an overlapping scheduled one; a user's cancel skips the round, a failure waits 5 min; `discovery.scheduled`. New: `GET /discovery` (there was no list), `scheduled` on jobs, `rescan` on map items.
   - Two races found by the new tests and fixed: a scheduled scan could start between a manual job's end and its record. The record now comes before the job is marked finished, and a scheduled start checks under the lock that the group is still due.
   - `inedctl group rescan`, `discover list`, the Rescan row; GUI "Rescan ▸" and the details row; `ined-alert` `[new_hosts] enabled` (mailed once the routes know the host; a host deleted before that isn't).
   - Docs: Spec v0.57 (SPEC-206, 207, 735, 957, SPEC-501, 901, config table); TC-215–219, 756, 958; Changes `[Unreleased]` (schema 8 can't be opened by an older server); README; API examples regenerated (only the new fields); design status.
   - All Go tests with `-race` and the 54 Python tests pass; the scheduler tests 20 runs, the GUI test 5 runs in a row.
   - While the tests ran I switched to `docs/architecture` once by mistake with the code uncommitted; both branches were at the same commit, nothing changed, and I switched straight back.
12. **User**: pushed; run the crosscheck; will do the rerun later mac resources are hogged again possibly by our work.
   **Claude** checked the push (`main` and `docs/architecture` at `8054d74`, `feature/rescan` at `d2d7914`) and wrote `Crosscheck-2026-11-18-02.md`:
   - Two reviewers with read-only access: (1) the server, (2) the clients. Claude did the trace, the docs and CI, and repeated the M findings.
   - Findings: 0 H, 2 M, 7 L, 3 P.
     - C1: one group that can't be scanned (for example after `discovery.max_prefix` was lowered) stops the whole schedule.
     - C2: such a group can't be edited at all.
     - Both were repeated with overlay probes.
   - CI: `main` @ `8054d74` green, macOS included. On the user's guess about the macOS queue: the release's attempt 2 waited at the same time as `871e450`'s macOS job, so two of our own macOS jobs were queued at once, under GitHub's capacity limits.
   - Decisions asked: D1 fix C1, C2; D2 fix L1–L3, P1–P3; D3 rerun the release when no run on `main` is queued; rescan into 0.8.0.
13. **User**: pushed; OK, all.
   **Claude** checked the push (`main` and `docs/architecture` at `0076431`); the release's third attempt was queued. The crosscheck's fixes are on `fix/crosscheck-18-02` (merged as `0596b66`; `bin/` at v0.7.0-9-g0596b66):
   - C1: a group that can't be scanned is passed over, so the others are scanned.
   - C2: other edits of such a group work.
   - L1–L3: the waits end when the setting changes; a future last scan is due; a scan where every probe failed doesn't count.
   - P1–P3: Changes, README and SPEC-957, tests. Spec v0.58.
   - The first merge had `schedule_test.go` not gofmt-ed (seen right after with `gofmt -l`); fixed with a follow-up commit and a second merge before any push.
14. **User**: pushed (then, after the release's third attempt failed: "OK, add the macOS concurrency limit").
   **Claude** checked the push (`main` and `docs/architecture` at `372a8dc`, `fix/crosscheck-18-02` at `b586fab`; CI green on all three).
   - Release attempt 3 failed like the first two: its macOS job (queued 18:32:01) got no runner, while CI's macOS job for `0076431` (queued 18:31:42) got one at 18:41:47.
   - On `fix/ci-concurrency` (merged), CI's gui job got the concurrency group `ci-gui-${{ matrix.runner }}` without cancel-in-progress: one GUI build per runner type, and a newer push replaces one still waiting. TC-010 checks it; SPEC-007 v0.59.
   - The release's own GUI jobs aren't in the group. Otherwise a push to `main` would replace a release job still waiting.
   - actionlint couldn't be run: the session wasn't allowed to download it. The YAML parses and TC-010 passes.
15. **User**: rerun started (attempt 4 of the release run).
   **Claude** watched the run. The macOS job got a runner at once (19:04:10 UTC) and passed, and the run was green; 0.7.0 was published at 19:04:56 UTC.
   - Checked as for 0.6.0: 11 archives and SHA256SUMS, every checksum OK.
   - The linux/amd64 binaries print `v0.7.0`, the darwin and linux/arm64 GUI binaries are of the right architecture, `ined-gui.exe` is in the Windows zip, and the release text equals the `[0.7.0]` section.
   - Plan: "Release 0.7.0" done; TC-011 names the runs.
16. **User**: eye chack: mark all aos ok (confirmed "as": "mark all as ok").
   **Claude** marked every visual check waiting for the user as OK (2026-11-18):
   - Tests.md: TC-005, 702, 705, 708 (manual part), 747, 749, 751, 752 and 754.
   - Plan: the three open items for the visual check and TC-702, and the notes on the dialogs, the marker help, the file dialogs and the rescan menu.
   - Still open in the Plan: the SNMP items and the flaky TestDiscoveryDialog.
