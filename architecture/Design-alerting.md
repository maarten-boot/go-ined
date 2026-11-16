# Design: alerting by email (and syslog)

Status: **implemented** (2026-11-16): A1–A11 as recommended ("OK, all"), on `feature/alerting`. Settled in the build: before a down or recovery mail the host is read from the server once more, so an old event can't cause one; `/health` gives `access` so `ined-alert` can tell the token kinds apart; a route may name a whole view; `--check` and `--test-mail`; CI runs the tests with the runner's Python 3.12. SPEC-808, SPEC-950–956.

User request: alerts, for example by email, when a host or a service on a host is down; later, after SNMP monitoring, also alerts on values such as disk > 90 % or CPU > 80 %. To keep all of that out of the server, the user considered scripting; a Python client could be easier, especially if it only reads. Mail through a local SMTP relay for now. Alerts per group; only admins get all alerts. Syslog, so every action is logged to a central collector. A mail when the alerter restarts is an option.

## Today

- Every status change of a host arrives on the event stream as `host.status` with the full host (SPEC-305, 502). A client that reconnects with `Last-Event-ID` gets what it missed, or `resync` (SPEC-503).
- A host's status is `up`, `down` or `unknown` (SPEC-105). `unknown` also means "the server can't ping at all" (SPEC-304).
- There is one token, and it can do everything (SPEC-802).
- Services are checked every 6 hours, only to tick the Connect menu (SPEC-309). They have no status, and their changes are not events of their own.

## Proposal

A separate program, **`ined-alert`**, in Python. It reads the server's state and events with a read-only token and sends mail. The server gets one small change, the read-only token. Thresholds and recipients stay out of the server.

## Decisions (recommended)

- **A1 A Python program next to the server.**
  - It lives in `contrib/ined-alert/`:
    - `ined/` is a small client package (`Client(url, token)`: `hosts()`, `host(id)`, `groups()`, `views()`, `map(view)`, `health()`; `events()` yields events, reconnects with `Last-Event-ID` and passes on `resync`);
    - `ined-alert` is the program;
    - `alert.toml` is an example config;
    - `ined-alert.service` is a systemd unit.
  - Python 3.11 or later (for `tomllib`; Ubuntu 24.04 has 3.12). Standard library only, so nothing to install with pip.
  - It is not in the release archives yet; the README says how to install it.
- **A2 A read-only token on the server.**
  - `server.read_token` in `server.toml`, optional.
  - With that token, `GET` requests and the event stream work. Every other method answers 403 `forbidden`: "this token can only read".
  - The full token keeps working as now. The rule from SPEC-802 still holds: a non-loopback address needs the full token. Both tokens are compared in constant time.
  - `ined-alert` checks at start which kind of token it has. With the full token it logs a warning, but still runs.
  - New SPEC item and OpenAPI entry, plus tests.
- **A3 What is alerted.**
  - **Down:** a monitored host whose status becomes `down`.
  - **Recovered:** a host is `up` again after a down mail was sent.
  - `unknown` is not alerted per host. When the server can't ping (`/health` degraded) or the alerter can't reach the server, the admins get one mail for that (A9). Otherwise a ping failure on the server would send a mail for every host.
  - Hosts with monitoring off are never alerted. Deleting a host that is down ends its alert without a recovery mail; this is logged.
- **A4 Not for short drops.**
  - A host must stay down for `down_after`, default 2 minutes, before a mail goes out. If it comes back sooner, nothing is sent; this is logged.
  - There is no repeat while a host stays down. `repeat`, default off, could resend after an interval.
- **A5 Recipients per group.**
  - **Routes:** each `[[route]]` names a group by its path, view first, such as `Networks/dc/lan` or `Payroll/app servers`, with a list `to = [...]`.
  - **Matching:** a route covers the group and everything below it. A host's recipients are the routes of every group it is in, in every view. A host in "Networks/dc/lan" and in "Payroll/app servers" alerts both lists.
  - **Admins:** `admins = [...]` get every alert. A host in no routed group alerts only the admins.
  - **Duplicates:** each recipient gets each alert once.
  - **Live paths:** paths are resolved from the live groups and updated on group and view events.
  - **Mistakes are logged:** a route whose path matches nothing is a warning at start and after changes. A path that matches several groups with the same name covers all of them, with a warning.
