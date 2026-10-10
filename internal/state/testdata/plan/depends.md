# Ship autonomous DAG scheduling

## Context

Tasks declare their prerequisites so a later wave can start early.

## Waves

### Wave 1: Foundations

- [x] (W1.T1) Add schema

  **What to do**: Add a table.

  **Must NOT do**: Do not touch users.

  **Files**: `db/001.sql`

  **Depends on**: none

  **Acceptance Criteria**: Migration runs.

  **Model**: light

- [ ] (W1.T2) Add model

  **What to do**: Create the model.

  **Files**: `src/model.ts`

  **Depends on**: N/A

  **Acceptance Criteria**: Model instantiates.

  **Model**: light

### Wave 2: Services

- [ ] (W2.T1) Add service

  **What to do**: Write the service.

  **Files**: `src/service.ts`

  **Depends on**: W1.T1,
  `W1.T2`

  **Acceptance Criteria**: Service works.

  **Model**: balanced

- [ ] (W2.T2) Add docs

  **What to do**: Write docs.

  **Files**: `docs/service.md`

  **Depends on**: W1.T1, none

  **Acceptance Criteria**: Docs build.

  **Model**: light

### Wave 3: Surface

- [ ] (W3.T1) Add screen

  **What to do**: Build the screen.

  **Files**: `src/screen.tsx`

  **Depends on**: W2.T1, W2.T2

  **Acceptance Criteria**: Screen renders.

  **Model**: heavy

## Success Criteria

- [ ] Late waves start before early ones finish

## Assumptions

- [A1] The engine schedules by dependency, not by wave.
- [A2] Plans without **Depends on** keep wave barriers.
