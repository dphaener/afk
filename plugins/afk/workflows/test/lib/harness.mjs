// Test harness for the afk Workflow engine scripts (execute.js, review.js).
//
// A Workflow script is plain async JavaScript whose first statement is
// `export const meta = {...}` and whose body uses the globals agent, phase,
// log, parallel, pipeline, workflow, args, and budget. The harness reads the
// script text, strips the leading `export ` from `export const meta`, and runs
// the body inside an AsyncFunction that receives stubbed globals.
//
// Time is virtual. Each stubbed agent() call takes a scripted number of ticks
// and records its start and finish tick, so tests can assert ordering and
// concurrency without real timers. When the script is waiting and no agent is
// in flight, the harness reports a deadlock instead of hanging.

import { readFileSync } from 'node:fs'

export const WORKFLOWS_DIR = new URL('../../', import.meta.url)
export const EXECUTE_SCRIPT = new URL('execute.js', WORKFLOWS_DIR)
export const REVIEW_SCRIPT = new URL('review.js', WORKFLOWS_DIR)

const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor

export function readScript(scriptUrl) {
  return readFileSync(scriptUrl, 'utf8')
}

// Turns script text into an AsyncFunction body.
export function toBody(text) {
  if (!/^export const meta\s*=/m.test(text)) {
    throw new Error('script must declare `export const meta = {...}`')
  }
  return text.replace(/^export (const meta\s*=)/m, '$1')
}

// ---------------------------------------------------------------------------
// meta: must be a pure literal (no identifiers in value position, no calls,
// no spreads, no template interpolation). Returns the parsed value.

export function extractMeta(text) {
  const m = /^export const meta\s*=\s*/m.exec(text)
  if (!m) throw new Error('script has no `export const meta =` declaration')
  const p = new LiteralParser(text, m.index + m[0].length)
  const value = p.value()
  return value
}

class LiteralParser {
  constructor(src, pos) {
    this.src = src
    this.pos = pos
  }

  fail(what) {
    const near = this.src.slice(this.pos, this.pos + 40).replace(/\n/g, '\\n')
    throw new Error(`meta is not a pure literal: ${what} at offset ${this.pos} near "${near}"`)
  }

  ws() {
    for (;;) {
      const rest = this.src.slice(this.pos)
      const m = /^(\s+|\/\/[^\n]*|\/\*[\s\S]*?\*\/)/.exec(rest)
      if (!m) return
      this.pos += m[0].length
    }
  }

  value() {
    this.ws()
    const c = this.src[this.pos]
    if (c === '{') return this.object()
    if (c === '[') return this.array()
    if (c === "'" || c === '"' || c === '`') return this.string()
    const rest = this.src.slice(this.pos)
    const num = /^-?\d+(\.\d+)?/.exec(rest)
    if (num) {
      this.pos += num[0].length
      return Number(num[0])
    }
    const kw = /^(true|false|null)\b/.exec(rest)
    if (kw) {
      this.pos += kw[0].length
      return kw[1] === 'true' ? true : kw[1] === 'false' ? false : null
    }
    return this.fail('unexpected token')
  }

  string() {
    const q = this.src[this.pos]
    let out = ''
    this.pos++
    while (this.pos < this.src.length) {
      const c = this.src[this.pos]
      if (c === '\\') {
        out += this.src[this.pos + 1]
        this.pos += 2
        continue
      }
      if (q === '`' && c === '$' && this.src[this.pos + 1] === '{') this.fail('template interpolation')
      if (c === q) {
        this.pos++
        return out
      }
      out += c
      this.pos++
    }
    return this.fail('unterminated string')
  }

  key() {
    this.ws()
    const c = this.src[this.pos]
    if (c === "'" || c === '"') return this.string()
    const m = /^[A-Za-z_$][\w$]*/.exec(this.src.slice(this.pos))
    if (!m) this.fail('expected a property key')
    this.pos += m[0].length
    return m[0]
  }

