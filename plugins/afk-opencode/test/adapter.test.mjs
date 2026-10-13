// Adapter tests: the real execute.js / review.js through lib/engine.mjs and
// lib/agent.mjs, with a fake ctx.session standing in for OpenCode.

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { runWorkflow, scriptText } from '../lib/engine.mjs'
import { makeAgent, extractJson, validate } from '../lib/agent.mjs'
import { assemble, matchCommand } from '../lib/commands.mjs'
import { findTarget, transitions } from '../lib/status.mjs'
import { makeArgs, task } from '../workflows/test/lib/harness.mjs'

const ROOT = new URL('..', import.meta.url).pathname.replace(/\/$/, '')
const KNOWN = new Set(['p/tiny', 'p/mid', 'p/big', 'p/huge'])
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const TIERS = {
  light: { model: 'p/tiny', fallbackModel: null },
  balanced: { model: 'p/mid', fallbackModel: null },
  heavy: { model: 'p/big', fallbackModel: 'p/mid' },
  reasoning: { model: 'p/huge', fallbackModel: 'p/big' },
}

function withTiers(args) {
  args.tiers = TIERS
  args.verifierModel = TIERS.balanced.model
  for (const t of args.tasks) {
    t.model = TIERS[t.tier].model
    t.fallbackModel = TIERS[t.tier].fallbackModel
  }
  return args
}

const json = (v) => '```json\n' + JSON.stringify(v) + '\n```'
const DONE = json({ status: 'done', checks: [{ command: 'true', exitCode: 0 }], summary: 'ok' })

// Fake ctx.session. `reply({title, model, turn, text})` returns the assistant
// text for a turn (or throws). Each turn takes `delay` ms.
function fakeSession({ reply, delay = 5 } = {}) {
  const sessions = new Map()
  const events = []
  let seq = 0
  let active = 0
  const stats = { maxActive: 0, created: [], removed: [], interrupted: [] }
  const session = {
    async create(input) {
      const id = `ses_child_${++seq}`
      sessions.set(id, { ...input, id, turns: 0, messages: [], pending: null })
      stats.created.push({ id, title: input.title, model: input.model, parentID: input.parentID, agent: input.agent })
      return { id, model: input.model }
    },
    async prompt({ sessionID, text }) {
      const s = sessions.get(sessionID)
      s.messages.push({ type: 'user', text })
      const turn = ++s.turns
      active++
      stats.maxActive = Math.max(stats.maxActive, active)
      events.push({ t: 'start', title: s.title, turn })
      s.pending = (async () => {
        try {
          await sleep(typeof delay === 'function' ? delay(s) : delay)
          if (s.interrupted) return
          const out = reply({ title: s.title, model: s.model && `${s.model.providerID}/${s.model.id}`, turn, text })
          s.messages.push({ type: 'assistant', finish: 'stop', content: [{ type: 'text', text: out }] })
        } catch (e) {
          s.messages.push({ type: 'assistant', finish: 'error', content: [], error: { type: 'x', message: e.message } })
        } finally {
          active--
          events.push({ t: 'end', title: s.title, turn })
          s.messages.push({ type: 'idle' })
        }
      })()
    },
    async wait({ sessionID }, opts) {
      const s = sessions.get(sessionID)
      const signal = opts && opts.signal
      await Promise.race([
        s.pending,
        new Promise((_, rej) => signal && signal.addEventListener('abort', () => rej(new Error('aborted')), { once: true })),
      ])
    },
    async context({ sessionID }) {
      return sessions.get(sessionID).messages
    },
    async interrupt({ sessionID }) {
      const s = sessions.get(sessionID)
      if (s) s.interrupted = true
      stats.interrupted.push(sessionID)
    },
    async remove({ sessionID }) {
      stats.removed.push(sessionID)
    },
  }
  return { session, events, stats, sessions }
}

function runExecute(args, fake, extra = {}) {
  const agent = makeAgent({
    session: fake.session,
    parentID: 'ses_parent',
    directory: '/repo',
    knownModels: async () => KNOWN,
  })
  return runWorkflow({ text: scriptText('execute'), args, agent, concurrency: extra.concurrency || 3, signal: extra.signal })
}

const kind = (title) => (title.startsWith('verify:') ? 'verify' : title.startsWith('bookkeep:') ? 'bookkeep' : title.startsWith('fix:') ? 'fix' : 'task')
const idOf = (title) => (/W\d+\.T\d+/.exec(title) || [])[0]

