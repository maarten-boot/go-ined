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
| TC-109 | SPEC-114 | U | Nested network groups: a /24 inside a /16 that contains it → OK; outside it → rejected on create, move and CIDR change; an ordinary group in between is skipped when checking; hosts can move anywhere | inventory: TestNesting, TestNestingMessage; api: TestTreeRulesAPI | passing |
| TC-110 | SPEC-111 | I | Notes on a host and a group: set, read and clear through PATCH. A note over 64 KiB fails with 413 `too_large` | inventory: TestNotes; api: TestPersistence | passing |
| TC-111 | SPEC-112 | U | Visibility: an item under any collapsed group is hidden; nested expand/collapse combinations (table-driven). New groups start collapsed | inventory: TestVisibility | passing |
| TC-112 | SPEC-112, 502 | I | PATCH `collapsed` is saved, emits `group.updated`, and a second client sees the change | inventory: TestCollapsedEvent; api: TestPersistence, TestEventsStream | passing |
| TC-113 | SPEC-116 | U | Ids are never reused: delete the highest host and group id, restart, create new ones | inventory: TestIDsNeverReused; store: TestSequences, TestMigration2Seeds | passing |
| TC-201 | SPEC-201, 202 | U | CIDR expansion: /24 gives 254 addresses, /31 gives 2, /32 gives 1 | discovery: TestAddresses | passing |
| TC-202 | SPEC-203 | U | A range larger than the maximum is rejected. The rate limiter respects the probes-per-second setting | discovery: TestMaxPrefixAndRate | passing |
| TC-203 | SPEC-204 | I | Discovery with a fake prober adds the responders. An existing IP is neither duplicated nor moved | discovery: TestAddsResponders; inventory: TestAddDiscovered | passing |
| TC-204 | SPEC-205, 502 | I | A discovery job reports `discovery.progress` events, can be cancelled, and ends with `discovery.finished` with `state` = `done` / `cancelled` / `failed` | discovery: TestProgressCancelFail, TestCloseSavesFound; api: TestDiscoveryAPI | passing |
| TC-205 | SPEC-201, 204 | I | Discovery by network group id fills that group. Discovery by CIDR reuses an existing network group or creates one under the target group or the root | discovery: TestTargets | passing |
| TC-206 | SPEC-115 | U | `outside_cidr` is set for a host moved into a network group whose range doesn't contain its IP, and cleared when it moves back | inventory: TestOutsideCIDR | passing |
| TC-207 | SPEC-201 | I | Discovery with an IPv6 range or an IPv6 network group id → 400 `invalid_argument` | discovery: TestIPv6Rejected | passing |
| TC-208 | SPEC-201, 114 | I | Discovery of 192.168.1.0/24 with target group 10.0.0.0/8 → 409 before the job starts; no group or host is created | discovery: TestNestingConflict | passing |
| TC-209 | SPEC-204 | I | Discovery of a /16 puts a found host into an existing /24 subgroup that contains it | discovery: TestTargets; inventory: TestAddDiscovered | passing |
| TC-210 | SPEC-204 | U | A found host is monitored, `up`, with round-trip time, loss and `last_seen` from the probe; a host that answered only the second request has 50 % loss | discovery: TestAddsResponders; inventory: TestAddDiscovered | passing |
| TC-211 | SPEC-205 | I | A job whose range overlaps a running job → 409; finished jobs are kept up to the limit; job ids have the form `<run>-<n>` | discovery: TestOverlapAndKeep | passing |
| TC-212 | SPEC-201 | I | Without IPv4 ICMP, `POST /discovery` → 503 `unavailable`; with `monitor.enabled = false` discovery still works | discovery: TestUnavailable; api: TestDiscoveryAPI; ined-server: TestMonitorDisabled | passing |
| TC-213 | SPEC-205 | U | Probe errors are counted in `errors`, and the first message is in `error`, while the job still ends `done` | discovery: TestProbeErrors | passing |
| TC-214 | SPEC-203 | U | A range with more than 5,000 addresses logs a warning; `discovery.rate` outside 1–100,000 is rejected by the config check | discovery: TestLargeRangeWarning; config: TestDiscoveryRateBounds | passing |
| TC-301 | SPEC-302 | U | Status rules: 1 reply of 3 → up; down only after 2 failed rounds in a row; unknown before the first round | monitor: TestNextStatus, TestObservations; monitor: TestProberError | passing |
| TC-302 | SPEC-301 | U | Ping start times are spread across the interval | monitor: TestSpread | passing |
| TC-303 | SPEC-303 | U | Average round-trip time, loss, `last_seen` and `last_change` are computed correctly | monitor: TestSummarize; inventory: TestApplyObservations; ined-server: TestMonitoringPersistsAndStale | passing |
| TC-304 | SPEC-304 | I | If neither ICMP mode is available, `/health` reports the error and hosts stay `unknown` | api: TestHealthICMP; ined-server: TestNoICMP | passing |
| TC-305 | SPEC-305 | I | `host.status` on a host status change. `group.status` for every group whose computed status changes, caused by a host status change, a move, an add, a delete, or a change to `monitored` | inventory: TestStatusEvents | passing |
| TC-307 | SPEC-306 | U | A host with `monitored = false` is never handed to the prober; turning it back on schedules it | monitor: TestUnmonitoredNotPinged; inventory: TestMonitorTargets | passing |
| TC-308 | SPEC-307 | U | `host.status` only on a status change or when `stale` clears; a round-trip-time change alone emits nothing but is saved; results are saved in batches | inventory: TestApplyObservations; monitor: TestObservations | passing |
| TC-309 | SPEC-304 | I | The real prober finds a working mode (unprivileged or raw) and gets a reply from 127.0.0.1 and ::1 (skipped when no ICMP mode is available) | monitor: TestICMPLoopback, TestICMPCancel | passing (unprivileged mode; raw mode not exercised on this machine) |
| TC-310 | SPEC-308 | U | Changing a host's IP resets status to `unknown`, clears round-trip time and loss, sets `last_change`, emits `host.updated` then `host.status`; the monitor restarts the streak | inventory: TestIPChangeResets; monitor: TestIPChangeResetsStreak | passing |
| TC-311 | SPEC-301, 805 | I | With `monitor.enabled = false` nothing is pinged and `/health` reports `icmp_mode: disabled` with status `ok` | ined-server: TestMonitorDisabled | passing |
| TC-312 | SPEC-302 | I | The requests of a round are at least 200 ms apart | monitor: TestRoundGap, TestICMPLoopback | passing |
| TC-313 | SPEC-304 | U | IPv6 unavailable while IPv4 works: IPv6 hosts are not pinged, keep their status, and `/health` reports `icmp6_error` with status `ok` | monitor: TestFamilyUnavailable, TestSeenAtAndSkippedFamily; api: TestHealthICMP | passing |
| TC-314 | SPEC-304 | U | Raw mode: concurrent rounds on one shared socket each get exactly their own replies (fake socket) | monitor: TestRawMuxConcurrentRounds, TestRawMuxSocketFailure | passing (fake raw socket; a real raw socket is still not exercised) |
| TC-315 | SPEC-303 | U | `last_seen` is the arrival time of the last reply, not the end of the round | monitor: TestSeenAtAndSkippedFamily; inventory: TestSeenAt | passing |
| TC-401 | SPEC-402 | U | Display name priority: manual name, then PTR name (trailing dot stripped), then IP address | inventory: TestDisplayName | passing |
| TC-402 | SPEC-403 | U | A temporary DNS error keeps the old name. NXDOMAIN clears it | dnsname: TestName, TestKeepAndClear | passing |
| TC-403 | SPEC-401 | U | With a fake resolver and clock: a lookup when a host is added, and a refresh after `dns.refresh` | dnsname: TestScheduling | passing |
| TC-404 | SPEC-401 | U | No more than `dns.concurrency` lookups run at the same time | dnsname: TestConcurrency | passing |
| TC-405 | SPEC-401 | U | An IP change clears the PTR name at once and triggers a new lookup; a changed name emits `host.updated` | dnsname: TestIPChange; inventory: TestPTRName | passing |
| TC-406 | SPEC-401, 403 | U | At startup the refreshes are spread across `dns.refresh`; of several PTR names the first is used | dnsname: TestScheduling, TestName | passing |
| TC-407 | SPEC-401, 805 | I | `dns.enabled = false` makes no lookups; by default a new host is looked up | ined-server: TestDNSEnabled | passing |
| TC-408 | SPEC-401, 301 | U | Hosts waiting for a lookup or ping slot don't each hold a goroutine: with concurrency 4 and 50 due hosts, only about 4 workers exist | dnsname: TestNoGoroutinePerWaitingHost; monitor: TestNoGoroutinePerWaitingRound | passing |
| TC-501 | SPEC-501, 504 | I | CRUD round trip for hosts and groups (with and without `cidr`). Every error case returns the HTTP status and `code` from the SPEC-504 table | api: TestCRUD, TestRequestErrors; apitypes: TestOpt; inventory: TestRepoFailureKeepsModel | passing |
| TC-502 | SPEC-502, 503 | I | SSE: events arrive in order. Reconnecting with Last-Event-ID replays missed events; an id that has dropped out of the buffer gets `resync` | events: TestReplay, TestResync; api: TestEventsStream, TestEventsReplayAndResync; api: TestEventsKeepalive | passing |
| TC-503 | SPEC-506 | I | `pkg/client` works against a test server for all endpoints and the event stream | — | planned |
| TC-504 | SPEC-501 | I | `GET /map` returns every item with its type, parent, position, collapsed flag, `cidr`, status, `stale`, `has_note` and `outside_cidr` | api: TestMap | passing |
| TC-505 | SPEC-505 | I | `/api/v1/openapi.json` is valid OpenAPI 3 and lists every route the server registers | — | planned |
| TC-506 | SPEC-503 | I | Event ids have the form `<run>-<seq>`. After a restart, a `Last-Event-ID` from the previous run gets `resync` | events: TestEventIDs, TestResync; api: TestEventsReplayAndResync | passing |
| TC-507 | SPEC-502 | I | Event payloads: full object on created/updated/host.status, `{id}` on deleted (one per item on cascade), `{id,status}` on group.status. A client that falls behind gets `resync` and the stream ends | events: TestLaggedSubscriber; inventory: TestDeleteEvents; api: TestEventsStream, TestEventsLagged | passing |
| TC-508 | SPEC-504 | I | `GET /events` during shutdown gets 503 `unavailable` | api: TestEventsClosedAtShutdown | passing |
| TC-509 | SPEC-502, 804 | I | A client that stops reading is disconnected after the write timeout; shutdown then finishes at once | api: TestEventsStalledClient | passing |
| TC-601 | SPEC-602 | I | Migrations run on a fresh database and on a database at an older schema version | store: TestMigrateFresh, TestMigrateFromOlder, TestMigrateFailureRollsBack, TestMigration2Seeds | passing |
| TC-602 | SPEC-603 | I | After a restart, the last known status is served with `stale = true`. The flag clears after the first ping round, and the status value doesn't change because of the flag | inventory: TestStaleOnLoad, TestStaleClears; api: TestPersistence; ined-server: TestMonitoringPersistsAndStale; store: TestRoundTrip | passing |
| TC-603 | SPEC-603 | U | After a restart, unmonitored hosts are not stale; turning monitoring off clears `stale`, turning it on sets it | inventory: TestStaleOnlyMonitored | passing |
| TC-603 | SPEC-601, 805 | I | The database is created at `storage.db_path` (also with `?`, `#`, `%`, spaces or a relative path), or at the XDG default when the path is empty | store: TestMigrateFresh, TestSpecialCharacterPaths; config: TestXDGPaths; ined-server: TestDefaultDBPath | passing |
| TC-604 | SPEC-604 | I | A second store or server on the same database is refused at once; the first keeps working; the lock is released on close | store: TestExclusiveLock; ined-server: TestSecondServerRefused | passing |
| TC-605 | SPEC-605 | U | Orphans move to the root (saved, one warning each, not repeated); a cycle refuses to load | inventory: TestRepairOnLoad | passing |
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
| TC-802 | SPEC-804 | I | SIGTERM closes SSE streams and the database cleanly | ined-server: TestCleanShutdown; events: TestClose; api: TestEventsClosedAtShutdown; monitor: TestStopSavesPending | passing |
| TC-803 | SPEC-803 | I | With `tls_cert` and `tls_key` set, the server serves HTTPS and the client connects with the test CA | ined-server: TestTLS | passing |
| TC-804 | SPEC-805 | U | Config precedence (flag, then environment, then file, then default) and XDG path fallbacks when the variables are unset | config: TestServerFileOverridesDefaults, TestMissingFiles, TestXDGPaths, TestClientEnvOverridesFile; config: TestValidate | passing |
| TC-805 | SPEC-805 | U | Each file in `examples/*.toml` parses, has no unknown keys, and every value equals the built-in default (keeps the examples and the code in sync) | config: TestExamplesMatchDefaults, TestUnknownKeysRejected | passing |
| TC-806 | SPEC-501, 802 | I | `/health` without a token returns only `{status, version}`; with a valid token, the full details | api: TestAuthAndHealth; api: TestHealthDegraded | passing |
| TC-807 | SPEC-806 | U | `-version` prints the program name and version to stdout and exits 0 | ined-server: TestVersionFlag | passing (server; GUI and CLI later) |
| TC-901 | SPEC-901 | I | Each `inedctl` subcommand works against a test server, including `note set` from stdin | — | planned |
| TC-902 | SPEC-902 | I | `--json` output equals the API response; the default output is a table | — | planned |
| TC-903 | SPEC-903 | I | Exit codes: 0 OK, 1 API error, 2 usage error, 3 server unreachable or wrong token | — | planned |
| TC-904 | SPEC-805, 803 | I | `server.ca_cert` / `--ca-cert` lets the CLI talk to a server with a self-signed certificate; without it the connection fails with exit code 3 | — | planned |
| TC-905 | SPEC-506, 503 | I | The client's event stream reconnects after the server restarts or the connection drops, resumes with `Last-Event-ID`, and passes `resync` on | — | planned |
| TC-906 | SPEC-901 | I | `discover start --wait` shows progress and exits 0 on `done`, 1 on `failed` | — | planned |
