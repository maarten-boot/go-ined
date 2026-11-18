# Changes

All notable changes between releases. Format based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

## [0.8.0] - 2026-11-18

Update `ined-server` first: the database gets schemas 8 and 9 (three columns on groups, four on hosts), so a 0.7.0 server can't open it any more; keep a copy of the database if you may go back. Against an older server, the new `ined-gui`'s "Rescan ▸" and `inedctl group rescan` / `discover list` get the server's error ("unknown field", "method not allowed"), as do "Check SNMP now" and `inedctl host snmp --check`; everything else works. `ined-alert`'s state file becomes version 3 at its first save, which an older `ined-alert` can't read; keep a copy of it too if you may go back. SNMP is off until you set `[snmp] enabled = true`; see `examples/server.toml`.

### Added
- Networks can be scanned again on a schedule. A network group's context menu in `ined-gui` has "Rescan ▸" (off, every hour, 6 hours, day or week), and `inedctl group rescan <id> 24h|off` sets any interval from 15 minutes. The server scans one network at a time, the one waiting longest first, and remembers when each was last scanned, also over a restart. A scan you start by hand that covers the whole network counts as its scan, and one you start over a running scheduled scan stops that scan first. New hosts are added as with a scan by hand; nothing is moved or removed. The group's details show "Rescan: every day, last 03:15, 2 new". `discovery.scheduled = false` in `server.toml` turns all scheduled scans off.
- `inedctl discover list` (and `GET /api/v1/discovery`) lists the scans the server keeps; scheduled ones are marked "(scheduled)".
- `ined-alert` can mail new hosts: with `[new_hosts] enabled = true` every host added is mailed to the admins and the routes of its groups as "[ined] NEW printer-3 (10.0.1.7) in Networks/lan".
- `ined-alert` can mail SNMP values above a line: `[[threshold]]` with `metric` (`cpu`, `cpu_max` for the busiest CPU, `memory`, `disk`), `above` and `for`, for example "[ined] HIGH disk / 91 % on web (10.0.1.5)", and "OK" when it is back. The state file becomes version 3; an older `ined-alert` can't read it, so keep a copy if you may go back.
- SNMP, off until you turn it on (`[snmp] enabled = true` in `server.toml`): the server tries your community strings (`communities`, default "public") with SNMP v2c and v1 on the monitored hosts in `networks`, and for each host that answers shows its name, description and location, and every 5 minutes its CPU (the average, and with several CPUs the busiest one: "6 % (16 CPUs, busiest 100 %)"), memory and disk use and uptime. The GUI's host details have SNMP, CPU, Memory, Disks and Uptime rows and the host menu "Check SNMP now"; `inedctl host snmp <id> [--check]` prints them, and the API has them in the host's `snmp`. The community that answered is never shown. A host where nothing answers is tried again daily.

### Changed
- `ined-server` keeps its database from other users at every start, since it now holds SNMP community strings: a new database is readable only by its own user; an existing one loses the rights of other users (a group you set stays). When it can't change them, it logs a warning.

## [0.7.0] - 2026-11-18

Update `ined-server` together with `ined-gui`: the GUI warns about an older server ("ined-server v0.6.0 is older than this GUI"), and only the new server rounds positions and refuses them beyond ±1,000,000,000. The database doesn't change, so going back to 0.6.0 is possible. Update `ined-alert` too, for the fix of service alerts that become `unknown`.

### Added
- `ined-gui` explains the little triangle next to a host (a watched service is down) and the other map signs: the details show "▲ 1 watched service down: https (443) since 14:07" below the host's name; resting the pointer on a host shows a box with its address, status and the services down; and a new "Help ▾" button has "Map symbols…", a legend of every sign.

### Changed
- `ined-gui`: "Export…", "Import here…" and "Import into this group…" open your desktop's own file dialog, a window you can move and resize, with your bookmarks: GNOME's or KDE's on Linux (through the desktop portal), the system's on Windows and macOS. Where there is none, Fyne's dialog opens as before, now at 90 % of the window. Folders in it are an outline instead of a black shape. Exported files are only readable by you (mode 0600), as with `inedctl`, also when they replace an existing file. Only one file dialog opens at a time.
- Positions are saved with one decimal (`"x": 24.7` instead of `24.666666666666668`); exports are rounded the same way, also for positions saved before. Nothing visible moves: 0.1 is under half a pixel at the largest zoom.
- Positions beyond ±1,000,000,000 are refused ("position out of range"), also when a move, reflow, copy or import would put an item there. Such a position could become infinite, after which the host couldn't be sent to any client.

