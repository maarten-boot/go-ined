# Plan — go-ined

Status: **draft v0.19** (2026-11-11)

## Decisions
| # | Decision | Date |
|---|----------|------|
| D1 | Groups form a tree: each item has at most one parent (per view since D13) | 2026-11-10 |
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
| D14 | M4b details (user OK 2026-11-10): hosts keep their network placement in `hosts`, a `placements` table holds custom-view placements only (no `hosts` rebuild); one `view.deleted` per view; per-item events for cascade/reparent in a custom view; only `host.deleted` when a host goes; unique view names, new views last; `GET /groups?view=`; `inedctl view …`; the GUI tabs are specified in M5 | 2026-11-10 |
| D15 | M5 details (user OK 2026-11-11, all defaults): Fyne v2.8.2 in the same module with a cgo-free check for server and CLI; canvas-object map with culling; status and names in place, other changes reload the view; M5 changes only expand/collapse, notes and contrast; tabs not saved; find over all hosts; contrast saved line by line in `gui.toml`; derived palettes; shapes as in M5-9; no host-views API in M5. See the M5 decisions table | 2026-11-11 |

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
internal/gui/         Fyne window, map widget, tabs, panels
internal/gui/mapmodel/ the GUI's picture of the server: views, maps, events, layout, find (pure Go)
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

### M4b: Views ✅ (branch `feature/m4b-views`; design in `Design-views.md`)
- [x] Spec items (SPEC-117..124) and test cases (TC-120..129) for views and placements (V1–V6 as recommended); D1 becomes "at most one parent per view"
- [x] Decisions M4b-1..8 (D14)
- [x] Migration 3 (`views`, `placements`, `groups.view_id`) with a tested upgrade from schema 2
- [x] `inventory` per view: aggregation, visibility, free positions, move/cycle checks; network rules only in view 1
- [x] API: `/views`, `GET /map?view=`, `PUT|DELETE /views/{id}/hosts/{host_id}`, `view.*` and `placement.*` events
- [x] `pkg/client` and `inedctl view …` additions

### M5: GUI, read-only ✅ (branch `feature/m5-gui`)
- [x] View tabs and the rest of the GUI's behaviour: Spec items SPEC-715..718 (v0.18); SPEC-708/713/714 amended; test cases TC-710..713
- [x] Decisions M5-1..10 (D15)
- [x] `internal/theme` contrast math, presets and palettes (SPEC-001..005)
- [x] `cmd/ined-gui` with flags, config, `-version` (SPEC-701, 806); contrast saved to `gui.toml` (SPEC-005)
- [x] `internal/gui/mapmodel` (pure Go): state from `/views` and `/map`, event handling, visibility, frames, counts, coordinates, find
- [x] Map per view, icons, network-group style, collapse/expand, details and notes panel, live updates (SPEC-701..703, 705, 708, 709, 711, 712)
- [x] Pan, zoom, scroll bars, fit all and find host (SPEC-713, 714)
- [x] View tabs, toolbar, status bar (SPEC-715..718)
- [x] `make build-nogui` and `nogui-check` (M5-1)
- [x] Benchmark: layout and drawing of 5,000 hosts (about 10 ms). The server's `AggregateAll` benchmark is not done yet; it moves to M7 with the other performance checks
- [ ] The user's visual check of the GUI (TC-005, 702 partly, 705, 708 manual part)

### M6: GUI editing
- [ ] Drag and drop, moving items into groups, managing groups and hosts, discovery dialog (SPEC-704, 706, 707)

### M7: CI + release 0.1.0
- [ ] GitHub Actions (pinned to commit SHAs, SPEC-006): `go vet`, `staticcheck`, `go test -race`, builds
- [ ] OpenAPI document (SPEC-505)
- [ ] Update `Changes.md`, tag `v0.1.0`

### M5 decisions (proposed; defaults recommended)
| # | Question | Recommended default | Alternative |
|---|----------|---------------------|-------------|
| M5-1 | Toolkit, version and build | **Fyne v2.8.2** (2026-10-07) in the same Go module. Only `ined-gui` needs cgo and the X11/OpenGL dev packages (present here). `ined-server` and `inedctl` don't import Fyne and stay cgo-free; `make build-nogui` and a check (`CGO_ENABLED=0 go build ./cmd/ined-server ./cmd/inedctl`) keep it that way | A separate Go module for the GUI (keeps Fyne out of the server's `go.mod`, but needs a `replace` and two module versions) |
| M5-2 | Map drawing | A custom Fyne widget made of canvas objects (rectangles, lines, circles, text), created only for items in or near the visible area. TC-708 checks 5,000 hosts | Draw the whole map into one image (`canvas.Raster`) with our own text rendering: fast, but more code and blurrier text |
| M5-3 | Keeping up with the server | SPEC-716: status and name changes in place; other changes reload that view's map, at most every 250 ms; `resync` reloads all | Apply every event to the client model in place (less traffic, but much more code and more ways to drift from the server) |
| M5-4 | What M5 may change on the server | SPEC-718: expand/collapse, note, contrast only | Also drag and drop in M5 |
| M5-5 | View tabs | SPEC-715. Zoom, pan and the selected tab live only while the GUI runs (not saved) | Save the selected tab and zoom per view in `gui.toml` |
| M5-6 | Find host | Over all hosts; shown in the current tab if the host is there, else in the network view (SPEC-714) | Search only the current view |
| M5-7 | Saving the contrast preset (SPEC-005) | Rewrite only the `preset` and `min_ratio` lines of `[contrast]` in `gui.toml`, so comments and other keys stay. A missing file or section is created. The write goes to a temporary file that is renamed into place | Re-encode the whole file (loses comments) |
| M5-8 | Colours | Each colour role (text, frame, up, down, unknown, stale/off, selection) has a base hue, darkened step by step until it meets the preset's ratio against every background it is drawn on. `Max` is black and white only. A table-driven test checks every pair (TC-002) | Fixed hand-picked palettes per preset |
| M5-9 | Shapes (SPEC-702, 704, 708, 711) | Host: a square with the status glyph (● up, ✕ down, ? unknown) and the word next to the name. Collapsed ordinary group: a folder shape (rectangle with a tab). Collapsed network group: a rectangle with a double border and a CIDR line. Expanded frames: solid border for ordinary groups, dashed for network groups. Stale: a hollow glyph plus "(stale)". Note marker: a folded top-right corner | — |
| M5-10 | Which views a host is in (crosscheck 2026-11-11-01 I1) | Not in M5: no API change, and the details panel doesn't list views. Revisit in M6 | Add `views: [ids]` to host JSON now |

Testing: `internal/theme` and `internal/gui/mapmodel` are pure Go with unit tests. The Fyne widgets are tested headless with `fyne.io/fyne/v2/test`. A manual script runs the GUI under Xvfb against a test server with generated hosts; Claude checks the screenshots, and the user does a final visual check.

## Open questions
- GUI layout (M6): expanded frames are drawn around their members wherever those are, so items placed by hand or by older versions can overlap a frame. New members now start 120 units below their group (v0.19). Options for M6: dragging (planned), and perhaps a "tidy up" action for a group.
- GUI in Max contrast: buttons have a white background like the window, so only their text shows. Acceptable for "black and white only", or give buttons a black border?
- Not yet decided: configurable `log/slog` level and format, OpenAPI-first or generated from the handlers (before M7), `inedctl backup`.
- `staticcheck` / `govulncheck`: run in CI (M7), or locally once allowed.