test('execute: DAG order — a task starts only after its dependencies finish', async () => {
  const args = withTiers(
    makeArgs([task('W1.T1'), task('W1.T2'), task('W2.T1', { deps: ['W1.T1'] }), task('W2.T2', { deps: ['W1.T1', 'W1.T2'] })])
  )
  const fake = fakeSession({ reply: ({ title }) => (kind(title) === 'bookkeep' ? 'ok' : DONE), delay: (s) => (idOf(s.title) === 'W1.T2' ? 30 : 5) })
  const rec = await runExecute(args, fake)
  assert.equal(rec.ok, true, rec.error)
  assert.deepEqual([...rec.result.passed].sort(), ['W1.T1', 'W1.T2', 'W2.T1', 'W2.T2'])
  const at = (id, t) => fake.events.findIndex((e) => e.t === t && kind(e.title) === 'task' && idOf(e.title) === id)
  assert.ok(at('W2.T1', 'start') > at('W1.T1', 'end'))
  assert.ok(at('W2.T2', 'start') > at('W1.T2', 'end'))
  // W2.T1 needs only W1.T1, so it starts before the slow W1.T2 ends.
  assert.ok(at('W2.T1', 'start') < at('W1.T2', 'end'))
  assert.ok(fake.stats.created.every((c) => c.parentID === 'ses_parent' && c.agent === 'general'))
  assert.ok(fake.stats.maxActive >= 2, 'independent tasks run concurrently')
})

test('execute: tasks with overlapping Files run one at a time in plan order', async () => {
  const args = withTiers(
    makeArgs([task('W1.T1', { files: ['src/a.ts'] }), task('W1.T2', { files: ['src/a.ts'] }), task('W1.T3', { files: ['src/'] })])
  )
  const fake = fakeSession({ reply: ({ title }) => (kind(title) === 'bookkeep' ? 'ok' : DONE), delay: 10 })
  const rec = await runExecute(args, fake)
  assert.equal(rec.ok, true, rec.error)
  const order = fake.events.filter((e) => kind(e.title) === 'task').map((e) => `${e.t}:${idOf(e.title)}`)
  assert.deepEqual(order, ['start:W1.T1', 'end:W1.T1', 'start:W1.T2', 'end:W1.T2', 'start:W1.T3', 'end:W1.T3'])
})

test('execute: concurrency cap bounds child sessions in flight', async () => {
  const args = withTiers(makeArgs(['W1.T1', 'W1.T2', 'W1.T3', 'W1.T4', 'W1.T5'].map((id) => task(id))))
  const fake = fakeSession({ reply: ({ title }) => (kind(title) === 'bookkeep' ? 'ok' : DONE), delay: 10 })
  const rec = await runExecute(args, fake, { concurrency: 2 })
  assert.equal(rec.ok, true, rec.error)
  assert.equal(rec.result.passed.length, 5)
  assert.ok(fake.stats.maxActive <= 2, `maxActive ${fake.stats.maxActive}`)
  assert.equal(rec.maxInFlight, 2)
})

test('execute: unknown model throws, and the script retries on the fallback model', async () => {
  const args = withTiers(makeArgs([task('W1.T1', { tier: 'heavy' })]))
  args.tasks[0].model = 'p/missing'
  const fake = fakeSession({ reply: ({ title }) => (kind(title) === 'bookkeep' ? 'ok' : kind(title) === 'verify' ? json({ verdict: 'PASS', discrepancies: [] }) : DONE) })
  const rec = await runExecute(args, fake)
  assert.equal(rec.ok, true, rec.error)
  assert.deepEqual(rec.result.passed, ['W1.T1'])
  assert.ok(rec.logs.some((l) => /agent error on p\/missing: unknown model p\/missing/.test(l)), rec.logs.join('\n'))
  assert.ok(rec.logs.some((l) => /retrying once on fallback model p\/mid/.test(l)))
  const taskModels = fake.stats.created.filter((c) => kind(c.title) === 'task').map((c) => `${c.model.providerID}/${c.model.id}`)
  assert.deepEqual(taskModels, ['p/mid'], 'no child is created for the unknown model')
})

test('execute: a failed child turn throws and falls back', async () => {
  const args = withTiers(makeArgs([task('W1.T1', { tier: 'heavy' })]))
  const fake = fakeSession({
    reply: ({ title, model }) => {
      if (kind(title) === 'task' && model === 'p/big') throw new Error('overloaded')
      return kind(title) === 'bookkeep' ? 'ok' : kind(title) === 'verify' ? json({ verdict: 'PASS', discrepancies: [] }) : DONE
    },
  })
  const rec = await runExecute(args, fake)
  assert.deepEqual(rec.result.passed, ['W1.T1'])
  assert.ok(rec.logs.some((l) => /produced no text \(overloaded\)/.test(l)), rec.logs.join('\n'))
})