### Fixed
- `ined-gui`, export: the network is read from the server before the file dialog opens, and the file is replaced only when it is complete. Until now a failing server or a full disk left an earlier export empty (since 0.6.0).
- `ined-gui`, desktop file dialog: file names with `+`, `%`, `#` or `?` are used as chosen (until now `a+b.json` became `a b.json`, and `a#b.json` became `a`); a desktop dialog that ends without an answer no longer closes the GUI; an error after you chose a file is shown in the status bar instead of opening a second dialog.
- `ined-gui`, hover box: a click next to it reaches the map; it stays inside the window near the right and bottom edges; it closes when the host changes or you switch tabs.
- `ined-gui`: a file to import that is larger than 64 MiB is reported as such, not as "not a JSON file".
- Reflow saves groups with one decimal too.
- `ined-alert`: a service alert ends without a mail when the service becomes `unknown` (for example at every start of a server with `services.enabled = false`). Until now it stayed open and sent STILL DOWN mails.
- `inedctl host watch --add tcp:22` with `ssh` watched (the same port) does nothing, instead of an error from the server; `inedctl group export -o /dev/stdout` no longer fails after writing.
- The server: a check of a watched service that was removed and added again before the next round no longer counts for the new one.

## [0.6.0] - 2026-11-17

Update `ined-server` together with `ined-gui`: a 0.5.0 server refuses watch lists, the read-only token and export and import, and the GUI now says so ("ined-server v0.5.0 is older than this GUI"). The database gets two new columns and a table on first start (schemas 6 and 7), so an older server can't open it afterwards. `ined-alert` is new; it needs this server for service alerts and the read-only token.

