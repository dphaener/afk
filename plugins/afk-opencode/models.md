# afk Model Registry

This registry is the ONLY place in the afk plugin where concrete model IDs appear. Skills reference tiers; this file resolves them. To adopt a new model, edit this file only. IDs are OpenCode `providerID/modelID` references; pass them verbatim as the `subagent` tool's `model` and in `afk_workflow` args.

---

## Task Tiers

| Tier | Model | Fallback | Use when |
|------|-------|----------|----------|
| `light` | `anthropic/claude-haiku-5-5` | — | Pattern-following, zero judgment calls |
| `balanced` | `anthropic/claude-sonnet-5-5` | — | Judgment required, mostly clear path (default) |
| `heavy` | `anthropic/claude-opus-5-5` | `anthropic/claude-sonnet-5-5` | Demanding, high-stakes, complex implementation |
| `reasoning` | `anthropic/claude-fable-5-1` | `anthropic/claude-opus-5-5` | Novel reasoning, deep synthesis, architectural trade-offs |

---

## Internal Roles

| Role | Tier |
|------|------|
| Codebase / web research | `balanced` |
| Crucible plan review | `reasoning` |
| Reviewer checks (compliance, quality, scope) | `balanced` |
| Adversarial critics | `reasoning` |

---

## Escalation Chain

`light → balanced → heavy → reasoning`

`reasoning` is the ceiling. A `reasoning` task that exhausts its retries is marked failed — never escalated further.

---

## Fallback Rule

Fallback is the CALLER's responsibility: if a `subagent` call fails because the model is unknown or unavailable, the agent that made the call retries the same call once with the tier's Fallback model ID. (A note inside the sub-agent's prompt cannot handle this — the failure happens before the sub-agent runs.) If the registry itself is not in context, omit the `model` parameter entirely (inherit the session model) — never guess a model ID.
