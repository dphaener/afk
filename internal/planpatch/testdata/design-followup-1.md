# Design follow-up 1 — the shared recipe

You made the design system in `docs/design-system.md` for the recipe box. Most of it fits the plan and has been taken in: the colours, the type and the `RecipeCard` now appear in the plan. One screen does not fit, and it needs a change in the design, not in the plan.

## What the design shows

The "Shared recipe" screen shows a reader signing in with an email address and seeing the recipe only after that.

## What the plan requires

Task T2.2 says: "The link opens the recipe in a private window, and a wrong token shows a not-found page." Someone who gets the link has no account and must see the recipe straight away. The plan wins on behaviour, so the sign-in cannot stay.

## What to hand back

Redraw the "Shared recipe" screen so the link opens the recipe at once, with no sign-in form before it. Keep the Recipe screen's look, leave out the Print and Share buttons, and add one screen for a wrong token: a plain not-found page with a link back to the home page. Then save the updated design system over `docs/design-system.md`, commit it, and run `afk plan reconcile docs/design-system.md` again.
