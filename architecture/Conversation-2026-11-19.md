# Conversation log 2026-11-19

1. **User**: status?
   **Claude** (a new session): the release commit `203cc55` is on GitHub, but CI on `main` failed for it and for `a06116a`. The cause is staticcheck ST1018: `TestOneLine` (`internal/snmp/client_test.go`) held the Unicode format characters U+202E, U+200B and U+2066 themselves, not as `\u` escapes; the shell wrote them literally.
   - Fixed on `fix/staticcheck` (merged). staticcheck v0.8.2, CI's version, was run locally over the whole repository and is clean.
   - The tag `v0.8.0` goes on the commit after this fix, once CI is green. The release commit's content is unchanged.
2. **User**: pushed (then "pushed the tag").
   **Claude** checked the push (`main` and `docs/architecture` at `9738e02`). CI on `main` was green (run 44502615260), so the annotated tag `v0.8.0` was made on `9738e02` and pushed by the user.
   - The release run 44507479025 was green on the first attempt; its macOS job waited 11 min for a runner. 0.8.0 was published at 11:04 UTC.
   - Checked as before: 11 archives and SHA256SUMS, every checksum OK. The linux/amd64 binaries print `v0.8.0`, the darwin and linux/arm64 GUI binaries are of the right architecture, `ined-gui.exe` is in the Windows zip, and the release text equals the `[0.8.0]` section.
   - Releases and artifacts (the user's rule): 10 releases, no CI artifacts stored, so there is nothing to clean up.
3. **User**: how does one start a snmp scan from the gui?
   **Claude**: there is no network-wide SNMP scan in the GUI. The server looks for agents by itself once `[snmp] enabled = true`. The GUI has "Check SNMP now" for one host, and the results are in the details. Offered a design for groups and selections.
4. **User**: yes, write the design note; yes a group makes sense as we can configure a group to have the same community string, it would be helpfull to allow configuring community strings per group and per host via the gui.
   **Claude** wrote `Design-snmp-gui.md`:
   - G1: "Check SNMP now" on groups and selections, as a server job with progress and cancel (`POST /snmp/checks`, `inedctl snmp check`).
   - G2: write-only community lists per network-view group and per host, set in a GUI dialog with masked fields, `inedctl` (stdin) and the API; the host's own list, then the nearest group's, then the server's; schema 10.
   - G3: the most specific list replaces the others (G3a, recommended) or comes first (G3b).
   - G4: lists you set are used also outside `networks` (G4a, recommended).
   - G5: the strings are never shown again (G5a, recommended).
   - Waiting for the user's OK.
5. **User**: pused all (confirmed: pushed, and "OK, all as recommended" for `Design-snmp-gui.md`).
   **Claude** checked the push (`main` and `docs/architecture` at `af7d6b1`) and built G1–G6 on `feature/snmp-groups` (merged as `1657b2d`):
   - **Server:** community lists on network-view groups and hosts (schema 10), write-only, with the count and origin in JSON. The lookup is host, then nearest group, then server, and the most specific list replaces the others. Lists you set go outside `networks` too. A changed list is looked for again at once.
   - **Check jobs:** `POST/GET/DELETE /snmp/checks` with progress and finished events, cancelled at shutdown.
   - **`inedctl`:** `snmp-communities` (also from stdin), `snmp check|list|status|cancel`.
   - **GUI:** "Check SNMP now (N hosts)" on groups and selections, progress and "Cancel SNMP check" in the status bar; "SNMP communities…" dialog with masked fields; details rows.
   - **Fixed on the way:** a data race the new tests found (the progress throttle of a job was shared between goroutines), and a list order that depended on equal start times.
   - **Tests:** the full Go suite with `-race`, staticcheck v0.8.2, `make nogui-check` and the 65 Python tests pass.
   - **Docs:** Spec v0.65 (SPEC-315, 515, 737, and the additions to SPEC-312, 502, 804, 901); TC-328, 329, 758, 910; Changes; README; API examples; design status.
6. **User**: pushed; run the crosscheck.
   **Claude** checked the push (`main` and `docs/architecture` at `aa2be31`, `feature/snmp-groups` at `e7e0238`) and wrote `Crosscheck-2026-11-19-01.md`:
   - Two reviewers with read-only access: (1) the server side, (2) the GUI, `inedctl` and the docs.
   - Findings: 2 H, 2 M, 12 L, 1 P. No leak of a community string was found.
     - C1: the GUI's "Cancel SNMP check" can stay for good when the finished event comes before the start answer.
     - C2: `inedctl --json snmp check --wait` exits 1 at once.
     - C3: a changed list is lost for hosts past the concurrency limit.
     - C4: the group menu's host count is quadratic, under the GUI's lock.
     - Claude repeated C2 and C4 with probes, and checked C1 and C3 in the code, with the reviewers' probes.
   - CI on `main` @ `aa2be31` green.
   - Decisions asked: D1 C1–C4; D2 L1–L4, L6–L11, P1.
7. **User**: pushed; OK, all.
   **Claude** checked the push (`main` and `docs/architecture` at `39c7148`) and fixed the crosscheck on `fix/crosscheck-19-01` (merged as `c5e686d`; see its section 7). Spec v0.66.
   - Tests: the full Go suite with `-race`, staticcheck v0.8.2, `make nogui-check` and the 65 Python tests pass.
   - On the way, gofmt had realigned the `GUI` struct's fields, so three edits to `gui.go` didn't match at first. They were applied to the actual text before the build.
8. **User**: pushed; run the crosscheck.
   **Claude** checked the push (`main` and `docs/architecture` at `7993040`, `fix/crosscheck-19-01` at `20c1500`) and wrote `Crosscheck-2026-11-19-02.md`:
   - One reviewer with read-only access, since the scope was one fix commit. Every earlier fix works.
   - Findings: 0 H, 3 M, 5 L, 1 P.
     - C1: the per-host events of a group's new list can force a resync on every client (the buffer holds 256).
     - C2: the GUI stops following a check on any refresh error.
     - C3: Cancel during a save hides a failure.
     - Claude checked all three in the code.
   - CI on `main` @ `7993040` green.
   - Decisions asked: D1 C1–C3; D2 L1–L5, P1.
9. **User**: pushed; OK, all.
   **Claude** checked the push (`main` and `docs/architecture` at `8e0e2ed`) and fixed the crosscheck on `fix/crosscheck-19-02` (merged as `b3e0b87`; see its section 7). Spec v0.67.
   - The server no longer sends one event per host for a group's new list; the GUI reads the shown host again when a group above it changes or moves.
   - Tests: the full Go suite with `-race` and staticcheck v0.8.2 pass. The new C1 test fails against the old GUI code, as it should.
   - A first C2 test swapped the GUI's context while a goroutine could read it, which would race; it was replaced before the run by a test against a server with SNMP off (503).
10. **User**: pushed; run the crosscheck.
   **Claude** checked the push (`main` and `docs/architecture` at `55ddfb8`, `fix/crosscheck-19-02` at `e04ec30`) and CI on `main` (green, all 8 jobs), and wrote `Crosscheck-2026-11-19-03.md`:
   - One reviewer with read-only access. Every fix of 2026-11-19-02 works.
   - Findings: 0 H, 2 M, 5 L, 1 P.
     - C1: an ancestor group's event while the details pane loads another item can show a host under a group's title. Claude reproduced it with its own probe (2 of 4 runs).
     - C2: a 503 from the server (SNMP off after a restart) keeps a check followed for good, with its Cancel button.
   - Decisions asked: D1 C1, C2; D2 L1–L5, P1.
11. **User**: "snmp communities has inherit, that is nice if i can set a community on a group or a view but i cant it seems , can i ?"
   **Claude** explained: a network-view group can have a list ("SNMP communities…" on its menu, or `inedctl group snmp-communities`); groups in custom views and whole views can't; "Inherit" removes the list set on the item, so the nearest group's list or the server's is used.
12. **User**: "as i create new views on hosts with similar capabilities it would be very helpful if i can set communities on a view", "and then use inherit".
   **Claude** wrote `Design-snmp-views.md`: lists on custom views, which hosts get by inheriting (V1–V6); decisions asked. The crosscheck 2026-11-19-03 decisions are still open too.
13. **User**: pushed; OK, all as recommended (the view design V1–V6, and D1, D2 of crosscheck 2026-11-19-03).
   **Claude** checked the push (`main` and `docs/architecture` at `c8186e1`), fixed the crosscheck first on `fix/crosscheck-19-03` (merged as `16ebf4f`; see its section 7; Spec v0.68), then started the view lists on `feature/snmp-views`.
   - The first test edit didn't match: gofmt had wrapped a line of the test since. The test was rewritten from the file as it stood.
   - Tests: the full Go suite with `-race` and staticcheck v0.8.2 pass. The new C1 test fails against the old code.
14. **Claude**, continuing entry 13, built the view lists on `feature/snmp-views` (merged as `05dce9d`; Spec v0.69):
   - **Server:** schema 11, `PATCH /views/{id}` with `snmp_communities`, the lookup host → first view in tab order → network group → server, "view N" as the origin.
   - **GUI:** "SNMP communities…" in a custom view's View menu, "from view switches".
   - **`inedctl`:** `view snmp-communities`; `view list` and `view show` show the count.
   - **Tests:** TC-330, 760, 911. The full Go suite with `-race`, staticcheck v0.8.2 and the 65 Python tests pass.
   - **On the way:**
     - Two edit scripts didn't match the text and were redone against it: a comment said "group lan", not "lab".
     - The API tests had no log capture, so one was added to check that no string reaches the log.
     - The GUI test first assumed the details pane keeps its host across a tab switch; a new tab shows its own selection, which is right, so the test selects the host in the view.
15. **User**: pushed; run the crosscheck.
   **Claude** checked the push (`main` and `docs/architecture` at `98ecf43`, `fix/crosscheck-19-03` at `3751323`, `feature/snmp-views` at `2c66977`) and wrote `Crosscheck-2026-11-19-04.md`:
   - Two reviewers with read-only access: the server side, and the GUI, `inedctl` and the docs.
   - The server side has no H or M findings. 5,000 hosts with 10 views take 4.4 ms per round of the SNMP lookup.
   - Findings: 0 H, 2 M, 6 L, 2 P.
     - C1: an event while the details pane loads a host is lost.
     - C2: a 401 (wrong token) stops following an SNMP check.
     - Claude confirmed both in the code; reviewer 2 had reproduced them.
   - One speed probe of reviewer 1 was still running when it reported; Claude left it, as it ends by itself and writes nothing to the repo.
   - CI on `main` @ `98ecf43` green.
   - Decisions asked: D1 C1, C2; D2 L1, L3–L6, P1, P2.
16. **User**: pushed; OK, all.
   **Claude** checked the push (`main` and `docs/architecture` at `568a4a0`) and fixed the crosscheck on `fix/crosscheck-19-04` (merged as `0ce8164`; see its section 7). Spec v0.70.
   - Tests: the full Go suite with `-race` and staticcheck v0.8.2 pass. Both new tests fail without their fix.
   - On the way:
     - The first C1 test held the request rather than the server's answer, so it passed without the fix. It was changed to hold the answer, and now fails without the fix.
     - One test edit didn't match its anchor and was written as a new test instead.
   - The speed probe of the server reviewer has ended by itself.
17. **User**: pushed; run the crosscheck.
   **Claude** checked the push (`main` and `docs/architecture` at `a9cdba1`, `fix/crosscheck-19-04` at `a7eaca4`) and wrote `Crosscheck-2026-11-19-05.md`:
   - One reviewer with read-only access; its probes had to end before it reported.
   - Every fix of 2026-11-19-04 works, and the new tests passed 15 runs in a row.
   - Findings: 0 H, 1 M, 5 L, 1 P.
     - C1: an older `show()` answer can replace newer data in the details pane. Claude confirmed it in the code; part of it existed before.
     - The reviewer reported a second M: a group's dialog stays open when its view is deleted. Claude found that the GUI offers the dialog only for network-view groups, and the network view can't be deleted, so it is L5.
   - CI on `main` @ `a9cdba1` green.
   - Decisions asked: D1 C1; D2 L1, L3–L5, P1.
18. **User**: pushed; OK, all.
   **Claude** checked the push (`main` and `docs/architecture` at `32862b4`) and fixed the crosscheck on `fix/crosscheck-19-05` (merged as `f0c0918`; see its section 7). Spec v0.71.
   - Tests: the full Go suite with `-race` and staticcheck v0.8.2 pass. Both new tests fail without their fix.
19. **User**: pushed; run the crosscheck.
   **Claude** checked the push (`main` and `docs/architecture` at `cade2ed`, `fix/crosscheck-19-05` at `b8bcf1b`) and wrote `Crosscheck-2026-11-19-06.md`:
   - One reviewer with read-only access; the scope was one fix commit.
   - Findings: no H or M, 3 L (a note typed during a load is lost as before, a 300 ms wait in a test, a comment).
   - CI on `main` @ `cade2ed` green. The watcher ended before the macOS job had reported; Claude checked the run again: all 8 jobs green.
   - Decision asked: D1 L1–L3, optional, recommended together with the next change.
20. **User**: pushed; OK, all.
   **Claude** checked the push (`main` and `docs/architecture` at `757b886`) and fixed L1–L3 of crosscheck 2026-11-19-06 now, as they were small, on `fix/crosscheck-19-06` (`5ea9a33`, merged as `4483862`). L1 was fixed rather than only commented: a note typed while a host loads is kept. Spec v0.72.
   - Tests: the full Go suite with `-race` and staticcheck v0.8.2 pass. The changed test fails with the old behaviour.
21. **User**: pushed; run the crosscheck.
   **Claude** checked the push (`main` and `docs/architecture` at `4cc2539`, `fix/crosscheck-19-06` at `5ea9a33`) and wrote `Crosscheck-2026-11-19-07.md`:
   - One reviewer with read-only access. The 2026-11-19-06 fixes work.
   - The reviewer reported as L that "Save note" stays enabled with the previous item's text while a new item loads. Claude's probe showed that this loses data: host 1's note was saved onto host 2. It is old (since notes were added), and is C1 (H) here.
   - Findings: 1 H, 0 M, 2 L, 1 P.
   - CI on `main` @ `4cc2539` green.
   - Decisions asked: D1 C1; D2 L1, L2, P1.
22. **User**: pushed; OK, all.
   **Claude** checked the push (`main` and `docs/architecture` at `f4e0b30`) and fixed the crosscheck on `fix/crosscheck-19-07` (`3083d9c`, merged as `ed4b352`; see its section 7). Spec v0.73.
   - A new selection now empties and disables the note and Save until its note is in.
   - Tests: the full Go suite with `-race` and staticcheck v0.8.2 pass. Both new tests fail without their fix.
23. **User**: pushed; run the crosscheck.
   **Claude** checked the push (`main` and `docs/architecture` at `cf6344e`, `fix/crosscheck-19-07` at `3083d9c`) and wrote `Crosscheck-2026-11-19-08.md`:
   - One reviewer with read-only access, asked for every way a note can be lost.
   - The 2026-11-19-07 fixes work.
   - Findings: 1 H, 1 M, 2 L, 1 P.
     - C1 (H, old): selecting the same item again, as a tab switch does, wipes unsaved typing in the note. Claude reproduced it with its own probe.
     - C2 (M): a save on its way when the selection changes marks the new item's note as edited.
   - CI on `main` @ `cf6344e` green.
   - Decisions asked: D1 C1, C2; D2 L1, P1; D3 what to do with unsaved typing when switching to another item (recommended: a status-bar warning).

