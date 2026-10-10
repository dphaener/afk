#!/usr/bin/env bash
# PostToolUse hook. Closes status-schema.md section 5's "Known, accepted
# staleness window": granting a permission prompt fires no event, so the
# awaiting_input=true that the Notification hook set (S1a) survives while the
# session works on, and the dashboard shows a stale input badge for minutes.
#
# A granted permission is always followed by the tool running, so PostToolUse
# is the event that observes the grant. It clears the flag ONLY when a
# permission prompt is what set it - marked by the sentinel the Notification
# hook drops beside status.json. Without that gate this hook would also clear
# the conductor's own deliberate stops (S2/S3/S4), which it sets via a Bash
# call and which are never stale - blanking the badge during a real block.
# MUST exit 0 unconditionally - no hook script may ever fail the session.

HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 0
# shellcheck source=./lib.sh
source "$HOOK_DIR/lib.sh" 2>/dev/null || exit 0

main() {
  have_jq || return 0

  local payload
  payload="$(cat)" || return 0
  [ -n "$payload" ] || return 0
  printf '%s' "$payload" | jq -e . >/dev/null 2>&1 || return 0

  hook_log "PostToolUse" "$payload"

  local sid cwd
  sid="$(printf '%s' "$payload" | jq -r '.session_id // ""' 2>/dev/null)"
  cwd="$(printf '%s' "$payload" | jq -r '.cwd // ""' 2>/dev/null)"
  [ -n "$cwd" ] || return 0

  find_target "$cwd" "$sid"
  [ -n "$MATCH_FILE" ] || return 0

  # The gate: only a permission-prompt SET is ours to clear. No sentinel means
  # the flag came from a conductor stop (or nothing set it at all) - leave it.
  local sentinel
  sentinel="$(dirname "$MATCH_FILE")/.awaiting-permission"
  [ -f "$sentinel" ] || return 0

  rm -f "$sentinel" 2>/dev/null
  atomic_write_awaiting_input "$MATCH_FILE" false
  return 0
}

main
exit 0
