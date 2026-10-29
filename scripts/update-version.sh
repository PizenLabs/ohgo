#!/bin/sh
# Propagate the version in VERSION to every file that embeds it.
#
# Usage:
#   ./scripts/update-version.sh                 # sync the tree with VERSION
#   ./scripts/update-version.sh 0.0.4           # write VERSION=0.0.4, then sync
#   ./scripts/update-version.sh --bump patch    # bump VERSION, then sync
#   ./scripts/update-version.sh --release       # cut the release records, then sync
#   ./scripts/update-version.sh --check         # exit 1 when the tree is stale
#
# The logic lives in scripts/updateversion so it is covered by go test; this
# wrapper exists so a maintainer never has to remember the go run path.
#
# It builds a temporary binary instead of using `go run`: go run collapses every
# non-zero exit into 1, and --check must stay distinguishable from a real error
# (1 for drift, 2 for a usage or I/O failure).
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

go build -o "$tmp/update-version" ./scripts/updateversion
"$tmp/update-version" "$@"
