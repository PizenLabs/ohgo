#!/bin/sh
# Install ohgo from a release tarball.
#
# Canonical install (the release asset is published by the tag workflow):
#   OHGO_VERSION=v0.1.0 sh install.sh
#
# Usage:
#   sh install.sh --version v0.1.0 [--prefix DIR]
#   OHGO_VERSION=v0.1.0 sh install.sh
#   OHGO_VERSION=dev sh install.sh          # install ./ohgo from a local build
#
# Environment:
#   OHGO_VERSION        Release tag to install (or "dev" for ./ohgo).
#   PREFIX              Install prefix; the binary lands in $PREFIX/bin.
#   OHGO_REPO           GitHub owner/repo (default: PizenLabs/ohgo).
#   OHGO_RELEASES_URL   Release base URL override for mirrors and tests.
#   OHGO_SKIP_CHECKSUM  Not supported. Integrity verification is mandatory
#                       for release installs and never disabled by a flag.
#
# Flags:
#   --version TAG       Release tag (same as OHGO_VERSION).
#   --prefix DIR        Install prefix (same as PREFIX).
#   --gry-project NAME  After install, run `ohgo gry enable --project NAME`.
#   --gry-binary PATH   GRY binary for the adapter (default: resolve `gry`).
#   --dry-run           Print the plan and exit without changing anything.
#   --uninstall         Remove $PREFIX/bin/ohgo. Settings are preserved.
#   -h, --help          Show this help.
#
# set -u fails on unset variables rather than expanding them empty: an empty
# PREFIX would install into /bin, which is the one mistake this script must
# not make silently.
set -eu

REPO="${OHGO_REPO:-PizenLabs/ohgo}"
RELEASES_URL="${OHGO_RELEASES_URL:-https://github.com/$REPO/releases}"
VERSION="${OHGO_VERSION:-}"
PREFIX="${PREFIX:-/usr/local}"
DRY_RUN=0
UNINSTALL=0
GRY_PROJECT=""
GRY_BINARY=""

usage() {
	cat <<'EOF'
Install ohgo from a release tarball.

Usage:
  sh install.sh [options]

Options:
  --version <tag>       Release tag, e.g. v0.1.0 (or "dev" for ./ohgo)
  --prefix <dir>        Install prefix; binary goes to <dir>/bin (default: /usr/local)
  --gry-project <name>  Enable the opt-in GRY evidence adapter after install
  --gry-binary <path>   GRY binary for the adapter (default: resolve gry on PATH)
  --dry-run             Print the plan; change nothing
  --uninstall           Remove <prefix>/bin/ohgo (does not touch settings)
  -h, --help            Show this help

Environment: OHGO_VERSION, PREFIX, OHGO_REPO, OHGO_RELEASES_URL

Release installs verify sha256sums.txt from the same release. There is no
--insecure flag: a mirror must publish matching checksums.
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--version)
			[ "$#" -ge 2 ] || { echo "--version requires a value" >&2; exit 1; }
			VERSION="$2"
			shift 2
			;;
		--version=*) VERSION="${1#*=}"; shift ;;
		--prefix)
			[ "$#" -ge 2 ] || { echo "--prefix requires a value" >&2; exit 1; }
			PREFIX="$2"
			shift 2
			;;
		--prefix=*) PREFIX="${1#*=}"; shift ;;
		--gry-project)
			[ "$#" -ge 2 ] || { echo "--gry-project requires a value" >&2; exit 1; }
			GRY_PROJECT="$2"
			shift 2
			;;
		--gry-project=*) GRY_PROJECT="${1#*=}"; shift ;;
		--gry-binary)
			[ "$#" -ge 2 ] || { echo "--gry-binary requires a value" >&2; exit 1; }
			GRY_BINARY="$2"
			shift 2
			;;
		--gry-binary=*) GRY_BINARY="${1#*=}"; shift ;;
		--dry-run) DRY_RUN=1; shift ;;
		--uninstall) UNINSTALL=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) echo "unknown option: $1 (try --help)" >&2; exit 1 ;;
	esac
done

if [ "$UNINSTALL" -eq 0 ] && [ -z "$VERSION" ]; then
	echo "OHGO_VERSION is required, e.g. OHGO_VERSION=v0.1.0 sh install.sh" >&2
	exit 1
fi

case "$PREFIX" in
	"") echo "PREFIX must not be empty" >&2; exit 1 ;;
	"~") PREFIX="$HOME" ;;
	"~/"*) PREFIX="$HOME/${PREFIX#~/}" ;;
esac
while [ "${PREFIX%/}" != "$PREFIX" ]; do PREFIX="${PREFIX%/}"; done
BIN_DIR="$PREFIX/bin"
BIN="$BIN_DIR/ohgo"

