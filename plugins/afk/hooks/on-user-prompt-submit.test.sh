#!/usr/bin/env bash
# Hermetic tests for on-user-prompt-submit.sh. Fixtures a .afk/{name}/
# status.json directly (find_target's file-scan target) rather than a real
# afk session, and feeds the hook payloads on stdin the way Claude Code
# would. Case 6 also exercises on-post-tool-use.sh, but only its
# AFK_HOOK_LOG line — see the file header for why a fourth test file is
# unnecessary for that.
#
# Load-bearing rows: case 1 (the sidecar clear) and cases 5-8 (the
# AFK_HOOK_LOG opt-in) pin the implemented behavior of both hooks,
# including that hook_log runs before the cwd guard and find_target — cases
# 7 and 8 feed a missing-cwd and an unmatched-session_id payload and assert
# the log line still appears, which is the ordering the sidecar contract
# requires (hook_log right after `jq -e .`, before target matching).
set -u
unset AFK_HOOK_LOG

HOOK_DIR="$(cd "$(dirname "$0")" && pwd)"
UPS_SCRIPT="$HOOK_DIR/on-user-prompt-submit.sh"
PTU_SCRIPT="$HOOK_DIR/on-post-tool-use.sh"
ROOT="$(mktemp -d "${TMPDIR:-/tmp}/afk-ups-test.XXXXXX")"
pass=0 fail=0

command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required by the hook and is missing"; exit 1; }

check_exit() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc (expected exit $expected, got $actual)"
  fi
}

check_true() {
  local desc="$1" cond="$2"
  if [ "$cond" = "true" ]; then
    pass=$((pass+1)); echo "PASS: $desc"
  else
    fail=$((fail+1)); echo "FAIL: $desc"
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

# write_status FILE SESSION_ID AWAITING_INPUT [PHASE]
#
# Fixture carries all nine status-schema.md fields; only session_id,
# phase and awaiting_input vary across cases.
write_status() {
  local file="$1" sid="$2" awaiting="$3" phase="${4:-executing}"
  mkdir -p "$(dirname "$file")"
  jq -n --arg sid "$sid" --arg phase "$phase" --argjson awaiting "$awaiting" '
    {
      session_id: $sid,
      plan_name: "demo",
      phase: $phase,
      branch: "demo",
      base_branch: "main",
      loop_count: 0,
      awaiting_input: $awaiting,
      tmux_window: "",
      updated_at: "2026-01-01T00:00:00Z"
    }' > "$file"
}

# ups_payload SESSION_ID CWD PROMPT
ups_payload() {
  jq -n --arg sid "$1" --arg cwd "$2" --arg prompt "$3" \
    '{session_id: $sid, cwd: $cwd, hook_event_name: "UserPromptSubmit", prompt: $prompt}'
}

# ptu_payload SESSION_ID CWD
ptu_payload() {
  jq -n --arg sid "$1" --arg cwd "$2" \
    '{session_id: $sid, cwd: $cwd, hook_event_name: "PostToolUse",
      tool_name: "Bash", tool_input: {command: "echo hi"}, tool_response: {}}'
}

# ups_payload_no_cwd SESSION_ID PROMPT — valid JSON, cwd field omitted.
ups_payload_no_cwd() {
  jq -n --arg sid "$1" --arg prompt "$2" \
    '{session_id: $sid, hook_event_name: "UserPromptSubmit", prompt: $prompt}'
}

# ptu_payload_no_cwd SESSION_ID — valid JSON, cwd field omitted.
ptu_payload_no_cwd() {
  jq -n --arg sid "$1" \
    '{session_id: $sid, hook_event_name: "PostToolUse",
      tool_name: "Bash", tool_input: {command: "echo hi"}, tool_response: {}}'
}

awaiting_input_of() { # STATUS_FILE
  jq -r '.awaiting_input' "$1" 2>/dev/null
}

#### Case 1: matched + sidecar present with 1 task ####
# After the run: sidecar is gone AND awaiting_input is false.

wt1="$ROOT/wt1"
status1="$wt1/.afk/demo/status.json"
sidecar1="$wt1/.afk/demo/.background-tasks"
write_status "$status1" "sid-1" true
printf '["task1"]' > "$sidecar1"

