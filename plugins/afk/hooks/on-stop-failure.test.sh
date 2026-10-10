#!/usr/bin/env bash
# Hermetic tests for on-stop-failure.sh (the rate-limit marker writer,
# status-schema.md section 10). Each case gets its own "$ROOT/<case>/wt" so a
# marker left by an earlier case cannot satisfy a later "absent" assertion.
#
# Load-bearing rows: matched (marker written with a parseable `at`, beside the
# right session only), the no-write rows (unmatched, terminal phase, bad JSON,
# missing jq - each exits 0 and writes nothing), status.json byte-identical,
# and the final sweep for leftover temp files.
set -u
unset AFK_HOOK_LOG

SCRIPT="$(cd "$(dirname "$0")" && pwd)/on-stop-failure.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/afk-on-stop-failure-test.XXXXXX")"
pass=0 fail=0

command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required by the hook and is missing"; exit 1; }

if [ ! -e "$SCRIPT" ]; then
  fail=$((fail+1))
  echo "FAIL: $SCRIPT does not exist"
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

check_eq() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc"; echo "  expected: $expected"; echo "  actual:   $actual"
  fi
}

status_json() {
  jq -nc --arg sid "$1" --arg pn "$2" --arg ph "$3" \
    '{session_id:$sid, plan_name:$pn, phase:$ph, branch:"", base_branch:"",
      loop_count:0, awaiting_input:false, tmux_window:"",
      updated_at:"2026-01-01T00:00:00Z"}'
}

# Two sessions, so "wrote the marker" is paired with "wrote it beside the
# right status.json": a hook that globbed .afk/*/ would fail the second.
# The phase of "demo" is a parameter for the terminal-phase case.
worktree_with_two_sessions() {
  local d="$ROOT/$1/wt" phase="${2:-executing}"
  mkdir -p "$d/.afk/demo" "$d/.afk/other"
  status_json "sid-1" "demo" "$phase" > "$d/.afk/demo/status.json"
  status_json "sid-2" "other" "planning" > "$d/.afk/other/status.json"
  printf '%s' "$d"
}

# Only cwd and session_id are fields the hook may rely on; the rest mimics a
# real StopFailure payload and must not matter.
failure_payload() {
  jq -nc --arg sid "$1" --arg cwd "$2" \
    '{session_id:$sid, cwd:$cwd, hook_event_name:"StopFailure", reason:"rate_limit"}'
}

hash_of() { shasum -a 256 "$1" 2>/dev/null | awk '{print $1}'; }

run_hook() { printf '%s' "$1" | "$SCRIPT" 2>&1; }

test_matched_session() {
  local wt before after payload code out marker at
  wt=$(worktree_with_two_sessions matched)
  before=$(hash_of "$wt/.afk/demo/status.json")
  payload=$(failure_payload "sid-1" "$wt")
  out=$(run_hook "$payload"); code=$?
  check_exit "matched session exits 0" 0 "$code" "$out"
  check_eq "matched session prints nothing" "" "$out"

  marker="$wt/.afk/demo/.rate-limited"
  check_cond "marker written for the matched session" test -f "$marker"
  check_eq "marker is exactly one line" "1" "$(grep -c '' "$marker" 2>/dev/null)"
  at=$(jq -r '.at' "$marker" 2>/dev/null)
  check_cond "marker 'at' is an RFC 3339 UTC timestamp" \
    bash -c '[[ "$1" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]' _ "$at"
  check_eq "marker has only the 'at' key" '["at"]' "$(jq -c 'keys' "$marker" 2>/dev/null)"
  check_cond "unmatched session gets no marker" test ! -e "$wt/.afk/other/.rate-limited"
  after=$(hash_of "$wt/.afk/demo/status.json")
  check_eq "status.json is byte-identical after the hook runs" "$before" "$after"
}

test_repeat_overwrites() {
  local wt payload code out
  wt=$(worktree_with_two_sessions repeat)
  printf '{"at":"2020-01-01T00:00:00Z"}\n' > "$wt/.afk/demo/.rate-limited"
  payload=$(failure_payload "sid-1" "$wt")
  out=$(run_hook "$payload"); code=$?
  check_exit "second failure exits 0" 0 "$code" "$out"
  check_cond "second failure replaces the old timestamp" \
    test "$(jq -r '.at' "$wt/.afk/demo/.rate-limited" 2>/dev/null)" != "2020-01-01T00:00:00Z"
}

