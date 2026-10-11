#!/usr/bin/env bash
# trim-transcript.sh PROJECTS_DIR SESSION_ID OUT_DIR
#
# Copies one Claude Code session's transcripts into OUT_DIR, reduced to the
# fields a token reader needs, so the result can be committed as a fixture
# without prompt text, file contents, request ids or home paths.
#
#   PROJECTS_DIR  a projects folder, e.g. ~/.claude/projects; the main file is
#                 found as PROJECTS_DIR/*/SESSION_ID.jsonl
#   SESSION_ID    the session to trim
#   OUT_DIR       must not exist; it is created holding SESSION_ID.jsonl and
#                 SESSION_ID/subagents/... laid out as on disk
#
# The source is only read. The output is built in a sibling folder and renamed
# into place at the end, so a failure never leaves a half-written OUT_DIR.
# Output is deterministic (sorted file order, fixed jq programs), so a second
# run produces a byte-identical tree.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: trim-transcript.sh PROJECTS_DIR SESSION_ID OUT_DIR" >&2
  exit 2
fi
projects=$1
sid=$2
out=$3

command -v jq >/dev/null || { echo "trim-transcript: jq is required" >&2; exit 1; }

# Glob by session id, the same way the reader locates it. nullglob so an
# unmatched pattern is an empty list rather than the literal pattern.
shopt -s nullglob
matches=("$projects"/*/"$sid".jsonl)
shopt -u nullglob
if [ "${#matches[@]}" -eq 0 ]; then
  echo "trim-transcript: no transcript $sid.jsonl under $projects" >&2
  exit 1
fi
main=${matches[0]}
sidedir=${main%.jsonl}

if [ -e "$out" ]; then
  echo "trim-transcript: output folder $out already exists" >&2
  exit 1
fi

tmp="$out.partial.$$"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp"

# Keep assistant lines that carry a message id. Content is emptied except for
# a Bash tool_use that writes a phase into status.json; that one is rewritten
# to a canonical command so none of the original text is copied.
# The phase word is the last `.phase = "word"` in the command.
read -r -d '' trim_jq <<'JQ' || true
def phase_of($cmd):
  [$cmd | match("\\.phase = \"(\\w+)\""; "g") | .captures[0].string] | last;
select(.type == "assistant" and ((.message.id // "") != ""))
| {type, timestamp, isSidechain, agentId, sessionId}
  + {message: {
      id: .message.id,
      model: .message.model,
      role: .message.role,
      usage: {
        input_tokens: .message.usage.input_tokens,
        output_tokens: .message.usage.output_tokens,
        cache_creation_input_tokens: .message.usage.cache_creation_input_tokens,
        cache_read_input_tokens: .message.usage.cache_read_input_tokens
      },
      content: [
        (.message.content // [])[]
        | select(.type == "tool_use" and .name == "Bash"
                 and ((.input.command // "") | type == "string")
                 and (.input.command | contains("status.json"))
                 and (.input.command | test("\\.phase = \"\\w+\"")))
        | {type: "tool_use", name: "Bash",
           input: {command: ("jq '.phase = \"" + phase_of(.input.command) + "\"' .afk/t0-4/status.json")}}
      ]
    }}
| with_entries(select(.value != null))
JQ

trim_file() { # src dst
  mkdir -p "$(dirname "$2")"
  jq -c "$trim_jq" "$1" >"$2"
}

trim_meta() { # src dst
  mkdir -p "$(dirname "$2")"
  jq -c '{agentType, description, workflowPhase, model} | with_entries(select(.value != null))' "$1" >"$2"
}

trim_file "$main" "$tmp/$sid.jsonl"

if [ -d "$sidedir/subagents" ]; then
  # Sorted by path (LC_ALL=C) so the run order does not depend on the locale.
  while IFS= read -r f; do
    rel=${f#"$sidedir"/}
    case "$f" in
      *.meta.json) trim_meta "$f" "$tmp/$sid/$rel" ;;
      *.jsonl)
        # journal.jsonl is workflow bookkeeping, not a transcript.
        [ "$(basename "$f")" = journal.jsonl ] && continue
        trim_file "$f" "$tmp/$sid/$rel" ;;
    esac
  done < <(find "$sidedir/subagents" -type f \( -name 'agent-*.jsonl' -o -name 'agent-*.meta.json' \) | LC_ALL=C sort)
fi

mkdir -p "$(dirname "$out")"
mv "$tmp" "$out"
trap - EXIT
