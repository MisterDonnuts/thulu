# Design rules for the dashboard

Every UI change in `web/` follows this file. COMPONENTS.md says where a class
or a component lives; this file says how a screen should behave and look. When
the two disagree, fix the one that is wrong in the same change.

## 1. Product rules

Thulu runs agents through a governed pipeline: a task moves across the
stages of a board, agents work it, people answer questions and approve. Each
surface has one kind of user and one main job.

| Surface | Who | Primary action |
| --- | --- | --- |
| `/tasks/` board + drawer | everyone in the workspace | create a task, move it, read what the agent did |
| `/inbox/` | the person a stage is waiting on | open the task and answer or approve |
| `/` dashboard | team lead | spot what is stuck or failing |
| `/projects/`, `/project/` | whoever connects repos | add a project, set its environment |
| `/agents/`, `/skills/`, `/automations/` | the person who configures agents | edit and save one definition |
| Settings rail (`/settings/`, `/mcps/`, `/credentials/`, `/sandboxes/`, `/usage/`, `/infra/`) | workspace admin, operator | change one setting and confirm it took |

- One primary action per screen. It is the only `btn-primary` in view and sits
  in the page head tray or the modal footer. Everything else is secondary,
  ghost or an icon button.
- Progressive disclosure. The first view shows what most people need; advanced
  fields go behind a "More options" disclosure, a drawer or a settings section.
  Never show an empty form section for a feature that is off.
- Destructive actions (delete, revoke, cancel a run, discard edits) go through
  `confirmDialog({ title, message, confirmLabel, destructive: true })`.
  The title names the object ("Delete board Payments?"), the message says what
  is lost, the button repeats the verb ("Delete board"). Where the daemon can
  restore the object, prefer acting at once and offering Undo in the toast for
  6 s over asking first.
- Never lose user input. Pollers must not clobber a region where focus sits in
  an editable, a modal is open or a drag is running (`dirty` in
  `lib/reactive.ts`; patch with `reconcile` or compare HTML before writing, as
  `inbox.astro` does with `data-sig`). Closing a modal or navigating away with
  unsaved edits asks first. A failed save keeps the form filled.
- Every async action shows three states. Pending: the trigger is disabled and
  says what it is doing ("Saving…"), or a row shows a spinner; no double
  submits. Success: the UI reflects the new state, plus a toast only when the
  change is not visible where the person is looking. Error: a message that says
  what failed and what to do, next to the thing that failed; toasts are for
  errors with no place on screen.
- A refresh that fails after a good load keeps the last data on screen and
  says it is stale (see `inbox.astro` `showError`). Blank screens are for the
  first load only.
- Empty states teach the next step: what this list holds, why it is empty, and
  the action that fills it ("No projects yet. Add a repository to start running
  tasks on it." + the button). "No data" alone is not an empty state.

## 2. UX/UI rules

- Hierarchy comes from size and position first, then weight, then colour. One
  `h1` per page (`PageHead`), panel titles are `h2`, nothing skips a level.
- Spacing uses `--s-*` only. The scale is 4-based: 4, 8, 12, 16, 24, 32, 48
  (`--s-1 --s-3 --s-4 --s-5 --s-7 --s-8 --s-9`). `--s-2` (6px) is for gaps
  inside a control; do not add new uses of `--s-6` (20px). No pixel literals for margin, padding or gap.
- Forms: a visible `<label>` for every control (`Field.astro`). Placeholders
  show an example value, never the label. Hints sit under the field in
  `--fg-3`. Validate on blur and on submit, show the error under the field in
  `.field-error`, link it with `aria-describedby`, and move focus to the first
  invalid field on submit. Required fields are marked, optional ones are not.
- Keyboard. Everything clickable is a `<button>` or `<a href>`; no click
  handlers on `div`/`span`. Tab order follows reading order; no positive
  `tabindex`. Modals trap focus, focus their first field on open and return
  focus to the trigger on close. Esc closes the topmost layer through
  `initKeyboard({ onEscape })` (never a second Esc listener). Menus and
  listboxes move with arrow keys and close on Esc. Global shortcuts live in
  `lib/keyboard.ts` and are listed in `KbdHelp.astro`.
- Focus is always visible: the global `:focus-visible` ring (2px accent).
  Never `outline: none` without a replacement of equal contrast.
- Hit targets: at least 32×32 px on desktop and 44×44 px under
  `(pointer: coarse)`. `.icon-btn` is 28px; new icon buttons use
  `.icon-btn-lg` or extend the hit area with padding or a `::before`, and
  every icon-only button has an `aria-label` from the catalog.
- Drag and drop always has two alternatives that do the same thing: a "Move
  to…" item in the card's menu (stage picker, same pattern as
  `lib/move-picker.ts`) and a keyboard path (focus the card, Space to pick up,
  arrows to move, Space to drop, Esc to cancel) with the result announced in an
  `aria-live="polite"` region.
