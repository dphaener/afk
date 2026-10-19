# afk web page — design follow-up 2

This is a follow-up to the afk web page design system (`docs/design-system.md`). The master plan was
reconciled with the design system's new export on 2026-10-19. The rule between them: **the plan wins on
behaviour, the design system wins on looks.** Each point below is one where the new export leaves out
behaviour the plan requires and the page already has, so the design has to change. Keep every token,
component and class name as it is, and keep the new desktop back bar (`app-bar--desktop`) as it is; change
only what each point says.

For each point: what the design shows, what the plan requires, and what to hand back.

## 1. Sign-in prompt: the password field

- **The design shows:** the sign-in prompt as the `wordmark`, `h1` "This page is private", one line, a
  **Sign in** button and the meta line. There is no field to type anything into.
- **The plan requires:** the owner signs in with a password they set with `afk web password`. The prompt is
  a form with one labelled **Password** field and **Sign in**, and it works with JavaScript off. A wrong
  password is refused on the same page with a line saying so; after five wrong tries in a row it refuses for
  a minute and says that too.
- **Hand back:** the sign-in prompt with a labelled **Password** `field` (`type="password"`,
  `autocomplete="current-password"`) above **Sign in** (primary), and its error state: `aria-invalid` on the
  field and a `field__error` line under it, for both "wrong password" and "too many tries, wait a minute".

## 2. The bars: Sign out

- **The design shows:** the phone `app-bar` and the desktop `topnav` with no way to sign out.
- **The plan requires:** every signed-in page has a sign-out button that signs this browser out and lands
  on the sign-in prompt. It is a plain form that POSTs to `/signout`, so it works with JavaScript off.
- **Hand back:** a quiet **Sign out** button at the end of the phone `app-bar` and at the end of the desktop
  `topnav`, in its normal, hover and focus states, from the components the design system already has.
