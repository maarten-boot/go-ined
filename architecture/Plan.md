# Plan — go-ined

Status: **draft v0.4** (2026-11-10)

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
| D8 | Notes: one plain-text field per host, group and network | 2026-11-10 |
| D9 | Groups collapse into one icon or expand into a frame on the same map. No drill-down view in v0.1 | 2026-11-10 |
| D10 | Networks are groups with an optional `cidr` (network groups). No link lines. Hosts can be moved out of their network group by hand. Nested network groups are checked to fit their parent's range | 2026-11-10 |
| D11 | Go module path: `github.com/maarten-boot/go-ined` | 2026-11-10 |

## Package layout
```
cmd/ined-server/      daemon entry point
cmd/ined-gui/         Fyne client
cmd/inedctl/          CLI client
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
pkg/client/           public Go API client
```

## Milestones
### M0: Project setup ✅
- [x] git repo, `.gitignore`, `CLAUDE.md`, architecture docs, `Changes.md`

### M1: Server core (inventory + storage + REST)
- [ ] `go mod init`, skeleton, config loading (SPEC-801, A4)
- [ ] Domain model: hosts, groups, network groups, tree rules, notes, visibility, CIDR rules (SPEC-101..107, 111..115)
- [ ] SQLite store and migrations (SPEC-601..603)
- [ ] REST CRUD and error format (SPEC-501, 504), auth (SPEC-802), clean shutdown (SPEC-804)

### M2: Monitoring + events
- [ ] Event bus, SSE, replay buffer (SPEC-502, 503)
- [ ] ICMP prober, unprivileged first then privileged (SPEC-304)
- [ ] Ping scheduler, status rules, group aggregation (SPEC-301..303, 305, 306, 105)

### M3: Discovery + DNS
- [ ] CIDR expansion, rate limit, jobs (SPEC-201..205)
- [ ] Reverse DNS (SPEC-401..403)

### M4: Client package + CLI
- [ ] `pkg/client` (SPEC-506), `inedctl` with subcommands for hosts, groups (network groups included), notes, discovery and watching events

### M5: GUI, read-only
- [ ] `internal/theme` contrast math and presets (SPEC-001..005)
- [ ] Shared map, icons, network-group style, collapse/expand, details and notes panel, live updates (SPEC-701..703, 705, 708, 709, 711, 712)

### M6: GUI editing
- [ ] Drag and drop, moving items into groups, managing groups and hosts, discovery dialog (SPEC-704, 706, 707)

### M7: CI + release 0.1.0
- [ ] GitHub Actions (pinned to commit SHAs, SPEC-006): `go vet`, `staticcheck`, `go test -race`, builds
- [ ] OpenAPI document (SPEC-505), TLS (SPEC-803)
- [ ] Update `Changes.md`, tag `v0.1.0`

## Open questions
- Fixes proposed in `Crosscheck-2026-11-10-01.md`, waiting for the user's OK.
- Should `docs/architecture` be merged into `main` before M1?
