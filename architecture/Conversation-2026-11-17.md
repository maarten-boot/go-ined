# Conversation log — 2026-11-17

1. **User**: status?
   **Claude**: nothing in progress; GitHub one commit behind (the accepted export/import design); summed up yesterday's work and the open items, and asked whether to build the export/import or do the crosscheck first.
2. **User**: pushed; start the export/import build.
   **Claude** checked the push: `main` and `docs/architecture` at c2b194b. Built `Design-network-export.md` on `feature/network-export` (merged):
   - inventory (`transfer.go`):
     - `ExportGroup` builds the file;
     - `Import` checks the file like API input, plans on a copy of the model and applies one `Changes`.
     - Groups and hosts are matched through lookups made once, so a 10,000-item file doesn't scan the model per item.
   - API:
     - `GET /groups/{id}/export` (the read token may export);
     - `POST /import` with `parent_id`, `x`, `y`, `dry_run`, `update` and a 32 MiB body limit for that path only.
     - A 409 at first had only the report. The API test rejected it, since every error needs the SPEC-504 body. It now has `{error, report}` (`ImportConflict`).
   - Client: `ExportGroup` and `Import`. `Import` takes the file as it is, so the server sees unknown keys; on a 409 it returns the report with the error.
   - `inedctl`:
     - `group export <id> [-o FILE]` writes the file with mode 0600, since notes can be private;
     - `import FILE|-` exits 1 when the import is refused.
     - A missing file first exited 3, because Go's file error looks like a network error. It is now a usage error (2).
     - The report's wording is shared with the GUI (`ImportReport.Lines`).
   - GUI: "Export…" and "Import here…" on network-view groups, "Import here…" on empty space; the dry run's report first, then "Import" or "Update existing and import".
   - Tests: TC-130–134, 513, 751, 908. The first test comments used TC-125–129 and TC-905, which were taken. They were renumbered before anything was pushed, by amending the unpushed commit and merging again.
   - Spec v0.53: SPEC-125, 513, 733, 905; a "later" line about `.tki`. The API examples have an "Export and import" section. Their curl lines now quote the URL, so `?x=900&y=100` can be pasted into a shell.
   - Checked end to end with two real servers:
     - the dry run reported the skipped "Lab" view and the more specific 10.0.0.0/16;
     - the import went below 10.0.0.0/16, with "app servers" made in Payroll and the host placed there;
     - the same file again was refused (4 conflicts, exit 1);
     - a second export equalled the first, apart from the Lab placement.
   - All Go tests (`-race`), lint and the 40 Python tests pass.
3. **User**: pushed; run the crosscheck.
   **Claude** checked the push: `main`, `docs/architecture` and `feature/network-export` arrived. Wrote `Crosscheck-2026-11-17-01.md`, the first since 2026-11-13-03, covering 16 merged branches.
   - Method:
     - three reviewers with read-only access: alerting, watched services, export and import;
     - Claude: the GUI items, the trace, the docs, and repeating every M finding.
     - CI passed for `main` @ `e04ef54`.
   - Findings: 0 H, 6 M, 11 L in the code, 7 L in the docs.
     - C1: `ined-alert` stops on a state file of the wrong shape instead of moving it to `.bad`.
     - C2: a recovery is mailed unchecked when the server can't be read.
     - C3: with `services.enabled = false`, watched services keep their old state instead of `unknown`.
     - C4: import with update leaves matched groups in place without a warning below the top; N8 says positions are set.
     - C5: two "Import here…" items in a group's menu while a focus is set.
     - C6: a DOWN mail dropped after the retries still leaves the alert open.
     - Docs: TC-315 is used twice; the README's status stops at "Connect".
   - Decisions asked: D1 fix C1–C6; D2 fix the L items except L11; D3 release 0.6.0 after the fixes.
