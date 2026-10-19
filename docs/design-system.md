# afk web page — design system

Calm, plain, glanceable. A warm paper ground, ink text, one blue for actions, and orange used for one
thing only: **something needs the owner**. System fonts, no web fonts, no framework. Every value is a CSS
custom property; every component is a class that a Go `html/template` can emit.

The canvas this was drawn on holds the same tokens, components and screens as artboards. Class names
below are the names on the canvas.

Direction, in one line: *a quiet instrument panel* — the owner should read the state of every build in a
second, and find the one thing that needs them without hunting.

Rules that run through everything:

- **Colour never stands alone.** Every badge carries a word; every meter carries a number.
- **Orange = needs you.** Waiting on you, plan waiting for approval, blocking, parked, owner task. Nothing else is orange.
- **Red = something broke.** Failed, line stopped, checker errors, destructive buttons.
- **Blue = afk is doing something, or you can.** Running, shipping, primary buttons, links, focus.
- **Grey = quiet.** Paused, held, nothing ready, pending, superseded.
- **Green = done.** Done, finished, review passed, progress fill.
- Plain words in buttons: "Pause project", "Approve the plan…", "Answer in the inbox". An ellipsis means a confirm follows.

---

## 1. Tokens

All tokens live on `.afk` (put `class="afk"` on `<body>`). Dark follows the device through
`prefers-color-scheme`; `data-theme="light"` or `data-theme="dark"` on the same element forces one.

### 1.1 Colour

| Token | Light | Dark | Use |
|---|---|---|---|
| `--bg` | `#F6F5F1` | `#141412` | page ground |
| `--surface` | `#FDFDFB` | `#1C1B19` | cards, lists, inputs |
| `--surface-2` | `#EFEEE9` | `#252421` | hover, table heads, quiet fills |
| `--border` | `#DCDAD2` | `#34322E` | dividers, card edges |
| `--border-input` | `#8A877D` | `#7A776F` | input and secondary-button edges (3:1 on surface) |
| `--text` | `#1C1B18` | `#ECEAE4` | body text |
| `--text-2` | `#4F4D47` | `#C4C1B8` | secondary text |
| `--text-3` | `#66645C` | `#A19E95` | meta, hints (≥4.5:1 on bg and surface-2) |
| `--accent` | `#2457B0` | `#8AB4FF` | primary button, links, running |
| `--accent-hover` | `#1C478F` | `#A8C7FF` | |
| `--accent-active` | `#163A75` | `#C2D7FF` | |
| `--accent-soft` | `#E3EBF8` | `#1E2A40` | running badge fill, selected row, owner chat bubble, change flash |
| `--on-accent` | `#FFFFFF` | `#0E1A2E` | text on accent |
| `--attn` | `#9A4A00` | `#F2A65A` | needs-you text |
| `--attn-solid` | `#C25E08` | `#F2A65A` | needs-you row edge (non-text) |
| `--attn-strong` | `#A85006` | `#F2A65A` | count pill fill |
| `--on-attn` | `#FFFFFF` | `#1C1B18` | text on attn-strong |
| `--attn-soft` | `#FBEBD9` | `#3A2A18` | needs-you fills |
| `--ok` | `#2E6B3A` | `#7FC28C` | done text, progress fill |
| `--ok-soft` | `#E2EFE3` | `#1F3023` | |
| `--danger` | `#B3261E` | `#FF8A80` | failed, errors, danger button |
| `--danger-hover` | `#93201A` | `#FFA79F` | |
| `--on-danger` | `#FFFFFF` | `#2A0B08` | |
| `--danger-soft` | `#F8E3E1` | `#3D1F1C` | |
| `--idle` | `#66645C` | `#A19E95` | quiet badge text |
| `--idle-soft` | `#ECEAE4` | `#2A2926` | quiet badge fill, empty tracks |
| `--focus` | `#2457B0` | `#8AB4FF` | focus ring |
| `--scrim` | `rgb(28 27 24 / .45)` | `rgb(0 0 0 / .6)` | behind confirms |
| `--code-bg` | `#EFEEE9` | `#252421` | inline code, code blocks |

All text pairs meet WCAG 2.2 AA (4.5:1; status text on its soft fill is ≥5:1). Status colours that sit
side by side also differ in lightness, so they read in greyscale.

### 1.2 Type

No third-party fonts. Two system stacks:

```css
--font-sans: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI Variable Text", "Segoe UI", system-ui, sans-serif;
--font-mono: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
```

| Token | Size | Line height | Weight | Use |
|---|---|---|---|---|
| `--text-2xl` | 1.75rem / 28px | 1.2 | 600 | page heading (`.h1`) |
| `--text-xl` | 1.375rem / 22px | 1.3 | 600 | state headline, confirm title (`.h2`) |
| `--text-lg` | 1.125rem / 18px | 1.45 | 600 | card titles, app-bar title (`.h3`) |
| `--text-md` | 1rem / 16px | 1.5 | 400 | body, inputs (16px stops iOS zoom) |
| `--text-sm` | 0.875rem / 14px | 1.45 | 400 | rows, hints, meta |
| `--text-xs` | 0.75rem / 12px | 1.4 | 600 | badges, labels, live status |
| prose | 1rem | 1.6 | 400 | plans and agent messages, max `--measure` (68ch) |

Weights: `--weight-regular: 400`, `--weight-medium: 500`, `--weight-semibold: 600`.
Monospace for task ids (`T2.3`), session ids (`s-0405`), paths, file names, numbers in meters and stats,
and the `afk` wordmark.

### 1.3 Spacing, radii, shadows, sizes

```css
--space-1: 4px;  --space-2: 8px;  --space-3: 12px; --space-4: 16px;
--space-5: 24px; --space-6: 32px; --space-7: 48px; --space-8: 64px;

--radius-sm: 4px;    /* inline code, focus ring corners */
--radius-md: 8px;    /* buttons, inputs, options, stats */
--radius-lg: 12px;   /* cards, lists, notices, sheets */
--radius-pill: 999px;/* badges, counts, tracks */

--shadow-1: 0 1px 2px rgb(28 27 24 / .06), 0 1px 1px rgb(28 27 24 / .04);  /* question card */
--shadow-2: 0 12px 32px rgb(28 27 24 / .16), 0 2px 6px rgb(28 27 24 / .08); /* sheets, toast */
/* dark: --shadow-1: 0 1px 2px rgb(0 0 0 / .4); --shadow-2: 0 12px 32px rgb(0 0 0 / .55), 0 2px 6px rgb(0 0 0 / .4); */

--target: 44px;      /* every control on the phone */
--target-min: 24px;  /* floor for inline links */
--measure: 68ch;
--dur-fast: 120ms;   /* hover transitions */
--dur-flash: 1600ms; /* "just changed" fade */
```

