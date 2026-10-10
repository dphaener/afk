export const meta = {
  name: 'afk-execute',
  description: 'afk DAG execution of a plan',
  phases: [{ title: 'Execute', detail: 'DAG-scheduled tasks with tiered verification' }],
}

// afk execute engine. `args` is the JSON printed by
// plugins/afk/scripts/plan_to_args.rb. The orchestrator never edits this
// script; it runs it with Workflow({scriptPath, args}).
//
// Scheduling: one promise per task ID. A task starts when every dependsOn task
// reaches a terminal outcome and, in dag mode, all of them passed. Tasks whose
// Files overlap run one at a time in plan order. Verification is tiered: the
// independent verifier runs only when the task or its self-report is risky.
//
// The plan file: a Workflow script cannot run a command, and no agent is
// spawned only to tick a checkbox. The agent whose result settles a task runs
// `afk plan check ... done` as its last command; failed and blocked tasks are
// returned in the result's `mark` list for the caller to mark. See settle().

const MAX_ATTEMPTS = 3
const ESCALATE = { light: 'balanced', balanced: 'heavy', heavy: 'reasoning', reasoning: 'reasoning' }
const ALWAYS_VERIFY_TIERS = ['heavy', 'reasoning']

const SELF_REPORT_SCHEMA = {
  type: 'object',
  properties: {
    status: { type: 'string', enum: ['done', 'blocked', 'needs_input'] },
    checks: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          command: { type: 'string' },
          exitCode: { type: 'integer' },
        },
        required: ['command', 'exitCode'],
      },
    },
    summary: { type: 'string' },
    question: { type: 'string' },
  },
  required: ['status', 'checks', 'summary'],
}

const VERDICT_SCHEMA = {
  type: 'object',
  properties: {
    verdict: { type: 'string', enum: ['PASS', 'FAIL'] },
    discrepancies: { type: 'array', items: { type: 'string' } },
  },
  required: ['verdict', 'discrepancies'],
}

if (!args || typeof args !== 'object') {
  throw new Error('afk-execute: args must be the plan_to_args.rb JSON object with a tasks array')
}
if (!Array.isArray(args.tasks)) {
  throw new Error('afk-execute: args.tasks must be an array (pass the parsed plan_to_args.rb JSON as an object, not a string)')
}

const TASKS = args.tasks
const PLAN = String(args.plan || 'plan')
const PLAN_PATH = String(args.planPath || '')
const PLUGIN_ROOT = String(args.pluginRoot || '')
const MODE = args.mode === 'waves' ? 'waves' : 'dag'
const AUTONOMOUS = args.autonomous === true
const TIERS = args.tiers && typeof args.tiers === 'object' ? args.tiers : {}
const NOTEPAD_DIR = `.afk/${PLAN}`
const EVIDENCE_DIR = `${NOTEPAD_DIR}/evidence`

// plan_to_args.rb emits an absolute planPath of the form
// <project>/.afk/plans/<plan>.md, and `afk plan check` wants <project>. A
// plan path of any other shape (relative, or outside .afk/plans/) gives ''
// and the command is printed without --project, to be run from the project
// root: a relative --project would resolve against whatever directory the
// agent happens to be in.
const PROJECT_ROOT = (() => {
  const m = /^(\/.*)?\/\.afk\/plans\/[^/]+$/.exec(PLAN_PATH)
  if (!m) return ''
  return m[1] || '/'
})()

// Shell-quotes a word only when it holds a character the shell would act on,
// so the common command stays readable in the prompt.
function shq(word) {
  const s = String(word)
  return /^[A-Za-z0-9_@%+=:,./-]+$/.test(s) ? s : `'${s.replace(/'/g, "'\\''")}'`
}

function markDoneCommand(t) {
  return `afk plan check ${PROJECT_ROOT ? `--project ${shq(PROJECT_ROOT)} ` : ''}${shq(PLAN)} ${shq(t.id)} done`
}

