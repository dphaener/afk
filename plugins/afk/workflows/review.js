export const meta = {
  name: 'afk-review',
  description: 'afk review: one check agent per requested check, and a critic only for a check with a blocker or major finding',
  phases: [{ title: 'Review', detail: 'Independent checks; a critic judges only blocker and major findings' }],
}

const PROMPT_FILES = {
  compliance: 'check-plan-compliance.md',
  quality: 'check-code-quality.md',
  qa: 'check-hands-on-qa.md',
}

const FINDING_SCHEMA = {
  type: 'object',
  properties: {
    severity: { type: 'string', enum: ['blocker', 'major', 'minor'] },
    taskId: { type: 'string' },
    text: { type: 'string' },
  },
  required: ['severity', 'taskId', 'text'],
}

// The critic returns only the surviving subset of the findings it was given,
// so it has no verdict: the script computes every verdict itself.
const CRITIC_FINDING_SCHEMA = {
  type: 'object',
  properties: {
    n: { type: 'integer' },
    severity: { type: 'string', enum: ['blocker', 'major', 'minor'] },
    taskId: { type: 'string' },
    text: { type: 'string' },
  },
  required: ['n', 'severity', 'taskId', 'text'],
}

const CRITIC_SCHEMA = {
  type: 'object',
  properties: {
    findings: { type: 'array', items: CRITIC_FINDING_SCHEMA },
  },
  required: ['findings'],
}

const RESULT_SCHEMA = {
  type: 'object',
  properties: {
    verdict: { type: 'string', enum: ['PASS', 'FAIL'] },
    findings: { type: 'array', items: FINDING_SCHEMA },
  },
  required: ['verdict', 'findings'],
}

const ISOLATION = 'You know nothing about how this work was executed and must not seek it.'

const OUTPUT_FORMAT =
  'Return the structured result: verdict PASS or FAIL, and findings, each with severity (blocker, major, or minor), taskId (the plan task ID, or "general"), and text. Use the severity definitions in your prompt file.'

const CRITIC_OUTPUT_FORMAT =
  'Return the structured result: findings only, no verdict. Return the findings that survive, each with the n it was given, in the shape you were given (n, severity, taskId, text). A downgrade changes severity; a rejected finding is left out; never add a finding, never raise a severity, never rewrite text.'

if (!args || typeof args.planPath !== 'string' || typeof args.diffCommand !== 'string' || typeof args.pluginRoot !== 'string') {
  throw new Error('review.js: args must include planPath, diffCommand, and pluginRoot')
}
if (!Array.isArray(args.checks)) {
  throw new Error('review.js: args.checks must be an array of check names')
}
for (const name of args.checks) {
  if (!PROMPT_FILES[name]) throw new Error('review.js: unknown check "' + name + '"')
}

const promptsDir = args.pluginRoot + '/skills/reviewer/prompts/'

function errorText(e) {
  return e && e.message ? e.message : String(e)
}

function planNameLine() {
  return '- Plan name: ' + (typeof args.plan === 'string' && args.plan ? args.plan : '(not given)')
}

function listOf(value) {
  return Array.isArray(value) ? value : []
}

function checkPrompt(name) {
  return [
    'Follow the instructions in ' + promptsDir + PROMPT_FILES[name] + '.',
    ISOLATION,
    '',
    'Inputs:',
    planNameLine(),
    '- Plan path: ' + args.planPath,
    '- Diff command: ' + args.diffCommand,
    '- Output format: ' + OUTPUT_FORMAT,
  ].join('\n')
}

function criticPrompt(name, findings) {
  return [
    'Follow the instructions in ' + promptsDir + 'adversarial-critic.md.',
    ISOLATION,
    '',
    'Inputs:',
    '- Check under review: ' + name,
    planNameLine(),
    '- Plan path: ' + args.planPath,
    '- Diff command: ' + args.diffCommand,
    '- Check findings (JSON): ' + JSON.stringify(findings.map((f, i) => ({ n: i + 1, severity: f.severity, taskId: f.taskId, text: f.text }))),
    '- Output format: ' + CRITIC_OUTPUT_FORMAT,
  ].join('\n')
}

