// Specifies plugins/afk/workflows/review.js, the afk review Workflow
// script. args: {plan, planPath, pluginRoot, diffCommand, checks,
// checkModel, criticModel, criticFallbackModel}.

import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import {
  FORBIDDEN_API,
  PLAN_PATH,
  PLUGIN_ROOT,
  EXECUTE_SCRIPT,
  REVIEW_SCRIPT,
  WORKFLOWS_DIR,
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

// Without a scripted reply the critic keeps every finding it was given,
// numbered as the prompt numbered them.
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
    const rec = await review(makeReviewArgs(['quality']), { 'critic:quality': [ok({ findings: [{ n: 1, ...down }] })] }, {}, { quality: [MAJOR('quality')] })
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

// Load-bearing: the invented-finding, raised-severity, out-of-range, repeated
// and missing-n rows. They prove the script, not the critic's prompt, keeps a
// critic from adding or raising a finding that would start a fix cycle.
describe('review.js critic survivors', () => {
  const Q = ['quality']
  const found = { quality: [MAJOR('quality')] }
  const run = (findings, given = found) =>
    review(makeReviewArgs(Q), { 'critic:quality': [ok({ findings })] }, {}, given)

  it('numbers the findings the critic is shown from 1, in order', async () => {
    const two = { quality: [MAJOR('a'), { severity: 'blocker', taskId: 'W1.T3', text: 'second' }] }
    const rec = await review(makeReviewArgs(Q), {}, {}, two)
    const shown = JSON.parse(callsOf(rec, 'critic:quality')[0].prompt.match(/Check findings \(JSON\): (.*)/)[1])
    assert.deepEqual(shown.map((f) => f.n), [1, 2])
    assert.equal(shown[1].text, 'second')
  })

  it('critic: a finding with no n is dropped', async () => {
    const rec = await run([MAJOR('quality')])
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [] })
  })

  it('critic: an n out of range is dropped', async () => {
    const rec = await run([{ n: 2, ...MAJOR('quality') }, { n: 0, ...MAJOR('quality') }, { n: 1.5, ...MAJOR('quality') }])
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [] })
  })

  it('critic: a repeated n is kept once', async () => {
    const rec = await run([{ n: 1, ...MAJOR('quality') }, { n: 1, ...MAJOR('quality') }])
    assert.deepEqual(rec.result.checks.quality, { verdict: 'FAIL', findings: [MAJOR('quality')] })
  })

  it('critic: a blocker returned for a major finding comes back as major', async () => {
    const rec = await run([{ n: 1, ...MAJOR('quality'), severity: 'blocker' }])
    assert.deepEqual(rec.result.checks.quality, { verdict: 'FAIL', findings: [MAJOR('quality')] })
  })

  it('critic: a downgrade to minor keeps the finding and the check passes', async () => {
    const rec = await run([{ n: 1, ...MAJOR('quality'), severity: 'minor' }])
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [{ ...MAJOR('quality'), severity: 'minor' }] })
  })

  it('critic: an unrecognised severity keeps the original', async () => {
    const rec = await run([{ n: 1, ...MAJOR('quality'), severity: 'critical' }])
    assert.deepEqual(rec.result.checks.quality, { verdict: 'FAIL', findings: [MAJOR('quality')] })
  })

  it('critic: a changed taskId is kept', async () => {
    const rec = await run([{ n: 1, ...MAJOR('quality'), taskId: 'W2.T2' }])
    assert.deepEqual(rec.result.checks.quality.findings, [{ ...MAJOR('quality'), taskId: 'W2.T2' }])
  })

  it('critic: a changed text is replaced by the original', async () => {
    const rec = await run([{ n: 1, ...MAJOR('quality'), text: 'rewritten by the critic' }])
    assert.deepEqual(rec.result.checks.quality.findings, [MAJOR('quality')])
  })

  it('critic: only an invented finding leaves the minors and a PASS verdict', async () => {
    const rec = await run(
      [{ n: 7, severity: 'blocker', taskId: 'general', text: 'invented' }],
      { quality: [MAJOR('quality'), MINOR('quality')] }
    )
    assert.deepEqual(rec.result.checks.quality, { verdict: 'PASS', findings: [MINOR('quality')] })
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

  // A throw on a typed call is first retried without the agent type, on the
  // same model, so the fallback model is reached only when that throws too.
  it('retries a thrown critic once with criticFallbackModel', async () => {
    const rec = await review(
      makeReviewArgs(['quality']),
      { 'critic:quality': [boom('Unknown model alias: critic-alias'), boom('Unknown model alias: critic-alias')] },
      {},
      { quality: [MAJOR('quality')] }
    )
    assert.deepEqual(
      callsOf(rec, 'critic:quality').map((c) => [c.model, c.opts.agentType]),
      [
        ['critic-alias', 'afk:reviewer'],
        ['critic-alias', undefined],
        ['critic-fallback-alias', 'afk:reviewer'],
      ]
    )
    assert.equal(rec.result.checks.quality.verdict, 'FAIL')
  })

  it("reports a thrown check call as that check's FAIL with a blocker naming the error", async () => {
    const crash = () => boom('SENTINEL-ERROR check agent crashed')
    const rec = await review(makeReviewArgs(), { 'review:qa': [crash(), crash()] })
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

// Each check and critic names a plugin agent definition (plugins/afk/agents/),
// which limits the tools it starts with. A session that started before the
// plugin was updated does not know the types, and agent() then throws.
//
// Load-bearing: the refused-type run (without the retry every check of such a
// session would be reported as failed to run), the budget row (a budget stop
// must not start another agent), the labels row (token accounting detects an
// agent's role from its label), and the null row (a check that ran and
// returned nothing is not a refused type).
describe('review.js agent types', () => {
  const REFUSED = (type) =>
    `agent({agentType}): agent type '${type}' not found. Available agents: claude, Explore, general-purpose, Plan`

  // A session that knows no afk agent type: every typed call throws.
  const refusing = (inner) => (call) => (call.opts.agentType ? boom(REFUSED(call.opts.agentType)) : inner(call))

  it("each check's and critic's call carries its agent type, and labels and the other options are unchanged", async () => {
    const rec = await review(makeReviewArgs(), {}, {}, majorEverywhere)
    const types = Object.fromEntries(rec.calls.map((c) => [c.label, c.opts.agentType]))
    assert.deepEqual(types, {
      'review:compliance': 'afk:reviewer',
      'review:quality': 'afk:reviewer',
      'review:qa': 'afk:qa',
      'critic:compliance': 'afk:reviewer',
      'critic:quality': 'afk:reviewer',
      'critic:qa': 'afk:reviewer',
    })
    for (const c of rec.calls) {
      assert.deepEqual(Object.keys(c.opts).sort(), ['agentType', 'label', 'model', 'schema'], c.label)
    }
    assert.ok(!rec.logs.some((l) => /agent type/.test(l)), 'nothing is logged when the type is accepted')
  })

  it('a refused agent type is retried once without it, with one log line, and the run result is the same', async () => {
    const typed = await review(makeReviewArgs(), {}, {}, majorEverywhere)
    const rec = await runScript(REVIEW_SCRIPT, {
      args: makeReviewArgs(),
      respond: refusing(respond({}, {}, majorEverywhere)),
    })
    if (rec.error) throw rec.error
    assert.deepEqual(rec.result, typed.result)
    assert.equal(rec.calls.length, typed.calls.length * 2, 'every call is made once with the type and once without')
    for (const want of typed.calls) {
      const [first, second] = callsOf(rec, want.label)
      assert.equal(first.opts.agentType, want.opts.agentType)
      assert.ok(first.threw)
      assert.ok(!('agentType' in second.opts), 'the retry names no agent type')
      assert.deepEqual({ ...first.opts, agentType: undefined }, { ...second.opts, agentType: undefined }, 'the retry is the same call')
      assert.equal(second.prompt, want.prompt)
      assert.equal(second.model, want.model, 'the retry stays on the same model')
    }
    const lines = rec.logs.filter((l) => /agent type/.test(l))
    assert.equal(lines.length, typed.calls.length, 'one log line for each refused call')
    assert.ok(lines.some((l) => /^review:qa: agent type afk:qa failed \(.*not found.*\); retrying once without it/.test(l)))
  })

  it('an agent type check stopped by the budget ceiling is not retried without the type', async () => {
    const rec = await review(makeReviewArgs(['quality']), {
      'review:quality': [boom('Token budget exhausted: the turn budget ceiling was reached')],
    })
    assert.equal(rec.calls.length, 1)
    assert.equal(rec.result.checks.quality.verdict, 'FAIL')
    assert.match(rec.result.checks.quality.findings[0].text, /failed to run: Token budget exhausted/)
    assert.ok(!rec.logs.some((l) => /agent type/.test(l)))
  })

  it('an agent type critic stopped by the budget ceiling is not retried without the type', async () => {
    const args = makeReviewArgs(['quality'])
    delete args.criticFallbackModel
    const rec = await review(
      args,
      { 'critic:quality': [boom('Token budget exhausted: the turn budget ceiling was reached')] },
      {},
      { quality: [MAJOR('quality')] }
    )
    assert.equal(callsOf(rec, 'critic:quality').length, 1)
    assert.equal(rec.result.checks.quality.verdict, 'FAIL')
  })

  it('an agent type check that returns nothing is not retried without the type', async () => {
    const rec = await review(makeReviewArgs(['quality']), { 'review:quality': [nothing()] })
    assert.equal(rec.calls.length, 1)
    assert.match(rec.result.checks.quality.findings[0].text, /check agent returned no result/)
  })
})

// The definitions the two scripts name must exist, and their tool lists are
// pinned here because the agent CLI drops a tool name it does not know without
// a warning: a typo in a list silently takes a tool away from every agent of
// that type.
//
// Load-bearing: the qa rows. The QA check keeps every tool it is not denied,
// so its browser tools (built in or from a connector) stay; an allow list
// could not name them. It must never be denied ToolSearch: without it the CLI
// loads every on-demand tool in full, which measured 43,258 tokens on the
// first call against 15,813 for the default agent (measured for T1.5).
describe('plugin agent definitions', () => {
  const AGENTS_DIR = new URL('../agents/', WORKFLOWS_DIR)

  function definition(name) {
    const text = readFileSync(new URL(`${name}.md`, AGENTS_DIR), 'utf8')
    const m = /^---\n([\s\S]*?)\n---\n([\s\S]*)$/.exec(text)
    assert.ok(m, `${name}.md starts with a frontmatter block`)
    const keys = {}
    for (const line of m[1].split('\n')) {
      const kv = /^([A-Za-z]+): (.+)$/.exec(line)
      assert.ok(kv, `${name}.md frontmatter line "${line}" is "key: value"`)
      keys[kv[1]] = kv[2]
    }
    return { keys, body: m[2] }
  }

  const list = (value) => value.split(',').map((s) => s.trim())

  const rows = [
    { name: 'task', tools: ['Read', 'Edit', 'Write', 'Glob', 'Grep', 'Bash'] },
    { name: 'verifier', tools: ['Read', 'Glob', 'Grep', 'Bash'] },
    { name: 'reviewer', tools: ['Read', 'Glob', 'Grep', 'Bash'] },
    { name: 'research', tools: ['Read', 'Glob', 'Grep', 'Bash'] },
    { name: 'web-research', tools: ['WebSearch', 'WebFetch', 'Read'] },
    { name: 'qa', denied: ['Agent', 'NotebookEdit', 'Artifact', 'SendFeedback', 'ReportFindings', 'ListAgents'] },
  ]

  it('the agents folder holds exactly the six definitions', () => {
    assert.deepEqual(readdirSync(AGENTS_DIR).sort(), rows.map((r) => `${r.name}.md`).sort())
  })

  for (const row of rows) {
    it(`${row.name}: name, description, tool restriction and a body that keeps CLAUDE.md`, () => {
      const { keys, body } = definition(row.name)
      assert.equal(keys.name, row.name, 'the name is the file name; the type is afk:<name>')
      assert.ok(keys.description && !keys.description.includes('\n'))
      assert.match(body, /CLAUDE\.md/)
      assert.match(body, /prompt/)
      if (row.tools) {
        assert.deepEqual(Object.keys(keys).sort(), ['description', 'name', 'tools'])
        assert.deepEqual(list(keys.tools), row.tools)
        assert.ok(!list(keys.tools).includes('StructuredOutput'), 'a schema call adds StructuredOutput by itself')
      } else {
        assert.deepEqual(Object.keys(keys).sort(), ['description', 'disallowedTools', 'name'], 'a deny list only, no allow list')
        assert.deepEqual(list(keys.disallowedTools), row.denied)
        for (const kept of ['ToolSearch', 'Bash', 'Write', 'Read', 'Edit', 'Skill']) {
          assert.ok(!list(keys.disallowedTools).includes(kept), `the QA check keeps ${kept}`)
        }
        assert.ok(!list(keys.disallowedTools).some((t) => /browser|chrome|mcp__/i.test(t)), 'no browser or connector tool is denied')
      }
    })
  }

  it('every agent type the workflow scripts name has a definition', () => {
    const named = new Set()
    for (const script of [EXECUTE_SCRIPT, REVIEW_SCRIPT]) {
      for (const m of readScript(script).matchAll(/'afk:([a-z-]+)'/g)) named.add(m[1])
    }
    assert.deepEqual([...named].sort(), ['qa', 'reviewer', 'task', 'verifier'])
    for (const name of named) assert.ok(existsSync(new URL(`${name}.md`, AGENTS_DIR)), `agents/${name}.md exists`)
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