const MARK_WHERE = PROJECT_ROOT ? '' : ' (run it from the project root, the folder that holds .afk/)'

function tierEntry(tier) {
  const e = TIERS[tier] || {}
  return { model: e.model || undefined, fallbackModel: e.fallbackModel || null }
}

const VERIFIER_MODEL = args.verifierModel || tierEntry('balanced').model
const VERIFIER_FALLBACK = tierEntry('balanced').fallbackModel

const INDEX = {}
TASKS.forEach((t, i) => {
  if (!t || typeof t.id !== 'string' || !t.id) {
    throw new Error(`afk-execute: args.tasks[${i}] has no id`)
  }
  if (INDEX[t.id] !== undefined) throw new Error(`afk-execute: duplicate task id ${t.id} in args.tasks`)
  INDEX[t.id] = i
})
TASKS.forEach((t) => {
  const deps = Array.isArray(t.dependsOn) ? t.dependsOn : []
  for (const d of deps) {
    if (INDEX[d] === undefined) throw new Error(`afk-execute: task ${t.id} depends on unknown task ${d}`)
    if (INDEX[d] >= INDEX[t.id]) {
      throw new Error(`afk-execute: task ${t.id} depends on ${d}, which is not earlier in the plan`)
    }
  }
})

const WAVE_COUNT = Array.isArray(args.waves)
  ? args.waves.length
  : new Set(TASKS.map((t) => t.wave)).size

log(`Executing ${PLAN}: ${TASKS.length} tasks, ${WAVE_COUNT} waves, mode ${MODE}${AUTONOMOUS ? ', autonomous' : ''}`)
phase('Execute')

function oneLine(text, max) {
  const s = String(text == null ? '' : text).replace(/\s+/g, ' ').trim()
  return s.length > max ? `${s.slice(0, max - 3)}...` : s
}

function labelFor(t) {
  return `${t.id} ${t.title || ''}`.trim().slice(0, 60)
}

function location(t) {
  return `${PLAN_PATH} lines ${t.lineStart}-${t.lineEnd} (task ${t.id}: ${t.title || ''})`
}

