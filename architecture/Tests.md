# Test cases — go-ined

Each test traces to one or more Spec IDs. Status: `planned` / `implemented` / `passing` / `failing` / `partial: …` (part of the test belongs to a later milestone).
Go test: `package: TestName`. Run everything with `go test -race ./...`.
Test levels: **U** = unit, **I** = integration (real SQLite and HTTP server, fake ICMP), **M** = manual.

| ID | Spec | Lvl | Description | Go test | Status |
|----|------|-----|-------------|---------|--------|
| TC-001 | SPEC-001 | U | The default theme is #FFFFFF background with #000000 text | — | planned |
| TC-002 | SPEC-002, 709 | U | Every colour pair in every palette and preset has contrast ≥ 4.5:1 (table-driven) | — | planned |
| TC-003 | SPEC-003 | U | Each preset meets its minimum: AA 4.5, AAA 7, Max = 21 | — | planned |
| TC-004 | SPEC-003 | U | A custom ratio below 4.5 or above 21 is rejected | — | planned |
| TC-005 | SPEC-004, 709 | M | Changing the preset in the GUI recolours every visible element | — | planned |
| TC-006 | SPEC-005 | U | The preset is loaded from the config file. An invalid value falls back to AA with a warning | — | planned |
| TC-007 | SPEC-006 | U | Every `uses:` in the workflows points to a 40-character hex SHA and has a version comment | — | planned |
| TC-008 | SPEC-003, 702 | U | The Max palette contains only #000000 and #FFFFFF. Every status is still distinct by shape and text | — | planned |
| TC-009 | SPEC-005 | U | A preset change made through the GUI's settings code is applied without a restart and written back to `gui.toml`; other keys are kept | — | planned |
| TC-101 | SPEC-101, 107 | I | Adding a host with a duplicate IP address fails with 409 `conflict` | inventory: TestDuplicateIP; api: TestCRUD | passing |
| TC-102 | SPEC-102 | U | An item has at most one parent. Moving it changes the parent | inventory: TestMoveHost | passing |
| TC-103 | SPEC-103 | U | Moving a group into itself or into one of its subgroups is rejected | inventory: TestNoCycles; api: TestTreeRulesAPI | passing |
| TC-104 | SPEC-104 | I | Deleting a non-empty group without `children=` → 409. `reparent` and `cascade` behave as specified | inventory: TestDeleteGroupModes; api: TestDeleteGroupAPI | passing |
| TC-105 | SPEC-105, 306 | U | Group status aggregation: table of mixes of down/unknown/up/unmonitored hosts, nested groups, empty group. Network groups follow the same rules | inventory: TestAggregate | passing |
| TC-106 | SPEC-106 | I | A new item gets a free position near its parent. PATCH x/y saves the position | inventory: TestFreePosition; api: TestPersistence | passing |
| TC-107 | SPEC-113 | I | Group `cidr`: a duplicate fails with 409. An invalid or non-canonical value (such as 10.0.0.5/24) fails with 400. Clearing it works | inventory: TestGroupCIDR; api: TestCRUD | passing |
| TC-108 | SPEC-107 | U | Placing a manually added host with no parent: goes to the most specific network group containing its IP (10.1.2.3 with /8 and /16 groups → /16), or to the root if none matches. IPv4 and IPv6 | inventory: TestPlacement | passing |
| TC-109 | SPEC-114 | U | Nested network groups: a /24 inside a /16 that contains it → OK; outside it → rejected on create, move and CIDR change; an ordinary group in between is skipped when checking; hosts can move anywhere | inventory: TestNesting; api: TestTreeRulesAPI | passing |
| TC-110 | SPEC-111 | I | Notes on a host and a group: set, read and clear through PATCH. A note over 64 KiB fails with 413 `too_large` | inventory: TestNotes; api: TestPersistence | passing |
| TC-111 | SPEC-112 | U | Visibility: an item under any collapsed group is hidden; nested expand/collapse combinations (table-driven). New groups start collapsed | inventory: TestVisibility | passing |
| TC-112 | SPEC-112, 502 | I | PATCH `collapsed` is saved, emits `group.updated`, and a second client sees the change | api: TestPersistence | partial: saving passes; event part in M2 |
| TC-201 | SPEC-201, 202 | U | CIDR expansion: /24 gives 254 addresses, /31 gives 2, /32 gives 1 | — | planned |
| TC-202 | SPEC-203 | U | A range larger than the maximum is rejected. The rate limiter respects the probes-per-second setting | — | planned |
| TC-203 | SPEC-204 | I | Discovery with a fake prober adds the responders. An existing IP is neither duplicated nor moved | — | planned |
| TC-204 | SPEC-205, 502 | I | A discovery job reports `discovery.progress` events, can be cancelled, and ends with `discovery.finished` with `state` = `done` / `cancelled` / `failed` | — | planned |
| TC-205 | SPEC-201, 204 | I | Discovery by network group id fills that group. Discovery by CIDR reuses an existing network group or creates one under the target group or the root | — | planned |
| TC-206 | SPEC-115 | U | `outside_cidr` is set for a host moved into a network group whose range doesn't contain its IP, and cleared when it moves back | inventory: TestOutsideCIDR | passing |
| TC-207 | SPEC-201 | I | Discovery with an IPv6 range or an IPv6 network group id → 400 `invalid_argument` | — | planned |
| TC-208 | SPEC-201, 114 | I | Discovery of 192.168.1.0/24 with target group 10.0.0.0/8 → 409 before the job starts; no group or host is created | — | planned |
| TC-301 | SPEC-302 | U | Status rules: 1 reply of 3 → up; down only after 2 failed rounds in a row; unknown before the first round | — | planned |
| TC-302 | SPEC-301 | U | Ping start times are spread across the interval | — | planned |
| TC-303 | SPEC-303 | U | Average round-trip time, loss, `last_seen` and `last_change` are computed correctly | — | planned |
| TC-304 | SPEC-304 | I | If neither ICMP mode is available, `/health` reports the error and hosts stay `unknown` | — | planned |
| TC-305 | SPEC-305 | I | `host.status` on a host status change. `group.status` for every group whose computed status changes, caused by a host status change, a move, an add, a delete, or a change to `monitored` | — | planned |
| TC-307 | SPEC-306 | U | A host with `monitored = false` is never handed to the prober; turning it back on schedules it | — | planned |
| TC-401 | SPEC-402 | U | Display name priority: manual name, then PTR name (trailing dot stripped), then IP address | inventory: TestDisplayName | passing |
| TC-402 | SPEC-403 | U | A temporary DNS error keeps the old name. NXDOMAIN clears it | — | planned |
| TC-403 | SPEC-401 | U | With a fake resolver and clock: a lookup when a host is added, and a refresh after `dns.refresh` | — | planned |
| TC-404 | SPEC-401 | U | No more than `dns.concurrency` lookups run at the same time | — | planned |
| TC-501 | SPEC-501, 504 | I | CRUD round trip for hosts and groups (with and without `cidr`). Every error case returns the HTTP status and `code` from the SPEC-504 table | api: TestCRUD, TestRequestErrors | passing |
| TC-502 | SPEC-502, 503 | I | SSE: events arrive in order. Reconnecting with Last-Event-ID replays missed events; an id that has dropped out of the buffer gets `resync` | — | planned |
| TC-503 | SPEC-506 | I | `pkg/client` works against a test server for all endpoints and the event stream | — | planned |
| TC-504 | SPEC-501 | I | `GET /map` returns every item with its type, parent, position, collapsed flag, `cidr`, status, `stale`, `has_note` and `outside_cidr` | api: TestMap | passing |
| TC-505 | SPEC-505 | I | `/api/v1/openapi.json` is valid OpenAPI 3 and lists every route the server registers | — | planned |
| TC-506 | SPEC-503 | I | Event ids have the form `<run>-<seq>`. After a restart, a `Last-Event-ID` from the previous run gets `resync` | — | planned |
| TC-601 | SPEC-602 | I | Migrations run on a fresh database and on a database at an older schema version | store: TestMigrateFresh, TestMigrateFromOlder, TestMigrateFailureRollsBack | passing |
| TC-602 | SPEC-603 | I | After a restart, the last known status is served with `stale = true`. The flag clears after the first ping round, and the status value doesn't change because of the flag | inventory: TestStaleOnLoad; api: TestPersistence | partial: stale after restart passes; clearing after a ping round in M2 |
| TC-603 | SPEC-601, 805 | I | The database is created at `storage.db_path`, or at the XDG default when the path is empty | store: TestMigrateFresh; config: TestXDGPaths; ined-server: TestCleanShutdown | passing |
| TC-701 | SPEC-702 | U | Every status has a distinct shape and text label (not only a colour) | — | planned |
| TC-702 | SPEC-703, 704, 706, 707 | M | GUI manual script: expand/collapse groups, drag items and frames, drop into a group, manage groups (including network groups) and hosts, scan an IPv4 network group; "Scan this network" is disabled for an IPv6 network group | — | planned |
| TC-703 | SPEC-705 | I | After the server restarts, the GUI client reconnects and resyncs | — | planned |
| TC-704 | SPEC-702 | U | A network group has an icon shape and frame style different from an ordinary group, and shows its CIDR label | — | planned |
| TC-705 | SPEC-708, 711 | M | Edit a note in the details panel; the note marker appears and disappears. A stale host shows its stale marker | — | planned |
| TC-706 | SPEC-712 | U | Collapsed group counts are correct for nested groups. Only monitored hosts are counted, plus `· n off` when unmonitored hosts are present | — | planned |
| TC-707 | SPEC-701, 805 | U | The GUI reads its URL and token from flags, then INED_URL / INED_TOKEN, then `gui.toml`, then defaults, in that order of precedence | config: TestClientEnvOverridesFile | partial: config precedence passes; GUI flags in M5 |
| TC-708 | SPEC-713 | U+M | U: converting between screen and map coordinates gives the same result back at 10 %, 100 % and 400 % zoom. M: pan, zoom and "fit all" with 5,000 generated hosts stay responsive | — | planned |
| TC-709 | SPEC-714 | U | Find host matches display name, PTR name and IP address substrings, and returns the groups that must be expanded | — | planned |
| TC-801 | SPEC-801, 802 | I | A non-loopback listener without a token refuses to start. A wrong or missing token gets 401 | config: TestListenSecurity; ined-server: TestRefuseNonLoopbackWithoutToken; api: TestAuthAndHealth | passing |
| TC-802 | SPEC-804 | I | SIGTERM closes SSE streams and the database cleanly | ined-server: TestCleanShutdown | partial: DB and listener pass; closing SSE streams in M2 |
| TC-803 | SPEC-803 | I | With `tls_cert` and `tls_key` set, the server serves HTTPS and the client connects with the test CA | ined-server: TestTLS | passing |
| TC-804 | SPEC-805 | U | Config precedence (flag, then environment, then file, then default) and XDG path fallbacks when the variables are unset | config: TestServerFileOverridesDefaults, TestMissingFiles, TestXDGPaths, TestClientEnvOverridesFile | passing |
| TC-805 | SPEC-805 | U | Each file in `examples/*.toml` parses, has no unknown keys, and every value equals the built-in default (keeps the examples and the code in sync) | config: TestExamplesMatchDefaults, TestUnknownKeysRejected | passing |
| TC-806 | SPEC-501, 802 | I | `/health` without a token returns only `{status, version}`; with a valid token, the full details | api: TestAuthAndHealth | passing |
| TC-807 | SPEC-806 | U | `-version` prints the program name and version and exits 0 | ined-server: TestVersionFlag | passing |
| TC-901 | SPEC-901 | I | Each `inedctl` subcommand works against a test server, including `note set` from stdin | — | planned |
| TC-902 | SPEC-902 | I | `--json` output equals the API response; the default output is a table | — | planned |
| TC-903 | SPEC-903 | I | Exit codes: 0 OK, 1 API error, 2 usage error, 3 server unreachable or wrong token | — | planned |
