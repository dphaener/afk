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
  qa: 'check-hands-on-qa.md',
}

function makeReviewArgs(checks = ['compliance', 'quality', 'qa']) {
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

const MAJOR = (name) => ({ severity: 'major', taskId: 'W1.T1', text: `SENTINEL-FINDING-${name}` })
const MINOR = (name) => ({ severity: 'minor', taskId: 'W1.T2', text: `minor-${name}` })

// Without a scripted reply the critic keeps every finding it was given.
const criticOutput = (findings) => ({ findings })

const nameOf = (label) => label.split(':')[1]
const kindOf = (label) => label.split(':')[0]

// replies[label] is a queue of replies; otherwise defaults per kind.
// findingsFor[name] sets the findings a check returns by default.
function respond(replies = {}, durations = {}, findingsFor = {}) {
  return (call) => {
    const q = replies[call.label]
    if (q && q.length) return q.shift()
    const name = nameOf(call.label)
    const ticks = durations[call.label] || 1
    if (kindOf(call.label) === 'review') {
      const findings = findingsFor[name] || []
      return ok({ verdict: findings.length ? 'FAIL' : 'PASS', findings }, ticks)
    }
    if (kindOf(call.label) === 'critic') {
      const given = JSON.parse(call.prompt.match(/Check findings \(JSON\): (.*)/)[1])
      return ok(criticOutput(given), ticks)
    }
    return ok('unexpected call')
  }
}

async function review(args, replies, durations, findingsFor) {
  const rec = await runScript(REVIEW_SCRIPT, { args, respond: respond(replies, durations, findingsFor) })
  if (rec.error) throw rec.error
  return rec
}

const callsOf = (rec, label) => rec.calls.filter((c) => c.label === label)

const ALL = ['compliance', 'quality', 'qa']
const majorEverywhere = { compliance: [MAJOR('compliance')], quality: [MAJOR('quality')], qa: [MAJOR('qa')] }

describe('review.js pipelines', () => {
  it('spawns the hands-on QA check with its prompt file', async () => {
    const rec = await review(makeReviewArgs(['qa']))
    const check = callsOf(rec, 'review:qa')
    assert.equal(check.length, 1)
    assert.ok(check[0].prompt.includes(`${PLUGIN_ROOT}/skills/reviewer/prompts/check-hands-on-qa.md`))
  })

  it('a full run spawns exactly three checks and no review:scope', async () => {
    const rec = await review(makeReviewArgs())
    assert.deepEqual(rec.calls.map((c) => c.label).sort(), ['review:compliance', 'review:qa', 'review:quality'])
    assert.equal(callsOf(rec, 'review:scope').length, 0)
    assert.deepEqual(Object.keys(rec.result.checks).sort(), ALL.slice().sort())
  })

  it('spawns a critic for a check with a major finding, after that check, and for no other check', async () => {
    const rec = await review(makeReviewArgs(), {}, {}, { quality: [MAJOR('quality')] })
    const check = callsOf(rec, 'review:quality')[0]
    const critic = callsOf(rec, 'critic:quality')
    assert.equal(critic.length, 1)
    assert.ok(critic[0].start >= check.end, 'the critic runs after its check')
    assert.equal(check.model, 'check-alias')
    assert.equal(critic[0].model, 'critic-alias')
    assert.equal(rec.calls.filter((c) => c.label.startsWith('critic:')).length, 1)
  })

  it('spawns no critic for a check that passes or has only minor findings', async () => {
    const rec = await review(makeReviewArgs(), {}, {}, { quality: [MINOR('quality')] })
    assert.equal(rec.calls.filter((c) => c.label.startsWith('critic:')).length, 0)
    assert.equal(rec.result.checks.compliance.verdict, 'PASS')
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [MINOR('quality')] })
  })

  it('has no barrier between checks: a fast check reaches its critic while a slow check still runs', async () => {
    const rec = await review(makeReviewArgs(['compliance', 'quality']), {}, { 'review:compliance': 50 }, majorEverywhere)
    const slow = callsOf(rec, 'review:compliance')[0]
    const fastCritic = callsOf(rec, 'critic:quality')[0]
    assert.ok(fastCritic.start < slow.end)
  })

  it('a run with checks: ["quality"] spawns only that check (re-review of the failed check)', async () => {
    const rec = await review(makeReviewArgs(['quality']), {}, {}, majorEverywhere)
    assert.deepEqual(rec.calls.map((c) => c.label).sort(), ['critic:quality', 'review:quality'])
    assert.deepEqual(Object.keys(rec.result.checks), ['quality'])
  })
})

