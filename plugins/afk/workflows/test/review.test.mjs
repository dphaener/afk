// Specifies plugins/afk/workflows/review.js, the afk review Workflow
// script. args: {plan, planPath, pluginRoot, diffCommand, checks,
// checkModel, criticModel, criticFallbackModel}.

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  FORBIDDEN_API,
  PLAN_PATH,
  PLUGIN_ROOT,
  REVIEW_SCRIPT,
  boom,
  extractMeta,
  nothing,
  ok,
  readScript,
  runScript,
} from './lib/harness.mjs'

const DIFF = "git diff 4f2a9c1...HEAD -- . ':(exclude).afk'"

const PROMPT_FILES = {
  compliance: 'check-plan-compliance.md',
  quality: 'check-code-quality.md',
  scope: 'check-scope-fidelity.md',
}

function makeReviewArgs(checks = ['compliance', 'quality', 'scope']) {
  return {
    plan: 'sample',
    planPath: PLAN_PATH,
    pluginRoot: PLUGIN_ROOT,
    diffCommand: DIFF,
    checks,
    checkModel: 'check-alias',
    criticModel: 'critic-alias',
    criticFallbackModel: 'critic-fallback-alias',
  }
}

const checkOutput = (name) => ({
  verdict: 'FAIL',
  findings: [{ severity: 'major', taskId: 'W1.T1', text: `SENTINEL-FINDING-${name}` }],
})

const criticOutput = (name) => ({
  verdict: 'PASS',
  findings: [{ severity: 'minor', taskId: 'W1.T1', text: `reconciled-${name}` }],
})

const nameOf = (label) => label.split(':')[1]
const kindOf = (label) => label.split(':')[0]

// replies[label] is a queue of replies; otherwise defaults per kind.
function respond(replies = {}, durations = {}) {
  return (call) => {
    const q = replies[call.label]
    if (q && q.length) return q.shift()
    const name = nameOf(call.label)
    const ticks = durations[call.label] || 1
    if (kindOf(call.label) === 'review') return ok(checkOutput(name), ticks)
    if (kindOf(call.label) === 'critic') return ok(criticOutput(name), ticks)
    return ok('unexpected call')
  }
}

async function review(args, replies, durations) {
  const rec = await runScript(REVIEW_SCRIPT, { args, respond: respond(replies, durations) })
  if (rec.error) throw rec.error
  return rec
}

const callsOf = (rec, label) => rec.calls.filter((c) => c.label === label)

describe('review.js pipelines', () => {
  it('runs one check agent then one critic agent per requested check', async () => {
    const rec = await review(makeReviewArgs())
    for (const name of ['compliance', 'quality', 'scope']) {
      const check = callsOf(rec, `review:${name}`)
      const critic = callsOf(rec, `critic:${name}`)
      assert.equal(check.length, 1, `one review:${name}`)
      assert.equal(critic.length, 1, `one critic:${name}`)
      assert.ok(critic[0].start >= check[0].end, 'the critic runs after its check')
      assert.equal(check[0].model, 'check-alias')
      assert.equal(critic[0].model, 'critic-alias')
    }
    assert.equal(rec.calls.length, 6)
  })

  it('has no barrier between checks: a fast check reaches its critic while a slow check still runs', async () => {
    const rec = await review(makeReviewArgs(['compliance', 'quality']), {}, { 'review:compliance': 50 })
    const slow = callsOf(rec, 'review:compliance')[0]
    const fastCritic = callsOf(rec, 'critic:quality')[0]
    assert.ok(fastCritic.start < slow.end)
  })

  it('runs only the requested checks', async () => {
    const rec = await review(makeReviewArgs(['quality']))
    assert.deepEqual(rec.calls.map((c) => c.label).sort(), ['critic:quality', 'review:quality'])
    assert.deepEqual(Object.keys(rec.result.checks), ['quality'])
  })
})

describe('review.js schemas and result', () => {
  it('uses structured schemas for checks and critics', async () => {
    const rec = await review(makeReviewArgs(['scope']))
    const check = callsOf(rec, 'review:scope')[0]
    const critic = callsOf(rec, 'critic:scope')[0]
    assert.ok(check.schema && check.schema.type === 'object', 'check agent uses a schema')
    assert.ok(critic.schema && critic.schema.type === 'object', 'critic agent uses a schema')
    const props = critic.schema.properties
    assert.deepEqual([...props.verdict.enum].sort(), ['FAIL', 'PASS'])
    assert.equal(props.findings.type, 'array')
    const item = props.findings.items.properties
    assert.deepEqual([...item.severity.enum].sort(), ['blocker', 'major', 'minor'])
    assert.ok(item.taskId)
    assert.ok(item.text)
  })

  it("returns {checks: {name: {verdict, findings}}} from each critic's reconciled output", async () => {
    const rec = await review(makeReviewArgs())
    assert.deepEqual(Object.keys(rec.result), ['checks'])
    assert.deepEqual(Object.keys(rec.result.checks).sort(), ['compliance', 'quality', 'scope'])
    for (const name of ['compliance', 'quality', 'scope']) {
      const got = rec.result.checks[name]
      assert.deepEqual({ verdict: got.verdict, findings: got.findings }, criticOutput(name))
    }
  })
})

