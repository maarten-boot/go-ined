# Plan — go-ined

Status: **draft v0.16** (2026-11-10)

## Decisions
| # | Decision | Date |
|---|----------|------|
| D1 | Groups form a tree: each item has at most one parent | 2026-11-10 |
| D2 | API: REST + SSE first. 9P and ZMQ maybe later | 2026-11-10 |
| D3 | Storage: SQLite through `modernc.org/sqlite` (pure Go) | 2026-11-10 |
| D4 | GUI: Fyne (desktop; needs cgo and OpenGL development libraries) | 2026-11-10 |
| D5 | One shared map. Positions and the collapsed/expanded state are stored on the server | 2026-11-10 |
| D6 | No import of Tkined `.tki` files in v0.1 | 2026-11-10 (default) |
| D7 | ~~Network objects with link lines~~ replaced by D10 | 2026-11-10 |
| D8 | Notes: one plain-text field per host and group (network groups included) | 2026-11-10 |
| D9 | Groups collapse into one icon or expand into a frame on the same map. No drill-down view in v0.1 | 2026-11-10 |
| D10 | Networks are groups with an optional `cidr` (network groups). No link lines. Hosts can be moved out of their network group by hand. Nested network groups are checked to fit their parent's range | 2026-11-10 |
| D11 | Go module path: `github.com/maarten-boot/go-ined` | 2026-11-10 |
| D12 | ICMP: own small prober on `golang.org/x/net/icmp` (BSD-3), not `pro-bing` (Apache-2.0, would need a licence notice in binaries) | 2026-11-10 |
| D13 | Views (one tab per view, per-view host placements, network view fixed as view 1) as milestone M4b before the GUI; integer ids. See `Design-views.md` | 2026-11-10 |

## Package layout
```
cmd/ined-server/      daemon entry point
cmd/ined-gui/         Fyne client
cmd/inedctl/          CLI client
scripts/              helper scripts (gen-api-examples.sh)
internal/inventory/   domain model, tree rules, status aggregation (no I/O)
internal/store/       SQLite repository and migrations
internal/monitor/     ping scheduler and ICMP prober (behind an interface, so tests can fake it)
internal/discovery/   CIDR expansion, discovery jobs, rate limiting
internal/dnsname/     reverse-DNS resolver and refresh
internal/events/      event bus and replay buffer
internal/testserver/  real API server (fake prober) for client and CLI tests
internal/api/         REST handlers and SSE
internal/config/      TOML config and command-line flags
internal/theme/       contrast math, presets, palettes (shared by any UI)
internal/gui/         Fyne views and widgets
pkg/apitypes/         JSON types of the API, shared by server, client, GUI and CLI
pkg/client/           public Go API client
examples/             reference config files (server.toml, gui.toml, ctl.toml)
```

## Milestones
### M0: Project setup ✅
- [x] git repo, `.gitignore`, `CLAUDE.md`, architecture docs, `Changes.md`

### M1: Server core (inventory + storage + REST) ✅ (branch `feature/m1-server-core`)
- [x] `go mod init github.com/maarten-boot/go-ined`, skeleton, config loading with XDG paths, matching `examples/*.toml` (SPEC-801, 805, A4)
- [x] Domain model: hosts, groups, network groups, tree rules, notes, visibility, CIDR rules (SPEC-101..107, 111..115)
- [x] SQLite store and migrations, including the status columns (SPEC-601, 602)
- [x] REST CRUD, `/map`, `/health`, error table (SPEC-501, 504), auth (SPEC-802), clean shutdown (SPEC-804)
- [x] TLS (SPEC-803), pulled forward from M7 because it was only a few lines
- [x] `-version` flag on `ined-server` (SPEC-806)

### Crosscheck 02 fixes ✅ (branch `fix/crosscheck-02`)
- [x] Exclusive database lock (SPEC-604), id sequences (SPEC-116), escaped database path, load repair (SPEC-605)
- [x] README.md, MIT LICENSE, Makefile, `scripts/gen-api-examples.sh`

### M2: Monitoring + events ✅ (branch `feature/m2-monitoring-events`)
- [x] Event bus, SSE, replay buffer, run-id event ids (SPEC-502, 503)
- [x] ICMP prober (ICMP and ICMPv6), unprivileged first then privileged (SPEC-304)
- [x] Ping scheduler, status rules, group aggregation, status persistence and the `stale` flag (SPEC-301..303, 305..307, 105, 603)

### M2 follow-ups ✅ (branch `fix/m2-followups`)
- [x] 503 `unavailable` in SPEC-504; `monitor.enabled` (SPEC-301, 805) and deterministic `make examples`; `stale` only for monitored hosts (SPEC-603); status reset on IP change (SPEC-308)

