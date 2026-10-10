#!/usr/bin/env bash
# check-cat-sizes.sh — Scans every skill file under plugins/*/skills/*/ for
# !`cat "${CLAUDE_PLUGIN_ROOT}/..."` directives and verifies each referenced
# file is under 28,000 characters (margin under the ~30,000 harness cap).
#
# Uses wc -m (characters, not bytes) to match the harness cap unit.
#
# Usage (from repo root or any directory):
#   bash plugins/afk/scripts/check-cat-sizes.sh
#
# Exit codes:
#   0 — all referenced files are within the cap
#   1 — one or more files exceed the cap, or a referenced file is missing

set -euo pipefail

# Force a UTF-8 locale so `wc -m` counts characters, not bytes. Under
# LC_CTYPE=C/POSIX (common on minimal CI images) `wc -m` degrades to byte
# counting, which would overcount multi-byte punctuation (em-dashes, smart
# quotes) the prompt files use heavily. C.UTF-8 is present in glibc and musl
# by default, so this is portable.
export LC_ALL=C.UTF-8

THRESHOLD=28000

# Always operate relative to repo root regardless of CWD.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

failed=0
checked=0

echo "Checking !cat injection sizes (threshold: ${THRESHOLD} chars) ..."

for skill_file in "${REPO_ROOT}"/plugins/*/skills/*/SKILL.md; do
  # Skip glob that didn't expand (no matching files).
  [ -e "$skill_file" ] || continue

  # The plugin root is the grandparent of the skills/<name>/ directory.
  plugin_dir="$(dirname "$(dirname "$(dirname "$skill_file")")")"

  # Find lines with !`cat "${CLAUDE_PLUGIN_ROOT}/..."` directives.
  # Use grep -F for a fixed-string match (portable, no PCRE needed),
  # then sed to extract just the relative path after ${CLAUDE_PLUGIN_ROOT}/.
  while IFS= read -r rel_path; do
    [ -n "$rel_path" ] || continue

    # Resolve to an absolute path: substitute ${CLAUDE_PLUGIN_ROOT} → plugin_dir.
    resolved="${plugin_dir}/${rel_path}"

    if [ ! -f "$resolved" ]; then
      echo "  ERROR: missing file referenced in $(basename "$(dirname "$skill_file")"): $resolved"
      failed=1
      continue
    fi

    char_count=$(wc -m < "$resolved")
    checked=$((checked + 1))

    if [ "$char_count" -gt "$THRESHOLD" ]; then
      echo "  FAIL: ${resolved} — ${char_count} chars (exceeds ${THRESHOLD})"
      failed=1
    else
      echo "  OK:   ${resolved} — ${char_count} chars"
    fi
  done < <(
    grep -F 'cat "${CLAUDE_PLUGIN_ROOT}/' "$skill_file" \
      | sed 's|.*cat "${CLAUDE_PLUGIN_ROOT}/\([^"]*\)".*|\1|' \
      2>/dev/null \
    || true
  )
done

echo ""
if [ "$failed" -ne 0 ]; then
  echo "RESULT: FAILED — one or more !cat injections exceed ${THRESHOLD} chars or reference missing files"
  exit 1
fi

echo "RESULT: OK — ${checked} !cat injection(s) checked, all within ${THRESHOLD} chars"
exit 0