// Normalized path: trimmed, backticks and leading ./ removed, no trailing
// slash. A glob entry is reduced to the directory before its first wildcard.
function normPath(p) {
  let s = String(p || '').replace(/`/g, '').trim().replace(/^\.\//, '').replace(/\/{2,}/g, '/')
  const star = s.search(/[*?[]/)
  if (star >= 0) s = s.slice(0, star).replace(/[^/]*$/, '')
  return s.replace(/\/+$/, '')
}

function pathsOverlap(a, b) {
  if (a === '' || b === '') return true
  return a === b || b.startsWith(`${a}/`) || a.startsWith(`${b}/`)
}

function filesOf(t) {
  return (Array.isArray(t.files) ? t.files : []).map(normPath).filter((p, i, all) => all.indexOf(p) === i)
}

function filesOverlap(a, b) {
  return a.some((x) => b.some((y) => pathsOverlap(x, y)))
}

function isRiskyFile(p) {
  return (
    /(^|\/)\.github(\/|$)/.test(p) ||
    /(^|\/)hooks(\/|$)/.test(p) ||
    /(^|\/)(test|tests|spec)(\/|$)/.test(p) ||
    /(_test|\.test|_spec|\.spec)\.[^/]+$/.test(p)
  )
}

// Only the Workflow budget-ceiling message counts as the backup stop signal; a
// message that merely mentions the word "budget" (an unknown alias, a task
// about budgets) is an ordinary agent error.
const BUDGET_CEILING = /\b(budget (ceiling|exhausted|exceeded|limit)|(exceeded|exhausted|reached|hit) (the |your )?(token |turn |cost )?budget)\b/i

function isBudgetError(e) {
  return Boolean(e && BUDGET_CEILING.test(String(e.message || e)))
}

const BUDGET_STOP = { budgetStop: true }

function budgetExhausted() {
  return Boolean(budget && budget.total && budget.remaining() <= 0)
}

let stopped = false
function stopForBudget(why) {
  if (!stopped) {
    stopped = true
    log(`Budget stop: ${why}. No new tasks start; running tasks finish.`)
  }
}

// A null return or a thrown error is retried once on the tier's fallback
// model; a second null or throw returns null (one failed attempt). Every call
// is refused once the budget is gone: nothing has to run after a task settles,
// because the agent that settled it has already marked the plan file.
async function callAgent(prompt, opts, fallbackModel) {
  if (budgetExhausted()) {
    stopForBudget(`${opts.label}: budget.remaining() reached 0 before start`)
    return BUDGET_STOP
  }
  try {
    const result = await agent(prompt, opts)
    if (result != null) return result
    log(`${opts.label}: agent returned nothing on ${opts.model || 'default model'}`)
  } catch (e) {
    if (isBudgetError(e)) {
      stopForBudget(`${opts.label}: ${oneLine(e.message, 160)}`)
      return BUDGET_STOP
    }
    log(`${opts.label}: agent error on ${opts.model || 'default model'}: ${oneLine(e && e.message, 160)}`)
  }
  if (!fallbackModel || fallbackModel === opts.model) return null
  if (budgetExhausted()) {
    stopForBudget(`${opts.label}: budget.remaining() reached 0 before the fallback retry`)
    return BUDGET_STOP
  }
  log(`${opts.label}: retrying once on fallback model ${fallbackModel}`)
  try {
    const result = await agent(prompt, Object.assign({}, opts, { model: fallbackModel }))
    return result == null ? null : result
  } catch (e) {
    if (isBudgetError(e)) {
      stopForBudget(`${opts.label}: ${oneLine(e.message, 160)}`)
      return BUDGET_STOP
    }
    log(`${opts.label}: fallback error on ${fallbackModel}: ${oneLine(e && e.message, 160)}`)
    return null
  }
}

const SELF_REPORT_RULES = [
  'Return the self-report:',
  '- status: "done" when the task is complete and its checks pass; "blocked" when you could not complete it (say why in summary)' +
    (AUTONOMOUS ? '.' : '; "needs_input" only under the input rule.'),
  '- checks: every lint, test, and acceptance command you ran, each with its real exit code. An empty list forces an independent verification.',
  '- summary: files changed and anything left undone.',
].join('\n')

const INPUT_RULE = AUTONOMOUS
  ? 'Input rule (autonomous mode): never return needs_input — decide, and record the decision and reason in decisions.md.'
  : 'Input rule: return status "needs_input" with a `question` only for a decision the plan, the code, and the notepads cannot settle. Do not use it for anything they settle.'

// Shared by the task and fix prompts. The instruction to mark the task done
// must never live here: a fix is always re-verified, so a fix agent that
// ticked the checkbox would mark a task the script is still working on.
function obligations(t) {
  const evidence = (Array.isArray(t.qa) ? t.qa : [])
    .map((sc) => oneLine(sc && sc.evidence, 200))
    .filter((p, i, all) => p && all.indexOf(p) === i)
  return [
    'Obligations:',
    `- Stay inside the task's Files list, the three notepads in ${NOTEPAD_DIR}/, and the task's evidence files under ${EVIDENCE_DIR}/. Never edit the plan file by hand; \`afk plan check\` is its only writer, and you run it only if this prompt tells you to.`,
    '- Honor the task\'s Must NOT do. No stubs, TODOs, placeholders, or new dependencies.',
    '- Do not commit, stash, push, or switch branches. Other agents edit other files in this working tree at the same time.',
    "- Run only the focused lint and tests for the files you touched and the task's Acceptance Criteria commands; never run the whole suite. Report each command with its exit code.",
    '- Never sleep or poll. No single command may run longer than about four minutes.',
    "- Carry out each of the task's QA Scenarios and save its evidence at the path the plan names" +
      (evidence.length ? `: ${evidence.map((p) => `\`${p}\``).join(', ')}.` : '.'),
    `- Before finishing, APPEND (never overwrite) findings to ${NOTEPAD_DIR}/learnings.md, decisions to decisions.md, and issues to issues.md, each under a heading "## (${t.id}) <label>". Never write a "## (ID) FAILED" heading in issues.md; \`afk plan check\` writes those.`,
  ].join('\n')
}