function isGated(f) {
  return f.severity === 'blocker' || f.severity === 'major'
}

// A check fails if and only if a blocker or major finding remains. Agents'
// own verdicts are never passed through, so a stray PASS cannot hide a
// blocker and a stray FAIL cannot block on minors alone.
function verdictOf(findings) {
  return findings.some(isGated) ? 'FAIL' : 'PASS'
}

const SEVERITY_RANK = { blocker: 3, major: 2, minor: 1 }

// Keeps only what the critic was shown. The critic's prompt forbids adding a
// finding or raising a severity, but a prompt is not a guarantee, and a
// critic-invented blocker would start a fix cycle nobody checked. So a
// returned finding counts only when its number matches a gated finding not
// already kept, and it carries the original text, the original severity
// unless the critic lowered it, and the critic's task ID when it gave one.
function survivors(gated, returned) {
  const kept = []
  const seen = new Set()
  for (const r of listOf(returned)) {
    if (!r || !Number.isInteger(r.n) || r.n < 1 || r.n > gated.length || seen.has(r.n)) continue
    seen.add(r.n)
    const orig = gated[r.n - 1]
    const lowered = SEVERITY_RANK[r.severity] < SEVERITY_RANK[orig.severity] ? r.severity : orig.severity
    kept.push({
      severity: lowered,
      taskId: typeof r.taskId === 'string' && r.taskId ? r.taskId : orig.taskId,
      text: orig.text,
    })
  }
  return kept
}

function blocker(text) {
  return { severity: 'blocker', taskId: 'general', text: text }
}

async function runCritic(name, findings) {
  const prompt = criticPrompt(name, findings)
  const models = [args.criticModel]
  if (args.criticFallbackModel) models.push(args.criticFallbackModel)
  let lastError = null
  for (const model of models) {
    try {
      const out = await agent(prompt, { label: 'critic:' + name, model: model, schema: CRITIC_SCHEMA })
      if (out) return { out: out, error: null }
    } catch (e) {
      lastError = errorText(e)
    }
  }
  return { out: null, error: lastError || 'critic returned no result' }
}

phase('Review')
log('Reviewing ' + args.planPath + ': checks ' + args.checks.join(', '))

const entries = await pipeline(
  args.checks,
  async (name) => {
    try {
      const out = await agent(checkPrompt(name), { label: 'review:' + name, model: args.checkModel, schema: RESULT_SCHEMA })
      if (!out) return { name: name, error: 'check agent returned no result' }
      return { name: name, found: out }
    } catch (e) {
      return { name: name, error: errorText(e) }
    }
  },
  async (prev, name) => {
    if (prev.error) {
      return { name: name, verdict: 'FAIL', findings: [blocker('Check ' + name + ' failed to run: ' + prev.error)] }
    }
    const checkFindings = listOf(prev.found.findings)
    const gated = checkFindings.filter(isGated)
    if (gated.length === 0) {
      return { name: name, verdict: verdictOf(checkFindings), findings: checkFindings }
    }
    const minors = checkFindings.filter((f) => !isGated(f))
    const critic = await runCritic(name, gated)
    if (!critic.out) {
      const findings = checkFindings.concat([blocker('Critic for ' + name + ' failed to run: ' + critic.error)])
      return { name: name, verdict: verdictOf(findings), findings: findings }
    }
    const findings = survivors(gated, critic.out.findings).concat(minors)
    return { name: name, verdict: verdictOf(findings), findings: findings }
  }
)

const checks = {}
for (let i = 0; i < args.checks.length; i++) {
  const name = args.checks[i]
  const e = entries[i]
  checks[name] = e
    ? { verdict: e.verdict, findings: e.findings }
    : { verdict: 'FAIL', findings: [blocker('Check ' + name + ' crashed before producing a result')] }
}
return { checks: checks }
