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
