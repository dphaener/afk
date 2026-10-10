// Builds the prompt for /afk-go, /afk-plan, /afk-execute, /afk-review
// from skills/{name}/SKILL.md, the way Claude Code expands a skill: the
// frontmatter is dropped, each !`cat "${CLAUDE_PLUGIN_ROOT}/…"` line becomes
// that file's contents, and !`echo "${CLAUDE_PLUGIN_ROOT}"` becomes the
// plugin root. The file list stays defined in SKILL.md.

import { readFileSync } from 'node:fs'
import { join } from 'node:path'

export const COMMANDS = Object.freeze(['go', 'plan', 'execute', 'review'])

const CAT_LINE = /^!`cat "\$\{CLAUDE_PLUGIN_ROOT\}\/([^"]+)"`[ \t]*$/gm
const ECHO_ROOT = /!`echo "\$\{CLAUDE_PLUGIN_ROOT\}"`/g
const COMMAND_PREFIX = /^\s*\/afk-(go|plan|execute|review)(?=\s|$)([\s\S]*)$/

export function parseFrontmatter(text) {
  const m = /^---\n([\s\S]*?)\n---\n?/.exec(text)
  if (!m) return { meta: {}, body: text }
  const meta = {}
  for (const line of m[1].split('\n')) {
    const kv = /^([\w-]+):\s*(.*)$/.exec(line)
    if (!kv) continue
    let v = kv[2].trim()
    if (/^".*"$/.test(v)) v = v.slice(1, -1)
    meta[kv[1]] = v
  }
  return { meta, body: text.slice(m[0].length) }
}

export function skillMeta(root, name) {
  return parseFrontmatter(readFileSync(join(root, 'skills', name, 'SKILL.md'), 'utf8')).meta
}

export function assemble(root, name, argumentsText) {
  if (!COMMANDS.includes(name)) throw new Error(`afk: unknown command ${name}`)
  const raw = readFileSync(join(root, 'skills', name, 'SKILL.md'), 'utf8')
  const args = String(argumentsText || '').trim()
  let body = parseFrontmatter(raw).body.replace(/\$ARGUMENTS/g, args)
  body = body.replace(CAT_LINE, (_, rel) => {
    const file = join(root, rel)
    try {
      return readFileSync(file, 'utf8').trimEnd()
    } catch {
      throw new Error(`afk-${name}: cannot read ${file} named in skills/${name}/SKILL.md`)
    }
  })
  body = body.replace(ECHO_ROOT, root)
  return [
    body.trimEnd(),
    '',
    '## Invocation',
    '',
    `Command: \`/afk-${name}\``,
    `Plugin root: \`${root}\``,
    `$ARGUMENTS for this invocation: ${args ? '`' + args.replace(/`/g, "'") + '`' : '(empty)'}`,
    '',
  ].join('\n')
}

// For prompt text that starts with a afk command (headless `opencode run`),
// returns { name, args }; otherwise null.
// `opencode run "…"` delivers the message wrapped in one pair of quotes.
export function matchCommand(text) {
  let s = String(text || '').trim()
  const q = /^(["'])([\s\S]*)\1$/.exec(s)
  if (q) s = q[2]
  const m = COMMAND_PREFIX.exec(s)
  return m ? { name: m[1], args: m[2].trim() } : null
}
