# Changes

All notable changes between releases. Format based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]
### Added
- Project setup: git repo, `.gitignore`, project `CLAUDE.md` rules.
- Architecture docs: Spec, Plan, Tests, conversation log.
- Spec v0.3: notes, collapse/expand on a single shared map.
- Spec v0.4: networks modelled as groups with a CIDR (network groups), no link lines.
- Spec v0.5: crosscheck 01 fixes: pan/zoom and find host, `stale` flag, run-id event ids, `inedctl` spec, config keys and XDG paths, error-code table.
- Example config files: `examples/server.toml`, `examples/gui.toml`, `examples/ctl.toml`.
- M1 server core: `ined-server` with SQLite inventory, hosts, groups and network groups, notes, REST API (`/api/v1`: hosts, groups, map, health), bearer-token auth, optional TLS, TOML config.
- README, MIT licence, Makefile, `scripts/gen-api-examples.sh`, `architecture/API-examples.md`.
- Server takes an exclusive database lock; a second server on the same database refuses to start.
- Host and group ids are never reused (database migration 2).
- Background ping of every monitored host (ICMP and ICMPv6): unprivileged sockets first, raw sockets as fallback; start times spread across the interval; up/down/unknown status with `down_after`; round-trip time, loss, `last_seen` and `last_change` saved to the database.
- Live event stream `GET /api/v1/events` (Server-Sent Events) with replay after reconnect (`Last-Event-ID`), `resync`, and keepalive comments.
- `/health` reports the ICMP mode and, if ping is unavailable, the reason (`icmp_error`, status `degraded`).
- New error code `unavailable` (503) while the server shuts down.
- Config key `monitor.enabled` (default `true`) to turn pinging off.
- Live-events example in `architecture/API-examples.md`.
- Discovery: `POST /discovery` probes an IPv4 range (up to /16) at a configurable rate and adds the hosts that answer, into the most specific matching network group; jobs can be followed (`GET`, events) and cancelled (`DELETE`).
- Reverse DNS: PTR names are looked up when a host is added or re-addressed, and refreshed every `dns.refresh`; config key `dns.enabled`.
- Architecture overview diagram (`architecture/overview.svg`) in the README.
- Go client package `pkg/client`: typed calls for every endpoint, `*client.APIError`, and an event stream that reconnects (1 s doubling to 30 s), resumes with `Last-Event-ID` and passes `resync` on.
- `inedctl`: hosts, groups, notes, discovery (`--wait` follows a job), events and health; tables or `--json`; exit codes 0–3.
- Client config key `server.ca_cert` (and `inedctl --ca-cert`) to trust a self-signed server certificate.
- Views: custom maps with their own groups, where hosts can be placed in addition to the network view (`/views`, `GET /map?view=`, `PUT|DELETE /views/{id}/hosts/{host_id}`, `GET /groups?view=`, `view.*` and `placement.*` events); database migration 3; `pkg/client` view calls; `inedctl view …` and `--view`.
- Spec: a Hosts tab for the GUI (sortable list by IP and DNS name, with find) listed as a future enhancement.
- Spec: MAC address collection (ping/ARP over SSH, or an agent on the target network) listed as a future enhancement.

### Fixed
- API: `null` in a PATCH body is rejected (400) for every field except `parent_id`; before, it silently set the zero value (for example, `"monitored": null` turned monitoring off).
- `inedctl`: `group list` has a VIEW column and `group show` a `View:` row; `view show` exits non-zero when it can't read the view's hosts; the usage text says that `view place` without `--parent` puts the host at the view's root.
- `inedctl`: Ctrl-C exits 130 and, for `discover start --wait`, says the job keeps running on the server; a relative `ca_cert` is read from the config file's folder; `--token ""` clears a token from the config or environment; `--json --wait` prints the API's response unchanged; round-trip times below 1 ms get two decimals.
- `inedctl` warns when it sends a token unencrypted to a host that isn't loopback.
- `architecture/API-examples.md` has generated `inedctl` examples.
- Discovery jobs report probes that fail with an error (`errors`, and the first message in `error`).
- Monitoring and reverse DNS no longer keep a waiting goroutine per due host; memory stays flat after a large discovery.
- `discovery.rate` is limited to 1–100,000 (a larger value crashed the server at start); ranges above ~5,000 addresses log a warning.
- An event-stream client that stops reading is disconnected after 10 s instead of blocking the server's shutdown.
- Raw-socket ping mode uses one shared socket per address family, so each reply is received once (scales to thousands of hosts).
- The echo requests of a round are at least 200 ms apart; `last_seen` is the arrival time of the last reply.
- ICMP mode is detected per address family. If only IPv6 is unavailable, IPv6 hosts are not pinged (and not marked down), and `/health` shows `icmp6_error`.
- Turning monitoring back on marks the host `stale` until its next ping round.
- Hosts with monitoring off no longer stay `stale` forever after a restart.
- Changing a host's IP address resets its status to `unknown` instead of keeping the old address's status.
- Database paths containing `?`, `#` or `%` opened the wrong file.
- Rows whose parent group is missing are moved to the root at startup (with a warning).
- `ined-server -version` prints to stdout.
