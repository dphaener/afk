# afk Plan Format

This document defines the exact markdown schema for `.afk/plans/{name}.md` files. The afk planner generates these files; the afk orchestrator parses and executes them.

Plans are **pure markdown**. No YAML frontmatter, no JSON, no custom syntax.

---

## Plan Name Rules

Plan names are lowercase, kebab-case strings derived from a ticket number or short description.

| Input | Plan name |
|---|---|
| `TURT-1234 Add billing webhook` | `turt-1234-add-billing-webhook` |
| `Refactor auth middleware` | `refactor-auth-middleware` |
| `PROJ-99` | `proj-99` |

Rules:
- Lowercase only
- Spaces become hyphens
- Remove special characters except hyphens
- Ticket numbers stay at the front when present

File path: `.afk/plans/{plan-name}.md`

---

## Top-Level Structure

Every plan must contain these sections in this order:

```
# {Plan Title}

## Context

## Objectives

## Capability Detection

## Verification Strategy

## Waves

## Success Criteria
```

Optional sections (appended after `## Success Criteria`):

```
## Learnings

## Decisions

## Assumptions
```

---

## Section Definitions

### `# {Plan Title}`

A short, human-readable title. Sentence case. No period.

```markdown
# Add billing webhook support
```

### `## Context`

One to three paragraphs explaining why this work is needed. What problem does it solve? What is the current state? What will change?

```markdown
## Context

The payments team needs webhook events from Stripe to update subscription
status in real time. Currently, status updates only happen on the next
login, causing a lag of up to 24 hours.
```

### `## Objectives`

A numbered list of concrete goals. Each objective should be independently verifiable.

```markdown
## Objectives

1. Receive and validate Stripe webhook events at `/webhooks/stripe`
2. Update subscription status on `customer.subscription.updated` events
3. Log all received events for debugging
```

### `## Capability Detection`

Documents what tools and integrations are available in this environment. The planner fills this in during the planning phase by probing the environment.

```markdown
## Capability Detection

- **Jira MCP**: available — ticket context can be fetched
- **Git**: available — branch `feat/billing-webhook` checked out
- **CLAUDE.md**: present — project conventions loaded
- **Test runner**: `npm test` (detected from package.json)
- **Lint**: `npm run lint` (detected from package.json)
```

Possible values for each capability: `available`, `not available`, `unknown`.

### `## Verification Strategy`

The commands used to verify the work is complete and correct. These run after all waves finish.

```markdown
## Verification Strategy

- **Lint**: `npm run lint`
- **Tests**: `npm test`
- **Type check**: `npm run typecheck`
- **Manual check**: Hit `POST /webhooks/stripe` with a test payload and confirm the subscription record updates
```

### `## Waves`

Contains all waves. Each wave is a `### Wave N: {name}` heading followed by task checkboxes.

