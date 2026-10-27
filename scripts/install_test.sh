#!/usr/bin/env bash
#
# Deterministic tests for install.sh. No network, no real user settings.
#
# Usage: bash scripts/install_test.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALLER="$ROOT/install.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/ohgo-install-test.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT INT TERM
PASS=0
FAIL=0
VERSION="v9.9.9"

pass() { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1" >&2; }

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) ARCH="$(uname -m)" ;;
esac
NAME="ohgo-$VERSION-$OS-$ARCH"

make_release() {
  local rel="$1/download/$VERSION"
  mkdir -p "$rel"
  local dir="$WORK/pkg"
  mkdir -p "$dir/$NAME"
  cat >"$dir/$NAME/ohgo" <<'SH'
#!/bin/sh
case "$1" in
  version) echo "ohgo v9.9.9" ;;
  gry) printf '%s\n' "gry $*" >>"$OHGO_TEST_LOG" ;;
  *) : ;;
esac
exit 0
SH
  chmod 0755 "$dir/$NAME/ohgo"
  tar -czf "$rel/$NAME.tar.gz" -C "$dir" "$NAME"
  (
    cd "$rel"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$NAME.tar.gz" >sha256sums.txt
    else
      shasum -a 256 "$NAME.tar.gz" >sha256sums.txt
    fi
  )
}

run_install() {
  local home="$1"
  shift
  env HOME="$home" \
    PREFIX="${PREFIX_OVERRIDE:-$home/prefix}" \
    OHGO_RELEASES_URL="file://$WORK/rel" \
    OHGO_TEST_LOG="$home/calls.log" \
    PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
    sh "$INSTALLER" "$@"
}

make_release "$WORK/rel"

# --- fresh install -----------------------------------------------------------
H="$WORK/home-fresh"
mkdir -p "$H"
if run_install "$H" --version "$VERSION" >"$H/out" 2>"$H/err"; then
  if [ -x "$H/prefix/bin/ohgo" ] && [ "$("$H/prefix/bin/ohgo" version)" = "ohgo v9.9.9" ]; then
    pass "fresh install verifies and installs the release"
  else
    fail "fresh install binary missing"
  fi
else
  fail "fresh install failed: $(cat "$H/err")"
fi

# --- repeat ------------------------------------------------------------------
if run_install "$H" --version "$VERSION" >"$H/out2" 2>"$H/err2" &&
  grep -Fq "already installed" "$H/out2"; then
  pass "repeated install reuses the matching version"
else
  fail "repeated install failed: $(cat "$H/err2")"
fi

# --- dry run -----------------------------------------------------------------
H2="$WORK/home-dry"
mkdir -p "$H2"
if run_install "$H2" --version "$VERSION" --dry-run >"$H2/out" 2>"$H2/err"; then
  if [ ! -e "$H2/prefix/bin/ohgo" ] && grep -Fq "dry run" "$H2/out"; then
    pass "--dry-run changes nothing"
  else
    fail "--dry-run changed the prefix"
  fi
else
  fail "--dry-run failed: $(cat "$H2/err")"
fi

# --- checksum mismatch -------------------------------------------------------
H3="$WORK/home-badsum"
mkdir -p "$H3"
printf 'tampered\n' >>"$WORK/rel/download/$VERSION/$NAME.tar.gz"
if run_install "$H3" --version "$VERSION" >"$H3/out" 2>"$H3/err"; then
  fail "checksum mismatch must fail"
else
  if [ ! -e "$H3/prefix/bin/ohgo" ] && grep -Eqi 'sha-256|checksum|mismatch' "$H3/err"; then
    pass "checksum mismatch fails before installing"
  else
    fail "checksum failure left the prefix dirty"
  fi
fi
rm -rf "$WORK/rel"
make_release "$WORK/rel"

# --- archive traversal -------------------------------------------------------
H4="$WORK/home-traversal"
mkdir -p "$H4"
python3 - "$WORK/rel/download/$VERSION/$NAME.tar.gz" <<'PY'
import sys, tarfile
out = sys.argv[1]
with tarfile.open(out, "w:gz") as archive:
    data = b"#!/bin/sh\nexit 0\n"
    info = tarfile.TarInfo("../ohgo-evil")
    info.size = len(data)
    import io
    archive.addfile(info, io.BytesIO(data))
