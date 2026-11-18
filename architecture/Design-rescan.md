# Design: scanning networks again on a schedule

Status: **implemented** (2026-11-18): R1a–R5a as recommended ("OK, all as recommended"), on `feature/rescan` (SPEC-206, 207, 735, 957; Spec v0.57). Settled in the build:
- The schedule is checked every 10 s (not every minute), so a new setting starts within seconds.
- The longest interval is 8760 h. A CIDR change that would leave a rescan on a group that can't have one is 400; another IPv4 range clears the last scan.
- There was no list of jobs, so `GET /discovery` and `inedctl discover list` were added. Jobs carry `scheduled`, and map items of groups carry `rescan` for the menu's tick.
- `inedctl group rescan <id> 24h|off`, like `group color`, instead of a flag on `group edit`.
- A job records its scan before it is marked finished, and a scheduled start checks under the lock that its group is still due. Without that, a scheduled scan could start right after a manual one that covered it (found by the test).
- A scheduled scan cancelled by a user skips the round. One that failed is tried again after 5 min. Both waits are kept in memory only.
- The new-host mail waits until `ined-alert` has loaded its routes again after the event, so the group's route gets it. Pending new hosts aren't kept in the state file.
- Schema 8: an older server can't open the database any more (`Changes.md` says so).

User question: "do we automatically discover new hosts in a network at regular intervals?" Answer: no. A scan runs only when started (toolbar "Discover…", "Scan this network", `inedctl discover start`, `POST /discovery`; SPEC-201, 707). The user asked for this design.

## What exists

- A scan of a range already in the inventory is safe to repeat: a known IP address isn't added twice and its host isn't moved (SPEC-204). A scan that overlaps a running one is refused with 409 (SPEC-205).
- Jobs live in memory only; the last 100 are kept (SPEC-205).
- The server already repeats other work on its own: ping (`monitor.interval`), reverse DNS (`dns.refresh`), the service check (`services.refresh`, 6 h).
- A cron job running `inedctl discover start <cidr> --wait` works today, but the schedule then lives outside go-ined, one line per range, and nobody sees it in the GUI.

## Proposal

- **R1 Where the schedule lives.**
  - **R1a (recommended): a setting on the network group.**
    - A group with an IPv4 `cidr` gets `rescan`: off (`""`) or an interval as a Go duration (`"6h"`, `"24h"`, `"168h"`).
    - The shortest interval is 15 min. It is refused with 400 on a group without a CIDR, on an IPv6 network, and on a range larger than `discovery.max_prefix`.
    - It is saved with the group (schema 8), so it survives a restart and is the same in every GUI, like the bar colour (SPEC-727).
    - It is changed through `PATCH /groups/{id}` and gives `group.updated`.
    - The network group says what to scan, so the setting has no range of its own to get out of step.
  - **R1b:** a list of ranges with intervals in `server.toml`.
    - Simpler on the server, but it can only be seen and changed in the config, and a restart is needed for a change.
- **R2 When a scan runs.**
  - **Due time:** the group stores when its last scheduled scan ended (`rescan_last`, kept over a restart). A scan is due at `rescan_last + rescan`. A new setting, or one never run, is due at once.
  - **After a restart:** due scans don't all start together. They are started in order of due time, the rule below takes care of the rest, and the server does not scan the whole inventory at start.
  - **Manual scans count:** a manual scan that finishes `done` and covers the group's whole range also sets `rescan_last`, so a scan by hand isn't repeated straight after.
  - **Load:** one scheduled scan at a time. It uses the same `discovery.rate` and concurrency as a manual one.
    - A scheduled scan that would overlap a running job waits and tries again a minute later.
    - A manual job is never refused because of a scheduled one: the scheduled job is cancelled (hosts found so far are kept, SPEC-205) and retried after the manual job.
  - **Config switch:** `discovery.scheduled = true` in `server.toml` turns all of it off at once without touching the groups. `false` keeps the settings but runs nothing.
  - **Scale:** a /24 takes about 1.3 s at 200 probes/s, a /16 about 5.5 min.
- **R3 What a scheduled scan does.**
  - The same as a manual scan (SPEC-204): new hosts are added, monitored, with `host.created`; known hosts stay where they are.
  - Hosts that no longer answer are left alone: monitoring already marks them down, and removing hosts automatically is not proposed.
  - The job shows in `GET /discovery` like any other, with `scheduled: true` and the group's id.
  - **Group fields:** the group gets `rescan_last` and `rescan_found` (new hosts in the last scheduled scan). Both are read-only in the API.
- **R4 Showing it.**
  - **Context menu:** a network group's menu gets **"Rescan ▸ Off / Every hour / Every 6 hours / Every day / Every week"**, with the current choice checked. An interval set elsewhere to another value shows as an extra checked item ("Every 3h").
  - **Details pane:** a row "Rescan: every day, last 03:15, 2 new" (or "off").
  - **Status bar:** it doesn't show scheduled scans, so the GUI stays quiet; their progress is in `discovery.progress` events, as now.
  - **`inedctl`:** `group rescan <id> 24h|off`, like `group color`. `group show` prints the row, and `discover status` lists scheduled jobs like the others.
  - **Export:** `rescan` is **not** exported (SPEC-125). An import elsewhere should not start scanning a network on its own.
- **R5 Telling someone about new hosts.**
  - **R5a (recommended):** `ined-alert` gets an optional **new-host mail**: `[new_hosts] enabled = false` by default.
    - When it is on, a `host.created` in a routed group (SPEC-953) is mailed as "[ined] NEW 10.0.1.7 (printer-3) in Networks/lan", batched like the other alerts (SPEC-954).
    - Hosts added by hand are mailed too: "a host appeared" is the useful fact, whoever added it.
  - **R5b:** nothing now; new hosts are seen on the map.
- **R6 Docs and tests.**
  - **Spec:** SPEC-206 (the schedule), SPEC-207 (load and the config switch), SPEC-122/501 (the group fields), SPEC-735 (GUI), SPEC-902 (`inedctl`), SPEC-957 (the new-host mail), and the config table.
  - **Tests.md:**
    - the schedule with a fake clock: due, not due, after a restart, a manual scan counting;
    - waiting for and cancelling on an overlap;
    - the 400 cases;
    - the menu and the details row;
    - the `ined-alert` mail.

## Not now

- Removing or flagging hosts that haven't answered for a long time.
- Scans by TCP or ARP for hosts that block ICMP (already LATER in the Spec).
- Different times of day ("only at night"). The interval counts from the last scan, and a first scan at a quiet hour keeps the rhythm.

## Decisions for the user

- **R1:** R1a a setting on the network group (recommended) / R1b a list in `server.toml`.
- **R2:** one scheduled scan at a time, a manual scan cancels a scheduled one, and a 15-min minimum: OK / other.
- **R4:** menu choices off, 1 h, 6 h, 1 day, 1 week, and no export of the setting: OK / other.
- **R5:** R5a the optional new-host mail in `ined-alert` (recommended) / R5b none.