Page gutter 16px on the phone, 24–32px on desktop. Sections are 24px apart; items inside a section 12px.
Motion is limited to the live pulse, the spinner and the change flash, and all of it stops under
`prefers-reduced-motion`.

---

## 2. Components

Each component is a class (BEM-ish: `block`, `block__part`, `block--variant`, `is-state`). States use
real pseudo-classes in the product (`:hover`, `:focus-visible`, `:active`, `:disabled`); the `.is-hover`,
`.is-focus`, `.is-active` classes in the stylesheet exist only to draw states on the canvas and can be
dropped. Focus everywhere: 2px `--focus` outline, 2px offset.

### app-bar
Top bar of every phone screen. Sticky.
Parts: `app-bar__title`, `app-bar__actions`; `app-bar--back` when it starts with a back `icon-btn`.
Holds the `wordmark` on Overview, otherwise back + title + `live`.
```html
<header class="app-bar app-bar--back">
  <a class="icon-btn" href="/" aria-label="Back to all projects">…chevron svg…</a>
  <h1 class="app-bar__title">garden-planner</h1>
  <span class="live live--working">Working</span>
</header>
```

### topnav (desktop)
Desktop replacement for app-bar. Parts `topnav__links`, `topnav__link` (`aria-current="page"` = current).
Every desktop screen below the home screen keeps the phone's back navigation: an `app-bar app-bar--back
app-bar--desktop` sits under the `topnav` with the same back `icon-btn` (same chevron, same
`aria-label` "Back to …", same target as on the phone), the page title, and optional meta. On desktop it is
not sticky and its back button lines up with the 24px content edge. The home screen (Overview) has no
back button, the same as on the phone.

### live
The quiet "this page updates by itself" signal, top right.
| State | Class | Looks |
|---|---|---|
| live, idle | `live` | green dot, "Live" |
| working | `live--working` | blue pulsing dot, "Working" |
| offline | `live--offline` | red ring, "Can't reach afk, retrying" |
Pair with a `meta` "Updated 12 s ago" where freshness matters (Project, Usage).

### btn
| Variant | Use |
|---|---|
| `btn--primary` | the one main action on a screen |
| `btn--secondary` | other actions (Pause, Refresh, Add) |
| `btn--quiet` | Cancel, back-links, low-weight |
| `btn--danger` | Remove, Clean — only inside a confirm |

Modifiers: `btn--block` (full width, phone), `btn--sm` (36px, desktop only).
| State | Rule |
|---|---|
| default | as variant |
| hover | darker fill (`--accent-hover`, `--surface-2`, `--accent-soft`, `--danger-hover`) |
| focus | focus ring |
| active | `--accent-active` / `--border` |
| disabled | `--surface-2` fill, `--text-3` text, `--border` edge, `cursor: not-allowed` |
| loading | `is-loading` + `aria-busy="true"`, a `spinner` before an "-ing" label ("Refreshing"), width kept |

Groups: `btn-row` (wrapping row), `btn-row--end`, `btn-stack` (column, phone).

### icon-btn
44×44 icon-only button or link. Always has `aria-label`. Hover: `--surface-2`.

### spinner
16px ring in `currentColor`; used in loading buttons and "Creating the folder…" lines.

### badge
24px pill: dot + word. Never only colour.
| Group | Variants (class → word) |
|---|---|
| Project state | `badge--running` Building · `badge--needs-you` Waiting on you / Plan waiting for approval · `badge--paused` Paused · `badge--stopped` Line stopped · `badge--held` Held for usage · `badge--idle` Nothing ready · `badge--finished` Finished |
| Task status | `badge--pending` · `badge--running` · `badge--shipping` · `badge--parked` · `badge--done` · `badge--failed` · `badge--superseded` (struck through) |
| Inbox / review / checker | `badge--blocking` · `badge--pass` Review passed · `badge--error` · `badge--warning` |
`badge--plain` drops the dot.

### count
Number pill for inbox counts: `count` (orange, needs you), `count--quiet` (zero).

### tid · path · wordmark
`tid`: monospace task or session id. `path`: monospace, wraps anywhere. `wordmark`: the lowercase `afk`.

### field · input · textarea
Parts: `field__label` (always a real `<label for>`), `field__hint`, `field__error`.
`input--mono` for paths and names.
| State | Rule |
|---|---|
| default | 1px `--border-input` |
| hover | edge `--text-2` |
| focus | 2px `--focus` outline at 0 offset |
| error | `is-invalid` / `aria-invalid="true"`: 2px `--danger` edge, `field__error` below linked by `aria-describedby` |
| disabled | `--surface-2` fill, `--text-3` |

### choice-list · choice
Radio list in a card: status picker, repo yes/no, project to remove. Each `choice` is a `<label>` 48px tall
with a native radio. `is-checked` = `--accent-soft`; disabled options show why in `choice__meta`.
Wrap in `<fieldset>` + `<legend>`.

### segmented
Small radio group shown as joined buttons (task filter: Open / Done / All). Checked = inverted.

### card · section · section-head · divider
`card`: surface with 12px radius and 16px padding (`card--flush` for no padding). `section` stacks a
`section-head` (`section-head__title`, optional right-hand meta) over its content.

### list · row
The workhorse: project rows, task rows, session rows, inbox rows. `list` is a `<ul>`; each `row` is an
`<a>` (or a `<button>` for an action row).
Parts: `row__main`, `row__top` (title + badges), `row__title`, `row__sub`, `row__meta`, `row__aside`, `row__chev`.
| State | Class | Looks |
|---|---|---|
| default | `row` | |
| hover | `:hover` | `--surface-2` |
| focus | `:focus-visible` | inset ring |
| needs you | `row--needs-you` | 4px `--attn-solid` inner edge on the leading side |
| just changed | `is-changed` | background fades from `--accent-soft` over 1.6s; add for one update, then remove |
| selected | `is-selected` + `aria-current` | `--accent-soft` (desktop list beside detail) |

### progress
Tasks done of total: `progress__track`, `progress__fill` (green), `progress__label` ("12 of 31").

### meter
Usage window: `meter__top` (`meter__name`, `meter__value` "88% used"), `meter__track` with
`role="progressbar"` and aria values, `meter__fill`, `meter__foot` ("Resets at 3:40 pm").
States: default blue, `meter--warn` (≥80%, orange), `meter--full` (100%, red).

### split-bar · legend · stats · stat · kv
Token split by phase: `split-bar` segments + `legend` (`legend__swatch`, `legend__value`). `stats` grid of
`stat` (`stat__value` mono, `stat__label`). `kv`: a `<dl>` of label / value pairs.

### notice
Inline message with icon and `notice__body` (`notice__title` optional).
Variants: `notice--attention` (needs you), `notice--error` (`role="alert"`), `notice--info`, `notice--ok`.

### nav-list · nav-item
Two-column grid of large links from a project to its pages, each with a live hint (count or "12 of 31").

### confirm-sheet
Every action that changes something goes through it. A native `<dialog>` opened with `showModal()`.
Parts: `scrim`, `confirm-sheet__title` (the question, naming the thing), `confirm-sheet__body` (one
sentence on what happens and what is not touched), `btn-row` (action + safe choice).
Phone: sheet from the bottom. Desktop: `scrim--center`. Focus starts on the safe button; Escape and the
safe button close it; on confirm the action button turns loading, then the sheet closes and a `toast` says
what happened.

### toast
Inverted strip, `role="status"`, for "Paused trail-log. Undo" or "Answer sent." Auto-hides after 5s.

### empty
Centered icon, `empty__title`, one line. "Nothing needs you", "No planning session".

### prose · table-wrap
Rendered markdown for plans and agent messages: headings, lists, inline code, `pre` (scrolls sideways),
tables inside `table-wrap` (scrolls sideways). Line height 1.6, 68ch measure.

### finding
Checker line: severity `badge` + `finding__body` + `finding__where` (file and line, links into the plan).
Errors are listed before warnings.

### thread · msg · question · option · composer · working
- `thread`: the conversation column, `aria-live="polite"`.
- `msg msg--agent`: `msg__who` + `prose`, full width. `msg msg--owner`: right-aligned `msg__bubble` in `--accent-soft`.
- `question`: card with `question__text`, `option` buttons, then a `field` for the owner's own answer.
- `option`: `option__top` (label + `option__mark`), `option__desc`. `option--recommended` is listed first,
  has a 2px accent edge and the word "Recommended". States: hover `--surface-2`, focus ring, chosen
  `is-chosen` + `aria-pressed="true"`, disabled after the answer is sent.
- `composer`: sticky reply box at the bottom: textarea + Send. Send is disabled while empty.
- `working`: three pulsing dots + "The agent is working · drafting milestone 4".

### steps
Planning phases as a vertical list: `step`, `step__dot`; `is-done` (green), `is-current` (blue, `aria-current="step"`).

### shell · split
Desktop layout: `shell` (column, full height), `split` (wrapping row), `split__list` (380–460px, list or
side info) and `split__detail` (the rest). Below 760px the list stacks above the detail.

### Reference stylesheet

The complete `afk.css` the canvas uses. It is meant to be served as-is (the `.afk.screen` and `.page`
rules frame the phone artboards; keep `.page` and drop `.afk.screen`).

```css
/* afk.css — tokens and components for the afk web page.
   Plain CSS, served by the Go program as-is. System fonts only.
   Dark follows the device; data-theme="light|dark" on .afk forces one. */