describe('review.js schemas and result', () => {
  it('uses a structured schema for checks and a findings-only schema for critics', async () => {
    const rec = await review(makeReviewArgs(['qa']), {}, {}, { qa: [MAJOR('qa')] })
    const check = callsOf(rec, 'review:qa')[0]
    const critic = callsOf(rec, 'critic:qa')[0]
    assert.ok(check.schema && check.schema.type === 'object', 'check agent uses a schema')
    assert.ok(critic.schema && critic.schema.type === 'object', 'critic agent uses a schema')
    assert.deepEqual(Object.keys(critic.schema.properties), ['findings'], 'the critic returns no verdict')
    assert.deepEqual(critic.schema.required, ['findings'])
    const props = critic.schema.properties
    assert.equal(props.findings.type, 'array')
    const item = props.findings.items.properties
    assert.deepEqual([...item.severity.enum].sort(), ['blocker', 'major', 'minor'])
    assert.ok(item.taskId)
    assert.ok(item.text)
  })

  it('returns {checks: {name: {verdict, findings}}} with the script-computed verdict', async () => {
    const rec = await review(makeReviewArgs(), {}, {}, majorEverywhere)
    assert.deepEqual(Object.keys(rec.result), ['checks'])
    assert.deepEqual(Object.keys(rec.result.checks).sort(), ALL.slice().sort())
    for (const name of ALL) {
      assert.deepEqual(rec.result.checks[name], { verdict: 'FAIL', findings: [MAJOR(name)] })
    }
  })

  it('a critic that rejects a major finding removes it and the check becomes PASS', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [ok({ findings: [] })] }, {}, { quality: [MAJOR('quality')] })
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [] })
  })

  it('a critic that downgrades a major finding to minor makes the check PASS and keeps the finding', async () => {
    const down = { ...MAJOR('quality'), severity: 'minor' }
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [ok({ findings: [down] })] }, {}, { quality: [MAJOR('quality')] })
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [down] })
  })

  it('minor findings pass through the critic untouched and the critic is shown only blocker and major findings', async () => {
    const found = [MINOR('quality'), MAJOR('quality')]
    const rec = await review(makeReviewArgs(['quality']), {}, {}, { quality: found })
    const prompt = callsOf(rec, 'critic:quality')[0].prompt
    assert.ok(prompt.includes('SENTINEL-FINDING-quality'))
    assert.ok(!prompt.includes('minor-quality'), 'the critic never sees minor findings')
    assert.deepEqual(rec.result.checks.quality, { verdict: 'FAIL', findings: [MAJOR('quality'), MINOR('quality')] })
  })

  it('a check that returns verdict PASS with a blocker finding is reported FAIL', async () => {
    const blockerFinding = { severity: 'blocker', taskId: 'W1.T1', text: 'hard failure' }
    const rec = await review(makeReviewArgs(['quality']), {
      'review:quality': [ok({ verdict: 'PASS', findings: [blockerFinding] })],
    })
    assert.deepEqual(rec.result.checks.quality, { verdict: 'FAIL', findings: [blockerFinding] })
  })

  it('a check that returns verdict FAIL with only minor findings is reported PASS', async () => {
    const rec = await review(makeReviewArgs(['quality']), {
      'review:quality': [ok({ verdict: 'FAIL', findings: [MINOR('quality')] })],
    })
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [MINOR('quality')] })
    assert.equal(callsOf(rec, 'critic:quality').length, 0)
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

  it("critic prompts name the critic file, the plan path, the diff command, and the check's gated findings", async () => {
    const rec = await review(makeReviewArgs(), {}, {}, majorEverywhere)
    for (const name of Object.keys(PROMPT_FILES)) {
      const prompt = callsOf(rec, `critic:${name}`)[0].prompt
      assert.ok(prompt.includes(`${PLUGIN_ROOT}/skills/reviewer/prompts/adversarial-critic.md`))
      assert.ok(prompt.includes(PLAN_PATH))
      assert.ok(prompt.includes(DIFF))
      assert.ok(prompt.includes(`SENTINEL-FINDING-${name}`), "critic receives the check's findings")
      assert.match(prompt, /findings only, no verdict/)
    }
  })

  it('prompts never point at notepads, learnings, or execution history', async () => {
    const rec = await review(makeReviewArgs(), {}, {}, majorEverywhere)
    for (const call of rec.calls) {
      assert.doesNotMatch(call.prompt, /notepad|learnings|decisions\.md|execution history/i, call.label)
    }
  })
})

