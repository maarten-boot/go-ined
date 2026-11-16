# Conversation log — 2026-11-16

1. **User**: would it make sense to embed kukel into the GUI or the server (`../kukel/`)? It could turn the network monitoring into a programmable platform. What do you think?
   **Claude** looked at Kukel's embedding API (step limits, contexts, module allow-lists; memory not limited; one interpreter per goroutine; the package pulls in pgx, MySQL and zmq4). Advised: server side rather than GUI (alerts and checks must run when no GUI is open; the GUI could start scripts, like Tkined's tools), but first as a separate process on the REST and SSE API, not inside `ined-server`.
2. **User**: pushed; the main use would be alerting, e.g. by email, when a host or a service on the host is down; later, after SNMP monitoring, alerts on disk > 90 % or CPU > 80 %. To keep that out of the server I was considering scripting, but a Python scripting module could be easier (especially if it is read-only).
   (Spelling corrected: "moritoring" → "monitoring", "alers" → "alerts", "pypton" → "Python", "couls" → "could", "especiaclly" → "especially".)
   **Claude** checked the push: `main`, `docs/architecture` and `feature/details-selection` arrived. Agreed: Python as a separate client, as everything needed is on the API and Python has mail and SNMP libraries. Two gaps: there is no read-only token, and services have no status of their own (checked every 6 h, for the Connect menu only). Asked about mail delivery, recipients and a mail on restart.
3. **User**: yes, write Design-alerting.md; SMTP with a local relay for now; alerts per group is a good idea, only admins should get all alerts; it may be handy to add syslog to log all activities to a central collector; a mail on restart is an option.
   (Spelling corrected: "a main on restart" → "a mail on restart", "a option" → "an option".)
   **Claude** wrote `Design-alerting.md`:
   - A1: `ined-alert`, a Python 3.11+ program in `contrib/ined-alert/`, standard library only.
   - A2: a read-only token on the server (`server.read_token`), the only server change.
   - A3: alerts for "down" and "recovered" of monitored hosts; no per-host alert for `unknown`.
   - A4: a host must stay down for 2 minutes before a mail.
   - A5: routes per group path in any view, covering subgroups; admins get everything.
   - A6: alerts for a recipient batched for 30 seconds.
   - A7: mail through a local SMTP relay, with retries.
   - A8: syslog as `key=value` lines to a collector.
   - A9: a state file for restarts and `resync`, an optional restart summary to the admins, and an alert when the server is unreachable.
   - A10: service and SNMP alerts later, after server work.
   - A11: tests.
   - Decisions A1–A11 wait for the user's OK.
4. **User**: pushed; OK, all (A1–A11).
   **Claude** checked the push: `main` and `docs/architecture` arrived.
   - Built it on `feature/alerting` (merged):
     - server: `server.read_token`, which can only read (403 `forbidden` otherwise; it needs `server.token` and must differ from it); `access` in `/health`; `inedctl health` prints it; OpenAPI, example config (SPEC-808, TC-808);
     - `contrib/ined-alert`: a client package `ined` and the program, Python standard library only: routes per group path in any view, a delay with a check on the server before mailing, batching per recipient, SMTP with retries, `key=value` logging to stderr and syslog, a state file for restarts, mails when the server is unreachable or degraded, the start summary, `--check` and `--test-mail`, a systemd unit (SPEC-950–956, TC-950–956);
     - `make alert-test` and a CI job with the runner's Python 3.12 (actions/setup-python was not added, to avoid an unpinned action).
   - Checked end to end with a scratch server on port 18470, a fake SMTP relay and a UDP syslog listener: the start summary and a down alert for a host already down, a recovery after its IP changed, the server stopped and started (unreachable after 8 s, back, `resync`), and a restart of `ined-alert` with a recovery while it was stopped. Small fixes from that run: the summary's wording in a mixed subject, consistent rounding, only the first failed reconnect is a warning.
   - All Go tests (`-race`), lint and the 31 Python tests pass. Only Python 3.14 is installed here; CI checks 3.12. 3.11 is the stated minimum but not tested.
5. **User**: pushed. Then: write the design for service alerts.
   **Claude** checked the push: `main`, `docs/architecture` and `feature/alerting` arrived. CI on `feature/alerting` passed every job; the new `alert` job ran the 31 Python tests on Python 3.12.3.
   Wrote `Design-service-alerts.md`:
   - S1: watch the known services (`ssh`, `rdp`, `vnc[:n]`, `http`, `https`) or any `tcp:N`, by TCP connect.
   - S2: a `watch` list per host, set on purpose, never automatic; per-group watching later.
   - S3: checked every 60 s, down after 2 failures, at most 50 connects at once.
   - S4: a status per watched service, stored (schema 7), reset on an IP change.
   - S5: `watch` in host JSON and `PATCH`, a `service.status` event, `services_down` in `/map`, `inedctl host watch`.
   - S6: a service marker on the map; host and group status stay ping-based.
   - S7: "Watch services…" in the GUI, also for a selection.
   - S8: `ined-alert` alerts on services with the same delay, routes and batching, held back while the host itself is down.
   - S9: the 6-hour check for the Connect menu stays.
   - Decisions S1–S9 wait for the user's OK.