/* ───────── Tokens: light ───────── */
.afk {
  --bg: #F6F5F1;
  --surface: #FDFDFB;
  --surface-2: #EFEEE9;
  --border: #DCDAD2;
  --border-input: #8A877D;
  --text: #1C1B18;
  --text-2: #4F4D47;
  --text-3: #66645C;
  --accent: #2457B0;
  --accent-hover: #1C478F;
  --accent-active: #163A75;
  --accent-soft: #E3EBF8;
  --on-accent: #FFFFFF;
  --attn: #9A4A00;
  --attn-solid: #C25E08;
  --attn-soft: #FBEBD9;
  --attn-strong: #A85006;
  --on-attn: #FFFFFF;
  --ok: #2E6B3A;
  --ok-soft: #E2EFE3;
  --danger: #B3261E;
  --danger-hover: #93201A;
  --danger-soft: #F8E3E1;
  --on-danger: #FFFFFF;
  --idle: #66645C;
  --idle-soft: #ECEAE4;
  --focus: #2457B0;
  --scrim: rgb(28 27 24 / 0.45);
  --code-bg: #EFEEE9;
  --shadow-1: 0 1px 2px rgb(28 27 24 / 0.06), 0 1px 1px rgb(28 27 24 / 0.04);
  --shadow-2: 0 12px 32px rgb(28 27 24 / 0.16), 0 2px 6px rgb(28 27 24 / 0.08);

  --font-sans: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI Variable Text", "Segoe UI", system-ui, sans-serif;
  --font-mono: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
  --text-xs: 0.75rem;   --lh-xs: 1.4;
  --text-sm: 0.875rem;  --lh-sm: 1.45;
  --text-md: 1rem;      --lh-md: 1.5;
  --text-lg: 1.125rem;  --lh-lg: 1.45;
  --text-xl: 1.375rem;  --lh-xl: 1.3;
  --text-2xl: 1.75rem;  --lh-2xl: 1.2;
  --weight-regular: 400; --weight-medium: 500; --weight-semibold: 600;

  --space-1: 4px; --space-2: 8px; --space-3: 12px; --space-4: 16px;
  --space-5: 24px; --space-6: 32px; --space-7: 48px; --space-8: 64px;
  --radius-sm: 4px; --radius-md: 8px; --radius-lg: 12px; --radius-pill: 999px;
  --target: 44px; --target-min: 24px;
  --measure: 68ch;
  --dur-fast: 120ms; --dur-flash: 1600ms;

  color-scheme: light;
  background: var(--bg);
  color: var(--text);
  font-family: var(--font-sans);
  font-size: var(--text-md);
  line-height: var(--lh-md);
  -webkit-text-size-adjust: 100%;
}

/* ───────── Tokens: dark ───────── */
.afk[data-theme="dark"] {
  --bg: #141412; --surface: #1C1B19; --surface-2: #252421; --border: #34322E; --border-input: #7A776F;
  --text: #ECEAE4; --text-2: #C4C1B8; --text-3: #A19E95;
  --accent: #8AB4FF; --accent-hover: #A8C7FF; --accent-active: #C2D7FF; --accent-soft: #1E2A40; --on-accent: #0E1A2E;
  --attn: #F2A65A; --attn-solid: #F2A65A; --attn-soft: #3A2A18; --attn-strong: #F2A65A; --on-attn: #1C1B18;
  --ok: #7FC28C; --ok-soft: #1F3023;
  --danger: #FF8A80; --danger-hover: #FFA79F; --danger-soft: #3D1F1C; --on-danger: #2A0B08;
  --idle: #A19E95; --idle-soft: #2A2926;
  --focus: #8AB4FF; --scrim: rgb(0 0 0 / 0.6); --code-bg: #252421;
  --shadow-1: 0 1px 2px rgb(0 0 0 / 0.4);
  --shadow-2: 0 12px 32px rgb(0 0 0 / 0.55), 0 2px 6px rgb(0 0 0 / 0.4);
  color-scheme: dark;
}
@media (prefers-color-scheme: dark) {
  .afk:not([data-theme="light"]) {
    --bg: #141412; --surface: #1C1B19; --surface-2: #252421; --border: #34322E; --border-input: #7A776F;
    --text: #ECEAE4; --text-2: #C4C1B8; --text-3: #A19E95;
    --accent: #8AB4FF; --accent-hover: #A8C7FF; --accent-active: #C2D7FF; --accent-soft: #1E2A40; --on-accent: #0E1A2E;
    --attn: #F2A65A; --attn-solid: #F2A65A; --attn-soft: #3A2A18; --attn-strong: #F2A65A; --on-attn: #1C1B18;
    --ok: #7FC28C; --ok-soft: #1F3023;
    --danger: #FF8A80; --danger-hover: #FFA79F; --danger-soft: #3D1F1C; --on-danger: #2A0B08;
    --idle: #A19E95; --idle-soft: #2A2926;
    --focus: #8AB4FF; --scrim: rgb(0 0 0 / 0.6); --code-bg: #252421;
    --shadow-1: 0 1px 2px rgb(0 0 0 / 0.4);
    --shadow-2: 0 12px 32px rgb(0 0 0 / 0.55), 0 2px 6px rgb(0 0 0 / 0.4);
    color-scheme: dark;
  }
}