// Tasks that failed or were blocked in this run, in the order they settled.
// A failure is written to the plan file only after the run returns, so in
// waves mode, where a failed task does not stop the next wave, this list is
// the only way a later task can learn that something it builds on failed.
const failedInRun = []

function dependsNote() {
  if (MODE !== 'waves') return 'ignore Depends on for scheduling (every dependency has passed)'
  const failed = failedInRun.length
    ? `these tasks have failed so far in this run: ${failedInRun.map((f) => `${f.id} (${f.reason})`).join('; ')}`
    : 'no task has failed so far in this run'
  return `ignore Depends on for scheduling: the previous wave has finished, but a task in it may have failed. A failure in this run is written to the plan file only after the run ends, so the plan's checkboxes do not show it; ${failed}. Check that anything you build on exists and works before you use it`
}

// True when the verifier runs whatever the self-report says. The script knows
// this before the task agent starts, which is what lets the task prompt say
// who marks the task done.
function verifierCertain(t, files) {
  return ALWAYS_VERIFY_TIERS.includes(t.tier) || files.some(isRiskyFile)
}

// The condition here is needsVerify() restated for the agent: at least one
// check, every check exit 0, status done. They must stay the same rule, or an
// agent ticks a task that the script then sends to the verifier and may fail.
function taskMarkRule(t) {
  return `Marking the task done: if, and only if, you ran at least one check, every check you ran exited 0, and the status you are about to return is "done", run \`${markDoneCommand(t)}\` as your very last command${MARK_WHERE}. In every other case do not run it; an independent verifier then decides. It is not one of your checks: leave it out of \`checks\`, and if it fails say so in summary.`
}

function taskPrompt(t, files, attempt, lastReason) {
  const lines = [
    `afk plan "${PLAN}", task ${t.id}: ${t.title || ''}`,
    '',
    `Your binding contract is the task block at ${location(t)}. Read it, and read ${NOTEPAD_DIR}/learnings.md, decisions.md, issues.md first. If those lines no longer hold the (${t.id}) task line, find it in the plan; the plan may have shifted. Also read the plan's ## Context and ## Assumptions sections. Its What to do, Must NOT do, Files, and Acceptance Criteria are binding; ${dependsNote()}.`,
    '',
    `Read ${PLUGIN_ROOT}/skills/orchestrator/prompts/codename-announcement.md and apply its block for the ${t.tier || 'balanced'} tier.`,
  ]
  if (attempt > 1) {
    lines.push(
      '',
      `This is attempt ${attempt} of ${MAX_ATTEMPTS}. The previous attempt failed: ${lastReason}. Inspect the working tree for its partial work, then complete the task.`
    )
  }
  lines.push('', obligations(t))
  if (!verifierCertain(t, files)) lines.push('', taskMarkRule(t))
  lines.push('', INPUT_RULE, '', SELF_REPORT_RULES)
  return lines.join('\n')
}

