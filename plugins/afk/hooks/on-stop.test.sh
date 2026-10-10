#!/usr/bin/env bash
# Hermetic tests for on-stop.sh. check_exit/check_cond report a missing
# script as an ordinary FAIL line (exit 127 from "$SCRIPT") rather than
# aborting the run, which doubles as a guard if $SCRIPT is ever deleted or
# renamed.
#
# Fixture layout: each case gets its own "$ROOT/<case>/wt" worktree rather
# than sharing one "$ROOT/wt". Cases 2-4 assert on the *absence* or exact
# content of .afk/demo/.background-tasks, which a sidecar left behind by an
# earlier case would silently satisfy or contradict; per-case roots make each
# assertion mean what it says. The final .background-tasks.tmp.* sweep still
# covers every case at once because it walks all of $ROOT.
set -u
unset AFK_HOOK_LOG

SCRIPT="$(cd "$(dirname "$0")" && pwd)/on-stop.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/afk-on-stop-test.XXXXXX")"
pass=0 fail=0

command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required by the hook and is missing"; exit 1; }

if [ ! -e "$SCRIPT" ]; then
  fail=$((fail+1))
  echo "FAIL: $SCRIPT does not exist"
fi

# Takes the assertion as a command to run rather than a precomputed status:
# the `[ cond ]; rc=$?; check "$rc"` form trips shellcheck SC2319 on some
# occurrences and not on structurally identical others in this same file.
check_cond() {
  local desc="$1"
  shift
  if "$@"; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc"
  fi
}

# The optional 4th argument carries whatever the hook printed. The hook is
# required to be silent, so on an unexpected exit code that text is usually
# the only evidence of why.
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

# grep's own "No such file" complaint would land on the suite's stderr and
# read like a bug in the test rather than a reported FAIL, so absence of the
# log file is swallowed here and surfaced as a plain failed assertion.
log_has() { grep -q -- "$1" "$2" 2>/dev/null; }

# grep -c '' over wc -l: a final line without a trailing newline still counts,
# and the missing-file case is grep's own stderr, silenced above.
log_line_count() {
  local n
  n=$(grep -c '' "$1" 2>/dev/null) || n=0
  printf '%s' "${n:-0}"
}

# All nine status.json fields from status-schema.md section 2 - the schema
# forbids adding or omitting any of them, so the fixture carries every one.
status_json() {
  jq -nc --arg sid "$1" --arg pn "$2" --arg ph "$3" \
    '{session_id:$sid, plan_name:$pn, phase:$ph, branch:"", base_branch:"",
      loop_count:0, awaiting_input:false, tmux_window:"",
      updated_at:"2026-01-01T00:00:00Z"}'
}

# Two sessions, so every "wrote the sidecar" assertion is paired with a
# "wrote it beside the *right* status.json" one: a hook that globbed
# .afk/*/ instead of honoring find_target would pass the first and fail
# the second.
worktree_with_two_sessions() {
  local d="$ROOT/$1/wt"
  mkdir -p "$d/.afk/demo" "$d/.afk/other"
  status_json "sid-1" "demo" "planning" > "$d/.afk/demo/status.json"
  status_json "sid-2" "other" "planning" > "$d/.afk/other/status.json"
  printf '%s' "$d"
}

# Omitting the background-tasks argument yields a payload with no
# background_tasks key at all, covering the "field absent" case.
stop_payload() {
  if [ "$#" -ge 3 ]; then
    jq -nc --arg sid "$1" --arg cwd "$2" --argjson bg "$3" \
      '{session_id:$sid, cwd:$cwd, hook_event_name:"Stop", background_tasks:$bg}'
  else
    jq -nc --arg sid "$1" --arg cwd "$2" \
      '{session_id:$sid, cwd:$cwd, hook_event_name:"Stop"}'
  fi
}

ONE_TASK='[{"id":"a1","type":"subagent","status":"running","description":"research"}]'

run_stop() {
  printf '%s' "$1" | "$SCRIPT" 2>&1
}

run_stop_logged() {
  printf '%s' "$1" | AFK_HOOK_LOG="$2" "$SCRIPT" 2>&1
}

test_matched_session_one_task() {
  local wt before_hash after_hash payload code sidecar expected actual out
  wt=$(worktree_with_two_sessions matched-one-task)
  before_hash=$(shasum -a 256 "$wt/.afk/demo/status.json" 2>/dev/null | awk '{print $1}')
  payload=$(stop_payload "sid-1" "$wt" "$ONE_TASK")
  out=$(run_stop "$payload"); code=$?
  check_exit "matched session with one task exits 0" 0 "$code" "$out"

  sidecar="$wt/.afk/demo/.background-tasks"
  check_cond "sidecar written for the matched session" test -f "$sidecar"
  expected=$(printf '%s' "$ONE_TASK" | jq -c .)
  actual=$(jq -c . "$sidecar" 2>/dev/null)
  check_eq "sidecar content equals the payload's background_tasks array" "$expected" "$actual"
  check_cond "unmatched session gets no sidecar" test ! -e "$wt/.afk/other/.background-tasks"
  after_hash=$(shasum -a 256 "$wt/.afk/demo/status.json" 2>/dev/null | awk '{print $1}')
  check_eq "status.json is byte-identical after the hook runs" "$before_hash" "$after_hash"
}

