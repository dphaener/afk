#!/usr/bin/env bash
# UserPromptSubmit hook (W3.T3). Implements status-schema.md section 5's
# C1 transition (awaiting_input -> false) and clears the background-tasks
# sidecar (§10) - the user submitting a typed prompt or answering an
# AskUserQuestion dialog both fire this event and both clear the flag via
# this single path. User present → new turn → stale sidecar must not
# suppress a later genuine idle. MUST exit 0 unconditionally - no hook
# script may ever fail the session.

HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 0
# shellcheck source=./lib.sh
source "$HOOK_DIR/lib.sh" 2>/dev/null || exit 0

main() {
  have_jq || return 0

  local payload
  payload="$(cat)" || return 0
  [ -n "$payload" ] || return 0
  printf '%s' "$payload" | jq -e . >/dev/null 2>&1 || return 0

  hook_log "UserPromptSubmit" "$payload"

  local sid cwd
  sid="$(printf '%s' "$payload" | jq -r '.session_id // ""' 2>/dev/null)"
  cwd="$(printf '%s' "$payload" | jq -r '.cwd // ""' 2>/dev/null)"
  [ -n "$cwd" ] || return 0

  find_target "$cwd" "$sid"
  [ -n "$MATCH_FILE" ] || return 0

  clear_background_tasks_sidecar "$(dirname "$MATCH_FILE")"
  # A submitted prompt means the owner is back: the rate-limit marker (§10)
  # no longer describes the session.
  rm -f "$(dirname "$MATCH_FILE")/.rate-limited" 2>/dev/null
  atomic_write_awaiting_input "$MATCH_FILE" false
  return 0
}

main
exit 0
