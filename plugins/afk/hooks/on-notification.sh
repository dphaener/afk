#!/usr/bin/env bash
# Notification hook (W3.T3; W6.T1 ownership-split amendment). Implements
# status-schema.md section 5's narrowed S1 transition and section 9's
# notification-ownership table: the hook writes awaiting_input=true AND
# toasts on EXACTLY two cases -
#   (a) notification_type == permission_prompt, any matched non-terminal phase
#   (b) notification_type == idle_prompt AND the matched phase is planning AND
#       the background-tasks sidecar (section 10) reports no pending tasks
# Every other Notification (AskUserQuestion display, idle_prompt outside
# planning, any other type) is either the conductor's to announce (deliberate
# stops, S2-S4) or a self-resuming false positive (non-planning idle while
# background work runs) - the hook writes nothing and toasts nothing.
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

  hook_log "Notification" "$payload"

  local sid cwd ntype msg
  sid="$(printf '%s' "$payload" | jq -r '.session_id // ""' 2>/dev/null)"
  cwd="$(printf '%s' "$payload" | jq -r '.cwd // ""' 2>/dev/null)"
  ntype="$(printf '%s' "$payload" | jq -r '.notification_type // ""' 2>/dev/null)"
  msg="$(printf '%s' "$payload" | jq -r '.message // "Claude needs your attention"' 2>/dev/null)"
  [ -n "$cwd" ] || return 0

  find_target "$cwd" "$sid"
  [ -n "$MATCH_FILE" ] || return 0

  # Section 5 (S1) / section 9 ownership: decide whether the hook owns this
  # moment BEFORE writing. The write and the toast are coupled - the hook
  # only ever asserts awaiting_input=true on a moment it is also going to
  # toast, and vice versa.
  local act=0
  case "$ntype" in
    permission_prompt)
      # (a) A permission prompt pauses the very tool call the conductor is
      # trying to make, so the conductor cannot self-report it, and it is a
      # genuine block regardless of any background work. Any phase.
      act=1
      ;;
    idle_prompt)
      # (b) Idle only means "user walked away" when no background work can
      # self-resume the session. Phase alone no longer proves that: the
      # planner also spawns research/Crucible agents during planning, so an
      # idle_prompt there can be the same self-resuming false positive as
      # executing/reviewing/fixing (the W6.T1 class) rather than a genuine
      # stop. The background-tasks sidecar is the documented busy signal for
      # that (status-schema.md section 10) - act only when planning AND the
      # sidecar reports nothing pending.
      if [ "$MATCH_PHASE" = "planning" ] && ! background_tasks_pending "$(dirname "$MATCH_FILE")"; then
        act=1
      fi
      ;;
    *)
      # AskUserQuestion display and every other Notification type: the
      # conductor owns deliberate stops (S2-S4) and toasts them itself.
      act=0
      ;;
  esac

  [ "$act" -eq 1 ] || return 0

  # S1: assert awaiting_input=true, then toast (coupled - section 9).
  atomic_write_awaiting_input "$MATCH_FILE" true

  # Mark a permission-prompt SET so on-post-tool-use.sh knows this flag is its
  # to clear once the granted tool runs. Only permission prompts get the
  # sentinel: an idle_prompt SET has no grant event to resolve it, and the
  # conductor's deliberate stops (S2-S4) are never stale.
  if [ "$ntype" = "permission_prompt" ]; then
    : > "$(dirname "$MATCH_FILE")/.awaiting-permission" 2>/dev/null || true
  fi

  local worktree
  worktree="$(basename "$cwd" 2>/dev/null)"
  "$HOOK_DIR/notify.sh" "${MATCH_PLAN_NAME:-afk session}" "$worktree" "$msg" >/dev/null 2>&1

  return 0
}

main
exit 0