function fixPrompt(t, discrepancies, attempt) {
  const list = discrepancies.length
    ? discrepancies.map((d) => `- ${d}`).join('\n')
    : '- The verifier returned FAIL without details. Re-check every Acceptance Criteria command and fix what fails.'
  return [
    `afk plan "${PLAN}", fix for task ${t.id}: ${t.title || ''} (attempt ${attempt} of ${MAX_ATTEMPTS})`,
    '',
    `The task block is at ${location(t)}. Read it, and read ${NOTEPAD_DIR}/learnings.md, decisions.md, issues.md first. If those lines no longer hold the (${t.id}) task line, find it in the plan.`,
    '',
    `Read ${PLUGIN_ROOT}/skills/orchestrator/prompts/codename-announcement.md and apply its block for the ${t.tier || 'balanced'} tier.`,
    '',
    'An independent verifier rejected the work. Its discrepancies, verbatim:',
    list,
    '',
    'Address every discrepancy exactly as written. Then re-run the checks.',
    '',
    obligations(t),
    '',
    INPUT_RULE,
    '',
    SELF_REPORT_RULES,
  ].join('\n')
}

function verifyPrompt(t, report) {
  const checks = (Array.isArray(report.checks) ? report.checks : [])
    .map((c) => `- \`${c && c.command}\` reported exit ${c && c.exitCode}`)
    .join('\n')
  // A PASS settles the task only when the self-report says done; on any other
  // status the task agent runs again, so the verifier must not tick the box.
  const mark =
    report.status === 'done'
      ? `Do not modify any file, with one exception: if, and only if, your verdict is PASS, run \`${markDoneCommand(t)}\` as your very last command${MARK_WHERE}. It ticks this task's checkbox in the plan file. Never run it on FAIL, and never edit the plan file by hand.`
      : `Do not modify any file, and do not run \`afk plan check\`: the implementing agent did not report the task done, so it runs again whatever your verdict.`
  return [
    `Independent verification of afk plan "${PLAN}", task ${t.id}: ${t.title || ''}`,
    '',
    `The task block is at ${location(t)}. Read it. ${mark}`,
    '',
    `The implementing agent's self-report (status: ${report.status}):`,
    `Summary: ${report.summary || '(none)'}`,
    'Checks:',
    checks || '- (none reported)',
    '',
    `Re-run every reported check command and every Acceptance Criteria command in the task block. Confirm each Files entry was changed as What to do says, nothing outside Files (other than the notepads and the task's evidence files under ${EVIDENCE_DIR}/) was changed for this task, Must NOT do holds, and no stubs, TODOs, or placeholders were left.`,
    '',
    'Return verdict PASS only when every command passes and the work matches the task block. Otherwise return FAIL with discrepancies: each a concrete, actionable statement naming the file, command, or criterion.',
  ].join('\n')
}

function needsVerify(t, files, report) {
  if (verifierCertain(t, files)) return true
  const checks = Array.isArray(report.checks) ? report.checks : []
  if (checks.length === 0) return true
  if (checks.some((c) => !c || c.exitCode !== 0)) return true
  return report.status !== 'done'
}

function attemptModels(t, attempt) {
  if (attempt < MAX_ATTEMPTS) {
    const base = tierEntry(t.tier || 'balanced')
    return {
      model: t.model || base.model,
      fallbackModel: t.fallbackModel !== undefined ? t.fallbackModel : base.fallbackModel,
    }
  }
  const up = ESCALATE[t.tier] || 'heavy'
  return tierEntry(up)
}

