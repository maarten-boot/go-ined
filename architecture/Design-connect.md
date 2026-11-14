# Design: "Connect ▸" on hosts (SSH, RDP, VNC, HTTP, HTTPS)

Status: **proposed** (2026-11-14), waiting for the user's OK.

User request: "when I right-click on a host can we also get a menu to ssh/rdp/vnc to that host; we don't need to do a full port scan, just the default tcp/22, tcp/590x and the tcp/rdp port, and port 80/443 (http/https would also be handy). Later we can add an SNMP scan for all hosts and a set of default community strings to try."

## Proposal

- A host's context menu gets **"Connect ▸"**:
  ```
  Connect ▸  ✓ SSH (22)
             ✓ Remote desktop (3389)
               VNC (5900)
             ✓ VNC :1 (5901)
             ✓ Web (http, 80)
               Web (https, 443)
               ──────────────
               Check services now
  ```
- A tick means the port answered at the last check.
- The server checks a fixed, short list of TCP ports per host: 22, 3389, 5900–5909, 80, 443. It stores what answered, so the menu opens at once and the details panel can show it.

## Decisions (recommended)

- **C1 Who checks: the server.** It already pings every host, and the later SNMP scan has to run there too. A check is one TCP connect per port (1 s timeout), no banner reading, so it's not a port scan.
  - It runs when a host is created or discovered, every 6 hours for monitored hosts, and on "Check services now" (from the menu, `POST /hosts/{id}/check-services`, `inedctl host services <id> [--check]`).
  - Unmonitored hosts are only checked on request.
  - Results go into the host: `services` (for example `["ssh","rdp","vnc:5901","http"]`) and `services_checked_at`. A change emits `host.updated`. Schema 6 adds two columns.
  - Caveat: this is what the server can reach. When the GUI runs elsewhere, a firewall can differ. That is why C2 exists.
- **C2 Every entry stays clickable.** Ports that didn't answer, or haven't been checked yet, are shown without a tick, not disabled. VNC shows 5900 always, and every other display (5901–5909) that answered.
- **C3 How a connection opens: commands you can configure.** They go in `gui.toml` under `[connect]`, with `{ip}`, `{name}` and `{port}` as placeholders. Defaults per platform:

  | | Linux (Ubuntu) | macOS | Windows |
  |---|---|---|---|
  | SSH | `x-terminal-emulator -e ssh {ip}` | `open ssh://{ip}` | `cmd /c start ssh {ip}` |
  | RDP | `remmina -c rdp://{ip}` | `open rdp://full%20address=s:{ip}` | `mstsc /v:{ip}` |
  | VNC | `remmina -c vnc://{ip}:{port}` | `open vnc://{ip}:{port}` | `cmd /c start vnc://{ip}:{port}` |
  | HTTP(S) | the system browser (Fyne's OpenURL) on every platform | | |

  If a program is missing, the status bar says which command failed and which `gui.toml` key changes it. On Ubuntu, `sudo apt install remmina` gives you RDP and VNC.
- **C4 Address and user.**
  - The connection uses the host's **IP address**, which always works, rather than its name. `{name}` is there for anyone who wants names.
  - SSH gives no user name, so your `~/.ssh/config` and local user name apply. Set `ssh = "… ssh admin@{ip}"` if you want a fixed one.
- **C5 Safe to run.**
  - Commands are split into arguments once, from the config, and run without a shell.
  - `{ip}` comes from the server and is checked as an IP address. `{name}` is passed as a single argument, so a host name can't inject anything.
- **C6 Details panel.** It gets a "Services" row ("ssh, rdp, vnc :1, http · checked 14:02").
- **C7 SNMP later.** It's out of scope here and goes in the Plan. It would reuse the same idea: a server-side check per host, a stored result, and a setting for the community strings to try.

## Changes

- New SPEC items for the server check (API, storage, scheduling), the GUI menu and connect commands, and the `[connect]` config.
- OpenAPI, API examples, README (config and what to install). TCs to be numbered.
- Tests:
  - the check against a local listener on a free port (the port list is configurable in tests);
  - menu ticks;
  - building the command from the config, with placeholders and no shell;
  - errors when a program is missing.
