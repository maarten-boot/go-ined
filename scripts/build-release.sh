#!/usr/bin/env bash
# Builds the release archives (SPEC-008). Each archive holds the binaries,
# LICENSE, README.md, Changes.md and examples/; SHA256SUMS lists the
# archives built.
#
#   scripts/build-release.sh VERSION [OUTDIR]
#       ined-server and inedctl without cgo for linux, darwin and windows on
#       amd64 and arm64, and ined-gui for linux/amd64 (cgo, needs the
#       X11/OpenGL dev packages). Used by the release workflow and the CI
#       build job.
#   scripts/build-release.sh --gui VERSION [OUTDIR [ARCH...]]
#       Only ined-gui, for the OS this runs on and the given architectures
#       (default: this machine's). Fyne needs cgo, so each OS builds its
#       own GUI (SPEC-007 v0.41): windows/amd64, darwin/arm64 and amd64
#       (clang on one macOS runner builds both), linux/arm64.
#
# OUTDIR defaults to dist and is emptied first.
set -euo pipefail
gui_only=false
if [ "${1:-}" = --gui ]; then
  gui_only=true
  shift
fi
version=${1:?usage: build-release.sh [--gui] VERSION [OUTDIR [ARCH...]]}
out=$(realpath -m "${2:-dist}")
shift $(($# < 2 ? $# : 2))
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
    if command -v zip >/dev/null; then
      (cd "$out/.work" && zip -qr "$out/$name.zip" "$name")
    else # Git Bash on a Windows runner has 7-Zip, not zip
      (cd "$out/.work" && 7z a -tzip -bd -bso0 "$out/$name.zip" "$name")
    fi
  else
    tar -C "$out/.work" -czf "$out/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
}

# gui OS ARCH: build and pack ined-gui (cgo) for OS/ARCH.
gui() {
  local os=$1 arch=$2 ext="" flags=$ldflags cc=${CC:-} bins="$out/.work/bin-gui-$1-$2"
  if [ "$os" = windows ]; then
    ext=.exe
    flags="$flags -H=windowsgui" # no console window
  elif [ "$os" = darwin ]; then
    cc="clang -arch arm64"
    [ "$arch" = amd64 ] && cc="clang -arch x86_64"
  fi
  mkdir -p "$bins"
  CC=$cc CGO_ENABLED=1 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$flags" -o "$bins/ined-gui$ext" ./cmd/ined-gui
  pack "go-ined-gui-$version-$os-$arch" "$os" "$bins"
  rm -rf "$bins"
}

if $gui_only; then
  os=$(go env GOOS)
  archs=("$@")
  [ ${#archs[@]} -gt 0 ] || archs=("$(go env GOARCH)")
  for arch in "${archs[@]}"; do
    gui "$os" "$arch"
  done
else
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
  gui linux amd64
fi
rm -rf "$out/.work"

(cd "$out" && shopt -s nullglob && sha256sum -- *.tar.gz *.zip >SHA256SUMS)
echo "built $(ls "$out" | wc -l) files in $out"
