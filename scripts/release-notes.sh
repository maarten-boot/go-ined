#!/usr/bin/env bash
# Prints the section of Changes.md for a version, without its heading, as
# the text of a GitHub release (SPEC-008). Fails if there is none.
#
#   scripts/release-notes.sh v0.1.0
set -euo pipefail
v=${1:?usage: release-notes.sh VERSION}
v=${v#v}
cd "$(dirname "$0")/.."
notes=$(awk -v v="$v" '
  index($0, "## [" v "]") == 1 { on = 1; next }
  on && /^## \[/ { exit }
  on { print }
' Changes.md)
if [ -z "${notes//[[:space:]]/}" ]; then
  echo "release-notes: no section \"## [$v]\" in Changes.md" >&2
  exit 1
fi
printf '%s\n' "$notes"