- Status is never colour alone: pair the colour with an icon or a word
  (`badgeHTML` tones always carry text).
- Motion answers an action (open, expand, move, confirm) and lasts
  `--dur-fast` to `--dur-normal`. No entrance animations on page load, no
  looping motion besides `.dot-pulse` on live things. `tokens.css` zeroes every
  duration under `prefers-reduced-motion`; JS animations check
  `matchMedia("(prefers-reduced-motion: reduce)")` too.
- Contrast is WCAG AA in both themes: 4.5:1 for text under 18px, 3:1 for large
  text, icons that carry meaning, focus rings and control edges.
  `scripts/theme.test.mjs` measures the token pairs and fails the build when
  one drops below. The light theme passes every pair it checks: text on every
  surface, status and tag colours on their own tint, control edges, the focus
  ring, the `--viz-*` series and the stage colours from data. The dark theme
  still has these known misses:
  - `--fg-3` passes on `--surface-0` (4.9) and `--surface-1` (4.6) but fails on
    `--surface-2..4` (3.7 to 4.3). Secondary text inside popovers, modals,
    drawers and inputs uses `--fg-2`.
  - `--fg-4` and `--fg-5` are below 3:1 in both themes. Use them for disabled
    text and decoration only.
  - `--danger` text on `--surface-3` is 4.1; inside modals put it on
    `--danger-soft`, use `--color-danger-strong` or set it at 16px+.
  - `--line` and `--line-strong` are dividers (about 1.2:1). A text field's
    edge is `--color-field-border`, which is `--color-border-control` in both
    themes: 3:1 in light, 3.1:1 on `--surface-3` in dark. Any other
    control edge starts from `--color-border-control` too.
- Text wraps at 80 characters or less in prose (subtitles, descriptions,
  empty states). Tables may run wider; truncate cells with `.truncate` and a
  `title` holding the full value.
- Layouts work from 390px wide up. Below 900px the sidebar is an overlay and
  page heads wrap; tables either scroll inside `.table-wrap` or collapse to
  rows. No horizontal page scroll.

## 3. Aesthetic: stay in family

The direction in `tokens.css` is "Quiet workspace": a calm, dense tool for
people who keep it open all day. Redesigns refine it; they do not replace it.

- Type: Inter Variable for UI (with `ss01`, `cv11`, `tnum` on, so numbers line
  up in tables), JetBrains Mono for ids, commands, branch names, paths, costs in
  tables and code. Mono marks machine values, never decoration or labels.
  Base size 13px; page title `--t-xl`/`--t-2xl` at weight 600 with
  `--tracking-tight`; weights stay at 400, 500 and 600.
- Surfaces: near-neutral greys stepping up by elevation (`--surface-0` page,
  `-1` panel, `-2` hover and popover, `-3` modal and drawer, `-4` inputs and
  code). Depth comes from the surface step plus a 1px `--line` border.
- Borders over shadows. Panels and cards have a border and no shadow. Shadows
  belong to things that float above the page: modal, popover, menu, toast.
- One accent, `--accent` (soft blue), for the primary button, links, focus,
  selection and the active nav item. Never for decoration, headings or large
  fills. `--accent-soft` marks a selected row or chip.
- Semantic colours are muted (`--ok`, `--warn`, `--danger`, `--info`) and appear
  as small text, a dot or a `-soft` badge background, never as a full panel
  fill. Charts use `--viz-1..8` in order.
- Radii by size: `--r-sm` 4px for badges, small buttons and kbd; `--r-md` 6px
  for buttons and inputs; `--r-lg` 8px for panels, cards and modals;
  `--r-pill` for chips and status pills. No other values.
- Density: rows 32 to 40px tall, panel heads `--s-4 --s-5`, tables with header
  bands. Whitespace separates groups; dividers are for lists.
- Icons: the inline sprite in `Layout.astro` (24px viewBox, stroke
  `--icon-stroke` 1.5px, round caps, `currentColor`), drawn through
  `Icon.astro` or `icoSvg()`. Sizes 16/20/24 via `.ico`, `.ico-lg`, `.ico-xl`.
  A new glyph is a new `<symbol>` in the same style; no icon fonts, no emoji, no
  filled icons next to stroked ones.
- Copy is part of the look: sentence case, verbs on buttons ("Save changes",
  "Add project"), the same word through a flow (the "Publish" button yields
  "Published"). No ALL-CAPS labels beyond the sidebar group headings that
  exist, no "→" on buttons, no middle-dot meta strings.

