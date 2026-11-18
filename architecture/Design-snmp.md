# Design: SNMP (finding agents, reading values, thresholds)

Status: **implemented** (2026-11-18). Part 1, N1–N6 as recommended ("OK, all as recommended"), on `feature/snmp` (SPEC-312–314, 514, 736; Spec v0.60). Part 2, N7 and N8, on `feature/snmp-thresholds` (SPEC-958; Spec v0.63). Part 2 also has the metric `cpu_max`, the busiest CPU, added after the user's question on CPU % with several CPUs (v0.62). A HIGH mail is sent only after the value is read from the server once more; OK is at or below the line; two rules for one metric use the lower line; the state file goes to version 3.

Settled in the build of part 1:
- gosnmp v1.47.0 (2026-10-23). `go mod tidy` also raised `testify` to 1.12.1, used only in tests (Fyne's and gosnmp's).
- `snmp.port` was added (161), for agents on another port and for the tests.
- An SNMPv1 agent answers a request with one unknown name with an error for the whole request, so `sysDescr` is then asked alone.
- `snmp.values` also counts a changed disk size as a change.
- The GUI's SNMP row shows version, sysName and sysDescr. The status bar repeats it after "Check SNMP now".
- Correction to N3's load estimate: a host without an agent costs every community and version times the timeout and retries (3 × 2 × 2 s = 12 s). With 20 hosts at once, 5,000 such hosts take about 50 minutes the first time, not "a few minutes". `networks` keeps that to your own ranges.

User request (2026-11-18): "start with snmp". It picks up the two open SNMP items of the Plan:
- `Design-connect.md` C7, Spec §13: "Later we can add an SNMP scan for all hosts and a set of default community strings to try."
- `Design-alerting.md` A10: "after SNMP monitoring, also alerts on values such as disk > 90 % or CPU > 80 %".

This note covers both, in two parts that are built one after the other: **part 1** finds the agents and reads the values on the server; **part 2** adds thresholds to `ined-alert`.

## What exists

- The server already checks every monitored host on its own: ping, reverse DNS, the TCP service check every 6 h (SPEC-309), and watched services every 5 min (SPEC-310). SNMP fits the same pattern: a check per host on the server, a stored result, and a setting.
- Nothing speaks SNMP yet. Go's standard library has no SNMP; writing the BER encoding by hand is a lot of code for little gain.

## Proposal, part 1: the server

- **N1 Library: `github.com/gosnmp/gosnmp`.** Pure Go (no cgo, so `make nogui-check` stays clean), BSD-2-Clause, widely used, and covers v1, v2c and v3. It's a new direct dependency, pinned in `go.mod` and checked by `govulncheck` in CI like the others.
- **N2 Off unless you turn it on; where it may try.**
  - Community strings are sent in clear text to every host they are tried on. A host that isn't yours learns them that way. So SNMP is off by default.
  - `[snmp]` in `server.toml`:
    - `enabled = false`;
    - `communities = ["public"]`, tried in this order;
    - `networks = []`: the ranges where communities may be tried, such as `["10.0.0.0/8"]`; empty means everywhere;
    - `timeout = "1s"`, `retries = 1`, `concurrency = 20`;
    - `interval = "5m"` (how often values are read, N4);
    - `rescan = "24h"` (how often a host without SNMP is tried again).
  - **N2a (recommended):** the global switch with the `networks` list. **N2b:** a per-group setting in the GUI, like "Rescan ▸". It's more to build, and a list in the config is enough to keep strings off foreign networks.
- **N3 Finding the agent.**
  - For a monitored host in `networks`: try each community, first with v2c and then with v1, by reading the **system group**: `sysName`, `sysDescr`, `sysObjectID`, `sysUpTime`, `sysContact` and `sysLocation`. The first answer wins.
  - The host then has an SNMP version, the community that worked, the system values and `snmp_checked_at` (schema 9). A host where nothing answers gets `snmp_checked_at` only.
  - **When:** soon after a host is added or discovered; again every `rescan` for a host without SNMP, so a new agent is found; at once on request; and with the whole list again when the stored community stops working.
  - **The community is never shown.** It isn't in the API (not even with the full token), the event stream, the export file or the log; the API says only `"version": "v2c"`. A user who wants to know which one worked sees it in `server.toml`'s order and the log line "SNMP found on 10.0.1.5 (v2c, community 2 of 3)".
  - **Load:** at most `concurrency` hosts at once, one UDP request per community and version, and 1 s timeout. 5,000 hosts with three communities is about 30,000 requests the first time, spread over a few minutes, and none afterwards for hosts that answered.