test_matched_session_empty_array() {
  local wt payload code sidecar actual out
  wt=$(worktree_with_two_sessions matched-empty-array)
  payload=$(stop_payload "sid-1" "$wt" "[]")
  out=$(run_stop "$payload"); code=$?
  check_exit "matched session with empty array exits 0" 0 "$code" "$out"
  sidecar="$wt/.afk/demo/.background-tasks"
  actual=$(jq -c . "$sidecar" 2>/dev/null)
  check_eq "sidecar is [] when background_tasks is []" "[]" "$actual"
}

test_matched_session_field_absent() {
  local wt payload code sidecar actual out
  wt=$(worktree_with_two_sessions matched-field-absent)
  payload=$(stop_payload "sid-1" "$wt")
  out=$(run_stop "$payload"); code=$?
  check_exit "matched session with no background_tasks field exits 0" 0 "$code" "$out"
  sidecar="$wt/.afk/demo/.background-tasks"
  actual=$(jq -c . "$sidecar" 2>/dev/null)
  check_eq "sidecar is [] when background_tasks is absent" "[]" "$actual"
}

test_unmatched_session_id() {
  local wt payload code out
  wt=$(worktree_with_two_sessions unmatched-session)
  payload=$(stop_payload "sid-9" "$wt" "$ONE_TASK")
  out=$(run_stop "$payload"); code=$?
  check_exit "unmatched session_id exits 0" 0 "$code" "$out"
  check_cond "no sidecar under demo when unmatched" test ! -e "$wt/.afk/demo/.background-tasks"
  check_cond "no sidecar under other when unmatched" test ! -e "$wt/.afk/other/.background-tasks"
}

test_invalid_json_stdin() {
  local wt code out
  wt=$(worktree_with_two_sessions invalid-json)
  out=$(printf '{not valid json' | "$SCRIPT" 2>&1); code=$?
  check_exit "invalid JSON on stdin exits 0" 0 "$code" "$out"
  check_cond "invalid JSON writes no sidecar" test ! -e "$wt/.afk/demo/.background-tasks"
  check_cond "invalid JSON writes no sidecar (other)" test ! -e "$wt/.afk/other/.background-tasks"
}

test_empty_stdin() {
  local code out
  out=$(printf '' | "$SCRIPT" 2>&1); code=$?
  check_exit "empty stdin exits 0" 0 "$code" "$out"
}

# Deliberately driven by a payload the targeting rule rejects: the log is
# specified as being written before find_target runs, so an unmatched
# session_id is the case that proves that ordering.
test_hook_log_unmatched_event() {
  local logfile wt payload code out lines
  logfile="$ROOT/hook.log"
  rm -f "$logfile"

  wt=$(worktree_with_two_sessions log-unset)
  payload=$(stop_payload "sid-9" "$wt" "$ONE_TASK")
  out=$(run_stop "$payload"); code=$?
  check_exit "unset AFK_HOOK_LOG still exits 0" 0 "$code" "$out"
  check_cond "no log file created when AFK_HOOK_LOG is unset" test ! -e "$logfile"

  wt=$(worktree_with_two_sessions log-set)
  payload=$(stop_payload "sid-9" "$wt" "$ONE_TASK")
  out=$(run_stop_logged "$payload" "$logfile"); code=$?
  check_exit "AFK_HOOK_LOG set still exits 0" 0 "$code" "$out"
  check_cond "log file created when AFK_HOOK_LOG is set" test -f "$logfile"
  lines=$(log_line_count "$logfile")
  check_eq "log gains exactly one line" "1" "$lines"
  check_cond "log line records EVENT=Stop" log_has "EVENT=Stop" "$logfile"
  # Quoted, because the payload is logged as compact JSON - a bare sid-9
  # would also match a hook that logged only the raw session id.
  check_cond "log line records the unmatched session_id" log_has '"sid-9"' "$logfile"
  check_cond "still no sidecar for the unmatched event" test ! -e "$wt/.afk/demo/.background-tasks"
}

# A matched Stop proves a turn completed after any StopFailure, so it removes
# the rate-limit marker (status-schema.md section 10); an unmatched one must
# leave it, since it belongs to a session this event is not about.
test_matched_stop_removes_rate_limit_marker() {
  local wt payload code out
  wt=$(worktree_with_two_sessions rate-limit-marker)
  printf '{"at":"2026-01-01T00:00:00Z"}\n' > "$wt/.afk/demo/.rate-limited"

  payload=$(stop_payload "sid-9" "$wt" "[]")
  out=$(run_stop "$payload"); code=$?
  check_exit "unmatched Stop exits 0 with a marker present" 0 "$code" "$out"
  check_cond "unmatched Stop leaves the rate-limit marker" test -f "$wt/.afk/demo/.rate-limited"

  payload=$(stop_payload "sid-1" "$wt" "[]")
  out=$(run_stop "$payload"); code=$?
  check_exit "matched Stop exits 0 with a marker present" 0 "$code" "$out"
  check_cond "matched Stop removes the rate-limit marker" test ! -e "$wt/.afk/demo/.rate-limited"
}

test_matched_session_one_task
test_matched_session_empty_array
test_matched_session_field_absent
test_unmatched_session_id
test_invalid_json_stdin
test_empty_stdin
test_hook_log_unmatched_event
test_matched_stop_removes_rate_limit_marker

# The sidecar write is specified as "write .background-tasks.tmp.$$ then
# mv -f", so a surviving temp file means the rename never happened or an
# error path forgot to clean up.
leftovers=$(find "$ROOT" -name '.background-tasks.tmp.*' 2>/dev/null | wc -l | tr -d ' ')
check_eq "no .background-tasks.tmp.* leftover anywhere under ROOT" "0" "$leftovers"

rm -rf "$ROOT"
echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ] || exit 1
