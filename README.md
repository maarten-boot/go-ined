# go-ined

A network monitor loosely modelled on [Tkined](https://github.com/flightaware/scotty) (Scotty), split into two parts:

- **`ined-server`**: a daemon that keeps the inventory in SQLite and serves it over a REST + Server-Sent Events API. Planned: CIDR discovery, background ping and reverse DNS.
- **`ined-gui`** (planned): a desktop client ([Fyne](https://fyne.io)) that shows hosts and groups on one shared map with live status. Groups can be collapsed and expanded, as in Tkined.
- **`inedctl`** (planned): a command-line client.

Hosts and groups form a tree. A group with a CIDR is a **network group**: discovery fills it, and a host added by hand goes into the most specific network group that contains its IP address. Every host and group can have a note.

## Status

Early development. **Milestone 1 (server core) is done**: inventory, network groups, notes, the REST API, bearer-token auth, optional TLS and the TOML config.
Next up: live events (SSE) and background ping (M2), discovery and DNS (M3), the CLI (M4), and the GUI (M5–M6).
See [`architecture/Plan.md`](architecture/Plan.md) for the roadmap.

## Build and run

Requires Go 1.26 or later. The server is pure Go (no cgo).

```bash
make            # gofmt check, vet, tests (-race), build into bin/
# or: go build -o bin/ ./cmd/...

bin/ined-server                         # listens on 127.0.0.1:8470
bin/ined-server -listen 127.0.0.1:9000 -db /tmp/ined.db
bin/ined-server -version
```

A quick try:

```bash
INED=http://127.0.0.1:8470
curl -s -X POST $INED/api/v1/groups -d '{"cidr":"192.168.1.0/24"}'
curl -s -X POST $INED/api/v1/hosts  -d '{"ip":"192.168.1.10","name":"nas"}'   # goes into 192.168.1.0/24
curl -s $INED/api/v1/map
```

More (verified) calls are in [`architecture/API-examples.md`](architecture/API-examples.md).

## Configuration

Files follow XDG:

| File | Default location |
|------|------------------|
| Server config | `$XDG_CONFIG_HOME/go-ined/server.toml` (falls back to `~/.config/go-ined/server.toml`) |
| Database | `$XDG_DATA_HOME/go-ined/ined.db` (falls back to `~/.local/share/go-ined/ined.db`) |

Every key, with its default, is in [`examples/server.toml`](examples/server.toml) (and `gui.toml` / `ctl.toml` for the clients). Command-line flags override the file.

Security: by default the server listens on loopback only. To listen on any other address you must set `server.token`; clients then send `Authorization: Bearer <token>`. Set `server.tls_cert` and `server.tls_key` to turn on TLS.

Only one `ined-server` can use a database at a time; a second one refuses to start.

## Documentation

All design documents live in [`architecture/`](architecture/):

- [`Spec.md`](architecture/Spec.md): requirements (`SPEC-NNN`)
- [`Plan.md`](architecture/Plan.md): decisions and milestones
- [`Tests.md`](architecture/Tests.md): test cases traced to the Spec, with the Go test that covers each one
- `Crosscheck-*.md`: periodic checks of code against the docs

Changes between releases are listed in [`Changes.md`](Changes.md).

## License

[MIT](LICENSE)
