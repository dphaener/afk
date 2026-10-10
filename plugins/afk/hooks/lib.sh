#!/usr/bin/env bash
# Shared helpers for afk session-status hooks (W3.T3). Implements the
# hook targeting rule, the status.json write protocol, and the
# background-tasks sidecar / opt-in event log exactly as specified in
# plugins/afk/skills/conductor/status-schema.md sections 5, 6, 7 and 10.
# This file is sourced, never executed directly, and MUST NOT itself
# decide to exit the calling script - callers own exit codes.
#
# Pure transcription: no design decisions live here, only the mechanics
# already decided in status-schema.md.

# have_jq: hooks may probe for jq but must degrade (no reads, no writes)
# rather than error when it is unavailable.
have_jq() {
  command -v jq >/dev/null 2>&1
}

# find_target CWD SESSION_ID
#
# Implements status-schema.md section 7's targeting rule against
# {cwd}/.afk/*/status.json. On match, sets the globals MATCH_FILE,
# MATCH_PLAN_NAME, MATCH_PHASE, MATCH_BRANCH. On no match (including the
# "ambiguity acts on nothing" case), MATCH_FILE is left empty.
find_target() {
  local cwd="$1" sid="$2"
  MATCH_FILE=""
  MATCH_PLAN_NAME=""
  MATCH_PHASE=""
  MATCH_BRANCH=""

  [ -n "$cwd" ] && [ -d "$cwd" ] || return 0

  local primary=() fallback=()
  local f json fsid fphase

  shopt -s nullglob
  for f in "$cwd"/.afk/*/status.json; do
    json="$(cat "$f" 2>/dev/null)" || continue
    [ -n "$json" ] || continue
    printf '%s' "$json" | jq -e . >/dev/null 2>&1 || continue

    fsid="$(printf '%s' "$json" | jq -r '.session_id // ""' 2>/dev/null)"
    fphase="$(printf '%s' "$json" | jq -r '.phase // ""' 2>/dev/null)"

    if [ "$fsid" = "$sid" ] && [ "$fphase" != "done" ] && [ "$fphase" != "failed" ]; then
      primary+=("$f")
    fi
    if [ -z "$fsid" ] && [ "$fphase" = "planning" ]; then
      fallback+=("$f")
    fi
  done
  shopt -u nullglob

  local target=""
  if [ "${#primary[@]}" -eq 1 ]; then
    target="${primary[0]}"
  elif [ "${#primary[@]}" -eq 0 ] && [ "${#fallback[@]}" -eq 1 ]; then
    target="${fallback[0]}"
  else
    # Zero matches, or ambiguity in either bucket: act on nothing.
    return 0
  fi

  json="$(cat "$target" 2>/dev/null)" || return 0
  printf '%s' "$json" | jq -e . >/dev/null 2>&1 || return 0

  MATCH_FILE="$target"
  MATCH_PLAN_NAME="$(printf '%s' "$json" | jq -r '.plan_name // ""' 2>/dev/null)"
  MATCH_PHASE="$(printf '%s' "$json" | jq -r '.phase // ""' 2>/dev/null)"
  MATCH_BRANCH="$(printf '%s' "$json" | jq -r '.branch // ""' 2>/dev/null)"
}

# atomic_write_awaiting_input FILE true|false
#
# status-schema.md section 6 write protocol: re-read immediately before
# rename, modify only awaiting_input + updated_at, write to a same-directory
# temp file, atomic rename over the target. Every other field passes through
# unchanged because it is carried by the freshly re-read copy. Silent no-op
# on any failure (missing file, unparseable JSON, write error) - callers
# always exit 0 regardless.
atomic_write_awaiting_input() {
  local file="$1" value="$2"
  [ -f "$file" ] || return 0

  local dir json now tmp
  dir="$(dirname "$file")"

  # Re-read immediately before the write - this IS the merge base.
  json="$(cat "$file" 2>/dev/null)" || return 0
  [ -n "$json" ] || return 0
  printf '%s' "$json" | jq -e . >/dev/null 2>&1 || return 0

  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  tmp="$dir/.status.json.tmp.$$"

  if ! printf '%s' "$json" | jq --argjson v "$value" --arg now "$now" \
      '.awaiting_input = $v | .updated_at = $now' > "$tmp" 2>/dev/null; then
    rm -f "$tmp" 2>/dev/null
    return 0
  fi

  if [ -s "$tmp" ]; then
    mv -f "$tmp" "$file" 2>/dev/null
  else
    rm -f "$tmp" 2>/dev/null
  fi
  return 0
}

# hook_log EVENT PAYLOAD
#
# status-schema.md section 10 "Opt-in hook event log (AFK_HOOK_LOG)":
# when that variable names a non-empty path, appends one line per hook
# invocation to it. Callers invoke this immediately after the payload
# passes `jq -e .` validation and before find_target, so unmatched events
# are logged too. Opt-in and silent-on-failure throughout: an unset
# AFK_HOOK_LOG writes nothing, and any error (bad path, permissions, an
# unparseable payload) is swallowed rather than surfaced.
hook_log() {
  [ -n "${AFK_HOOK_LOG:-}" ] || return 0

  local event="$1" payload="$2" now compact
  [ -n "$event" ] || event="?"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  compact="$(printf '%s' "$payload" | jq -c . 2>/dev/null)" || return 0

  # 2>/dev/null FIRST: redirections are applied left to right, so with the
  # append written first the shell's own "No such file or directory" for an
  # unwritable AFK_HOOK_LOG path reaches the real stderr before stderr is
  # silenced. This ordering is what makes the failure actually silent.
  printf '[%s] EVENT=%s PAYLOAD=%s\n' "$now" "$event" "$compact" \
    2>/dev/null >> "$AFK_HOOK_LOG"
  return 0
}

# write_background_tasks_sidecar DIR PAYLOAD
#
# status-schema.md section 10: writer side of the `.background-tasks`
# sidecar. Extracts the Stop payload's background_tasks array as compact
# JSON (a missing field writes "[]") and atomically replaces
# DIR/.background-tasks via a same-directory temp file, per section 6's
# write protocol. Never touches status.json.
write_background_tasks_sidecar() {
  local dir="$1" payload="$2" bg tmp
  [ -n "$dir" ] && [ -d "$dir" ] || return 0

  bg="$(printf '%s' "$payload" | jq -c '.background_tasks // []' 2>/dev/null)" || return 0
  [ -n "$bg" ] || return 0

  tmp="$dir/.background-tasks.tmp.$$"
  # 2>/dev/null precedes the truncation for the same reason as in hook_log: a
  # redirection that fails (read-only directory) reports through whatever
  # stderr is bound at the moment that redirection is applied.
  if ! printf '%s\n' "$bg" 2>/dev/null > "$tmp"; then
    rm -f "$tmp" 2>/dev/null
    return 0
  fi

  mv -f "$tmp" "$dir/.background-tasks" 2>/dev/null || rm -f "$tmp" 2>/dev/null
  return 0
}

# background_tasks_pending DIR
#
# status-schema.md section 10: reader side of the sidecar, used only from
# on-notification.sh's idle_prompt + planning branch. "Pending" requires
# DIR/.background-tasks to exist, parse as a JSON array, and have
# length > 0; every other outcome (missing, unparseable, not an array,
# empty array) is "not pending". The one helper here whose return value
# callers branch on: 0 for pending, 1 otherwise.
background_tasks_pending() {
  local dir="$1" file="$1/.background-tasks" json len
  [ -f "$file" ] || return 1

  json="$(cat "$file" 2>/dev/null)" || return 1
  [ -n "$json" ] || return 1
  printf '%s' "$json" | jq -e 'type == "array"' >/dev/null 2>&1 || return 1

  len="$(printf '%s' "$json" | jq 'length' 2>/dev/null)" || return 1
  if [ -n "$len" ] && [ "$len" -gt 0 ] 2>/dev/null; then
    return 0
  fi
  return 1
}

# clear_background_tasks_sidecar DIR
#
# status-schema.md section 10: clearer side of the sidecar, called from
# on-user-prompt-submit.sh alongside its existing awaiting_input clear. A
# user prompt means the user is present and a new turn is beginning; the
# next Stop event rewrites the sidecar from that turn's own background
# work.
clear_background_tasks_sidecar() {
  local dir="$1"
  rm -f "$dir/.background-tasks" 2>/dev/null
  return 0
}
