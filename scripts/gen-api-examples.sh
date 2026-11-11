#!/usr/bin/env bash
# Regenerates architecture/API-examples.md from a live ined-server run.
# Usage: scripts/gen-api-examples.sh   (from the repository root; needs go and curl)
# Used by every crosscheck: regenerate, then `git diff architecture/API-examples.md`.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
# Temporary files go under one per-project folder, never loose in /tmp.
mkdir -p /tmp/go-ined
WORK=$(mktemp -d /tmp/go-ined/gen-examples.XXXXXX)
trap 'kill "${PID:-}" 2>/dev/null || true; rm -rf "$WORK"; rmdir /tmp/go-ined 2>/dev/null || true' EXIT

go build -C "$REPO" -o "$WORK/ined-server" ./cmd/ined-server
go build -C "$REPO" -o "$WORK/inedctl" ./cmd/inedctl
# Monitoring and reverse DNS off: the output must not depend on which
# example addresses answer pings or have names on the machine that runs
# this script. Discovery probes 127.0.0.0/30, which answers on Linux.
mkdir -p "$WORK/cfg/go-ined"
printf '[monitor]\nenabled = false\n\n[dns]\nenabled = false\n' >"$WORK/cfg/go-ined/server.toml"
XDG_CONFIG_HOME="$WORK/cfg" "$WORK/ined-server" -listen 127.0.0.1:0 -db "$WORK/ex.db" >"$WORK/server.log" 2>&1 &
PID=$!

ADDR=""
for _ in $(seq 100); do
  ADDR=$(sed -n 's/.*listen=\([^ ]*\).*/\1/p' "$WORK/server.log" | head -1)
  [ -n "$ADDR" ] && curl -s -o /dev/null "http://$ADDR/api/v1/health" && break
  sleep 0.1
done
[ -n "$ADDR" ] || { echo "server did not start:" >&2; cat "$WORK/server.log" >&2; exit 1; }

OUT="$WORK/body.md"
: >"$OUT"
# ex TITLE METHOD PATH [JSON-BODY]
ex() {
  local title=$1 m=$2 p=$3 b=${4:-} cmd resp code body
  if [ -n "$b" ]; then cmd="curl -s -X $m \$INED/api/v1$p -d '$b'"
  elif [ "$m" = GET ]; then cmd="curl -s \$INED/api/v1$p"
  else cmd="curl -s -X $m \"\$INED/api/v1$p\""; fi
  { echo "### $title"; echo; echo '```bash'; echo "$cmd"; echo '```'; echo; } >>"$OUT"
  if [ -n "$b" ]; then resp=$(curl -s -w '\n%{http_code}' -X "$m" "http://$ADDR/api/v1$p" -d "$b")
  else resp=$(curl -s -w '\n%{http_code}' -X "$m" "http://$ADDR/api/v1$p"); fi
  code=$(tail -1 <<<"$resp"); body=$(sed '$d' <<<"$resp")
  { echo "Response: \`$code\`"; if [ -n "$body" ]; then echo; echo '```json'; echo "$body"; echo '```'; fi; echo; } >>"$OUT"
}

ex "Health" GET /health
ex "Create a network group (name defaults to the CIDR)" POST /groups '{"cidr":"192.168.1.0/24","note":"home LAN","x":100,"y":100}'
ex "Create an ordinary group inside it" POST /groups '{"name":"servers","parent_id":1,"collapsed":false}'
ex "Add a host without parent_id (placed in the most specific network group)" POST /hosts '{"ip":"192.168.1.10","name":"nas"}'
ex "Move the host into a group, set a note and a position" PATCH /hosts/1 '{"parent_id":2,"note":"Synology, rack 2","x":140,"y":260}'
ex "Expand the network group" PATCH /groups/1 '{"collapsed":false}'
ex "Get the whole map" GET /map
ex "Error: duplicate IP address" POST /hosts '{"ip":"192.168.1.10"}'
ex "Error: CIDR with host bits set" POST /groups '{"cidr":"10.0.0.5/24"}'
ex "Error: nested network group outside its parent" POST /groups '{"cidr":"10.0.0.0/24","parent_id":1}'
ex "Error: deleting a non-empty group without children=" DELETE /groups/1
ex "Delete a group and move its contents up one level" DELETE '/groups/2?children=reparent'
ex "Delete a group with everything in it" DELETE '/groups/1?children=cascade'

# Live events (SSE): capture the stream while a host is added and deleted.
curl -sN "http://$ADDR/api/v1/events" >"$WORK/events.txt" &
CURL=$!
sleep 0.3
curl -s -o /dev/null -X POST "http://$ADDR/api/v1/hosts" -d '{"ip":"192.168.1.20"}'
curl -s -o /dev/null -X DELETE "http://$ADDR/api/v1/hosts/2"
sleep 0.3
kill "$CURL" 2>/dev/null || true
wait "$CURL" 2>/dev/null || true
{
  echo "### Watch live events (Server-Sent Events)"
  echo
  echo "In one terminal:"
  echo
  echo '```bash'
  echo 'curl -sN $INED/api/v1/events     # add -H "Last-Event-ID: <id>" to resume after a reconnect'
  echo '```'
  echo
  echo "In another: \`POST /hosts {\"ip\":\"192.168.1.20\"}\`, then \`DELETE /hosts/2\`. The stream shows:"
  echo
  echo '```text'
  cat "$WORK/events.txt"
  echo '```'
  echo
} >>"$OUT"

