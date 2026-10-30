#!/bin/sh
# Build one release archive for the GOOS/GOARCH in the environment.
#
# This is the single implementation of the release artifact layout: the release
# workflow runs it once per matrix entry and `make release` loops over the same
# targets locally. Keep the target list aligned with Target.Supported in
# internal/update/release.go; an archive the updater rejects is not a release
# artifact.
#
# Usage:
#   GOOS=linux GOARCH=amd64 VERSION=vX.Y.Z ./scripts/release-target.sh
set -eu

: "${GOOS:?set GOOS}"
: "${GOARCH:?set GOARCH}"
: "${VERSION:?set VERSION}"

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

out=${OUT:-dist}
name="ohgo-${VERSION}-${GOOS}-${GOARCH}"

# A stale directory from an interrupted run must not leak into the archive.
rm -rf "$out/$name"
mkdir -p "$out/$name"

CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
	go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
	-o "$out/$name/ohgo" ./cmd/ohgo
cp README.md LICENSE "$out/$name/"
tar -czf "$out/$name.tar.gz" -C "$out" "$name"
rm -rf "$out/$name"
printf 'built %s\n' "$out/$name.tar.gz"