if [ "$UNINSTALL" -eq 1 ]; then
	if [ "$DRY_RUN" -eq 1 ]; then
		echo "would remove $BIN"
	elif [ -e "$BIN" ]; then
		rm -f "$BIN"
		echo "removed $BIN"
	else
		echo "$BIN is not installed"
	fi
	echo "ohgo settings in ~/.ohgo were not touched"
	exit 0
fi

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
	x86_64|amd64) ARCH="amd64" ;;
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
URL="$RELEASES_URL/download/$VERSION/$NAME.tar.gz"
SUMS_URL="$RELEASES_URL/download/$VERSION/sha256sums.txt"

if [ "$DRY_RUN" -eq 1 ]; then
	echo "dry run: install ohgo $VERSION for $OS/$ARCH"
	echo "dry run: binary $BIN"
	if [ "$VERSION" != "dev" ]; then
		echo "dry run: download $URL"
		echo "dry run: verify $SUMS_URL"
	fi
	if [ -n "$GRY_PROJECT" ]; then
		echo "dry run: ohgo gry enable --project $GRY_PROJECT"
	fi
	exit 0
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/ohgo-install.XXXXXX")"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT INT TERM HUP

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		echo "sha256sum or shasum is required to verify the download" >&2
		exit 1
	fi
}

fetch() {
	url="$1"
	dest="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 15 --max-time 1800 -o "$dest" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$dest" "$url"
	else
		echo "need curl or wget" >&2
		exit 1
	fi
}

install_binary() {
	src="$1"
	[ -f "$src" ] && [ ! -L "$src" ] || { echo "binary is not a regular file: $src" >&2; exit 1; }
	mkdir -p "$BIN_DIR"
	[ -w "$BIN_DIR" ] || { echo "cannot write to $BIN_DIR; re-run with sudo or PREFIX=\"\$HOME/.local\"" >&2; exit 1; }
	tmp="$BIN_DIR/.ohgo.tmp.$$"
	if ! cp "$src" "$tmp"; then
		rm -f "$tmp"
		echo "cannot copy ohgo into $BIN_DIR" >&2
		exit 1
	fi
	if ! chmod 0755 "$tmp" || ! mv -f "$tmp" "$BIN"; then
		rm -f "$tmp"
		echo "cannot install ohgo into $BIN_DIR" >&2
		exit 1
	fi
}

if [ "$VERSION" = "dev" ]; then
	if [ ! -f "./ohgo" ]; then
		echo "OHGO_VERSION=dev installs ./ohgo, but it does not exist; run make build first" >&2
		exit 1
	fi
	echo "installing local build ./ohgo"
	install_binary "./ohgo"
else
	# Reuse an already installed matching version instead of downloading it
	# again. A different tag always reinstalls.
	skip=0
	if [ -x "$BIN" ]; then
		installed="$("$BIN" version 2>/dev/null | awk '{print $2}' | head -n 1 || true)"
		if [ "$installed" = "$VERSION" ]; then
			echo "ohgo $VERSION is already installed at $BIN"
			skip=1
		fi
	fi
	if [ "$skip" -eq 0 ]; then
		echo "downloading $URL"
		fetch "$URL" "$TMP/$NAME.tar.gz"
		echo "verifying $SUMS_URL"
		fetch "$SUMS_URL" "$TMP/sha256sums.txt"
		expected="$(awk -v asset="$NAME.tar.gz" '$2 == asset || $2 == "./" asset {print $1}' "$TMP/sha256sums.txt")"
		[ -n "$expected" ] || { echo "sha256sums.txt does not list $NAME.tar.gz" >&2; exit 1; }
		actual="$(sha256_of "$TMP/$NAME.tar.gz")"
		if [ "$expected" != "$actual" ]; then
			echo "SHA-256 mismatch for $NAME.tar.gz (expected $expected, got $actual)" >&2
			exit 1
		fi
		# Archives are untrusted input even from the project's own release.
		if tar -tzf "$TMP/$NAME.tar.gz" | grep -Eq '(^/|(^|/)\.\.($|/))'; then
			echo "refusing to extract $NAME.tar.gz: archive contains an unsafe path" >&2
			exit 1
		fi
		tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP"
		install_binary "$TMP/$NAME/ohgo"
	fi
fi

echo "installed ohgo $VERSION to $BIN"
"$BIN" version

STATUS=0
if [ -n "$GRY_PROJECT" ]; then
	echo "enabling GRY evidence adapter for project $GRY_PROJECT"
	if [ -n "$GRY_BINARY" ]; then
		"$BIN" gry enable --project "$GRY_PROJECT" --binary "$GRY_BINARY" || STATUS=1
	else
		"$BIN" gry enable --project "$GRY_PROJECT" || STATUS=1
	fi
fi

echo "next: ohgo init && ohgo doctor"
if [ "$STATUS" -ne 0 ]; then
	echo "ohgo was installed, but the GRY adapter was not enabled; run: ohgo gry enable --project <name>" >&2
	exit 1
fi
