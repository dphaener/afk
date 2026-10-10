# afk Model Registry

This registry is the ONLY place in the afk plugin where concrete model aliases appear. Skills reference tiers; this file resolves them. To adopt a new model, edit this file only.

---

## Task Tiers

| Tier | Model | Fallback | Use when |
|------|-------|----------|----------|
| `light` | `haiku` | — | Pattern-following, zero judgment calls |
| `balanced` | `sonnet` | — | Judgment required, mostly clear path (default) |
| `heavy` | `opus` | `sonnet` | Demanding, high-stakes, complex implementation |
| `reasoning` | `fable` | `opus` | Novel reasoning, deep synthesis, architectural trade-offs |

---

## Internal Roles

| Role | Tier |
|------|------|
| Codebase / web research | `balanced` |
| Crucible plan review | `reasoning` |
| Reviewer checks (compliance, quality, hands-on QA) | `balanced` |
| Adversarial critics | `reasoning` |

---

## Escalation Chain

`light → balanced → heavy → reasoning`

`reasoning` is the ceiling. A `reasoning` task that exhausts its retries is marked failed — never escalated further.

---

## Fallback Rule

Fallback is the CALLER's responsibility: if an Agent call is rejected because the model alias is unknown or unavailable, the agent that made the call retries the same call once with the tier's Fallback alias. (A note inside the sub-agent's prompt cannot handle this — rejection happens before the sub-agent runs.) If the registry itself is not in context, omit the `model` parameter entirely (inherit the session model) — never guess an alias.
