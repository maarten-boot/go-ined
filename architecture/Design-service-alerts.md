# Design: watched services and service alerts

Status: **proposed** (2026-11-16), waiting for the user's OK.

User request: write the design for service alerts: a mail when a service on a host is down (the first part of the original alerting request, deferred in `Design-alerting.md` A10).

## Today

- **The service check is for the Connect menu only.** Every 6 hours, the server tries one TCP connection to each of ssh 22, rdp 3389, vnc 5900–5909, http 80 and https 443 on each monitored host. It stores the names that answered (`services`, `services_checked_at`; SPEC-309).
- **The result is a list, not a status per service.** A service that stops answering just drops out of the list, and only `host.updated` says so, up to 6 hours late.
- **There is no way to say "this service matters on this host".** A VNC session that closes is normal; a database port that closes is not.
- **`ined-alert` only watches hosts.** It alerts on a host's ping status (`host.status`), routes alerts per group, and keeps a state file (SPEC-950–956).

## Proposal

Make services **watched on purpose**, per host. The server checks watched services as often as it pings, keeps a status for each with its own event, and `ined-alert` alerts on them the same way as on hosts. The 6-hour check for the Connect menu stays as it is.

## Decisions (recommended)

- **S1 What can be watched.**
  - The names the server already knows: `ssh`, `rdp`, `vnc`, `vnc:1`…`vnc:9`, `http`, `https`.
  - Any other TCP port as `tcp:N`, such as `tcp:5432` for PostgreSQL. A label for these, like "postgres", is left for later.
  - The check is a TCP connect, as now: "up" means the port accepts a connection, not that the application behind it answers correctly. An HTTP status check is left for later.
- **S2 Per host, set on purpose.**
  - Each host has a list `watch`, which is empty at first.
  - Nothing is watched automatically just because it answered, so a closing VNC session or a laptop's SSH doesn't cause alerts.
  - The GUI's dialog shows which services answered at the last check, to make picking easy (S7).
  - Watching per group, inherited by the hosts below as host labels are (SPEC-725), is left for later. The GUI can already set the list on a whole selection of hosts.
- **S3 How often.**
  - Watched services of a monitored host are checked every `services.watch_interval`, 60 s by default, with the existing 1 s timeout.
  - A service is `down` after `services.watch_down_after` failed checks in a row (2, like `monitor.down_after`). It is `up` again after one success.
  - Up to `services.watch_concurrency` (50) connects at once. 5,000 hosts with 3 watched services each is 15,000 connects a minute; a closed port answers at once, and only ports that don't answer at all take the full second.
  - An unmonitored host's watched services are not checked, and their status is `unknown`.
- **S4 A status per watched service.**
  - Each watched service has `status` (`up`, `down`, `unknown`), `last_change` and `last_ok`.
  - It starts as `unknown` and is stored in a new table (schema 7), so it survives a restart, as host status does (SPEC-603).
  - Adding a host's service to the list, or changing the host's IP address, resets the status to `unknown`.
  - While the host itself is down (ping), its services are still checked and keep their own status. `ined-alert` decides what to mail (S8).
- **S5 Events and API.**
  - Host JSON gets `watch`: a list of `{"name", "port", "status", "last_change", "last_ok"}`, in the order given.
  - `PATCH /hosts/{id} {"watch": ["ssh", "https", "tcp:5432"]}` sets the list.
    - An unknown name, a port outside 1–65535, a duplicate or more than 20 entries is 400.
    - A change emits `host.updated`.
  - A new event, **`service.status`**, is emitted when a watched service's status changes. It carries `{"host_id", "name", "port", "status", "last_change"}` plus the host's `display_name` and `ip`, so a client needs no extra call.
  - `GET /map` items of hosts get `services_down`, the number of watched services down, so the map can mark them (S6).
  - OpenAPI and the API examples are updated.
  - `inedctl`:
    - `host watch <id> ssh,https,tcp:5432`, plus `--add`, `--remove` and `--clear`;
    - `host show` gets a "Watched" line: "ssh up, https down since 14:02, tcp:5432 up";
    - `host list` gets a `SVC` column showing "1 down".
