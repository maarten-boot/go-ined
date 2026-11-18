# ined-alert

Email alerts per group for go-ined, with syslog (design: `architecture/Design-alerting.md` and `Design-service-alerts.md`; SPEC-950 to SPEC-957).

- It reads the server's hosts and events with a **read-only token** (`server.read_token` in `server.toml`, SPEC-808) and changes nothing.
- A monitored host that stays **down** for `down_after` (2 min) sends a mail; when it is **up** again, the same people get a recovery mail.
- A **watched service** (`inedctl host watch 12 ssh,https,tcp:5432`, SPEC-310) that stays down for `down_after` sends "[ined] DOWN https on web (192.0.2.10)", and a recovery mail later. While the host itself is down, or waiting for its own alert, its service alerts wait: the host's mail says enough. A service still down when the host is back is mailed `down_after` after its return. A service that becomes `unknown` (the server's service checks turned off, a new IP address) ends its alert without a recovery mail and without STILL DOWN repeats, as a host whose monitoring is turned off.
- **New hosts** (optional, `[new_hosts] enabled = true`, SPEC-957): every host added, found by a scan (also one on a schedule, SPEC-206) or added by hand, is mailed as "[ined] NEW printer-3 (10.0.1.7) in Networks/lan" to the admins and the routes of its groups, a few seconds after it appears (once the routes know it). After a short disconnect the missed events are replayed, so their hosts are mailed too; hosts that only a resync shows (after a long disconnect or a restart of the server or of `ined-alert`) aren't.
- **Routes** send the alerts of a group, and of everything below it, to a list; the **admins** get everything, also when the server can't be reached or can't ping.
- Alerts for one recipient within `batch` (30 s) go out as one mail. Mail goes through an SMTP relay (`localhost:25`) without login. A failed mail is tried again after 10 s, 1 min and 5 min, then dropped and logged as an error (`mail_dropped`); the alert still counts as sent, so its recovery mail goes out later. Watch the log for `mail_dropped`.
- Every action is logged as `key=value` lines to stderr and, if set, to a **syslog** collector.
- A **state file** keeps open host and service alerts over a restart (version 2; a version 1 file is read as before; an unreadable or damaged file, or one from a newer version, is moved to `state.json.bad` and the program starts without open alerts); `startup_summary = true` mails the admins the hosts, and the watched services, that are down at start.

Python 3.11 or later, standard library only (Ubuntu 24.04 has 3.12).

## Try it

```sh
# On the server, in server.toml: token = "..." and read_token = "..."; restart it.
cp alert.toml ~/.config/go-ined/alert.toml        # then edit: token, mail.from, admins, routes
./ined-alert --check                               # token, routes and the hosts they cover
./ined-alert --test-mail                           # one mail to the admins through the relay
./ined-alert                                       # runs until Ctrl+C or SIGTERM
```

`ined-alert.service` is a systemd unit; the comment at its top says how to install it.

Exit codes: 0 stopped by a signal (or `--check`/`--test-mail` went well), 1 a runtime error such as a refused token or a route that matches nothing in `--check`, 2 a configuration error.

## Files

- `ined/`: a small client for the go-ined API (`Client(url, token)`: `hosts()`, `host(id)`, `groups()`, `views()`, `map(view)`, `health()`, `events()`), usable for your own scripts.
- `ined_alert/`: the program: `config`, `routes`, `engine` (what is alerted and when), `mail`, `logs`, `main`.
- `tests/`: `make alert-test` from the repository root.