### Added
- **Export and import of a network**: `inedctl group export 12 -o lan.json` saves a group of the network view with its subgroups, hosts, notes, watch lists and custom-view placements as one JSON file. `inedctl import lan.json --parent 3` adds it to any server in one step; `--dry-run` shows first what would happen. Hosts or groups that are there already refuse the import, unless `--update`; groups match by CIDR, or by their exact name under the same parent, and a matched group stays where it is (the report says so). Views that don't exist are skipped; missing groups in a custom view are made. In `ined-gui`, a group's menu has "Export…" and "Import into this group…", and empty space has "Import here…"; both show the report before importing. API: `GET /groups/{id}/export` and `POST /import`.
- **Watched services**: name the services that matter on a host (`ssh`, `rdp`, `vnc`, `vnc:1`…`vnc:9`, `http`, `https`, or any TCP port as `tcp:5432`), and the server checks them every 5 minutes, each with its own status: down after two failed checks in a row, up again after one success. `inedctl host watch <id> ssh,https,tcp:5432` sets them (`--add`, `--remove`, `--clear`; an empty list is refused, so an empty variable in a script can't clear it); `host show` has a "Watched" line and `host list` an SVC column ("1 down"). Nothing is watched until you say so. Writing `ssh` again as `tcp:22` keeps its status. With `services.enabled = false` they are all set to `unknown` at start.
  - Server: `watch_interval` (`5m`), `watch_down_after` (2) and `watch_concurrency` (50) under `[services]`; `watch` in host JSON and `PATCH /hosts/{id}`; a `service.status` event; `services_down` in `/map`. The database gets a new table on first start (schema 7), so an older server can't open it afterwards.
  - `ined-alert` mails them like hosts: "[ined] DOWN https on web (192.0.2.10)" after 2 minutes, and a recovery mail. While the host itself is down its service alerts wait, since the host's mail says enough; a service still down when the host is back is mailed 2 minutes after its return. Its state file becomes version 2 (an older file is read as before; a damaged one, or one from a newer version, is moved to `state.json.bad`).
  - When a host comes back up, the server checks its watched services at once.
  - `ined-gui`: "Watch services…" in a host's menu, also for several selected hosts at once, with a tick per service (marked where it answered at the last check) and a field for other ports. A host with a service down gets a small warning triangle at its icon; the details panel has a "Watched" row.
  - `service.status` also carries `services_down`, the host's number of watched services down.
- **`ined-alert`** (`contrib/ined-alert/`), email alerts per group: a host that stays down for 2 minutes sends a mail to the lists of the groups it is in (in any view) and to the admins, and a recovery mail when it is back. Alerts within 30 seconds go out as one mail; mail goes through a local SMTP relay; every action is logged, also to a syslog collector. It remembers open alerts over a restart, tells the admins when the server can't be reached or can't ping, and can mail a list of the hosts down when it starts. A recovery is mailed only once the server confirms it. A mail the relay keeps refusing for about 6 minutes is dropped and logged as an error; the alert counts as sent, so its recovery mail still goes out. Python 3.11 or later, nothing to install; see its README.
- `server.read_token` in `server.toml`: a second token that can only read (lists, the map and the event stream). Anything else gets 403. `/health` and `inedctl health` say what a token may do (`Access: read`).
- `ined-gui` details panel: the title and the values can be selected and copied (Ctrl+C). The note now follows right after the last row and takes the rest of the height.
- "Remove from this view" on groups in a custom view. It takes the group, its subgroups and their hosts out of that view; the hosts stay in the network view and in other views. It asks first when the group isn't empty.
- With several items selected, "Remove N items from this view" and "Show N items in view ▸" act on all of them: hosts are placed in the chosen view, groups are copied there.
- **"Connect ▸"** in a host's menu: SSH, Remote desktop (RDP), VNC and the web page (http, https). The server tries a TCP connection to ports 22, 3389, 5900–5909, 80 and 443 of each monitored host when it's added and then every 6 hours, and the menu ticks what answered; "Check services now" checks at once. Every entry stays clickable, since your machine may reach ports the server can't. The details panel has a "Services" row.
  - SSH, RDP and VNC run a command you can set in `gui.toml` under `[connect]`, with `{ip}`, `{name}` and `{port}`; on Ubuntu the defaults are `x-terminal-emulator -e ssh {ip}` and Remmina for RDP and VNC (`sudo apt install remmina`). Commands run without a shell. Web pages open in your browser.
  - Server: `[services]` in `server.toml` (`enabled`, `refresh`, `timeout`); `services` and `services_checked_at` on hosts; `POST /hosts/{id}/check-services`; `inedctl host services <id> [--check]`. The database gets two new columns on first start (schema 6), so an older server can't open it afterwards.
- "Show in view ▸" on a group copies it, with its subgroups and hosts, into another custom view. The hosts stay the same hosts; only the groups are new. API: `POST /views/{id}/copy-group`; `inedctl group copy <id> --view V`.
- `ined-gui` warns when the server is from an older release than the GUI ("ined-server v0.2.0 is older than this GUI (v0.5.0): … update and restart the server"), in a line above the status bar. An older server refuses newer changes, such as a bar colour, and until now the only sign was an error message in the status bar.

### Changed
- `ined-gui` details panel: the rows are closer together, with half the white space between them.
- `ined-gui`: the map fits all at start (also when the window is maximized right away) and again after every resize of the map area, including the details panel's divider. Your own zoom lasts until the next resize.
- `ined-gui` toolbar: the five zoom items are one button, "Zoom 100 % ▾", that shows the current zoom; its menu has Zoom in, Zoom out, 100 % and Fit all, with their shortcuts. The "Show" list moved into the toolbar, next to it. The trail (`Networks › lan`) has its own row, which appears only while a single group is shown.

## [0.5.0] - 2026-11-14

Update `ined-server` together with `ined-gui`: a server older than 0.5.0 refuses a bar colour ("unknown field \"color\"" in the status bar), and a new server's database can't be opened by an older one.

### Added
- `ined-gui`: an expanded group has a **bottom bar**. Dropping something from inside the group onto it moves it out, to the group's parent, just below the frame; while you drag, the bar says "out".
- A **colour for a group's top bar**: "Bar colour ▸" in a group's menu has 16 colours and "Custom…" (a colour picker). The colour is saved on the server and is the same in every GUI. The group name on the bar is black or white, whichever reads better; under the Max contrast preset bars aren't coloured. API: `color` on groups (`"#RRGGBB"` or `""`); `inedctl group color <id> '#rrggbb'|none`, `group add --color`. The database gets a new column on first start (schema 5).

## [0.4.1] - 2026-11-13

### Added
- Release archives of `ined-gui` for Windows (amd64), macOS (Apple silicon and Intel) and Linux on arm64, next to Linux on amd64. Each is built on its own platform, since the GUI needs cgo. The programs aren't signed: Windows SmartScreen and macOS Gatekeeper warn on the first start (see the README).

## [0.4.0] - 2026-11-13

### Added
- `ined-gui`: **a group as a view.** A "Show" list above the map (Ctrl+L; type to filter, Enter takes the first match) has every view with its groups. Choosing a group, "Show only this group" in a group's menu, or the key **o** shows only that group's contents, at their usual places, also when it is collapsed. A trail (`Networks › lan › servers`) and Backspace go back up, with the zoom you had. New items, **g** and Reflow work inside the shown group, and an empty group says so. Nothing is stored on the server; the tabs stay.
- `ined-gui`: the key **j** joins the selected hosts to the selected group, at their places. With several groups selected it asks which one. Hosts inside a selected group stay there, and the other groups don't move.

### Fixed
- `ined-gui`: "Reflow…" on a collapsed group sized an expanded subgroup inside it as an icon, so after expanding, the subgroup's frame could cover its neighbours. Members are now measured as they show when the group is expanded.
- `ined-gui`: a frame's label could run past the frame's right edge (the width left out the status sign, and bold text was measured as regular). Long bold host names also get the room they need when names are wrapped or reflowed.
- Reverse DNS: when a host's IP address changed while its name was being looked up, the server log reported a name change for the old address that was never saved. Answers for an address the host no longer has are now ignored, and only saved changes are logged.

## [0.3.1] - 2026-11-12

### Changed
- Reverse DNS: a host's name is no longer dropped after a single "doesn't exist" answer. The server looks again a minute later and clears the name only after 3 such answers in a row; until then the host keeps showing its name. Hosts that have no name are still looked up once an hour. The server log says at Info level when a name is cleared, changes, or comes back.

### Fixed
- `ined-gui`: expanding a group that was already expanded (with **e**, or "Expand N groups" on a selection where only some were collapsed) moved everything inside it. Groups already in the requested state are now left alone.

## [0.3.0] - 2026-11-12

### Added
- Host labels per group: a group's context menu has "Host labels ▸ Name / IP address / Name and IP address / As the parent group". The choice applies to the hosts inside the group and its subgroups (until one sets its own), is saved on the server and is the same in every GUI. API: `labels` on groups (`""`, `name`, `ip`, `both`); `inedctl group labels <id> name|ip|both|inherit`. The database gets a new column on first start (schema 4). For a group that takes its parent's mode, the menu shows that mode in brackets ("As the parent group (IP address)"); the details panel has a "Host labels" row. `inedctl group add --labels`.

### Changed
- `ined-gui`: with several groups selected, the context menu's "Expand", "Collapse" and "Host labels" act on all selected groups ("Expand 2 groups", "Host labels (2 groups)"), like "Move" and "Delete" and the keys **c** and **e**.

### Fixed
- `ined-gui`: with a group and an item inside it both selected, the context menu acted as if both were chosen ("Move 2 items…", "Delete 2 items…") and its delete question had no "delete everything" choice. It now acts on the group only, as the keys do, with the group's usual delete question.
- When the server switches to a better ping mode, the old one is now closed when the last check using it ends, instead of after a fixed two minutes, which a long check (many pings with a long timeout) could exceed.

## [0.2.0] - 2026-11-12

### Added
- `ined-gui`: "Wrap names" (toolbar toggle, on by default, saved in `gui.toml`): a long DNS name breaks after its dots where it would run into the next item. "Reflow…" on a group and "Reflow view…" lay out the members on a grid wide enough for the longest full name.
- `ined-gui`: select several items (Ctrl+click, Shift+drag rubber band); keys as in Tkined: **g** groups the selection, **c** / **e** collapse and expand the selected groups, Delete deletes the selection, Esc clears it; dragging a selected item moves the whole selection. A right-click on one of the selected items keeps the selection, and the menu's "Move N items to group…" and "Delete N items…" act on all of them.
- `ined-gui`: expanding and collapsing a group keep its top-left corner in place: the frame opens where the icon was, and the icon appears where the frame started. API: `POST /groups/{id}/move` takes `"contents_only": true`.
- API: `POST /views/{id}/arrange` lays out a group's members (or a view's top level) on a grid, with one `map.arranged` event; `inedctl view arrange`.

