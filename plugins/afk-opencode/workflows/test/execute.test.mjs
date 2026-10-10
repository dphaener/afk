// Specifies plugins/afk/workflows/execute.js, the afk DAG execution
// engine. `args` follows the plan_to_args.rb JSON contract (W1.T2).
// Numbered describe blocks match the execute.js behaviors in the plan task.

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  EXECUTE_SCRIPT,
  FORBIDDEN_API,
  PASS,
  PLAN_PATH,
  PLUGIN_ROOT,
  TIERS,
  attemptCalls,
  boom,
  callsFor,
  entry,
  extractMeta,
  failVerdict,
  ids,
  kindOf,
  makeArgs,
  nothing,
  ok,
  planState,
  readScript,
  responder,
  runScript,
  selfReport,
  span,
  task,
} from './lib/harness.mjs'

async function run(args, respondOpts = {}, extra = {}) {
  const rec = await runScript(EXECUTE_SCRIPT, { args, respond: responder(respondOpts), ...extra })
  return rec
}

function assertCompleted(rec) {
  if (rec.error) throw rec.error
}

const failingCheck = [{ command: 'bash check.sh', exitCode: 1 }]

// ---------------------------------------------------------------------------
describe('execute.js (1) DAG scheduling', () => {
  it('starts a task as soon as its dependencies pass, without waiting for unrelated slow tasks', async () => {
    const args = makeArgs([
      task('W1.T1'),
      task('W1.T2'),
      task('W2.T1', { deps: ['W1.T2'] }),
    ])
    const rec = await run(args, { durations: { 'W1.T1': 100 } })
    assertCompleted(rec)
    const slow = span(rec, 'W1.T1')
    const dep = span(rec, 'W1.T2')
    const child = span(rec, 'W2.T1')
    assert.ok(child.start >= dep.end, 'W2.T1 must not start before W1.T2 has passed')
    assert.ok(child.start < slow.end, 'W2.T1 must not wait for the unrelated slow W1.T1')
    assert.deepEqual(ids(rec.result.passed).sort(), ['W1.T1', 'W1.T2', 'W2.T1'])
  })

  it('waits for every dependency before starting a task', async () => {
    const args = makeArgs([
      task('W1.T1'),
      task('W1.T2'),
      task('W2.T1', { deps: ['W1.T1', 'W1.T2'] }),
    ])
    const rec = await run(args, { durations: { 'W1.T1': 30, 'W1.T2': 5 } })
    assertCompleted(rec)
    assert.ok(span(rec, 'W2.T1').start >= span(rec, 'W1.T1').end)
    assert.ok(span(rec, 'W2.T1').start >= span(rec, 'W1.T2').end)
  })

  it('does not start a dependent before its dependency has passed verification', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' }), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args)
    assertCompleted(rec)
    const verify = callsFor(rec, 'W1.T1', 'verify')
    assert.equal(verify.length, 1)
    assert.ok(span(rec, 'W2.T1').start >= verify[0].end)
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (2) overlapping files', () => {
  it('never runs tasks with the same file concurrently and runs them in plan order', async () => {
    const args = makeArgs([
      task('W1.T1', { files: ['a/x.md'] }),
      task('W1.T2', { files: ['a/x.md', 'a/y.md'] }),
      task('W1.T3', { files: ['b.md'] }),
    ])
    const rec = await run(args, { durations: { 'W1.T1': 10, 'W1.T2': 10, 'W1.T3': 10 } })
    assertCompleted(rec)
    const t1 = span(rec, 'W1.T1')
    const t2 = span(rec, 'W1.T2')
    const t3 = span(rec, 'W1.T3')
    assert.ok(t2.start >= t1.end, 'W1.T2 overlaps W1.T1 on a/x.md and must run after it')
    assert.ok(t3.start < t1.end, 'W1.T3 shares no file and must run concurrently with W1.T1')
  })

  it('treats a directory entry as overlapping the files under it', async () => {
    const args = makeArgs([
      task('W1.T1', { files: ['a/fixtures/'] }),
      task('W1.T2', { files: ['a/fixtures/x.md'] }),
    ])
    const rec = await run(args, { durations: { 'W1.T1': 10, 'W1.T2': 10 } })
    assertCompleted(rec)
    assert.ok(span(rec, 'W1.T2').start >= span(rec, 'W1.T1').end)
  })

  it('does not treat a partial path-segment prefix as a directory overlap', async () => {
    const args = makeArgs([
      task('W1.T1', { files: ['a/fix'] }),
      task('W1.T2', { files: ['a/fixtures/x.md'] }),
    ])
    const rec = await run(args, { durations: { 'W1.T1': 10, 'W1.T2': 10 } })
    assertCompleted(rec)
    assert.ok(span(rec, 'W1.T2').start < span(rec, 'W1.T1').end)
  })

  it('releases the file to the next task when the earlier task fails (no deadlock)', async () => {
    const args = makeArgs([
      task('W1.T1', { files: ['shared.md'] }),
      task('W1.T2', { files: ['shared.md'] }),
    ])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [nothing(), nothing(), nothing()] } },
    })
    assertCompleted(rec)
    assert.deepEqual(ids(rec.result.failed), ['W1.T1'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T2'])
    assert.ok(span(rec, 'W1.T2').start >= span(rec, 'W1.T1').end)
  })

  it('releases the file when the earlier overlapping task is skipped', async () => {
    const args = makeArgs([
      task('W1.T1'),
      task('W2.T1', { deps: ['W1.T1'], files: ['shared.md'] }),
      task('W2.T2', { files: ['shared.md'] }),
    ])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [nothing(), nothing(), nothing()] } },
    })
    assertCompleted(rec)
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'blocked')
    assert.ok(ids(rec.result.passed).includes('W2.T2'))
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (3) task states and failure propagation', () => {
  it('skips done tasks, counts them as passed, and runs their dependents', async () => {
    const args = makeArgs([task('W1.T1', { status: 'done' }), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args)
    assertCompleted(rec)
    assert.equal(callsFor(rec, 'W1.T1').length, 0, 'a done task gets no agent calls, not even bookkeeping')
    assert.deepEqual(ids(rec.result.passed).sort(), ['W1.T1', 'W2.T1'])
  })

  it('in dag mode, a task already marked failed blocks its dependents', async () => {
    const args = makeArgs([task('W1.T1', { status: 'failed' }), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args)
    assertCompleted(rec)
    assert.equal(callsFor(rec, 'W1.T1').length, 0)
    assert.equal(attemptCalls(rec, 'W2.T1').length, 0)
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'blocked')
    assert.equal(planState(rec, args)['W2.T1'], 'failed')
  })

  it('in waves mode, a failed task does not block the next wave, which still waits for it', async () => {
    const args = makeArgs([task('W1.T1'), task('W1.T2'), task('W2.T1')], { mode: 'waves' })
    assert.deepEqual(args.tasks[2].dependsOn, ['W1.T1', 'W1.T2'])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [nothing(), nothing(), nothing()] } },
    })
    assertCompleted(rec)
    assert.deepEqual(ids(rec.result.failed), ['W1.T1'])
    assert.ok(ids(rec.result.passed).includes('W2.T1'))
    assert.ok(span(rec, 'W2.T1').start >= span(rec, 'W1.T1').end)
    assert.equal(planState(rec, args)['W2.T1'], 'done')
  })

  it('in waves mode, the next wave starts after the failed task is marked [!], and its prompt warns a dependency may have failed', async () => {
    const args = makeArgs([task('W1.T1'), task('W2.T1')], { mode: 'waves' })
    const rec = await run(args, {
      bookkeepTicks: 5,
      scripts: { 'W1.T1': { task: [nothing(), nothing(), nothing()] } },
    })
    assertCompleted(rec)
    const failBook = callsFor(rec, 'W1.T1', 'bookkeep')[0]
    assert.ok(failBook, 'the failed task is bookkept')
    assert.ok(span(rec, 'W2.T1').start >= failBook.end, 'W2.T1 starts after the [!] mark lands')
    const prompt = callsFor(rec, 'W2.T1', 'task')[0].prompt
    assert.match(prompt, /may have FAILED/)
    assert.match(prompt, /issues\.md/)
    assert.doesNotMatch(prompt, /every dependency has passed/)
  })

  it('in dag mode, the task prompt says every dependency has passed', async () => {
    const args = makeArgs([task('W1.T1'), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args)
    assertCompleted(rec)
    assert.match(callsFor(rec, 'W2.T1', 'task')[0].prompt, /every dependency has passed/)
  })

  it('in waves mode, a task already marked failed does not block the next wave', async () => {
    const args = makeArgs([task('W1.T1', { status: 'failed' }), task('W2.T1')], { mode: 'waves' })
    const rec = await run(args)
    assertCompleted(rec)
    assert.ok(ids(rec.result.passed).includes('W2.T1'))
  })

  it('class (a): dependents of a task that used all 3 attempts end [!] blocked, transitively, with an issues.md entry', async () => {
    const args = makeArgs([
      task('W1.T1'),
      task('W2.T1', { deps: ['W1.T1'] }),
      task('W3.T1', { deps: ['W2.T1'] }),
    ])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [nothing(), nothing(), nothing()] } },
    })
    assertCompleted(rec)
    assert.deepEqual(ids(rec.result.failed), ['W1.T1'])
    for (const id of ['W2.T1', 'W3.T1']) {
      assert.equal(attemptCalls(rec, id).length, 0, `${id} must never start`)
      assert.equal(entry(rec.result.skipped, id).reason, 'blocked')
      assert.equal(planState(rec, args)[id], 'failed', `${id} must end [!] in the plan file`)
      const book = callsFor(rec, id, 'bookkeep')
      assert.equal(book.length, 1)
      assert.match(book[0].prompt, /FAILED: blocked by W\d+\.T\d+/)
      assert.match(book[0].prompt, /issues\.md/)
    }
    assert.match(callsFor(rec, 'W2.T1', 'bookkeep')[0].prompt, /FAILED: blocked by W1\.T1/)
    assert.ok(rec.logs.some((l) => l.includes('W2.T1')), 'every skipped task is logged')
  })

  it('class (b): a needs_input task and its dependents stay [ ] and are returned for a re-run', async () => {
    const args = makeArgs([
      task('W1.T1'),
      task('W2.T1', { deps: ['W1.T1'] }),
      task('W3.T1', { deps: ['W2.T1'] }),
    ])
    const rec = await run(args, {
      scripts: {
        'W1.T1': { task: [ok(selfReport('needs_input', [], { question: 'Which database?' }))] },
      },
    })
    assertCompleted(rec)
    const state = planState(rec, args)
    assert.equal(state['W1.T1'], 'todo')
    assert.equal(state['W2.T1'], 'todo', 'a needs_input dependent stays [ ] in the plan file')
    assert.equal(state['W3.T1'], 'todo')
    for (const id of ['W1.T1', 'W2.T1', 'W3.T1']) {
      assert.equal(callsFor(rec, id, 'bookkeep').length, 0, `${id} must never be bookkept`)
    }
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'needs_input')
    assert.equal(entry(rec.result.skipped, 'W3.T1').reason, 'needs_input')
    assert.ok(ids(rec.result.needsInput).includes('W1.T1'))
    assert.deepEqual(ids(rec.result.failed), [])
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (4) task agent call', () => {
  it('uses a structured self-report schema and the task model', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args)
    assertCompleted(rec)
    const [call] = callsFor(rec, 'W1.T1', 'task')
    assert.equal(call.model, TIERS.heavy.model)
    assert.ok(call.schema && call.schema.type === 'object', 'task agent must pass a structured schema')
    const props = call.schema.properties
    for (const key of ['status', 'checks', 'summary', 'question']) {
      assert.ok(props[key], `self-report schema must define ${key}`)
    }
    assert.deepEqual([...props.status.enum].sort(), ['blocked', 'done', 'needs_input'])
    assert.equal(props.checks.type, 'array')
    assert.ok(props.checks.items.properties.command)
    assert.ok(props.checks.items.properties.exitCode)
  })

  it('labels the task agent with the task ID and title', async () => {
    const args = makeArgs([task('W1.T1', { title: 'Write the parser' })])
    const rec = await run(args)
    assertCompleted(rec)
    assert.equal(rec.calls[0].label, 'W1.T1 Write the parser')
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (5) tiered verification', () => {
  const verifyCount = async (t, report) => {
    const args = makeArgs([t], { verifierModel: 'verifier-alias' })
    const scripts = report ? { [t.id]: { task: [ok(report)] } } : {}
    const rec = await run(args, { scripts })
    assertCompleted(rec)
    return callsFor(rec, t.id, 'verify')
  }

  it('skips the verifier for light and balanced tasks with passing checks and done status', async () => {
    assert.equal((await verifyCount(task('W1.T1', { tier: 'light' }))).length, 0)
    assert.equal((await verifyCount(task('W1.T1', { tier: 'balanced' }))).length, 0)
  })

  for (const tier of ['heavy', 'reasoning']) {
    it(`always verifies ${tier} tasks, on args.verifierModel`, async () => {
      const calls = await verifyCount(task('W1.T1', { tier }))
      assert.equal(calls.length, 1)
      assert.equal(calls[0].model, 'verifier-alias')
      assert.equal(calls[0].label, 'verify:W1.T1')
      assert.ok(calls[0].schema && calls[0].schema.properties.verdict, 'verifier returns a structured verdict')
    })
  }

  const riskyFiles = [
    '.github/workflows/ci.yml',
    'plugins/afk/hooks/guard.sh',
    'plugins/afk/workflows/test/execute.test.mjs',
    'spec/plugins/afk/plan_to_args_spec.rb',
    'lib/parser_test.rb',
    'src/widget.test.ts',
    'lib/widget_spec.rb',
  ]
  for (const file of riskyFiles) {
    it(`verifies a light task that touches ${file}`, async () => {
      const calls = await verifyCount(task('W1.T1', { tier: 'light', files: ['docs/readme.md', file] }))
      assert.equal(calls.length, 1)
    })
  }

  it('verifies when the self-report has no checks', async () => {
    assert.equal((await verifyCount(task('W1.T1'), selfReport('done', []))).length, 1)
  })

  it('verifies when any check has a non-zero exit code', async () => {
    const checks = [
      { command: 'a', exitCode: 0 },
      { command: 'b', exitCode: 2 },
    ]
    assert.equal((await verifyCount(task('W1.T1'), selfReport('done', checks))).length, 1)
  })

  it('verifies when the status is not done', async () => {
    assert.equal((await verifyCount(task('W1.T1'), selfReport('blocked'))).length, 1)
  })

  it('passes the self-report to the verifier', async () => {
    const report = selfReport('done', [{ command: 'node --test sentinel-cmd', exitCode: 1 }], {
      summary: 'SENTINEL-SUMMARY',
    })
    const [call] = await verifyCount(task('W1.T1'), report)
    assert.match(call.prompt, /SENTINEL-SUMMARY/)
    assert.match(call.prompt, /node --test sentinel-cmd/)
    assert.ok(call.prompt.includes(PLAN_PATH))
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (6) retries, fixes, and escalation', () => {
  it('runs a fix agent with the verifier discrepancies after a verify FAIL', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { verify: [ok(failVerdict('SENTINEL-DISCREPANCY: the parser drops wrapped lines')), ok(PASS)] } },
    })
    assertCompleted(rec)
    const fixes = callsFor(rec, 'W1.T1', 'fix')
    assert.equal(fixes.length, 1)
    assert.ok(fixes[0].label.startsWith('fix:W1.T1'))
    assert.ok(fixes[0].prompt.includes('SENTINEL-DISCREPANCY: the parser drops wrapped lines'))
    assert.ok(fixes[0].prompt.includes(PLAN_PATH))
    assert.equal(fixes[0].model, TIERS.heavy.model, 'the first fix stays on the task tier')
    assert.equal(callsFor(rec, 'W1.T1', 'verify').length, 2, 'the fix is verified again')
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  const escalation = [
    ['light', ['tiny', 'tiny', 'mid']],
    ['balanced', ['mid', 'mid', 'big']],
    ['heavy', ['big', 'big', 'huge']],
    ['reasoning', ['huge', 'huge', 'huge']],
  ]
  for (const [tier, models] of escalation) {
    it(`escalates a ${tier} task one tier on the 3rd attempt (${models.join(' → ')})`, async () => {
      const args = makeArgs([task('W1.T1', { tier })])
      const rec = await run(args, {
        scripts: {
          'W1.T1': {
            task: [ok(selfReport('done', failingCheck))],
            fix: [ok(selfReport('done', failingCheck)), ok(selfReport('done', failingCheck))],
            verify: [ok(failVerdict('x')), ok(failVerdict('y')), ok(PASS)],
          },
        },
      })
      assertCompleted(rec)
      assert.deepEqual(attemptCalls(rec, 'W1.T1').map((c) => c.model), models)
      assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
    })
  }

  it('fails a task after 3 attempts, skips its dependents, and lets independent tasks continue', async () => {
    const args = makeArgs([
      task('W1.T1', { tier: 'heavy' }),
      task('W1.T2'),
      task('W2.T1', { deps: ['W1.T1'] }),
      task('W2.T2', { deps: ['W1.T2'] }),
    ])
    const rec = await run(args, {
      scripts: { 'W1.T1': { verify: [ok(failVerdict('a')), ok(failVerdict('b')), ok(failVerdict('c'))] } },
    })
    assertCompleted(rec)
    assert.equal(attemptCalls(rec, 'W1.T1').length, 3)
    assert.equal(callsFor(rec, 'W1.T1', 'verify').length, 3)
    assert.deepEqual(ids(rec.result.failed), ['W1.T1'])
    assert.equal(planState(rec, args)['W1.T1'], 'failed')
    assert.match(callsFor(rec, 'W1.T1', 'bookkeep')[0].prompt, /FAILED:/)
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'blocked')
    assert.deepEqual(ids(rec.result.passed).sort(), ['W1.T2', 'W2.T2'])
  })

  it('re-runs the task agent (not a fix agent) after an attempt that failed without a verify FAIL, sharing the attempt counter', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'light' })])
    const rec = await run(args, {
      scripts: {
        'W1.T1': {
          task: [nothing(), ok(selfReport('done', failingCheck))],
          fix: [ok(selfReport('done', failingCheck))],
          verify: [ok(failVerdict('still broken')), ok(PASS)],
        },
      },
    })
    assertCompleted(rec)
    const attempts = attemptCalls(rec, 'W1.T1')
    assert.deepEqual(attempts.map((c) => kindOf(c.label)), ['task', 'task', 'fix'])
    assert.deepEqual(attempts.map((c) => c.model), ['tiny', 'tiny', 'mid'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('does not pass a blocked self-report on a verifier PASS; the task agent runs again with the reason', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'light' })])
    const rec = await run(args, {
      scripts: {
        'W1.T1': {
          task: [ok(selfReport('blocked', [], { summary: 'SENTINEL-BLOCKER: cannot write the fixture' })), ok(selfReport('done'))],
          verify: [ok(PASS)],
        },
      },
    })
    assertCompleted(rec)
    const attempts = attemptCalls(rec, 'W1.T1')
    assert.deepEqual(attempts.map((c) => kindOf(c.label)), ['task', 'task'])
    assert.equal(callsFor(rec, 'W1.T1', 'verify').length, 1)
    assert.match(attempts[1].prompt, /attempt 2 of 3/)
    assert.match(attempts[1].prompt, /blocked/)
    assert.match(attempts[1].prompt, /SENTINEL-BLOCKER/)
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('re-runs the verifier, not the task agent, after a verifier that returned nothing', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args, { scripts: { 'W1.T1': { verify: [nothing(), ok(PASS)] } } })
    assertCompleted(rec)
    assert.equal(attemptCalls(rec, 'W1.T1').length, 1, 'the work on disk is kept')
    assert.equal(callsFor(rec, 'W1.T1', 'verify').length, 2)
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('names the reason the earlier attempt failed in the retry prompt', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'light' })], { autonomous: true })
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [ok(selfReport('needs_input', [], { question: 'which?' })), ok(selfReport('done'))] } },
    })
    assertCompleted(rec)
    const attempts = attemptCalls(rec, 'W1.T1')
    assert.equal(attempts.length, 2)
    assert.match(attempts[1].prompt, /needs_input/)
    assert.doesNotMatch(attempts[1].prompt, /did not report/)
  })

  it('counts a thrown task agent as a failed attempt and re-runs the task agent', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'light' })])
    const rec = await run(args, { scripts: { 'W1.T1': { task: [boom('agent crashed')] } } })
    assertCompleted(rec)
    assert.deepEqual(attemptCalls(rec, 'W1.T1').map((c) => kindOf(c.label)), ['task', 'task'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('fails a task whose task agent fails 3 times without a verify FAIL', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'light' })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [boom('crash 1'), nothing(), boom('crash 3')] } },
    })
    assertCompleted(rec)
    const attempts = attemptCalls(rec, 'W1.T1')
    assert.deepEqual(attempts.map((c) => kindOf(c.label)), ['task', 'task', 'task'])
    assert.deepEqual(attempts.map((c) => c.model), ['tiny', 'tiny', 'mid'])
    assert.deepEqual(ids(rec.result.failed), ['W1.T1'])
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (7) agent errors, fallback models, and budget', () => {
  it('retries a thrown call once with the tier fallback model (unknown alias)', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [boom('Unknown model alias: big')] } },
    })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['big', 'mid'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('retries a null return once with the tier fallback model', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'reasoning' })])
    const rec = await run(args, { scripts: { 'W1.T1': { task: [nothing()] } } })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['huge', 'big'])
    assert.deepEqual(attemptCalls(rec, 'W1.T1').length, 2, 'the fallback retry is not a new attempt')
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('counts a null after the fallback as one failed attempt, then retries on the task tier', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args, { scripts: { 'W1.T1': { task: [nothing(), nothing()] } } })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['big', 'mid', 'big'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('bookkeeps a task that passes after the budget runs out, so it is not re-run', async () => {
    let seen = null
    const exhausted = () => Boolean(seen && seen.calls.some((c) => c.label.startsWith('W1.T1 ') && c.end !== null))
    const budget = {
      total: 1000,
      spent: () => (exhausted() ? 1000 : 0),
      remaining: () => (exhausted() ? 0 : 1000),
    }
    const args = makeArgs([task('W1.T1', { tier: 'light' }), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(
      args,
      {
        onCall: (_call, r) => {
          seen = r
        },
      },
      { budget }
    )
    assertCompleted(rec)
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
    assert.equal(callsFor(rec, 'W1.T1', 'bookkeep').length, 1, 'the passed task is still marked [x]')
    assert.equal(planState(rec, args)['W1.T1'], 'done')
    assert.equal(callsFor(rec, 'W2.T1').length, 0)
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'budget')
  })

  it('counts a throw after the fallback as one failed attempt, then retries on the task tier', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [boom('Unknown model alias: big'), boom('Unknown model alias: mid')] } },
    })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['big', 'mid', 'big'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('does not treat a throw that merely mentions "budget" as the budget ceiling', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' }), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [boom('Unknown model alias: budget-model')] } },
    })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['big', 'mid'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T1', 'W2.T1'])
    assert.ok(!rec.logs.some((l) => /Budget stop/i.test(l)))
  })

  it('checks the budget before verify, fix, and bookkeep starts, not only before the first task call', async () => {
    let seen = null
    // The budget runs out when the task agent for W1.T1 finishes.
    const exhausted = () => Boolean(seen && seen.calls.some((c) => c.label.startsWith('W1.T1 ') && c.end !== null))
    const budget = {
      total: 1000,
      spent: () => (exhausted() ? 1000 : 0),
      remaining: () => (exhausted() ? 0 : 1000),
    }
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(
      args,
      {
        onCall: (_call, r) => {
          seen = r
        },
      },
      { budget }
    )
    assertCompleted(rec)
    assert.equal(callsFor(rec, 'W1.T1', 'verify').length, 0, 'no verify agent starts after the budget is gone')
    assert.equal(callsFor(rec, 'W1.T1', 'bookkeep').length, 0)
    assert.equal(entry(rec.result.skipped, 'W1.T1').reason, 'budget')
    assert.equal(planState(rec, args)['W1.T1'], 'todo')
  })

  it('does not retry with a fallback when the tier has none', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'balanced' })])
    const rec = await run(args, { scripts: { 'W1.T1': { task: [nothing()] } } })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['mid', 'mid'])
  })

  it('stops new starts when budget.remaining() reaches 0 and lets running tasks finish', async () => {
    // The budget runs out the moment W1.T2 finishes; W1.T1 is still running.
    let seen = null
    const exhausted = () => Boolean(seen && seen.calls.some((c) => c.label.startsWith('W1.T2 ') && c.end !== null))
    const budget = {
      total: 1000,
      spent: () => (exhausted() ? 1000 : 0),
      remaining: () => (exhausted() ? 0 : 1000),
    }
    const args = makeArgs([
      task('W1.T1'),
      task('W1.T2'),
      task('W2.T1', { deps: ['W1.T2'] }),
      task('W3.T1', { deps: ['W2.T1'] }),
    ])
    const rec = await run(
      args,
      {
        durations: { 'W1.T1': 20 },
        onCall: (_call, rec) => {
          seen = rec
        },
      },
      { budget }
    )
    assertCompleted(rec)
    assert.ok(ids(rec.result.passed).includes('W1.T2'))
    assert.ok(ids(rec.result.passed).includes('W1.T1'), 'the running task finishes')
    for (const id of ['W2.T1', 'W3.T1']) {
      assert.equal(callsFor(rec, id).length, 0, `${id} must not start after the budget stop`)
      assert.equal(entry(rec.result.skipped, id).reason, 'budget')
      assert.equal(planState(rec, args)[id], 'todo')
    }
    assert.ok(rec.logs.some((l) => /budget/i.test(l)), 'the budget stop is logged')
  })

  it('treats a budget-ceiling throw as a stop signal, not a failed attempt', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' }), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [boom('Token budget exhausted: the turn budget ceiling was reached')] } },
    })
    assertCompleted(rec)
    assert.equal(callsFor(rec, 'W1.T1', 'task').length, 1, 'no fallback retry and no new attempt after a budget throw')
    assert.equal(callsFor(rec, 'W1.T1', 'bookkeep').length, 0)
    assert.ok(!ids(rec.result.failed).includes('W1.T1'))
    assert.equal(callsFor(rec, 'W2.T1').length, 0)
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'budget')
    assert.equal(planState(rec, args)['W2.T1'], 'todo')
    assert.ok(rec.logs.some((l) => /budget/i.test(l)))
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (8) needs_input', () => {
  it('default mode: stops the task and its dependents and returns the question', async () => {
    const args = makeArgs([task('W1.T1'), task('W1.T2'), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [ok(selfReport('needs_input', [], { question: 'Keep the legacy API?' }))] } },
    })
    assertCompleted(rec)
    assert.equal(attemptCalls(rec, 'W1.T1').length, 1, 'no retry and no fix for needs_input in default mode')
    assert.equal(entry(rec.result.needsInput, 'W1.T1').question, 'Keep the legacy API?')
    assert.equal(callsFor(rec, 'W2.T1').length, 0)
    assert.ok(ids(rec.result.passed).includes('W1.T2'), 'independent work continues')
    assert.ok(!/never return needs_input/i.test(callsFor(rec, 'W1.T1', 'task')[0].prompt))
  })

  it('autonomous mode: the prompt forbids needs_input', async () => {
    const args = makeArgs([task('W1.T1')], { autonomous: true })
    const rec = await run(args)
    assertCompleted(rec)
    assert.match(callsFor(rec, 'W1.T1', 'task')[0].prompt, /never return needs_input/i)
  })

  it('autonomous mode: a returned needs_input counts as a failed attempt', async () => {
    const args = makeArgs([task('W1.T1')], { autonomous: true })
    const ask = () => ok(selfReport('needs_input', [], { question: 'Which?' }))
    const rec = await run(args, { scripts: { 'W1.T1': { task: [ask(), ask(), ask()] } } })
    assertCompleted(rec)
    const attempts = attemptCalls(rec, 'W1.T1')
    assert.deepEqual(attempts.map((c) => kindOf(c.label)), ['task', 'task', 'task'])
    assert.deepEqual(attempts.map((c) => c.model), ['tiny', 'tiny', 'mid'])
    assert.deepEqual(ids(rec.result.failed), ['W1.T1'])
    assert.deepEqual(rec.result.needsInput, [])
    assert.equal(planState(rec, args)['W1.T1'], 'failed')
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (9) bookkeeping', () => {
  it('marks each finished task through one bookkeep agent at a time on the light model', async () => {
    const args = makeArgs([task('W1.T1'), task('W1.T2'), task('W1.T3'), task('W1.T4')])
    const rec = await run(args, { bookkeepTicks: 10 })
    assertCompleted(rec)
    const book = rec.calls.filter((c) => kindOf(c.label) === 'bookkeep')
    assert.equal(book.length, 4)
    for (const c of book) {
      assert.equal(c.model, args.tiers.light.model)
      assert.equal(c.label, `bookkeep:${/W\d+\.T\d+/.exec(c.label)[0]}`)
      assert.ok(c.prompt.includes(PLAN_PATH))
      assert.ok(c.prompt.includes(/W\d+\.T\d+/.exec(c.label)[0]))
    }
    const sorted = [...book].sort((a, b) => a.start - b.start)
    for (let i = 1; i < sorted.length; i++) {
      assert.ok(sorted[i].start >= sorted[i - 1].end, 'bookkeep agents must never overlap')
    }
    assert.deepEqual(Object.values(planState(rec, args)), ['done', 'done', 'done', 'done'])
  })
})