## 4. Tokens

`tokens.css` has three layers. The theme layer names every colour, shadow and
glow by role; it is the only block a theme redefines. The scale layer (type,
space, radii, motion, z-order, fonts) is the same in every theme. The alias
layer points the short names at the theme layer, so a theme reaches every page
that uses them.

Two themes, dark and light. Dark is the base `:root` block. Light redefines
the whole theme layer twice with the same body: under
`:root[data-theme="light"]` (an explicit choice) and under
`@media (prefers-color-scheme: light)` for `:root:not([data-theme="dark"])`
(System on a light OS). `scripts/theme.test.mjs` fails when the two bodies
differ or when light misses a name dark defines. How the choice reaches
`<html>` is in section 5, "Theme".

- Colour and depth, by role: `--color-bg`, `--color-surface`, `-surface-hover`,
  `-surface-raised`, `-surface-strong`, `-surface-glass`; `--color-fill-chip`
  for a badge, pill or chip on a panel (light's raised surface is white like
  the panel, so a chip can't borrow it); `--color-well` and
  `-well-strong` for sunken logs and media; `--color-tint-1..4` for
  washes over any surface (white in dark, black in light: hover, pressed,
  selected) and `--color-shade` for a dark wash over a
  coloured chip; `--color-scrim*`; `--color-text`, `-text-secondary`,
  `-text-muted`, `-text-faint`, `-text-disabled`, `-text-on-accent`;
  `--color-border`, `-border-strong`, `-border-subtle`, `-border-hover`,
  `-border-control`; `--color-accent`, `-accent-hover`, `-accent-faint`,
  `-accent-soft`, `-accent-border`; status triplets `--color-success|warning|
  danger` with `-bg` and `-border` (plus `--color-danger-strong` for small
  danger text on a tinted or raised background), `--color-info`,
  `--color-special*` (agent purple), `--color-notice*` (yellow);
  `--shadow-1`, `-2`, `-popover`, `-popover-lg`, `-toast`, `-drag`, `-modal`,
  `-drawer-start`, `-drawer-end`, `-on-media`; `--glow-*` on status dots
  (`none` in light); `--color-field`, `-field-border`, `-field-focus-border`
  for text fields; `--mix-data-ink`, how much of a colour from data a label
  keeps before the rest turns into `--color-text` (65% in dark, 45% in light);
  `--tag-<colour>-bg|fg` for the `.nc-*` chips; `--brand-*`; `--viz-1..8` plus
  `--viz-grid`, `-axis`, `-area`, `-point-ring`.
- The short aliases stay valid and are what most pages use: `--surface-0..4`,
  `--fg`, `--fg-2..5`, `--on-brand`, `--line*`, `--accent*`, `--ok*`,
  `--warn*`, `--danger*`, `--info`. Reach for a `--color-*` name when no alias
  covers the role.
- Scale: `--t-*`, `--lh-*`, `--tracking-*`, `--s-*`, `--r-*`, `--dur-*`,
  `--ease-*`, `--z-*`, `--font-*`, `--header-h`.
- No raw colours (`#hex`, `rgb()`, `rgba()`, named colours) in `.astro`,
  `lib/*.ts` or any stylesheet other than `tokens.css`. A missing value
  becomes a new token in the theme layer, defined for both themes in the same
  change. That includes hover shades, overlay scrims, shadows and gradients.
  A one-off alpha of a token is `color-mix(in srgb, var(--color-x) N%,
  transparent)`, never a new `rgba()`.
- A colour that comes from data (a stage or task-type colour) reaches CSS
  through a custom property: `style="--dot-color:…"` on `.dot`,
  `--pill-color` on `.stage-pill`, `--chip-color` on the dashboard's
  `.stage-chip`. The class holds the fallback.
- SVG drawn from a string cannot put `var()` in a presentation attribute
  (`fill="…"`); give the element a class and set `fill`/`stroke` in CSS, as
  the usage chart does.
- New code never uses the compatibility aliases at the bottom of `tokens.css`
  (`--bg`, `--card`, `--text`, `--blue`, `--radius`…).
- Inline `style=""` is allowed only to set a custom property
  (`style="--page-max: 1100px"`), never a colour or a size.

## 5. Astro flows

**Static build inside the daemon.** `astro build` writes `web/dist`, which
`embed.go` compiles into the Go binary; a web change ships only after a Go
build. There is no server rendering and no dynamic route. One page per screen;
per-object state rides in the query or the hash (`/tasks/?board=x#task-123`,
`/settings/#boards`). Page links end in `/`: `/inbox/` is the page, `/inbox`
is the API.

**ClientRouter.** `Layout.astro` mounts `<ClientRouter />`. The header, the
sidebar, the confirm dialog and the chat drawer carry
`transition:persist="<name>"` and survive every navigation. Anything else that
must survive gets its own unique persist name. The router replaces
`<html>`'s attributes with the incoming page's, so state kept there
(`data-sidebar`, `data-sb-mode`) is copied onto
`e.newDocument.documentElement` in `astro:before-swap`, as `Sidebar.astro`
does; `public/boot/theme.js` does the same for `data-theme`. Navigate from
code with `navigate()` from `astro:transitions/client`, not `location.href`,
except to force a full reload.

**Page lifecycle.** All page code runs inside
`onPage("/path", (signal) => { … })` from `lib/page.ts`. The body runs on
every showing of the page; `signal` aborts on swap. Anything that outlives the
page's own elements stops with it: `addEventListener(…, { signal })` on
`window`/`document`, `every(ms, fn, signal)` instead of `setInterval`,
`startPoller` (bound to `pageSignal()`), `events.subscribe` unsubscribed on
abort, `fetch(…, { signal })`. Persisted components (sidebar, drawer) re-read
the location on `astro:page-load` instead.

**No inline scripts.** The CSP is `script-src 'self'`. Each page has bundled
`<script>` blocks (processed by Astro, emitted as files because
`assetsInlineLimit: 0`). Never `is:inline`, `define:vars`, `on*=` attributes
or `javascript:` URLs; the `octopus:no-inline-script` integration fails the
build if one gets through. The only classic scripts are files in
`public/boot/`, loaded with `<script is:inline src="/boot/…">` in the head.
`style-src-elem 'self'` also refuses `<style is:inline>`.

**Runtime markup.** Most rows are built with `innerHTML` strings in `lib/`.
Every interpolated value goes through `esc()`, URLs through `lib/safe-url.ts`,
and the classes they use live in the global layers (COMPONENTS.md, "two-layer
rule"). `set:html` is banned by lint. An element with a `display` rule also
needs `.x[hidden] { display: none }`, or `hidden` does nothing.

**Transitions.** Astro's default cross-fade on swap is enough for most links.
Use `transition:name` only for one element that is the same object on both
pages (a board title in the sidebar and in the board head, a task card and its
full view). Names are unique on a page; for markup built at runtime set
`el.style.viewTransitionName` with the object id. Custom animations stay
under `--dur-normal`, and Astro drops them under reduced motion.

**Prefetch.** With the ClientRouter, Astro prefetches every link on hover.
Keep that default; pages are small static files. Add
`data-astro-prefetch="false"` to links that download, export or log out, and
`data-astro-reload` to links that must do a full load (locale switch, sign
out, auth flows). Legal pages and `auth/oidc/done` sit outside the router and
load fully on their own.

**Theme.** System, Light or Dark, picked in the account menu and stored per
browser in localStorage `octopus.theme` by `lib/theme.ts` (no key means
System). `public/boot/theme.js`, a classic script in the head of Layout,
LegalLayout, login and the `/qa/` redirect, applies it before the first
paint: `data-theme` on `<html>` for an explicit choice and no attribute for
System, `<meta name="color-scheme">` narrowed to the choice, and
`theme-color` (the address bar) set to the resolved theme's `--color-bg`. The same script writes the
theme onto the incoming document in `astro:before-swap`, applies it again on
`astro:after-swap`, and follows the OS and other tabs while the page is open,
so a navigation never flashes the other theme. Components never branch on
the theme; they read tokens. Check every new screen in both themes before
calling it done.

**i18n.** Every string a person can see or hear goes through
`t("namespace.key", { vars })` with a literal key: template text, `title`,
`aria-label`, `placeholder`, `alt`, toasts, confirm text, thrown errors.
English (`src/i18n/locales/en`) is the source; `es` has the same keys and the
same variables, written in rioplatense Spanish with voseo ("Abrí la tarea").
Plurals are `{ "one": …, "other": … }` picked by `count`. A sentence that holds
markup uses `tParts(key, { name: SLOT })` and the page renders the element.
Numbers and dates go through `lib/format.ts` and `lib/time.ts` (Intl with the
build's locale). `// i18n-ignore` is only for strings no person reads (CSS,
ids, protocol values). `scripts/i18n-guard.test.mjs` fails on a missing key,
an unused key, a locale without a key, or bare text in a template.

**Checks.** Run `make check` (eslint, prettier, vitest with the i18n and
bootstrap guards, astro check, build). A change is done when the checks pass and you
have looked at screenshots of the screen in dark and light, at 1440×900 and
390×844, including its empty, loading and error states.
