# Plan — go-ined

Status: **draft v0.29** (2026-11-12)

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
| D19 | M7 details (user OK 2026-11-11, all defaults): GitHub Actions with jobs check, test, raw-icmp, build; only `actions/checkout` v7.0.1 and `actions/setup-go` v7.0.0, SHA-pinned; staticcheck v0.8.2 and govulncheck v1.9.0 via `go run`; raw ICMP as root in CI; GUI built for linux/amd64 only; hand-written OpenAPI with tests; `[log]` settings; backup LATER (README note); a tag workflow with `gh release`; Dependabot weekly | 2026-11-11 |
| D18 | Crosscheck 2026-11-11-03 decisions (user OK 2026-11-11): a `PATCH` of a group's position moves only the group, a new parent takes the contents along (D1 a); "New host here…" on empty space puts the host next to the network group the server picks (D2 a) | 2026-11-11 |
| D17 | M6 details (user OK 2026-11-11, all defaults): dragging a group moves its subtree; `POST /groups/{id}/move` with one `group.moved`, and `inedctl group shift`; a new parent gives a free position; dropping onto empty space keeps the parent; `views` in host JSON; a View menu button; the discovery dialog can close while the job runs; always confirm deletes, Delete key, no undo; single-item drag with a 4-pixel threshold and Esc; dialog errors stay in the dialog. See the M6 decisions table | 2026-11-11 |
| D16 | Crosscheck 2026-11-11-02 decisions (user OK 2026-11-11): black button borders in Max contrast (D1 a); overlapping frames are handled by dragging in M6, no tidy-up action (D2 a); rows of a group grow with its size, 10–50 per row (D3 a); the toolbar shows the zoom (I1) | 2026-11-11 |
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
- [x] Benchmark: one layout and drawing pass of 5,000 hosts (about 10 ms). Burst test (TC-714): 1,000 status events with 5,000 hosts take about 15 ms with one redraw (crosscheck 2026-11-11-02 C1). The server's `AggregateAll` benchmark moves to M7 with the other performance checks
- [x] Load probe with the real binaries (2,046 discovered hosts, about 1,600 changes in 10 s under Xvfb): memory +69 MB, no backlog after the changes stop. Repeat it after large GUI changes
- [ ] The user's visual check of the GUI (TC-005, 702 partly, 705, 708 manual part)

### M6: GUI editing ✅ (branch `feature/m6-gui-editing`)
- [x] Spec items (draft v0.21): SPEC-507..509 (API), SPEC-704/706/707 detailed, SPEC-719..721; test cases TC-715..722
- [x] Decisions M6-1..10 (D17)
- [x] API: `POST /groups/{id}/move` and `group.moved`; new parent → free position with the subtree; `views` in host JSON; `pkg/client` and `inedctl group shift` (M6-2)
- [x] GUI: drag (hosts, groups with their contents), drop into a group, "Move to group…"
- [x] GUI: create, edit and delete groups and hosts; confirmations; Delete key
- [x] GUI: discovery dialog with progress and cancel
- [x] GUI: View menu; hosts in custom views
- [x] Smoke test of the real binary under Xvfb driven by `xdotool`: drag onto a group (marked "into"), frame drag with contents, right-click menus, New group dialog, Delete group dialog. Fixed from it: dragged items drawn on top, the first field of a form gets focus, plurals in the delete question
- [ ] TC-702 manual script and the user's visual check of M5 and M6