/* ───────── Base ───────── */
.afk *, .afk *::before, .afk *::after { box-sizing: border-box; }
.afk h1, .afk h2, .afk h3, .afk p, .afk ul, .afk ol, .afk dl, .afk dd, .afk figure { margin: 0; }
.afk ul.list, .afk ol.list { padding: 0; list-style: none; }
.afk a { color: var(--accent); text-underline-offset: 3px; }
.afk a:hover { color: var(--accent-hover); }
.afk :focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; border-radius: var(--radius-sm); }
.afk .mono, .afk code, .afk kbd { font-family: var(--font-mono); font-size: 0.9em; }
.afk .h1 { font-size: var(--text-2xl); line-height: var(--lh-2xl); font-weight: var(--weight-semibold); letter-spacing: -0.01em; }
.afk .h2 { font-size: var(--text-xl); line-height: var(--lh-xl); font-weight: var(--weight-semibold); }
.afk .h3 { font-size: var(--text-lg); line-height: var(--lh-lg); font-weight: var(--weight-semibold); }
.afk .small { font-size: var(--text-sm); line-height: var(--lh-sm); }
.afk .meta { font-size: var(--text-sm); line-height: var(--lh-sm); color: var(--text-3); }
.afk .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
@media (prefers-reduced-motion: reduce) { .afk * { animation: none !important; transition: none !important; } }

/* Canvas-only phone frame (not shipped) */
.afk.screen { width: 390px; min-height: 844px; display: flex; flex-direction: column; }
.page { display: flex; flex-direction: column; gap: var(--space-5); padding: var(--space-4) var(--space-4) var(--space-7); }

/* ───────── app-bar ───────── */
.app-bar { position: sticky; top: 0; z-index: 2; display: flex; align-items: center; gap: var(--space-2);
  min-height: 56px; padding: var(--space-2) var(--space-2) var(--space-2) var(--space-4);
  background: var(--bg); border-bottom: 1px solid var(--border); }
.app-bar--back { padding-left: var(--space-1); }
.app-bar__title { flex: 1; min-width: 0; font-size: var(--text-lg); font-weight: var(--weight-semibold); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.app-bar__actions { display: flex; align-items: center; gap: var(--space-1); }
.wordmark { font-family: var(--font-mono); font-weight: var(--weight-semibold); font-size: var(--text-lg); letter-spacing: -0.02em; }

/* ───────── live-status ───────── */
.live { display: inline-flex; align-items: center; gap: 6px; font-size: var(--text-xs); line-height: var(--lh-xs); color: var(--text-3); white-space: nowrap; }
.live::before { content: ""; width: 8px; height: 8px; border-radius: 50%; background: var(--ok); }
.live--working::before { background: var(--accent); animation: afk-pulse 1.4s ease-in-out infinite; }
.live--offline { color: var(--danger); }
.live--offline::before { background: transparent; border: 2px solid var(--danger); width: 8px; height: 8px; }
@keyframes afk-pulse { 50% { opacity: 0.3; } }

/* ───────── buttons ───────── */
.btn { display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2);
  min-height: var(--target); padding: 0 var(--space-4); border-radius: var(--radius-md);
  border: 1px solid transparent; font: inherit; font-size: var(--text-md); font-weight: var(--weight-medium);
  line-height: 1.2; text-decoration: none; cursor: pointer; white-space: nowrap;
  transition: background var(--dur-fast), border-color var(--dur-fast), color var(--dur-fast); }
.btn svg { width: 18px; height: 18px; flex: none; }
.btn--primary { background: var(--accent); color: var(--on-accent); }
.btn--primary:hover, .btn--primary.is-hover { background: var(--accent-hover); color: var(--on-accent); }
.btn--primary:active, .btn--primary.is-active { background: var(--accent-active); }
.btn--secondary { background: var(--surface); color: var(--text); border-color: var(--border-input); }
.btn--secondary:hover, .btn--secondary.is-hover { background: var(--surface-2); color: var(--text); }
.btn--secondary:active, .btn--secondary.is-active { background: var(--border); }
.btn--quiet { background: transparent; color: var(--accent); padding: 0 var(--space-3); }
.btn--quiet:hover, .btn--quiet.is-hover { background: var(--accent-soft); color: var(--accent-hover); }
.btn--danger { background: var(--danger); color: var(--on-danger); }
.btn--danger:hover, .btn--danger.is-hover { background: var(--danger-hover); color: var(--on-danger); }
.btn--block { width: 100%; }
.btn--sm { min-height: 36px; padding: 0 var(--space-3); font-size: var(--text-sm); }
.btn.is-focus { outline: 2px solid var(--focus); outline-offset: 2px; }
.btn:disabled, .btn[aria-disabled="true"] { background: var(--surface-2); color: var(--text-3); border-color: var(--border); cursor: not-allowed; }
.btn.is-loading { cursor: progress; }
.btn-row { display: flex; flex-wrap: wrap; gap: var(--space-2); }
.btn-row--end { justify-content: flex-end; }
.btn-stack { display: flex; flex-direction: column; gap: var(--space-2); }

.icon-btn { display: inline-grid; place-items: center; width: var(--target); height: var(--target); flex: none;
  border: 0; border-radius: var(--radius-md); background: transparent; color: var(--text-2); cursor: pointer; text-decoration: none; }
.icon-btn svg { width: 22px; height: 22px; }
.icon-btn:hover, .icon-btn.is-hover { background: var(--surface-2); color: var(--text); }

.spinner { width: 16px; height: 16px; border-radius: 50%; border: 2px solid currentColor; border-right-color: transparent; animation: afk-spin 0.8s linear infinite; flex: none; }
@keyframes afk-spin { to { transform: rotate(360deg); } }

