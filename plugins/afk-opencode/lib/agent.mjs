// agent() for afk Workflow scripts, backed by OpenCode child sessions.
//
// Each call creates a child session of the calling session on the requested
// model, sends the prompt (plus a JSON-schema instruction when opts.schema is
// set), waits for the turn to finish, and reads the final assistant text. A
// reply that fails the schema gets one repair turn; a second failure returns
// null. An unknown model or a failed turn throws, so the script's
// fallback-model retry runs.

import { AbortError } from './engine.mjs'

export function parseModel(ref) {
  if (!ref) return undefined
  const s = String(ref)
  const i = s.indexOf('/')
  if (i <= 0 || i === s.length - 1) throw new Error(`model "${s}" is not a providerID/modelID reference`)
  return { providerID: s.slice(0, i), id: s.slice(i + 1) }
}

// ---------------------------------------------------------------------------
// JSON extraction and validation

function tryParse(s) {
  try {
    return { ok: true, value: JSON.parse(s) }
  } catch {
    return { ok: false }
  }
}

// Last balanced {...} or [...] block in the text, scanning backwards.
function lastBalanced(text) {
  for (let end = text.length - 1; end >= 0; end--) {
    const close = text[end]
    if (close !== '}' && close !== ']') continue
    const open = close === '}' ? '{' : '['
    let depth = 0
    let inStr = false
    for (let i = end; i >= 0; i--) {
      const c = text[i]
      if (c === '"') {
        let bs = 0
        for (let j = i - 1; j >= 0 && text[j] === '\\'; j--) bs++
        if (bs % 2 === 0) inStr = !inStr
        continue
      }
      if (inStr) continue
      if (c === close) depth++
      else if (c === open) {
        depth--
        if (depth === 0) {
          const r = tryParse(text.slice(i, end + 1))
          if (r.ok) return r
          break
        }
      }
    }
  }
  return { ok: false }
}

export function extractJson(text) {
  const s = String(text || '').trim()
  if (!s) return { ok: false }
  const fences = [...s.matchAll(/```(?:json|JSON)?\s*\n([\s\S]*?)```/g)]
  for (let i = fences.length - 1; i >= 0; i--) {
    const r = tryParse(fences[i][1].trim())
    if (r.ok) return r
  }
  const whole = tryParse(s)
  if (whole.ok) return whole
  return lastBalanced(s)
}

function typeOk(type, v) {
  switch (type) {
    case 'object':
      return v !== null && typeof v === 'object' && !Array.isArray(v)
    case 'array':
      return Array.isArray(v)
    case 'string':
      return typeof v === 'string'
    case 'integer':
      return Number.isInteger(v)
    case 'number':
      return typeof v === 'number' && Number.isFinite(v)
    case 'boolean':
      return typeof v === 'boolean'
    case 'null':
      return v === null
    default:
      return true
  }
}

// Validates the subset of JSON Schema the afk scripts use: type, required,
// enum, properties, items. Returns a list of human-readable errors.
export function validate(schema, value, path = '$') {
  if (!schema || typeof schema !== 'object') return []
  const errors = []
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type]
    if (!types.some((t) => typeOk(t, value))) {
      errors.push(`${path}: expected ${types.join(' or ')}`)
      return errors
    }
  }
  if (Array.isArray(schema.enum) && !schema.enum.includes(value)) {
    errors.push(`${path}: must be one of ${schema.enum.map((e) => JSON.stringify(e)).join(', ')}`)
  }
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const key of schema.required || []) {
      if (!(key in value)) errors.push(`${path}.${key}: required`)
    }
    for (const [key, sub] of Object.entries(schema.properties || {})) {
      if (key in value && value[key] !== undefined) errors.push(...validate(sub, value[key], `${path}.${key}`))
    }
  }
  if (Array.isArray(value) && schema.items) {
    value.forEach((item, i) => errors.push(...validate(schema.items, item, `${path}[${i}]`)))
  }
  return errors
}

export function schemaInstruction(schema) {
  return [
    '',
    '---',
    'When your work is finished, end your final reply with exactly one JSON object that matches the JSON Schema below, in a ```json fenced block, with nothing after the block. Use real values from your work, not placeholders.',
    '```json',
    JSON.stringify(schema, null, 2),
    '```',
  ].join('\n')
}