### M6 decisions (proposed; defaults recommended)
| # | Question | Recommended default | Alternative |
|---|----------|---------------------|-------------|
| M6-1 | What dragging a group moves | The group **with its whole subtree**, by one offset (SPEC-704), for a collapsed icon and an expanded frame alike, so expanding later shows the members next to the group | Only the group's own position (members stay; an expanded frame then jumps back to them) |
| M6-2 | How a subtree move reaches the server | New `POST /groups/{id}/move {dx, dy}`: one transaction and one `group.moved` event (SPEC-507). Also `inedctl group shift <id> <dx> <dy>` | The GUI sends one PATCH per item: up to thousands of requests and events for a big group, and no atomicity |
| M6-3 | Position after a change of parent | A free position near the new parent; a group's subtree moves along (SPEC-508). Changes `inedctl host move` / `group move`, which kept the old position | Keep the old position (the item may sit far from its new group, and the frame stretches to it) |
| M6-4 | Dropping onto empty space | Only moves the item; the parent stays and the frame grows. Moving to the root: "Move to group… → Root" | Dropping outside the parent's frame moves the item to the root (easy to do by accident) |
| M6-5 | Which views a host is in (M5-10) | `views` in host JSON (SPEC-509), used by "Show in view ▸" and the details panel | Load every custom view's map to find out |
| M6-6 | Managing views | A "View" menu button in the toolbar acting on the current tab (SPEC-720); order changes swap `order` with the neighbour (two PATCHes) | A context menu on the tabs (Fyne's tabs have none, so it would need a custom tab bar) |
| M6-7 | Discovery dialog | From the toolbar and "Scan this network"; closing it leaves the job running with progress in the status bar (SPEC-707) | A modal dialog that stays open until the job ends |
| M6-8 | Deleting | Always confirm, naming what goes; reparent or cascade for non-empty groups; Delete key; no undo (SPEC-719) | Undo for the last delete (needs server support: soft delete) |
| M6-9 | Selection and dragging | One item at a time; drag starts after 4 pixels of movement, so a click stays a click; Esc cancels | Multi-select with rubber band (LATER) |
| M6-10 | Dialog errors | The API's message under the input; the dialog stays open (SPEC-706) | Close the dialog and show the error in the status bar |

Testing: the API additions get unit and API tests as before. GUI actions are tested headless (`fyne.io/fyne/v2/test`: Drag, Tap, Type) against the test server; dialogs are built by functions that tests can drive, as in M5. A manual script (TC-702) and the user's visual check close the milestone.

### M7: CI + release 0.1.0 ✅ (released 2026-11-11)
- [x] Spec items (draft v0.24): SPEC-007 (CI), SPEC-008 (release), SPEC-505 detailed, SPEC-807 (server log); test cases TC-010..013, TC-505 and TC-309 extended
- [x] Decisions M7-1..10 (D19)
- [x] Run `staticcheck` and `govulncheck` locally first and fix what they find: one unused field; 15 reachable vulnerabilities, fixed with toolchain go1.26.11 (standard library) and `golang.org/x/image` v0.48.0 (via Fyne)
- [x] `.github/workflows/ci.yml` and `release.yml` (pinned SHAs), `.github/dependabot.yml`; `scripts/build-release.sh` (dry run: 7 archives and SHA256SUMS) and `scripts/release-notes.sh`; repository tests in `internal/repocheck`
- [x] First CI run on GitHub green: run 43091727389 failed (GLFW 3.4 needs the Wayland and xkbcommon headers); after adding them, run 43093370048 passed all four jobs. raw-icmp ran as root in raw mode (127.0.0.1 and ::1); all 7 archives checked
- [x] OpenAPI document with its tests (SPEC-505). Checked by a formal validator on 2026-11-13: `openapi-spec-validator` 0.9.0 (`--schema 3.1.0`) reports OK
- [x] `[log]` config (SPEC-807)
- [x] User report (2026-11-11): scanning as an ordinary user failed (no ICMP mode on the user's machine) and the GUI's error couldn't be copied. Fixed: copyable messages in the discovery dialog and status bar, a ping warning on connect, the server message names the commands, README "Ping permissions" (crosscheck 2026-11-11-04 U1–U5)
- [x] Curated `[0.1.0]` section in `Changes.md` (crosscheck 2026-11-11-04 D1)
- [x] CI on `main` green: run 43099982463 (commit `a871677`) passed all four jobs after the GitHub Actions outage; the earlier run 43096857026, queued during the outage, was superseded
- [ ] The user's visual check and TC-702 (open since M5/M6). The user chose to release 0.1.0 without reporting it (2026-11-11); it stays open for 0.1.x
- [x] `## [0.1.0] - 2026-11-11` in `Changes.md`; annotated tag `v0.1.0` made locally (tagger: the GitHub noreply address)
- [x] Release run 43102899949 green (CI's four jobs, then release); https://github.com/maarten-boot/go-ined/releases/tag/v0.1.0 has 7 archives and SHA256SUMS. Downloaded: every checksum OK; `ined-server`, `inedctl` and `ined-gui` print `v0.1.0`; built with go1.26.11; the release text equals the `[0.1.0]` section (TC-011)

### M7 decisions (proposed; defaults recommended)
| # | Question | Recommended default | Alternative |
|---|----------|---------------------|-------------|
| M7-1 | CI platform and layout | GitHub Actions with four jobs: check, test, raw-icmp, build (SPEC-007), on `ubuntu-24.04` | One job doing everything (simpler, slower, less clear when it fails) |
| M7-2 | Actions used | Only `actions/checkout` **v7.0.1** (`3d3c42e5aac5ba805825da76410c181273ba90b1`) and `actions/setup-go` **v7.0.0** (`b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`), both the latest releases (July 2026), pinned by SHA (SPEC-006). Go comes from `go.mod` (1.26) | Third-party actions for staticcheck, golangci-lint or releases (more SHAs to keep up to date) |
| M7-3 | Lint tools | `staticcheck` from module `honnef.co/go/tools` **v0.8.2** (2026-10-12) and `govulncheck` **v1.9.0**, both run with `go run …@version`; findings fail the job. First run both locally and fix what they report | golangci-lint with a larger rule set (more noise, another config file) |
| M7-4 | Raw-socket ICMP in CI (open since M2) | A job that runs the ICMP tests with `sudo`, forcing the raw mode, against 127.0.0.1 and ::1 (TC-309) | Leave raw mode tested only with the fake socket |
| M7-5 | Builds | `ined-server` and `inedctl`: linux, darwin, windows × amd64, arm64 without cgo. `ined-gui`: linux/amd64 only (cgo cross-compiling for other systems needs their SDKs) | Also build the GUI for macOS and Windows on their own runners (three more runners and signing questions) |
| M7-6 | OpenAPI (open question) | Written by hand, embedded, checked by tests against the real routes and `apitypes` (SPEC-505) | Generated from the code with an annotation tool (a new dependency and comment syntax) |
| M7-7 | Server log (open question) | `[log] level` and `format` plus `-log-level` (SPEC-807) | Keep the fixed text log at info |
| M7-8 | `inedctl backup` (open question) | LATER: for now the README says how to copy the database safely (`sqlite3 ined.db ".backup copy.db"` while the server runs) | An API endpoint for a consistent copy now |
| M7-9 | Release | A tag workflow with archives, `SHA256SUMS` and the `Changes.md` section as text, published with `gh release` (preinstalled on the runner) and the job's `GITHUB_TOKEN` (SPEC-008). No signing in 0.1.0 | GoReleaser (another tool and config), or signing with Sigstore now |
| M7-10 | Updates | Dependabot weekly for `github-actions` and `gomod`; it keeps the SHA pins and their version comments current | Update by hand |

Process: a Dependabot update of the `toolchain` line in `go.mod` makes CI fail (`TestCIWorkflow`) until `GO_VERSION` in `ci.yml` and the Go version in `release.yml` are changed to match; this is on purpose, so the three can't drift apart. Claude can't push. Claude writes the workflows and runs everything they run locally (apart from the GitHub-only parts). The user pushes, and Claude reads the run results with `gh run view` and fixes them. The same goes for the tag `v0.1.0`.

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

### After 0.1.0
- [x] User request (2026-11-11): find ICMP again without a restart. `monitor.Rechecker` (tries again every minute while a family is missing, and before each discovery job); monitoring starts even without ICMP; `/health` and the log follow; the GUI reads `/health` every 30 s (SPEC-304, 717 v0.27; TC-315)
- [x] User request (2026-11-11): long DNS names. The user chose both options: "Wrap names" (display) and "Reflow" (positions). Defaults taken by Claude: wrap only where a name would run into its row neighbour; break after dots/colons; the GUI computes the cell from the longest full name; `POST /views/{id}/arrange` with one `map.arranged` event; `inedctl view arrange` (SPEC-510, 722, 723; TC-723, 724). Checked under Xvfb: wrapped names, then Reflow gives room and the names unwrap
- [x] User request: keep a group's top-left corner in place on expand/collapse: `contents_only` move; the GUI aligns on collapse (group position) and on expand (contents first); empty frames start at the icon's corner (SPEC-507, 703 v0.29; TC-725). Checked under Xvfb, also after dragging the collapsed icon elsewhere
- [x] User request: multi-select with Tkined keys. User's answers: Ctrl+click and Shift+drag rubber band; g asks a name; c/e/Delete act on all selected (SPEC-724; TC-726). Checked under Xvfb: Ctrl+click, g, c, rubber band, Delete
- [x] Crosscheck 2026-11-12-01 fixes (`fix/crosscheck-10`): delayed close of a replaced prober (C1); right-click keeps a multi-selection and the menu's Move/Delete act on all selected (C2, D1 b; TC-727); "Cmd on macOS" (S1); arrange and `contents_only` examples (E1); "arranged 1 member" (SPEC-304, 724 v0.30)
- [x] Release 0.2.0 prepared (crosscheck 2026-11-12-01 D2 a): `[0.2.0] - 2026-11-12` in `Changes.md` (curated: follow-up fixes folded into the new features), OpenAPI `info.version` 0.2.0, README status; local release build: 8 files, checksums OK, binaries print `v0.2.0`; annotated tag `v0.2.0`
- [x] Crosscheck 2026-11-12-02 fixes (`fix/crosscheck-11`): the menu counts and acts on the top-level selection (C1); a replaced prober is closed after its last round instead of after two minutes (C2) (SPEC-304, 724 v0.31; TC-315, 727)
- [x] User request (2026-11-12): show the IP address instead of the DNS name, per group. User's answers: Name / IP / both; saved on the server. Defaults taken by Claude: `""` inherits from the parent group, the root shows names; one group per menu choice (not the whole selection); `both` puts the IP on the line below the name (SPEC-725 v0.32; TC-728)
- [x] The user's report "hosts now show their IP instead of the DNS name" (names showed before): not reproduced here (a fresh server names hosts added by hand, found by a scan and after a restart; the GUI only shows the server's display name). Waiting for the user's `inedctl --json host show` and `dig -x` output; later explained (crosscheck 2026-11-12-04 P2, below)
- [x] Crosscheck 2026-11-12-03 fixes (`fix/crosscheck-12`): Expand, Collapse and Host labels in the menu act on all selected groups (C1, D1 b); the inherited mode in brackets in the submenu and a "Host labels" row in the details panel (C2); `inedctl group add --labels` (S1); README (P1) (SPEC-724, 725 v0.33; TC-727, 728)
- [x] Release 0.3.0 prepared (crosscheck 2026-11-12-03): `[0.3.0] - 2026-11-12` in `Changes.md`, OpenAPI `info.version` 0.3.0, README status; local release build: 8 files, checksums OK, binaries print `v0.3.0`; annotated tag `v0.3.0`
- [x] Release run 43300728621 green; CI on `main` (43298101064), `docs/architecture` and `fix/crosscheck-12` green. The v0.3.0 release has 7 archives and SHA256SUMS; downloaded: every checksum OK, the three binaries print `v0.3.0`, built with go1.26.11, and the release text equals the `[0.3.0]` section (TC-011)
- [x] The user's report "hosts show their IP instead of the DNS name": explained. The names came back by themselves after a while, which matches the reverse-DNS refresher: one NXDOMAIN answer cleared a name until the next hourly lookup (crosscheck 2026-11-12-04 P2)
- [x] Crosscheck 2026-11-12-04 fixes (`fix/crosscheck-13`): expanding leaves already expanded groups alone (C1); a known PTR name is kept until 3 NXDOMAIN answers in a row, rechecked a minute apart, and changes are logged at Info (D2 a+b) (SPEC-403, 703 v0.34; TC-402, 726, 727)
- [x] Release 0.3.1 prepared (D1 a): `[0.3.1] - 2026-11-12` in `Changes.md`, OpenAPI `info.version` 0.3.1, README status; local release build: 8 files, checksums OK, binaries print `v0.3.1`; annotated tag `v0.3.1`
- [x] Release run 43303659200 green; CI on `main` (43303613070), `docs/architecture` and `fix/crosscheck-13` green. The v0.3.1 release has 7 archives and SHA256SUMS; downloaded: every checksum OK, the three binaries print `v0.3.1`, built with go1.26.11, and the release text equals the `[0.3.1]` section (TC-011)
- [x] Crosscheck 2026-11-12-05 fix (`fix/crosscheck-14`): DNS answers for an address the host no longer has are dropped, and only saved name changes are logged (C1; SPEC-403 v0.35; TC-405). In `[Unreleased]`, no release on its own
- [x] Crosscheck 2026-11-13-01 fix (`fix/crosscheck-15`): frames fit their label (the glyph counted; map text measured bold, as drawn) (C1; SPEC-703 v0.36; TC-725). Checked under Xvfb. In `[Unreleased]`, no release yet
- [x] Crosscheck 2026-11-13-02 fix (`fix/crosscheck-16`): Reflow on a collapsed group measures expanded subgroups as frames (C1; SPEC-723 v0.37; TC-724). In `[Unreleased]`, no release yet (D2)
- [x] User request: key **j** joins the selected hosts to the selected group at their places; asks which group when several are selected (`feature/join-key`; SPEC-724 v0.38; TC-729). In `[Unreleased]` under "Added"
- [x] User idea: a group as a view (`feature/group-focus`; `Design-group-focus.md`, F1–F9 as recommended, "OK, all"): a focus per tab, the trail, the "Show" list (Ctrl+L, filter), the key o, Backspace, zoom restored; GUI only (SPEC-726 v0.39; TC-730..734). Checked under Xvfb `:97`. In `[Unreleased]` under "Added"
- [x] Crosscheck 2026-11-13-03 fixes (`fix/focus-crosscheck-03`): expanding inside a focus keeps the corner under a collapsed ancestor (C1), typing in "Show" only filters (C2), an empty focus says so (L1) (SPEC-726 v0.40; TC-725, 733, 734)
- [x] Release 0.4.0 prepared (D2): `[0.4.0] - 2026-11-13` in `Changes.md` (the focus fixes folded into "Added"), OpenAPI `info.version` 0.4.0, README status; local release build: 8 files, checksums OK, binaries print `v0.4.0`; annotated tag `v0.4.0`
- [x] Release run 43511721535 green; CI on `main` (43511561499), `docs/architecture` (43511573169) and `fix/focus-crosscheck-03` (43511573012) green. The v0.4.0 release has 7 archives and SHA256SUMS; downloaded: every checksum OK, the three binaries print `v0.4.0`, built with go1.26.11, and the release text equals the `[0.4.0]` section (TC-011)
- [x] User request: `ined-gui` in the release also for windows/amd64, darwin arm64/amd64 and linux/arm64 (`feature/gui-platforms`): `build-release.sh --gui` on native runners; CI's gui job on `main`; the release as a draft until the GUI archives and `SHA256SUMS` are there (SPEC-007/008 v0.41; TC-010..012). Release 0.4.1 prepared: `[0.4.1]` in `Changes.md`, OpenAPI 0.4.1, README (platforms, unsigned-program notes); annotated tag `v0.4.1`, to be pushed after CI's gui job is green on `main`
- [x] CI gui job on `main` (43589541992): Windows and Linux arm64 green; macOS failed on `realpath -m`, fixed on `fix/macos-release-build`, tag `v0.4.1` moved
- [x] CI on `main` (43604749905) green, all three gui jobs included. Release run 43618686176 green (draft, gui, publish). The v0.4.1 release has 11 archives and SHA256SUMS; downloaded: every checksum OK, `inedctl` prints `v0.4.1`, `ined-gui.exe` is in the Windows zip, and the darwin amd64/arm64 and linux arm64 GUI binaries are of the right architecture (TC-011)
- [x] User report: after choosing a bar colour the bar stays plain and "None" stays checked. Not reproduced with the current server and GUI (driven with xdotool under Xvfb: the bar turns blue, the details say "Blue"). Reproduced with a v0.4.1 server: it answers 400 `unknown field "color"`, shown only in the status bar, and the map doesn't change. Cause: a server older than the GUI. `Changes.md` [0.5.0] says to update both together
- [x] Release 0.5.0 prepared: `[0.5.0]` in `Changes.md`, OpenAPI 0.5.0, README; annotated tag `v0.5.0`
- [x] User report: while a group is dragged its bottom bar line stays at the old height, outside the box. Fixed on `fix/drag-foot-line` (merged): the line isn't drawn during the drag (SPEC-704 v0.43, TC-736); the test fails without the fix. Tag `v0.5.0` (unpushed) moved to include it
- [x] CI on `main` (43620690509) green, gui jobs included
- [x] Release run 43621609026 green (draft, gui, publish); CI on `main` (43621608527) green. The v0.5.0 release has 11 archives and SHA256SUMS; downloaded: every checksum OK, `ined-server`, `inedctl` and `ined-gui` (linux/amd64) print `v0.5.0`, `ined-gui.exe` is in the Windows zip, the darwin amd64/arm64 and linux arm64 GUI binaries are of the right architecture (TC-011)
- [x] User request: one Zoom menu and the "Show" list in the toolbar (`Design-toolbar.md`, T1–T4, "OK, all"), on `feature/toolbar` (merged): "Zoom N % ▾" with the shortcuts in the labels (Fyne's menu shortcuts are 1.6:1), the list after it, the trail row only for a group (SPEC-717/726 v0.44; TC-737). Checked under Xvfb. In `[Unreleased]`
- [x] User report: the bar colour still doesn't show. Server (scratch) checked for network, nested and custom-view groups: fine. Cause confirmed by the user's `inedctl health`: the running server is `v0.2.0-5-g812912d-dirty` (12 Nov). The user restarts it with the new `bin/ined-server` (glibc 2.34; the GUI needs 2.38; Ubuntu 24.04 has 2.39)
- [x] User request: a version check in the GUI (`feature/version-check`, merged): a warning line for a server of an older release (SPEC-728; TC-738); checked under Xvfb against a v0.2.0 server. In `[Unreleased]`
- [x] Stray `inedctl` binary (tracked since `07cd371`) removed from git on `fix/stray-inedctl`; root binaries ignored
- [x] User request: fit all on start and after a resize (`Design-fit-on-resize.md`, R1–R5, "OK, all"), on `feature/fit-on-resize` (merged; SPEC-713 v0.46, TC-739). Checked under Xvfb with two window resizes
- [x] User request: "Show in view" on groups (`Design-group-show-in-view.md`, G1–G6, "OK, all"), on `feature/group-copy` (merged): `POST /views/{id}/copy-group`, `inedctl group copy`, the GUI menu (SPEC-729, TC-740); OpenAPI, API examples
- [x] User request: "Connect ▸" on hosts (`Design-connect.md`, C1–C7, "OK, all"), on `feature/connect` (merged): `internal/services` checks TCP 22, 3389, 5900–5909, 80, 443 (new monitored hosts at once, then every 6 h; any host on request); `services` + `services_checked_at` (schema 6); `POST /hosts/{id}/check-services`, `inedctl host services`; the GUI menu with ticks, "Check services now", a Services row; commands from `gui.toml` `[connect]` without a shell (SPEC-309, 511, 730, 731; TC-316, 317, 510, 741, 742). Checked end to end with real binaries against 127.0.0.1 and ::1 (ssh, http found). In `[Unreleased]`. The menu's look under Xvfb is not checked; it uses the same Fyne sub-menu and ticks as "Show in view"
- [x] User request: selectable details, the note below them, and "Remove from this view" / "Show in view" on groups and selections (`Design-details-and-selection.md`, D1–D6, "OK, all"), on `feature/details-selection` (merged): selectable title and values, the note right below the rows filling the rest of the height, "Remove from this view" on groups of custom views (cascade delete, asks when not empty), "Remove N items from this view" and "Show N items in view ▸" on a selection. GUI only (SPEC-708, 721, 724 v0.48; TC-743–746). In `[Unreleased]`. The look is not checked under Xvfb
- [x] User request: alerting by email per group, with syslog (`Design-alerting.md`, A1–A11, "OK, all"), on `feature/alerting` (merged): `server.read_token` (403 for changes, `access` in `/health`, SPEC-808); `contrib/ined-alert` in Python (client package, routes per group path, delay with a check on the server, batching, SMTP relay with retries, syslog, state file, server unreachable/degraded, start summary; SPEC-950–956; TC-808, 950–956); `make alert-test` and a CI job. Checked end to end with a real server, a fake relay and a UDP syslog listener: down, recovery, unreachable and back, resync, a restart with a recovery while stopped
- [x] Service alerts (`Design-service-alerts.md`, S1–S9 accepted): services watched per host (`watch`, known names and `tcp:N`), checked every 5 min by default (`services.watch_interval`) with a status each (schema 7), `service.status` events, a map marker, "Watch services…" in the GUI, `inedctl host watch`; `ined-alert` alerts on them, held back while the host itself is down. Order: server/API/inedctl, then ined-alert, then the GUI
  - [x] Part 1, server, API and inedctl, on `feature/service-watch` (merged): `watch` list per host, `internal/services` Watcher (5 min, down after 2, 50 connects at once), table `host_services` (schema 7), `service.status`, `services_down` in `/map`, `inedctl host watch` with the Watched line and SVC column (SPEC-310, 311, 512, 904; TC-318–321, 907). Checked end to end with real binaries against 127.0.0.1 (10 s interval): down after two failed checks, up again, kept over a restart, unknown with monitoring off
  - [x] Part 2, `ined-alert` service alerts, on `feature/service-alerts` (merged): from `service.status` and the `watch` lists, same delay, check on the server, routes, batching and repeats as hosts; held while the host is down or waiting, then `down_after` after its return; state file version 2; mail subjects and bodies; the server now checks a host's watched services at once when it comes back up (SPEC-310, 952, 954, 956 v0.51; TC-322, 957). Checked end to end with a real server, `ined-alert` and a fake relay: down mails for a closed port and a stopped listener, a restart of `ined-alert` with a recovery from the state file, unwatching closed without mail
  - [x] Part 3, GUI, on `feature/service-gui` (merged): "Watch services…" for a host or a selection, the service marker, the Watched row; `service.status` carries `services_down` so the map needs no extra request (SPEC-732, 311, 716 v0.52; TC-748–750). Marker rendered to PNG under AA and Max and checked; the user's look in the real window is pending
- [x] User request: the details rows more compact, "start with half the current whitespace between the lines", on `feature/compact-details` (merged): `compactForm` and `compactTheme`, 10 px instead of 20 between rows (`detailsSpacing` = 0.5), alignment kept (SPEC-708 v0.51; TC-747). Rendered to PNG and checked; the user's look in the real window is pending
- [x] User request: export and import of a network with its groups and hosts (`Design-network-export.md`, N1–N11, "OK, all"), on `feature/network-export` (merged): `GET /groups/{id}/export` (read token too), `POST /import` with a dry run, conflicts refused or updated, 32 MiB bodies; `inedctl group export` and `import`; GUI "Export…" and "Import here…" with the report dialog (SPEC-125, 513, 733, 905; TC-130–134, 513, 751, 908). Checked end to end with two real servers: dry run, import below another network group, custom-view group made, a missing view skipped, the same file refused, a second export equal to the first. The GUI's file and report dialogs are not checked by eye
- [x] Crosscheck 2026-11-17-01 (0 H, 6 M, 11 L in the code, 7 L in the docs; "OK, all"): fixes on `fix/crosscheck-17` (merged). `ined-alert`: a state file of the wrong shape or a newer version goes to `.bad` (C1); a recovery the server can't confirm waits for the next tick (C2); a renamed service (same port) keeps its alert (L4/P7); a snapshot without `watch` closes service alerts (L8); a refused token is named so (L9); a route without recipients is a warning (L10). Server: services off resets watched services to `unknown` (C3); a list given again keeps status by port (L4); a removed service's check in flight leaves no count (L7); import update warns for every matched group that stays where it is (C4) and refuses a plain group name twice under one parent (L1). `inedctl`: `-o` sets 0600 on an existing file (L3); an empty LIST is a usage error and `--add` of a watched service does nothing (L5). GUI: "Import into this group…" (C5); blanks after `tcp:` (L6). Docs: C4, C6 (a dropped mail still gets its recovery; re-sending is LATER), L2, P1–P6 (Spec v0.54). L11 (mail sent from the main loop) only noted
- [x] Release 0.6.0 prepared (crosscheck 2026-11-17-01 D3, "OK"): `[0.6.0]` in `Changes.md` (update server and GUI together; schemas 6 and 7), OpenAPI `info.version` 0.6.0, README status; local release build: 8 files, checksums OK, `ined-server`, `inedctl` and `ined-gui` print `v0.6.0`; annotated tag `v0.6.0`
- [x] Release 0.6.0 published. CI on `main` (44173191481) green after a rerun of the macOS GUI job, which first found no runner within 15 min (the user reran it; this session's token may not). Release run 44184266794 green (ci, release, gui ×3, publish). The v0.6.0 release has 11 archives and SHA256SUMS; downloaded: every checksum OK, `ined-server`, `inedctl` and `ined-gui` (linux/amd64) print `v0.6.0`, built with go1.26.11, `ined-gui.exe` is in the Windows zip, the darwin amd64/arm64 and linux arm64 GUI binaries are of the right architecture, and the release text equals the `[0.6.0]` section apart from a trailing blank line (TC-011)
- [ ] Later (`Design-alerting.md` A10): SNMP thresholds, after SNMP collection
- [ ] Later (user): an SNMP scan of all hosts with a set of default community strings to try (C7; Spec §13)
- [x] `TestClearAfterMisses` (dnsname) timed out twice under load. Cause: the test set the DNS answer after starting the refresher, whose first lookup is due at once, so under load the lookup could run first, get "not found", and the next one is an hour away on the fake clock. Three more tests had the same pattern; all now set the answers before the start (`newRigWith`). 20 of 20 runs pass with `-race`
- [x] User rule: run `make build` after every merge into `main`, so `bin/` matches `main` (rebuilt: `v0.4.1-4-g217c949`)
- [x] User request: a group's bottom bar to leave it, and a top bar colour (`feature/group-bars`; `Design-group-bars.md`, B1–B8 as recommended, "OK, all"): 12-unit bottom bar, items inside go to the parent below the frame; `color` on groups (schema 5, API, `inedctl group color`), "Bar colour ▸" with 16 colours and Custom…, contrast per preset (SPEC-704 v0.42, SPEC-727; TC-735, 736). Rendered to PNG for AA and Max and checked. In `[Unreleased]`; release 0.5.0 after 0.4.1
- [ ] TestDiscoveryDialog is flaky under `-race` (6–7 of 10 runs fail, also on `main` before this feature): the copied job details can still say `running` after the status bar says `done`. To look at
- [x] Release run 43227857742 green; CI on `main` (43227827366), `docs/architecture` and `fix/crosscheck-10` green. https://github.com/maarten-boot/go-ined/releases/tag/v0.2.0 has 7 archives and SHA256SUMS. Downloaded: every checksum OK; `ined-server`, `inedctl` and `ined-gui` print `v0.2.0`; built with go1.26.11; the release text equals the `[0.2.0]` section (TC-011)

## Open questions
- The log, OpenAPI and backup questions, and the `staticcheck` / `govulncheck` question, are in the M7 decisions (M7-3, M7-6..8).