/* ───────── badges ───────── */
.badge { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 var(--space-2);
  border-radius: var(--radius-pill); font-size: var(--text-xs); font-weight: var(--weight-semibold);
  line-height: 1; white-space: nowrap; background: var(--idle-soft); color: var(--idle); }
.badge::before { content: ""; width: 7px; height: 7px; border-radius: 50%; background: currentColor; }
.badge--plain::before { display: none; }
.badge--running, .badge--shipping { background: var(--accent-soft); color: var(--accent); }
.badge--needs-you, .badge--parked, .badge--blocking, .badge--warning { background: var(--attn-soft); color: var(--attn); }
.badge--finished, .badge--done, .badge--pass { background: var(--ok-soft); color: var(--ok); }
.badge--failed, .badge--stopped, .badge--error { background: var(--danger-soft); color: var(--danger); }
.badge--paused, .badge--held, .badge--idle, .badge--pending, .badge--superseded { background: var(--idle-soft); color: var(--idle); }
.badge--paused::before { border-radius: 1px; width: 7px; height: 8px; background: linear-gradient(90deg, currentColor 0 35%, transparent 35% 65%, currentColor 65%); }
.badge--superseded { text-decoration: line-through; }

.count { display: inline-grid; place-items: center; min-width: 22px; height: 22px; padding: 0 6px;
  border-radius: var(--radius-pill); background: var(--attn-strong); color: var(--on-attn); font-size: var(--text-xs); font-weight: var(--weight-semibold); line-height: 1; }
.count--quiet { background: var(--idle-soft); color: var(--text-2); }

.tid { font-family: var(--font-mono); font-size: var(--text-sm); color: var(--text-2); white-space: nowrap; }
.path { font-family: var(--font-mono); font-size: var(--text-sm); overflow-wrap: anywhere; }

/* ───────── fields ───────── */
.field { display: flex; flex-direction: column; gap: 6px; }
.field__label { font-size: var(--text-sm); font-weight: var(--weight-semibold); color: var(--text); }
.field__hint { font-size: var(--text-sm); color: var(--text-3); }
.field__error { font-size: var(--text-sm); color: var(--danger); display: flex; gap: 6px; align-items: flex-start; }
.input, .textarea { width: 100%; min-height: var(--target); padding: 10px var(--space-3); font: inherit; font-size: var(--text-md);
  color: var(--text); background: var(--surface); border: 1px solid var(--border-input); border-radius: var(--radius-md); }
.input--mono { font-family: var(--font-mono); font-size: 15px; }
.textarea { min-height: 96px; resize: vertical; line-height: var(--lh-md); }
.input::placeholder, .textarea::placeholder { color: var(--text-3); }
.input:hover, .textarea:hover, .input.is-hover { border-color: var(--text-2); }
.input:focus-visible, .textarea:focus-visible, .input.is-focus { outline: 2px solid var(--focus); outline-offset: 0; border-color: var(--focus); }
.input:disabled { background: var(--surface-2); color: var(--text-3); border-color: var(--border); }
.input.is-invalid, .input[aria-invalid="true"] { border-color: var(--danger); border-width: 2px; padding: 9px 11px; }

.choice-list { display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: var(--radius-lg); background: var(--surface); overflow: hidden; }
.choice { display: flex; align-items: center; gap: var(--space-3); min-height: 48px; padding: var(--space-2) var(--space-4); cursor: pointer; }
.choice + .choice { border-top: 1px solid var(--border); }
.choice input { width: 20px; height: 20px; margin: 0; accent-color: var(--accent); flex: none; }
.choice:hover { background: var(--surface-2); }
.choice.is-checked { background: var(--accent-soft); }
.choice__label { flex: 1; }
.choice__meta { font-size: var(--text-sm); color: var(--text-3); }

.switch-row { display: flex; gap: var(--space-2); }
.segmented { display: inline-flex; border: 1px solid var(--border-input); border-radius: var(--radius-md); overflow: hidden; }
.segmented label { display: grid; place-items: center; min-height: 40px; padding: 0 var(--space-4); font-size: var(--text-sm); font-weight: var(--weight-medium); cursor: pointer; }
.segmented label + label { border-left: 1px solid var(--border-input); }
.segmented input { position: absolute; opacity: 0; }
.segmented label.is-checked { background: var(--text); color: var(--bg); }

/* ───────── surfaces ───────── */
.card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); padding: var(--space-4); display: flex; flex-direction: column; gap: var(--space-3); }
.card--flush { padding: 0; gap: 0; overflow: hidden; }
.section { display: flex; flex-direction: column; gap: var(--space-3); }
.section-head { display: flex; align-items: baseline; justify-content: space-between; gap: var(--space-3); }
.section-head__title { font-size: var(--text-sm); font-weight: var(--weight-semibold); color: var(--text-2); letter-spacing: 0.02em; }
.divider { height: 1px; background: var(--border); border: 0; margin: 0; }

/* ───────── list rows ───────── */
.list { display: flex; flex-direction: column; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); overflow: hidden; }
.row { display: flex; gap: var(--space-3); align-items: flex-start; min-height: 56px; padding: var(--space-3) var(--space-4);
  color: inherit; text-decoration: none; position: relative; }
.list > li + li > .row, .list > .row + .row { border-top: 1px solid var(--border); }
a.row:hover, a.row.is-hover { background: var(--surface-2); color: inherit; }
a.row:focus-visible { outline-offset: -2px; border-radius: 0; }
.row__main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.row__top { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; }
.row__title { font-weight: var(--weight-semibold); overflow-wrap: anywhere; }
.row__sub { font-size: var(--text-sm); color: var(--text-2); overflow-wrap: anywhere; }
.row__meta { font-size: var(--text-sm); color: var(--text-3); display: flex; flex-wrap: wrap; gap: 4px var(--space-3); }
.row__aside { display: flex; flex-direction: column; align-items: flex-end; gap: 6px; flex: none; }
.row__chev { color: var(--text-3); width: 18px; height: 18px; flex: none; align-self: center; }
.row--needs-you { box-shadow: inset 4px 0 0 var(--attn-solid); }
.row.is-changed { animation: afk-flash var(--dur-flash) ease-out; }
.row.is-changed-static { background: var(--accent-soft); }
.row.is-selected { background: var(--accent-soft); }
@keyframes afk-flash { from { background: var(--accent-soft); } to { background: transparent; } }

/* ───────── progress + meter ───────── */
.progress { display: flex; align-items: center; gap: var(--space-2); font-size: var(--text-sm); color: var(--text-2); }
.progress__track { flex: 1; height: 6px; border-radius: var(--radius-pill); background: var(--idle-soft); overflow: hidden; min-width: 60px; }
.progress__fill { display: block; height: 100%; background: var(--ok); border-radius: inherit; }
.progress__label { font-family: var(--font-mono); font-size: var(--text-xs); white-space: nowrap; }