describe('execute.js (9b) bookkeeping happens at each task\'s terminal state', () => {
  it('marks a fast later task [x] while an earlier-in-plan task is still running', async () => {
    const args = makeArgs([task('W1.T1'), task('W1.T2')])
    const rec = await run(args, { durations: { 'W1.T1': 50, 'W1.T2': 1 } })
    assertCompleted(rec)
    const slowTask = callsFor(rec, 'W1.T1', 'task')[0]
    const fastBook = callsFor(rec, 'W1.T2', 'bookkeep')[0]
    assert.ok(fastBook.start < slowTask.end, 'W1.T2 is bookkept before W1.T1 finishes')
    const slowBook = callsFor(rec, 'W1.T1', 'bookkeep')[0]
    assert.ok(slowBook.start >= slowTask.end)
    assert.ok(fastBook.end <= slowBook.start, 'one bookkeep agent at a time')
  })

  it('marks a failed task [!] at once and its blocked dependents as they settle', async () => {
    const args = makeArgs([task('W1.T1'), task('W1.T2'), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args, {
      durations: { 'W1.T2': 60 },
      scripts: { 'W1.T1': { task: [nothing(), nothing(), nothing()] } },
    })
    assertCompleted(rec)
    const slowTask = callsFor(rec, 'W1.T2', 'task')[0]
    assert.ok(callsFor(rec, 'W2.T1', 'bookkeep')[0].start < slowTask.end)
    assert.deepEqual(planState(rec, args), { 'W1.T1': 'failed', 'W1.T2': 'done', 'W2.T1': 'failed' })
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (9a) argument validation and opening log', () => {
  it('throws before any agent starts when args.tasks is missing', async () => {
    const args = makeArgs([task('W1.T1')])
    delete args.tasks
    const rec = await run(args)
    assert.ok(rec.error, 'the script must throw')
    assert.match(String(rec.error.message), /tasks/)
    assert.equal(rec.calls.length, 0)
  })

  it('throws before any agent starts when args.tasks is not an array', async () => {
    const args = makeArgs([task('W1.T1')])
    args.tasks = JSON.stringify(args.tasks)
    const rec = await run(args)
    assert.ok(rec.error)
    assert.match(String(rec.error.message), /tasks/)
    assert.equal(rec.calls.length, 0)
  })

  it('logs the plan name, task count, and wave count before the first agent call', async () => {
    const args = makeArgs([task('W1.T1'), task('W1.T2'), task('W2.T1'), task('W3.T1')])
    const rec = await run(args)
    assertCompleted(rec)
    const first = rec.logs[0]
    assert.ok(first, 'the script must log before starting')
    assert.match(first, /sample/)
    assert.match(first, /\b4 tasks\b/)
    assert.match(first, /\b3 waves\b/)
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (10) result shape', () => {
  it('returns {passed, failed, skipped, needsInput} arrays', async () => {
    const rec = await run(makeArgs([task('W1.T1')]))
    assertCompleted(rec)
    assert.deepEqual(Object.keys(rec.result).sort(), ['failed', 'needsInput', 'passed', 'skipped'])
    for (const k of ['passed', 'failed', 'skipped', 'needsInput']) assert.ok(Array.isArray(rec.result[k]))
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (11) prompts point to disk', () => {
  it('task prompts reference the codename file and the task block location, not the task text', async () => {
    const args = makeArgs([task('W1.T1', { title: 'Write the parser' })])
    const rec = await run(args)
    assertCompleted(rec)
    const t = args.tasks[0]
    const prompt = callsFor(rec, 'W1.T1', 'task')[0].prompt
    assert.ok(prompt.includes(`${PLUGIN_ROOT}/skills/orchestrator/prompts/codename-announcement.md`))
    assert.ok(prompt.includes(PLAN_PATH))
    assert.ok(prompt.includes(`${t.lineStart}-${t.lineEnd}`), 'prompt names the line range')
    assert.ok(prompt.includes('W1.T1'))
    assert.ok(prompt.includes('Write the parser'))
    assert.ok(prompt.includes('learnings.md'))
    assert.ok(!prompt.includes('**What to do**'))
    assert.ok(!prompt.includes('**Acceptance Criteria**'))
  })

  it('fix prompts reference the task block location', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args, { scripts: { 'W1.T1': { verify: [ok(failVerdict('d')), ok(PASS)] } } })
    assertCompleted(rec)
    const t = args.tasks[0]
    const prompt = callsFor(rec, 'W1.T1', 'fix')[0].prompt
    assert.ok(prompt.includes(PLAN_PATH))
    assert.ok(prompt.includes(`${t.lineStart}-${t.lineEnd}`))
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (12) Workflow script constraints', () => {
  it('declares meta as a pure literal', () => {
    const meta = extractMeta(readScript(EXECUTE_SCRIPT))
    assert.equal(meta.name, 'afk-execute')
    assert.equal(typeof meta.description, 'string')
  })

  it('uses no Date.now, Math.random, argless new Date, require, or imports', () => {
    assert.doesNotMatch(readScript(EXECUTE_SCRIPT), FORBIDDEN_API)
  })

  it('runs a mixed plan with Date and Math guarded', async () => {
    const args = makeArgs([
      task('W1.T1', { tier: 'heavy' }),
      task('W1.T2'),
      task('W2.T1', { deps: ['W1.T1', 'W1.T2'] }),
    ])
    const rec = await run(args)
    assertCompleted(rec)
    assert.deepEqual(ids(rec.result.passed).sort(), ['W1.T1', 'W1.T2', 'W2.T1'])
  })
})
