# Design: SNMP checks for groups, and community strings per group and host

Status: **proposed** (2026-11-19), waiting for the user's OK.

User request (2026-11-19), after "how does one start a SNMP scan from the GUI?":
- "Check SNMP now" on a group;
- "a group makes sense as we can configure a group to have the same community string";
- "it would be helpful to allow configuring community strings per group and per host via the GUI".

## What exists (0.8.0)

- The server looks for SNMP agents on its own, with one list for all hosts: `[snmp] communities` in `server.toml`. These are sent only to hosts in `networks` (SPEC-312, 314).
- The GUI's "Check SNMP now" works on one host only (SPEC-736). Through `POST /hosts/{id}/check-snmp` it can take minutes: every community × v2c and v1 × the timeout and retries.
- Communities are secret. They are stored in the database (mode 0600) and never shown by the API, events, export or log.

## Proposal

- **G1 "Check SNMP now" on a group and on a selection.**
  - A group's menu (in any view) and a multi-selection get **"Check SNMP now (N hosts)"**. It covers every host in the group with all its subgroups, or the selected hosts.
  - Such a check can run long, so it runs on the server as a **job**, as discovery does.
    - `POST /snmp/checks` takes `{"group_id": …}` or `{"host_ids": […]}` and answers 202 with the job.
    - `GET /snmp/checks/{id}` and `DELETE` (cancel) work on it.
    - The events are `snmp.check.progress` and `snmp.check.finished`, with checked, total, found and no answer.
    - The job checks at most `snmp.concurrency` hosts at once.
  - The GUI shows its progress in the status bar, with a cancel button, as for a discovery job. The end says "SNMP: 12 of 40 hosts answered".
  - `inedctl snmp check --group ID | HOST-ID…` does the same; `--wait` follows it.
  - A host outside `networks` with no community of its own (G2) is skipped and counted as "skipped".
- **G2 Communities per group and per host, set in the GUI.**
  - A network-view group and a host can each have their own list of community strings.
    - **Where:** network-view groups only, because a host has exactly one place there, so "which group's list counts" is always clear. Groups in custom views have none.
    - **Lookup:** for a host, the server uses the first of: the host's own list; the list of the nearest group above it in the network view that has one; the server's `communities`.
  - **Setting them:**
    - **GUI:** **"SNMP communities…"** on a host's and on a network group's menu. It opens a dialog with one masked field per string (a "show" eye per field), "Add", "Remove", and **"Inherit"** to clear the list.
    - **API:** `PATCH /hosts/{id}` or `/groups/{id}` with `"snmp_communities": ["a","b"]`; `[]` means inherit.
    - **`inedctl`:** `host snmp-communities <id> a,b | --clear` and the same for `group`. A `-` instead of the list reads them from stdin, so they stay out of the shell history.
  - **Write-only:** the strings are never read back, not even with the full token. The API, `inedctl` and the GUI show only where the list comes from:
    - `snmp_communities_set` (the number set here, 0 = none);
    - and on a host, `snmp_communities_from`: `"host"`, `"group 12"` or `"server"`.

    The dialog says "2 set here", "from group lan (3)" or "the server's list". Editing replaces the whole list; you can't see what was there.
  - **Limits:** at most 10 per list, each 1–255 bytes. Not in the export file (secret).
  - **Effect:** a host whose list changes, its own or through a group, is looked for again at once with the new list (SPEC-312's counts start again). The log says "SNMP communities set on group 12 (2)", never the strings.
  - **Storage:** schema 10, a column `snmp_communities` on groups and on hosts, as JSON. The database file stays private (SPEC-314).
- **G3 Which list is tried.**
  - **G3a (recommended):** the most specific list **replaces** the others, so a group with "lab-ro" sends only "lab-ro" to its hosts, never the server's "public". This is what "a group has its community string" means, and it sends fewer strings to fewer hosts.
  - **G3b:** the specific list first, then the server's list.
- **G4 `networks` and lists you set.**
  - **G4a (recommended):** a list set on a host or group is used **also outside `networks`**. You named those strings for those hosts; `networks` keeps the server's own list off foreign hosts.
  - **G4b:** `networks` applies to every list.
- **G5 Showing the strings again.**
  - **G5a (recommended):** never, as above. Whoever knows them types them again.
  - **G5b:** with the full token, the API and the dialog can show them. This is easier to manage, but it puts the secret in API answers and screenshots.
- **G6 Docs and tests.**
  - **Spec:** SPEC-312 and 314 (the lookup, the `networks` rule), a new SPEC-315 (communities per group and host), SPEC-515 (the check jobs and the PATCH fields), SPEC-737 (the GUI dialog and menu items), and SPEC-901 (`inedctl`).
  - **Tests:**
    - the lookup (host, nearest group, server) and moving a host to another group;
    - replace (G3a) and the `networks` rule (G4a);
    - write-only: no string in any answer, event, export or log;
    - the job with progress, cancel and the skipped count;
    - the dialog (masked fields, inherit, the shown origin) and the menu items;
    - `inedctl` with stdin;
    - migration 10.

## Not now

- SNMPv3 users and keys per group (the same dialog could take them later).
- A community list per custom-view group.
- Showing the strings in any form.

## Decisions for the user

- **G1:** "Check SNMP now" on groups and selections as a server job with progress and cancel: OK / other.
- **G2:** lists per network-view group and per host, write-only, set in the GUI, `inedctl` and the API: OK / other.
- **G3:** G3a the most specific list replaces (recommended) / G3b specific first, then the server's.
- **G4:** G4a lists you set are used also outside `networks` (recommended) / G4b `networks` always.
- **G5:** G5a never shown again (recommended) / G5b shown with the full token.
