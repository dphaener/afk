#!/usr/bin/env bash
# StopFailure hook (T0.2 W1.T5). Writes the rate-limit marker described in
# status-schema.md section 10: `.afk/{name}/.rate-limited`, one line
# {"at":"<RFC 3339 UTC>"}, for the session that find_target (section 7)
# matches. hooks.json registers it with the `rate_limit` matcher, so this
# script never looks at the failure reason itself: Claude Code only runs it
# for that reason, and the payload's cwd and session_id are the only fields
# it relies on. The marker is cleared by on-user-prompt-submit.sh and
# on-stop.sh. MUST exit 0 unconditionally - no hook script may ever fail
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

  hook_log "StopFailure" "$payload"

  local sid cwd
  sid="$(printf '%s' "$payload" | jq -r '.session_id // ""' 2>/dev/null)"
  cwd="$(printf '%s' "$payload" | jq -r '.cwd // ""' 2>/dev/null)"
  [ -n "$cwd" ] || return 0

  find_target "$cwd" "$sid"
  [ -n "$MATCH_FILE" ] || return 0

  local dir tmp now
  dir="$(dirname "$MATCH_FILE")"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  tmp="$dir/.rate-limited.tmp.$$"
  # 2>/dev/null precedes the truncation so a failing redirection (read-only
  # directory) stays silent, as in write_background_tasks_sidecar.
  if ! jq -nc --arg at "$now" '{at:$at}' 2>/dev/null > "$tmp"; then
    rm -f "$tmp" 2>/dev/null
    return 0
  fi
  if [ -s "$tmp" ]; then
    mv -f "$tmp" "$dir/.rate-limited" 2>/dev/null || rm -f "$tmp" 2>/dev/null
  else
    rm -f "$tmp" 2>/dev/null
  fi
  return 0
}

main
exit 0