  object() {
    const out = {}
    this.pos++
    for (;;) {
      this.ws()
      if (this.src[this.pos] === '}') {
        this.pos++
        return out
      }
      if (this.src.startsWith('...', this.pos)) this.fail('spread')
      const k = this.key()
      this.ws()
      if (this.src[this.pos] !== ':') this.fail('expected ":" (shorthand or computed property)')
      this.pos++
      out[k] = this.value()
      this.ws()
      if (this.src[this.pos] === ',') this.pos++
      else if (this.src[this.pos] !== '}') this.fail('expected "," or "}"')
    }
  }

  array() {
    const out = []
    this.pos++
    for (;;) {
      this.ws()
      if (this.src[this.pos] === ']') {
        this.pos++
        return out
      }
      if (this.src.startsWith('...', this.pos)) this.fail('spread')
      out.push(this.value())
      this.ws()
      if (this.src[this.pos] === ',') this.pos++
      else if (this.src[this.pos] !== ']') this.fail('expected "," or "]"')
    }
  }
}

// Forbidden APIs in Workflow scripts (they break resume).
export const FORBIDDEN_API = /Date\.now|Math\.random|new Date\(\s*\)|require\(|^\s*import\s/m

// ---------------------------------------------------------------------------
// Virtual clock

class VirtualClock {
  constructor() {
    this.now = 0
    this.timers = []
  }

  sleep(ticks) {
    return new Promise((resolve) => {
      this.timers.push({ at: this.now + Math.max(0, ticks), resolve })
    })
  }

  // Fires every timer due at the earliest pending tick. Returns false when
  // nothing is pending.
  advance() {
    if (this.timers.length === 0) return false
    const at = Math.min(...this.timers.map((t) => t.at))
    this.now = at
    const due = this.timers.filter((t) => t.at === at)
    this.timers = this.timers.filter((t) => t.at !== at)
    for (const t of due) t.resolve()
    return true
  }
}

const flush = () => new Promise((resolve) => setImmediate(resolve))

// Guarded Date and Math, shadowing the real ones inside the script.
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

// ---------------------------------------------------------------------------
// Replies. A responder returns one of these for each agent() call.

export const ok = (value, duration = 1) => ({ value, duration })
export const nothing = (duration = 1) => ({ value: null, duration })
export const boom = (message, duration = 1) => ({ error: new Error(message), duration })

export const UNLIMITED_BUDGET = Object.freeze({
  total: null,
  spent: () => 0,
  remaining: () => Infinity,
})

// Runs a Workflow script with stubbed globals.
//
// options.args    the Workflow args value
// options.respond (call, rec) => reply   (see ok/nothing/boom)
// options.budget  budget object (default: no target)
//
// Returns rec: { result, error, calls, logs, phases, now }.
// Each call: { seq, label, prompt, opts, model, schema, start, end, threw }.
export async function runScript(scriptUrl, options = {}) {
  const text = readScript(scriptUrl)
  const body = toBody(text)
  const clock = new VirtualClock()
  const respond = options.respond || (() => ok('ok'))
  const budget = options.budget || UNLIMITED_BUDGET
  const rec = { result: undefined, error: undefined, calls: [], logs: [], phases: [], now: 0 }

  const agent = (prompt, opts = {}) => {
    if (rec.calls.length >= 500) throw new Error('harness: more than 500 agent() calls (runaway loop)')
    const call = {
      seq: rec.calls.length,
      label: opts && typeof opts.label === 'string' ? opts.label : '',
      prompt: String(prompt),
      opts: opts || {},
      model: opts ? opts.model : undefined,
      schema: opts ? opts.schema : undefined,
      start: clock.now,
      end: null,
      threw: false,
    }
    rec.calls.push(call)
    let reply
    try {
      reply = respond(call, rec) || ok('ok')
    } catch (e) {
      reply = { error: e, duration: 1 }
    }
    return clock.sleep(reply.duration == null ? 1 : reply.duration).then(() => {
      call.end = clock.now
      if (reply.error) {
        call.threw = true
        throw reply.error
      }
      return reply.value === undefined ? null : reply.value
    })
  }

  const phase = (title) => {
    rec.phases.push(String(title))
  }
  const log = (message) => {
    rec.logs.push(String(message))
  }
  const parallel = async (thunks) =>
    Promise.all(
      thunks.map((thunk) =>
        Promise.resolve()
          .then(thunk)
          .catch(() => null)
      )
    )
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
    throw new Error('harness: workflow() is not stubbed')
  }

  const fn = new AsyncFunction(
    'agent', 'phase', 'log', 'parallel', 'pipeline', 'workflow', 'args', 'budget', 'Date', 'Math',
    body
  )

  let settled = false
  fn(agent, phase, log, parallel, pipeline, workflow, options.args, budget, GuardedDate, GuardedMath).then(
    (value) => {
      settled = true
      rec.result = value
    },
    (error) => {
      settled = true
      rec.error = error
    }
  )

  while (!settled) {
    await flush()
    if (settled) break
    if (!clock.advance()) {
      rec.error = new Error('harness: deadlock — the script is waiting but no agent() call is in flight')
      break
    }
  }
  rec.now = clock.now
  return rec
}

