#!/usr/bin/env bash
# Builds the release archives (SPEC-008): ined-server and inedctl without
# cgo for linux, darwin and windows on amd64 and arm64, and ined-gui for
# linux/amd64 (cgo, needs the X11/OpenGL dev packages). Each archive holds
# the binaries, LICENSE, README.md, Changes.md and examples/. Writes
# SHA256SUMS. Used by the release workflow and the CI build job.
#
#   scripts/build-release.sh VERSION [OUTDIR]     (OUTDIR default: dist)
set -euo pipefail
version=${1:?usage: build-release.sh VERSION [OUTDIR]}
out=$(realpath -m "${2:-dist}")
cd "$(dirname "$0")/.."
rm -rf "$out"
mkdir -p "$out/.work"
ldflags="-s -w -X github.com/maarten-boot/go-ined/internal/version.Version=$version"

# pack NAME OS DIR: archive the binaries in DIR with the docs.
pack() {
  local name=$1 os=$2 bins=$3 stage="$out/.work/$1"
  mkdir -p "$stage"
  cp "$bins"/* LICENSE README.md Changes.md "$stage/"
  cp -r examples "$stage/"
  if [ "$os" = windows ]; then
    (cd "$out/.work" && zip -qr "$out/$name.zip" "$name")
  else
    tar -C "$out/.work" -czf "$out/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
}

for os in linux darwin windows; do
  for arch in amd64 arm64; do
    ext=""
    [ "$os" = windows ] && ext=.exe
    bins="$out/.work/bin-$os-$arch"
    mkdir -p "$bins"
    for cmd in ined-server inedctl; do
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$bins/$cmd$ext" "./cmd/$cmd"
    done
    pack "go-ined-$version-$os-$arch" "$os" "$bins"
    rm -rf "$bins"
  done
done

bins="$out/.work/bin-gui"
mkdir -p "$bins"
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$ldflags" -o "$bins/ined-gui" ./cmd/ined-gui
pack "go-ined-gui-$version-linux-amd64" linux "$bins"
rm -rf "$out/.work"

(cd "$out" && sha256sum -- *.tar.gz *.zip >SHA256SUMS)
echo "built $(ls "$out" | wc -l) files in $out"
