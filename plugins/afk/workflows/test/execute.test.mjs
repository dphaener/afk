// Specifies plugins/afk/workflows/execute.js, the afk DAG execution
// engine. `args` follows the plan_to_args.rb JSON contract (W1.T2).
// Numbered describe blocks match the execute.js behaviors in the plan task.

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  AGENT_KINDS,
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
  markDoneCommand,
  markers,
  nothing,
  ok,
  readScript,
  responder,
  runScript,
  selfReport,
  span,
  task,
  tellsToMark,
} from './lib/harness.mjs'

// Every agent the engine spawns does a task's work or verifies it. Nothing is
// spawned only to edit the plan file: `afk plan check` does that, run by the
// agent whose result settles the task.
function assertOnlyWorkAgents(rec) {
  for (const c of rec.calls) {
    assert.doesNotMatch(c.label, /bookkeep/i, `no bookkeeping agent may be spawned (got "${c.label}")`)
    assert.match(c.label, /^(verify:W\d+\.T\d+|fix:W\d+\.T\d+#\d+|W\d+\.T\d+( .*)?)$/, `unexpected agent "${c.label}"`)
    assert.ok(AGENT_KINDS.includes(kindOf(c.label)))
    assert.equal(c.opts.phase, 'Execute', `"${c.label}" must run in the Execute phase`)
    assert.ok(c.schema, `"${c.label}" must return structured output; a bare "ok" reply is the old bookkeeping shape`)
  }
}

// Every run in this file goes through here, so the no-bookkeeping-agent rule
// is checked on every run shape the suite exercises, not on one sample.
async function run(args, respondOpts = {}, extra = {}) {
  const rec = await runScript(EXECUTE_SCRIPT, { args, respond: responder(respondOpts), ...extra })
  assertOnlyWorkAgents(rec)
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
    assert.equal(callsFor(rec, 'W1.T1').length, 0, 'a done task gets no agent calls')
    assert.deepEqual(ids(rec.result.passed).sort(), ['W1.T1', 'W2.T1'])
    assert.deepEqual(rec.result.mark, [])
  })

  it('in dag mode, a task already marked failed blocks its dependents', async () => {
    const args = makeArgs([task('W1.T1', { status: 'failed' }), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args)
    assertCompleted(rec)
    assert.equal(callsFor(rec, 'W1.T1').length, 0)
    assert.equal(attemptCalls(rec, 'W2.T1').length, 0)
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'blocked')
    assert.deepEqual(rec.result.mark, [{ id: 'W2.T1', state: 'failed', reason: 'blocked by W1.T1' }])
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
    assert.deepEqual(markers(rec)['W2.T1'], ['task'], 'W2.T1 passed and its own agent marked it')
    assert.deepEqual(ids(rec.result.mark), ['W1.T1'])
  })

  it('in waves mode, the next wave is told which tasks failed in this run, not to look for [!] in the plan', async () => {
    const args = makeArgs([task('W1.T1'), task('W2.T1')], { mode: 'waves' })
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [nothing(), nothing(), nothing()] } },
    })
    assertCompleted(rec)
    assert.ok(span(rec, 'W2.T1').start >= span(rec, 'W1.T1').end, 'W2.T1 starts after W1.T1 has failed')
    assert.match(callsFor(rec, 'W1.T1', 'task')[0].prompt, /no task has failed so far in this run/)
    const prompt = callsFor(rec, 'W2.T1', 'task')[0].prompt
    assert.match(prompt, /a task in it may have failed/)
    assert.match(prompt, /these tasks have failed so far in this run: W1\.T1 \(3 attempts failed/)
    assert.match(prompt, /written to the plan file only after the run ends/)
    assert.doesNotMatch(prompt, /every dependency has passed/)
    for (const c of rec.calls) {
      assert.doesNotMatch(c.prompt, /\[!\]/, `"${c.label}" must not send an agent looking for a [!] mark mid-run`)
      assert.doesNotMatch(c.prompt, /listed in [^ ]*issues\.md/)
    }
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

  it('class (a): dependents of a task that used all 3 attempts are blocked, transitively, and returned in mark as failed', async () => {
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
      assert.equal(callsFor(rec, id).length, 0, `${id} must never start, and no agent is spawned to mark it`)
      assert.equal(entry(rec.result.skipped, id).reason, 'blocked')
    }
    assert.equal(entry(rec.result.skipped, 'W2.T1').by, 'W1.T1')
    assert.equal(entry(rec.result.skipped, 'W3.T1').by, 'W2.T1')
    assert.deepEqual(rec.result.mark.slice(1), [
      { id: 'W2.T1', state: 'failed', reason: 'blocked by W1.T1' },
      { id: 'W3.T1', state: 'failed', reason: 'blocked by W2.T1' },
    ])
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
    assert.deepEqual(rec.result.mark, [], 'needs_input tasks and their dependents stay [ ]: nothing to mark')
    assert.equal(rec.calls.length, 1, 'only the one task agent ran')
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
    assert.equal(entry(rec.result.failed, 'W1.T1').reason, entry(rec.result.mark, 'W1.T1').reason)
    assert.match(entry(rec.result.mark, 'W1.T1').reason, /^3 attempts failed; last: verify FAIL: c/)
    assert.deepEqual(rec.result.mark.map((m) => `${m.id}:${m.state}`), ['W1.T1:failed', 'W2.T1:failed'])
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

  // A throw on a typed call is first retried without the agent type (13), so
  // an attempt fails on a throw only when that retry throws too: two booms.
  it('counts a thrown task agent as a failed attempt and re-runs the task agent', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'light' })])
    const rec = await run(args, { scripts: { 'W1.T1': { task: [boom('agent crashed'), boom('agent crashed')] } } })
    assertCompleted(rec)
    const calls = attemptCalls(rec, 'W1.T1')
    assert.deepEqual(calls.map((c) => kindOf(c.label)), ['task', 'task', 'task'])
    assert.match(calls[2].prompt, /attempt 2 of 3/)
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('fails a task whose task agent fails 3 times without a verify FAIL', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'light' })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [boom('crash 1'), boom('crash 1 again'), nothing(), boom('crash 3'), boom('crash 3 again')] } },
    })
    assertCompleted(rec)
    const attempts = attemptCalls(rec, 'W1.T1')
    assert.deepEqual(attempts.map((c) => kindOf(c.label)), ['task', 'task', 'task', 'task', 'task'])
    assert.deepEqual(attempts.map((c) => c.model), ['tiny', 'tiny', 'tiny', 'mid', 'mid'], 'each throw is retried once without the agent type, on the same model')
    assert.deepEqual(ids(rec.result.failed), ['W1.T1'])
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (7) agent errors, fallback models, and budget', () => {
  it('retries a thrown call once with the tier fallback model (unknown alias)', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [boom('Unknown model alias: big'), boom('Unknown model alias: big')] } },
    })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['big', 'big', 'mid'])
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

  it('a task that passes as the budget runs out was already marked by its own agent, so nothing runs after the stop', async () => {
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
    assert.equal(rec.calls.length, 1, 'no agent starts after the budget is gone, not even to mark the plan')
    assert.ok(tellsToMark(rec.calls[0], 'W1.T1'), 'the task agent was told to mark its own task, so it is not re-run')
    assert.equal(callsFor(rec, 'W2.T1').length, 0)
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'budget')
    assert.deepEqual(rec.result.mark, [])
  })

  it('counts a throw after the fallback as one failed attempt, then retries on the task tier', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' })])
    const rec = await run(args, {
      scripts: {
        'W1.T1': {
          task: [
            boom('Unknown model alias: big'),
            boom('Unknown model alias: big'),
            boom('Unknown model alias: mid'),
            boom('Unknown model alias: mid'),
          ],
        },
      },
    })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['big', 'big', 'mid', 'mid', 'big'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('does not treat a throw that merely mentions "budget" as the budget ceiling', async () => {
    const args = makeArgs([task('W1.T1', { tier: 'heavy' }), task('W2.T1', { deps: ['W1.T1'] })])
    const rec = await run(args, {
      scripts: { 'W1.T1': { task: [boom('Unknown model alias: budget-model'), boom('Unknown model alias: budget-model')] } },
    })
    assertCompleted(rec)
    assert.deepEqual(callsFor(rec, 'W1.T1', 'task').map((c) => c.model), ['big', 'big', 'mid'])
    assert.deepEqual(ids(rec.result.passed), ['W1.T1', 'W2.T1'])
    assert.ok(!rec.logs.some((l) => /Budget stop/i.test(l)))
  })

  it('checks the budget before verify and fix starts, not only before the first task call', async () => {
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
    assert.equal(rec.calls.length, 1)
    assert.equal(entry(rec.result.skipped, 'W1.T1').reason, 'budget')
    assert.deepEqual(markers(rec), {}, 'a heavy task is marked by its verifier, which never ran, so it stays [ ]')
    assert.deepEqual(rec.result.mark, [])
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
      assert.equal(entry(rec.result.mark, id), undefined, `${id} stays [ ] for a re-run`)
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
    assert.equal(rec.calls.length, 1)
    assert.ok(!ids(rec.result.failed).includes('W1.T1'))
    assert.equal(callsFor(rec, 'W2.T1').length, 0)
    assert.equal(entry(rec.result.skipped, 'W2.T1').reason, 'budget')
    assert.deepEqual(rec.result.mark, [], 'budget stops stay [ ] for a re-run')
    assert.ok(rec.logs.some((l) => /budget/i.test(l)))
  })
})

