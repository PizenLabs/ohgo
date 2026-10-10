#!/bin/sh
# Install ohgo from a release tarball.
#
# Usage:
#   OHGO_VERSION=v0.1.0 sh install.sh
#   OHGO_VERSION=dev PREFIX=$HOME/.local sh install.sh   # local build install
#
# set -u fails on unset variables rather than expanding them empty: an empty
# PREFIX would install into /bin, which is the one mistake this script must
# not make silently.
set -eu

REPO="${OHGO_REPO:-PizenLabs/ohgo}"
VERSION="${OHGO_VERSION:-}"
PREFIX="${PREFIX:-/usr/local}"

if [ -z "$VERSION" ]; then
	echo "OHGO_VERSION is required, e.g. OHGO_VERSION=v0.1.0 sh install.sh" >&2
	exit 1
fi

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

NAME="ohgo-$VERSION-$OS-$ARCH"
URL="https://github.com/$REPO/releases/download/$VERSION/$NAME.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

echo "downloading $URL"
if command -v curl >/dev/null 2>&1; then
	curl -fsSL -o "$TMP/$NAME.tar.gz" "$URL"
elif command -v wget >/dev/null 2>&1; then
	wget -qO "$TMP/$NAME.tar.gz" "$URL"
else
	echo "need curl or wget" >&2
	exit 1
fi

tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
BIN_DIR="$PREFIX/bin"
mkdir -p "$BIN_DIR"
install -m 755 "$TMP/$NAME/ohgo" "$BIN_DIR/ohgo"
echo "installed ohgo $VERSION to $BIN_DIR/ohgo"
"$BIN_DIR/ohgo" version
echo "next: ohgo init && ohgo doctor"
