#!/bin/sh
# Install ohgo from a release tarball.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/PizenLabs/ohgo/main/install.sh | sh
#   OHGO_VERSION=vX.Y.Z sh install.sh
#   PREFIX=/usr/local sh install.sh
#
# With OHGO_VERSION unset the script resolves the latest stable release. It
# installs into $PREFIX/bin (default $HOME/.local/bin), verifies the published
# SHA-256 manifest, and smoke-tests the binary before replacing anything.
#
# The multi-language artifact is preferred because it can parse TypeScript,
# TSX, JavaScript, Java, C#, Rust, Kotlin, Ruby, C, and C++. Set
# OHGO_VARIANT=portable to install the Go-only static build instead.
#
# set -u fails on unset variables rather than expanding them empty: an empty
# PREFIX would install into /bin, which is the one mistake this script must
# not make silently.
set -eu

REPO="${OHGO_REPO:-PizenLabs/ohgo}"
VERSION="${OHGO_VERSION:-}"
PREFIX="${PREFIX:-$HOME/.local}"

# Download failures must not leave a partial file in the target directory.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

fetch() { # fetch URL DEST
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		echo "need curl or wget" >&2
		return 1
	fi
}

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
x86_64) ARCH="amd64" ;;
aarch64|arm64) ARCH="arm64" ;;
*)
	echo "unsupported architecture: $ARCH" >&2
	exit 1
	;;
esac
case "$OS" in
linux|darwin) ;;
*)
	echo "unsupported OS: $OS" >&2
	exit 1
	;;
esac

if [ -z "$VERSION" ]; then
	echo "resolving the latest release of $REPO"
	META="$TMP/release.json"
	fetch "https://api.github.com/repos/$REPO/releases/latest" "$META"
	# The API pretty-prints one field per line. A malformed or rate-limited
	# response yields no match and a clear instruction instead of a URL for a
	# release that does not exist.
	VERSION="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$META" | head -n 1)"
	if [ -z "$VERSION" ]; then
		echo "could not resolve the latest release; set OHGO_VERSION=vX.Y.Z" >&2
		exit 1
	fi
fi

BASE="https://github.com/$REPO/releases/download/$VERSION"
BASE_NAME="ohgo-$VERSION-$OS-$ARCH"

fetch "$BASE/sha256sums.txt" "$TMP/sha256sums.txt"

# The multi-language artifact compiles the tree-sitter adapters and is the
# capable default. The portable artifact remains the fallback for a release
# that predates it or for a caller that explicitly asks for it.
case "${OHGO_VARIANT:-auto}" in
auto)
	if awk -v name="$BASE_NAME-multilang.tar.gz" '$2 == name { found = 1 } END { exit !found }' "$TMP/sha256sums.txt"; then
		NAME="$BASE_NAME-multilang"
	else
		NAME="$BASE_NAME"
	fi
	;;
multilang)
	NAME="$BASE_NAME-multilang"
	;;
portable)
	NAME="$BASE_NAME"
	;;
*)
	echo "OHGO_VARIANT must be auto, multilang, or portable" >&2
	exit 1
	;;
esac

EXPECTED="$(awk -v name="$NAME.tar.gz" '$2 == name { print $1; exit }' "$TMP/sha256sums.txt")"
if [ -z "$EXPECTED" ]; then
	echo "sha256sums.txt has no entry for $NAME.tar.gz" >&2
	exit 1
fi

echo "downloading $BASE/$NAME.tar.gz"
fetch "$BASE/$NAME.tar.gz" "$TMP/$NAME.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
	ACTUAL="$(sha256sum "$TMP/$NAME.tar.gz" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
	ACTUAL="$(shasum -a 256 "$TMP/$NAME.tar.gz" | awk '{ print $1 }')"
else
	echo "need sha256sum or shasum to verify the download" >&2
	exit 1
fi
if [ "$ACTUAL" != "$EXPECTED" ]; then
	echo "checksum mismatch for $NAME.tar.gz" >&2
	exit 1
fi

tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"

# Smoke-test before touching an existing installation: an artifact that cannot
# answer with its own tag must leave the current binary in place.
if ! GOT="$("$TMP/$NAME/ohgo" version)"; then
	echo "downloaded binary cannot run on this system" >&2
	exit 1
fi
if [ "$GOT" != "ohgo $VERSION" ]; then
	echo "downloaded binary reports $GOT, want ohgo $VERSION" >&2
	exit 1
fi

BIN_DIR="$PREFIX/bin"
mkdir -p "$BIN_DIR"
install -m 755 "$TMP/$NAME/ohgo" "$BIN_DIR/ohgo"
echo "installed ohgo $VERSION to $BIN_DIR/ohgo"
"$BIN_DIR/ohgo" version
echo "next: ohgo init && ohgo doctor"
