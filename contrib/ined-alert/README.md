# ined-alert

Email alerts per group for go-ined, with syslog (design: `architecture/Design-alerting.md`; SPEC-950 to SPEC-956).

- It reads the server's hosts and events with a **read-only token** (`server.read_token` in `server.toml`, SPEC-808) and changes nothing.
- A monitored host that stays **down** for `down_after` (2 min) sends a mail; when it is **up** again, the same people get a recovery mail.
- **Routes** send the alerts of a group, and of everything below it, to a list; the **admins** get everything, also when the server can't be reached or can't ping.
- Alerts for one recipient within `batch` (30 s) go out as one mail. Mail goes through an SMTP relay (`localhost:25`) without login.
- Every action is logged as `key=value` lines to stderr and, if set, to a **syslog** collector.
- A **state file** keeps open alerts over a restart; `startup_summary = true` mails the admins the hosts that are down at start.

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