// ---------------------------------------------------------------------------
// Execute-engine fixtures and helpers.

// Tier table shaped like plan_to_args.rb output (W1.T2 contract), using the
// spec fixture aliases so every tier and fallback is distinguishable.
export const TIERS = Object.freeze({
  light: { model: 'tiny', fallbackModel: null },
  balanced: { model: 'mid', fallbackModel: null },
  heavy: { model: 'big', fallbackModel: 'mid' },
  reasoning: { model: 'huge', fallbackModel: 'big' },
})

export const PLAN_PATH = '/repo/.afk/plans/sample.md'
export const PROJECT_ROOT = '/repo'
export const PLUGIN_ROOT = '/repo/plugins/afk'

// Builds one task entry of the W1.T2 contract.
//   id        'W1.T1'
//   opts.deps dependsOn list (dag mode); omitted in waves mode → synthesized
export function task(id, opts = {}) {
  const tier = opts.tier || 'light'
  return {
    id,
    title: opts.title || `Task ${id}`,
    wave: Number(/^W(\d+)\./.exec(id)[1]),
    status: opts.status || 'todo',
    dependsOn: opts.deps,
    files: opts.files || [`src/${id.replace('.', '_').toLowerCase()}.md`],
    ...(opts.qa ? { qa: opts.qa } : {}),
    tier,
    model: TIERS[tier].model,
    fallbackModel: TIERS[tier].fallbackModel,
    lineStart: 0,
    lineEnd: 0,
  }
}

// Builds a complete args object matching the W1.T2 JSON contract.
export function makeArgs(tasks, opts = {}) {
  const mode = opts.mode || 'dag'
  const waveNumbers = [...new Set(tasks.map((t) => t.wave))].sort((a, b) => a - b)
  let line = 20
  const built = tasks.map((t) => {
    const out = { ...t, lineStart: line, lineEnd: line + 11 }
    line += 12
    return out
  })
  for (const t of built) {
    if (t.dependsOn !== undefined) continue
    if (mode === 'dag') {
      t.dependsOn = []
      continue
    }
    const prev = waveNumbers.filter((n) => n < t.wave).pop()
    t.dependsOn = prev === undefined ? [] : built.filter((x) => x.wave === prev).map((x) => x.id)
  }
  return {
    plan: 'sample',
    planPath: opts.planPath || PLAN_PATH,
    pluginRoot: PLUGIN_ROOT,
    autonomous: Boolean(opts.autonomous),
    mode,
    tiers: JSON.parse(JSON.stringify(TIERS)),
    verifierModel: opts.verifierModel || TIERS.balanced.model,
    waves: waveNumbers.map((n) => ({ number: n, name: `Wave ${n} name` })),
    tasks: built,
  }
}

