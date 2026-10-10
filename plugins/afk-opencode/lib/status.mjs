// awaiting_input writers for .afk/*/status.json, per
// skills/conductor/status-schema.md §5 (transitions), §6 (write protocol), and
// §7 (targeting rule). Writers touch only awaiting_input and updated_at, never
// create the file, and fail silently.

import { existsSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const TERMINAL = new Set(['done', 'failed'])
export const PERMISSION_SENTINEL = '.awaiting-permission'

function readStatus(file) {
  try {
    const json = JSON.parse(readFileSync(file, 'utf8'))
    return json && typeof json === 'object' && !Array.isArray(json) ? json : null
  } catch {
    return null
  }
}

// §7: primary = session_id match in a non-terminal phase; fallback = a single
// empty-session_id file in planning. Anything else, including ambiguity, is
// no match.
export function findTarget(directory, sessionID) {
  const afk = join(directory || '', '.afk')
  let entries
  try {
    entries = readdirSync(afk, { withFileTypes: true })
  } catch {
    return null
  }
  const primary = []
  const fallback = []
  for (const e of entries) {
    if (!e.isDirectory()) continue
    const file = join(afk, e.name, 'status.json')
    const s = readStatus(file)
    if (!s) continue
    if (sessionID && s.session_id === sessionID && !TERMINAL.has(s.phase)) primary.push({ file, status: s })
    if (s.session_id === '' && s.phase === 'planning') fallback.push({ file, status: s })
  }
  if (primary.length === 1) return primary[0]
  if (primary.length === 0 && fallback.length === 1) return fallback[0]
  return null
}

export function rfc3339(date = new Date()) {
  return date.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

// §6: re-read immediately before the rename, change only the sanctioned
// fields, write a same-directory temp file, rename over status.json.
export function writeAwaitingInput(file, value) {
  if (!existsSync(file)) return false
  const s = readStatus(file)
  if (!s) return false
  s.awaiting_input = Boolean(value)
  s.updated_at = rfc3339()
  const tmp = join(file, '..', `.status.json.tmp.${process.pid}.${Math.random().toString(36).slice(2, 8)}`)
  try {
    writeFileSync(tmp, JSON.stringify(s, null, 2) + '\n')
    renameSync(tmp, file)
    return true
  } catch {
    try {
      rmSync(tmp, { force: true })
    } catch {}
    return false
  }
}

function sentinelPath(file) {
  return join(file, '..', PERMISSION_SENTINEL)
}

// The transitions the plugin owns. `target` is a findTarget() match.
export const transitions = {
  // S1a: a permission prompt is showing.
  permissionAsked(target) {
    if (!target) return false
    try {
      writeFileSync(sentinelPath(target.file), '')
    } catch {}
    return writeAwaitingInput(target.file, true)
  },
  // Clears only a flag a permission prompt set; a conductor's deliberate stop
  // (S2-S4) has no sentinel and stays true.
  permissionReplied(target) {
    if (!target) return false
    const sentinel = sentinelPath(target.file)
    if (!existsSync(sentinel)) return false
    try {
      rmSync(sentinel, { force: true })
    } catch {}
    return writeAwaitingInput(target.file, false)
  },
  // S1b: a question is showing during planning.
  questionShown(target) {
    if (!target || target.status.phase !== 'planning') return false
    return writeAwaitingInput(target.file, true)
  },
  // C1: the user answered a question or submitted a prompt.
  userInput(target) {
    if (!target) return false
    try {
      rmSync(sentinelPath(target.file), { force: true })
    } catch {}
    return writeAwaitingInput(target.file, false)
  },
}
