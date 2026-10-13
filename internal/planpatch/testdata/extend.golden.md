# Recipe box — master plan

**How to use this document.** Read Context and Decisions first, then the milestone you are working in.
Each task says what it waits for, what to build and how to tell it is done.

## Context

The owner cooks from a folder of loose pages and wants them in one place.
The owner asked: "let me type a recipe in once, find it by what is in the fridge, and send it to my sister".

## Decisions

| Topic | Decision |
|---|---|
| Language | Go, standard library where it is enough |
| Storage | One SQLite file beside the binary |
| Pages | Plain HTML rendered on the server, no script |

## Milestones

### M1 — Keep recipes

**Outcome:** the owner can type a recipe in and find it again.

#### T1.1 — Store a recipe
- **Depends on:** —
- **Goal:** A recipe typed into a form is saved and shown on its own page.
- **Scope:** The recipes table, the form and the recipe page.
- **Acceptance:** A recipe saved through the form is still there after a restart.

#### T1.2 — List the recipes
- **Depends on:** T1.1
- **Goal:** The home page lists every recipe by name.
- **Scope:** The home page, sorted by name, with a link to each recipe.
- **Acceptance:** Three saved recipes show on the home page in name order.

#### T1.3 — Search by ingredient
- **Depends on:** T1.2
- **Goal:** The owner finds recipes by what is in the fridge.
- **Scope:** A search box on the home page that matches ingredient lines.
- **Acceptance:** Searching for "leek" lists only the recipes with leek in them.

### M2 — Share recipes

**Outcome:** a recipe can leave the app, on paper or as a link.

#### T2.1 — Print a recipe
- **Depends on:** T1.2
- **Goal:** A recipe prints cleanly on one sheet.
- **Scope:** A print style sheet for the recipe page.
- **Acceptance:** The print preview of a long recipe has no menu and no cut-off lines.

#### T2.2 — Share a recipe by link
- **Depends on:** T2.1 (it reuses the print layout)
- **Goal:** The owner sends a link that shows one recipe to someone with no account.
- **Scope:** A share button, a link with a random token, and a read-only page behind it.
- **Acceptance:** The link opens the recipe in a private window, and a wrong token shows a not-found page.

### M3 — Plan the week

**Outcome:** the owner picks the week's meals and gets one shopping list.

#### T3.1 — Pick recipes for a week
- **Depends on:** T1.2
- **Goal:** The owner puts a recipe on each day of the coming week.
- **Scope:** A week page with seven days and a way to put a recipe on a day.
- **Acceptance:** A recipe put on Tuesday is still on Tuesday after a restart.

#### T3.2 — Make a shopping list
- **Depends on:** T3.1
- **Goal:** One list of everything the week's recipes need.
- **Scope:** A list page that adds up the ingredient lines of the week's recipes.
- **Acceptance:** Two recipes that both need leeks give one leek line on the list.

## Deferred

- Photos of the dish.
- Scaling a recipe to more or fewer servings.

## Open questions

| Question | Needed by |
|---|---|
| Should a shared link stop working after a while? | T2.2 |
| Which paper size should the print page fit? | T2.1 |

## Definition of done

- `make check` passes.
- Every task's Acceptance line was tried by hand against a running copy.
