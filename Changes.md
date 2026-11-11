# Changes

All notable changes between releases. Format based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

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