- **A6 One mail for many changes.** When a switch fails, 40 hosts go down at once.
  - Alerts for a recipient are collected for `batch`, default 30 seconds, and sent as one mail: "[ined] 40 hosts down in Networks/dc".
  - A single alert is sent alone, with the host in the subject: "[ined] DOWN web (192.0.2.10)".
- **A7 Mail through a local relay.**
  - SMTP to `mail.host`/`mail.port`, default `localhost:25`, without login or TLS for now. `mail.from` must be set.
  - The mail is plain text: the host's name, IP address, the groups it is in (as paths), since when it is down, when it was last seen, and the server's URL.
  - A send that fails is retried 3 times (after 10 s, 1 min, 5 min), then dropped. Every attempt is logged.
  - Login and STARTTLS can be added later as settings, without changing anything else.
- **A8 Syslog for every action.**
  - Everything goes to stderr, which journald picks up under systemd. With `[syslog]` set, it also goes to a collector.
  - **Settings:** `address = "udp://collector:514"` or `"tcp://…"` or `"/dev/log"`; `facility = "local0"`; the tag is `ined-alert`.
  - **What is logged:**
    - start and stop, with the config and token kind;
    - connected, reconnecting and `resync`;
    - every status change seen;
    - each alert waiting, cancelled (back up in time), sent or failed;
    - config warnings.
  - **Format:** one line each, as `key=value` so a collector can parse it, for example: `event=down host=web ip=192.0.2.10 groups="Networks/dc/lan" to=ops@example.com,admin@example.com`.
- **A9 Restart and lost connection.**
  - **State file:** a small JSON file (`state_file`) records which hosts have an open down alert, so a restart doesn't resend or forget anything.
    - On start, after loading the hosts:
      - a host down now with an open alert: nothing;
      - down now without an alert: an alert, with the delay counted from the host's `last_change`;
      - an open alert for a host that is up now: a recovery mail.
    - The same comparison runs after a `resync`.
  - **Restart summary:** `startup_summary`, default off, also sends the admins one mail at start that lists every host down at that moment.
  - **Lost connection:** when the server can't be reached for `server_down_after` (5 minutes), the admins get "ined-server unreachable", and later "reachable again". The same goes for `/health` degraded, which means no pings.
- **A10 Later, in separate designs.**
  - **Service alerts:** these need a server change first. Services would be watched per host on purpose, checked more often than every 6 hours, and their changes sent as events. The alerter would then add "service down" the same way as "host down".
  - **SNMP thresholds** such as disk > 90 % and CPU > 80 %: the server collects the values, and `ined-alert` gets `[[threshold]]` rules. Routes, batching, mail and syslog stay as they are.
- **A11 Tests.**
  - **Python unit tests** (`unittest`, standard library) cover routing, the delay, batching, the state file across a restart, `resync`, and mail with a small fake SMTP server; they read a recorded event stream.
  - **Go tests** cover the read-only token.
  - **CI** runs the Python tests.
  - **By hand:** a run against a real server and a local relay.

## Example `alert.toml`

```toml
[server]
url = "http://127.0.0.1:8470"
token = ""                       # the read-only token (server.read_token)

[mail]
host = "localhost"
port = 25
from = "ined-alert@example.com"

[alert]
admins = ["admin@example.com"]   # every alert
down_after = "2m"
batch = "30s"
repeat = ""                      # "" = no repeat
startup_summary = false
state_file = "/var/lib/ined-alert/state.json"
server_down_after = "5m"

[[route]]
group = "Networks/dc"            # the group and everything below it
to = ["ops@example.com"]

[[route]]
group = "Payroll/app servers"
to = ["payroll-it@example.com"]

[syslog]
address = "udp://syslog.example.com:514"   # "" = stderr only
facility = "local0"
```

## Changes

- **Server:** the read-only token (A2): config, auth, OpenAPI, Spec, tests; README section "Alerting".
- **New:** `contrib/ined-alert/` with the client package, the program, an example config, a systemd unit and tests (A1, A3–A9, A11).
- **SPEC items to add:** read-only token (section 11), alerting (new section).
- **TCs:** for the token, routing, delay, batching, restart and syslog.
