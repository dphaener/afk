# Add billing webhook support

## Context

The payments team needs webhook events from Stripe to update subscription
status in real time.

## Objectives

1. Receive and validate Stripe webhook events at `/webhooks/stripe`
2. Update subscription status on `customer.subscription.updated` events

## Capability Detection

- **Jira MCP**: available
- **Git**: available

## Verification Strategy

- **Lint**: `npm run lint`
- **Tests**: `npm test`

## Waves

### Wave 1: Data layer

- [x] (W1.T1) Create notification preferences schema

  **What to do**: Add a table.

  **Must NOT do**: Do not modify users table.

  **Files**: `db/migrations/001.sql`

  **Acceptance Criteria**: Migration runs.

  **Model**: light

- [!] (W1.T2) Add NotificationPreferences model FAILED: could not resolve merge conflict after 3 retries

  **What to do**: Create the model file.

  **Must NOT do**: Do not add controller logic.

  **Files**: `src/models/notificationPreferences.ts`

  **Acceptance Criteria**: Model can be instantiated.

  **Model**: light

### Wave 2: API layer

- [ ] (W2.T1) Add preferences API endpoints

  **What to do**: Add GET/PATCH endpoints.

  **Must NOT do**: Do not add auth logic here.

  **Files**: `src/routes/notificationPreferences.ts`

  **Acceptance Criteria**: GET/PATCH work.

  **Model**: balanced

- [ ] (W2.T2) Write API tests

  **What to do**: Write integration tests.

  **Must NOT do**: Do not test the model directly.

  **Files**: `src/routes/__tests__/notificationPreferences.test.ts`

  **Acceptance Criteria**: All new tests pass.

  **Model**: balanced

### Wave 3: UI and notification gating

- [ ] (W3.T1) Build preferences screen

  **What to do**: Add a preferences screen.

  **Must NOT do**: Do not modify the settings layout component.

  **Files**: `src/screens/NotificationPreferencesScreen.tsx`

  **Acceptance Criteria**: Screen renders.

  **Model**: reasoning

## Success Criteria

- [ ] Preferences persist across sessions
- [ ] Disabling a notification type stops that type from being sent
- [ ] All tests pass
