#!/usr/bin/env bash
# Hermetic tests for on-pre-tool-use.sh. The hook is a deliberate no-op that
# must exit 0 and print nothing for any input; the only side effect allowed is
# the one hook_log line when AFK_HOOK_LOG is set. A missing script shows up as
# an ordinary FAIL (exit 127) rather than aborting the run.
set -u
unset AFK_HOOK_LOG

SCRIPT="$(cd "$(dirname "$0")" && pwd)/on-pre-tool-use.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/afk-on-pre-tool-use-test.XXXXXX")"
pass=0 fail=0

command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required by the hook and is missing"; exit 1; }

check_eq() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc"; echo "  expected: $expected"; echo "  actual:   $actual"
  fi
}

VALID='{"session_id":"s1","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[]},"tool_use_id":"t1"}'

out="$(printf '%s' "$VALID" | bash "$SCRIPT" 2>&1)"; rc=$?
check_eq "valid payload exits 0" 0 "$rc"
check_eq "valid payload prints nothing" "" "$out"

out="$(printf 'not json' | bash "$SCRIPT" 2>&1)"; rc=$?
check_eq "invalid payload exits 0" 0 "$rc"
check_eq "invalid payload prints nothing" "" "$out"

out="$(bash "$SCRIPT" </dev/null 2>&1)"; rc=$?
check_eq "empty stdin exits 0" 0 "$rc"
check_eq "empty stdin prints nothing" "" "$out"

LOG="$ROOT/hook.log"
out="$(printf '%s' "$VALID" | AFK_HOOK_LOG="$LOG" bash "$SCRIPT" 2>&1)"; rc=$?
check_eq "with AFK_HOOK_LOG exits 0" 0 "$rc"
check_eq "with AFK_HOOK_LOG prints nothing" "" "$out"
check_eq "with AFK_HOOK_LOG appends one line" 1 "$(grep -c '' "$LOG" 2>/dev/null || echo 0)"
check_eq "the log line names the event" 1 "$(grep -c 'EVENT=PreToolUse' "$LOG" 2>/dev/null || echo 0)"

printf 'not json' | AFK_HOOK_LOG="$LOG" bash "$SCRIPT" >/dev/null 2>&1
check_eq "invalid payload adds no log line" 1 "$(grep -c '' "$LOG" 2>/dev/null || echo 0)"

# No other file may appear: the hook writes only the log.
check_eq "the hook leaves only the log behind" "hook.log" "$(ls "$ROOT")"

rm -rf "$ROOT"
echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ] || exit 1