// ---------------------------------------------------------------------------
// Each agent names a plugin agent definition (plugins/afk/agents/), which
// limits the tools it starts with. A session that started before the plugin
// was updated does not know the types, and agent() then throws.
//
// Load-bearing: the refused-type run (without the retry every task of such a
// session would fail all three attempts), the budget row (a budget stop must
// never start another agent), the labels row (token accounting detects an
// agent's role from its label), and the null row (an agent that ran and
// returned nothing is not a refused type; repeating it would double the cost
// of every real failure).
describe('execute.js (13) agent types', () => {
  const REFUSED = (type) =>
    `agent({agentType}): agent type '${type}' not found. Available agents: claude, Explore, general-purpose, Plan`

  // A session that knows no afk agent type: every typed call throws.
  const refusing = (respondOpts = {}) => {
    const inner = responder(respondOpts)
    return (call, rec) => (call.opts.agentType ? boom(REFUSED(call.opts.agentType)) : inner(call, rec))
  }

  const fixScripts = () => ({
    'W1.T1': { task: [ok(selfReport('done', failingCheck))], verify: [ok(failVerdict('d')), ok(PASS)] },
  })

  it("each role's call carries its agent type, and labels and the other options are unchanged", async () => {
    const rec = await run(makeArgs([task('W1.T1', { tier: 'heavy', title: 'Write the parser' })]), { scripts: fixScripts() })
    assertCompleted(rec)
    assert.deepEqual(
      rec.calls.map((c) => [c.label, c.opts.agentType]),
      [
        ['W1.T1 Write the parser', 'afk:task'],
        ['verify:W1.T1', 'afk:verifier'],
        ['fix:W1.T1#1', 'afk:task'],
        ['verify:W1.T1', 'afk:verifier'],
      ]
    )
    for (const c of rec.calls) {
      assert.deepEqual(Object.keys(c.opts).sort(), ['agentType', 'label', 'model', 'phase', 'schema'], c.label)
      assert.equal(c.opts.phase, 'Execute')
    }
    assert.ok(!rec.logs.some((l) => /agent type/.test(l)), 'nothing is logged when the type is accepted')
  })

  it('a refused agent type is retried once without it, with one log line, and the run result is the same', async () => {
    const args = () => makeArgs([task('W1.T1', { tier: 'heavy' }), task('W2.T1', { deps: ['W1.T1'] })])
    const typed = await run(args(), { scripts: fixScripts() })
    const rec = await runScript(EXECUTE_SCRIPT, { args: args(), respond: refusing({ scripts: fixScripts() }) })
    assertOnlyWorkAgents(rec)
    assertCompleted(rec)
    assert.deepEqual(rec.result, typed.result)
    assert.deepEqual(ids(rec.result.passed), ['W1.T1', 'W2.T1'])

    assert.equal(rec.calls.length, typed.calls.length * 2, 'every call is made once with the type and once without')
    for (let i = 0; i < rec.calls.length; i += 2) {
      const [first, second] = [rec.calls[i], rec.calls[i + 1]]
      const want = typed.calls[i / 2]
      assert.equal(first.opts.agentType, want.opts.agentType)
      assert.ok(first.threw)
      assert.ok(!('agentType' in second.opts), 'the retry names no agent type')
      assert.deepEqual({ ...first.opts, agentType: undefined }, { ...second.opts, agentType: undefined }, 'the retry is the same call')
      assert.equal(second.label, want.label, 'labels are unchanged')
      assert.equal(second.prompt, want.prompt, 'the retry does not use up a task attempt')
      assert.equal(second.model, want.model, 'the retry stays on the same model')
    }
    const lines = rec.logs.filter((l) => /agent type/.test(l))
    assert.equal(lines.length, typed.calls.length, 'one log line for each refused call')
    assert.match(lines[0], /^W1\.T1 Task W1\.T1: agent type afk:task failed \(.*not found.*\); retrying once without it/)
  })

  it('an agent type call stopped by the budget ceiling is not retried without the type', async () => {
    const rec = await run(makeArgs([task('W1.T1', { tier: 'heavy' })]), {
      scripts: { 'W1.T1': { task: [boom('Token budget exhausted: the turn budget ceiling was reached')] } },
    })
    assertCompleted(rec)
    assert.equal(rec.calls.length, 1)
    assert.equal(rec.calls[0].opts.agentType, 'afk:task')
    assert.equal(entry(rec.result.skipped, 'W1.T1').reason, 'budget')
    assert.ok(!rec.logs.some((l) => /agent type/.test(l)))
  })

  it('an agent type call is not retried without the type once the budget is gone', async () => {
    let seen = null
    const exhausted = () => Boolean(seen && seen.calls.some((c) => c.end !== null))
    const budget = { total: 1000, spent: () => (exhausted() ? 1000 : 0), remaining: () => (exhausted() ? 0 : 1000) }
    const rec = await run(
      makeArgs([task('W1.T1', { tier: 'heavy' })]),
      { scripts: { 'W1.T1': { task: [boom('agent crashed')] } }, onCall: (_c, r) => (seen = r) },
      { budget }
    )
    assertCompleted(rec)
    assert.equal(rec.calls.length, 1, 'neither the retry without the type nor the fallback model starts')
    assert.equal(entry(rec.result.skipped, 'W1.T1').reason, 'budget')
  })

  it('an agent type call that returns nothing is not retried without the type: it is a failed attempt', async () => {
    const rec = await run(makeArgs([task('W1.T1', { tier: 'balanced' })]), { scripts: { 'W1.T1': { task: [nothing()] } } })
    assertCompleted(rec)
    const calls = callsFor(rec, 'W1.T1', 'task')
    assert.deepEqual(calls.map((c) => c.opts.agentType), ['afk:task', 'afk:task'])
    assert.match(calls[1].prompt, /attempt 2 of 3/)
    assert.ok(!rec.logs.some((l) => /agent type/.test(l)))
  })

  it('the retry without the agent type comes before the fallback model, which names the type again', async () => {
    const rec = await run(makeArgs([task('W1.T1', { tier: 'heavy' })]), {
      scripts: { 'W1.T1': { task: [boom('Unknown model alias: big'), boom('Unknown model alias: big')] } },
    })
    assertCompleted(rec)
    assert.deepEqual(
      callsFor(rec, 'W1.T1', 'task').map((c) => [c.model, c.opts.agentType]),
      [
        ['big', 'afk:task'],
        ['big', undefined],
        ['mid', 'afk:task'],
      ]
    )
    assert.ok(callsFor(rec, 'W1.T1', 'task').every((c) => !/attempt 2 of 3/.test(c.prompt)), 'all three calls are one attempt')
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('a refused agent type on the verifier and on the fallback model is retried without it too', async () => {
    const rec = await runScript(EXECUTE_SCRIPT, {
      args: makeArgs([task('W1.T1', { tier: 'heavy' })]),
      respond: refusing({ scripts: { 'W1.T1': { task: [nothing()] } } }),
    })
    assertOnlyWorkAgents(rec)
    assertCompleted(rec)
    assert.deepEqual(
      rec.calls.map((c) => [kindOf(c.label), c.model, c.opts.agentType]),
      [
        ['task', 'big', 'afk:task'],
        ['task', 'big', undefined],
        ['task', 'mid', 'afk:task'],
        ['task', 'mid', undefined],
        ['verify', 'mid', 'afk:verifier'],
        ['verify', 'mid', undefined],
      ]
    )
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
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
    assert.equal(entry(rec.result.mark, 'W1.T1').state, 'failed')
    assert.match(entry(rec.result.mark, 'W1.T1').reason, /needs_input in autonomous mode/)
  })
})

// ---------------------------------------------------------------------------
describe('execute.js (9) marking the plan file', () => {
  // Load-bearing rows: the two "verifier certain" groups (tier, risky file)
  // must NOT put the command in the task prompt, because the script sends
  // those tasks to the verifier whatever the self-report says; a task agent
  // that ticked the box there would mark a task that can still fail. The
  // default task() file is a plain .md, so the light and balanced rows are
  // the only ones where the task agent settles the task by itself.
  const whoMarks = [
    { name: 'light task, plain file', t: () => task('W1.T1', { tier: 'light' }), by: ['task'] },
    { name: 'balanced task, plain file', t: () => task('W1.T1', { tier: 'balanced' }), by: ['task'] },
    { name: 'heavy task', t: () => task('W1.T1', { tier: 'heavy' }), by: ['verify'] },
    { name: 'reasoning task', t: () => task('W1.T1', { tier: 'reasoning' }), by: ['verify'] },
    { name: 'light task touching a test file', t: () => task('W1.T1', { files: ['src/a.md', 'src/a.test.ts'] }), by: ['verify'] },
    { name: 'light task touching a hook', t: () => task('W1.T1', { files: ['plugins/afk/hooks/x.sh'] }), by: ['verify'] },
    { name: 'light task touching CI', t: () => task('W1.T1', { files: ['.github/workflows/ci.yml'] }), by: ['verify'] },
  ]
  for (const row of whoMarks) {
    it(`${row.name}: the ${row.by[0]} agent is the one told to run afk plan check ... done`, async () => {
      const rec = await run(makeArgs([row.t()]))
      assertCompleted(rec)
      assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
      assert.deepEqual(markers(rec), { 'W1.T1': row.by })
      assert.equal(tellsToMark(callsFor(rec, 'W1.T1', 'task')[0], 'W1.T1'), row.by[0] === 'task')
      assert.deepEqual(rec.result.mark, [], 'a passed task is never left for the caller to mark')
    })
  }

  it('the task prompt gives the command with the script\'s own pass condition: at least one check, all exit 0, status done', async () => {
    const rec = await run(makeArgs([task('W1.T1')]))
    assertCompleted(rec)
    const prompt = callsFor(rec, 'W1.T1', 'task')[0].prompt
    assert.ok(prompt.includes(`\`${markDoneCommand('W1.T1')}\``))
    assert.equal(markDoneCommand('W1.T1'), 'afk plan check --project /repo sample W1.T1 done')
    assert.match(prompt, /if, and only if, you ran at least one check, every check you ran exited 0, and the status you are about to return is "done"/)
    assert.match(prompt, /as your very last command/)
    assert.match(prompt, /In every other case do not run it/)
  })

  it('a task agent that reports no checks is not the one that settles the task: the verifier is told to mark it', async () => {
    const rec = await run(makeArgs([task('W1.T1')]), {
      scripts: { 'W1.T1': { task: [ok(selfReport('done', []))] } },
    })
    assertCompleted(rec)
    assert.deepEqual(markers(rec), { 'W1.T1': ['task', 'verify'] })
    assert.match(callsFor(rec, 'W1.T1', 'task')[0].prompt, /at least one check/)
  })

  it('a retried task agent still carries the command when no verifier is certain', async () => {
    const rec = await run(makeArgs([task('W1.T1')]), { scripts: { 'W1.T1': { task: [nothing()] } } })
    assertCompleted(rec)
    const attempts = callsFor(rec, 'W1.T1', 'task')
    assert.equal(attempts.length, 2)
    assert.match(attempts[1].prompt, /attempt 2 of 3/)
    assert.ok(tellsToMark(attempts[1], 'W1.T1'))
  })

  for (const tier of ['light', 'heavy']) {
    it(`the fix prompt never carries the command (${tier} task): a fix is always re-verified`, async () => {
      const rec = await run(makeArgs([task('W1.T1', { tier })]), {
        scripts: {
          'W1.T1': {
            task: [ok(selfReport('done', failingCheck))],
            verify: [ok(failVerdict('still broken')), ok(PASS)],
          },
        },
      })
      assertCompleted(rec)
      const [fix] = callsFor(rec, 'W1.T1', 'fix')
      assert.ok(fix, 'a fix agent ran')
      assert.equal(tellsToMark(fix, 'W1.T1'), false)
      assert.doesNotMatch(fix.prompt, /afk plan check [^`]*\bdone\b/)
      assert.match(fix.prompt, /you run it only if this prompt tells you to/)
      const verifies = callsFor(rec, 'W1.T1', 'verify')
      assert.equal(verifies.length, 2)
      assert.ok(verifies.every((c) => tellsToMark(c, 'W1.T1')), 'the verifier of the fix marks the task on PASS')
      assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
    })
  }

  it('the verifier prompt carries the command only when the self-report it was given says done', async () => {
    const rec = await run(makeArgs([task('W1.T1', { tier: 'heavy' })]), {
      scripts: {
        'W1.T1': {
          task: [ok(selfReport('blocked', [], { summary: 'cannot write the fixture' })), ok(selfReport('done'))],
          verify: [ok(PASS), ok(PASS)],
        },
      },
    })
    assertCompleted(rec)
    const [first, second] = callsFor(rec, 'W1.T1', 'verify')
    assert.equal(tellsToMark(first, 'W1.T1'), false, 'a PASS on a blocked report re-runs the task agent, so it must not mark')
    assert.match(first.prompt, /Do not modify any file, and do not run `afk plan check`/)
    assert.ok(tellsToMark(second, 'W1.T1'))
    assert.match(second.prompt, /Do not modify any file, with one exception: if, and only if, your verdict is PASS, run `afk plan check /)
    assert.match(second.prompt, /Never run it on FAIL/)
    assert.deepEqual(ids(rec.result.passed), ['W1.T1'])
  })

  it('a re-verification after a silent verifier keeps the command for a done self-report', async () => {
    const rec = await run(makeArgs([task('W1.T1', { tier: 'heavy' })]), {
      scripts: { 'W1.T1': { verify: [nothing(), ok(PASS)] } },
    })
    assertCompleted(rec)
    assert.deepEqual(markers(rec), { 'W1.T1': ['verify', 'verify'] })
  })

  // Load-bearing rows: W1.T1 and W1.T2 were already [!] and [x] when the run
  // started and must be absent, or a re-run would replace the recorded reason
  // of an earlier failure and add a second issues.md entry. W2.T1 is blocked
  // by a task that failed before this run and is itself new, so it is listed.
  it('mark names every task that failed or was blocked in this run and leaves out tasks already marked', async () => {
    const args = makeArgs([
      task('W1.T1', { status: 'failed' }),
      task('W1.T2', { status: 'done' }),
      task('W1.T3'),
      task('W2.T1', { deps: ['W1.T1'] }),
      task('W2.T2', { deps: ['W1.T3'] }),
      task('W2.T3', { deps: ['W1.T2'] }),
    ])
    const rec = await run(args, {
      scripts: { 'W1.T3': { task: [nothing(), nothing(), nothing()] } },
    })
    assertCompleted(rec)
    assert.deepEqual(rec.result.mark, [
      { id: 'W1.T3', state: 'failed', reason: '3 attempts failed; last: agent returned no self-report' },
      { id: 'W2.T1', state: 'failed', reason: 'blocked by W1.T1' },
      { id: 'W2.T2', state: 'failed', reason: 'blocked by W1.T3' },
    ])
    assert.deepEqual(ids(rec.result.failed), ['W1.T1', 'W1.T3'], 'failed still reports the preexisting failure')
    for (const f of rec.result.failed) assert.equal(typeof f.reason, 'string')
    assert.deepEqual(rec.result.skipped, [
      { id: 'W2.T1', reason: 'blocked', by: 'W1.T1' },
      { id: 'W2.T2', reason: 'blocked', by: 'W1.T3' },
    ])
    assert.deepEqual(ids(rec.result.passed), ['W1.T2', 'W2.T3'])
    assert.deepEqual(
      Object.keys(markers(rec)).sort(),
      ['W1.T3', 'W2.T3'],
      'only tasks that ran had an agent that could mark them; blocked and preexisting tasks spawn none'
    )
  })

  it('quotes a project root the shell would split, and drops --project when the plan is not under <project>/.afk/plans/', async () => {
    const spaced = await run(makeArgs([task('W1.T1')], { planPath: "/home/o'neil/my repo/.afk/plans/sample.md" }))
    assertCompleted(spaced)
    assert.ok(
      spaced.calls[0].prompt.includes(`afk plan check --project '/home/o'\\''neil/my repo' sample W1.T1 done`),
      'the root is single-quoted with the embedded quote escaped'
    )
    for (const planPath of ['/elsewhere/sample.md', 'repo/.afk/plans/sample.md']) {
      const rec = await run(makeArgs([task('W1.T1')], { planPath }))
      assertCompleted(rec)
      const prompt = rec.calls[0].prompt
      assert.ok(prompt.includes('`afk plan check sample W1.T1 done`'), `no --project for planPath "${planPath}"`)
      assert.match(prompt, /run it from the project root, the folder that holds \.afk\//)
    }
  })

  it('never spawns a bookkeeping agent, in a run where tasks pass, are fixed, fail, and are blocked', async () => {
    const args = makeArgs([
      task('W1.T1'),
      task('W1.T2', { tier: 'heavy' }),
      task('W1.T3'),
      task('W2.T1', { deps: ['W1.T3'] }),
      task('W2.T2', { deps: ['W1.T1', 'W1.T2'] }),
    ])
    const rec = await run(args, {
      scripts: {
        'W1.T2': { verify: [ok(failVerdict('x')), ok(PASS)] },
        'W1.T3': { task: [nothing(), nothing(), nothing()] },
      },
    })
    assertCompleted(rec)
    assert.deepEqual(ids(rec.result.passed), ['W1.T1', 'W1.T2', 'W2.T2'])
    assert.equal(rec.calls.filter((c) => /bookkeep/i.test(c.label)).length, 0)
    assert.deepEqual(
      rec.calls.map((c) => `${kindOf(c.label)}:${/W\d+\.T\d+/.exec(c.label)[0]}`).sort(),
      [
        'fix:W1.T2',
        'task:W1.T1',
        'task:W1.T2',
        'task:W1.T3',
        'task:W1.T3',
        'task:W1.T3',
        'task:W2.T2',
        'verify:W1.T2',
        'verify:W1.T2',
      ],
      'every agent call is task work or verification; none exists to edit the plan'
    )
    assert.deepEqual(rec.phases, ['Execute'])
    assert.doesNotMatch(readScript(EXECUTE_SCRIPT), /bookkeep/i)
  })
})

describe('execute.js (9b) lean-cycle rules and QA evidence in the prompts', () => {
  const fixRun = (t) =>
    run(makeArgs([t]), {
      scripts: { [t.id]: { task: [ok(selfReport('done', failingCheck))], verify: [ok(failVerdict('d')), ok(PASS)] } },
    })

  it('task and fix prompts carry the focused-tests, no-polling, and four-minute rules', async () => {
    const rec = await fixRun(task('W1.T1'))
    assertCompleted(rec)
    for (const kind of ['task', 'fix']) {
      const prompt = callsFor(rec, 'W1.T1', kind)[0].prompt
      assert.match(prompt, /Run only the focused lint and tests for the files you touched and the task's Acceptance Criteria commands; never run the whole suite/, kind)
      assert.match(prompt, /Never sleep or poll/, kind)
      assert.match(prompt, /No single command may run longer than about four minutes/, kind)
    }
  })

  it('task and fix prompts allow the evidence files and name afk plan check as the plan file\'s only writer', async () => {
    const rec = await fixRun(task('W1.T1'))
    assertCompleted(rec)
    for (const kind of ['task', 'fix']) {
      const prompt = callsFor(rec, 'W1.T1', kind)[0].prompt
      assert.match(prompt, /the task's evidence files under \.afk\/sample\/evidence\//, kind)
      assert.match(prompt, /Never edit the plan file by hand; `afk plan check` is its only writer/, kind)
      assert.doesNotMatch(prompt, /engine marks checkboxes/, kind)
      assert.match(prompt, /Never write a "## \(ID\) FAILED" heading in issues\.md/, kind)
    }
    const verify = callsFor(rec, 'W1.T1', 'verify')[0].prompt
    assert.match(verify, /other than the notepads and the task's evidence files under \.afk\/sample\/evidence\//)
    assert.match(verify, /never edit the plan file by hand/)
  })

  it('a task given qa in args.tasks[] has each evidence path in its task and fix prompts', async () => {
    const qa = [
      { tool: 'Bash', steps: 'run the tests', expected: 'exit 0', evidence: '.afk/sample/evidence/w1-t1-tests.txt' },
      { tool: 'Bash', steps: 'run the binary', expected: 'prints ok', evidence: '.afk/sample/evidence/w1-t1-run.txt' },
    ]
    const rec = await fixRun(task('W1.T1', { qa }))
    assertCompleted(rec)
    for (const kind of ['task', 'fix']) {
      const prompt = callsFor(rec, 'W1.T1', kind)[0].prompt
      assert.match(prompt, /Carry out each of the task's QA Scenarios and save its evidence at the path the plan names: /, kind)
      for (const sc of qa) assert.ok(prompt.includes(`\`${sc.evidence}\``), `${kind} prompt names ${sc.evidence}`)
      assert.ok(!prompt.includes('run the binary'), 'the scenario text stays in the plan; the prompt points to disk')
    }
  })

  it('a task with no qa in args still gets the QA Scenarios obligation', async () => {
    const rec = await run(makeArgs([task('W1.T1')]))
    assertCompleted(rec)
    assert.match(rec.calls[0].prompt, /Carry out each of the task's QA Scenarios and save its evidence at the path the plan names\./)
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
  it('returns {passed, failed, skipped, needsInput, mark} arrays', async () => {
    const rec = await run(makeArgs([task('W1.T1')]))
    assertCompleted(rec)
    assert.deepEqual(Object.keys(rec.result).sort(), ['failed', 'mark', 'needsInput', 'passed', 'skipped'])
    for (const k of ['passed', 'failed', 'skipped', 'needsInput', 'mark']) assert.ok(Array.isArray(rec.result[k]))
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
    assert.deepEqual(meta.phases.map((p) => p.title), ['Execute'], 'there is no bookkeeping phase')
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
