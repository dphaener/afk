# Design follow-up 3 — Notifications page and app icon

Paste this into the afk design system in your design tool. Where the plan and `docs/design-system.md`
disagree, the plan wins on behaviour and the design system wins on looks; these are the points where
the plan's behaviour stands and the design has to follow it. Hand back the whole design system with
these changes made.

## 1. Two more states on the Notifications page's This device card

**What the design shows:** five states (On, Off, opened over `http://`, iPhone or iPad Safari not on the
home screen, blocked), decided in the order http, then iPhone/iPad, then blocked, then on or off.

**What the plan requires (T15.5):** two more states.

- **Unsupported**: the browser has no web push at all (an old browser, or one that leaves it out). It is
  decided after the http and iPhone/iPad checks and before blocked. There is nothing the owner can do on
  this device except use another browser or device.
- **JavaScript off**: the page cannot turn notifications on without JavaScript, so it says notifications
  need JavaScript. Nothing else on the site changes with JavaScript off.

**What to hand back:** both states in the This device state table (badge, sentence, any notice or
action) and the decision order written with "unsupported" in its place:
http, then iPhone/iPad not installed, then unsupported, then blocked, then on or off.

## 2. Which icons the manifest lists

**What the design shows:** in the App icon section, the manifest names "the three app icons"
(`icon-192.png`, `icon-512.png`, `icon-maskable-512.png`).

**What the plan requires (T15.2, already built):** the manifest lists every icon in
`internal/web/static/icons/`, with `purpose` `maskable` on the maskable one only.

**What to hand back:** the App icon section saying the manifest lists every icon in the folder, the
maskable one marked `maskable`.

## 3. What is served without sign-in

**What the design shows:** "The icons, the manifest and the service worker are served without sign-in;
nothing else is."

**What the plan requires (T15.2):** the sign-in form's two stylesheets are also served without sign-in,
as they have been since the sign-in page was built, because the sign-in page has to be styled before
anyone is signed in.

**What to hand back:** that sentence naming the sign-in form's stylesheets too.
