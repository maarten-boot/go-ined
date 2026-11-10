# Test cases — go-ined

Each test traces to one or more Spec IDs. Status: `planned` / `implemented` / `passing` / `failing`.
Test levels: **U** = unit, **I** = integration (real SQLite and HTTP server, fake ICMP), **M** = manual.

| ID | Spec | Lvl | Description | Status |
|----|------|-----|-------------|--------|
| TC-001 | SPEC-001 | U | The default theme is #FFFFFF background with #000000 text | planned |
| TC-002 | SPEC-002, 709 | U | Every colour pair in every palette and preset has contrast ≥ 4.5:1 (table-driven) | planned |
| TC-003 | SPEC-003 | U | Each preset meets its minimum: AA 4.5, AAA 7, Max = 21 | planned |
| TC-004 | SPEC-003 | U | A custom ratio below 4.5 or above 21 is rejected | planned |
| TC-005 | SPEC-004, 709 | M | Changing the preset in the GUI recolours every visible element | planned |
| TC-006 | SPEC-005 | U | The preset is loaded from the config file. An invalid value falls back to AA with a warning | planned |
| TC-007 | SPEC-006 | U | Every `uses:` in the workflows points to a 40-character hex SHA and has a version comment | planned |
| TC-101 | SPEC-101, 107 | I | Adding a host with a duplicate IP address fails with 409 | planned |
| TC-102 | SPEC-102 | U | An item has at most one parent. Moving it changes the parent | planned |
| TC-103 | SPEC-103 | U | Moving a group into itself or into one of its subgroups is rejected | planned |
| TC-104 | SPEC-104 | I | Deleting a non-empty group without `children=` → 409. `reparent` and `cascade` behave as specified | planned |
| TC-105 | SPEC-105, 306 | U | Group status aggregation: table of mixes of down/unknown/up/unmonitored hosts, nested groups, empty group. Networks don't affect group status | planned |
| TC-106 | SPEC-106 | I | A new item gets a free position near its parent or network. PATCH x/y saves the position | planned |
| TC-107 | SPEC-108 | I | Network CRUD. A duplicate CIDR fails with 409. An invalid CIDR fails with 400 | planned |
| TC-108 | SPEC-109 | U | Network links: longest-prefix match (a host in 10.0.0.0/8 and 10.1.0.0/16 links to the /16). Links update when a network is added or removed. IPv4 and IPv6 | planned |
| TC-109 | SPEC-110 | U | A network's status is the worst of its linked monitored hosts. A network with no linked hosts is `unknown` | planned |
| TC-110 | SPEC-111 | I | Notes on a host, group and network: set, read and clear through PATCH. A note over 64 KiB fails with 413 | planned |
| TC-111 | SPEC-112 | U | Visibility: an item under any collapsed group is hidden; nested expand/collapse combinations (table-driven). New groups start collapsed | planned |
| TC-112 | SPEC-112, 502 | I | PATCH `collapsed` is saved, emits `group.updated`, and a second client sees the change | planned |
| TC-201 | SPEC-201, 202 | U | CIDR expansion: /24 gives 254 addresses, /31 gives 2, /32 gives 1 | planned |
| TC-202 | SPEC-203 | U | A range larger than the maximum is rejected. The rate limiter respects the probes-per-second setting | planned |
| TC-203 | SPEC-204 | I | Discovery with a fake prober adds the responders. An existing IP is neither duplicated nor moved | planned |
| TC-205 | SPEC-201 | I | Discovery by network id uses the network's CIDR and parent group. Discovery by CIDR creates a network if none exists | planned |
| TC-204 | SPEC-205 | I | A discovery job reports progress events and can be cancelled (state ends as `cancelled`) | planned |
| TC-301 | SPEC-302 | U | Status rules: 1 reply of 3 → up; down only after 2 failed rounds in a row; unknown before the first round | planned |
| TC-302 | SPEC-301 | U | Ping start times are spread across the interval | planned |
| TC-303 | SPEC-303 | U | Average round-trip time, loss, `last_seen` and `last_change` are computed correctly | planned |
| TC-304 | SPEC-304 | I | If neither ICMP mode is available, `/health` reports the error and hosts stay `unknown` | planned |
| TC-305 | SPEC-305 | I | A host status change emits `host.status`, plus `group.status` for each ancestor group whose status changed | planned |
| TC-401 | SPEC-402 | U | Display name priority: manual name, then PTR name (trailing dot stripped), then IP address | planned |
| TC-402 | SPEC-403 | U | A temporary DNS error keeps the old name. NXDOMAIN clears it | planned |
| TC-501 | SPEC-501, 504 | I | CRUD round trip for hosts, groups and networks. Errors have the specified JSON shape | planned |
| TC-504 | SPEC-501 | I | `GET /map` returns every item with its type, parent, position, collapsed flag, status, `has_note` and network link | planned |
| TC-502 | SPEC-502, 503 | I | SSE: events arrive in order. Reconnecting with Last-Event-ID replays missed events; an ID that is too old gets `resync` | planned |
| TC-503 | SPEC-506 | I | `pkg/client` works against a test server for all endpoints and the event stream | planned |
| TC-601 | SPEC-602 | I | Migrations run on a fresh database and on a database at an older schema version | planned |
| TC-602 | SPEC-603 | I | After a restart, the last known status is served and marked stale | planned |
| TC-801 | SPEC-801, 802 | I | A non-loopback listener without a token refuses to start. A wrong or missing token gets 401 | planned |
| TC-802 | SPEC-804 | I | SIGTERM closes SSE streams and the database cleanly | planned |
| TC-701 | SPEC-702 | U | Every status has a distinct shape and text label (not only a colour) | planned |
| TC-702 | SPEC-703, 704, 706, 707 | M | GUI manual script: expand/collapse groups, drag items and frames, drop into a group, manage groups/networks/hosts, scan a network | planned |
| TC-704 | SPEC-710 | U | Link lines: host → bar; a host inside a collapsed group → one line from the group icon per network; no line when the bar is hidden | planned |
| TC-705 | SPEC-708, 711 | M | Edit a note in the details panel; the note marker appears and disappears | planned |
| TC-706 | SPEC-712 | U | Collapsed group counts are correct for nested groups | planned |
| TC-703 | SPEC-705 | I | After the server restarts, the GUI client reconnects and resyncs | planned |
