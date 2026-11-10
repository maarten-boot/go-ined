# Plan — go-ined

Status: **draft v0.10** (2026-11-10)

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

### M3: Discovery + DNS
- [ ] CIDR expansion, rate limit, jobs (SPEC-201..205)
- [ ] Reverse DNS with a concurrency limit (SPEC-401..403)

### M4: Client package + CLI
- [ ] `pkg/client` (SPEC-506), with Go `Example*` functions
- [ ] `inedctl`: subcommands, table or `--json` output, exit codes, `-version` (SPEC-901..903, 806)

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
- Not yet decided: configurable `log/slog` level and format, OpenAPI-first or generated from the handlers (before M7), `inedctl backup`.
- `staticcheck` / `govulncheck`: run in CI (M7), or locally once allowed.