out=$(ups_payload "sid-1" "$wt1" "hi" | "$UPS_SCRIPT" 2>&1); code=$?
check_exit "case1: exits 0" 0 "$code"
check_eq "case1: no output" "" "$out"
if [ -f "$sidecar1" ]; then
  fail=$((fail+1)); echo "FAIL: case1: sidecar removed after a matched UserPromptSubmit"
else
  pass=$((pass+1)); echo "PASS: case1: sidecar removed after a matched UserPromptSubmit"
fi
check_true "case1: awaiting_input cleared to false" "$([ "$(awaiting_input_of "$status1")" = false ] && echo true || echo false)"

#### Case 2: matched + no sidecar ####
# exit 0, awaiting_input false, no error output.

wt2="$ROOT/wt2"
status2="$wt2/.afk/demo/status.json"
write_status "$status2" "sid-1" true

out=$(ups_payload "sid-1" "$wt2" "hi" 2>"$ROOT/case2.stderr" | "$UPS_SCRIPT" 2>>"$ROOT/case2.stderr"); code=$?
check_exit "case2: exits 0 with no sidecar" 0 "$code"
check_true "case2: awaiting_input cleared to false" "$([ "$(awaiting_input_of "$status2")" = false ] && echo true || echo false)"
check_eq "case2: no stdout" "" "$out"
err="$(cat "$ROOT/case2.stderr" 2>/dev/null)"
check_eq "case2: no error output" "" "$err"

#### Case 3: unmatched session_id + sidecar present ####
# Sidecar untouched, awaiting_input still true.

wt3="$ROOT/wt3"
status3="$wt3/.afk/demo/status.json"
sidecar3="$wt3/.afk/demo/.background-tasks"
write_status "$status3" "sid-1" true
printf '["task1"]' > "$sidecar3"

out=$(ups_payload "sid-999" "$wt3" "hi" | "$UPS_SCRIPT" 2>&1); code=$?
check_exit "case3: exits 0 on an unmatched session" 0 "$code"
check_eq "case3: no output" "" "$out"
if [ -f "$sidecar3" ]; then
  pass=$((pass+1)); echo "PASS: case3: sidecar untouched on an unmatched session"
else
  fail=$((fail+1)); echo "FAIL: case3: sidecar untouched on an unmatched session"
fi
check_true "case3: awaiting_input still true" "$([ "$(awaiting_input_of "$status3")" = true ] && echo true || echo false)"

#### Case 4: invalid JSON ####
# exit 0, nothing changed.

wt4="$ROOT/wt4"
status4="$wt4/.afk/demo/status.json"
sidecar4="$wt4/.afk/demo/.background-tasks"
write_status "$status4" "sid-1" true
printf '["task1"]' > "$sidecar4"

out=$(printf '{not valid json' | "$UPS_SCRIPT" 2>&1); code=$?
check_exit "case4: exits 0 on invalid JSON" 0 "$code"
check_eq "case4: no output" "" "$out"
if [ -f "$sidecar4" ]; then
  pass=$((pass+1)); echo "PASS: case4: sidecar untouched on invalid JSON"
else
  fail=$((fail+1)); echo "FAIL: case4: sidecar untouched on invalid JSON"
fi
check_true "case4: awaiting_input unchanged on invalid JSON" "$([ "$(awaiting_input_of "$status4")" = true ] && echo true || echo false)"

#### Case 5: AFK_HOOK_LOG on case-1-shaped run; unset produces no file ####

wt5="$ROOT/wt5"
status5="$wt5/.afk/demo/status.json"
sidecar5="$wt5/.afk/demo/.background-tasks"
write_status "$status5" "sid-1" true
printf '["task1"]' > "$sidecar5"

hooklog="$ROOT/hook.log"
out=$(ups_payload "sid-1" "$wt5" "hi" | AFK_HOOK_LOG="$hooklog" "$UPS_SCRIPT" 2>&1); code=$?
check_exit "case5: exits 0 with AFK_HOOK_LOG set" 0 "$code"
check_eq "case5: no output" "" "$out"

