#!/usr/bin/env bash
# Hermetic tests for on-notification.sh. Stubs terminal-notifier (appends its
# args to $ROOT/calls.log) and restricts PATH so notify.sh's first probe,
# `afk notify --check`, cannot find a real `afk` — the stub directory
# deliberately does NOT contain one, so the probe falls through to the
# terminal-notifier stub the way a real machine without `afk` on PATH would.
#
# Load-bearing rows: case 1 and case 8 pin the background-tasks sidecar gate
# on idle_prompt + planning, and case 10 pins the AFK_HOOK_LOG opt-in.
# Cases 2-7 and 9 pin the unchanged behavior outside that gate (non-planning
# idle, permission_prompt, missing/unparseable sidecar, unknown types).
set -u
unset AFK_HOOK_LOG

HOOK_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HOOK_DIR/on-notification.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/afk-notif-test.XXXXXX")"
pass=0 fail=0

command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required by the hook and is missing"; exit 1; }

# notify.sh probes `afk notify --check` first, then `terminal-notifier`,
# then `osascript`. This PATH omits ~/bin, where a real `afk` lives, so the
# first probe misses. /usr/bin stays on it (jq's dir alone is not enough for
# the hook's own `cat`/`date`/`basename`), which means /usr/bin/osascript is
# still reachable — the stub is what keeps the probe from ever getting there,
# so a missing stub would toast for real rather than fail loudly.
JQ_DIR="$(dirname "$(command -v jq)")"
TEST_PATH="$ROOT/bin:$JQ_DIR:/usr/bin:/bin"

WT="$ROOT/wt"
AFK_DIR="$WT/.afk/demo"
STATUS="$AFK_DIR/status.json"
SIDECAR="$AFK_DIR/.background-tasks"
PERM_SENTINEL="$AFK_DIR/.awaiting-permission"
FIXED_TS="2020-01-01T00:00:00Z"

check_exit() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc (expected exit $expected, got $actual)"
  fi
}

check_eq() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc (expected '$expected', got '$actual')"
  fi
}

check_contains() {
  local desc="$1" needle="$2" haystack="$3"
  case "$haystack" in
  *"$needle"*) pass=$((pass+1)); echo "PASS: $desc" ;;
  *) fail=$((fail+1)); echo "FAIL: $desc"; echo "  expected substring: $needle"; echo "  actual: $haystack" ;;
  esac
}

