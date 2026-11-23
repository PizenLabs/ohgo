#!/bin/sh
# Build one release archive for the GOOS/GOARCH in the environment.
#
# This is the single implementation of the release artifact layout: the release
# workflow runs it once per matrix entry and `make release` loops over the same
# targets locally. Keep the target list aligned with Target.Supported in
# internal/update/release.go; an archive the updater rejects is not a release
# artifact.
#
# VARIANT selects the structural adapter set:
#   portable   CGO disabled; Go adapter only; runs without libc or a toolchain.
#   multilang  CGO enabled; the pinned tree-sitter grammars compile in.
#
# Both variants are published. The portable one stays the universal fallback;
# the multi-language one is built natively on each target platform.
#
# Usage:
#   GOOS=linux GOARCH=amd64 VERSION=vX.Y.Z ./scripts/release-target.sh
#   GOOS=linux GOARCH=amd64 VERSION=vX.Y.Z VARIANT=multilang ./scripts/release-target.sh
set -eu

: "${GOOS:?set GOOS}"
: "${GOARCH:?set GOARCH}"
: "${VERSION:?set VERSION}"

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

out=${OUT:-dist}
variant=${VARIANT:-portable}
case "$variant" in
portable)
	suffix=""
	cgo=0
	tags=""
	extldflags=""
	;;
multilang)
	suffix="-multilang"
	cgo=1
	tags=""
	extldflags=""
	;;
*)
	echo "VARIANT must be portable or multilang, got $variant" >&2
	exit 2
	;;
esac

# A multi-language Linux artifact must not depend on the build host's glibc:
# netgo and osusergo keep DNS and user lookup inside Go, and static linking
# removes the libc dependency entirely. macOS has no static libSystem, so the
# system library is the platform baseline there.
if [ "$variant" = "multilang" ] && [ "$GOOS" = "linux" ]; then
	tags="netgo,osusergo"
	extldflags='-linkmode external -extldflags "-static"'
fi

name="ohgo-${VERSION}-${GOOS}-${GOARCH}${suffix}"

# A stale directory from an interrupted run must not leak into the archive.
rm -rf "$out/$name"
mkdir -p "$out/$name"

CGO_ENABLED=$cgo GOOS="$GOOS" GOARCH="$GOARCH" \
	go build -trimpath -tags "$tags" \
	-ldflags "-s -w -X main.version=${VERSION} ${extldflags}" \
	-o "$out/$name/ohgo" ./cmd/ohgo
cp README.md LICENSE "$out/$name/"
tar -czf "$out/$name.tar.gz" -C "$out" "$name"
rm -rf "$out/$name"
printf 'built %s\n' "$out/$name.tar.gz"
