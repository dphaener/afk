#!/usr/bin/env bash
# Stop hook (W4.T1). Implements status-schema.md section 10's writer side of
# the `.background-tasks` sidecar: every Stop event whose payload matches a
# session under section 7's targeting rule (find_target) gets its
# background_tasks array persisted atomically beside that session's
# status.json. MUST exit 0 unconditionally - no hook script may ever fail
# the session. Never touches status.json.

HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 0
# shellcheck source=./lib.sh
source "$HOOK_DIR/lib.sh" 2>/dev/null || exit 0

main() {
  have_jq || return 0

  local payload
  payload="$(cat)" || return 0
  [ -n "$payload" ] || return 0
  printf '%s' "$payload" | jq -e . >/dev/null 2>&1 || return 0

  hook_log "Stop" "$payload"

  local sid cwd
  sid="$(printf '%s' "$payload" | jq -r '.session_id // ""' 2>/dev/null)"
  cwd="$(printf '%s' "$payload" | jq -r '.cwd // ""' 2>/dev/null)"
  [ -n "$cwd" ] || return 0

  find_target "$cwd" "$sid"
  [ -n "$MATCH_FILE" ] || return 0

  write_background_tasks_sidecar "$(dirname "$MATCH_FILE")" "$payload"
  # A completed turn after a StopFailure proves the limit has lifted or the
  # turn works again; this covers an agent CLI that waits it out and continues
  # with no prompt submitted (status-schema.md section 10). Both failure
  # markers go.
  rm -f "$(dirname "$MATCH_FILE")/.rate-limited" \
    "$(dirname "$MATCH_FILE")/.turn-failed" 2>/dev/null
  return 0
}

main
exit 0
