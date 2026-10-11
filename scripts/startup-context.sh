#!/usr/bin/env bash
#
# startup-context.sh: show how much context each agent of one session carried
# on its first model call, and what that context was made of.
#
# Why this exists: every agent starts with a block of context (system prompt,
# tool definitions, the skills list, connector listings, CLAUDE.md) before it
# does any work, and that block is paid for again by every agent a session
# spawns. This script itemises it from the session's transcripts so a change to
# the agent definitions can be measured before and after.
#
# It prints sizes and metadata only (agent description, agent type, model, tool
# names). It never prints prompts, instructions or any other transcript text.
#
# The transcript layout it reads is the agent CLI's own and is not a published
# contract. Every field is read defensively: a missing field prints as 0 or
# "unknown" rather than failing, and a line that is not valid JSON (the last
# line of a transcript that is still being written) is skipped.

set -euo pipefail

usage() {
	cat <<'EOF'
Usage: startup-context.sh [PROJECTS_DIR] SESSION_ID

Print the start-up context of every agent in one session: the main session and
each sub-agent it spawned.

  PROJECTS_DIR  the folder that holds one sub-folder per project, each with the
                session transcripts (default: ~/.claude/projects)
  SESSION_ID    the session to read, for example
                1a10f95c-966b-434c-b2a9-996c9abf3bec

For each agent it prints:
  - the description, agent type and model
  - the token usage of its first model call; the sum of the three input fields
    is the start-up size
  - the size in characters of everything recorded before that first call, by
    kind, and of each tool definition

Sizes only. No transcript text is printed.

Exit codes: 0 printed, 1 no such directory or session, 2 wrong arguments.
EOF
}

case "${1:-}" in
-h | --help)
	usage
	exit 0
	;;
esac

if [ "$#" -eq 1 ]; then
	projects_dir="$HOME/.claude/projects"
	session_id="$1"
elif [ "$#" -eq 2 ]; then
	projects_dir="$1"
	session_id="$2"
else
	usage >&2
	exit 2
fi

if ! command -v jq >/dev/null 2>&1; then
	echo "startup-context: jq is not installed. Install jq and run this again." >&2
	exit 1
fi

if [ ! -d "$projects_dir" ]; then
	echo "startup-context: no projects directory at $projects_dir. Pass the folder that holds the session transcripts." >&2
	exit 1
fi

# The session id becomes part of a file-name pattern below, so anything that is
# not a plain id (a slash, a glob character) is refused here instead of being
# allowed to match some other file.
if [[ ! "$session_id" =~ ^[A-Za-z0-9._-]+$ ]]; then
	echo "startup-context: no transcript was found for session $session_id under $projects_dir." >&2
	exit 1
fi

shopt -s nullglob