PY
(
  cd "$WORK/rel/download/$VERSION"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$NAME.tar.gz" >sha256sums.txt
  else
    shasum -a 256 "$NAME.tar.gz" >sha256sums.txt
  fi
)
if run_install "$H4" --version "$VERSION" >"$H4/out" 2>"$H4/err"; then
  fail "unsafe archive must fail"
else
  if [ ! -e "$WORK/ohgo-evil" ] && [ ! -e "$H4/prefix/bin/ohgo" ]; then
    pass "unsafe archive is rejected before extraction"
  else
    fail "unsafe archive escaped the extraction directory"
  fi
fi
rm -rf "$WORK/rel"
make_release "$WORK/rel"

# --- gry adapter opt-in ------------------------------------------------------
H5="$WORK/home-gry"
mkdir -p "$H5"
if run_install "$H5" --version "$VERSION" --gry-project demo --gry-binary /opt/gry >"$H5/out" 2>"$H5/err"; then
  if grep -Fq "gry enable --project demo --binary /opt/gry" "$H5/calls.log"; then
    pass "--gry-project configures the adapter explicitly"
  else
    fail "adapter command not forwarded: $(cat "$H5/calls.log" 2>/dev/null || true)"
  fi
else
  fail "gry adapter install failed: $(cat "$H5/err")"
fi

# --- spaces and Unicode ------------------------------------------------------
H6="$WORK/home space ü"
mkdir -p "$H6"
if PREFIX_OVERRIDE="$H6/bin space ü" run_install "$H6" --version "$VERSION" >"$H6/out" 2>"$H6/err"; then
  if [ -x "$H6/bin space ü/bin/ohgo" ]; then
    pass "prefix with spaces and Unicode works"
  else
    fail "space/Unicode prefix did not receive the binary"
  fi
else
  fail "space/Unicode prefix failed: $(cat "$H6/err")"
fi

# --- dev local build ---------------------------------------------------------
H7="$WORK/home-dev"
mkdir -p "$H7"
cp "$WORK/pkg/$NAME/ohgo" "$WORK/ohgo"
if (cd "$WORK" && HOME="$H7" PREFIX="$H7/prefix" PATH="/usr/bin:/bin" sh "$INSTALLER" --version dev >"$H7/out" 2>"$H7/err"); then
  if [ -x "$H7/prefix/bin/ohgo" ]; then
    pass "--version dev installs ./ohgo without a release download"
  else
    fail "dev install did not place the binary"
  fi
else
  fail "dev install failed: $(cat "$H7/err")"
fi

# --- unsupported architecture ------------------------------------------------
H8="$WORK/home-arch"
mkdir -p "$H8" "$WORK/fakebin"
cat >"$WORK/fakebin/uname" <<'SH'
#!/bin/sh
case "$1" in -s) echo Darwin ;; -m) echo riscv64 ;; *) echo riscv64 ;; esac
SH
chmod 0755 "$WORK/fakebin/uname"
if env HOME="$H8" PREFIX="$H8/prefix" PATH="$WORK/fakebin:/usr/bin:/bin" sh "$INSTALLER" --version "$VERSION" >"$H8/out" 2>"$H8/err"; then
  fail "unsupported architecture must fail"
else
  if grep -Fq "unsupported architecture" "$H8/err"; then
    pass "unsupported architecture fails clearly"
  else
    fail "unexpected unsupported-architecture error: $(cat "$H8/err")"
  fi
fi

# --- uninstall keeps settings -------------------------------------------------
H9="$WORK/home-uninstall"
mkdir -p "$H9"
run_install "$H9" --version "$VERSION" >"$H9/out" 2>"$H9/err" || true
if run_install "$H9" --uninstall >"$H9/out2" 2>"$H9/err2"; then
  if [ ! -e "$H9/prefix/bin/ohgo" ] && grep -Fq "settings" "$H9/out2"; then
    pass "--uninstall removes only the binary"
  else
    fail "--uninstall behaved unexpectedly"
  fi
else
  fail "--uninstall failed: $(cat "$H9/err2")"
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