test('agent: bad JSON gets one repair turn, then null', async () => {
  const fake = fakeSession({ reply: () => 'I did the work but forgot the JSON.' })
  const agent = makeAgent({ session: fake.session, parentID: 'p', knownModels: async () => KNOWN })
  const schema = { type: 'object', properties: { status: { type: 'string', enum: ['done'] } }, required: ['status'] }
  const out = await agent('do it', { label: 'x', model: 'p/mid', schema })
  assert.equal(out, null)
  const s = fake.sessions.get(fake.stats.created[0].id)
  assert.equal(s.turns, 2, 'exactly one repair turn')
  assert.match(s.messages.filter((m) => m.type === 'user')[0].text, /JSON Schema/)
  assert.match(s.messages.filter((m) => m.type === 'user')[1].text, /did not end with a JSON object/)
})

test('agent: repair turn recovers a schema-invalid reply', async () => {
  const fake = fakeSession({ reply: ({ turn }) => (turn === 1 ? json({ status: 'maybe' }) : 'Fixed:\n' + json({ status: 'done' })) })
  const agent = makeAgent({ session: fake.session, parentID: 'p', knownModels: async () => KNOWN })
  const schema = { type: 'object', properties: { status: { type: 'string', enum: ['done'] } }, required: ['status'] }
  assert.deepEqual(await agent('do it', { label: 'x', model: 'p/mid', schema }), { status: 'done' })
})

test('execute: a task whose agents never return JSON fails after 3 attempts', async () => {
  const args = withTiers(makeArgs([task('W1.T1'), task('W2.T1', { deps: ['W1.T1'] })]))
  const fake = fakeSession({ reply: ({ title }) => (kind(title) === 'bookkeep' ? 'ok' : 'no json here') })
  const rec = await runExecute(args, fake)
  assert.equal(rec.ok, true, rec.error)
  assert.deepEqual(rec.result.failed.map((f) => f.id), ['W1.T1'])
  assert.deepEqual(rec.result.skipped, [{ id: 'W2.T1', reason: 'blocked', by: 'W1.T1' }])
})

test('execute: abort interrupts and removes running children and ends the run', async () => {
  const args = withTiers(makeArgs([task('W1.T1'), task('W1.T2'), task('W2.T1', { deps: ['W1.T1'] })]))
  const fake = fakeSession({ reply: () => DONE, delay: 200 })
  const ac = new AbortController()
  setTimeout(() => ac.abort(), 30)
  const started = Date.now()
  const rec = await runExecute(args, fake, { signal: ac.signal })
  assert.equal(rec.ok, false)
  assert.match(rec.error, /aborted/)
  assert.ok(Date.now() - started < 150, 'returns promptly')
  await sleep(10)
  assert.equal(fake.stats.created.length, 2)
  assert.deepEqual([...fake.stats.interrupted].sort(), fake.stats.created.map((c) => c.id).sort())
  assert.deepEqual([...fake.stats.removed].sort(), fake.stats.created.map((c) => c.id).sort())
  await sleep(250)
  assert.equal(fake.stats.created.length, 2, 'no new children after abort')
})

test('review: runs checks and critics through child sessions', async () => {
  const fake = fakeSession({
    reply: ({ title }) =>
      json(title.startsWith('critic:quality') ? { verdict: 'FAIL', findings: [{ severity: 'major', taskId: 'W1.T1', text: 'bad' }] } : { verdict: 'PASS', findings: [] }),
  })
  const agent = makeAgent({ session: fake.session, parentID: 'p', knownModels: async () => KNOWN })
  const rec = await runWorkflow({
    text: scriptText('review'),
    agent,
    args: { plan: 'x', planPath: '/r/p.md', pluginRoot: ROOT, diffCommand: 'git diff main', checks: ['compliance', 'quality'], checkModel: 'p/mid', criticModel: 'p/huge', criticFallbackModel: 'p/big' },
  })
  assert.equal(rec.ok, true, rec.error)
  assert.equal(rec.result.checks.compliance.verdict, 'PASS')
  assert.equal(rec.result.checks.quality.findings[0].text, 'bad')
  assert.equal(fake.stats.created.length, 4)
})