describe('review.js prompts', () => {
  it('check prompts name their prompt file, the plan path, and the diff command verbatim', async () => {
    const rec = await review(makeReviewArgs())
    for (const [name, file] of Object.entries(PROMPT_FILES)) {
      const prompt = callsOf(rec, `review:${name}`)[0].prompt
      assert.ok(prompt.includes(`${PLUGIN_ROOT}/skills/reviewer/prompts/${file}`), `${name} prompt names ${file}`)
      assert.ok(prompt.includes(PLAN_PATH))
      assert.ok(prompt.includes(DIFF), 'diff command is carried verbatim')
    }
  })

  it("critic prompts name the critic file, the plan path, the diff command, and the check's findings", async () => {
    const rec = await review(makeReviewArgs())
    for (const name of Object.keys(PROMPT_FILES)) {
      const prompt = callsOf(rec, `critic:${name}`)[0].prompt
      assert.ok(prompt.includes(`${PLUGIN_ROOT}/skills/reviewer/prompts/adversarial-critic.md`))
      assert.ok(prompt.includes(PLAN_PATH))
      assert.ok(prompt.includes(DIFF))
      assert.ok(prompt.includes(`SENTINEL-FINDING-${name}`), "critic receives the check's findings")
    }
  })

  it('prompts never point at notepads, learnings, or execution history', async () => {
    const rec = await review(makeReviewArgs())
    for (const call of rec.calls) {
      assert.doesNotMatch(call.prompt, /notepad|learnings|decisions\.md|execution history/i, call.label)
    }
  })
})

describe('review.js errors and fallback', () => {
  it('retries a null critic once with criticFallbackModel', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [nothing()] })
    assert.deepEqual(callsOf(rec, 'critic:quality').map((c) => c.model), ['critic-alias', 'critic-fallback-alias'])
    assert.deepEqual(rec.result.checks.quality.findings, criticOutput('quality').findings)
  })

  it('retries a thrown critic once with criticFallbackModel', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [boom('Unknown model alias: critic-alias')] })
    assert.deepEqual(callsOf(rec, 'critic:quality').map((c) => c.model), ['critic-alias', 'critic-fallback-alias'])
    assert.equal(rec.result.checks.quality.verdict, 'PASS')
  })

  it("reports a thrown check call as that check's FAIL with a blocker naming the error", async () => {
    const rec = await review(makeReviewArgs(), { 'review:scope': [boom('SENTINEL-ERROR check agent crashed')] })
    const scope = rec.result.checks.scope
    assert.equal(scope.verdict, 'FAIL')
    assert.ok(
      scope.findings.some((f) => f.severity === 'blocker' && f.text.includes('SENTINEL-ERROR check agent crashed')),
      'a blocker finding names the error'
    )
    assert.equal(callsOf(rec, 'critic:scope').length, 0, 'no critic runs for a check that threw')
    assert.equal(rec.result.checks.compliance.verdict, 'PASS', 'other checks are unaffected')
    assert.equal(rec.result.checks.quality.verdict, 'PASS')
  })
})

describe('review.js tolerates missing findings and names the plan', () => {
  it('defaults missing findings to [] when the check output omits them and the critic fails', async () => {
    const rec = await review(makeReviewArgs(['quality']), {
      'review:quality': [ok({ verdict: 'PASS' })],
      'critic:quality': [nothing(), nothing()],
    })
    const quality = rec.result.checks.quality
    assert.equal(quality.verdict, 'FAIL')
    assert.equal(quality.findings.length, 1)
    assert.equal(quality.findings[0].severity, 'blocker')
  })

  it('defaults missing critic findings to []', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [ok({ verdict: 'PASS' })] })
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [] })
  })

  it('passes the plan name to check and critic prompts', async () => {
    const rec = await review(makeReviewArgs(['quality']))
    for (const c of rec.calls) assert.match(c.prompt, /Plan name: sample/)
  })
})

describe('review.js Workflow script constraints', () => {
  it('declares meta as a pure literal', () => {
    const meta = extractMeta(readScript(REVIEW_SCRIPT))
    assert.equal(typeof meta.name, 'string')
    assert.equal(typeof meta.description, 'string')
  })

  it('uses no Date.now, Math.random, argless new Date, require, or imports', () => {
    assert.doesNotMatch(readScript(REVIEW_SCRIPT), FORBIDDEN_API)
  })
})
