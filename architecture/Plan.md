# Plan — go-ined

Status: **draft v0.2** (2026-11-10)

## Decisions
| # | Decision | Date |
|---|----------|------|
| D1 | Groups form a tree: each item has at most one parent | 2026-11-10 |
| D2 | API: REST + SSE first. 9P and ZMQ maybe later | 2026-11-10 |
| D3 | Storage: SQLite through `modernc.org/sqlite` (pure Go) | 2026-11-10 |
| D4 | GUI: Fyne (desktop; needs cgo and OpenGL development libraries) | 2026-11-10 |
| D5 | Icon positions are stored on the server, per parent view | 2026-11-10 (default) |
| D6 | No import of Tkined `.tki` files in v0.1 | 2026-11-10 (default) |

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
- [ ] Domain model and tree rules (SPEC-101..107)
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
- [ ] `pkg/client` (SPEC-506), `inedctl` with subcommands for hosts, groups, discovery and watching events

### M5: GUI, read-only
- [ ] `internal/theme` contrast math and presets (SPEC-001..005)
- [ ] Canvas view, icons, navigation, live updates (SPEC-701..703, 705, 708, 709)

### M6: GUI editing
- [ ] Drag and drop, moving items into groups, managing groups and hosts, discovery dialog (SPEC-704, 706, 707)

### M7: CI + release 0.1.0
- [ ] GitHub Actions (pinned to commit SHAs, SPEC-006): `go vet`, `staticcheck`, `go test -race`, builds
- [ ] OpenAPI document (SPEC-505), TLS (SPEC-803)
- [ ] Update `Changes.md`, tag `v0.1.0`

## Open questions
- Go module path (for example `github.com/<owner>/go-ined`)?
- Confirm assumptions A1–A5 in Spec.md.