.meter { display: flex; flex-direction: column; gap: var(--space-2); }
.meter__top { display: flex; justify-content: space-between; align-items: baseline; gap: var(--space-3); }
.meter__name { font-weight: var(--weight-semibold); }
.meter__value { font-family: var(--font-mono); font-size: var(--text-sm); }
.meter__track { height: 10px; border-radius: var(--radius-pill); background: var(--idle-soft); overflow: hidden; }
.meter__fill { display: block; height: 100%; background: var(--accent); border-radius: inherit; }
.meter--warn .meter__fill { background: var(--attn-solid); }
.meter--full .meter__fill { background: var(--danger); }
.meter__foot { font-size: var(--text-sm); color: var(--text-3); }

/* ───────── notice ───────── */
.notice { display: flex; gap: var(--space-3); align-items: flex-start; padding: var(--space-3) var(--space-4);
  border-radius: var(--radius-lg); background: var(--surface-2); color: var(--text); font-size: var(--text-sm); line-height: var(--lh-sm); }
.notice svg { width: 20px; height: 20px; flex: none; margin-top: 1px; }
.notice__body { flex: 1; display: flex; flex-direction: column; gap: var(--space-2); }
.notice__title { font-weight: var(--weight-semibold); font-size: var(--text-md); }
.notice--attention { background: var(--attn-soft); }
.notice--attention svg { color: var(--attn); }
.notice--error { background: var(--danger-soft); }
.notice--error svg { color: var(--danger); }
.notice--info { background: var(--accent-soft); }
.notice--info svg { color: var(--accent); }
.notice--ok { background: var(--ok-soft); }
.notice--ok svg { color: var(--ok); }

/* ───────── key/value + stats ───────── */
.kv { display: grid; grid-template-columns: auto 1fr; gap: var(--space-2) var(--space-4); font-size: var(--text-sm); }
.kv dt { color: var(--text-3); }
.kv dd { color: var(--text); overflow-wrap: anywhere; }
.stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: var(--space-2); }
.stat { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--space-3); display: flex; flex-direction: column; gap: 2px; }
.stat__value { font-family: var(--font-mono); font-size: var(--text-lg); font-weight: var(--weight-semibold); }
.stat__label { font-size: var(--text-xs); color: var(--text-3); }
.split-bar { display: flex; height: 10px; border-radius: var(--radius-pill); overflow: hidden; gap: 2px; }
.split-bar span { display: block; height: 100%; }
.legend { display: flex; flex-direction: column; gap: 6px; font-size: var(--text-sm); }
.legend__item { display: flex; align-items: center; gap: var(--space-2); }
.legend__swatch { width: 10px; height: 10px; border-radius: 2px; flex: none; }
.legend__value { margin-left: auto; font-family: var(--font-mono); font-size: var(--text-sm); }

/* ───────── nav-list (project links) ───────── */
.nav-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--space-2); }
.nav-item { display: flex; align-items: center; justify-content: space-between; gap: var(--space-2); min-height: 52px; padding: 0 var(--space-4);
  background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); color: var(--text); text-decoration: none; font-weight: var(--weight-medium); }
.nav-item:hover { background: var(--surface-2); color: var(--text); }

/* ───────── confirm-sheet ───────── */
.scrim { position: absolute; inset: 0; background: var(--scrim); display: flex; align-items: flex-end; z-index: 10; }
.confirm-sheet { width: 100%; background: var(--surface); border-radius: var(--radius-lg) var(--radius-lg) 0 0; box-shadow: var(--shadow-2);
  padding: var(--space-5) var(--space-4) var(--space-6); display: flex; flex-direction: column; gap: var(--space-4); border: 1px solid var(--border); }
.confirm-sheet__title { font-size: var(--text-xl); line-height: var(--lh-xl); font-weight: var(--weight-semibold); }
.confirm-sheet__body { color: var(--text-2); }
.scrim--center { align-items: center; justify-content: center; }
.scrim--center .confirm-sheet { width: min(480px, calc(100% - 32px)); border-radius: var(--radius-lg); }

/* ───────── empty-state ───────── */
.empty { display: flex; flex-direction: column; align-items: center; text-align: center; gap: var(--space-3); padding: var(--space-7) var(--space-4); color: var(--text-2); }
.empty__title { font-size: var(--text-lg); font-weight: var(--weight-semibold); color: var(--text); }
.empty svg { width: 32px; height: 32px; color: var(--text-3); }

/* ───────── toast ───────── */
.toast { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-3) var(--space-4); border-radius: var(--radius-md);
  background: var(--text); color: var(--bg); font-size: var(--text-sm); box-shadow: var(--shadow-2); }
.toast a { color: inherit; font-weight: var(--weight-semibold); }

/* ───────── prose (plans, agent messages) ───────── */
.prose { font-size: var(--text-md); line-height: 1.6; max-width: var(--measure); overflow-wrap: break-word; }
.prose > * + * { margin-top: 0.85em; }
.prose h1 { font-size: var(--text-2xl); line-height: var(--lh-2xl); font-weight: var(--weight-semibold); }
.prose h2 { font-size: var(--text-xl); line-height: var(--lh-xl); font-weight: var(--weight-semibold); margin-top: 1.6em; }
.prose h3 { font-size: var(--text-lg); line-height: var(--lh-lg); font-weight: var(--weight-semibold); margin-top: 1.3em; }
.prose ul, .prose ol { padding-left: 1.3em; }
.prose li + li { margin-top: 0.3em; }
.prose code { background: var(--code-bg); padding: 0.1em 0.35em; border-radius: var(--radius-sm); }
.prose pre { background: var(--code-bg); padding: var(--space-3); border-radius: var(--radius-md); overflow-x: auto; font-size: 13px; line-height: 1.5; }
.prose pre code { background: none; padding: 0; font-size: inherit; }
.table-wrap { overflow-x: auto; border: 1px solid var(--border); border-radius: var(--radius-md); }
.prose table, .table { border-collapse: collapse; width: 100%; font-size: var(--text-sm); }
.prose th, .prose td, .table th, .table td { text-align: left; padding: var(--space-2) var(--space-3); border-bottom: 1px solid var(--border); vertical-align: top; }
.prose th, .table th { font-weight: var(--weight-semibold); background: var(--surface-2); white-space: nowrap; }
.prose tr:last-child td, .table tr:last-child td { border-bottom: 0; }

/* ───────── checker finding ───────── */
.finding { display: flex; gap: var(--space-3); padding: var(--space-3) var(--space-4); align-items: flex-start; }
.finding + .finding { border-top: 1px solid var(--border); }
.finding__body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; font-size: var(--text-sm); }
.finding__where { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-3); }