check_file_exists() {
  local desc="$1" path="$2"
  if [ -e "$path" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc ($path does not exist)"
  fi
}

check_file_absent() {
  local desc="$1" path="$2"
  if [ ! -e "$path" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc ($path unexpectedly exists)"
  fi
}

stub() {
  local path="$1" code="$2"
  mkdir -p "$(dirname "$path")"
  cat > "$path" <<STUB
#!/usr/bin/env bash
echo "\$(basename "$path") \$*" >> "$ROOT/calls.log"
exit $code
STUB
  chmod +x "$path"
}
stub "$ROOT/bin/terminal-notifier" 0

calls_count() {
  local c
  c=$(grep -c '^terminal-notifier ' "$ROOT/calls.log" 2>/dev/null)
  [ -n "$c" ] || c=0
  printf '%s' "$c"
}

awaiting_input_of() {
  jq -r '.awaiting_input' "$STATUS" 2>/dev/null
}

updated_at_of() {
  jq -r '.updated_at' "$STATUS" 2>/dev/null
}

# All nine status-schema.md fields are required on every write; only phase
# varies across cases, session_id stays sid-1 so find_target's primary match
# always hits.
write_status() {
  local phase="$1"
  mkdir -p "$AFK_DIR"
  jq -n --arg phase "$phase" --arg ts "$FIXED_TS" '
    {
      session_id: "sid-1",
      plan_name: "demo",
      phase: $phase,
      branch: "demo",
      base_branch: "main",
      loop_count: 0,
      awaiting_input: false,
      tmux_window: "",
      updated_at: $ts
    }' > "$STATUS"
}

reset_case() {
  write_status "$1"
  rm -f "$SIDECAR" "$PERM_SENTINEL"
  : > "$ROOT/calls.log"
}

write_sidecar() {
  printf '%s' "$1" > "$SIDECAR"
}

run_hook() {
  local ntype="$1" msg="${2:-Claude needs your attention}"
  jq -n --arg cwd "$WT" --arg ntype "$ntype" --arg msg "$msg" '
    {
      session_id: "sid-1",
      cwd: $cwd,
      hook_event_name: "Notification",
      notification_type: $ntype,
      message: $msg
    }' | env PATH="$TEST_PATH" "$SCRIPT" 2>&1
}

RUNNING_TASK='[{"id":"a1","type":"subagent","status":"running","description":"research"}]'
TWO_TASKS='[{"id":"a1","type":"shell","status":"running","description":"x"},{"id":"a2","type":"subagent","status":"running","description":"y"}]'

#### Case 1: planning + idle_prompt + sidecar with 1 pending task ####

reset_case planning
write_sidecar "$RUNNING_TASK"
out=$(run_hook idle_prompt); code=$?
check_exit "case1: exits 0" 0 "$code"
check_eq "case1: no output" "" "$out"
check_eq "case1: no notifier call while a background task is pending" 0 "$(calls_count)"
check_eq "case1: awaiting_input stays false" false "$(awaiting_input_of)"
check_file_absent "case1: no .awaiting-permission sentinel" "$PERM_SENTINEL"
check_eq "case1: status.json updated_at unchanged" "$FIXED_TS" "$(updated_at_of)"

#### Case 2: planning + idle_prompt + sidecar is an empty array ####

reset_case planning
write_sidecar '[]'
out=$(run_hook idle_prompt); code=$?
check_exit "case2: exits 0" 0 "$code"
check_eq "case2: no output" "" "$out"
check_eq "case2: exactly one notifier call" 1 "$(calls_count)"
check_eq "case2: awaiting_input set true" true "$(awaiting_input_of)"

#### Case 3: planning + idle_prompt + no sidecar at all ####

reset_case planning
out=$(run_hook idle_prompt); code=$?
check_exit "case3: exits 0" 0 "$code"
check_eq "case3: no output" "" "$out"
check_eq "case3: exactly one notifier call with no sidecar" 1 "$(calls_count)"
check_eq "case3: awaiting_input set true with no sidecar" true "$(awaiting_input_of)"

#### Case 4: planning + idle_prompt + sidecar that fails to parse ####

reset_case planning
write_sidecar 'not json'
out=$(run_hook idle_prompt); code=$?
check_exit "case4: exits 0" 0 "$code"
check_eq "case4: no output" "" "$out"
check_eq "case4: exactly one notifier call with an unparseable sidecar" 1 "$(calls_count)"
check_eq "case4: awaiting_input set true with an unparseable sidecar" true "$(awaiting_input_of)"

#### Case 5: planning + permission_prompt + sidecar with 1 task ####
# The sidecar gate is scoped to idle_prompt only; permission_prompt fires
# regardless of pending background work.

reset_case planning
write_sidecar "$RUNNING_TASK"
out=$(run_hook permission_prompt); code=$?
check_exit "case5: exits 0" 0 "$code"
check_eq "case5: no output" "" "$out"
check_eq "case5: exactly one notifier call for a permission prompt" 1 "$(calls_count)"
check_eq "case5: awaiting_input set true for a permission prompt" true "$(awaiting_input_of)"
check_file_exists "case5: .awaiting-permission sentinel created" "$PERM_SENTINEL"

#### Case 6: executing + idle_prompt + sidecar is an empty array ####
# The existing phase check runs before the sidecar is ever consulted.

reset_case executing
write_sidecar '[]'
out=$(run_hook idle_prompt); code=$?
check_exit "case6: exits 0" 0 "$code"
check_eq "case6: no output" "" "$out"
check_eq "case6: no notifier call outside planning" 0 "$(calls_count)"
check_eq "case6: awaiting_input stays false outside planning" false "$(awaiting_input_of)"

#### Case 7: executing + idle_prompt + no sidecar ####

reset_case executing
out=$(run_hook idle_prompt); code=$?
check_exit "case7: exits 0" 0 "$code"
check_eq "case7: no output" "" "$out"
check_eq "case7: no notifier call outside planning with no sidecar" 0 "$(calls_count)"
check_eq "case7: awaiting_input stays false outside planning with no sidecar" false "$(awaiting_input_of)"

#### Case 8: planning + idle_prompt + sidecar with 2 mixed-type tasks ####
# "Pending" is length > 0, not a check on task type or status.

reset_case planning
write_sidecar "$TWO_TASKS"
out=$(run_hook idle_prompt); code=$?
check_exit "case8: exits 0" 0 "$code"
check_eq "case8: no output" "" "$out"
check_eq "case8: no notifier call with two pending tasks" 0 "$(calls_count)"
check_eq "case8: awaiting_input stays false with two pending tasks" false "$(awaiting_input_of)"

#### Case 9: planning + unknown notification_type ####

reset_case planning
out=$(run_hook agent_completed); code=$?
check_exit "case9: exits 0" 0 "$code"
check_eq "case9: no output" "" "$out"
check_eq "case9: no notifier call for an unknown notification_type" 0 "$(calls_count)"

#### Case 10: AFK_HOOK_LOG opt-in on a case-1-shaped run ####
# Same LOG_PATH reused for the unset run below, so its absence there proves
# the env var gates the write rather than the path just never existing.

LOG_PATH="$ROOT/hook.log"

reset_case planning
write_sidecar "$RUNNING_TASK"
rm -f "$LOG_PATH"
out=$(AFK_HOOK_LOG="$LOG_PATH" run_hook idle_prompt); code=$?
check_exit "case10: exits 0 with AFK_HOOK_LOG set" 0 "$code"
check_eq "case10: no stdout output" "" "$out"
if [ -f "$LOG_PATH" ]; then
  lines=$(wc -l < "$LOG_PATH" | tr -d ' ')
  check_eq "case10: exactly one log line written" "1" "$lines"
  logline="$(cat "$LOG_PATH" 2>/dev/null)"
  check_contains "case10: log line carries EVENT=Notification" "EVENT=Notification" "$logline"
  check_contains "case10: log line carries the notification_type" "idle_prompt" "$logline"
else
  fail=$((fail+1)); echo "FAIL: case10: AFK_HOOK_LOG set but no log file was written"
fi

reset_case planning
write_sidecar "$RUNNING_TASK"
rm -f "$LOG_PATH"
unset AFK_HOOK_LOG
out=$(run_hook idle_prompt); code=$?
check_exit "case10: exits 0 with AFK_HOOK_LOG unset" 0 "$code"
check_eq "case10: no stdout output with AFK_HOOK_LOG unset" "" "$out"
check_file_absent "case10: no log file with AFK_HOOK_LOG unset" "$LOG_PATH"

rm -rf "$ROOT"
echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ] || exit 1