export function selfReport(status = 'done', checks = [{ command: 'bash check.sh', exitCode: 0 }], extra = {}) {
  return { status, checks, summary: `self-report ${status}`, ...extra }
}

export const PASS = { verdict: 'PASS', discrepancies: [] }
export const failVerdict = (...discrepancies) => ({ verdict: 'FAIL', discrepancies })

// The three kinds of agent execute.js spawns. Anything that is not a verifier
// or a fix is a task agent, whose label is the task ID and title.
export const AGENT_KINDS = Object.freeze(['task', 'fix', 'verify'])

export function kindOf(label) {
  if (label.startsWith('verify:')) return 'verify'
  if (label.startsWith('fix:')) return 'fix'
  return 'task'
}

// The exact command execute.js tells an agent to run to tick a task's
// checkbox (contract C1), for the fixture plan built by makeArgs.
export function markDoneCommand(id) {
  return `afk plan check --project ${PROJECT_ROOT} sample ${id} done`
}

// True when the prompt tells its agent to run the mark command for `id`.
export function tellsToMark(call, id) {
  return call.prompt.includes(markDoneCommand(id))
}

export function idOf(label) {
  const m = /W\d+\.T\d+/.exec(label)
  return m ? m[0] : null
}

// Scripted responder for execute.js. `scripts[id][kind]` is a queue of replies
// consumed in call order; when a queue is empty the default reply is used:
//   task / fix → done self-report with one passing check
//   verify     → PASS
// `durations[id]` sets the default tick count for that task's task/fix calls.
export function responder({ scripts = {}, durations = {}, onCall } = {}) {
  const queues = JSON.parse(JSON.stringify(scripts, (k, v) => (v instanceof Error ? { __error: v.message } : v)))
  return (call, rec) => {
    if (onCall) onCall(call, rec)
    const kind = kindOf(call.label)
    const id = idOf(call.label)
    const q = id && queues[id] && queues[id][kind]
    if (q && q.length) {
      const r = q.shift()
      if (r && r.error && r.error.__error) return { error: new Error(r.error.__error), duration: r.duration }
      return r
    }
    const ticks = durations[id] || 1
    if (kind === 'task' || kind === 'fix') return ok(selfReport(), ticks)
    if (kind === 'verify') return ok(PASS, 1)
    return ok('ok')
  }
}

export function callsFor(rec, id, kind) {
  return rec.calls.filter((c) => idOf(c.label) === id && (!kind || kindOf(c.label) === kind))
}

// Calls that are attempts of the task (task agent or fix agent).
export function attemptCalls(rec, id) {
  return rec.calls.filter((c) => idOf(c.label) === id && ['task', 'fix'].includes(kindOf(c.label)))
}

// Work span of a task: first start to last finish of its task/verify/fix calls.
export function span(rec, id) {
  const cs = rec.calls.filter((c) => idOf(c.label) === id)
  if (cs.length === 0) return null
  return { start: Math.min(...cs.map((c) => c.start)), end: Math.max(...cs.map((c) => c.end)) }
}

export function ids(list) {
  return (list || []).map((x) => (typeof x === 'string' ? x : x.id))
}

export function entry(list, id) {
  return (list || []).find((x) => (typeof x === 'string' ? x === id : x.id === id))
}

// Which tasks an agent was told to mark done, and by which kind of agent,
// read from the prompts: { 'W1.T1': ['task'], 'W1.T2': ['verify'] }. A task
// no prompt told anyone to mark is absent. This is what stands in for the
// plan file's [x] marks, which in a real run the agents write themselves.
export function markers(rec) {
  const out = {}
  for (const c of rec.calls) {
    const id = idOf(c.label)
    if (!id || !tellsToMark(c, id)) continue
    out[id] = (out[id] || []).concat(kindOf(c.label))
  }
  return out
}
