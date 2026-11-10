# Changes

All notable changes between releases. Format based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]
### Added
- Project setup: git repo, `.gitignore`, project `CLAUDE.md` rules.
- Architecture docs: Spec, Plan, Tests, conversation log.
- Spec v0.3: notes, collapse/expand on a single shared map.
- Spec v0.4: networks modelled as groups with a CIDR (network groups), no link lines.
- Spec v0.5: crosscheck 01 fixes: pan/zoom and find host, `stale` flag, run-id event ids, `inedctl` spec, config keys and XDG paths, error-code table.
- Example config files: `examples/server.toml`, `examples/gui.toml`, `examples/ctl.toml`.
- M1 server core: `ined-server` with SQLite inventory, hosts, groups and network groups, notes, REST API (`/api/v1`: hosts, groups, map, health), bearer-token auth, optional TLS, TOML config.
