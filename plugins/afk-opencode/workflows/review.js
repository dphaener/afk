export const meta = {
  name: 'afk-review',
  description: 'afk review: one check agent and one adversarial critic per requested check',
  phases: [{ title: 'Review', detail: 'Independent checks, each reconciled by a critic' }],
}

const PROMPT_FILES = {
  compliance: 'check-plan-compliance.md',
  quality: 'check-code-quality.md',
  scope: 'check-scope-fidelity.md',
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
    '- Check findings (JSON): ' + JSON.stringify(findings),
    '- Output format: ' + OUTPUT_FORMAT,
  ].join('\n')
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
      const out = await agent(prompt, { label: 'critic:' + name, model: model, schema: RESULT_SCHEMA })
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
    const critic = await runCritic(name, checkFindings)
    if (!critic.out) {
      return {
        name: name,
        verdict: 'FAIL',
        findings: checkFindings.concat([blocker('Critic for ' + name + ' failed to run: ' + critic.error)]),
      }
    }
    return { name: name, verdict: critic.out.verdict, findings: listOf(critic.out.findings) }
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
