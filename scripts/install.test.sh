#!/usr/bin/env bash
# The test looks for literal "~/.bashrc" and '$HOME/bin' text in the script's
# messages, so unexpanded tildes and dollars are the point here.
# shellcheck disable=SC2088,SC2016
#
# Hermetic tests for install.sh, the bootstrap script behind
# `curl -fsSL .../install.sh | sh`. The script runs under `sh` (dash on Debian
# and Ubuntu) with a scratch HOME and TMPDIR per case, and a PATH whose first
# folder holds stand-ins for curl, uname, id and codesign. The stand-in curl
# serves a fake release from fixture folders this test builds and logs every
# URL it is asked for; the archives hold a stand-in afk that logs its arguments
# and the first line of its stdin, so the test can tell whether `afk install`
# read from the file given as AFK_TTY. Nothing touches the real ~/bin or GitHub.
#
# Load-bearing rows: linux amd64 (right archive, binary placed, afk install fed
# from AFK_TTY, the latest release looked up), the darwin arm64 pair (codesign
# only when the first run fails, and never otherwise), the refusals that must
# write nothing (unknown architecture, checksum mismatch and a missing checksum
# line - the stand-in afk never ran - and root, which must not even call curl),
# AFK_VERSION skipping the lookup, AFK_NO_INSTALL stopping before afk install,
# the PATH hint, and the leftover sweep (only afk in ~/bin, TMPDIR empty).
set -u
SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/install.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/afk-install-script-test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT
pass=0 fail=0

if [ ! -f "$SCRIPT" ]; then
  echo "FAIL: $SCRIPT does not exist"
  exit 1
fi

check_cond() {
  local desc="$1"
  shift
  if "$@"; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc"
  fi
}

check_exit() {
  local desc="$1" expected="$2" actual="$3" out="${4:-}"
  if [ "$actual" = "$expected" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc (expected exit $expected, got $actual)"
    [ -n "$out" ] && echo "  output: $out"
  fi
  return 0
}

check_contains() {
  local desc="$1" haystack="$2" needle="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc"; echo "  wanted: $needle"; echo "  in:     $haystack"
  fi
}

check_absent() {
  local desc="$1" haystack="$2" needle="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc"; echo "  unwanted: $needle"; echo "  in:       $haystack"
  fi
}

hash_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# --- stand-ins -------------------------------------------------------------

STUBS="$ROOT/stubs"
RELEASES="$ROOT/releases"
mkdir -p "$STUBS" "$RELEASES"

# curl: -w '%{url_effective}' answers FAKE_LATEST_URL (or fails when it is
# empty, as for "no release yet"); a download maps
# .../releases/download/<tag>/<file> to $RELEASES/<tag>/<file> and fails like
# `curl -f` on a 404 when the fixture is missing.
cat > "$STUBS/curl" <<'EOF'
#!/bin/sh
out="" fmt="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    -w) fmt=$2; shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf '%s\n' "$url" >> "$AFK_TEST_LOG/curl.log"
case "$fmt" in
  *url_effective*)
    [ -n "${FAKE_LATEST_URL:-}" ] || exit 6
    printf '%s' "$FAKE_LATEST_URL"
    exit 0 ;;
esac
case "$url" in
  https://github.com/dphaener/afk/releases/download/*) rel=${url#https://github.com/dphaener/afk/releases/download/} ;;
  *) exit 22 ;;
esac
[ -f "$FAKE_RELEASES/$rel" ] || exit 22
cp "$FAKE_RELEASES/$rel" "$out"
EOF

cat > "$STUBS/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' "$FAKE_OS" ;;
  -m) printf '%s\n' "$FAKE_ARCH" ;;
  *) exit 1 ;;
esac
EOF

cat > "$STUBS/id" <<'EOF'
#!/bin/sh
[ "$1" = "-u" ] || exit 1
printf '%s\n' "$FAKE_UID"
EOF

# codesign: logs its call and leaves the mark the stand-in afk looks for when
# FAKE_NEEDS_SIGN=1, standing in for "this binary only runs once signed".
cat > "$STUBS/codesign" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$AFK_TEST_LOG/codesign.log"
: > "$AFK_TEST_LOG/signed"
EOF