### Changed
- When the server can't ping (no ICMP mode), it now checks again every minute and before each scan, so allowing ping (the sysctl or `setcap`) works without restarting it; the log says when ICMP is found. The GUI re-reads the server's health every 30 seconds, so its ping warning goes away by itself.

## [0.1.0] - 2026-11-11

The first release: a network monitor in the spirit of Tkined, with a server, a command-line client and a desktop GUI.

### Server (`ined-server`)
- Inventory of hosts and groups in SQLite (pure Go, no cgo). Groups form a tree; a group with a CIDR is a network group, and a new host goes into the most specific network group that contains its address. Every host and group has a note.
- **Views:** besides the network view, custom maps (tabs in the GUI) with their own groups, where hosts can appear as well. A host is pinged once and has the same status in every view.
- **Monitoring:** every monitored host is pinged in the background (ICMP and ICMPv6), with up/down/unknown status, round-trip time, packet loss, `last_seen` and `last_change`. Group status is the worst status of the hosts below it. Unprivileged ping sockets are tried first, then raw sockets; when neither works, the server keeps running, says so, and names the commands that allow ping (see README, "Ping permissions").
- **Discovery** of IPv4 ranges (up to /16, rate-limited): the hosts that answer go into the network group for the range. Jobs can be followed and cancelled.
- **Reverse DNS** names, looked up when a host is added and refreshed periodically.
- **REST API** under `/api/v1` with a **live event stream** (Server-Sent Events) that resumes after a reconnect; the API describes itself at `GET /api/v1/openapi.json` (OpenAPI 3.1).
- Optional bearer token (required when listening beyond loopback) and TLS. TOML configuration with XDG paths; `[log]` level and format.
- Safe to run: one server per database (lock), ids never reused, automatic schema migrations, repairs of broken links at startup, clean shutdown.