/* ───────── chat ───────── */
.thread { display: flex; flex-direction: column; gap: var(--space-5); }
.msg { display: flex; flex-direction: column; gap: var(--space-2); }
.msg__who { font-size: var(--text-xs); font-weight: var(--weight-semibold); color: var(--text-3); letter-spacing: 0.03em; }
.msg--owner { align-items: flex-end; }
.msg--owner .msg__bubble { background: var(--accent-soft); border-radius: var(--radius-lg) var(--radius-lg) var(--radius-sm) var(--radius-lg); padding: var(--space-3) var(--space-4); max-width: 88%; }
.msg--agent .prose { font-size: var(--text-md); }
.question { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-lg); padding: var(--space-4); display: flex; flex-direction: column; gap: var(--space-3); box-shadow: var(--shadow-1); }
.question__text { font-weight: var(--weight-semibold); }
.option { display: flex; flex-direction: column; align-items: flex-start; gap: 2px; width: 100%; min-height: var(--target); padding: var(--space-3) var(--space-4);
  border: 1px solid var(--border-input); border-radius: var(--radius-md); background: var(--surface); color: var(--text); font: inherit; text-align: left; cursor: pointer; }
.option:hover, .option.is-hover { background: var(--surface-2); }
.option--recommended { border: 2px solid var(--accent); padding: 11px 15px; }
.option__top { display: flex; align-items: center; gap: var(--space-2); width: 100%; font-weight: var(--weight-semibold); }
.option__mark { margin-left: auto; font-size: var(--text-xs); font-weight: var(--weight-semibold); color: var(--accent); white-space: nowrap; }
.option__desc { font-size: var(--text-sm); color: var(--text-2); }
.option.is-chosen { background: var(--accent-soft); border-color: var(--accent); }
.composer { position: sticky; bottom: 0; display: flex; gap: var(--space-2); align-items: flex-end; padding: var(--space-3) var(--space-4) var(--space-4);
  background: var(--bg); border-top: 1px solid var(--border); }
.composer .textarea { min-height: var(--target); height: var(--target); padding-top: 10px; resize: none; }
.working { display: inline-flex; align-items: center; gap: var(--space-2); font-size: var(--text-sm); color: var(--text-2); }
.working__dots { display: inline-flex; gap: 4px; }
.working__dots span { width: 6px; height: 6px; border-radius: 50%; background: var(--accent); animation: afk-pulse 1.2s infinite; }
.working__dots span:nth-child(2) { animation-delay: 0.2s; }
.working__dots span:nth-child(3) { animation-delay: 0.4s; }

/* ───────── layout: split view (desktop) ───────── */
.shell { display: flex; flex-direction: column; min-height: 100vh; }
.topnav { display: flex; align-items: center; gap: var(--space-5); padding: 0 var(--space-5); min-height: 60px; border-bottom: 1px solid var(--border); background: var(--bg); flex-wrap: wrap; }
.topnav__links { display: flex; gap: var(--space-1); flex-wrap: wrap; }
.topnav__link { display: inline-flex; align-items: center; gap: 6px; min-height: 40px; padding: 0 var(--space-3); border-radius: var(--radius-md); color: var(--text-2); text-decoration: none; font-weight: var(--weight-medium); }
.topnav__link:hover { background: var(--surface-2); color: var(--text); }
.topnav__link[aria-current="page"] { background: var(--surface-2); color: var(--text); }
.split { display: flex; flex-wrap: wrap; flex: 1; min-height: 0; }
.split__list { flex: 1 1 380px; max-width: 460px; border-right: 1px solid var(--border); padding: var(--space-5); display: flex; flex-direction: column; gap: var(--space-4); }
.split__detail { flex: 999 1 560px; min-width: 0; padding: var(--space-5) var(--space-6); display: flex; flex-direction: column; gap: var(--space-5); }
@media (max-width: 760px) { .split__list { max-width: none; border-right: 0; } }

/* ───────── phase-steps (planning phases) ───────── */
.steps { display: flex; flex-direction: column; gap: 0; padding: 0; margin: 0; list-style: none; }
.step { display: flex; align-items: center; gap: var(--space-3); min-height: 36px; font-size: var(--text-sm); color: var(--text-3); }
.step__dot { width: 12px; height: 12px; border-radius: 50%; border: 2px solid var(--border-input); flex: none; }
.step.is-done { color: var(--text-2); }
.step.is-done .step__dot { background: var(--ok); border-color: var(--ok); }
.step.is-current { color: var(--text); font-weight: var(--weight-semibold); }
.step.is-current .step__dot { background: var(--accent); border-color: var(--accent); box-shadow: 0 0 0 4px var(--accent-soft); }