See [Wave and Task Format](#wave-and-task-format) below.

### `## Success Criteria`

A checklist of observable outcomes that confirm the plan is complete. These are higher-level than individual task acceptance criteria.

```markdown
## Success Criteria

- [ ] Stripe webhook endpoint accepts and validates signed payloads
- [ ] Subscription status updates within 5 seconds of a webhook event
- [ ] All events are logged with timestamp and event type
- [ ] Existing tests pass
- [ ] No new lint errors
```

### `## Assumptions` (optional)

One bullet per decision the planner made without asking the user. Appended after `## Success Criteria`, alongside `## Learnings` and `## Decisions`.

Format: `- [A1] {decision} → {chosen answer} — {reason} ({tag})`

`{tag}` is copied from the draft's `## Resolved Decisions` line: `assumed` for a decision the planner made, `assumed, would have escalated` for a node that met the escalation rule but was decided in autonomous mode. Every entry ends with one of the two tags. Decisions the user answered are not assumptions and are not listed.

```markdown
## Assumptions

- [A1] Store preferences in a new table → `notification_preferences`, not a JSON column on `users` — keeps per-type toggles queryable and leaves `users` untouched (assumed)
- [A2] Default SMS to off → `sms_enabled` defaults to false — SMS costs money and needs explicit opt-in (assumed, would have escalated)
```

IDs are sequential (`A1`, `A2`, ...) and unique within the plan.

### `## Learnings` (optional)

Stored in `.afk/{plan-name}/learnings.md` during execution. Records patterns, conventions, and discoveries made while working through the plan.

### `## Decisions` (optional)

Stored in `.afk/{plan-name}/decisions.md` during execution. Records architectural choices and the reasoning behind them.

---

## Model Tier Definitions

Each task specifies a **Model** tier that tells the orchestrator which model to use for execution. See [afk Model Registry](../../models.md) for tier-to-model mappings.

| Tier | Use When |
|------|----------|
| `light` | **Pattern-following** — no judgment calls. Config updates, boilerplate, documentation, renaming, simple tests, mechanical multi-file changes |
| `balanced` | **Judgment required** — understanding context and making reasonable decisions. Standard features, API endpoints, moderate logic. Most tasks default here |
| `heavy` | **Demanding execution** — high-stakes or complex implementation where the path is discoverable. Refactoring entangled code, security-sensitive logic, performance optimization. Hard execution, not hard thinking |
| `reasoning` | **Deep synthesis** — requiring step-by-step reasoning and novel integration across systems. High-complexity problem-solving, major design decisions, or cross-layer architecture work |

---

## Wave and Task Format

### Wave heading

```markdown
### Wave N: {name}
```

- `N` is a positive integer starting at 1
- `{name}` is a short description of what this wave accomplishes
- Waves are readable groups. Wave order alone is always a valid execution order; `**Depends on**` (below) lets the engine start a task earlier
- Tasks within a wave can run in parallel

**Constraints:**
- Every wave must contain at least one task. Empty waves are not allowed.
- Single-task waves are allowed (e.g., a critical dependency that must complete before anything else).

### Task format

```markdown
- [ ] (WN.TM) Task Title

  **What to do**: [Detailed instructions for completing this task]

  **Must NOT do**: [Explicit scope boundaries — what to avoid or leave alone]

  **Files**: [Comma-separated list of files to create or modify]

  **Depends on**: [Comma-separated task IDs from earlier waves, or `none`]

  **Acceptance Criteria**: [How to verify this task is done correctly]

  **Model**: light | balanced | heavy | reasoning
```

- `WN` is the wave number (e.g., `W1`, `W2`)
- `TM` is the task number within the wave (e.g., `T1`, `T2`)
- Task IDs are stable and deterministic — the orchestrator uses them to track state
- The task title is a short imperative phrase (no period)

### Task ID format

```
(WN.TM)
```

Examples: `(W1.T1)`, `(W1.T2)`, `(W2.T1)`, `(W3.T4)`

Task IDs must be unique within a plan. They do not need to be globally unique across plans.

### Checkbox states

| Markdown | Meaning |
|---|---|
| `- [ ] (WN.TM)` | Task is incomplete |
| `- [x] (WN.TM)` | Task is complete |
| `- [!] (WN.TM) FAILED: {reason}` | Task failed after retries — see `issues.md` |

The orchestrator marks tasks complete by replacing `- [ ]` with `- [x]`. Tasks that fail after 3 retry attempts are marked `- [!]` with a reason, and the failure is logged in `.afk/{plan-name}/issues.md`. Nothing else in the task block changes.

### Task subsections

| Subsection | Required | Purpose |
|---|---|---|
| `**What to do**` | Yes | Step-by-step instructions |
| `**Must NOT do**` | Yes | Scope boundaries, things to avoid |
| `**Files**` | Yes | Files to create or modify |
| `**Depends on**` | Yes (new plans) | Task IDs this task needs, or `none` |
| `**Acceptance Criteria**` | Yes | How to verify the task is done |
| `**Model**` | Yes | Which model tier (`light`, `balanced`, `heavy`, `reasoning`) the orchestrator should use for this task |

All six subsections are required. If a subsection has nothing meaningful to say, write `N/A` rather than omitting it. `**Depends on**` takes `none` instead of `N/A`.

### Depends on

The value is a comma-separated list of task IDs (`W1.T1, W2.T3`) or the literal `none`. It sits after `**Files**` and before `**Acceptance Criteria**`.

Rules:
- A task may depend only on tasks in EARLIER waves. Never depend on a task in the same wave, so wave order alone is always a valid execution order.
- No cycles.
- A task may depend on any earlier wave, not only the previous one.
- Waves stay as readable groups of related work.

Presence:
- The field is REQUIRED for newly generated plans.
- A plan in which NO task has the field is a legacy plan. It executes with wave barriers: every task waits for the whole previous wave.
- A plan in which SOME tasks have the field and others do not is invalid.

### Subsection value extent

A subsection value runs from its `**Label**:` line until the next `**Label**:` line, blank line, task line, or heading. Wrapped lines are joined with a space.

---

## Edge Cases

**Empty waves**: Not allowed. Every wave must have at least one task. If a wave would be empty, either remove it or merge its intent into an adjacent wave.

**Single-task waves**: Allowed. Use them when a task is a hard dependency for everything in the next wave.

**Tasks with no dependencies**: Can be placed in any wave. If a task has no dependencies on other tasks, it can go in Wave 1 alongside other independent work.

**Plan name sanitization**: Strip special characters, lowercase everything, replace spaces with hyphens. Ticket numbers stay at the front.

**Long task lists**: No hard limit on tasks per wave, but keep waves focused. A wave with more than 8 tasks is a signal to split it.

---

## Complete Example

```markdown
# Add user notification preferences

## Context

Users currently receive all notification types with no way to opt out.
Support tickets show this is a top complaint. We need a preferences
screen and the backend logic to respect those preferences when sending.

## Objectives

1. Add a notification preferences model with per-type toggles
2. Expose a preferences API endpoint (GET and PATCH)
3. Build a preferences UI screen
4. Gate all outgoing notifications against user preferences

## Capability Detection

- **Jira MCP**: available — TURT-5678 context loaded
- **Git**: available — branch `feat/notification-prefs` checked out
- **CLAUDE.md**: present
- **Test runner**: `npm test`
- **Lint**: `npm run lint`

## Verification Strategy

- **Lint**: `npm run lint`
- **Tests**: `npm test`
- **Manual check**: Toggle email notifications off, trigger an email event,
  confirm no email is sent

## Waves

### Wave 1: Data layer

- [ ] (W1.T1) Create notification preferences schema

  **What to do**: Add a `notification_preferences` table with columns for
  `user_id` (foreign key), `email_enabled` (boolean, default true),
  `push_enabled` (boolean, default true), `sms_enabled` (boolean,
  default false). Write and run the migration.

  **Must NOT do**: Do not modify the existing `users` table. Do not add
  any application logic in this task.

  **Files**: `db/migrations/YYYYMMDD_create_notification_preferences.sql`

  **Depends on**: none

  **Acceptance Criteria**: Migration runs without errors. Table exists
  with correct columns and defaults.

  **Model**: light

- [ ] (W1.T2) Add NotificationPreferences model

  **What to do**: Create the model file with validation that `user_id`
  is present and unique. Add a `belongs_to :user` association.

  **Must NOT do**: Do not add any controller or route logic here.

  **Files**: `src/models/notificationPreferences.ts`

  **Depends on**: W1.T1

  **Acceptance Criteria**: Model can be instantiated and saved. Duplicate
  `user_id` raises a validation error.

  **Model**: light

### Wave 2: API layer

- [ ] (W2.T1) Add preferences API endpoints

  **What to do**: Add `GET /api/users/:id/notification-preferences` and
  `PATCH /api/users/:id/notification-preferences` endpoints. GET returns
  current preferences (creating defaults if none exist). PATCH updates
  only the fields provided.

  **Must NOT do**: Do not add authentication logic — assume auth
  middleware is already applied at the router level. Do not change
  existing user endpoints.

  **Files**: `src/routes/notificationPreferences.ts`,
  `src/controllers/notificationPreferencesController.ts`

  **Depends on**: W1.T2

  **Acceptance Criteria**: GET returns 200 with preferences object. PATCH
  with `{ "email_enabled": false }` returns 200 and persists the change.
  PATCH with unknown fields returns 400.

  **Model**: balanced

- [ ] (W2.T2) Write API tests

  **What to do**: Write integration tests covering the GET and PATCH
  endpoints. Test the default-creation behavior on first GET. Test
  partial updates.

  **Must NOT do**: Do not test the model directly here — that belongs in
  unit tests. Do not add tests for unrelated endpoints.

  **Files**: `src/routes/__tests__/notificationPreferences.test.ts`

  **Depends on**: W1.T2

  **Acceptance Criteria**: All new tests pass. Coverage includes happy
  path, partial update, and unknown field rejection.

  **Model**: balanced

### Wave 3: UI and notification gating

- [ ] (W3.T1) Build preferences screen

  **What to do**: Add a preferences screen at `/settings/notifications`
  with toggles for each notification type. Load current preferences on
  mount. Save on toggle change with optimistic UI update and error
  rollback.

  **Must NOT do**: Do not modify the existing settings layout component.
  Do not add new routes to the router — the route already exists as a
  placeholder.

  **Files**: `src/screens/NotificationPreferencesScreen.tsx`,
  `src/hooks/useNotificationPreferences.ts`

  **Depends on**: W2.T1

  **Acceptance Criteria**: Screen renders current preferences. Toggling
  a switch calls PATCH and updates the UI. On API error, the toggle
  reverts and an error toast appears.

  **Model**: balanced

- [ ] (W3.T2) Gate outgoing notifications against preferences

  **What to do**: In the notification dispatch service, look up the
  recipient's preferences before sending. Skip the send if the relevant
  preference is disabled. Log a debug message when a notification is
  skipped.

  **Must NOT do**: Do not change the notification payload format. Do not
  add preferences checks in the email/push/SMS provider adapters
  themselves — keep the check in the dispatch layer.

  **Files**: `src/services/notificationDispatcher.ts`

  **Depends on**: W1.T2

  **Acceptance Criteria**: A user with `email_enabled: false` does not
  receive email notifications. The skip is logged. All existing
  notification tests still pass.

  **Model**: reasoning

## Success Criteria

- [ ] Preferences persist across sessions
- [ ] Disabling a notification type stops that type from being sent
- [ ] Preferences screen is reachable from the settings menu
- [ ] All tests pass
- [ ] No new lint errors

## Assumptions

- [A1] Store preferences in a new table → `notification_preferences`, not a JSON column on `users` — keeps per-type toggles queryable and leaves `users` untouched (assumed)
- [A2] Default SMS to off → `sms_enabled` defaults to false — SMS costs money and needs explicit opt-in (assumed, would have escalated)
```