### Command line (`inedctl`)
- Hosts, groups, notes, views, discovery (`--wait` follows a job), the event stream and health; tables or the API's JSON (`--json`); clear exit codes (0 OK, 1 API error, 2 usage, 3 connection, 130 interrupted).
- `group shift` moves a group with everything in it.

### Desktop GUI (`ined-gui`)
- One tab per view. Hosts and groups on a map, with status shown as a shape and a word (never by colour alone); network groups with their own icon and dashed frames; notes and stale status marked.
- Expand and collapse groups, pan, zoom, fit all, and find a host.
- Edit on the map: drag and drop (a group takes its contents along), drop into a group, create, edit and delete groups and hosts with confirmations, place hosts in other views, manage views.
- A discovery dialog with progress and cancel; messages and errors can be copied.
- Live updates with automatic reconnect; the status bar shows the connection state and warns when the server can't ping.
- White background with black text by default; contrast presets AA (4.5:1), AAA (7:1), Max (black and white) and custom, applied at once and saved in `gui.toml`.
- Stays responsive with thousands of hosts.

### Go package
- `pkg/client`: a typed client for every endpoint and an event stream that reconnects and resumes by itself.

### Packaging
- Archives for linux, macOS and Windows (amd64, arm64) with `ined-server` and `inedctl`, and `ined-gui` for linux/amd64, with `SHA256SUMS`. Built with Go 1.26.11.
- Every change is checked in CI: formatting, vet, staticcheck, govulncheck, the tests with the race detector, raw-socket ping as root, and all builds.