async function runTask(t, files) {
  let discrepancies = null
  let lastReason = 'no self-report'
  let fixes = 0
  let report = null
  let reverify = false
  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    const { model, fallbackModel } = attemptModels(t, attempt)
    if (attempt === MAX_ATTEMPTS && model !== attemptModels(t, 1).model) {
      log(`${t.id}: attempt ${attempt} escalates to ${model}`)
    }
    if (reverify) {
      // The work is on disk; only the verifier failed to answer.
      reverify = false
      log(`${t.id}: attempt ${attempt} re-verifies the previous self-report`)
    } else {
      const isFix = Boolean(discrepancies)
      if (isFix) {
        fixes += 1
        report = await callAgent(
          fixPrompt(t, discrepancies, attempt),
          { label: `fix:${t.id}#${fixes}`, phase: 'Execute', model, schema: SELF_REPORT_SCHEMA },
          fallbackModel
        )
      } else {
        report = await callAgent(
          taskPrompt(t, files, attempt, lastReason),
          { label: labelFor(t), phase: 'Execute', model, schema: SELF_REPORT_SCHEMA },
          fallbackModel
        )
      }
      if (report === BUDGET_STOP) return { kind: 'budget' }
      discrepancies = null

      if (!report || typeof report !== 'object' || !report.status) {
        report = null
        lastReason = 'agent returned no self-report'
        log(`${t.id}: attempt ${attempt} failed (${lastReason})`)
        continue
      }
      if (report.status === 'needs_input') {
        if (!AUTONOMOUS) {
          return { kind: 'needs_input', question: String(report.question || report.summary || 'no question given') }
        }
        lastReason = 'returned needs_input in autonomous mode'
        log(`${t.id}: attempt ${attempt} failed (${lastReason})`)
        continue
      }

      // A fix answers a verify FAIL, so it is always re-verified.
      if (!isFix && report.status === 'done' && !needsVerify(t, files, report)) return { kind: 'passed' }
    }

    const verdict = await callAgent(
      verifyPrompt(t, report),
      { label: `verify:${t.id}`, phase: 'Execute', model: VERIFIER_MODEL, schema: VERDICT_SCHEMA },
      VERIFIER_FALLBACK
    )
    if (verdict === BUDGET_STOP) return { kind: 'budget' }
    if (!verdict || typeof verdict !== 'object' || !verdict.verdict) {
      lastReason = 'verifier returned no verdict'
      reverify = true
      log(`${t.id}: attempt ${attempt} failed (${lastReason})`)
      continue
    }
    if (verdict.verdict === 'PASS') {
      if (report.status === 'done') return { kind: 'passed' }
      // A verifier PASS cannot overrule an agent that said it could not finish.
      lastReason = `agent reported ${report.status}: ${oneLine(report.summary, 160) || 'no summary'}`
      log(`${t.id}: attempt ${attempt} failed (${lastReason}; the verifier found no discrepancy, so the task agent runs again)`)
      continue
    }
    discrepancies = (Array.isArray(verdict.discrepancies) ? verdict.discrepancies : []).map((d) => String(d))
    lastReason = `verify FAIL: ${discrepancies.join('; ') || 'no details'}`
    log(`${t.id}: attempt ${attempt} failed verification (${discrepancies.length} discrepancies)`)
  }
  return { kind: 'failed', reason: oneLine(`${MAX_ATTEMPTS} attempts failed; last: ${lastReason}`, 200) }
}

const outcomes = {}
const terminal = {}

function resolveDeps(t) {
  const deps = Array.isArray(t.dependsOn) ? t.dependsOn : []
  return Promise.all(deps.map((d) => outcomes[d].then((o) => ({ id: d, o }))))
}

// Class (a) blocks (permanent, returned in `mark` as failed) beat class (b)
// stops (stay [ ]).
function gate(depResults) {
  if (MODE === 'dag') {
    const bad = depResults.find((r) => ['failed', 'blocked'].includes(r.o.kind))
    if (bad) return { kind: 'blocked', by: bad.id }
  }
  const ni = depResults.find((r) => r.o.kind === 'needs_input' || r.o.kind === 'held')
  if (ni) return { kind: 'held', reason: 'needs_input', by: ni.id }
  const bs = depResults.find((r) => r.o.kind === 'budget' || r.o.kind === 'budget_skip')
  if (bs) return { kind: 'budget_skip', by: bs.id }
  return null
}

