#!/usr/bin/env bash
# PreToolUse hook, registered for the AskUserQuestion tool only.
#
# It does nothing on purpose. Its registration is what makes the agent CLI
# write the open question's tool_use line to the transcript before the dialog
# opens (seen on claude 2.1.299 on 2026-10-14, evidence
# .afk/t8-4/evidence/w4-t1-live-choice.txt). Without a PreToolUse hook the
# line is written only together with the answer, so the web page's chat
# (T8.3, T8.4) reads "working" while a question is open and refuses a choice.
#
# It runs before the dialog, so it must stay fast and silent: no files
# written, nothing printed, never blocking. MUST exit 0 unconditionally - no
# hook script may ever fail the session.

HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 0
# shellcheck source=./lib.sh
source "$HOOK_DIR/lib.sh" 2>/dev/null || exit 0

main() {
  have_jq || return 0

  local payload
  payload="$(cat)" || return 0
  [ -n "$payload" ] || return 0
  printf '%s' "$payload" | jq -e . >/dev/null 2>&1 || return 0

  hook_log "PreToolUse" "$payload"
  return 0
}

main
exit 0