test_unmatched_session_id() {
  local wt payload code out
  wt=$(worktree_with_two_sessions unmatched)
  payload=$(failure_payload "sid-9" "$wt")
  out=$(run_hook "$payload"); code=$?
  check_exit "unmatched session_id exits 0" 0 "$code" "$out"
  check_cond "no marker under demo when unmatched" test ! -e "$wt/.afk/demo/.rate-limited"
  check_cond "no marker under other when unmatched" test ! -e "$wt/.afk/other/.rate-limited"
}

test_terminal_phase() {
  local wt payload code out phase
  for phase in done failed; do
    wt=$(worktree_with_two_sessions "terminal-$phase" "$phase")
    payload=$(failure_payload "sid-1" "$wt")
    out=$(run_hook "$payload"); code=$?
    check_exit "phase $phase exits 0" 0 "$code" "$out"
    check_cond "phase $phase writes no marker" test ! -e "$wt/.afk/demo/.rate-limited"
  done
}

test_invalid_json_stdin() {
  local wt code out
  wt=$(worktree_with_two_sessions invalid-json)
  out=$(printf '{not valid json' | "$SCRIPT" 2>&1); code=$?
  check_exit "invalid JSON on stdin exits 0" 0 "$code" "$out"
  check_cond "invalid JSON writes no marker" test ! -e "$wt/.afk/demo/.rate-limited"
}

test_empty_stdin_and_missing_cwd() {
  local code out wt
  out=$(printf '' | "$SCRIPT" 2>&1); code=$?
  check_exit "empty stdin exits 0" 0 "$code" "$out"
  wt=$(worktree_with_two_sessions no-cwd)
  out=$(printf '{"session_id":"sid-1"}' | "$SCRIPT" 2>&1); code=$?
  check_exit "payload without cwd exits 0" 0 "$code" "$out"
  check_cond "payload without cwd writes no marker" test ! -e "$wt/.afk/demo/.rate-limited"
}

# jq is hidden by running the hook with a PATH holding only dirname (which the
# script's own HOOK_DIR line needs), so the exit is reached through have_jq
# rather than through an earlier failure to find the script directory.
test_missing_jq() {
  local wt payload code out empty
  wt=$(worktree_with_two_sessions no-jq)
  payload=$(failure_payload "sid-1" "$wt")
  empty="$ROOT/empty-path"
  mkdir -p "$empty"
  ln -sf "$(command -v dirname)" "$empty/dirname"
  out=$(printf '%s' "$payload" | PATH="$empty" "$(command -v bash)" "$SCRIPT" 2>&1); code=$?
  check_exit "missing jq exits 0" 0 "$code" "$out"
  check_cond "missing jq writes no marker" test ! -e "$wt/.afk/demo/.rate-limited"
}

test_hook_log() {
  local logfile wt payload code out
  logfile="$ROOT/hook.log"
  rm -f "$logfile"
  wt=$(worktree_with_two_sessions log)
  payload=$(failure_payload "sid-9" "$wt")
  out=$(printf '%s' "$payload" | AFK_HOOK_LOG="$logfile" "$SCRIPT" 2>&1); code=$?
  check_exit "AFK_HOOK_LOG set still exits 0" 0 "$code" "$out"
  check_cond "log line records EVENT=StopFailure even when unmatched" \
    grep -q 'EVENT=StopFailure' "$logfile"
}

test_matched_session
test_repeat_overwrites
test_unmatched_session_id
test_terminal_phase
test_invalid_json_stdin
test_empty_stdin_and_missing_cwd
test_missing_jq
test_hook_log

# The marker is written to .rate-limited.tmp.$$ then renamed, so a surviving
# temp file means the rename never happened or an error path forgot cleanup.
leftovers=$(find "$ROOT" -name '.rate-limited.tmp.*' 2>/dev/null | wc -l | tr -d ' ')
check_eq "no .rate-limited.tmp.* leftover anywhere under ROOT" "0" "$leftovers"

rm -rf "$ROOT"
echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ] || exit 1
