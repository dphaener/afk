#!/usr/bin/env bash
# Desktop notifier for afk session status (W3.T3). Probes in order:
# afk notify -> terminal-notifier -> osascript (macOS) -> notify-send
# (Linux) -> silent no-op. No notifier is ever required to be installed,
# and this script MUST exit 0 unconditionally - a missing/broken notifier
# is never a session failure.
#
# `afk` is the afk binary. This plugin never installs it.
#
# The probe is `afk notify --check`, NOT `command -v afk`, because a
# present-but-old binary passes the existence check then exits 1 for every
# toast (verified failure: a stale ~/bin/afk was byte-identical to a build
# lacking the notify subcommand; `command -v` returned success while
# `afk notify` answered "Unknown subcommand" and failed silently for every
# notification). The probe also rejects an unrelated binary named `afk`
# that happens to be on PATH.
#
# Usage: notify.sh <plan_name> <worktree> <message>

plan_name="${1:-afk session}"
worktree="${2:-}"
message="${3:-Claude needs your attention}"

title="afk: ${plan_name}"
if [ -n "$worktree" ]; then
  body="${message} (${worktree})"
else
  body="${message}"
fi

if afk notify --check >/dev/null 2>&1; then
  # Delegate to the binary. Let `afk` derive the repo identity from the
  # git common dir (basename $PWD is the session name here, not the repo,
  # and passing it would void the group-collision guarantee). Call, do NOT
  # exec: exec replaces the shell and the script's final `exit 0` would
  # never run, so afk's exit status would become the script's.
  afk notify --plan "$plan_name" --message "$message" >/dev/null 2>&1
elif command -v terminal-notifier >/dev/null 2>&1; then
  terminal-notifier -title "$title" -message "$body" >/dev/null 2>&1
elif command -v osascript >/dev/null 2>&1; then
  # Escape BACKSLASHES FIRST, then double quotes. The order is the whole point:
  # reversing it re-escapes the backslash this step just added, and quoting alone
  # (what this used to do) leaves a lone backslash in the AppleScript string
  # literal, where it starts an escape sequence and osascript aborts with
  # "syntax error ... (-2740/-2741)" and delivers nothing.
  esc_title="${title//\\/\\\\}"
  esc_title="${esc_title//\"/\\\"}"
  esc_body="${body//\\/\\\\}"
  esc_body="${esc_body//\"/\\\"}"
  osascript -e "display notification \"${esc_body}\" with title \"${esc_title}\"" >/dev/null 2>&1
elif command -v notify-send >/dev/null 2>&1; then
  notify-send "$title" "$body" >/dev/null 2>&1
fi

exit 0