# Discovery: start a job, wait, fetch the result.
ex "Start a discovery job (a network group 127.0.0.0/30 is created)" POST /discovery '{"cidr":"127.0.0.0/30"}'
JOB=$(grep -o '"id": "[^"]*-[0-9]*"' "$OUT" | tail -1 | sed 's/.*"\([^"]*\)"$/\1/')
sleep 2
ex "Get the job: done, with 2 hosts found and added" GET "/discovery/$JOB"
ex "Error: a range larger than discovery.max_prefix (/16)" POST /discovery '{"cidr":"10.0.0.0/8"}'
ex "Error: discovery is IPv4 only" POST /discovery '{"cidr":"2001:db8::/120"}'

# inedctl: the same server through the CLI.
# ctl TITLE DISPLAYED-ARGS ARG... runs inedctl and records its output and
# exit code. FILTER, if set, post-processes the output.
ctl() {
  local title=$1 shown=$2 out code
  shift 2
  set +e
  out=$(XDG_CONFIG_HOME="$WORK/cfg" INED_URL="http://$ADDR" "$WORK/inedctl" "$@" 2>&1)
  code=$?
  set -e
  [ -n "${FILTER:-}" ] && out=$(eval "$FILTER" <<<"$out")
  { echo "### $title"; echo; echo '```bash'; echo "inedctl $shown"; echo '```'; echo
    if [ -n "$out" ]; then echo '```text'; echo "$out"; echo '```'; else echo "No output."; fi
    echo; echo "Exit code: \`$code\`"; echo; } >>"$OUT"
}
{ echo "## inedctl"; echo
  echo 'The same server through the command-line client. `INED_URL` (or `--url`) points it at the server.'; echo; } >>"$OUT"
# Remove the discovered loopback hosts first: their round-trip times differ
# on every run.
ctl "Delete a group with everything in it" "group rm 3 --cascade" group rm 3 --cascade
ctl "Add a network group" "group add --cidr 10.10.0.0/24 --name lab" group add --cidr 10.10.0.0/24 --name lab
ctl "Add a host (goes into the matching network group)" "host add 10.10.0.5 --name printer --note '2nd floor'" host add 10.10.0.5 --name printer --note '2nd floor'
ctl "List the hosts" "host list" host list
ctl "List the groups" "group list" group list
ctl "The API's JSON instead of a table" "--json host show 5" --json host show 5
ctl "Replace a note from stdin" "note set host 5 < note.txt" note set host 5 <<<"Toner replaced 2026-11"
ctl "Read the note" "note get host 5" note get host 5
FILTER='sed -n "1p;\$p"' ctl "Discover a range and wait for the result (progress lines in between left out here)" "discover start 127.0.0.4/30 --wait" discover start 127.0.0.4/30 --wait
ctl "Error from the API: exit code 1" "host show 99" host show 99
ctl "Usage error: exit code 2" "host move 5" host move 5
ctl "Server not reachable: exit code 3" "--url http://127.0.0.1:1 health" --url http://127.0.0.1:1 health

# Views (SPEC-117..124): a custom view with its own grouping of hosts.
{ echo "## Views"; echo
  echo 'Each view is its own map (a tab in the GUI). Hosts are shared: a host placed in a custom view keeps its place in the network view (view 1).'; echo; } >>"$OUT"
ex "Create a custom view" POST /views '{"name":"Printers"}'
ex "Add a group to the view" POST /groups '{"name":"2nd floor","view_id":2,"collapsed":false}'
ex "Put host 5 in that group (only in this view)" PUT /views/2/hosts/5 '{"parent_id":6}'
ex "The view's map" GET '/map?view=2'
ex "Error: network groups exist only in the network view" POST /groups '{"cidr":"10.20.0.0/24","view_id":2}'
ex "Error: the network view can't be deleted" DELETE /views/1
ctl "The views in tab order" "view list" view list
ctl "A view and its hosts" "view show 2" view show 2

# Moving a group with everything in it (SPEC-507, 508).
{ echo "## Moving groups"; echo
  echo 'A group moves with its contents in one request; one `group.moved` event tells clients about the contents. A new parent without a position puts the item next to the new parent.'; echo; } >>"$OUT"
ex "Move group 6 (in view 2) and its hosts 50 units right" POST /groups/6/move '{"dx":50,"dy":0}'
ctl "The same with inedctl" "group shift 6 --dy 40" group shift 6 --dy 40
ex "Move only the contents of group 6, not the group itself" POST /groups/6/move '{"dx":0,"dy":30,"contents_only":true}'
ctl "A new parent: the host moves next to it" "host move 5 root" host move 5 root

# Arranging a group's members on a wider grid (SPEC-510, "Reflow" in the GUI).
{ echo "## Arranging"; echo
  echo 'Lays out the direct members of a group (or of the view'"'"'s root) on a grid, so long names have room. One `map.arranged` event; the response says how many members moved.'; echo; } >>"$OUT"
ex "Arrange the root of the network view on 160 × 80 cells" POST /views/1/arrange '{"cell_width":160,"cell_height":80}'
ex "Error: cell size out of range" POST /views/1/arrange '{"cell_width":5,"cell_height":80}'
ctl "The same with inedctl, for one group" "view arrange 2 --group 6 --width 200" view arrange 2 --group 6 --width 200

{
cat <<'EOF'
# API examples — go-ined

Generated by `scripts/gen-api-examples.sh` from a live `ined-server`. IDs, timestamps and `run_id` will differ on your system.
The steps run in order; each one depends on the ones before it.

```bash
ined-server -listen 127.0.0.1:8470 &
INED=http://127.0.0.1:8470
# With a token configured, add: -H "Authorization: Bearer $TOKEN"
```

EOF
cat "$OUT"
} >"$REPO/architecture/API-examples.md"
echo "wrote architecture/API-examples.md"
