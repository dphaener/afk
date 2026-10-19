#!/usr/bin/env bash
# Hermetic tests for release-decide.sh. Each case builds a scratch git
# repository holding a copy of the script and of scripts/nextversion (without
# its tests), so the real afk tree is never touched.
#
# Load-bearing rows: a hand tag on the <to-sha> commit (skip, so a hand tag
# never gets a second release), a bad <to-sha> (non-zero with nothing on
# stdout, so an error is never read as a version), and the clean-tree check
# after every case (the release script refuses a changed tree).
set -u
REAL="$(cd "$(dirname "$0")/.." && pwd)"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/afk-release-decide-test.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT
pass=0 fail=0

# Keep the owner's git settings out of the scratch repositories.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

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

check_equal() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc"; echo "  wanted: [$expected]"; echo "  got:    [$actual]"
  fi
}

gover=$(grep '^go ' "$REAL/go.mod" | head -1)

commit() { # commit <dir> <message>
  git -C "$1" add -A
  git -C "$1" -c user.name=t -c user.email=t@example.com commit -q -m "$2"
}

make_repo() { # make_repo <dir>: one committed program, nothing tagged
  local dir="$1" f
  mkdir -p "$dir/scripts/nextversion"
  cp "$REAL/scripts/release-decide.sh" "$dir/scripts/release-decide.sh"
  for f in "$REAL"/scripts/nextversion/*.go; do
    case "$f" in *_test.go) continue ;; esac
    cp "$f" "$dir/scripts/nextversion/"
  done
  printf 'module example\n\n%s\n' "$gover" > "$dir/go.mod"
  echo scratch > "$dir/README.md"
  printf 'package main\n\nfunc main() {}\n' > "$dir/main.go"
  git -C "$dir" init -q
  commit "$dir" "feat: first"
}

# run_case <name> <dir> <args...>: sets out, err, code, and checks a clean tree.
run_case() {
  local name="$1" dir="$2"
  shift 2
  code=0
  out=$("$dir/scripts/release-decide.sh" "$@" 2>"$ROOT/err") || code=$?
  err=$(cat "$ROOT/err")
  check_equal "$name: work tree stays clean" "" "$(git -C "$dir" status --porcelain)"
}

# Case a: docs-only change after a release.
d="$ROOT/a"; make_repo "$d"
git -C "$d" tag v0.1.0
base=$(git -C "$d" rev-parse HEAD)
mkdir -p "$d/docs"; echo x > "$d/docs/x.md"; commit "$d" "docs: add x"
run_case "docs only" "$d" "$base" HEAD
check_exit "docs only: exit 0" 0 "$code" "$err"
check_equal "docs only: skip" skip "$out"

# Case b: a fix to a root .go file after a release.
d="$ROOT/b"; make_repo "$d"
git -C "$d" tag v0.1.0
base=$(git -C "$d" rev-parse HEAD)
printf 'package main\n\nfunc main() { _ = 1 }\n' > "$d/main.go"; commit "$d" "fix: tweak main"
run_case "fix" "$d" "$base" HEAD
check_exit "fix: exit 0" 0 "$code" "$err"
check_equal "fix: patch bump" 0.1.1 "$out"

# Case c: the <to-sha> commit carries a hand tag.
d="$ROOT/c"; make_repo "$d"
git -C "$d" tag v0.1.0
base=$(git -C "$d" rev-parse HEAD)
printf 'package main\n\nfunc main() { _ = 2 }\n' > "$d/main.go"; commit "$d" "feat: new thing"
git -C "$d" tag v0.2.0
run_case "hand tag on to-sha" "$d" "$base" HEAD
check_exit "hand tag on to-sha: exit 0" 0 "$code" "$err"
check_equal "hand tag on to-sha: skip" skip "$out"

# Case d: a commit that does not exist.
d="$ROOT/d"; make_repo "$d"
run_case "bad to-sha" "$d" HEAD not-a-commit
if [ "$code" != 0 ]; then
  pass=$((pass+1)); echo "PASS: bad to-sha: non-zero exit"
else
  fail=$((fail+1)); echo "FAIL: bad to-sha: expected a non-zero exit"
fi
check_equal "bad to-sha: nothing on stdout" "" "$out"
if [ -n "$err" ]; then
  pass=$((pass+1)); echo "PASS: bad to-sha: says why on stderr"
else
  fail=$((fail+1)); echo "FAIL: bad to-sha: stderr is empty"
fi

# Case e: wrong argument count.
d="$ROOT/e"; make_repo "$d"
run_case "one argument" "$d" onlyone
check_exit "one argument: exit 2" 2 "$code" "$err"
check_equal "one argument: nothing on stdout" "" "$out"
case "$err" in *usage*) pass=$((pass+1)); echo "PASS: one argument: usage line" ;;
  *) fail=$((fail+1)); echo "FAIL: one argument: no usage line: $err" ;; esac

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
