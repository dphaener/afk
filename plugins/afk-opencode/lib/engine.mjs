// Runs a afk Workflow script (workflows/execute.js or review.js) outside
// Claude Code. A script is async JavaScript whose first statement is
// `export const meta = {...}` and whose body uses the globals agent, phase,
// log, parallel, pipeline, workflow, args, and budget. The semantics of those
// globals match workflows/test/lib/harness.mjs; only agent() is real here.

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor

export const WORKFLOWS_DIR = fileURLToPath(new URL('../workflows/', import.meta.url))
export const SCRIPTS = Object.freeze({ execute: 'execute.js', review: 'review.js' })

export function scriptText(name) {
  const file = SCRIPTS[name]
  if (!file) throw new Error(`afk_workflow: unknown script "${name}" (allowed: ${Object.keys(SCRIPTS).join(', ')})`)
  return readFileSync(WORKFLOWS_DIR + file, 'utf8')
}

export function toBody(text) {
  if (!/^export const meta\s*=/m.test(text)) throw new Error('script must declare `export const meta = {...}`')
  return text.replace(/^export (const meta\s*=)/m, '$1')
}

// Scripts must stay deterministic, as under the Workflow tool.
const GuardedDate = new Proxy(Date, {
  construct(target, argv) {
    if (argv.length === 0) throw new Error('argless new Date() is unavailable in Workflow scripts')
    return new target(...argv)
  },
  apply() {
    throw new Error('Date() is unavailable in Workflow scripts')
  },
  get(target, key) {
    if (key === 'now') {
      return () => {
        throw new Error('Date.now() is unavailable in Workflow scripts')
      }
    }
    return Reflect.get(target, key)
  },
})

const GuardedMath = Object.create(Math, {
  random: {
    value() {
      throw new Error('Math.random() is unavailable in Workflow scripts')
    },
  },
})

export class AbortError extends Error {
  constructor(message = 'afk_workflow: aborted') {
    super(message)
    this.name = 'AbortError'
  }
}

function semaphore(limit) {
  let active = 0
  const waiting = []
  const release = () => {
    active -= 1
    const next = waiting.shift()
    if (next) next()
  }
  return {
    get active() {
      return active
    },
    acquire(signal) {
      if (signal && signal.aborted) return Promise.reject(new AbortError())
      if (active < limit) {
        active += 1
        return Promise.resolve(release)
      }
      return new Promise((resolve, reject) => {
        const start = () => {
          if (signal) signal.removeEventListener('abort', onAbort)
          active += 1
          resolve(release)
        }
        const onAbort = () => {
          const i = waiting.indexOf(start)
          if (i >= 0) waiting.splice(i, 1)
          reject(new AbortError())
        }
        waiting.push(start)
        if (signal) signal.addEventListener('abort', onAbort, { once: true })
      })
    },
  }
}

// options:
//   text         script source
//   args         the script's args value
//   agent        async (prompt, opts, signal) => value; the real sub-agent call
//   concurrency  max agent() calls in flight (default 3)
//   maxAgents    optional cap on total agent() calls, surfaced through `budget`
//   signal       AbortSignal; aborting rejects every pending and future agent()
//   onLog(line)  progress callback for phase() and log() lines
//
// Resolves { ok, result, error, logs, phases, agents, maxInFlight }; never rejects.
export async function runWorkflow(options) {
  const { text, args, signal } = options
  const concurrency = Math.max(1, Math.floor(options.concurrency || 3))
  const maxAgents = options.maxAgents > 0 ? Math.floor(options.maxAgents) : 0
  const onLog = options.onLog || (() => {})
  const rec = { ok: false, result: undefined, error: undefined, logs: [], phases: [], agents: 0, maxInFlight: 0 }
  const sem = semaphore(concurrency)

  const emit = (line) => {
    rec.logs.push(line)
    try {
      onLog(line)
    } catch {}
  }

  const agent = async (prompt, opts = {}) => {
    if (signal && signal.aborted) throw new AbortError()
    if (maxAgents && rec.agents >= maxAgents) {
      throw new Error(`budget exhausted: maxAgents ${maxAgents} reached`)
    }
    rec.agents += 1
    const release = await sem.acquire(signal)
    rec.maxInFlight = Math.max(rec.maxInFlight, sem.active)
    try {
      return await options.agent(String(prompt), opts || {}, signal)
    } finally {
      release()
    }
  }
  const phase = (title) => {
    rec.phases.push(String(title))
    emit(`phase: ${title}`)
  }
  const log = (message) => emit(String(message))
  const parallel = async (thunks) =>
    Promise.all(thunks.map((thunk) => Promise.resolve().then(thunk).catch(() => null)))
  const pipeline = async (items, ...stages) =>
    Promise.all(
      items.map(async (item, index) => {
        try {
          let prev = item
          for (const stage of stages) prev = await stage(prev, item, index)
          return prev
        } catch {
          return null
        }
      })
    )
  const workflow = async () => {
    throw new Error('workflow() is not supported by afk_workflow')
  }
  const budget = maxAgents
    ? { total: maxAgents, spent: () => rec.agents, remaining: () => maxAgents - rec.agents }
    : { total: 0, spent: () => rec.agents, remaining: () => Infinity }

  let body
  try {
    body = toBody(text)
  } catch (e) {
    rec.error = e.message
    return rec
  }
  const fn = new AsyncFunction(
    'agent', 'phase', 'log', 'parallel', 'pipeline', 'workflow', 'args', 'budget', 'Date', 'Math',
    body
  )

  const run = fn(agent, phase, log, parallel, pipeline, workflow, args, budget, GuardedDate, GuardedMath)
  let onAbort
  const aborted = new Promise((_, reject) => {
    if (!signal) return
    onAbort = () => reject(new AbortError())
    if (signal.aborted) onAbort()
    else signal.addEventListener('abort', onAbort, { once: true })
  })
  try {
    rec.result = await Promise.race([run, aborted])
    rec.ok = true
  } catch (e) {
    rec.error = e && e.message ? e.message : String(e)
  } finally {
    if (signal && onAbort) signal.removeEventListener('abort', onAbort)
  }
  run.catch(() => {})
  return rec
}