describe('review.js argument validation', () => {
  it('rejects scope as an unknown check', async () => {
    const rec = await runScript(REVIEW_SCRIPT, { args: makeReviewArgs(['scope']), respond: respond() })
    assert.match(String(rec.error && rec.error.message), /unknown check "scope"/)
    assert.equal(rec.calls.length, 0)
  })
})

describe('review.js errors and fallback', () => {
  it('retries a null critic once with criticFallbackModel', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [nothing()] }, {}, { quality: [MAJOR('quality')] })
    assert.deepEqual(callsOf(rec, 'critic:quality').map((c) => c.model), ['critic-alias', 'critic-fallback-alias'])
    assert.deepEqual(rec.result.checks.quality.findings, [MAJOR('quality')])
  })

  it('retries a thrown critic once with criticFallbackModel', async () => {
    const rec = await review(
      makeReviewArgs(['quality']),
      { 'critic:quality': [boom('Unknown model alias: critic-alias')] },
      {},
      { quality: [MAJOR('quality')] }
    )
    assert.deepEqual(callsOf(rec, 'critic:quality').map((c) => c.model), ['critic-alias', 'critic-fallback-alias'])
    assert.equal(rec.result.checks.quality.verdict, 'FAIL')
  })

  it("reports a thrown check call as that check's FAIL with a blocker naming the error", async () => {
    const rec = await review(makeReviewArgs(), { 'review:qa': [boom('SENTINEL-ERROR check agent crashed')] })
    const qa = rec.result.checks.qa
    assert.equal(qa.verdict, 'FAIL')
    assert.ok(
      qa.findings.some((f) => f.severity === 'blocker' && f.text.includes('SENTINEL-ERROR check agent crashed')),
      'a blocker finding names the error'
    )
    assert.equal(callsOf(rec, 'critic:qa').length, 0, 'no critic runs for a check that threw')
    assert.equal(rec.result.checks.compliance.verdict, 'PASS', 'other checks are unaffected')
    assert.equal(rec.result.checks.quality.verdict, 'PASS')
  })
})

describe('review.js tolerates missing findings and names the plan', () => {
  it('defaults missing findings to [] and reports PASS without a critic', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'review:quality': [ok({ verdict: 'FAIL' })] })
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [] })
    assert.equal(callsOf(rec, 'critic:quality').length, 0)
  })

  it('a failed critic keeps the check findings and adds a blocker', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [nothing(), nothing()] }, {}, { quality: [MAJOR('quality')] })
    const quality = rec.result.checks.quality
    assert.equal(quality.verdict, 'FAIL')
    assert.equal(quality.findings.length, 2)
    assert.equal(quality.findings[1].severity, 'blocker')
  })

  it('defaults missing critic findings to []', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [ok({})] }, {}, { quality: [MAJOR('quality')] })
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [] })
  })

  it('passes the plan name to check and critic prompts', async () => {
    const rec = await review(makeReviewArgs(['quality']), {}, {}, majorEverywhere)
    assert.ok(rec.calls.length === 2)
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