- **N4 Reading values.**
  - For a host with an agent, every `interval` (5 min), from HOST-RESOURCES-MIB:
    - **CPU:** the average of `hrProcessorLoad` over the processors, in %;
    - **storage:** each fixed disk and the RAM from `hrStorageTable`: name (`/`, `/data`, `C:\`, "Physical memory"), size and % used;
    - **uptime:** `sysUpTime`.
  - A device without HOST-RESOURCES-MIB (most switches and printers) keeps only its system values. That's normal, not an error.
  - **Only the latest values are kept, in memory.** After a restart they come back with the first round. No history and no graphs (see "Not now").
  - **Events:** `snmp.values` with the host id and the values, only when a rounded value changes (whole %, uptime left out). That keeps the stream small, since a busy disk changes rarely in whole percents. The events buffer (1,000) is unchanged.
- **N5 Showing it.**
  - **API:** host JSON gets `snmp`:
    ```json
    {"version": "v2c", "name": "web", "descr": "Linux web 6.8.0 …", "object_id": "1.3.6.1.4.1.8072.3.2.10",
     "contact": "", "location": "rack 4", "checked_at": "…",
     "values": {"at": "…", "uptime_s": 1053224, "cpu_pct": 12,
                "storage": [{"name": "/", "kind": "disk", "size_bytes": 53687091200, "used_pct": 91}]}}
    ```
    `null` while never checked; `{"version": "", "checked_at": …}` when nothing answered.
  - **New calls:** `POST /hosts/{id}/check-snmp` checks at once. `inedctl host snmp <id> [--check]` prints the system values and the latest values.
  - **GUI details:** rows "SNMP" ("v2c · web · Linux web 6.8.0 …", or "no answer · checked 14:02"), "Location", "CPU" ("12 %"), "Memory" ("63 %"), "Disks" ("/ 91 %, /data 40 %") and "Uptime" ("12 d 3 h"). The host menu gets "Check SNMP now".
  - **Map:** no new sign in part 1. A host over a threshold is part 2's mail, not a map sign.
- **N6 Tests.**
  - A small fake SNMP agent in the tests: UDP on 127.0.0.1, built from gosnmp's own packet code, answering only for a set community.
  - Cases: finding the agent with the right community and version; the community never appearing in the API, events or log; no answer; `networks`; the values from a fake HOST-RESOURCES table; `snmp.values` only on a change; off by default; migration 9.

## Proposal, part 2: thresholds in `ined-alert`

- **N7 Rules.** `[[threshold]]` in `alert.toml`, for example `metric = "disk"`, `above = 90`, `for = "10m"`. The metrics are `cpu`, `cpu_max` (the busiest CPU, added in v0.62), `disk` (each fixed disk) and `memory`.
  - A host over a threshold for `for` gets a mail: "[ined] HIGH disk / 91 % on web (10.0.1.5)". When it is below again, "[ined] OK disk / 78 % on web" goes out.
  - The routes, batching, repeat, state file and syslog work as for down alerts.
  - `ined-alert` reads `snmp.values` events and, after a resync, the hosts' `snmp` values.
- **N8 Defaults.** No rules unless written: the example `alert.toml` has `disk > 90 %` and `cpu > 80 % for 10m` commented out.

## Not now

- SNMPv3 (users, authentication, encryption). gosnmp supports it, so it can follow; v3 is what keeps credentials off the wire.
- Traps (the agent sending to us).
- History and graphs of the values.
- Interface traffic and errors (IF-MIB), and custom OIDs.
- Finding hosts by SNMP that don't answer ping.

## Decisions for the user

- **N1:** gosnmp as a new dependency (recommended) / something else.
- **N2:** N2a the global switch with a `networks` list, off by default (recommended) / N2b a per-group setting.
- **N3–N5:** finding the agent, reading CPU, disks, memory and uptime every 5 min, keeping only the latest values, and the API, `inedctl` and GUI as above: OK / changes.
- **N7, N8:** thresholds in `ined-alert` as part 2, after part 1 is merged (recommended) / together / not now.