if [ -f "$hooklog" ]; then
  lines=$(wc -l < "$hooklog" | tr -d ' ')
  if [ "$lines" -eq 1 ]; then
    pass=$((pass+1)); echo "PASS: case5: exactly one log line written"
  else
    fail=$((fail+1)); echo "FAIL: case5: expected exactly one log line, got $lines"
  fi
  if grep -q 'EVENT=UserPromptSubmit' "$hooklog"; then
    pass=$((pass+1)); echo "PASS: case5: log line carries EVENT=UserPromptSubmit"
  else
    fail=$((fail+1)); echo "FAIL: case5: log line missing EVENT=UserPromptSubmit"
    echo "  actual: $(cat "$hooklog" 2>/dev/null)"
  fi
else
  fail=$((fail+1)); echo "FAIL: case5: AFK_HOOK_LOG set but no log file was written"
fi

# Same path as the set sub-run, cleared first: the absence below then proves
# the env var gates the write, not that an unrelated path went untouched.
rm -f "$hooklog"
wt5b="$ROOT/wt5b"
status5b="$wt5b/.afk/demo/status.json"
sidecar5b="$wt5b/.afk/demo/.background-tasks"
write_status "$status5b" "sid-1" true
printf '["task1"]' > "$sidecar5b"
unset AFK_HOOK_LOG
out=$(ups_payload "sid-1" "$wt5b" "hi" | "$UPS_SCRIPT" 2>&1); code=$?
check_exit "case5: exits 0 with AFK_HOOK_LOG unset" 0 "$code"
check_eq "case5: no output with AFK_HOOK_LOG unset" "" "$out"
if [ -f "$hooklog" ]; then
  fail=$((fail+1)); echo "FAIL: case5: a log file appeared despite AFK_HOOK_LOG being unset"
else
  pass=$((pass+1)); echo "PASS: case5: no log file with AFK_HOOK_LOG unset"
fi

#### Case 6: on-post-tool-use.sh log coverage ####
# A matched PostToolUse payload with AFK_HOOK_LOG set produces one line
# with EVENT=PostToolUse. Sentinel semantics are out of scope here.

wt6="$ROOT/wt6"
status6="$wt6/.afk/demo/status.json"
write_status "$status6" "sid-1" true

ptulog="$ROOT/ptu-hook.log"
out=$(ptu_payload "sid-1" "$wt6" | AFK_HOOK_LOG="$ptulog" "$PTU_SCRIPT" 2>&1); code=$?
check_exit "case6: on-post-tool-use.sh exits 0 with AFK_HOOK_LOG set" 0 "$code"
check_eq "case6: no output" "" "$out"

if [ -f "$ptulog" ]; then
  lines=$(wc -l < "$ptulog" | tr -d ' ')
  if [ "$lines" -eq 1 ]; then
    pass=$((pass+1)); echo "PASS: case6: exactly one log line written"
  else
    fail=$((fail+1)); echo "FAIL: case6: expected exactly one log line, got $lines"
  fi
  if grep -q 'EVENT=PostToolUse' "$ptulog"; then
    pass=$((pass+1)); echo "PASS: case6: log line carries EVENT=PostToolUse"
  else
    fail=$((fail+1)); echo "FAIL: case6: log line missing EVENT=PostToolUse"
    echo "  actual: $(cat "$ptulog" 2>/dev/null)"
  fi
else
  fail=$((fail+1)); echo "FAIL: case6: AFK_HOOK_LOG set but no log file was written"
fi

#### Case 7: valid JSON, no cwd, AFK_HOOK_LOG set — both hooks ####
# hook_log runs before the cwd guard, so a payload that never reaches
# find_target must still produce exactly one log line.

log7ups="$ROOT/case7-ups.log"
out=$(ups_payload_no_cwd "sid-1" "hi" | AFK_HOOK_LOG="$log7ups" "$UPS_SCRIPT" 2>&1); code=$?
check_exit "case7: on-user-prompt-submit.sh exits 0 with no cwd" 0 "$code"
check_eq "case7: no output (ups, no cwd)" "" "$out"
if [ -f "$log7ups" ]; then
  lines=$(wc -l < "$log7ups" | tr -d ' ')
  check_eq "case7: exactly one log line (ups, no cwd)" "1" "$lines"
  if grep -q 'EVENT=UserPromptSubmit' "$log7ups"; then
    pass=$((pass+1)); echo "PASS: case7: log line carries EVENT=UserPromptSubmit (no cwd)"
  else
    fail=$((fail+1)); echo "FAIL: case7: log line missing EVENT=UserPromptSubmit (no cwd)"
    echo "  actual: $(cat "$log7ups" 2>/dev/null)"
  fi
