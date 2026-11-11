# Spec — go-ined

Status: **draft v0.21** (2026-11-11)
Withdrawn IDs are kept and marked ~~withdrawn~~ so they aren't reused.
Every requirement has a stable ID (`SPEC-NNN`). Tests in `Tests.md` reference these IDs.
Keywords: **MUST** = required for v0.1, **SHOULD** = wanted for v0.1 but can slip, **LATER** = out of scope for v0.1.

## 1. Purpose
go-ined is a network monitor loosely modelled on Tkined (Scotty). It splits Tkined's single app into:
- **`ined-server`**: a daemon that holds the inventory (SQLite), discovers hosts in CIDR ranges, pings them in the background, does reverse DNS lookups, and serves a REST + SSE API.
- **`ined-gui`**: a Fyne desktop client that shows hosts and groups (including network groups) on one shared map with live status. Groups can be collapsed and expanded, as in Tkined.
- **`inedctl`**: a command-line client for scripting and testing.

## 2. Assumptions (confirmed by the user, 2026-11-10)
- **A1** Up to about 5,000 hosts per server.
- **A2** Discovery sweeps are IPv4 only. IPv6 hosts can be added by hand (scanning IPv6 ranges isn't feasible).
- **A3** By default the server listens on loopback only. A non-loopback address requires a bearer token.
- **A4** The config file format is TOML. Command-line flags override the config file.
- **A5** v0.1 keeps only the current status, not a status history.

## 3. Cross-cutting requirements

### UI colour and contrast (applies to any GUI/TUI component)
- **SPEC-001** MUST: The default theme is a white background with black text.
- **SPEC-002** MUST: Every foreground/background colour pair has a WCAG 2.x contrast ratio ≥ 4.5:1.
- **SPEC-003** MUST: Contrast presets: `AA` (≥ 4.5:1, default), `AAA` (≥ 7:1), `Max` (21:1, pure black on pure white), and `custom` (a user-given minimum ratio between 4.5 and 21). In `Max`, the palette is black and white only, so status is shown by shape and text alone (SPEC-702 guarantees both). In the GUI, buttons then get a black border, because their background is white like the window's (v0.20).
- **SPEC-004** MUST: The user can change the contrast preset interactively at runtime.
- **SPEC-005** MUST: The contrast preset or custom ratio is read from the GUI config file at startup. A change made in the GUI applies immediately, with no restart, and is saved back to the config file.

### Build / CI
- **SPEC-006** MUST: Every CI action (GitHub Actions or similar) is pinned to a full 40-character commit SHA, with the version in a trailing comment, using the latest workable release. Any exception has its reason documented inline.

## 4. Inventory and data model
There are two kinds of **items**: host and group. A group with a `cidr` is a **network group**. Items form a tree, and all items share one map.

- **SPEC-101** MUST: A **host** has an id, a unique IP address, a PTR name, an optional manual name, a description, a note, an optional parent group, a monitored flag (default on), a status, and created/updated timestamps.
- **SPEC-102** MUST: A **group** has an id, a name, a description, a note, an optional `cidr` (IPv4 or IPv6), an optional parent group, and a `collapsed` flag. If a group is created with a CIDR and no name, its name is the CIDR; a name can't be changed to empty. Hosts and groups form a **tree**: each item has at most one parent, and an item with no parent sits at the root. With views (§4b) this holds per view (v0.16).
- **SPEC-103** MUST: Moving a group into itself or into one of its own subgroups is rejected (no cycles).
- **SPEC-104** MUST: Deleting a non-empty group is rejected unless the request says what to do with its children: `reparent` (move them to the group's parent) or `cascade` (delete them as well).
- **SPEC-105** MUST: A host's status is one of `up`, `down` or `unknown`. A group's status is the worst status among all monitored hosts in its subtree, in the order `down` > `unknown` > `up`. A group with no monitored hosts in its subtree is `unknown`. Network groups follow the same rule.
- **SPEC-106** MUST: Every item has (x, y) coordinates on the single shared map. A new item gets a free position near its parent; a group's members start one row below it, far enough (120 units) that its labels and, in the GUI, the label strip of its expanded frame don't overlap (v0.19). Rows grow with the group (about the square root of its size, 10–50 per row), so a large group is about square (v0.20).
- **SPEC-107** MUST: A host can be added by hand by giving its IP address (IPv4 or IPv6). If `parent_id` is omitted or null, it goes into the most specific network group whose CIDR contains its IP. If none matches, it goes to the root. Changing the IP later doesn't move the host; `outside_cidr` (SPEC-115) shows any mismatch.
- **SPEC-108** ~~withdrawn~~ (v0.4: separate network objects replaced by network groups, SPEC-113)
- **SPEC-109** ~~withdrawn~~ (v0.4: no host-to-network links)
- **SPEC-110** ~~withdrawn~~ (v0.4: a network group's status follows SPEC-105)
- **SPEC-111** MUST: Every host and group has one plain-text **note**. It is optional, empty by default, and at most 64 KiB. Other text limits: a name is at most 255 bytes, and a description at most 4 KiB.
- **SPEC-112** MUST: A group's `collapsed` flag is stored on the server and shared by all clients. New groups start collapsed. An item is **visible** on the map only if none of the groups above it is collapsed.
- **SPEC-113** MUST: A group's `cidr` must be a valid, unique range in canonical form, with no host bits set. Two network groups can't share the same CIDR.
- **SPEC-114** MUST: A network group nested inside another network group must have a CIDR within its ancestor's range. A create, move or update that breaks this is rejected. Ordinary groups (without `cidr`) can sit anywhere, and hosts can be moved anywhere: network membership is a starting point, not enforced.
- **SPEC-115** SHOULD: A host whose IP is outside the CIDR of the nearest network group above it gets an `outside_cidr` flag in the API. This is informational only.
- **SPEC-116** MUST: Ids of hosts and groups are never reused, not even after the item with the highest id is deleted and the server restarts. The id sequences are stored in the database.
## 4b. Views (M4b, v0.16)
Design: `Design-views.md` (approved 2026-11-10, V1–V6 as recommended). A **view** is one map: one tab in the GUI. Hosts are shared by all views (one status, one note, pinged once); each view has its own groups and its own placement of hosts. Where these items conflict with §4, they take precedence: §4's "tree" and "single map" hold **per view**.

- **SPEC-117** MUST: A **view** has an id, a name, a kind (`network` or `custom`), an `order` and created/updated timestamps. There is exactly one network view, with id 1, created by the server (initial name "Networks"). It can be renamed and reordered but not deleted (409). Custom views can be created, renamed, reordered and deleted by any client; views are shared by all clients (V5). Names are unique and 1–255 bytes. Views are listed by `order`, then id; a new view gets an `order` after all others. View ids are never reused (SPEC-116).
- **SPEC-118** MUST: Every group belongs to exactly one view (`view_id`, default 1), fixed when the group is created. A group's parent must be a group in the same view (400 otherwise). Network groups (with `cidr`) exist only in the network view: a `cidr` in a custom view is rejected with 400 (V2). SPEC-103, 104 and 112 apply within each view.
- **SPEC-119** MUST: A **placement** puts a host in a view: a parent group of that view (or the view's root) and a position. In the network view every host has exactly one placement: its `parent_id`, `x` and `y`, with SPEC-106/107 as before. In a custom view a host has at most one placement (V1), and only where a client put it. Hosts created by hand or by discovery are placed only in the network view (V4). A placement without `x`/`y` gets a free position near its parent (SPEC-106).
- **SPEC-120** MUST: Deleting in a custom view never deletes hosts (V3). Deleting a group with `children=cascade` removes its subgroups and the placements in them; `children=reparent` moves its subgroups and placements to its parent. Deleting a custom view removes all its groups and placements. Deleting a host removes all its placements. In the network view, SPEC-104 is unchanged (cascade deletes hosts).
- **SPEC-121** MUST: Group status (SPEC-105) and visibility (SPEC-112) are evaluated within the group's view, over the hosts placed in that view. A host status change can therefore change the status of groups in several views; each change emits `group.status`. `outside_cidr` (SPEC-115) refers to the network placement only.
- **SPEC-122** MUST: API (additive; today's calls keep their meaning):
  - `GET/POST /views`, `GET/PATCH/DELETE /views/{id}` (`name`, `order`; deleting view 1 → 409).
  - `GET /map?view=<id>`: the items of one view, with the placement's parent and position for hosts. Without `view`, the network view.
  - `PUT /views/{id}/hosts/{host_id}` with `{parent_id, x, y}` adds or moves a host in a custom view; `DELETE /views/{id}/hosts/{host_id}` removes it from the view. For view 1 both return 400: there, use `PATCH /hosts/{id}`.
  - `POST /groups` takes `view_id` (default 1); group JSON has `view_id`. `GET /groups?view=<id>` lists one view's groups; without `view`, all groups of all views, each with its `view_id` (v0.17).
  - Host JSON keeps `parent_id`, `x`, `y`: the network placement.
  - Events (SPEC-502): `view.created|updated` (full view), `view.deleted` (`{id}`, one event; the view's groups and placements go with it), `placement.updated` (`{view_id, host_id, parent_id, x, y}`), `placement.deleted` (`{view_id, host_id}`). Deleting a host emits only `host.deleted`, also for its placements. Cascade or reparent in a custom view emits `placement.deleted` / `placement.updated` and `group.deleted` / `group.updated` per item.
  - `GET /map` returns `view_id` next to `items`; in custom views `outside_cidr` is always `false`.
- **SPEC-123** MUST: Storage: migration 3 adds views and custom-view placements; an existing database is upgraded with all its groups in view 1. A host's network placement stays in the `hosts` table (decision M4b-1). The startup check (SPEC-605) also covers views: a group of a missing view moves to view 1's root, a group or placement whose parent is in another view or missing moves to the root of its view, each with a warning.
- **SPEC-124** MUST: `inedctl view list|add|show|rename|order|rm|place|unplace`, `--view` on `group add` and `group list`. `view show <id>` prints the view and a table of its hosts (with `--json`: the view only). `view place <view> <host> [--parent ID] [--x X --y Y]`; without `--parent` the host goes to the view's root, as with the API's `PUT` (v0.17).

## 5. Discovery
- **SPEC-201** MUST: A discovery job takes either an IPv4 CIDR range or the id of an IPv4 network group, and probes every address in the range with ICMP echo. Hosts it finds go into the network group for that range. If no network group with that CIDR exists, one is created (named after the CIDR) under the optional target group, or at the root if none is given. An IPv6 range or IPv6 network group is rejected with 400 (A2). A job whose new network group would break SPEC-114 is rejected with 409 before it starts. Each address gets one echo request, and a second one 200 ms later only if the first gets no answer; each waits up to `monitor.timeout`. Discovery uses the same ICMP prober as monitoring and works even with `monitor.enabled = false`. Without a working IPv4 ICMP mode, `POST /discovery` returns 503 `unavailable` with the reason (v0.11).
- **SPEC-202** MUST: For prefixes /30 and shorter, the network and broadcast addresses are skipped. For /31 and /32, every address is probed.
- **SPEC-203** MUST: The largest allowed range (default /16), the probe rate (default 200 probes/s) and the concurrency are configurable. A range larger than the maximum is rejected. `discovery.rate` must be between 1 and 100,000. A job whose range has more addresses than A1 (5,000) logs a warning, since the server is designed for about that many hosts (v0.12).
- **SPEC-204** MUST: A host that responds is added to the inventory, in the most specific network group inside the subtree of the SPEC-201 group that contains its IP (v0.11; for example, discovering 10.0.0.0/16 puts 10.0.1.5 into an existing 10.0.1.0/24 subgroup). It is added with `monitored = true` and status `up`, with round-trip time, loss and `last_seen` from the probe; monitoring takes over from there. Found hosts are saved in batches of about 1 s, with one `host.created` event per host. An IP address that is already in the inventory is not added twice, and that host is not moved.
- **SPEC-205** MUST: Each discovery job has an id, a state (`running`, `done`, `cancelled` or `failed`) and progress (addresses probed, total, hosts found). A job can be cancelled; hosts found until then are kept. Progress is published as events: `discovery.progress` at most once a second, and `discovery.finished` at the end, both carrying the full job (v0.11). Jobs are kept in memory only, so they are gone after a restart. Job ids are strings `<run>-<n>`. The last 100 finished jobs are kept. A job whose range overlaps a running job is rejected with 409 (v0.11). A probe that fails with an error (for example, no route to the range) counts as probed but not found; the job counts these in `errors`, and `error` gives the first message even when the state is `done`. If saving found hosts fails, the job stops with state `failed` and `error` set; hosts saved before that are kept (v0.12).

## 6. Monitoring
- **SPEC-301** MUST: Every monitored host is pinged in the background every `interval` (default 60 s). `monitor.enabled = false` turns pinging off for the whole server; hosts then keep their stored status and `/health` reports `icmp_mode: disabled` (v0.9). Start times are spread across the interval so the pings don't all go out at once.
- **SPEC-302** MUST: Each round sends `count` echo requests (default 3) with a `timeout` (default 1 s). The requests of a round are at least 200 ms apart, so devices that rate-limit ICMP don't drop them (v0.10). A host is `up` if at least one reply arrives. It becomes `down` after `down_after` failed rounds in a row (default 2). Before its first result, a host is `unknown`.
- **SPEC-303** MUST: Each round records the average round-trip time, the packet loss in percent, `last_seen` (time the last reply arrived, not the end of the round; v0.10) and `last_change` (time of the last status change).
- **SPEC-304** MUST: The server tries unprivileged ICMP first and falls back to privileged (raw) ICMP. The startup log says which mode is in use. If neither works, the server keeps running, marks hosts `unknown` and reports a clear error through the API's health endpoint (`status: degraded`, `icmp_mode: unavailable`, `icmp_error`). IPv4 hosts are pinged with ICMP, IPv6 hosts with ICMPv6 (v0.8). The mode is chosen per address family. If IPv6 ICMP is unavailable while IPv4 works, IPv6 hosts are not pinged, their status isn't changed, and `/health` reports `icmp6_error`; `status` stays `ok` (v0.10). In raw mode, all rounds of one family share one socket, so each reply is received once (v0.10).
- **SPEC-305** MUST: When a host's status changes, the server emits a `host.status` event. Whenever a group's computed status changes, for any cause (a host status change, a move, an add, a delete, or a change to `monitored`), the server emits `group.status` for that group.
- **SPEC-306** MUST: A host with monitoring turned off (`monitored = false`) is not pinged and is left out of group status.
- **SPEC-307** MUST (v0.8): `host.status` is emitted only when a host's status changes or its `stale` flag clears. Changes to round-trip time or loss alone are saved but emit no event. Ping results are written to the database in batches, one transaction about every second.
- **SPEC-308** MUST (v0.9): When a host's IP address changes, its status is reset to `unknown`, its round-trip time and loss are cleared, `last_change` is set if the status changed, and `host.status` is emitted after `host.updated`. The ping streak starts over.

## 7. Reverse DNS
- **SPEC-401** MUST: The server looks up the PTR record when a host is added, and then every `dns.refresh` (default 1 h), with at most `dns.concurrency` lookups running at once (default 20). It uses Go's resolver (the system's DNS settings), with 5 s per lookup. At startup, the refreshes of the existing hosts are spread across `dns.refresh`. A changed name is saved and emits `host.updated`. When a host's IP changes, its PTR name is cleared at once and looked up again (v0.11). `dns.enabled = false` turns reverse DNS off; PTR names then stay as stored (v0.11).
- **SPEC-402** MUST: The name shown for a host is its manual name if set, otherwise the first PTR name (without the trailing dot), otherwise its IP address.
- **SPEC-403** MUST: A temporary DNS error keeps the last known PTR name. An NXDOMAIN answer (the name doesn't exist) clears it. Of several PTR names, the first is used. DNS results never affect a host's status.

## 8. API (REST + SSE)
- **SPEC-501** MUST: The JSON REST API lives under `/api/v1`:
  - `GET/POST /hosts`, `GET/PATCH/DELETE /hosts/{id}`
  - `GET/POST /groups`, `GET/PATCH/DELETE /groups/{id}?children=reparent|cascade`
  - `GET /map`: every item with its type, parent, position, `collapsed` flag, `cidr`, status, `stale` flag, display name, `has_note` flag, `outside_cidr` flag, `monitored` flag (v0.19, for SPEC-712) and, for hosts, `ip`. Flags that don't apply to an item type (`collapsed` on hosts, `outside_cidr` and `monitored` on groups) are `false`. A client builds the map from this one response.
  - `POST /discovery` (202 with the new job), `GET /discovery/{id}`, `DELETE /discovery/{id}` (cancel; 200 with the job's state at that moment, `discovery.finished` follows)
  - `GET /health`: without a token, returns only `{status, version}`. With a valid token, it also returns the ICMP mode (`unprivileged`, `privileged`, `unavailable` or `disabled`), `icmp_error` and `icmp6_error` when set, the database status and the run id. `status` is `degraded` when the database fails or no ICMP mode works (v0.10).
  - Moving, repositioning, collapsing, expanding and editing notes are all done with `PATCH` on `parent_id`, `x` / `y`, `collapsed` or `note`. In a `PATCH`, an absent field is left unchanged; `null` is allowed only for `parent_id` (the root) and is rejected with 400 for every other field (v0.17).
  - Views and placements: SPEC-122.
- **SPEC-502** MUST: `GET /api/v1/events` is a Server-Sent Events (SSE) stream. Event types: `host.created|updated|deleted|status`, `group.created|updated|deleted|status`, `discovery.progress|finished` (`finished` includes the final `state`: `done`, `cancelled` or `failed`), `view.created|updated|deleted` and `placement.updated|deleted` (SPEC-122), and `resync`.
  Payloads (v0.8): `*.created` and `*.updated` carry the full object (the same JSON as `GET`); `*.deleted` carries `{"id": …}`, one event per deleted item (a cascade delete emits one per host and group); `host.status` carries the full host; `group.status` carries `{"id": …, "status": …}`; `resync` carries `{"reason": …}`. The server sends a keepalive comment every 30 s. The token is sent in the `Authorization` header, as for every other endpoint. A client that falls too far behind gets `resync` and the stream ends. A client that accepts no data for 10 s is disconnected, so it can't hold up the server or its shutdown (v0.10).
- **SPEC-503** MUST: Every event id has the form `<run>-<seq>`. `run` is a random id chosen at server start, and `seq` increases within a run. The server keeps a buffer of the most recent events (`events.buffer`, default 1,000). A client that reconnects with `Last-Event-ID` gets the events it missed. If those events are no longer in the buffer, or the id comes from a different run, the client gets a `resync` event, which tells it to reload the full state. The `resync` event carries the id of the newest event, so a client that reloads and keeps the stream open continues without a gap (v0.8).
- **SPEC-504** MUST: Errors use the right HTTP status code with the body `{"error":{"code":"…","message":"…"}}`:

  | HTTP | `code` | When |
  |------|--------|------|
  | 400 | `invalid_argument` | Malformed JSON, unknown JSON field, `null` for a field that can't be null (v0.17), invalid IP or CIDR, non-canonical CIDR, IPv6 discovery, a `parent_id` that doesn't exist or is in another view, a `cidr` in a custom view, placing a host in view 1, text over its length limit (except notes) |
  | 401 | `unauthenticated` | Missing or invalid token |
  | 404 | `not_found` | Unknown id in the path, unknown endpoint |
  | 405 | `method_not_allowed` | Method not supported on an existing endpoint (with an `Allow` header) |
  | 409 | `conflict` | Duplicate IP, CIDR or view name, cycle, non-empty group delete, SPEC-114 violation, deleting the network view |
  | 413 | `too_large` | Note over 64 KiB, request body too large |
  | 503 | `unavailable` | The server is shutting down (for example, a new `GET /events` during shutdown) (v0.9) |
  | 500 | `internal` | Unexpected server error |
- **SPEC-505** SHOULD: The server serves an OpenAPI document at `/api/v1/openapi.json`.
- **SPEC-506** MUST: A Go client package (`pkg/client`) wraps the REST and SSE API. The GUI and the CLI both use it. The JSON types live in `pkg/apitypes` and are shared by the server and all clients. API errors are returned as a typed `*client.APIError` with the HTTP status, `code` and message. The event stream reconnects automatically (waiting 1 s at first, doubling up to 30 s), resumes with `Last-Event-ID`, and passes `resync` on to the caller (v0.13). It stops for good on a 4xx answer (for example 401) and retries 5xx answers and connection errors. A connection that sends nothing, not even a keepalive, for 75 s is dropped and reopened. Optionally the first connection attempt may fail at once instead of being retried; `inedctl` uses this to report an unreachable server (v0.15).
- **SPEC-507** MUST (draft v0.21): **Moving a group with everything in it.** `POST /groups/{id}/move` with `{"dx": …, "dy": …}` moves the group and every item in its subtree, in the group's view, by the same offset in one transaction. In the network view that changes the hosts' `x`/`y`; in a custom view their placements. It emits one `group.moved` event `{"id", "view_id", "dx", "dy"}` instead of one event per item (SPEC-502); clients that keep positions apply the offset or reload the view. The offset must be finite (400).
- **SPEC-508** MUST (draft v0.21): **A new parent gives a new position.** When `PATCH` changes an item's `parent_id` (or `PUT` a placement's parent) without giving `x` and `y`, the item gets a free position near its new parent (SPEC-106), as new items do; a group's subtree moves along by the same offset. With `x` and `y` given, they are used. This changes `inedctl host move` and `group move`, which so far kept the old position.
- **SPEC-509** MUST (draft v0.21): Host JSON has `views`: the ids of the views the host is placed in, in tab order (the network view always first). It is filled in `GET /hosts` and `GET /hosts/{id}` and in host events. A placement change emits `placement.*` only, so a client that shows `views` reloads the host on `placement.*`.

## 9. Storage
- **SPEC-601** MUST: Data is stored in SQLite through `modernc.org/sqlite` (pure Go, no cgo). The database file path is configurable.
- **SPEC-602** MUST: The database schema is versioned. Migrations run automatically at startup.
- **SPEC-603** MUST: The current status, round-trip time, packet loss, `last_seen` and `last_change` are saved to the database, so a restarted server shows the last known state. After a restart, every monitored host has `stale = true` until its first ping round completes. Hosts with `monitored = false` are never stale; turning monitoring off clears the flag (v0.9), and turning it back on sets it until the first round completes (v0.10). `stale` is a separate flag and doesn't change the status value.
- **SPEC-604** MUST: The server takes an exclusive lock on its database at startup. A second server on the same database refuses to start at once, with the message "database is in use by another process". The lock is released when the server stops.
- **SPEC-605** MUST: At startup the server checks the tree. A host or group whose parent group doesn't exist is moved to the root, the repair is saved, and a warning is logged for each one. A parent cycle stops the startup with an error, because it needs a manual fix.

## 10. GUI (Fyne)
- **SPEC-701** MUST: `ined-gui` connects to a server URL (and token) given in its config file or by command-line flags.
- **SPEC-702** MUST: The GUI shows **one shared map per view** (§4b; the view tabs are specified at the start of M5, D14-8) with every visible item at its stored position (SPEC-112). Hosts and collapsed groups are icons labelled with their display name. A network group has its own icon shape and frame style and also shows its CIDR, so it can be told apart without relying on colour. Status is shown by **colour plus shape plus text** (for example: up = ● "up", down = ✕ "down", unknown = ? "unknown"), never by colour alone. An unmonitored host shows a dash and the word "off". Far zoomed out, where text would be too small to read, labels and the "?" are left out; the shapes stay distinct (v0.19).
- **SPEC-703** MUST: A **collapsed** group is one icon showing the group's status. An **expanded** group is a labelled frame around its visible members, with the group's name and status. Double-clicking a collapsed group icon expands it. Double-clicking an expanded group's frame label collapses it. Both actions are also in the context menu, and the change is saved on the server.
- **SPEC-704** MUST: The user can drag hosts, collapsed groups and expanded group frames. Dragging a group (its icon or its frame) moves it with everything in it (SPEC-507). New positions are saved on the server. Dropping an item onto a collapsed group icon or an expanded frame label moves it into that group, at a free position (SPEC-508); while dragging, that target is marked with a thicker border and the word "into". Dropping onto empty space only moves the item: its parent stays, and the frame around it grows. A context menu action "Move to group…" (with "Root") does the same as a drop. Only one item is dragged at a time; Esc cancels a drag (v0.21).
- **SPEC-705** MUST: The GUI updates live from SSE events. It reconnects automatically and shows the connection state.
- **SPEC-706** MUST: From the GUI, the user can create, rename and delete groups (setting or clearing the CIDR) and hosts. Details (v0.21): the context menu of empty space has "New group here…" and, in the network view, "New host here…" (IP address, name; it goes where the user clicked). A group's menu has "Edit…" (name, description, CIDR in the network view), "Move to group…" and "Delete…"; a host's menu has "Edit…" (IP address, name, description, monitored), "Move to group…" and "Delete…". A dialog shows the API's error and stays open, so the input can be fixed.
- **SPEC-707** MUST: A discovery dialog takes a CIDR range and a target group. It shows the job's progress and lets the user cancel it. "Scan this network" is available from the context menu of an IPv4 network group, and disabled for IPv6 network groups. Details (v0.21): the dialog opens from the toolbar ("Discover…") or from "Scan this network" (range filled in). The target group is chosen from the network view's groups, or the root. Progress comes from `discovery.progress` events (probed / total, found). Closing the dialog leaves the job running; the status bar then shows its progress until it ends.
- **SPEC-708** MUST: A details panel shows every field of the selected host or group: for a host, its IP address, PTR name, manual name, status, round-trip time, packet loss, `last_seen`, `last_change` and whether the status is stale. The panel includes an editable **note**. A stale host is marked on the map with a shape and text marker as well. The parent shown is the item's parent in the current view (v0.18).
- **SPEC-709** MUST: SPEC-001 to SPEC-005 apply to the GUI. A contrast preset selector is available in the GUI.
- **SPEC-710** ~~withdrawn~~ (v0.4: no network link lines)
- **SPEC-711** SHOULD: An item that has a note shows a small note marker. The marker is a shape, not a colour.
- **SPEC-712** SHOULD: A collapsed group's icon shows its counts of monitored hosts, for example `3 down · 1 ? · 40 up`, followed by `· 2 off` when unmonitored hosts are present.
- **SPEC-713** MUST: The map supports pan (drag on empty space, scroll bars) and zoom (keyboard and mouse wheel, 10 %–400 %, plus "fit all"). Zoom and pan are kept per GUI and per view tab, and not stored on the server. Positions are stored in map units at zoom 100 %. Keys: Ctrl+= or Ctrl++ zoom in, Ctrl+- zoom out, Ctrl+0 100 %, Ctrl+1 fit all, Ctrl+F find host (v0.19).
- **SPEC-714** SHOULD: "Find host" by display name, PTR name or IP address (substring match), over all hosts. Selecting a result shows the host in the current tab if it is placed in that view, otherwise in the network view's tab (v0.18). It expands the groups above that host and centres the map on it. Expanding groups this way changes the shared `collapsed` state, like any other expand.
- **SPEC-715** MUST (v0.18): **View tabs.** The map area has one tab per view, in the order of `GET /views` (`order`, then id), labelled with the view's name. Tabs follow `view.created|updated|deleted` live. The network view's tab is selected at start. If the view of the selected tab is deleted, the GUI switches to the network view's tab and says so in the status bar. Creating, renaming, reordering and deleting views from the GUI: SPEC-720.
- **SPEC-716** MUST (v0.18): **Keeping up with the server.** The GUI loads `GET /views` at start and a view's map (`GET /map?view=`) when its tab is first shown. It then follows the event stream (SPEC-705): `host.status`, `host.updated` and `group.status` change items in place in every loaded view; any other change to a view's items (`*.created`, `*.deleted`, `group.updated`, `placement.*`) reloads that view's map, at most once per 250 ms per view; `resync` reloads the views and every loaded map. Selecting a host or group loads its full record for the details panel (`GET /hosts/{id}`, `GET /groups/{id}`). A view is redrawn at most every 100 ms; changes in between are drawn together, so a burst of events costs one redraw (v0.20). A view list fetched while a view event arrives is fetched again.
- **SPEC-717** MUST (v0.18): **Window.** A toolbar (zoom out, the current zoom, zoom in, 100 %, fit all, find host, contrast preset selector; the zoom since v0.20), the view tabs with the map, a details panel beside the map, and a status bar. The status bar shows the server URL, the connection state in words (`connected`, `reconnecting in N s`, or `stopped: <reason>`), and the warning when the token is sent unencrypted (SPEC-802).
- **SPEC-718** MUST (v0.18): **What the GUI changes in M5.** Only expand and collapse (SPEC-703), the note (SPEC-708) and the contrast preset (SPEC-005). An API error on such a change is shown in the status bar and the map keeps the server's state. Dragging, moving, creating, deleting, discovery and view management follow in M6 (SPEC-704, 706, 707).
- **SPEC-719** MUST (draft v0.21): **Deleting.** Every delete asks first and says what goes. Deleting a non-empty group offers "Move its contents up" (reparent) or "Delete its contents too" (cascade); in the network view the cascade question names the number of hosts that will be deleted, in a custom view it says that hosts stay. The Delete key deletes the selected item the same way. There is no undo in v0.1.
- **SPEC-720** MUST (draft v0.21): **Managing views.** A "View" menu button in the toolbar has "New view…", "Rename view…", "Move view left", "Move view right" and "Delete view…" for the current tab. The network view can be renamed and moved but not deleted (its delete entry is disabled). Deleting a custom view asks first and says that its groups go and its hosts stay.
- **SPEC-721** MUST (draft v0.21): **Hosts in custom views.** A host's context menu has "Show in view ▸" with every custom view, marked where the host is placed already (SPEC-509); choosing an unmarked view places the host at that view's root (`PUT`), a marked one does nothing. In a custom view, a host's menu also has "Remove from this view" (no confirmation: the host stays). Dragging and "Move to group…" in a custom view change the placement. The details panel lists the views a host is in.

## 11. Configuration and security
- **SPEC-801** MUST: The server listens on `127.0.0.1:8470` by default. The address is configurable.
- **SPEC-802** MUST: Bearer-token authentication is optional, but required when the server listens on a non-loopback address: without a configured token, the server refuses to start. When a token is configured, every request without a valid token gets 401, except the reduced `/health` response (SPEC-501). Clients warn (on stderr, or in the GUI's status area) when they would send the token over plain `http://` to a host that isn't loopback (v0.15).
- **SPEC-803** SHOULD: TLS can be turned on by configuring a certificate file and a key file.
- **SPEC-804** MUST: The server shuts down cleanly on SIGINT or SIGTERM. It finishes database writes and closes SSE streams. Order (v0.8, v0.11): cancel discovery jobs and save the hosts found, stop reverse DNS, stop the monitor and flush its pending results, close the SSE streams, stop the HTTP server, close the database.
- **SPEC-805** MUST: Config files and defaults. Precedence: command-line flags, then environment variables (`INED_URL` and `INED_TOKEN`, clients only), then the config file, then built-in defaults. Default paths follow XDG (`$XDG_CONFIG_HOME`, falling back to `~/.config`, and `$XDG_DATA_HOME`, falling back to `~/.local/share`):
  - Server: `go-ined/server.toml`. Database: `go-ined/ined.db` under the data directory.
  - GUI: `go-ined/gui.toml`. CLI: `go-ined/ctl.toml`.
  - Every key and its default is documented in `examples/server.toml`, `examples/gui.toml` and `examples/ctl.toml`, which are the reference.

  | File | Key | Default |
  |------|-----|---------|
  | server | `server.listen` | `127.0.0.1:8470` |
  | server | `server.token` | `""` |
  | server | `server.tls_cert`, `server.tls_key` | `""` (TLS off) |
  | server | `storage.db_path` | `""` (= `$XDG_DATA_HOME/go-ined/ined.db`) |
  | server | `monitor.enabled` | `true` (v0.9) |
  | server | `monitor.interval` / `count` / `timeout` / `down_after` | `60s` / `3` / `1s` / `2` |
  | server | `discovery.max_prefix` / `rate` / `concurrency` | `16` / `200` / `64` |
  | server | `dns.enabled` | `true` (v0.11) |
  | server | `dns.refresh` / `concurrency` | `1h` / `20` |
  | server | `events.buffer` | `1000` |
  | gui, ctl | `server.url` | `http://127.0.0.1:8470` |
  | gui, ctl | `server.token` | `""` |
  | gui, ctl | `server.ca_cert` | `""` (system roots; set a PEM file to trust a self-signed server; a relative path is relative to the config file's folder) (v0.13, v0.15) |
  | gui | `contrast.preset` | `AA` (`AA`, `AAA`, `Max` or `custom`) |
  | gui | `contrast.min_ratio` | `4.5` (used only when `preset = "custom"`, range 4.5–21) |
- **SPEC-806** SHOULD: Every binary supports `-version`, which prints the version (set at build time) and exits.

## 12. CLI (`inedctl`)
- **SPEC-901** MUST: Subcommands:
  - `host list|add|show|edit|rm|move`
  - `group list|add|show|edit|rm|move|collapse|expand`
  - `note get|set|clear <host|group> <id>` (`set` reads stdin when no text is given)
  - `discover start|status|cancel`
  - `events` (streams events until interrupted)
  - `health`
  - `view list|add|show|rename|order|rm|place|unplace` (SPEC-124)

  The server URL and token come from SPEC-805.

  Shape (v0.13): `inedctl [--url U] [--token T] [--ca-cert F] [--json] [--config F] <object> <action> [flags] [args]`, parsed with the standard library `flag` package. Examples: `host add <ip> [--name N] [--parent ID] [--note T] [--unmonitored]`, `host move <id> <group-id|root>`, `group add [--cidr C] [--name N] [--parent ID]`, `group rm <id> [--reparent|--cascade]`. `discover start` prints the job; with `--wait` it follows the events, shows progress until the job ends, and exits 1 if the job fails or is cancelled (v0.14). `events` prints one line per event (time, type, id, short summary); with `--json`, one JSON object per line: `{"id", "type", "data"}` (v0.14). Global flags are also accepted after the command, and command flags after positional arguments (v0.14). `note set` without text reads stdin and removes trailing newlines (v0.14).
- **SPEC-902** MUST: Output is a human-readable table by default. `--json` prints the API's JSON unchanged. Tables are sorted by ID. Hosts: ID, IP, NAME, STATUS, RTT, PARENT, MON. Groups: ID, NAME, CIDR, STATUS, PARENT, COLLAPSED (v0.13), VIEW (v0.17); `group show` includes the view. A stale host shows `(stale)` after its status, so the status is always in words (v0.14).
- **SPEC-903** MUST: Exit codes: `0` OK, `1` the API returned an error (or a waited-for discovery job failed or was cancelled), `2` usage or config error (bad config file, URL or CA file), `3` connection, TLS or authentication error (including 401), `130` interrupted with Ctrl-C (`events` exits 0 instead). After an interrupted `discover start --wait`, inedctl says that the job keeps running on the server. `health` exits 0 whenever the server answers, also when its status is `degraded`; scripts read `--json` (v0.15).

## 13. LATER (not in v0.1)
- A 9P front end that exposes the inventory as a file tree.
- A ZMQ publisher for alerts.
- TCP-port probes for hosts that block ICMP, and ARP discovery.
- A **Hosts** notebook tab in the GUI (v0.11, user request): the inventory as a list with IP address and DNS name, sortable by column, with a find box to jump to an item.
- MAC address collection (v0.9, user request). MAC addresses are only visible (ARP/NDP) from inside the host's own layer-2 segment, so remote networks need a vantage point there: either ping and ARP/neighbour-table reads over SSH on a host in that network, or a small go-ined agent that runs there and reports to the server.
- Status history and graphs.
- Importing Tkined `.tki` map files.
- Free-floating text labels on the map, like Tkined's text objects.
- A drill-down view that opens a single group on its own.
- Host references, so one host can appear in several groups (for example its subnet and a functional group, or a router on several networks). **Covered by views** (milestone M4b, `Design-views.md`, approved in v0.14); SPEC items follow at the start of M4b.
- A topology view with link lines.
- Notes as a log with dates and authors.
- mTLS, multiple users, read-only roles.