### Crosscheck 03 fixes ✅ (branch `fix/crosscheck-03`)
- [x] SSE write deadline; shared raw socket per family; 200 ms gap; `last_seen` from the reply; ICMP mode per family; stale when monitoring is turned on; keepalive option; generator temp dir (SPEC-302..304, 502, 603, 804)

### M3: Discovery + DNS ✅ (branch `feature/m3-discovery-dns`)
- [x] CIDR expansion, rate limit, jobs (SPEC-201..205)
- [x] Reverse DNS with a concurrency limit (SPEC-401..403), `dns.enabled`
- [x] README: SVG architecture overview (`architecture/overview.svg`)

### Crosscheck 04 fixes ✅ (branch `fix/crosscheck-04`)
- [x] Probe errors in discovery jobs; one goroutine per free slot in monitor and DNS; WaitGroup at close; `discovery.rate` bounds; A1 warning (SPEC-203, 205)

### M4: Client package + CLI ✅ (branch `feature/m4-client-cli`)
- [x] `pkg/client` (SPEC-506), with Go `Example*` functions
- [x] `inedctl`: subcommands, table or `--json` output, exit codes, `-version` (SPEC-901..903, 806)
- [x] `server.ca_cert` / `--ca-cert` (SPEC-805); `internal/testserver` for client and CLI tests

### Crosscheck 05 fixes ✅ (branch `fix/crosscheck-05`)
- [x] Exit 130 on interrupt, relative `ca_cert`, empty flags override, raw JSON with `--wait`, RTT format, token-over-HTTP warning, `inedctl` examples (SPEC-506, 802, 805, 903)

### M4b: Views (design in `Design-views.md`, approved 2026-11-10)
- [x] Spec items (SPEC-117..124, draft) and test cases (TC-120..129) for views and placements (V1–V6 as recommended); D1 becomes "at most one parent per view"
- [ ] Decisions M4b-1..8 (below), then the code
- [ ] Migration 3 (`views`, `placements`, `groups.view_id`) with a tested upgrade from schema 2
- [ ] `inventory` per view: aggregation, visibility, free positions, move/cycle checks; network rules only in view 1
- [ ] API: `/views`, `GET /map?view=`, `PUT|DELETE /views/{id}/hosts/{host_id}`, `view.*` and `placement.*` events
- [ ] `pkg/client` and `inedctl view …` additions

### M5: GUI, read-only
- [ ] `internal/theme` contrast math and presets (SPEC-001..005)
- [ ] `ined-gui -version` (SPEC-806)
- [ ] Shared map, icons, network-group style, collapse/expand, details and notes panel, live updates (SPEC-701..703, 705, 708, 709, 711, 712)
- [ ] Pan/zoom and find host (SPEC-713, 714)

### M6: GUI editing
- [ ] Drag and drop, moving items into groups, managing groups and hosts, discovery dialog (SPEC-704, 706, 707)

### M7: CI + release 0.1.0
- [ ] GitHub Actions (pinned to commit SHAs, SPEC-006): `go vet`, `staticcheck`, `go test -race`, builds
- [ ] OpenAPI document (SPEC-505)
- [ ] Update `Changes.md`, tag `v0.1.0`

## Open questions
- **M4b decisions** (proposed 2026-11-10, defaults first; waiting for the user):
  1. Storage: hosts keep `parent_id/x/y` as their network placement; a new `placements` table holds custom-view placements only. No rebuild of `hosts` (this differs from `Design-views.md` §4, lower risk; the API is the same).
  2. `DELETE /views/{id}` needs no extra parameter and emits one `view.deleted` (clients drop the tab and its groups); no per-group events.
  3. Cascade or reparent in a custom view emits `group.deleted` / `group.updated` and `placement.deleted` / `placement.updated` per item, as today.
  4. Deleting a host emits only `host.deleted`; clients remove it from every view.
  5. View names unique (409 on duplicates); a new view goes last; `PATCH order` sets only that view's value.
  6. `GET /groups` still lists all groups (now with `view_id`); `?view=` filters. In custom views, map items have `outside_cidr: false`.
  7. `inedctl view list|add|show|rename|rm|place|unplace`; `--view` on `group add|list`.
  8. The GUI part (a notebook tab per view) is specified in M5, not now.
- Not yet decided: configurable `log/slog` level and format, OpenAPI-first or generated from the handlers (before M7), `inedctl backup`.
- `staticcheck` / `govulncheck`: run in CI (M7), or locally once allowed.