else
  fail=$((fail+1)); echo "FAIL: case7: AFK_HOOK_LOG set but no log file was written (ups, no cwd)"
fi

log7ptu="$ROOT/case7-ptu.log"
out=$(ptu_payload_no_cwd "sid-1" | AFK_HOOK_LOG="$log7ptu" "$PTU_SCRIPT" 2>&1); code=$?
check_exit "case7: on-post-tool-use.sh exits 0 with no cwd" 0 "$code"
check_eq "case7: no output (ptu, no cwd)" "" "$out"
if [ -f "$log7ptu" ]; then
  lines=$(wc -l < "$log7ptu" | tr -d ' ')
  check_eq "case7: exactly one log line (ptu, no cwd)" "1" "$lines"
  if grep -q 'EVENT=PostToolUse' "$log7ptu"; then
    pass=$((pass+1)); echo "PASS: case7: log line carries EVENT=PostToolUse (no cwd)"
  else
    fail=$((fail+1)); echo "FAIL: case7: log line missing EVENT=PostToolUse (no cwd)"
    echo "  actual: $(cat "$log7ptu" 2>/dev/null)"
  fi
else
  fail=$((fail+1)); echo "FAIL: case7: AFK_HOOK_LOG set but no log file was written (ptu, no cwd)"
fi

#### Case 8: valid JSON, unmatched session_id, AFK_HOOK_LOG set — both hooks ####
# hook_log runs before find_target, so an unmatched session still logs.

wt8="$ROOT/wt8"
status8="$wt8/.afk/demo/status.json"
write_status "$status8" "sid-1" true

log8ups="$ROOT/case8-ups.log"
out=$(ups_payload "sid-999" "$wt8" "hi" | AFK_HOOK_LOG="$log8ups" "$UPS_SCRIPT" 2>&1); code=$?
check_exit "case8: on-user-prompt-submit.sh exits 0 on unmatched session" 0 "$code"
check_eq "case8: no output (ups, unmatched)" "" "$out"
if [ -f "$log8ups" ]; then
  lines=$(wc -l < "$log8ups" | tr -d ' ')
  check_eq "case8: exactly one log line (ups, unmatched)" "1" "$lines"
  if grep -q 'EVENT=UserPromptSubmit' "$log8ups"; then
    pass=$((pass+1)); echo "PASS: case8: log line carries EVENT=UserPromptSubmit (unmatched)"
  else
    fail=$((fail+1)); echo "FAIL: case8: log line missing EVENT=UserPromptSubmit (unmatched)"
    echo "  actual: $(cat "$log8ups" 2>/dev/null)"
  fi
else
  fail=$((fail+1)); echo "FAIL: case8: AFK_HOOK_LOG set but no log file was written (ups, unmatched)"
fi

log8ptu="$ROOT/case8-ptu.log"
out=$(ptu_payload "sid-999" "$wt8" | AFK_HOOK_LOG="$log8ptu" "$PTU_SCRIPT" 2>&1); code=$?
check_exit "case8: on-post-tool-use.sh exits 0 on unmatched session" 0 "$code"
check_eq "case8: no output (ptu, unmatched)" "" "$out"
if [ -f "$log8ptu" ]; then
  lines=$(wc -l < "$log8ptu" | tr -d ' ')
  check_eq "case8: exactly one log line (ptu, unmatched)" "1" "$lines"
  if grep -q 'EVENT=PostToolUse' "$log8ptu"; then
    pass=$((pass+1)); echo "PASS: case8: log line carries EVENT=PostToolUse (unmatched)"
  else
    fail=$((fail+1)); echo "FAIL: case8: log line missing EVENT=PostToolUse (unmatched)"
    echo "  actual: $(cat "$log8ptu" 2>/dev/null)"
  fi
else
  fail=$((fail+1)); echo "FAIL: case8: AFK_HOOK_LOG set but no log file was written (ptu, unmatched)"
fi

rm -rf "$ROOT"
echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ] || exit 1
