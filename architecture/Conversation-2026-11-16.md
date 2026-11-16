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
6. **User**: pushed; OK, all. Each watched service gets its own up/down status, checked every minute: make the time configurable, default every 5 minutes.
   **Claude** checked the push: `main` and `docs/architecture` arrived. The interval was already a setting (`services.watch_interval`); its default is now 5 minutes. With `watch_down_after` 2, a service that stops is `down` 5 to 10 minutes later. `Design-service-alerts.md` is accepted with that change. Started on part 1 (server, API, `inedctl`) on `feature/service-watch`.
   - Built part 1 on `feature/service-watch` (merged):
     - a `watch` list per host (known names or `tcp:N`, at most 20, no port twice), set with `PATCH /hosts/{id}`;
     - a Watcher in `internal/services`: one TCP connect per watched service every `services.watch_interval` (5 min), down after `watch_down_after` (2) failures, up after one success, at most `watch_concurrency` (50) connects at once;
     - a status per service with `last_change` and `last_ok`, in a new table `host_services` (schema 7); a new IP address or monitoring off makes them `unknown`;
     - the `service.status` event, `services_down` in `/map`, OpenAPI;
     - `inedctl host watch <id> [LIST | --add | --remove | --clear]`, a Watched line in `host show`, an SVC column in `host list`.
   - Docs: Spec v0.50 (SPEC-310, 311, 512, 904), TC-318–321 and TC-907, regenerated API examples with a "Watched services" section, README, Changes, Plan.
   - Checked end to end with real binaries against 127.0.0.1, with a 10 s interval and a local HTTP listener: a closed port went `down` after two checks, the listener's port went `down` when it was stopped and `up` when it was back, statuses were kept over a real restart, and monitoring off made them `unknown`. One slip during the check: the first "restart" stopped a shell wrapper instead of the server, so it didn't happen; the second server refused the locked database, as it should. It was redone with the server's own PID.
   - All Go tests (`-race`), lint and the 31 Python tests pass. `bin/` rebuilt: v0.5.0-33-g389453a.
7. **User**: pushed; continue with part 2. The lines on the right information pane can be a bit more compact; start with half the current whitespace between the lines.
   **Claude** checked the push: GitHub still had `main` and `docs/architecture` at d4c9dd6 and no `feature/service-watch`, so the push had not arrived; the work went on locally and the push command is given again.
   - Part 2 on `feature/service-alerts` (merged):
     - `ined-alert` reads `service.status` and the hosts' `watch` lists. A service down for `down_after` is checked with the server and mailed ("[ined] DOWN https on web (192.0.2.10)") to the host's recipients; a recovery mail follows. Repeats, batching and syslog as for hosts.
     - While the host is down, or waiting for its own alert, its service alerts are held (logged once). A service still down when the host is back is mailed `down_after` after the host's return. An open service alert stays open while the host is down, without repeats.
     - Unwatched, monitoring off or host deleted: closed without mail. State file version 2 (`services`); version 1 still reads. The start summary lists services down.
     - One server change: holding alerts only works if the service status is fresh when the host comes back, and with a 5-minute interval it can be 5 minutes old. The Watcher now checks a host's watched services at once when its ping status becomes up.
     - Spec v0.51 (SPEC-310, 952, 954, 956), TC-322 and TC-957.
     - Checked end to end with a real server (10 s interval), `ined-alert` (15 s delay) and a fake SMTP relay: a closed port mailed down, a stopped HTTP listener mailed down, `ined-alert` stopped and the listener started meanwhile: the recovery came from the state file at the next start; unwatching the closed port closed its alert without mail. The holding-back while the host is down is covered by unit tests only, since 127.0.0.1 can't be made to stop answering pings. As in part 1, `setsid ... &` gave the wrapper's PID; the real PIDs were taken from `ps` and checked before stopping.
   - The details panel on `feature/compact-details` (merged): the rows had Fyne's form spacing, 8 px inner padding above and below each label plus 4 px between rows, so 20 px of white space between two rows' text. They now have 10 px (inner padding 4, gap 2; `detailsSpacing = 0.5`, so it is easy to change again). Fyne's form layout reads the app-wide padding, so the rows got a small layout of their own; the labels' padding comes from a theme override that asks the app's theme for everything else, so the contrast presets still apply. Keys and values start where they did. Rendered to PNG and checked; SPEC-708 v0.51, TC-747. The look in the real window is for the user to judge, and the spacing can go further.
   - All Go tests (`-race`), lint and the 40 Python tests pass.
8. **User**: pushed; continue with part 3.
   **Claude** checked the push: `main`, `docs/architecture`, `feature/service-watch`, `feature/service-alerts` and `feature/compact-details` arrived.
   - Part 3 on `feature/service-gui` (merged):
     - A host's menu has "Watch services…", or "Watch services on N hosts…" on a selection. The dialog has a tick per known service ("· answered at the last check" from the 6-hour check, or "· answered on 2 of 5") and a field for other TCP ports. With several hosts only the services all of them watch are ticked, and the dialog says that saving sets the same list on all of them. The list is checked like the server does; mistakes stay in the dialog.
     - The service marker: an outlined warning triangle with a bar off the icon's bottom-right corner, in the `down` colour. Rendered under AA and Max and checked; after that it was made a bit larger (9 to 11 units).
     - The details panel's "Watched" row, kept current from `service.status`.
     - One API addition: `service.status` names one service and the map only knows a count per host, so the GUI couldn't update the count from the event. The event now carries `services_down`, the host's count after the change (SPEC-311). The API examples are regenerated.
     - Spec v0.52 (SPEC-732, 311, 716), TC-748–750; TC-321 checks `services_down`.
   - All Go tests (`-race`), lint and the 40 Python tests pass. `TestDiscoveryDialog` failed once in the full run (the known flaky test, already on the open list); 5 runs of it and the package again passed.
   - All three parts of `Design-service-alerts.md` are done.