chmod 755 "$STUBS"/*

# The afk inside every archive. `version` fails with 137 (what the macOS
# kernel's kill looks like) until codesign has run when FAKE_NEEDS_SIGN=1, and
# `install` logs the first line of its stdin so the test can see it came from
# the AFK_TTY file.
AFK_STANDIN="$ROOT/afk-standin"
cat > "$AFK_STANDIN" <<'EOF'
#!/bin/sh
printf 'args=%s\n' "$*" >> "$AFK_TEST_LOG/afk.log"
case "$1" in
  version)
    if [ "${FAKE_NEEDS_SIGN:-}" = 1 ] && [ ! -e "$AFK_TEST_LOG/signed" ]; then
      exit 137
    fi
    echo "afk FAKE-VERSION-LINE" ;;
  install)
    line=""
    IFS= read -r line || true
    printf 'stdin=%s\n' "$line" >> "$AFK_TEST_LOG/afk.log"
    exit "${FAKE_INSTALL_EXIT:-0}" ;;
esac
EOF
chmod 755 "$AFK_STANDIN"

# make_release <tag> builds every platform's archive under $RELEASES/<tag> and
# a matching checksums.txt in the release's own format.
make_release() {
  local tag="$1" version="${1#v}" dir="$RELEASES/$1" stage
  mkdir -p "$dir"
  for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
    stage="$ROOT/stage-$tag-$target"
    mkdir -p "$stage"
    cp "$AFK_STANDIN" "$stage/afk"
    echo "readme" > "$stage/README.md"
    tar -czf "$dir/afk_${version}_${target}.tar.gz" -C "$stage" afk README.md
  done
  (cd "$dir" && for f in afk_*.tar.gz; do printf '%s  %s\n' "$(hash_of "$f")" "$f"; done > checksums.txt)
}

make_release v9.9.9
make_release v1.2.3
# v6.6.6: the linux_amd64 line holds the wrong hash. v6.6.7: that line is gone.
make_release v6.6.6
sed -i.bak 's/^[0-9a-f]*\(  afk_6.6.6_linux_amd64\.tar\.gz\)$/0000000000000000000000000000000000000000000000000000000000000000\1/' "$RELEASES/v6.6.6/checksums.txt"
make_release v6.6.7
sed -i.bak '/afk_6.6.7_linux_amd64/d' "$RELEASES/v6.6.7/checksums.txt"
# v5.5.5: a "*name" (binary mode) checksum line must still match.
make_release v5.5.5
sed -i.bak 's/  afk_/ *afk_/' "$RELEASES/v5.5.5/checksums.txt"

LATEST="https://github.com/dphaener/afk/releases/tag/v9.9.9"
BASE_PATH="$STUBS:$PATH"

# run_install <case> [VAR=value...] runs install.sh under sh in its own
# scratch HOME/TMPDIR/log folder and sets OUT, CODE, H (the HOME), LOG and TMP.
run_install() {
  local name="$1"
  shift
  H="$ROOT/$name/home" LOG="$ROOT/$name/log" TMP="$ROOT/$name/tmp"
  mkdir -p "$H" "$LOG" "$TMP"
  echo "tty-token-$name" > "$ROOT/$name/tty"
  # env stops reading options at the first NAME=value, so the -u flags lead;
  # a later NAME=value from the caller overrides an earlier default.
  OUT=$(env -u AFK_VERSION -u AFK_NO_INSTALL HOME="$H" TMPDIR="$TMP" PATH="$BASE_PATH" \
    AFK_TEST_LOG="$LOG" FAKE_RELEASES="$RELEASES" FAKE_LATEST_URL="$LATEST" \
    FAKE_OS=Linux FAKE_ARCH=x86_64 FAKE_UID=1000 AFK_TTY="$ROOT/$name/tty" \
    "$@" sh "$SCRIPT" </dev/null 2>&1)
  CODE=$?
}

logof() { cat "$LOG/$1" 2>/dev/null; }
bin_listing() { ls -A "$H/bin" 2>/dev/null; }
tmp_empty() { [ -z "$(ls -A "$TMP")" ]; }
no_afk() { [ ! -e "$H/bin/afk" ] && [ -z "$(bin_listing)" ]; }
standin_never_ran() { [ ! -e "$LOG/afk.log" ]; }

# --- rows --------------------------------------------------------------------

run_install linux-amd64
check_exit "linux amd64: exits 0" 0 "$CODE" "$OUT"
check_cond "linux amd64: afk placed in \$HOME/bin" test -x "$H/bin/afk"
check_contains "linux amd64: latest release looked up" "$(logof curl.log)" "https://github.com/dphaener/afk/releases/latest"
check_contains "linux amd64: linux_amd64 archive fetched" "$(logof curl.log)" "/releases/download/v9.9.9/afk_9.9.9_linux_amd64.tar.gz"
check_contains "linux amd64: checksums fetched" "$(logof curl.log)" "/releases/download/v9.9.9/checksums.txt"
check_contains "linux amd64: afk install called" "$(logof afk.log)" "args=install"
check_contains "linux amd64: afk install read from AFK_TTY" "$(logof afk.log)" "stdin=tty-token-linux-amd64"
check_contains "linux amd64: version line printed" "$OUT" "afk FAKE-VERSION-LINE"
check_contains "linux amd64: messages prefixed" "$OUT" "afk-install: "
check_absent "linux amd64: codesign never called" "$(logof codesign.log)" "sign"
check_cond "linux amd64: only afk left in \$HOME/bin" test "$(bin_listing)" = "afk"
check_cond "linux amd64: temporary folder removed" tmp_empty

run_install darwin-arm64-signs FAKE_OS=Darwin FAKE_ARCH=arm64 FAKE_NEEDS_SIGN=1
check_exit "darwin arm64 needing a signature: exits 0" 0 "$CODE" "$OUT"
check_contains "darwin arm64 needing a signature: darwin_arm64 archive fetched" "$(logof curl.log)" "afk_9.9.9_darwin_arm64.tar.gz"
check_contains "darwin arm64 needing a signature: codesign called on ~/bin/afk" "$(logof codesign.log)" "--force --sign - $H/bin/afk"
check_contains "darwin arm64 needing a signature: afk install called" "$(logof afk.log)" "stdin=tty-token-darwin-arm64-signs"
check_cond "darwin arm64 needing a signature: only afk left in \$HOME/bin" test "$(bin_listing)" = "afk"

run_install darwin-arm64-runs FAKE_OS=Darwin FAKE_ARCH=arm64
check_exit "darwin arm64 already running: exits 0" 0 "$CODE" "$OUT"
check_contains "darwin arm64 already running: darwin_arm64 archive fetched" "$(logof curl.log)" "afk_9.9.9_darwin_arm64.tar.gz"
check_cond "darwin arm64 already running: codesign never called" test ! -e "$LOG/codesign.log"
check_contains "darwin arm64 already running: PATH hint names ~/.zshrc" "$OUT" "~/.zshrc"

# A binary that still fails after signing (this codesign leaves no mark): stop,
# and say what to run by hand.
mkdir -p "$ROOT/nosign"
printf '#!/bin/sh\nexit 0\n' > "$ROOT/nosign/codesign"; chmod 755 "$ROOT/nosign/codesign"
run_install darwin-never-runs FAKE_OS=Darwin FAKE_ARCH=arm64 FAKE_NEEDS_SIGN=1 PATH="$ROOT/nosign:$BASE_PATH"
check_exit "darwin binary that never runs: exits 1" 1 "$CODE" "$OUT"
check_contains "darwin binary that never runs: codesign command to try" "$OUT" "codesign --force --sign -"
check_absent "darwin binary that never runs: afk install not called" "$(logof afk.log)" "args=install"

run_install unknown-arch FAKE_ARCH=riscv64
check_exit "unknown architecture: exits 1" 1 "$CODE" "$OUT"
check_contains "unknown architecture: names the supported ones" "$OUT" "Linux and macOS, on x86_64 (amd64) and arm64"
check_contains "unknown architecture: points at building from source" "$OUT" "docs/install.md"
check_cond "unknown architecture: nothing in \$HOME/bin" no_afk

run_install unknown-os FAKE_OS=FreeBSD
check_exit "unknown OS: exits 1" 1 "$CODE" "$OUT"
check_cond "unknown OS: nothing in \$HOME/bin" no_afk

run_install mismatch AFK_VERSION=6.6.6
check_exit "checksum mismatch: exits 1" 1 "$CODE" "$OUT"
check_contains "checksum mismatch: says so" "$OUT" "checksum did not match, nothing was installed"
check_cond "checksum mismatch: \$HOME/bin absent or empty" no_afk
check_cond "checksum mismatch: stand-in afk never ran" standin_never_ran
check_cond "checksum mismatch: temporary folder removed" tmp_empty

run_install missing-line AFK_VERSION=6.6.7
check_exit "missing checksum line: exits 1" 1 "$CODE" "$OUT"
check_contains "missing checksum line: says the checksum did not match" "$OUT" "checksum did not match"
check_cond "missing checksum line: \$HOME/bin absent or empty" no_afk
check_cond "missing checksum line: stand-in afk never ran" standin_never_ran

run_install star-line AFK_VERSION=5.5.5 AFK_NO_INSTALL=1
check_exit "binary-mode (*name) checksum line: exits 0" 0 "$CODE" "$OUT"
check_cond "binary-mode (*name) checksum line: afk placed" test -x "$H/bin/afk"

run_install root FAKE_UID=0
check_exit "root: exits 1" 1 "$CODE" "$OUT"
check_contains "root: refused as a normal-user program" "$OUT" "afk runs as a normal user"
check_contains "root: says how to make a user" "$OUT" "adduser"
check_cond "root: no curl call" test ! -e "$LOG/curl.log"
check_cond "root: nothing written to \$HOME" test -z "$(ls -A "$H")"

run_install pinned AFK_VERSION=v1.2.3
check_exit "AFK_VERSION=v1.2.3: exits 0" 0 "$CODE" "$OUT"
check_absent "AFK_VERSION=v1.2.3: no latest lookup" "$(logof curl.log)" "/releases/latest"
check_contains "AFK_VERSION=v1.2.3: the 1.2.3 archive fetched" "$(logof curl.log)" "/releases/download/v1.2.3/afk_1.2.3_linux_amd64.tar.gz"

run_install pinned-bare AFK_VERSION=1.2.3 AFK_NO_INSTALL=1
check_contains "AFK_VERSION=1.2.3: the 1.2.3 archive fetched" "$(logof curl.log)" "/releases/download/v1.2.3/afk_1.2.3_linux_amd64.tar.gz"

run_install bad-version AFK_VERSION='1.2.3/../x'
check_exit "AFK_VERSION that is not a version: exits 1" 1 "$CODE" "$OUT"
check_cond "AFK_VERSION that is not a version: no curl call" test ! -e "$LOG/curl.log"

run_install no-install AFK_NO_INSTALL=1
check_exit "AFK_NO_INSTALL=1: exits 0" 0 "$CODE" "$OUT"
check_cond "AFK_NO_INSTALL=1: binary placed" test -x "$H/bin/afk"
check_absent "AFK_NO_INSTALL=1: afk install never called" "$(logof afk.log)" "args=install"
check_contains "AFK_NO_INSTALL=1: names the next step" "$OUT" "bin/afk install"

run_install path-hint
check_contains "~/bin not on PATH: export hint printed" "$OUT" 'export PATH="$HOME/bin:$PATH"'
check_contains "~/bin not on PATH: names ~/.bashrc on Linux" "$OUT" "~/.bashrc"

run_install on-path PATH="$ROOT/on-path/home/bin:$BASE_PATH"
check_exit "~/bin on PATH: exits 0" 0 "$CODE" "$OUT"
check_absent "~/bin on PATH: no export hint" "$OUT" "export PATH="

run_install no-release FAKE_LATEST_URL="https://github.com/dphaener/afk/releases"
check_exit "no release yet: exits 1" 1 "$CODE" "$OUT"
check_contains "no release yet: says it found none" "$OUT" "could not find the latest afk release"
check_cond "no release yet: nothing in \$HOME/bin" no_afk

run_install offline FAKE_LATEST_URL=""
check_exit "network down: exits 1" 1 "$CODE" "$OUT"
check_cond "network down: nothing in \$HOME/bin" no_afk

run_install missing-archive AFK_VERSION=7.7.7
check_exit "download fails: exits 1" 1 "$CODE" "$OUT"
check_contains "download fails: names the URL" "$OUT" "https://github.com/dphaener/afk/releases/download/v7.7.7/afk_7.7.7_linux_amd64.tar.gz"
check_cond "download fails: nothing in \$HOME/bin" no_afk
check_cond "download fails: temporary folder removed" tmp_empty

run_install no-tty AFK_TTY="$ROOT/no-such-tty"
check_exit "no terminal: exits 0" 0 "$CODE" "$OUT"
check_contains "no terminal: says to run afk install from a terminal" "$OUT" "bin/afk install' from a terminal"
check_absent "no terminal: afk install not called" "$(logof afk.log)" "args=install"
check_cond "no terminal: binary placed" test -x "$H/bin/afk"

run_install install-fails FAKE_INSTALL_EXIT=3
check_exit "afk install's exit status passed through" 3 "$CODE" "$OUT"

# Replacing an existing afk: the rename leaves no temporary file behind.
run_install replace AFK_NO_INSTALL=1
echo old > "$H/bin/afk"
OUT=$(env -u AFK_VERSION HOME="$H" TMPDIR="$TMP" PATH="$BASE_PATH" AFK_TEST_LOG="$LOG" FAKE_RELEASES="$RELEASES" \
  FAKE_LATEST_URL="$LATEST" FAKE_OS=Linux FAKE_ARCH=x86_64 FAKE_UID=1000 AFK_NO_INSTALL=1 sh "$SCRIPT" </dev/null 2>&1)
check_exit "existing afk replaced: exits 0" 0 "$?" "$OUT"
check_cond "existing afk replaced: new binary in place" cmp -s "$H/bin/afk" "$AFK_STANDIN"
check_cond "existing afk replaced: no leftover temporary file" test "$(bin_listing)" = "afk"

echo
echo "pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
