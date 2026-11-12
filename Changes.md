# Changes

All notable changes between releases. Format based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

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
