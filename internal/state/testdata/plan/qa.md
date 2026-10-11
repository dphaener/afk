# Ship QA scenarios

## Context

Tasks name exact files and carry QA scenarios.

## Waves

### Wave 1: Foundations

- [ ] (W1.T1) Two scenarios and line ranges

  **What to do**: Do the thing.

  **Must NOT do**: Nothing risky.

  **Files**: `internal/state/plan.go:107-125`, `internal/state/plan_test.go`,
  `internal/state/testdata/`

  **Depends on**: none

  **Acceptance Criteria**: Tests pass.

  **QA Scenarios**:
  - Tool: Bash
    Case: happy
    Steps: run `go test ./internal/state/ -count=1`
    Expected: exit 0 and an `ok` line
    Evidence: `.afk/qa/evidence/w1-t1-tests.txt`
  - Tool: Bash
    Case: failure
    Steps: run the binary and note: Expected output is a table,
      then check the exit code
    Expected: exit 0
    Evidence: `.afk/qa/evidence/w1-t1-run.txt`

  **Model**: balanced

- [ ] (W1.T2) Scenario missing a field

  **What to do**: Do another thing.

  **Files**: `a.go`

  **Depends on**: none

  **Acceptance Criteria**: Works.

  **QA Scenarios**:
  - Tool: Read
    Steps: open the file

  **Model**: light

- [ ] (W1.T3) No QA subsection and N/A files

  **What to do**: Old style.

  **Files**: N/A

  **Depends on**: none

  **Acceptance Criteria**: Works.

  **Model**: light
