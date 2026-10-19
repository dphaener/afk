#!/usr/bin/env bash
# Hermetic tests for release.sh. Each case builds a scratch git repository
# holding a copy of the script and a tiny Go program that prints main.version,
# so the real afk tree is never built or touched.
#
# Load-bearing rows: the clean build with the output folder INSIDE the
# checkout (the regression for the 2026-10-19 dry run, where the first archive
# made every later binary say +dirty), and the three refusals (an untracked
# file, a modified tracked file, not a git checkout), each of which must name
# the problem and build nothing.
set -u
REAL="$(cd "$(dirname "$0")/.." && pwd)"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/afk-release-script-test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT
pass=0 fail=0

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

gover=$(grep '^go ' "$REAL/go.mod" | head -1)

# make_repo <dir> <git|plain>: the scratch program, committed when "git".
make_repo() {
  local dir="$1" kind="$2"
  mkdir -p "$dir/scripts"
  cp "$REAL/scripts/release.sh" "$dir/scripts/release.sh"
  echo "scratch" > "$dir/README.md"
  printf 'module example\n\n%s\n' "$gover" > "$dir/go.mod"
  cat > "$dir/main.go" <<'GO'
package main

import "fmt"

var version string

func main() { fmt.Println(version) }
GO
  printf '/dist/\n' > "$dir/.gitignore"
  if [ "$kind" = git ]; then
    git -C "$dir" init -q
    git -C "$dir" add -A
    git -C "$dir" -c user.name=t -c user.email=t@example.com commit -q -m init
  fi
}

modified_of() { # modified_of <archive>: prints the vcs.modified line of its afk
  local d
  d=$(mktemp -d "$ROOT/x.XXXXXX")
  tar -xzf "$1" -C "$d" afk
  go version -m "$d/afk" | grep 'vcs.modified'
}

# Case 1: clean tree, output folder inside the checkout.
repo="$ROOT/clean"
make_repo "$repo" git
out=$(cd "$repo" && bash scripts/release.sh 0.0.1 dist 2>&1)
code=$?
check_exit "clean tree: exits 0" 0 "$code" "$out"
n=$(find "$repo/dist" -name 'afk_0.0.1_*.tar.gz' 2>/dev/null | wc -l)
check_cond "clean tree: four archives" test "$n" -eq 4
check_cond "clean tree: checksums.txt written" test -s "$repo/dist/checksums.txt"
for t in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  line=$(modified_of "$repo/dist/afk_0.0.1_$t.tar.gz" 2>&1)
  check_contains "clean tree: $t is vcs.modified=false" "$line" "vcs.modified=false"
done

# Case 2: an untracked file.
repo="$ROOT/stray"
make_repo "$repo" git
touch "$repo/stray.txt"
out=$(cd "$repo" && bash scripts/release.sh 0.0.1 dist 2>&1)
code=$?
check_exit "untracked file: exits 1" 1 "$code" "$out"
check_contains "untracked file: names stray.txt" "$out" "stray.txt"
check_cond "untracked file: nothing built" test -z "$(ls -A "$repo/dist" 2>/dev/null)"

# Case 3: a modified tracked file.
repo="$ROOT/edited"
make_repo "$repo" git
echo "more" >> "$repo/README.md"
out=$(cd "$repo" && bash scripts/release.sh 0.0.1 dist 2>&1)
code=$?
check_exit "modified file: exits 1" 1 "$code" "$out"
check_contains "modified file: names README.md" "$out" "README.md"
check_cond "modified file: nothing built" test -z "$(ls -A "$repo/dist" 2>/dev/null)"

# Case 4: not a git checkout.
repo="$ROOT/plain"
make_repo "$repo" plain
out=$(cd "$repo" && bash scripts/release.sh 0.0.1 dist 2>&1)
code=$?
check_exit "not a checkout: exits 1" 1 "$code" "$out"
check_contains "not a checkout: says so" "$out" "git checkout"
check_cond "not a checkout: nothing built" test -z "$(ls -A "$repo/dist" 2>/dev/null)"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
