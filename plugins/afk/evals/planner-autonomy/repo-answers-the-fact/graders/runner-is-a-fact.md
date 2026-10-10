---
type: llm
focus:
  source: file
  path: .afk/plans/tally-count-tests.md
---

The repo answers the test-runner question: package.json has `"test": "vitest run"` and a
vitest.config.js.

PASS only if the plan says tests run with vitest (npm test / vitest run) AND does not list the
test runner, its configuration, or the test file location as an entry under `## Assumptions`.
FAIL if the plan names a different runner, leaves the runner open, or records the runner choice
as an assumption.
