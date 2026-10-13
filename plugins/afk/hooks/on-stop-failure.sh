#!/usr/bin/env bash
# StopFailure hook (T0.2 W1.T5, widened by T2.5 W2.T1). Writes one of the two
# failure markers described in status-schema.md section 10, for the session
# that find_target (section 7) matches, picked by the payload's `error` field:
#   rate_limit        -> `.afk/{name}/.rate-limited`, {"at":"<RFC 3339 UTC>"}
#   a retryable error -> `.afk/{name}/.turn-failed`,
#                        {"at":"<RFC 3339 UTC>","reason":"<error>"}
#   anything else, or no `error` -> nothing.
# Never both markers for one failure. hooks.json registers the script for
# `rate_limit` plus the retryable list, but the script checks `error` itself
# so a wider matcher can never write a marker the list does not allow. The
# markers are cleared by on-user-prompt-submit.sh and on-stop.sh. MUST exit 0
# unconditionally - no hook script may ever fail the session. Never touches
# status.json.

# Reasons worth trying the turn again, from the `error` values documented for
# StopFailure (hooks reference) and confirmed by the T2.5 capture: a lost
# connection reports `server_error`. Keep in step with the hooks.json matcher.
# The rest are left out because trying again cannot fix them: a login
# (authentication_failed, oauth_org_not_allowed, account_on_hold,
# cloud_credential_error), a billing fault (billing_error), a bad request
# (invalid_request, max_output_tokens) or a missing model (model_not_found).
RETRYABLE_ERRORS=" overloaded server_error unknown "

HOOK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || exit 0
# shellcheck source=./lib.sh
source "$HOOK_DIR/lib.sh" 2>/dev/null || exit 0

# write_marker DIR NAME JQ_FILTER REASON: one-line JSON through a temp file
# and mv -f, so a reader never sees half a line.
write_marker() {
  local dir="$1" name="$2" filter="$3" reason="$4" tmp now
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  tmp="$dir/$name.tmp.$$"
  # 2>/dev/null precedes the truncation so a failing redirection (read-only
  # directory) stays silent, as in write_background_tasks_sidecar.
  if ! jq -nc --arg at "$now" --arg reason "$reason" "$filter" 2>/dev/null > "$tmp"; then
    rm -f "$tmp" 2>/dev/null
    return 0
  fi
  if [ -s "$tmp" ]; then
    mv -f "$tmp" "$dir/$name" 2>/dev/null || rm -f "$tmp" 2>/dev/null
  else
    rm -f "$tmp" 2>/dev/null
  fi
  return 0
}

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

  local err
  err="$(printf '%s' "$payload" | jq -r '.error // ""' 2>/dev/null)"
  [ -n "$err" ] || return 0

  local dir
  dir="$(dirname "$MATCH_FILE")"
  if [ "$err" = "rate_limit" ]; then
    write_marker "$dir" ".rate-limited" '{at:$at}' "$err"
  else
    case "$RETRYABLE_ERRORS" in
      *" $err "*) write_marker "$dir" ".turn-failed" '{at:$at,reason:$reason}' "$err" ;;
    esac
  fi
  return 0
}

main
exit 0