export function repairInstruction(errors) {
  return [
    'Your reply did not end with a JSON object that matches the required schema.',
    errors.length ? `Problems:\n${errors.map((e) => `- ${e}`).join('\n')}` : 'No JSON object was found.',
    'Do not redo the work. Reply with only the corrected JSON object in a ```json fenced block.',
  ].join('\n\n')
}

// ---------------------------------------------------------------------------
// Session plumbing

export function finalAssistant(messages) {
  const list = Array.isArray(messages) ? messages : (messages && messages.data) || []
  for (let i = list.length - 1; i >= 0; i--) {
    const m = list[i]
    if (m && m.type === 'assistant') return m
  }
  return null
}

export function assistantText(message) {
  if (!message || !Array.isArray(message.content)) return ''
  return message.content
    .filter((c) => c && c.type === 'text' && typeof c.text === 'string')
    .map((c) => c.text)
    .join('\n')
    .trim()
}

// deps:
//   session      ctx.session (create, prompt, wait, context, interrupt, remove, get)
//   parentID     calling session ID
//   directory    location for child sessions
//   knownModels  async () => Set of "providerID/modelID" strings
//   agentName    OpenCode agent for children (default "general")
//   onChild      ({id, label, model}) => void, for progress reporting
export function makeAgent(deps) {
  const { session, parentID, directory } = deps
  const agentName = deps.agentName || 'general'
  const live = new Set()
  let modelsPromise

  const knownModels = () => {
    if (!modelsPromise) modelsPromise = Promise.resolve(deps.knownModels())
    return modelsPromise
  }

  async function turn(sessionID, text, signal) {
    if (signal && signal.aborted) throw new AbortError()
    await session.prompt({ sessionID, text })
    await session.wait({ sessionID }, signal ? { signal } : undefined)
    if (signal && signal.aborted) throw new AbortError()
    const messages = await session.context({ sessionID })
    const last = finalAssistant(messages)
    const out = assistantText(last)
    if (!out) {
      const why = last && last.error ? last.error.message || last.error.type : 'no assistant reply'
      throw new Error(`child session ${sessionID} produced no text (${why})`)
    }
    return out
  }

  async function agent(prompt, opts = {}, signal) {
    const label = typeof opts.label === 'string' && opts.label ? opts.label : 'afk agent'
    const model = parseModel(opts.model)
    if (model) {
      const known = await knownModels()
      const key = `${model.providerID}/${model.id}`
      if (known && known.size && !known.has(key)) throw new Error(`unknown model ${key}`)
    }
    if (signal && signal.aborted) throw new AbortError()

    const child = await session.create({
      parentID,
      title: label,
      agent: agentName,
      ...(model ? { model } : {}),
      ...(directory ? { location: { directory } } : {}),
    })
    const id = child.id
    live.add(id)
    if (deps.onChild) {
      try {
        deps.onChild({ id, label, model: opts.model })
      } catch {}
    }
    const onAbort = () => {
      void cleanup(id)
    }
    if (signal) signal.addEventListener('abort', onAbort, { once: true })
    try {
      const text = await turn(id, opts.schema ? prompt + schemaInstruction(opts.schema) : prompt, signal)
      if (!opts.schema) return text
      let parsed = extractJson(text)
      let errors = parsed.ok ? validate(opts.schema, parsed.value) : ['no JSON object found']
      if (parsed.ok && errors.length === 0) return parsed.value
      const repaired = await turn(id, repairInstruction(parsed.ok ? errors : []), signal)
      parsed = extractJson(repaired)
      errors = parsed.ok ? validate(opts.schema, parsed.value) : ['no JSON object found']
      return parsed.ok && errors.length === 0 ? parsed.value : null
    } finally {
      if (signal) signal.removeEventListener('abort', onAbort)
      live.delete(id)
    }
  }

  async function cleanup(id) {
    try {
      await session.interrupt({ sessionID: id })
    } catch {}
    try {
      await session.remove({ sessionID: id })
    } catch {}
  }

  agent.abortAll = async () => {
    await Promise.all([...live].map(cleanup))
  }
  agent.live = live
  return agent
}