async function schedule(t, i) {
  const files = filesOf(t)
  if (t.status === 'done') return { kind: 'passed', preexisting: true }
  if (t.status === 'failed') return { kind: 'failed', preexisting: true, reason: 'already marked FAILED in the plan' }

  const blocked = gate(await resolveDeps(t))
  if (blocked) return blocked

  // Per-file lock: wait for every earlier overlapping task to reach any
  // terminal state, so overlapping tasks run in plan order.
  const earlier = TASKS.slice(0, i).filter((o) => filesOverlap(files, filesOf(o)))
  await Promise.all(earlier.map((o) => terminal[o.id]))

  if (stopped) return { kind: 'budget_skip' }
  if (budgetExhausted()) {
    stopForBudget('budget.remaining() reached 0')
    return { kind: 'budget_skip' }
  }
  return runTask(t, files)
}

// Nothing here writes the plan file. A passed task was marked [x] by the agent
// whose result settled it (the task agent when no verifier was certain, else
// the verifier on PASS), as that agent's last command: a script cannot run a
// command, and marking finished work the moment it finishes means a crashed
// run never redoes it. A failed or blocked task is not marked during the run
// at all; it is returned in the result's `mark` list and the caller runs
// `afk plan check ... failed` for each entry. So all settle() does is record
// the failure for dependsNote(), before the outcome reaches any task waiting
// on this one.
function settle(t, o) {
  if (o.kind === 'failed' && !o.preexisting) failedInRun.push({ id: t.id, reason: o.reason })
  if (o.kind === 'blocked') failedInRun.push({ id: t.id, reason: `blocked by ${o.by}` })
  return o
}

TASKS.forEach((t, i) => {
  const p = Promise.resolve()
    .then(() => schedule(t, i))
    .catch((e) => {
      if (isBudgetError(e)) {
        stopForBudget(`${t.id}: ${oneLine(e.message, 160)}`)
        return { kind: 'budget' }
      }
      return { kind: 'failed', reason: oneLine(`engine error: ${e && e.message}`, 200) }
    })
    .then((o) => settle(t, o))
  outcomes[t.id] = p
  terminal[t.id] = p.then(() => undefined)
})

// `mark` lists, in plan order, every task no agent could have marked: the ones
// that failed or were blocked in this run. A task that was already [x] or [!]
// when the run started is left out, so a re-run never replaces the reason
// recorded for an earlier failure or adds a second issues.md entry for it.
const result = { passed: [], failed: [], skipped: [], needsInput: [], mark: [] }

for (const t of TASKS) {
  const o = await outcomes[t.id]
  if (o.kind === 'passed') {
    result.passed.push(t.id)
  } else if (o.kind === 'failed') {
    result.failed.push({ id: t.id, reason: o.reason })
    if (!o.preexisting) {
      result.mark.push({ id: t.id, state: 'failed', reason: o.reason })
      log(`${t.id} failed: ${o.reason}`)
    }
  } else if (o.kind === 'blocked') {
    const reason = `blocked by ${o.by}`
    result.skipped.push({ id: t.id, reason: 'blocked', by: o.by })
    result.mark.push({ id: t.id, state: 'failed', reason })
    log(`${t.id} skipped (blocked): ${reason}`)
  } else if (o.kind === 'needs_input') {
    result.needsInput.push({ id: t.id, question: o.question })
    log(`${t.id} needs input: ${oneLine(o.question, 200)}`)
  } else if (o.kind === 'held') {
    result.skipped.push({ id: t.id, reason: 'needs_input', by: o.by })
    log(`${t.id} skipped (needs_input): waits on ${o.by}; stays [ ] for a re-run`)
  } else if (o.kind === 'budget') {
    result.skipped.push({ id: t.id, reason: 'budget' })
    log(`${t.id} skipped (budget): stopped by the budget ceiling while running; stays [ ] for a re-run`)
  } else {
    result.skipped.push({ id: t.id, reason: 'budget' })
    log(`${t.id} skipped (budget): not started after the budget stop; stays [ ] for a re-run`)
  }
}

log(
  `Done: ${result.passed.length} passed, ${result.failed.length} failed, ${result.skipped.length} skipped, ${result.needsInput.length} need input`
)
return result
