# Spec — go-ined

Status: **draft v0.4** (2026-11-10)
Withdrawn IDs are kept and marked ~~withdrawn~~ so they aren't reused.
Every requirement has a stable ID (`SPEC-NNN`). Tests in `Tests.md` reference these IDs.
Keywords: **MUST** = required for v0.1, **SHOULD** = wanted for v0.1 but can slip, **LATER** = out of scope for v0.1.

## 1. Purpose
go-ined is a network monitor loosely modelled on Tkined (Scotty). It splits Tkined's single app into:
- **`ined-server`**: a daemon that holds the inventory (SQLite), discovers hosts in CIDR ranges, pings them in the background, does reverse DNS lookups, and serves a REST + SSE API.
- **`ined-gui`**: a Fyne desktop client that shows hosts and groups (including network groups) on one shared map with live status. Groups can be collapsed and expanded, as in Tkined.
- **`inedctl`**: a command-line client for scripting and testing.

## 2. Assumptions (confirm or correct)
- **A1** Up to about 5,000 hosts per server.
- **A2** Discovery sweeps are IPv4 only. IPv6 hosts can be added by hand (scanning IPv6 ranges isn't feasible).
- **A3** By default the server listens on loopback only. A non-loopback address requires a bearer token.
- **A4** The config file format is TOML. Command-line flags override the config file.
- **A5** v0.1 keeps only the current status, not a status history.

## 3. Cross-cutting requirements

### UI colour and contrast (applies to any GUI/TUI component)
- **SPEC-001** MUST: The default theme is a white background with black text.
- **SPEC-002** MUST: Every foreground/background colour pair has a WCAG 2.x contrast ratio ≥ 4.5:1.
- **SPEC-003** MUST: Contrast presets: `AA` (≥ 4.5:1, default), `AAA` (≥ 7:1), `Max` (21:1, pure black on pure white), and `custom` (a user-given minimum ratio between 4.5 and 21).
- **SPEC-004** MUST: The user can change the contrast preset interactively at runtime.
- **SPEC-005** MUST: The contrast preset or custom ratio can be set in a config file. A change made at runtime takes effect without a restart.

### Build / CI
- **SPEC-006** MUST: Every CI action (GitHub Actions or similar) is pinned to a full 40-character commit SHA, with the version in a trailing comment, using the latest workable release. Any exception has its reason documented inline.

## 4. Inventory and data model
There are two kinds of **items**: host and group. A group with a `cidr` is a **network group**. Items form a tree, and all items share one map.

- **SPEC-101** MUST: A **host** has an id, a unique IP address, a PTR name, an optional manual name, a description, a note, an optional parent group, a monitored flag (default on), a status, and created/updated timestamps.
- **SPEC-102** MUST: A **group** has an id, a name, a description, a note, an optional `cidr` (IPv4 or IPv6), an optional parent group, and a `collapsed` flag. Hosts and groups form a **tree**: each item has at most one parent, and an item with no parent sits at the root.
- **SPEC-103** MUST: Moving a group into itself or into one of its own subgroups is rejected (no cycles).
- **SPEC-104** MUST: Deleting a non-empty group is rejected unless the request says what to do with its children: `reparent` (move them to the group's parent) or `cascade` (delete them as well).
- **SPEC-105** MUST: A host's status is one of `up`, `down` or `unknown`. A group's status is the worst status among all monitored hosts in its subtree, in the order `down` > `unknown` > `up`. A group with no monitored hosts in its subtree is `unknown`. Network groups follow the same rule.
- **SPEC-106** MUST: Every item has (x, y) coordinates on the single shared map. A new item gets a free position near its parent.
- **SPEC-107** MUST: A host can be added by hand by giving its IP address (IPv4 or IPv6). If no parent is given, it goes into the most specific network group whose CIDR contains its IP. If none matches, it goes to the root.
- **SPEC-108** ~~withdrawn~~ (v0.4: separate network objects replaced by network groups, SPEC-113)
- **SPEC-109** ~~withdrawn~~ (v0.4: no host-to-network links)
- **SPEC-110** ~~withdrawn~~ (v0.4: a network group's status follows SPEC-105)
- **SPEC-111** MUST: Every host and group has one plain-text **note**. It is optional, empty by default, and at most 64 KiB.
- **SPEC-112** MUST: A group's `collapsed` flag is stored on the server and shared by all clients. New groups start collapsed. An item is **visible** on the map only if none of the groups above it is collapsed.
- **SPEC-113** MUST: A group's `cidr` must be a valid, unique range in canonical form, with no host bits set. Two network groups can't share the same CIDR.
- **SPEC-114** MUST: A network group nested inside another network group must have a CIDR within its ancestor's range. A create, move or update that breaks this is rejected. Ordinary groups (without `cidr`) can sit anywhere, and hosts can be moved anywhere: network membership is a starting point, not enforced.
- **SPEC-115** SHOULD: A host whose IP is outside the CIDR of the nearest network group above it gets an `outside_cidr` flag in the API. This is informational only.
## 5. Discovery
- **SPEC-201** MUST: A discovery job takes either an IPv4 CIDR range or the id of an IPv4 network group, and probes every address in the range with ICMP echo. Hosts it finds go into the network group for that range. If no network group with that CIDR exists, one is created (named after the CIDR) under the optional target group, or at the root if none is given.
- **SPEC-202** MUST: For prefixes /30 and shorter, the network and broadcast addresses are skipped. For /31 and /32, every address is probed.
- **SPEC-203** MUST: The largest allowed range (default /16), the probe rate (default 200 probes/s) and the concurrency are configurable. A range larger than the maximum is rejected.
- **SPEC-204** MUST: A host that responds is added to the inventory, in the network group from SPEC-201. An IP address that is already in the inventory is not added twice, and that host is not moved.
- **SPEC-205** MUST: Each discovery job has an id, a state (`running`, `done`, `cancelled` or `failed`) and progress (addresses probed, total, hosts found). A job can be cancelled. Progress is published as events.

## 6. Monitoring
- **SPEC-301** MUST: Every monitored host is pinged in the background every `interval` (default 60 s). Start times are spread across the interval so the pings don't all go out at once.
- **SPEC-302** MUST: Each round sends `count` echo requests (default 3) with a `timeout` (default 1 s). A host is `up` if at least one reply arrives. It becomes `down` after `down_after` failed rounds in a row (default 2). Before its first result, a host is `unknown`.
- **SPEC-303** MUST: Each round records the average round-trip time, the packet loss in percent, `last_seen` (time of the last reply) and `last_change` (time of the last status change).
- **SPEC-304** MUST: The server tries unprivileged ICMP first and falls back to privileged (raw) ICMP. The startup log says which mode is in use. If neither works, the server keeps running, marks hosts `unknown` and reports a clear error through the API's health endpoint.
- **SPEC-305** MUST: When a host's status changes, the server emits a `host.status` event. When a group's computed status changes as a result, it emits `group.status` for that group.
- **SPEC-306** MUST: A host with monitoring turned off (`monitored = false`) is not pinged and is left out of group status.

## 7. Reverse DNS
- **SPEC-401** MUST: The server looks up the PTR record when a host is added, and then every `dns_refresh` (default 1 h).
- **SPEC-402** MUST: The name shown for a host is its manual name if set, otherwise the first PTR name (without the trailing dot), otherwise its IP address.
- **SPEC-403** MUST: A temporary DNS error keeps the last known PTR name. An NXDOMAIN answer (the name doesn't exist) clears it. DNS results never affect a host's status.

## 8. API (REST + SSE)
- **SPEC-501** MUST: The JSON REST API lives under `/api/v1`:
  - `GET/POST /hosts`, `GET/PATCH/DELETE /hosts/{id}`
  - `GET/POST /groups`, `GET/PATCH/DELETE /groups/{id}?children=reparent|cascade`
  - `GET /map`: every item with its type, parent, position, `collapsed` flag, `cidr`, status, display name, `has_note` flag and `outside_cidr` flag. A client builds the map from this one response.
  - `POST /discovery`, `GET /discovery/{id}`, `DELETE /discovery/{id}` (cancel)
  - `GET /health`: version, ICMP mode, database status
  - Moving, repositioning, collapsing, expanding and editing notes are all done with `PATCH` on `parent_id`, `x` / `y`, `collapsed` or `note`.
- **SPEC-502** MUST: `GET /api/v1/events` is a Server-Sent Events (SSE) stream. Event types: `host.created|updated|deleted|status`, `group.created|updated|deleted|status`, `discovery.progress|done`. Every event has an increasing `id`.
- **SPEC-503** MUST: The server keeps a buffer of the most recent events (default 1,000). A client that reconnects with `Last-Event-ID` gets the events it missed. If those events are no longer in the buffer, it gets a `resync` event, which tells it to reload the full state.
- **SPEC-504** MUST: Errors use the right HTTP status code with the body `{"error":{"code":"…","message":"…"}}`.
- **SPEC-505** SHOULD: The server serves an OpenAPI document at `/api/v1/openapi.json`.
- **SPEC-506** MUST: A Go client package (`pkg/client`) wraps the REST and SSE API. The GUI and the CLI both use it.

## 9. Storage
- **SPEC-601** MUST: Data is stored in SQLite through `modernc.org/sqlite` (pure Go, no cgo). The database file path is configurable.
- **SPEC-602** MUST: The database schema is versioned. Migrations run automatically at startup.
- **SPEC-603** MUST: The current status, round-trip time, packet loss, `last_seen` and `last_change` are saved to the database, so a restarted server shows the last known state. A status saved before a restart counts as stale until the next ping round.

## 10. GUI (Fyne)
- **SPEC-701** MUST: `ined-gui` connects to a server URL (and token) given in its config file or by command-line flags.
- **SPEC-702** MUST: The GUI shows **one shared map** with every visible item at its stored position (SPEC-112). Hosts and collapsed groups are icons labelled with their display name. A network group has its own icon shape and frame style and also shows its CIDR, so it can be told apart without relying on colour. Status is shown by **colour plus shape plus text** (for example: up = ● "up", down = ✕ "down", unknown = ? "unknown"), never by colour alone.
- **SPEC-703** MUST: A **collapsed** group is one icon showing the group's status. An **expanded** group is a labelled frame around its visible members, with the group's name and status. Double-clicking a collapsed group icon expands it. Double-clicking an expanded group's frame label collapses it. Both actions are also in the context menu, and the change is saved on the server.
- **SPEC-704** MUST: The user can drag hosts, collapsed groups and expanded group frames. Dragging an expanded frame moves all its members with it. New positions are saved on the server. Dropping an item onto a collapsed group icon or an expanded frame label moves it into that group. A context menu action "Move to group…" does the same.
- **SPEC-705** MUST: The GUI updates live from SSE events. It reconnects automatically and shows the connection state.
- **SPEC-706** MUST: From the GUI, the user can create, rename and delete groups (setting or clearing the CIDR) and hosts.
- **SPEC-707** MUST: A discovery dialog takes a CIDR range and a target group. It shows the job's progress and lets the user cancel it. "Scan this network" is available from a network group's context menu.
- **SPEC-708** MUST: A details panel shows every field of the selected host or group: for a host, its IP address, PTR name, manual name, status, round-trip time, packet loss, `last_seen` and `last_change`. The panel includes an editable **note**.
- **SPEC-709** MUST: SPEC-001 to SPEC-005 apply to the GUI. A contrast preset selector is available in the GUI.
- **SPEC-710** ~~withdrawn~~ (v0.4: no network link lines)
- **SPEC-711** SHOULD: An item that has a note shows a small note marker. The marker is a shape, not a colour.
- **SPEC-712** SHOULD: A collapsed group's icon shows its host counts, for example `3 down · 1 ? · 40 up`.

## 11. Configuration and security
- **SPEC-801** MUST: The server listens on `127.0.0.1:8470` by default. The address is configurable.
- **SPEC-802** MUST: Bearer-token authentication is optional, but required when the server listens on a non-loopback address. Without a valid token, the server answers 401.
- **SPEC-803** SHOULD: TLS can be turned on by configuring a certificate file and a key file.
- **SPEC-804** MUST: The server shuts down cleanly on SIGINT or SIGTERM. It finishes database writes and closes SSE streams.

## 12. LATER (not in v0.1)
- A 9P front end that exposes the inventory as a file tree.
- A ZMQ publisher for alerts.
- TCP-port probes for hosts that block ICMP, and ARP discovery.
- Status history and graphs.
- Importing Tkined `.tki` map files.
- Free-floating text labels on the map, like Tkined's text objects.
- A drill-down view that opens a single group on its own.
- Host references, so one host can appear in several groups (for example its subnet and a functional group, or a router on several networks).
- A topology view with link lines.
- Notes as a log with dates and authors.
- mTLS, multiple users, read-only roles.