test('engine: script errors come back as ok:false', async () => {
  const rec = await runWorkflow({ text: scriptText('execute'), args: '{"tasks":[]}', agent: async () => null })
  assert.equal(rec.ok, false)
  assert.match(rec.error, /tasks array/)
  assert.throws(() => scriptText('../evil'), /unknown script/)
})

test('extractJson / validate', () => {
  assert.deepEqual(extractJson('text\n```json\n{"a":1}\n```').value, { a: 1 })
  assert.deepEqual(extractJson('Result: {"a": {"b": "}"}} trailing').value, { a: { b: '}' } })
  assert.equal(extractJson('nothing').ok, false)
  const s = { type: 'object', properties: { n: { type: 'integer' }, xs: { type: 'array', items: { type: 'string' } } }, required: ['n'] }
  assert.deepEqual(validate(s, { n: 1, xs: ['a'] }), [])
  assert.equal(validate(s, { n: 1.5, xs: [1] }).length, 2)
  assert.equal(validate(s, {}).length, 1)
})

test('commands: assemble inlines every cat line and substitutes arguments', () => {
  for (const name of ['go', 'work-plan', 'execute', 'review']) {
    const out = assemble(ROOT, name, '--autonomous TURT-1')
    assert.doesNotMatch(out, /!`|CLAUDE_PLUGIN_ROOT/)
    assert.ok(out.includes('$ARGUMENTS for this invocation: `--autonomous TURT-1`'))
    assert.ok(out.includes(ROOT))
    assert.ok(out.includes('--autonomous TURT-1'))
  }
  assert.ok(assemble(ROOT, 'go', '').includes('# afk Session Status Schema'))
  assert.deepEqual(matchCommand('  /afk-execute my-plan'), { name: 'execute', args: 'my-plan' })
  assert.equal(matchCommand('please /afk-go'), null)
  assert.deepEqual(matchCommand('"/afk-review my-plan"'), { name: 'review', args: 'my-plan' })
})

test('status: targeting rule and awaiting_input transitions', () => {
  const dir = mkdtempSync(join(tmpdir(), 'afk-status-'))
  const write = (name, s) => {
    mkdirSync(join(dir, '.afk', name), { recursive: true })
    writeFileSync(join(dir, '.afk', name, 'status.json'), JSON.stringify(s))
  }
  const base = { plan_name: 'a', phase: 'planning', branch: 'a', base_branch: 'main', loop_count: 0, awaiting_input: false, tmux_window: '', updated_at: '2026-01-01T00:00:00Z' }
  write('a', { ...base, session_id: 'ses_1' })
  write('old', { ...base, plan_name: 'old', session_id: 'ses_1', phase: 'done' })
  const read = () => JSON.parse(readFileSync(join(dir, '.afk/a/status.json'), 'utf8'))

  const t = findTarget(dir, 'ses_1')
  assert.equal(t.status.plan_name, 'a')
  assert.equal(findTarget(dir, 'ses_other'), null)

  assert.ok(transitions.questionShown(t))
  assert.equal(read().awaiting_input, true)
  assert.ok(transitions.userInput(findTarget(dir, 'ses_1')))
  assert.equal(read().awaiting_input, false)

  assert.ok(transitions.permissionAsked(findTarget(dir, 'ses_1')))
  assert.ok(existsSync(join(dir, '.afk/a/.awaiting-permission')))
  assert.ok(transitions.permissionReplied(findTarget(dir, 'ses_1')))
  assert.equal(read().awaiting_input, false)
  assert.equal(existsSync(join(dir, '.afk/a/.awaiting-permission')), false)

  // A conductor stop (no sentinel) is not cleared by a permission reply.
  write('a', { ...read(), phase: 'reviewing', awaiting_input: true })
  assert.equal(transitions.permissionReplied(findTarget(dir, 'ses_1')), false)
  assert.equal(read().awaiting_input, true)
  // Questions outside planning belong to the conductor.
  write('a', { ...read(), awaiting_input: false })
  assert.equal(transitions.questionShown(findTarget(dir, 'ses_1')), false)

  const after = read()
  assert.deepEqual(Object.keys(after).sort(), Object.keys({ ...base, session_id: '' }).sort(), 'no keys added')
  assert.match(after.updated_at, /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/)

  // Startup fallback: one empty-session planning file.
  write('b', { ...base, plan_name: 'b', session_id: '' })
  assert.equal(findTarget(dir, 'ses_new').status.plan_name, 'b')
  write('c', { ...base, plan_name: 'c', session_id: '' })
  assert.equal(findTarget(dir, 'ses_new'), null, 'ambiguity acts on nothing')
})