- **S6 On the map.**
  - A host's own status stays its ping status (SPEC-105), and group status doesn't change.
  - A host with a watched service down gets a **service marker**: a small shape next to the icon, like the note marker (SPEC-711). It is a shape, so it doesn't depend on colour.
  - The details panel gets a "Watched" row in the same form as `host show`.
  - Making service problems count in the host's or the group's colour would mean a new status; that is left for later.
- **S7 In the GUI.**
  - A host's context menu gets "Watch services…". The dialog has:
    - a tick box per known service, each with "(answered at the last check)" where that applies;
    - a field for other TCP ports, such as "5432, 8080".
  - With several hosts selected, it sets the same list on all of them ("Watch services on 5 hosts…"). Ticks that differ between the hosts show as unset, and the dialog says so.
  - "Connect ▸" stays as it is.
- **S8 `ined-alert`.** It alerts on `service.status` the same way as on hosts:
  - A watched service that stays `down` for `down_after` sends **"[ined] DOWN https on web (192.0.2.10)"**. Its recovery sends "[ined] UP https on web … after 12 min". The service is checked with the server before either mail.
  - **No double alerts:** while the host itself is down, or waiting to be alerted as down, its service alerts are held back; the host alert says enough.
    - When the host comes back and a service is still down, the service alert follows from then.
    - A service alert that is open when the host goes down stays open, and gets its recovery mail later.
  - Recipients, routes, batching, mail, syslog and repeats are the same as for hosts. A batch can mix them: "[ined] 2 hosts down, 3 services down".
  - The state file keeps open service alerts. Restart and `resync` compare them as for hosts.
  - A per-route choice of "hosts only" or "services only" is left for later. Everyone on a route gets both.
- **S9 The 6-hour check stays.** `services` and `services_checked_at` (the Connect menu's ticks) keep their meaning. Watched checks don't change them, so there aren't two writers.

## Example

```sh
inedctl host watch 12 ssh,https,tcp:5432
inedctl host show 12
...
Watched:  ssh up, https down since 14:02:10, tcp:5432 up
```

The mail to the routes of web's groups and to the admins, about 2 minutes later:

```
Subject: [ined] DOWN https on web (192.0.2.10)

DOWN       https (443) on web (192.0.2.10) since 14:02:10, 2 min
           groups: Networks/dc/lan, Payroll/app servers
           last answered: 14:01:10
```

## Changes

- **Server:** schema 7, a table `host_services`: host, name, port, status, `last_change`, `last_ok`, order.
  - The watch checker lives in `internal/services`.
  - `watch` in host JSON and `PATCH`; the `service.status` event; `services_down` in `/map`.
  - Config `services.watch_interval`, `watch_down_after`, `watch_concurrency`.
  - OpenAPI and the API examples.
- **inedctl:** `host watch`, the "Watched" line and the `SVC` column.
- **GUI:**
  - "Watch services…" for one host or a selection;
  - the service marker on the map;
  - the "Watched" row in the details panel.
- **ined-alert:** service alerts (S8), with state file version 2; version 1 files are read as before.
- **SPEC:** new items for watched services (server, API, inedctl, GUI) and for service alerts. SPEC-952 and SPEC-956 are extended.
- **TCs:**
  - checker: interval, down after 2 failures, unknown when unmonitored, reset on IP change, persistence;
  - API: validation and events;
  - inedctl;
  - GUI: dialog, selection, marker;
  - ined-alert: delay, holding back while the host is down, recovery, restart.
- **Order of work:**
  1. Server, API and inedctl.
  2. ined-alert.
  3. The GUI.
  
  Each part is usable on its own: with the first two, the watch list is set with `inedctl` and the mails work.