/* app-bar on desktop: sits under topnav, not sticky, back button lines up with the 24px content edge */
.app-bar--desktop { position: static; min-height: 52px; padding: var(--space-1) var(--space-5) var(--space-1) var(--space-3); gap: var(--space-2); }
.app-bar--desktop .app-bar__title { flex: 0 1 auto; }
.app-bar--desktop .meta { margin-left: var(--space-2); }
```

---

## 3. Screens

Phone width 390px unless said. Every screen: `app-bar` on top, `page` below (16px gutter, 24px between
sections). Live data replaces in place; a changed row gets `is-changed` once.

### Overview (home) — T8.5, T8.15, T8.16
**Phone.** `app-bar`: `wordmark`, `live`, `icon-btn` "Add a project". `h1` names the count:
"3 projects need you" (or "Nothing needs you").
Section **Needs you**: `list` of `row row--needs-you`, each with `row__title` (project), state `badge`,
`row__sub` (current task `tid` and phase, or why nothing runs), `progress` "12 of 31", `row__meta`
"3 in inbox, 2 hold a task up". "Plan waiting for approval" rows link to the plan.
Section **Everything else**: same rows, ordered running, held, paused, finished.
Why nothing runs, in words: "Paused by you. Nothing new will start." · "Line stopped: deploy check failed
after T2.6." · "Waiting on you." · "Held for usage until 3:40 pm." · "Nothing ready: every pending task waits
on another." · "Finished."
Footer `btn-stack`: **Plan a new project** (primary), **Add an existing project** (secondary), **Remove a project** (quiet).
**Desktop.** `topnav` with Projects (`count`), Usage, `live`, Add a project, Plan a new project. `split`:
project `list` on the left (selected row `is-selected`), the selected Project on the right.

### Plan a new project — T8.15
One form that unfolds:
1. **Where**: `field` "Folder path or project name" (`input--mono`); hint "A name makes a new folder in ~/code."; errors in `field__error` ("afk already builds this folder…").
2. **Check the folder**: `card` "afk will create and plan in" + full `path` + badge "New, empty folder" / "Existing folder".
3. **GitHub**: `choice-list` "Create a private GitHub repository for it?" Yes, create darin/<name> / No, I'll add one later.
**Start planning** (primary, loading "Creating the folder and repository…"), **Cancel** (quiet). Then the Chat view.

### Add a project / remove a project — T8.16
Add: `field` Folder, `field` Name (optional, hint "Defaults to the folder name"), **Add project**.
Remove: `choice-list` of projects, **Remove…** (secondary) opens a `confirm-sheet`:
"Remove shed-inventory from afk?" — "afk stops watching it. These are not touched: the folder ~/code/shed-inventory, its plan, its sessions." Buttons **Remove** (danger) and **Keep it** (focused).

### Project — T8.6, T8.11, T8.14
State `card`: state `badge` + "Updated 12 s ago"; `h2` headline in words ("2 questions hold up T3.4");
one line of detail; `progress`; the one next action as primary (**Answer in the inbox**, **Resume**,
**Read and approve the plan**, or none).
**Running now**: `row` per running session (`tid`, title, phase and step, started) → Session.
**Pause project** / **Resume project** (secondary, confirm: "The running task finishes; nothing new starts.").
**Plan** row: "Planning: amend" + phase badge → Chat; or "Plan waiting for your approval" → Plan; or "Plan approved".
`nav-list`: Inbox (count), Tasks (12 of 31), Sessions (1 running), Usage (88%), Plan, Chat.
**Change the plan**: action rows Extend / Amend / Reconcile, each one line of plain explanation; they open the Chat.
`kv`: Folder, Repository.
**Desktop.** Right side of the Overview split: heading + badge + Pause in one line; Inbox card beside the
Build card and `nav-list`; plan actions as a `btn-row`.

### Inbox list and inbox item — T8.6
List: `notice--attention` "2 of 3 hold a task up"; `list` of rows, blocking first, each `badge--blocking`
+ `tid`, the question as `row__title`, `row__meta` source and age. New arrivals flash `is-changed`.
Empty: `empty` "Nothing needs you".
Item: `badge--blocking`, linked `tid` + task title, `h2` question, `prose` context, `meta` source.
**Your answer**: `option` buttons (recommended first and marked), `textarea` "Or write your own",
**Send answer** (primary; disabled until something is picked or written), then `toast`
"Answer sent. T3.4 is ready again." and back to the list. Owner tasks show the instruction and a single
**Mark as done** in place of options.

### Tasks — T8.7
`progress` for the whole plan; `segmented` Open / Done / All.
One `section` per milestone (title + "2 of 7 done"), `list` of task rows: `tid`, title, status `badge`,
`row__meta` "Waits for T3.4" for unmet dependencies.
Task: status `badge`, `h2` title, one line on why it is in that state and its dependencies.
**Tokens** `card`: total in `section-head` meta; `split-bar` + `legend` by phase (plan, build, review);
`table` by role (input, output). **Open its session**, **Set status…** → `confirm-sheet` with a
`choice-list` of statuses (ones afk sets itself disabled) and one sentence that changes with the choice:
"T3.4 will be marked done and T3.5 can start." Action button reads the choice ("Mark done").

### Usage — T8.8
Governor `card`: "What afk is doing" + `badge` (Run / Launch nothing new / Pause / Hold / Refuse) + one
sentence. **Usage windows** `card`: one `meter` per window (Session, Weekly, …) with used % and reset time
(warn ≥80%, full at 100%). **Refresh usage** (secondary; loading "Refreshing", then "Checked just now").
A `<details>` explains the governor's five decisions.

### Sessions list and session — T8.9, T8.10
List: rows with `tid` + task title, phase and progress, review verdict `badge`, "Waits for you" badge,
session id in `row__aside`. **Start a new autonomous session…** opens a `card` form (Name `input--mono`,
"What should it do?" `textarea`, **Start session**), confirmed before it starts.
Session: `tid` + status + verdict badges, `h2` title, `kv` (phase, started, label, copy path),
**Summary** in `prose`, **Actions** rows: Restart, Label, Clean (danger text). Each opens a
`confirm-sheet`, e.g. "Clean s-0405?" — "Its copy of the project is deleted. The summary and token counts stay." **Clean** (danger) / **Cancel** (focused).

### Plan and checker — T8.11
**Phone.** Checker `card` first: badge, `stats` (tasks, milestones, errors, warnings), `finding` list
(errors first), then **Approve the plan…**. With errors the button is absent and a `notice--error`
takes its place: "Fix 2 errors before approving. Ask the agent in the chat." Below, the plan as `prose`.
**Desktop.** `topnav`, then the back bar (back to the project, title "Plan · recipe-box"). `split`: checker on the left (sticky reading aid, findings link to their lines), plan on the
right at 68ch. Approve opens a centred `confirm-sheet`: "Approve the plan for recipe-box?" — "afk starts
building 18 tasks in 4 milestones, beginning with T1.1. The checker found 0 errors and 2 warnings."
**Not yet** (focused) / **Approve and start**.

### Chat view — T8.12, T8.14
The long screen. `app-bar` with title "Planning recipe-box" and a second line "New plan · Rounds, 2 of about 4", `live`.
`thread`: agent `msg` as full-width `prose` (headings, lists, code, tables scroll inside), owner `msg` as
bubbles, `question` cards with `option`s (recommended first and marked) and an own-answer `field`.
Picking an option sends it as the owner's reply and marks it `is-chosen`.
`composer` pinned to the bottom.
States: **agent working** — `live--working` in the bar, `working` line where the next message will appear,
composer still accepts typing, Send disabled until the agent is done. **No planning session** — `empty`
"No planning session" + one line, and Extend / Amend / Reconcile buttons.
**Desktop.** `topnav`, then the back bar (back to the project, title "Planning recipe-box", phase as meta). `split`: left column with `steps` (brief, rounds, slices, draft, check, review), folder and
repository; right, the thread centred at 720px with the composer under it. Options sit side by side.

### Sign-in prompt and errors
Sign-in: no app-bar; `wordmark`, `h1` "This page is private", one line, a labelled **Password** `field` (`type="password"`, `autocomplete="current-password"`; a wrong password puts `aria-invalid` on it and a `field__error` line under it), **Sign in** (primary), meta
"Not the owner? There is nothing else here." Nothing about projects is shown.
Bars: the phone `app-bar` and the desktop `topnav` end with a quiet **Sign out** button, a form that POSTs to `/signout`; it signs this browser out and lands on the sign-in prompt.
Error: `wordmark`, `h1` "That didn't load", one sentence naming what failed, mono status and path,
**Go to all projects** (primary), **Try again** (secondary). Lost connection is not a page: the `live`
indicator turns `live--offline` and the last data stays on screen.

### Updating by itself
- New data swaps in place; a changed row or card gets `is-changed` once (1.6s fade).
- `live` shows Live / Working / Offline at the top of every screen.
- Buttons that start work turn loading until the server answers, then a `toast` confirms.
- Regions that change get `aria-live="polite"` (thread, inbox list, project state card).