# A session's transcript sits in the folder of the project it ran in, and the
# caller knows the session id, not the folder, so every project is searched.
mains=("$projects_dir"/*/"$session_id".jsonl)
if [ "${#mains[@]}" -eq 0 ]; then
	echo "startup-context: no transcript was found for session $session_id under $projects_dir." >&2
	exit 1
fi

# The jq program reads one transcript as raw lines (-R) and never slurps it: a
# main transcript can run to hundreds of megabytes, and only the lines up to the
# first model call plus that call's own lines matter.
#
# Counting rule (docs/PLAN.md section 4.7): one message id is written on several
# lines, one per streamed content block, and the usage on the early lines is
# incomplete. So the first call's usage is the maximum of each field over every
# line that carries its id, not the value on the first line.
read -r -d '' jq_program <<'JQ' || true
# Characters of text in a message body or a rendered attachment. Both are a
# string or a list of blocks whose text sits under .text or .content.
def textlen:
  if type == "string" then length
  elif type == "array" then
    (map(
      if type == "string" then length
      elif type == "object" then
        ((.text // .content // "") | if type == "string" then length else (tojson | length) end)
      else (tojson | length) end
    ) | add // 0)
  elif type == "null" then 0
  else (tojson | length) end;

def maxof($a; $b): if ($a // 0) > ($b // 0) then ($a // 0) else ($b // 0) end;

reduce (inputs | (try fromjson catch null) | select(type == "object")) as $l (
  {first: null, model: null, usage: {}, sent: {}, unsent: {}, system: 0, user: 0, tools: null};

  # Tool definitions are recorded in a prompt snapshot, and on this CLI version
  # the snapshot before the first call often carries the system prompt alone:
  # the one with the tools is written just after. They are part of the first
  # call either way, so the first snapshot that lists them is used wherever it
  # sits, and the output says which side of the first call it was on.
  (if .tools == null
      and $l.type == "attachment"
      and $l.attachment.type == "prompt_snapshot"
      and (($l.attachment.tools | type) == "array")
   then .tools = {
          before: (.first == null),
          list: ($l.attachment.tools | map({name: ((.name? // "unnamed") | tostring), chars: (tojson | length)}))
        }
   else . end)

  | if $l.type == "assistant"
       and (($l.message.usage | type) == "object")
       and $l.message.model != "<synthetic>"
    then
      (($l.message.id // $l.uuid // "unknown") | tostring) as $id
      | (if .first == null then .first = $id | .model = ($l.message.model // "unknown") else . end)
      | if $id == .first then
          .usage.input = maxof(.usage.input; $l.message.usage.input_tokens)
          | .usage.write = maxof(.usage.write; $l.message.usage.cache_creation_input_tokens)
          | .usage.read = maxof(.usage.read; $l.message.usage.cache_read_input_tokens)
        else . end
    elif .first != null then .
    elif $l.type == "attachment" then
      (($l.attachment.type // "unknown") | tostring) as $t
      | if $t == "prompt_snapshot" then
          # A snapshot restates the whole system prompt, so two of them before
          # the first call are the same prompt twice, not twice the prompt.
          .system = maxof(.system; ($l.attachment.systemPrompt | textlen))
        elif $l.rendered != null then
          .sent[$t] = ((.sent[$t] // 0) + ($l.rendered | textlen))
        else
          .unsent[$t] = ((.unsent[$t] // 0) + ($l.attachment | tojson | length))
        end
    elif $l.type == "user" then
      .user += ($l.message.content | textlen)
    else . end
)
| "agent: \($agent)",
  "description: \($description)",
  "agentType: \($agent_type)",
  "model: \($meta_model) (first call answered by \(.model // "unknown"))",
  (if .first == null then
     "first call: none found, this agent made no model call"
   else
     "first call usage (tokens):",
     "  input_tokens: \(.usage.input // 0)",
     "  cache_creation_input_tokens: \(.usage.write // 0)",
     "  cache_read_input_tokens: \(.usage.read // 0)",
     "  start-up size: \((.usage.input // 0) + (.usage.write // 0) + (.usage.read // 0))"
   end),
  "before the first call (characters):",
  "  system prompt (prompt_snapshot): \(.system)",
  (.sent | to_entries | sort_by(-.value) | .[] | "  attachment \(.key): \(.value)"),
  "  user prompt: \(.user)",
  (.unsent | to_entries | sort_by(-.value) | .[]
   | "  attachment \(.key): \(.value) (size of the record; it has no rendered text)"),
  (if .tools == null then
     "tool definitions: none recorded"
   else
     "tool definitions (characters, recorded \(if .tools.before then "before" else "after" end) the first call): \(.tools.list | length) tools, \(.tools.list | map(.chars) | add // 0) in all",
     (.tools.list | sort_by(-.chars) | .[] | "  tool \(.name): \(.chars)")
   end)
JQ

# meta_field FILE KEY prints one field of an agent's .meta.json, or "unknown"
# when the file is missing, unreadable or lacks the field. The main session has
# no such file at all.
meta_field() {
	local file="$1" key="$2" value=""
	if [ -f "$file" ]; then
		value="$(jq -r --arg k "$key" '.[$k] // empty | tostring' "$file" 2>/dev/null || true)"
	fi
	printf '%s' "${value:-unknown}"
}

print_block() {
	local transcript="$1" agent="$2" description="$3" agent_type="$4" meta_model="$5"
	jq -n -R -r \
		--arg agent "$agent" \
		--arg description "$description" \
		--arg agent_type "$agent_type" \
		--arg meta_model "$meta_model" \
		"$jq_program" "$transcript"
	echo
}

for main in "${mains[@]}"; do
	project="$(basename "$(dirname "$main")")"
	echo "session: $session_id"
	echo "project folder: $project"
	echo

	print_block "$main" "main session" "main session" "main" "unknown"

	# Sub-agents spawned through the Agent tool sit directly in subagents/;
	# those spawned by a workflow script sit one level down, per workflow run.
	subagents="${main%.jsonl}/subagents"
	for transcript in "$subagents"/agent-*.jsonl "$subagents"/workflows/wf_*/agent-*.jsonl; do
		meta="${transcript%.jsonl}.meta.json"
		print_block "$transcript" \
			"${transcript#"$subagents"/}" \
			"$(meta_field "$meta" description)" \
			"$(meta_field "$meta" agentType)" \
			"$(meta_field "$meta" model)"
	done
done
