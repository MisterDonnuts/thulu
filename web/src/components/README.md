# Component catalog

Which piece to reach for when you build dashboard UI, and where each one lives.
`web/COMPONENTS.md` holds the rules behind it (where a style lives and why);
this file is the current list.

## One component, three forms

Every component exists in up to three forms, and all of them produce the same
HTML:

- **Astro component** in `src/components/<Name>.astro`, for markup written in a
  template. Markup only: no `<style>` of its own.
- **TypeScript function** in `src/lib/ui/<family>.ts`, re-exported from
  `src/lib/ui.ts`, for markup a script builds as a string (pollers, drawers,
  anything drawn with `innerHTML`). It escapes every value with `esc()` from
  `lib/ui/dom.ts`.
- **CSS classes** in `src/styles/primitives/<family>.css`. `styles/primitives.css`
  imports every family sheet into the `octopus.primitives` layer, in a fixed
  order.

The family name is the same in the three places: `button` is
`lib/ui/button.ts` and `styles/primitives/button.css`.

## Rules

- **A page never redefines a primitive.** A page, a component or an area sheet
  (`board.css`, `activity.css`…) may place a primitive (margin, flex, grid
  placement, width, position, `display: none`) but not restyle it. If it needs
  to look different, add a variant to the family's sheet, its Astro prop and
  its TS option. `scripts/primitives-guard.test.mjs` enforces this in
  `npm test`.
- **Hide with `hidden`.** `base.css` gives `[hidden]` `display: none !important`,
  so any element hides with the attribute whatever its class sets. Do not write
  `X[hidden]` rules or toggle `style.display`.
- **Tokens only.** Colours, radii, spacing, type sizes, shadows and durations
  come from `styles/tokens.css`. Both themes must work.
- **Accessible by default.** An icon-only button has an `aria-label`; a menu,
  tab strip or modal handles the keyboard (Escape, arrows, focus return); a
  label is tied to its control; nothing depends on colour or a tooltip alone.

## Catalog

Status: **ready** means the three forms exist or the piece is complete as it
is; **partial** means some forms or variants are missing.

### Actions

| Family   | Astro                                                                                                          | TypeScript                                                                                                                             | CSS classes                                                                                                                                                                          | Use it for                                                                                                                                                                                                                                                                           | Status |
| -------- | -------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------ |
| `button` | `Button.astro` (`variant`, `size`, `icon`, `iconOnly`, `label`, `kbd`, `href`, `disabled`)                     | `buttonHTML()`, `buttonEl()` for a live element; `buttonClasses()` and `buttonA11y()` shared with the component, in `lib/ui/button.ts` | `.btn`, `.btn-primary`, `.btn-ghost`, `.btn-danger`, `.btn-link`, `.btn-sm`, `.btn-md`; `.icon-btn`, `.icon-btn-xs`, `-sm`, `-lg`, `.icon-btn-ghost`, `.icon-btn-danger`; `.is-busy` | Every button and every link that looks like one. One primary per view. `iconOnly` for a glyph alone; its `label` becomes `aria-label` and the tooltip.                                                                                                                               | ready  |
| `icon`   | `Icon.astro` (`name`, `size`, `label`)                                                                         | `icoSvg(name, { size, cls, label })`; `iconHref()`, `iconClasses()` in `lib/ui/icon.ts`                                                | `.ico`, `.ico-sm`, `.ico-lg`, `.ico-xl` (in `base.css`)                                                                                                                              | A glyph from the sprite Layout inlines. Decorative (`aria-hidden`) unless it gets a `label`. Never an inline `<svg>` path.                                                                                                                                                           | ready  |
| `kbd`    | `Kbd.astro` (`keys`, `size`, `decorative`, `align`, `inset`); Button's `kbd` prop; `KbdHelp.astro` (the sheet) | `kbdHTML(keys, { size, align, inset, cls, decorative })`, `kbdGroupClasses()`, `keyShortcuts()` in `lib/ui/kbd.ts`                     | bare `kbd`, `.kbd-sm`, `.kbd-group`, `.kbd-group-end`, `.kbd-inset`; the sheet's `.kbd-grid`, `.kbd-label`, `.kbd-sep`                                                               | A keyboard hint: one key, or a chord as a list (one cap per key). In a button the cap is hidden from screen readers and named by `aria-keyshortcuts`. `inset` puts a cap inside a field (the search box's "/"); `align="end"` sets a group against the label in the shortcuts sheet. | ready  |

Notes on kbd:

- **Platform keys.** ⌘ and ⌥ are written once, as `MOD_KEY` and `ALT_KEY`
  in `lib/shortcuts.ts`, and every cap and sentence that shows them goes
  through it: `kbdHTML()`, `Kbd.astro`, Button's and Menu's `kbd`, and
  `chordText()` for a shortcut inside a sentence ("Press Ctrl+C"). Off
  Apple platforms they read Ctrl and Alt. A hint such as `"⌘S"` is split
  into two caps so the modifier can change; `keyShortcuts()` names both
  Meta and Control for it. Markup rendered at build time says ⌘ with
  `data-mod-key`, and Layout's `applyPlatformKeys()` relabels it on each
  page-load. `scripts/platform-keys-guard.test.mjs` fails on a ⌘, ⌥ or Ctrl
  typed anywhere else, catalogs included.

Notes on the actions row:

- **Sizes line up.** A default `.btn` is 32px, as tall as an
  `.icon-btn-lg`; a `.btn-sm` is 26px, as tall as an `.icon-btn-sm` and a
  `.select-sm`. The default `.icon-btn` is 28px (panel heads, table rows) and
  `.icon-btn-xs` 20px (sidebar, with a 24px target). The heights are the
  `--control-h-*` tokens.
- **Icon-only variants.** `ghost` has no edge until hovered (menu triggers on
  cards and column heads), `danger` stays grey until hovered, `primary` is the
  same `.btn-primary` fill.
- **Link.** `variant="link"` is an action set in a sentence (the agent
  editor's checklist: "Go to Settings"): accent text, underlined, no box,
  at the size of the words around it. Text only, default size only; with
  `href` it is a page link that reads the same.
- **Text danger.** A text `.btn-danger` has a red edge at rest, so beside a
  bordered Cancel the destructive choice does not read as a link.
- **States.** `aria-expanded="true"` on a menu trigger (one with
  `aria-haspopup`) looks hovered while its menu is open; a disclosure such as
  the sidebar toggle keeps its resting look,
  `aria-pressed="true"` is a toggle that is on, `disabled` (or
  `aria-disabled` on a link, which loses its `href`) fades the button,
  and `is-busy` turns its glyph while the work it started runs (the refresh
  and Re-check of Credentials); the script sets `aria-busy` with it.
- **Touch.** Where the pointer is coarse, every `.btn` and `.icon-btn` keeps
  its size and takes a tap within `--tap-min` (44px) of its centre through an
  invisible `::before`. A page needs no 44px rule of its own.
- **Built with the DOM.** `buttonEl()` is `buttonHTML()` parsed into an
  element, for code that binds a handler before the node reaches the page.
- **Wrong combinations throw.** A text button with `size="lg"`, or an
  icon-only one without `label` or `icon`, fails the build in Astro and throws
  in `buttonHTML()`.
- **Markup after the label.** What Button holds in its slot beside the text
  (a count, a badge the page updates in place) is `trailingHTML` in
  `buttonHTML()`, written as is between the label and the hint: escape what
  goes into it.
- **Hint on an off primary.** A disabled primary loses its fill, and its
  `kbd` goes back to the plain cap.
- **Parity.** `src/components/parity.test.ts` renders Button, Icon and Kbd with
  the Astro container and compares them with their twins.
- **Caps in place.** The search box's cap is a `Kbd inset`, which fades
  once its field has focus or words; the shortcuts sheet's keys are a
  `Kbd align="end"` group, and the sheet's grid lives in `kbd.css`.
- **The sprite.** `IconSprite.astro` holds every symbol. Layout writes it on
  each page; login, which has no Layout, writes it itself.

### Status and labels

| Family     | Astro                                                                                                                                                                                     | TypeScript                                                                                                                                                                                                                          | CSS classes                                                                                                                                                                                                                                                                    | Use it for                                                                                                                                                                                                                                                                                                                                                                                                   | Status |
| ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------ |
| `badge`    | `Badge.astro` (`tone`, `shape`, `size`, `mono`, `edge`, `raised`, `icon`, `spin`, `dot`, `pulse`, `title`); `Dot.astro` (`tone`, `pulse`, `label`)                                        | `badgeHTML(label, tone, opts)`, `setBadge()`, `dotHTML()`, `statusGlyph()`, `badgeClasses()`, `dotClasses()` in `lib/ui/badge.ts`; status tables and `statusBadges()` in `lib/badges.ts`                                            | `.badge`, `.badge-ok`, `-warn`, `-danger`, `-muted`, `-accent`, `-info`, `-pill`, `-sm`, `-mono`, `-edge`, `-dashed`, `-hollow`, `-raised`, `-spin` (on the glyph); `.dot`, `.dot-ok`, `-warn`, `-danger`, `-accent`, `-info`, `-special`, `-empty`, `-pulse` (or `data-tone`) | The state of a thing: running, failed, disabled, default. Not clickable. A dot beside a word.                                                                                                                                                                                                                                                                                                                | ready  |
| `count`    | `Count.astro` (`n`, `pill`, `tone`, `size`, `label`, `srLabel`, `dot`, `pulse`, `max`, `hideZero`)                                                                                        | `countHTML(n, opts)`, `setCount()`, `countText()`, `countClasses()` in `lib/ui/count.ts`                                                                                                                                            | `.count`; `.count-pill`, `.count-sm`, `.count-accent`, `-warn`, `-danger`                                                                                                                                                                                                      | A number beside what it counts: bare in titles, columns and filters; a pill in tabs, the sidebar and heads.                                                                                                                                                                                                                                                                                                  | ready  |
| `cost`     | none                                                                                                                                                                                      | `costHTML()` and `estimatedCost()` in `lib/format.ts`                                                                                                                                                                               | `.est`, `.unpriced`                                                                                                                                                                                                                                                            | An amount of money that may be partly estimated or missing a price.                                                                                                                                                                                                                                                                                                                                          | ready  |
| `tag`      | `Tag.astro` (`label`, `color`, `tone`, `size`, `dot`, `icon`, `inline`, `dashed`, `mono`, `href`, `button`, `pressed`, `removable`)                                                       | `tagHTML()`, `chipColor()`, `priorityTagHTML()` in `lib/ui/tag.ts`; `stagePillHTML()` in `lib/stages.ts`; `labelTagHTML()`, `repoChipHTML()`, `agentChipHTML()` in `lib/chips.ts`; `taskPriorityTagHTML()`, `casePriorityTagHTML()` | `.tag`, `.tag-sm`, `.tag-lg`, `.tag-tint`, `.tag-<bucket>`, `.tag-muted`, `-special`, `-warn`, `-danger`, `.tag-inline`, `.tag-dashed`, `.tag-mono`, `.tag-dot`, `.tag-x`                                                                                                      | A data value with a colour: stage, label, repo, agent, priority. Not a state (that is `badge`) and not a filter toggle (that is `.chip`).                                                                                                                                                                                                                                                                    | ready  |
| `callout`  | `Callout.astro` (notes below)                                                                                                                                                             | `calloutHTML()`, `calloutEl()` in `lib/ui/callout.ts`                                                                                                                                                                               | `.callout`, `.callout-info`, `-accent`, `-warn`, `-danger`, `-ok`, `-special`, `-band`; `.text-warn`, `-hint`, `.text-danger`                                                                                                                                                  | A block that explains a whole form, section or drawer.                                                                                                                                                                                                                                                                                                                                                       | ready  |
| `toast`    | `ToastStack.astro`                                                                                                                                                                        | `makeToast(ms)` (kinds `ok`, `err`, `warn`, `info`), `toastClasses()` in `lib/ui/toast.ts`                                                                                                                                          | `.toast-stack`, `.toast`, `.toast.ok`, `.toast.err`, `.toast.warn`, `.toast-link`, `.toast-action`                                                                                                                                                                             | Feedback after an action. Layout writes the one `ToastStack`; pages add none. `err` is announced as an alert. Never to report a field the reader must fix: mark the field.                                                                                                                                                                                                                                   | ready  |
| `progress` | `ProgressBar.astro` (`segments`, `total`, `size`, `split`, `label`, `decorative`); `Meter.astro` (`value`, `label`/`labelledBy`, `valueText`, `tone`, `thresholds`, `size`, `inline`)     | `progressBarHTML(segments, opts)`, `meterHTML(value, opts)`, `setMeter()`, `meterTone()`, `progressHTML({ done, total, segments }, { text })`, `progressText()`, `progressTextHTML(p, { text, id })` in `lib/ui/progress.ts`        | `.seg-progress`, `-xs`, `-md`, `-split`, `.seg-progress-seg[data-tone]`, `.seg-progress-outline`; `.meter-bar`, `-xs`, `-md`, `-inline`, `.meter-bar-fill`; `.progress-line`, `.progress-line-text`. `.st-bar`                                                                 | Segmented: the share of each state in a set (cases, subtasks, a batch) or of each area in a total (tones `viz-1`…`viz-8`). Meter: one percentage, `role=meter`, needs a name; `thresholds` turns it warn at 75% and danger at 90%. `progressHTML` writes the bar with the one "1 of 4" text; `progressTextHTML` writes that text alone (a section head's aside), or empty with an `id` for a script to fill. | ready  |
| `skeleton` | `Skeleton.astro` (notes below)                                                                                                                                                            | `skeletonHTML()` in `lib/ui/skeleton.ts`                                                                                                                                                                                            | `.skeleton`, `.skeleton-line`, `-block`, `-chip`; `.skeleton-lines`                                                                                                                                                                                                            | The shape of a known layout while it loads.                                                                                                                                                                                                                                                                                                                                                                  | ready  |
| `empty`    | `EmptyState.astro` (`size`, `tone`, `dashed`, `icon`, `title`, `titleTag`, `steps`, `loading`, `as`; body in the slot, `action` slot); `EmptyRow.astro` (`as`, `cols`, `loading`, `tone`) | `emptyStateHTML()`, `emptyRowHTML()`, `emptyStepsHTML()`; `emptyStateClasses()`, `emptyRowClasses()`, `emptyA11y()` shared with the components, in `lib/ui/empty.ts`                                                                | `.empty-state`, `-sm`, `-inline`, `-dashed`, `-danger`, `.empty-state-icon`, `-text`, `-title`, `-body`, `-steps`, `-step`, `-action`; `.row-empty`, `.row-empty-danger`                                                                                                       | Loading, nothing-here and failed-load states: EmptyState for a block with a glyph, title or action (with `steps`, a numbered first-run guide, block size only), EmptyRow for the bare line of a list or table.                                                                                                                                                                                               | ready  |

Notes on badges and counts:

- **Which tone.** `ok`, `warn` and `danger` are the status colours.
  `muted` is a state with nothing to report (disabled, never checked).
  `accent` is the purple of what is special or the default ("Default",
  "Update", "Cloning"): one tone for "Default" everywhere. `info` is
  something under way (running, queued, live). A dot's `accent` is the blue
  one of the live marker and its `special` the purple of a run that broke;
  inside a badge a dot without a tone takes the badge's colour.
- **Which shape.** `rect` (the default) for a state in a table, a form or a
  panel head. `pill` for a run's state on a card, a drawer or a case. `size="sm"` (16px) for dense lists
  such as the stages on an agent row; `mono` for ids, versions, branches.
- **Edges.** `solid` outlines in the tone's line colour; `dashed` reads as
  "not from here" (an MCP configured by environment); `hollow` drops the fill,
  for a state that must not look like one with a colour (skipped, not run).
- **Raised.** `raised` lays the tint on an opaque panel with a shadow, for a
  pill over a picture (the poster of a case card), so the word reads on any
  screenshot behind it.
- **Glyphs.** `icon` wins over `dot`; `pulse` alone is a breathing dot. `spin`
  turns the icon while the work runs (the CI chip of a pull request).
  `statusGlyph(icon)` is the icon-or-breathing-dot in front of a run's word.
- **A status per table.** `statusBadges({ status: { tone, label, icon } },
look)` in `lib/badges.ts` builds the function a domain needs (sandbox, MCP,
  automation run, credential); a status it does not know shows as sent.
  Labels come from the catalog.
- **Live.** `setBadge(el, label, tone, opts)` and `setCount(el, n, opts)`
  repaint an element already on the page, classes included: never assign
  `className` by hand (that is how "badge badge" happened). Pass the page's
  placement classes in `cls`.
- **Counts.** Bare (`.count`) beside a title, a column head or a filter;
  `pill` for tabs (md, 18px), the sidebar (sm, 16px, `tone="accent"`, `max`,
  `hideZero`) and the head of a page or panel (`label` makes the number bold
  before its words, `dot` puts a status before it). A count that stands alone
  gets `srLabel`, the words only a screen reader hears. Inside a pressed
  chip the bare count takes the chip's colour.
- **Dots.** Never the only sign of a state. A dot beside a word is
  decorative; one that stands alone takes `label`, its accessible name and
  tooltip. Scripts that retone a dot in place can set `data-tone` instead of
  the class.
- **Parity.** `parity.test.ts` renders Badge, Dot and Count with the Astro
  container and compares them with `badgeHTML()`, `dotHTML()` and
  `countHTML()`.
- **One pill.** Every count pill is `Count pill`. The tasks area draws its run states with `runPillHTML()`, and every
  area draws its counts with Count.

Notes on the tag row:

- **One shape.** A tag is a pill whatever holds it; the card takes
  `size: "sm"` instead of a page rule that shrinks `.tag`, and a card whose
  name is a tag (a project's) takes `size: "lg"`. Inside, the label
  is `.tag-label`, which cuts a long value with an ellipsis; pass the full
  value as `title` where that can happen (repoChipHTML() does).
- **Colour, four ways.** Grey by default; `color` takes a bucket from
  `chipColor()` (labels) or a `#hex` from data (a stage's or a column's
  colour, written as `--tag-color` and checked by `isHexColor()`, so nothing
  else reaches the style attribute); `tone` is `muted`, `special` (agents),
  `warn` or `danger`. A colour and a tone together throw.
- **Dot.** `dot: true` paints the dot in the tag's colour (a stage); a bucket
  or a `#hex` gives a grey tag a dot of its own (a repo).
- **Inline.** `inline` drops the pill and keeps the glyph and the word, for a
  line of meta: the card's agent, a priority beside a case key. An inline
  agent has a violet glyph and grey text.
- **Interactive.** `href` makes an `<a>`, `button` a `<button type="button">`
  (the drawer head's stage that opens the picker), `pressed` a picker
  option with `aria-pressed` and a check that shows when it is on. A picker
  option off is the plain tag at full strength, never faded.
- **Removable.** `removable` adds an X button named "Remove <label>"
  (`ui.tag.remove`) or the `label` given, with the `attrs` the page's
  handler reads. A removable link or button throws: no control inside
  another.
- **Priority.** Tasks (urgent…low) and test cases (critical…low) share one
  scale: `priorityTagHTML(label, level)` gives each level its glyph and tone,
  and the word is always there.
- **Parity.** `src/components/parity.test.ts` renders Tag with the Astro
  container and compares it with `tagHTML()`.
- **Every chip is a tag.** The tasks area writes
  stages, repos, agents, labels, types and priorities as tags
  (`typeTagHTML()` in `lib/board.ts`, `sourceTagHTML()` in
  `lib/activity-html.ts`); the card's urgent and high glyph (`.prio` in
  `lib/task-card.ts`) stays an icon.

Notes on callout and skeleton:

- **Callout parts.** `Callout.astro` takes `tone` (`info` by default, `accent`,
  `warn`, `danger`, `ok`, `special`), `icon`, `title`, `band`, `bare` and `detail`
  (`{ summary, text }`, a folded `<pre>` for a log or a raw error); its default
  slot is the text and its `actions` slot holds sm buttons. `calloutHTML()`
  takes the same plus `body` (escaped) or `bodyHTML` (written as is) and
  `actionsHTML`. Both get their classes from `calloutClasses()`, and a tone
  that does not exist throws. A body of several paragraphs writes each as
  a `<p>`, and the sheet keeps them apart.
- **Which tone.** `info` is a neutral note, `accent` something in progress or a
  proposal to accept, `warn` what needs attention without having failed (a
  read-only reason, a stale page), `danger` what failed, `ok` what finished,
  `special` a run that broke rather than failed (Evidence's "couldn't run",
  the same purple as its badge and dot).
  The glyph and the title carry the colour; the text stays `--fg-2`. A
  callout says what it is in words or with a glyph, never by colour alone.
- **Band.** `band` puts the callout edge to edge with a rule underneath and no
  radius: the attention strip of a drawer, a notice inside a panel without
  padding. Its rows are one line, so glyph, text and button centre.
- **Narrow.** The actions drop under the text when the text would get less
  than about 240px; a page needs no 390px rule of its own.
- **Live regions.** A callout a script shows after load gets `role="status"`
  through `attrs` (or the Astro props); a failure the person must act on,
  `role="alert"`.
- **Bare.** `bare` writes the box and its text only, as a `<p>` a script
  fills with `textContent`: no glyph, title, detail or actions (giving one
  throws). A bare danger callout keeps its text red. `FormError` and
  `formErrorHTML()` write one with the `form-error` hook, and the read-only
  note (`showReadOnly()` in `lib/operator.ts`, the board and the comments in
  `tasks.astro`) is a bare warn one with the `readonly-note` hook; scripts
  and tests still find both by those classes, which carry no look of their
  own beyond the note's bottom margin.
- **The loose line.** `.text-warn` is amber text without a box, for a hint
  that warns.
  `.text-warn-hint` adds the size of a control's help line (`--t-xs`), for
  a warning that sits under a control instead of inside a sentence.
  `.text-danger` is the red one, for a line that says something failed (the
  provider's refusal under an AI engine profile in Settings).
- **Login.** `primitives/callout.css` needs only `tokens.css` and `base.css`,
  so `login.astro` can import it into `octopus.primitives` like it does
  `button.css`.
- **Skeleton or text.** A skeleton goes where the final layout is known (cards
  in a grid, the chips of a card, a row with fixed columns); a list of unknown
  length shows the empty family's "Loading…" text. `shape` is `line` (by
  default; `lines` stacks a paragraph with a shorter last bar), `block` (a
  card, stretching to its grid row) or `chip`; `tag` is `span`, `div` or `li`.
  A page may set a shape's width or flex, not its look.
- **Skeleton and screen readers.** Every shape is `aria-hidden`. The region
  that waits carries `aria-busy="true"` and says what it loads in words,
  visible or in a `.visually-hidden` span.
- **Motion.** The pulse is four `--dur-slow` long and stops when the person
  asks for reduced motion.
- **Parity.** `src/components/parity-callout.test.ts` renders Callout and
  Skeleton with the Astro container and compares them with their twins.
- **Every notice is a Callout.** The project's clone banner, an
  environment's proposal, a batch that failed to come up, Infrastructure's
  problems and the tasks area's attention strip (`attentionRowHTML()`),
  stale banner, flow warnings and pull request skeleton all write one; the
  page classes left on them (`.clone-banner`, `.env-proposal`,
  `.ev-bringup-fail`) only place it.

Notes on the empty row:

- **Two pieces.** EmptyState is the block: block (a whole panel, 24px
  glyph), sm (a sidebar list, a chart, a drawer section, 20px glyph) and
  inline (one left-aligned line with its action at the end). EmptyRow is the
  bare placeholder line of a table (`tr`, the default), a list (`li`) or a
  list of divs (`div`), centred and in italics.
- **Loading is not empty.** Pass `loading` while the first payload is on its
  way: the text defaults to "Loading…", the element gets `aria-busy="true"`
  (a polite `role="status"` outside a table) and the empty text replaces it
  once the payload says so. A failed load is `tone="danger"`: red glyph or
  text and `role="alert"`, so it does not rely on colour.
- **Hooks.** Pollers look for `.row-empty` (and can test `[aria-busy]`) to
  tell a placeholder from real content; the class stays on every EmptyRow.
- **Variants.** `dashed` outlines the hole where a card or a list would go
  (any size). `as="li"` puts an EmptyState inside a list.
- **Heading.** `titleTag="h2"` (or `h3`) writes the title as a heading, for
  a state that stands in for a whole section, such as a first-run guide,
  so heading navigation still finds it. The look stays in `empty.css`: a
  guide with `steps` gets the larger title whatever the tag.
- **Filled later.** `titleId` and `bodyId` write the title and body elements
  even while empty, for a page whose script sets their text.
- **Markup in a row.** What EmptyRow holds in its slot (a sentence with a
  link) is `html` in `emptyRowHTML()`, written as is: escape what goes into
  it.
- **Parity.** `parity.test.ts` checks both components against their twins.
- **Row styles.** `.row-empty` lives in `primitives/empty.css` with the
  rest of the family. Every selector names its element (`tr.row-empty td`,
  `li.row-empty`, `div.row-empty`), which keeps it above the table's own
  cell rules (`.list-table td`, compact, stacked) now that it shares their
  layer. A script writes its empty row with `emptyRowHTML()`.

### Forms and dialogs

| Family    | Astro                                                                                                                                                                                                                                                                             | TypeScript                                                                                                                                                                                                                                              | CSS classes                                                                                                                                                                                                                                                                        | Use it for                                                                                                                                                 | Status |
| --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ------ |
| `form`    | `Field.astro`, `FormError.astro`, `FormSection.astro` (`legend`, `ruled`, `group`; slots: default, `legend`), `InlineStatus.astro`, `DateInput.astro` (`id`, `mode`, `value`, `name`) (notes below)                                                                               | `fieldHTML()`, `formErrorHTML()`, `fieldErrorHTML()`, `inlineStatusHTML()`, setters (notes); `dateInputHTML()`, `readDateInput()`, `writeDateInput()` in `lib/ui/datetime.ts`                                                                           | `.field`, `.field-inline`, `.field-sm`, `.field-grow`, `.form-stack`, `.form-row`, `.date-input`… (notes below)                                                                                                                                                                    | A labelled control, the containers that space fields, the save status.                                                                                     | ready  |
| `check`   | `Check.astro` (`type`, `name`, `value`, `label`, `hint`, `checked`, `disabled`, `reason`, `card`, `head`, `icon`, `id`, `error`, `errorId`, `inputClass`, `inputAttrs`; slots: default, `hint`, `trailing`); `CheckGroup.astro` (`layout`, `wide`, `type`, `label`, `labelledBy`) | `checkHTML()`, `checkEl()`, `checkGroupHTML()`; `checkClasses()`, `checkGroupClasses()`, `checkIds()` shared with the components, in `lib/ui/check.ts`                                                                                                  | `.check`, `.check-card`, `.check-head`, `.check-input`, `.check-ico`, `.check-body`, `.check-label`, `.check-hint`, `.check-reason`, `.check-error`, `.check-trailing`; `.check-group`, `-grid`, `-wide`; `.check-list`; `.check-nested`                                           | A checkbox or radio with its label, alone, as a card (a radio card is `type="radio"` + `card`) or in a group.                                              | ready  |
| `picker`  | `Picker.astro` (`id`, `label`, `items`, `multi`, `search`, `query`, `chips`, `hint`, `create`, `empty`, `active`, `current`, `kind`, `errors`, `place`, `width`, `start`, `up`)                                                                                                   | `pickerHTML()`, `pickerListHTML()`, `openPicker()`, `bindPicker()`, `filterPickerItems()`; `pickerClasses()` and the `picker*Attrs()` helpers shared with the component, in `lib/ui/picker.ts`                                                          | `.nselect`, `-floating`, `-inline`, `-start`, `-up`, `-sm`, `-md`, `-lg`, `.nselect-headrow`, `-head`, `-input`, `-kind`, `-hint`, `-current`, `-current-row`, `-list`, `-group`, `-group-head`, `-item`, `-lead`, `-label`, `-item-hint`, `-check`, `-create`, `-empty`, `-error` | Picking one or several values from a list that drops from a property row or a chip, with search, chips and a create row. Not for actions (that is `menu`). | ready  |
| `swatch`  | `SwatchPicker.astro` (`label`, `items`, `selected`, `kind`, `role`, `boxed`, `keepOpen`)                                                                                                                                                                                          | `swatchPickerHTML()`, `checkSwatch()`, `moveSwatchFocus()`; `swatchPickerClasses()`, `swatchClasses()`, `swatchPickerAttrs()`, `swatchAttrs()` shared with the component, in `lib/ui/swatch.ts`                                                         | `.swatch-picker`, `-color`, `-glyph`, `-boxed`; `.swatch`, `.swatch-color`, `.swatch-glyph`                                                                                                                                                                                        | One colour or one glyph out of a grid: a column's colour in its menu, a task type's colour and icon. Not a list of words (that is `picker`).               | ready  |
| `prop`    | `PropList.astro` (`layout`, `class`), `PropRow.astro` (`name`, `label`, `icon`, `edit`, `valueAttrs`, `class`; default slot = the value, slot `trailing`)                                                                                                                         | `propRowHTML()`, `propEmptyHTML()`, `propListClasses()`, `setPropListLayout()`, `propValueClasses()`, `propValueA11y()` in `lib/ui/prop.ts`                                                                                                             | `.prop-list`, `.prop-list-auto`, `.prop-list-sheet`, `.nprop`, `.nprop-label`, `.nprop-value`, `.nprop-static`, `.nprop-value-empty`                                                                                                                                               | The properties of a record whose value opens a picker (a task's stage, agent, priority). Read-only facts go in `facts`.                                    | ready  |
| `modal`   | `Modal.astro` (`id`, `title`, `icon`, `size`, `variant`, `closable`, `lede`, `bodyClass`, `cancel`, `primary`; slots `head`, `actions`, `actions-start`, `foot`)                                                                                                                  | `modalHTML()`, `modalEl()`, `modalClasses()`, `overlayClasses()`, `modalCancelButton()`, `modalPrimaryButton()` in `lib/ui/modal.ts`; `bindModal()`, `openModal()`, `closeModal()`, `closeTopModal()`, `escapeCloses()` in `lib/modal.ts` (re-exported) | `.overlay`, `.overlay-sheet`, `.scrim`, `.modal`, `.modal-md`, `-lg`, `-xl`, `.modal-sheet`, `.modal-viewer`, `.modal-head`, `.modal-body`, `.modal-lede`, `.modal-foot`, `.modal-foot-start`                                                                                      | A dialog that asks for input or shows one thing over the page. Bind it with `bindModal()`; Escape closes it with no page code.                             | ready  |
| `confirm` | `ConfirmDialog.astro` (`title`, `message`, `details`, `option`, `requireText`, `warning`, `notes`, `confirmLabel`, `danger`, `icon`; slot = body)                                                                                                                                 | `confirmDialog()`, `confirmDialogHTML()`, `moveTasksBodyHTML()`, `confirmValues()` in `lib/ui/confirm.ts`                                                                                                                                               | `.confirm-overlay`, `.confirm-dialog*`                                                                                                                                                                                                                                             | Yes/no before a destructive or irreversible action, with an optional list, box to tick, destination picker or text to type. Never `window.confirm`.        | ready  |

Notes on confirm:

- **One call.** `confirmDialog(opts)` opens the dialog Layout mounts
  (`#confirm-overlay`, made on the fly on a page without Layout) and answers
  with a promise: a boolean for a plain question, `{ ok, values }` when the
  dialog holds controls (`option`, `bodyHTML` or `body`). `values` has each
  named control: a checkbox as a boolean, a radio group or a select as its
  value.
- **Say what happens.** `title` names the thing ("Delete the board
  Releases?") and `confirmLabel` is the verb ("Delete board"); both are
  required, so there is no generic "Are you sure? / Confirm" left to fall
  back on. `message` says the consequence and keeps its line breaks.
- **Variants.** `danger` paints the glyph box and the button red; without
  it they are grey and blue. `details` lists what goes along with it
  (`detailsMono` for ids); `option` is one box to tick with its hint;
  `bodyHTML` or `body` holds the caller's controls; `requireText` adds a
  field and keeps the button off until it matches (the slug before deleting
  a workspace), and `requireMatch(typed)` says what matches when an exact
  match is wrong (an email, in any case); `warning` is a warn callout with an optional link, which
  answers no when followed; `notes` are quieter lines at the end.
- **Moving tasks out.** Deleting a board or a column with tasks asks where
  they go: `moveTasksBodyHTML({ count, destinations, label })` is that
  picker as `bodyHTML` (empty with no tasks), and its select comes back as
  `values.move_to`.
- **Work before closing.** `action(values)` runs on yes with the dialog
  still open and both buttons waiting; if it throws, its message shows in
  the dialog as a danger callout and the person can retry or cancel.
- **Keyboard.** The dialog takes Escape, Tab and Enter before the page does
  (a capture listener), so Escape never also closes the modal underneath and
  the page's shortcuts stay quiet while it is open. Enter in a text field
  submits; Enter on Cancel cancels. Focus goes to the text to type, else the
  first control of the body, else the confirm button, and back to the
  trigger on close. A second confirmation answers the open one no.
- **The option's box.** A Check (`checkHTML()`), with the id
  `<prefix>-option`: its label names the box and its hint describes it.
- **Parity.** `src/components/parity-confirm.test.ts` renders ConfirmDialog
  with the Astro container and compares it with `confirmDialogHTML()`.
- **No generic question.** Layout's overlay starts empty, so every
  confirmation on the dashboard comes from a `confirmDialog()` call that names its thing and its verb.

Notes on the modal row:

- **One skin.** The banded box: head, a body that scrolls, and a foot on its
  own band that stays in view under a long form. `size` is `sm` (520px, the
  default), `md` (560), `lg` (680) or `xl` (760), from the `--modal-w-*`
  tokens; on a phone every size is 92% of the screen. `variant="sheet"` docks
  the box to the bottom edge at full width with a rule under the head, for
  a phone (the board's filters); a sheet has no size. `variant="viewer"` is
  the tall box that shows one file (the task's attachment viewer): as big as
  `--modal-w-viewer` × `--modal-h-viewer` allow, the head over a rule with
  the file's name and its controls in the `head` slot, and a body that is a
  dark well with no padding, for the file to fill. The Evidence capture
  viewer (`#shot-modal`) is one too, its arrows either side of the picture.
- **Head.** `icon` is the large glyph before the heading. `closable` writes
  one ✕ (the default icon button, 28px) with `data-modal-close`. The `head`
  slot (`headHTML`) goes between the heading and the ✕.
- **Lede.** `lede` is the paragraph that says what the dialog is for, first
  in the body, in the quiet colour; the dialog names it as its description
  (`aria-describedby`). The account, workspace, credential and column
  dialogs use it.
- **Foot.** `cancel` writes Cancel with the Esc hint (a string is its label,
  "Close" for a modal that only shows something); `primary` writes the main
  action last, `variant: "danger"` for one that deletes, `type: "submit"` and
  `form` to send a form from a foot outside it. The `actions` slot
  (`actionsHTML`) holds more buttons before Cancel; `actions-start`
  (`actionsStartHTML`) holds a Delete at the left end. The `foot` slot
  (`footHTML`) is the whole foot written by hand and wins over the rest. The
  foot wraps on a narrow screen. Its buttons are the default size.
- **Escape.** lib/keyboard.ts runs the one Esc listener: the help sheet,
  then the page's `onEscape` entries, then `closeTopModal()`, which closes
  the modal opened last if the key was pressed inside it (or on the body).
  A key pressed in a layer above it (the confirm dialog, the palette, a
  drawer) is that layer's. A page lists a modal in `onEscape`
  (`escapeCloses()`) only when it must close before another entry of the
  page. `dismissable: false` keeps the backdrop and Escape from closing it
  (the page's own handler decides); `beforeClose` can refuse any close but a
  forced one.
- **Focus.** `open(field)` focuses the field; with none, the box takes the
  focus (`tabindex="-1"`, no ring) so a screen reader announces the dialog.
  Tab cycles inside it. On close the focus goes back to what opened it,
  unless the page moved it elsewhere.
- **Overlay attributes.** Props the component does not know (`data-*`) go on
  the overlay, as `attrs` does in `modalHTML()`. KbdHelp marks its sheet
  with `data-kbd-help` that way.
- **Scrim.** `.overlay` and `.scrim` share the veil (`--color-scrim` and
  `--scrim-blur`); an overlay laid out its own way, like the command
  palette's, adds `.scrim` instead of copying them.
- **Parity.** `parity-modal.test.ts` renders Modal with the Astro container
  and compares it with `modalHTML()`.

Notes on checks:

- **One shape.** The control is always on the left, centred on the first
  line of the label, and the whole row is the `<label>`: a click on the text,
  the hint or the card ticks it.
- **Name and description.** Give the input an `id` whenever the row has a
  hint, a reason, an error or something trailing: the label becomes the
  input's name (`aria-labelledby`, `<id>-label`) and the rest its
  description (`aria-describedby`, `<id>-hint`, `<id>-reason`, `errorId`).
  Without an id the whole text of the row is the name.
- **Hint.** A line under the label; a label with a hint turns medium weight,
  as the title of what the hint explains. `hintHTML` (or the `hint` slot)
  takes a link.
- **Off.** `disabled` greys the label and `reason` says why, in words under
  it; a reason on an enabled check throws. An off card has a dashed edge.
- **Card.** `card` draws the bordered tile; it fills with the accent from
  the input's own state (`:has(:checked)`), so no script toggles `.on`, and
  the box or dot stays visible, so the state does not rest on colour.
  `icon` puts a glyph between the control and the label.
- **Radio cards.** `type="radio"` with `card`, in a `CheckGroup type="radio"`
  (a `radiogroup`, named by `label` or `labelledBy`). The browser moves
  between radios of one name with the arrow keys; nothing to bind.
- **Groups.** `stack` (default) one under the other; `grid` in columns at
  least `--check-grid-min` (160px) wide, `wide` at `--check-grid-min-wide`
  (240px); `list` in rows inside a sunken box that scrolls past
  `--check-list-max`, with `head` for the bold row that opens a group of
  rows (a server above its tools). `checkGroupHTML()` hands its `type` and
  `name` to the items that do not set their own; `CheckGroup.astro` does not,
  so each `Check` says its own.
- **Trailing.** `trailingHTML` (or the `trailing` slot) sits at the end of
  the row: a badge, the kind of a grant. Written as is.
- **Hooks.** `inputCls`/`inputClass` and `inputAttrs` reach the input (the
  `.perm-box`, `data-flag`, `data-tool` a save handler reads), `cls` and
  `attrs` the label (`data-flag-row`, `title`).
- **Errors.** `errorId` writes an empty, hidden `.check-error` line with
  `role="alert"` for a script to fill; `error` writes it with its text.
- **What opens under an option.** `.check-nested` lines a block up with the
  label rather than the control: the form a chosen radio reveals (the
  authentication modes of a new project). Hide it with `hidden`.
- **Touch.** Where the pointer is coarse the box grows to `--check-box-touch`.
- **Parity.** `src/components/parity-check.test.ts` renders Check and
  CheckGroup with the Astro container and compares them with their twins.
- **Every box is a Check.** The column's behaviour flags
  (`flagCheckHTML()` in `lib/column-editor.ts`), Evidence's environments and
  kinds, a project's origin and its repository access are Checks; handlers
  read `input.checked`, never a class.

Notes on forms:

- **Dates and times.** `DateInput` replaces `<input type="datetime-local">`,
  `date` and `time`, which write the date in the browser's language whatever
  language Thulu is in (09/17/2026, 05:04 PM on a Spanish dashboard). Its
  box shows and reads the date through Intl in the UI's locale; the ISO
  value sits in a hidden input. The page reads and sets it with
  `readDateInput(id)` and `writeDateInput(id, value)` and listens on the box
  for `input` and `change`. The calendar button opens the browser's own
  picker (`showPicker()`), whose popup still follows the browser's language;
  a pick lands in the box. Layout wires every one with `bindDateInputs()`.
  A text that is no date reads as "" like an empty box, so a page that
  applies or saves the value asks `dateInputError(id)` or
  `dateInputsValid(ids)` first and does nothing on an error. The reason
  shows in the `.field-error` line the component writes after the box
  (through `setFieldError()`, so the box points at it with
  `aria-describedby`): when the box is left or a page asks, never while a
  date is half typed. Put it inside a `Field` or a `FilterField`.
- **A field.** `Field.astro` takes `label`, `for`, `optional`, `hint` (or
  the `hint` slot), `help` (or the `help` slot), `helpId`, `warn`, `warnId`,
  `error`, `errorId`, `layout`, `size` and `grow`, with the control in the
  default slot. `fieldHTML()` takes the
  same options, `hintHTML` and `helpHTML` for the slots and `control` for the
  control. Both write a `.field` holding `label.field-label` (with
  `.field-optional` and `.field-hint` inside), the control, `p.field-help`,
  `p.field-warn` and `p.field-error`. The label is plain: `--t-sm`, 500, `--fg-2`, never
  uppercase. `hint` is a grey aside on the label's line; `help` is a line
  under the control. Both are `--t-xs` `--fg-2`.
- **Optional.** `optional` writes "(optional)" from the catalog after the
  label with its space. Do not write it into the label text.
- **Layouts.** `stacked` (the default) puts the label above the control;
  `inline` (`.field-inline`) puts it in a `--field-label-w` column on the left
  and stacks again under 480px.
- **Size and grow.** `size="sm"` (`.field-sm`) is a field in a dense strip of
  controls, such as the trigger form over a recipe: the label a size down and
  the control as tall as a `.select-sm` (`--control-h-sm`), so the two
  heights never meet in one row. `grow` (`.field-grow`) takes the room the
  other fields of a row leave, from `--field-grow`, and shrinks before it
  wraps to a line of its own.
- **Controls.** Bare `input`, `select` and `textarea` are styled globally:
  `--t-md`, `--s-3` by `--control-pad-x` padding, `--color-field`. A page does
  not restyle them; a control inside a field stretches to the field's width.
  `aria-invalid="true"` turns the edge red.
- **Ids and errors.** The help line's id is the control's id with `-help` and
  the error line's with `-error`, unless `helpId`/`errorId` say otherwise
  (`fieldIds()`). A control built in a string takes
  `fieldControlAttrs({ for, help, error, errorText })` for its
  `aria-describedby` and `aria-invalid`. `setFieldError(target, msg, opts)`,
  where the target is the control or its field, fills or clears the line,
  marks the control and points its `aria-describedby` at the error only while
  it shows; `{ focus: true }` moves focus to the control.
- **Warnings.** `warn` is an amber line between the help and the error for
  a value that saves but needs a look (the init script's cache key names a
  lockfile the repository lacks). Its id is the control's with `-warn`;
  `setFieldWarning(target, msg)` fills or clears it and adds it to the
  control's `aria-describedby` while it shows, without marking the control
  invalid.
- **Form-wide errors.** A refusal of the whole form (the daemon said no, the
  network failed) goes in `FormError.astro` / `formErrorHTML()`: the
  `.form-error` box (a bare danger callout), an alert, hidden while empty, filled with
  `setFormError()`. `clearFormErrors(form)` empties both kinds. Not a toast.
- **An error line on its own.** A refusal that belongs to a block rather
  than to one control (adding subtasks or files, answering an agent's
  question, a column's WIP limit) is the same red line without a field:
  `fieldErrorHTML({ id, tag, row })` writes the `.field-error` alert,
  hidden while empty, and `setFormError()` fills it.
- **Spacing.** A field has no outer margin. `.form-stack` is a column of
  fields (`--s-4` apart), `.form-row` puts fields side by side and wraps
  (`.grow` on a field takes the rest), `.form-actions` holds the buttons and
  their status.
- **Sections.** `FormSection.astro` / `formSectionHTML()` name a part of a
  long form: a `fieldset.form-section` with no border, its
  `legend.form-section-legend` in the look of a section title (`--t-md`, 600) and its children in a `.form-section-body` column (`--s-3` apart).
  `ruled` draws a rule above, between parts; `group` gives the legend the
  look of a field's label, for a set of checks that answer one question. The
  `legend` slot (`legendHTML`) takes a heading when the section sits in an
  outline of headings, as in the credential drawer.
- **Save status.** `InlineStatus.astro` / `inlineStatusHTML()` is the line
  beside a form's button: `.inline-status[data-tone]` with `neutral`, `ok` or
  `danger`, a polite status region that is never hidden. Write it with
  `setInlineStatus(el, tone, text, { fadeMs })`; a later call cancels the
  pending fade.
- **Parity.** `parity-form.test.ts` renders Field, FormError, FormSection,
  InlineStatus and DateInput with the Astro container and compares them with
  their twins.
- **Links in the help line** take the accent (`.field-help a`), as the
  column's default agent does with "per project" and "Agents".
- MCPs space their fields with a placement rule.

Notes on the picker:

- **Two ways in.** `openPicker(anchor, opts)` builds a picker beside an
  anchor (a property value, a chip, a textarea) and closes it on a pick,
  Escape, Tab, a click outside or another popover opening, giving the focus
  back to the anchor. `bindPicker(el, opts)` wires one already on the page,
  usually `Picker.astro` with `place="inline"` in a form.
- **Places.** `prop` hangs under the value column of its `.nprop` (the
  default when `openPicker` gets a `mount`), and turns `up` when there is no
  room below; `floating` is placed by script next to any anchor, also inside
  the task drawer, whose transform moves where fixed (0, 0) lands; `inline`
  sits in the flow, always open. `width` (`sm` 200px, `md` 260px, `lg` 340px)
  replaces the row's width.
- **Keyboard.** The list is a `listbox`. The focus stays in the search field
  (a `combobox`), or on the list when there is none, and
  `aria-activedescendant` names the option the arrows are on: arrows wrap and
  skip off options, Enter picks, Backspace in an empty field drops the last
  chip. A field outside that keeps the focus (the @mention textarea) passes
  its keys to `handle.keydown(e)` and its text to `handle.setQuery(q)` with
  `focus: false`.
- **Items.** `{ value, label, hint, icon | dot | leadHTML, selected,
disabled }`; `labelHTML` draws the option as a pill or a chip and `label`
  is still what the search matches. Markup (`labelHTML`, `leadHTML`,
  `createHTML`) belongs to `pickerHTML()`: Picker.astro fails the build on
  it rather than print raw HTML. `items` can be a function of the query,
  for a list that filters itself. A single picker shows a check on the
  chosen value; `multi` stays open on a pick, and `chips` lists the chosen
  values as removable tags before the field.
- **Create.** `createLabel(query)` returns the row's words, or null for no
  row; Enter on it (or a click) calls `onCreate(query)`.
- **Keyboard cursor.** An inline picker sits open with the focus elsewhere,
  so the option the arrows are on shows only while the list or its search
  has the focus; otherwise its first option would look picked.
- **Current values, groups, kind.** `current` (`{ heading, items, empty }`)
  lists what the record already has above the search, one row with an X
  each, for a value a chip cannot carry (a dependency's kind and the state of
  the task it waits on); the X calls `onRemove`. `BindPickerOptions.current`
  can be a function, read again on every `refresh()`. An item's `group`
  ("Same parent", "Other tasks") puts consecutive options under a heading in
  a `role="group"`. `kind` adds a select beside the search and `onSelect`
  gets its value as `pick.kind`. `errors` writes an alert line that
  `handle.setError(msg)` fills when a pick or a removal fails. With `current`
  or `kind`, Tab walks those controls and the picker closes when the focus
  leaves it.
- **Who uses it.** The tasks area's stage, priority, type, projects, tags,
  per-stage agent, dependency and @mention lists and the new task's
  projects.

Notes on properties:

- **A property is not a field.** The value is shown as it reads (a stage
  pill, chips, "None") and is itself the button that opens the picker:
  `edit` names it ("Change the stage") and makes it focusable with
  `aria-haspopup`. Without `edit` it only reads, and `trailing` holds the one
  button that edits it (the task's projects).
- **Layouts.** `rows` puts each label on the left of its value
  (`--prop-label-w`). `auto` does the same while the list is narrow and puts
  each label over its value once the list is 26rem wide; the list is the
  container (`prop-list`) that the rows and the page's columns query. `sheet`
  is `rows` for a phone's sheet: a `--prop-sheet-label-w` label column and
  rows of `--tap-min` whatever the pointer. A list moved into the sheet
  switches to it with `setPropListLayout()`.
- **Parity.** `parity-prop.test.ts` renders PropRow and PropList with the
  Astro container and compares them with their twins.

Notes on the swatch picker:

- **Two roles.** Standing alone (`role="radio"`, the default) the picker is
  a `radiogroup` named by `label`, with one tab stop on the picked swatch;
  `moveSwatchFocus(e)` walks it from a keydown listener (Left and Right one
  swatch, Up and Down one row, Home and End, wrapping) and returns whether
  the key was its. Inside a Menu (`role="menuitemradio"`) it is a
  `data-menu-row` the menu walks, and `keepOpen` keeps the menu open on a
  pick so the colour can be tried on the column behind it.
- **Items.** `{ value, label, color, icon, attrs }`. `label` names the
  swatch for screen readers and the tooltip: the colour's or the shape's
  name from the catalog, never the hex or the slug. A colour swatch fills
  from `color` (the value by default) through `--sw`; a glyph swatch draws
  `icon` from the sprite. `attrs` carries the hooks a page reads
  (`data-swatch`, `data-icon`).
- **Picks.** The page handles the click; `checkSwatch(el)` moves the check
  (and a radio picker's tab stop) to it without drawing the picker again.
- **Boxed.** `boxed` puts the picker on a raised box of its own, for one
  that opens under its trigger (Settings → Boards); in a menu it sits bare.
  The ring of a picked colour leaves a gap of the surface it sits on.

### Containers and lists

| Family      | Astro                                                                                                                                                                                                                                                                                                                                                    | TypeScript                                                                                                                                                                                                                                | CSS classes                                                                                                                                                                                                                                                                                                                                                        | Use it for                                                                                                                                                                                                                                         | Status                                                                                                                                                                                                                                            |
| ----------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- |
| `panel`     | `Panel.astro` (`icon`, `title`, `titleId`, `as`, `size`, `tone`, `pick`, `flush`, `pad`, `tag`, `bodyAttrs`; slots `title`, `status`, `aside`, `actions`, `foot`, `head`); `SectionHead.astro` (`title`, `id`, `level`, `count`, `countOpts`, `toggle`; slots `title`, `aside`, `actions`)                                                               | `panelHTML()`, `panelEl()`, `panelHeadHTML()`, `panelClasses()`, `panelBodyClasses()`; `sectionHeadHTML()`, `sectionHeadClasses()` in `lib/ui/panel.ts`                                                                                   | `.panel`, `.panel-sm`, `.panel-danger`, `.panel-pick`; `.panel-head`, `.panel-title`, `.panel-head-status`, `.panel-head-aside`, `.panel-head-actions`; `.panel-body`, `-md`, `-flush`; `.panel-foot`, `.panel-lede`, `.panel-stack`; `.sec-head`, `.sec-head-sm`, `.sec-title`, `.sec-toggle`, `.sec-actions`                                                     | A titled section of a page (md), a box nested in one (sm), a danger zone. SectionHead for a section with no box around it: the task drawer.                                                                                                        | ready                                                                                                                                                                                                                                             |
| `card`      | `Card.astro` (`title`, `heading`, `href`, `reveal`, `dashed`, `off`, `target`, `tag`; slots: default body, `head`, `media`, `foot`)                                                                                                                                                                                                                      | `cardHTML(opts)`, `cardClasses()`, `cardTags()`, `CARD_TARGET` in `lib/ui/card.ts`                                                                                                                                                        | `.card`, `.card-media`, `.card-head`, `.card-body`, `.card-foot`, `.card-cover`; `.card-link`, `.card-reveal`, `.card-dashed`, `.card-off`, `.card-target`                                                                                                                                                                                                         | The tile of one entity in a grid (a board, a project, a case, an environment), clickable as a whole with `href`. Notes below.                                                                                                                      | ready                                                                                                                                                                                                                                             |
| `task-card` | `TaskCard.astro` (`id`, `flags`)                                                                                                                                                                                                                                                                                                                         | `taskCardHTML()`, `taskCardEl()`, `taskCardShellHTML()`, `taskCardClasses()`, `setTaskCardFlag()`, `hasTaskCardFlag()`, `taskCardKidsId()`, `taskCardMenuButton()` in `lib/ui/task-card.ts`; the parts that fill it in `lib/task-card.ts` | `.task-card`, `-dragging`, `-ghost`, `-lifted`, `-hl`, `-dim`; `.task-card-tools`, `-handle`, `-menu-btn`, `-part`, `-part-text`, `-title-row`, `-type`, `-title`, `-prio`, `-state`, `-reason`, `-meta`, `-foot`, `-progress`, `-expand`, `-foot-rest`, `-pr`, `-id`, `-age`, `-kids`, `-kid`                                                                     | The card of one task in a board column: grabbed and moved, opened with a click or Enter. Notes below.                                                                                                                                              | ready                                                                                                                                                                                                                                             |
| `stat`      | `StatTile.astro` (`label`, `value`, `sub`, `tone`, `size`, `href`, `icon`, `meter`, `na`, `stale`, `tag`; slots `value`, `sub`, `spark`); `StatGroup.astro` (`layout`, `label`, `tag`)                                                                                                                                                                   | `statTileHTML(opts)`, `statGroupHTML(tiles, opts)`, `setStat()`, `statClasses()`, `statGroupClasses()`, `statTone()`, `statIds()` in `lib/ui/stat.ts`                                                                                     | `.stat`, `.stat-label`, `.stat-value`, `.stat-sub`, `.stat-icon`, `.stat-meter`, `.stat-spark`, `.stat-stale`, `.stat-sm`, `.stat-link`, `.stat-na`, `.stat[data-tone]`, `.stat[data-stale]`; `.stat-grid`, `.stat-flow`, `.stat-strip`                                                                                                                            | A number with what it counts and a line of detail: KPIs, health checks, signals. Notes below.                                                                                                                                                      | ready                                                                                                                                                                                                                                             |
| `page-head` | `PageHead.astro` (`title`, `subtitle`, `level`, `flush`, `subMax`, `titleId`, `back`, `crumbs`, `crumbsLabel`, `crumbsId`; slots `above`, `status`, `sub`, `actions`); `Crumbs.astro` (`crumbs`, `label`, `id`, `size`, `line`; slot `action`)                                                                                                           | `pageHeadHTML()`, `pageHeadClasses()`, `crumbsHTML()`, `crumbsClasses()`, `crumbClasses()`, `pageBackHTML()` in `lib/ui/page-head.ts`                                                                                                     | `.page-head`, `.page-head-section`, `-flush`, `.page-head-text`, `.page-title`, `.page-title-row`, `.page-head-status`, `.page-sub`, `.page-head-actions`; `.page-back`; `.crumbs`, `.crumbs-sm`, `.crumbs-line`, `.crumbs-list`, `.crumb`, `.crumb-sep`, `.crumb-mono`, `.crumb-hide-narrow`, `.crumbs-action`; `.sub-head`                                       | The title block at the top of a page, or of a view that stands for one inside a page (`level="section"`). `.sub-head` is the h3 that splits a panel's body.                                                                                        | ready                                                                                                                                                                                                                                             |
| `list-row`  | `ListRow.astro` (`as`, `href`, `title`, `titleHref`, `titleAttrs`, `mono`, `meta`, `aside`, `size`, `wrap`, `clamp`, `inline`, `boxed`, `reveal`, `dim`, `current`, `expanded`; slots `lead`, `title`, `badges`, `meta`, `tags`, `aside`, `actions`); `GroupHead.astro` (`title`, `kicker`, `icon`, `meta`, `level`, `band`, `as`, `cols`; slot `extra`) | `listRowHTML()`, `listRowsHTML()`, `rowGroupBoxedHTML()`, `groupHeadHTML()`; `listRowClasses()`, `listRowAttrs()`, `rowGroupBoxedClasses()`, `groupHeadClasses()` shared with the components, in `lib/ui/list-row.ts`                     | `.row`, `.row-sm`, `-wrap`, `-wrap-all`, `.row-clamp-1`, `-2`, `.row-inline`, `.row-boxed`, `-reveal`, `-dim`, `.row-caret`, `-lead`, `-main`, `-top`, `-title`, `-title-mono`, `-meta`, `-tags`, `-aside`, `-actions`, `.row-detail`; `.row-group-boxed`, `-edge`; `.row-list`; `.group-head`, `-band`, `-title`, `-kicker`, `-meta`, `-extra`, `.group-head-row` | One item of a list: what it is, a quiet line about it, and what you can do to it. GroupHead opens a group of rows, cards or table rows.                                                                                                            | ready                                                                                                                                                                                                                                             |
| `facts`     | `Facts.astro` (`rows`, `layout`, `boxed`)                                                                                                                                                                                                                                                                                                                | `factsHTML(rows, { layout, boxed })`, `factHTML()` in `lib/ui/facts.ts`; a row is `{ label, value \                                                                                                                                       | valueHTML, mono, note, attrs, valueAttrs }`                                                                                                                                                                                                                                                                                                                        | `.fact-list`, `-inline`, `-grid`, `-stacked`, `-boxed`; `.fact-list-mono`, `.fact-list-note`                                                                                                                                                       | A key/value list. `columns` (default) for details and config, `inline` for a summary strip, `grid` for a detail card, `stacked` for a narrow panel. The label is never bold. A row with no value is left out; `""` keeps an empty `<dd>` to fill. | ready |
| `avatar`    | `Avatar.astro` (`name`, `kind`, `size`, `shape`, `pending`, `icon`, `letters`, `status`, `pulse`, `label`)                                                                                                                                                                                                                                               | `avatarHTML(opts)`, `authorLineHTML({ name, kind, at, stage, avatar })`, `initialsOf()` in `lib/ui/avatar.ts`                                                                                                                             | `.avatar`, `-md`, `-square`, `-agent`, `-system`, `-pending`, `.avatar-status[data-tone]`; `.author-line`, `-name`, `-kind`, `-time`, `-in`                                                                                                                                                                                                                        | The mark of a person (initials), an agent (robot) or the system (Thulu), 24 or 28px; hidden from screen readers beside the name. The author line opens a comment, event or history entry with the relative time and the absolute one in its title. | ready                                                                                                                                                                                                                                             |
| `table`     | `Table.astro` (`cols`, `compact`, `top`, `stack`, `noHead`, `expandable`, `empty`, `loading`, `label`, `bodyId`, `bare`; slot = the body)                                                                                                                                                                                                                | `tableHTML(cols, rows, opts)`, `tableRowHTML()`, `tableHeadHTML()`, `tableCellHTML()`, `setTableSort()`; `tableClasses()`, `tableHeadAttrs()` shared with the component, in `lib/ui/table.ts`                                             | `.table-wrap`, `.list-table`, `-top`, `-compact`, `-stack`, `-no-head`, `-dim`, `-detail`; `tr[aria-current]`; cells `.num`, `.cell-shrink`, `-fill`, `-title`, `-sub`, `-caret`, `-actions`; `tr[data-tone]`; `th[aria-sort]`                                                                                                                                     | Tabular data: things compared column by column. Scrolls sideways inside its panel; `stack` turns each row into a card on a phone.                                                                                                                  | ready                                                                                                                                                                                                                                             |

Notes on panels and heads:

- **The panel head.** Left to right: glyph, title, `status`, `aside`,
  `actions`. The title pushes what follows to the right edge; with a status
  the status pushes instead, so a badge stays beside the words. Only one auto margin works per head: the
  aside and the tray sit where they land after a title. On a phone the tray
  wraps under the title.
- **Aside or actions.** `aside` is a grey note in `--t-sm` (a hint, "no data
  yet", a count in words, a link with an arrow); `actions` holds controls
  (sm buttons, `.select-sm`, an icon button, a filter check).
- **Title.** `title` is text; the `title` slot (`titleHTML`) puts markup
  inside the heading (a bold stage name, a count). `as="div"` is for a head
  whose title is a control, the editable name of an automation. `titleId`
  names the heading for the panel's `aria-labelledby` or a script that
  renames it; a script finds a title by id, never by `.panel-head h2`.
- **Sizes and pads.** md is the page panel; sm is a box inside a page or a
  panel (radius md, no fill, no band, the title as a small mono label), the
  per-stage blocks of the agents page. The body pads `--s-3` (sm) or
  `--s-4 --s-5 --s-5` (md, Boards and Infra); `flush` takes it away for a
  table or a list. A flush body with pad md, or a small panel with pad md,
  throws.
- **Foot, lede, stack.** The `foot` slot (`foot`/`footHTML`) is a band with a
  rule above, for how a figure is measured. A foot whose one element is a
  note the script shows only when it applies (`<span slot="foot" hidden>`)
  takes no room while the note is hidden. `.panel-lede` is the paragraph a
  body opens with. Panels one under another go in `.panel-stack`, which gaps
  them; a panel has no outer margin and no page sets one on `.panel`.
- **Danger.** `tone="danger"` gives the box a red edge and a red glyph. Its
  title still says what the zone does.
- **Pick.** `pick` on a small panel whose title is a check (`as="div"`, the
  check in the `title` slot or `titleHTML`) fills the box with the accent
  while the box is ticked, from the input's own state: an auditor stage, an
  MCP server granted to a stage. The title keeps the check's type. A picked
  page panel throws.
- **Heading level.** `as` is `h2` in a page, `h3` in a card or a modal and
  `h4` for a box under a section that has its own `h3` (the per-stage blocks
  of an agent's Skills and MCP tabs).
- **Hidden body.** `bodyAttrs` lands on the body (`hidden`, a `data-*` hook).
  A small panel pads its head all round and its body only below, so with
  the body hidden (nothing to show until the box is ticked) the box closes
  under its head as evenly as it opens.
- **Section heads.** `SectionHead` / `sectionHeadHTML()` is the head of a
  section with no box: Brief, Subtasks, Files, Activity, Pull requests and
  Test cases in the task drawer. `count` goes inside the heading; `toggle`
  makes the title a button that folds the section (`aria-expanded`,
  `aria-controls`, the chevron turns; `disabled` hides it); `level={4}` is a
  subsection inside one. Keep the `id`: the section's `aria-labelledby`
  points at it.
- **Page heads.** `level="section"` writes an h2 at `--t-lg` for a view inside
  a page that already has its h1 (a board's detail in Settings). Above the
  title go `back` (a link back to the list), `crumbs` and the `above` slot, in
  that order; `status` puts a badge beside the title. The crumbs are a list in
  a `<nav>`; the last step without an `href` is the current page and a step in
  the middle without one throws. Give a step an `id` when a script fills its
  label in, `mono` when it is an identifier, `hideNarrow` when a phone can do
  without it (the separator after it goes too), and `attrs` for its hooks.
- **A head's own row.** `size="sm"` with `line` makes the trail the eyebrow
  of a drawer: one line, the steps between the first and the last shrink with
  an ellipsis, and the `action` slot (`actionHTML`) holds the one button after
  it (copy the link).
- **Parity.** `parity-panel.test.ts` renders Panel, SectionHead, Crumbs and
  PageHead with the Astro container and compares them with their twins.
- **The ops area.** Home, Infrastructure, Sandboxes, Usage and Automations
  are done: its head hints sit in the `aside` slot, its
  feet in `foot`, its section head is a PageHead with `level="section"`, and
  `.usage-page` only lays out the column.

Notes on list rows:

- **Four zones.** A row is a lead (a dot, a glyph, an avatar, a checkbox),
  the main column (the title, the badges after it on its line, and the meta
  under it), an aside (a time, a number) and the actions. Each zone is left
  out when there is nothing for it. The title cuts with an ellipsis; the meta
  wraps, or keeps to `clamp` lines (1 or 2) and cuts, for a sidebar of names
  whose description is a hint (CatalogItem). Tags (`tagsHTML`, slot `tags`)
  go under the meta and the clamp leaves them whole: the stages an agent
  runs on. `inline` (`.row-inline`) puts the meta on the title's line, after
  it, and cuts it with an ellipsis, for a list where each row stays one line
  tall: the command palette's results. It takes no `clamp`.
- **Actions in view.** Actions always show; `reveal` hides them until the
  row is hovered or holds the focus, for a dense list where they would be
  noise, and a screen without hover shows them anyway. Row actions are sm
  buttons: `listRowHTML()` draws its `actions` at that size, and in Astro the
  `actions` slot holds `<Button size="sm">`. Controls that are not buttons
  (a role select) go in `actionsHTML` or the same slot.
- **What the row is.** `div` by default, `li` in a `<ul class="row-list">`,
  `a` with `href` (the row opens something) or `button` (the row selects or
  toggles). A row that is a link or a button cannot hold actions (a control
  inside another throws); give it `titleHref` instead, which links the title
  alone.
- **Run of rows.** `.row-list` (or `listRowsHTML(rows, { as: "ul" })`) draws a
  rule between the rows and squares them; rows outside one (the dashboard's
  lists) keep a rounded hover and no rule.
- **States.** `current` writes `aria-current` (`true` for the chosen item of
  a list, `"page"` for the link to the page on screen) and draws one mark:
  an accent bar on the leading edge over the hover fill. A listbox option
  with `aria-selected="true"` gets the same mark: the command palette writes
  its results as `listRowHTML({ href, size: "sm", inline: true, attrs: {
role: "option", "aria-selected" } })`, links that still open in a new tab. `dim` greys the words of
  something kept but no longer in force (a revoked key); a badge says why.
- **Dense.** `size="sm"` is the row of a list inside a drawer, a card or the
  sidebar (the subtasks, a card's subtask rows, the chats, a settled thread):
  as tall as a large control, tighter padding, the title in `--t-sm`. `wrap`
  lets the title take two lines before it cuts, for task titles;
  `wrap="all"` (`.row-wrap-all`) lets it take every line it needs, for a name
  the row's buttons act on (a custom metric to approve or delete).
- **Links in the meta.** A link in the meta line (the owner's note pointing
  at the transfer) takes the accent, as in a callout.
  `titleAttrs` puts `data-*` or an `aria-label` on the title (the link, with
  `titleHref`), for a script that opens what the title names.
- **Boxed.** `boxed` gives the row its own border and radius (the LLM
  profiles), and boxed rows in a run stand apart instead of sharing a rule.
- **Detail.** A button row with `expanded` gets `aria-expanded` and a caret
  that turns when open; the page puts the detail under it in a
  `.row-detail`. A row that opens its detail from a button among its actions
  (a webhook's deliveries) puts the `aria-expanded` on that button.
- **Boxed group.** `rowGroupBoxedHTML(row + detail, { edge, cls })` boxes a
  button row with the `.row-detail` it opens, so the two read as one item
  apart from the ones around it (a pull request, a report's files). `edge`
  thickens the leading edge in `--row-edge`; the page sets only that
  property from the item's state (`.pr-row-ready { --row-edge: var(--ok) }`).
  A script that builds the box itself takes the class from
  `rowGroupBoxedClasses()`.
- **Narrow.** Under 480px the actions drop under the text; a `size="sm"` row
  keeps them in line and wraps its title instead.
- **Group head.** `groupHeadHTML()` / `GroupHead.astro`: an optional glyph,
  a `kicker` that says what kind of group it is ("System"), the title (a
  heading with `level`), a `meta` such as "3 cases" and the `extra` slot at the
  far end (a tag, a progress bar). Never in capitals. `band` lays it on the
  header grey with rules, inside a list or a table; `as: "tr"` puts it in a
  `.group-head-row` across `cols` columns, with the attributes on the `<tr>`.
- **Hooks.** A poller that updates a row in place finds its parts by
  `.row-title`, `.row-meta`, `.row-aside` and `.row-actions`; the page's own
  hook classes go in `cls`.
- **Parity.** `parity-list.test.ts` renders ListRow and GroupHead with the
  Astro container and compares them with their twins.
- **Where.** Every area's lists are list rows. In the
  task drawer the dependencies of Blocked by and Blocks are `sm` rows with
  the state word as a badge (`blockedByHTML()` in `lib/task-drawer.ts`);
  each pull request and each report's files are button rows that open a
  `.row-detail`, in a boxed group (`lib/pr-section.ts`, the Files section);
  the per-stage
  agent picker is a picker box of `sm` rows whose agent is a Tag button.
  The archived tasks stay menu items.

Notes on tables:

- **Columns first.** `cols` says each column's `label`, `num` (right-aligned,
  tabular figures), `mono` (the cells, not the head), `width` (`shrink`, as
  narrow as the content; `fill`, the rest; `title`, a fixed 30% share for
  the column that names the row, so a task or a person keeps its line beside
  a column of prose and tables stacked on one page line up) and `hideLabel`
  (a name for screen readers only, the column of actions). No widths in a
  `style` attribute.
- **Cells.** A cell is text (escaped), or `{ html }` written as is, with an
  optional `sub`, a quieter second line. `{ actions }` draws sm buttons on one
  line at the end of the cell.
- **Rows.** `tone: "danger"` or `"warn"` puts a mark in the margin of a row
  worth finding (a refusal, a failed delivery); its badge or text says what
  it is. `dim` greys a row no longer in force. `current` marks the row whose
  content shows elsewhere on screen (the run in Evidence's player) with
  `aria-current="true"` and the accent's soft fill. `id` lands as `data-row`.
- **One header.** A grey band, `--t-xs`, sentence case. `compact` drops the
  band and tightens the cells for a table inside a row or a drawer;
  `noHead` keeps the names for screen readers only.
- **Sorting.** A column with `sort` (`none`, `ascending`, `descending`) and
  `key` gets `aria-sort`, `data-sort` and a button for its label; the arrow
  is drawn by the sheet. The page binds the click and calls
  `setTableSort(table, key, order)`.
- **Expandable.** `expandable` adds a leading column of carets: an icon
  button with `aria-expanded` and `data-expand` (the row's `id`), named
  "Details" or the row's `expandLabel`. A row with `expanded: true` and
  `detailHTML` is followed by a `.list-table-detail` row across the table.
- **Phone.** Every table sits in a `.table-wrap` that scrolls sideways
  (`bare` leaves it out); a shadow on the edge says there is more that way,
  and goes once that edge is reached. `stack` turns each row into a card under 640px,
  each cell under its column's name (`data-label`, written by
  `tableCellHTML()`), the first cell as the title.
- **Empty.** `empty` and `loading` write an EmptyRow across every column
  when there are no rows (in Table.astro, when the slot is empty). A poller
  redraws the body (`bodyId`) with `tableRowHTML()` over the same `cols`.
- **Astro body.** Table.astro writes the head from `cols`; its slot is the
  body, rows written by hand or none until the poller fills it.
- **Group rows.** `groupHeadHTML({ as: "tr", cols, band: true })` opens a
  group of rows; `tableSpan(cols, look)` is the number of columns it spans.
- **Parity.** `parity-list.test.ts` renders Table with the Astro container
  and compares it with `tableHTML()`.
- **Where.** Every area's tables use it. `.actions-col` sets the width of
  an actions column.

Notes on cards and stat tiles:

- **One card.** A card is `.card`: padding `--card-pad` (16px, 12px under
  640px), a poster in `.card-media` that eats the padding at the top, the head
  with the name and whatever follows it (badges, the ⋯ button), the body in
  `fg-2` and the foot of buttons. The card of a task on the board is a
  TaskCard, not a Card.
- **A card that opens something.** `href` makes the title a link
  (`.card-cover`) stretched over the whole card: a click anywhere opens it,
  the focus ring goes round the card, and every other link, button or field
  inside sits above the cover. Do not bind a click handler to the card or give
  it `tabindex`; a keyboard shortcut that opens the focused card finds it with
  `closest(".card")` from the link.
- **Actions on hover.** `reveal="hover"` shows the foot only while the pointer
  is over the card or the focus is inside it; on a touch screen
  (`hover: none`) the foot always shows. Use it only in a dense grid where the
  actions repeat on every card.
- **States.** `dashed` for a hole where a card goes or a thing made on demand
  (a Sandbox); `off` for a thing switched off, with a badge in the head that
  says so; `target` rings the card a link pointed at in the accent (Evidence
  flashes it for 4 s by toggling `CARD_TARGET`). While the ring shows it is
  the mark, with no focus outline on top; once the class goes, a card the
  keyboard still has shows the usual focus-visible outline.
- **Task cards.** A TaskCard is denser than a Card (12px by 16px), grabbed
  (`cursor: grab`) and focusable: Enter opens the task, Space picks it up and
  the arrows move it. Its slots start empty and hidden; the board fills them
  with the parts of `lib/task-card.ts` and leaves a slot alone when its HTML
  did not change. The drag handle and the ⋯ button show on hover or focus,
  in a gutter the first row keeps free; on a touch screen the handle goes and
  the ⋯ is a 44px target in the foot's corner. The states are classes the
  board toggles with `setTaskCardFlag()`: `dragging` (the card left behind,
  faded), `ghost` (the copy under the pointer), `lifted` (picked up with the
  keyboard), and `hl`/`dim` for the group under the pointer in As cards mode.
  The board's edit mode fades the column's list, not each card.
- **One order.** A stat tile reads label, value, detail, top to bottom, in
  every page. The label is `t-xs` in `fg-2`, in sentence case (no
  uppercase); the value `t-xl` (`t-lg` with `size="sm"`) with tabular figures.
- **Tone.** `tone` colours the value (`ok`, `warn`, `danger`, `accent`,
  `muted`); the detail line says why in words, so the colour is never the only
  sign. A trend is a tone plus a detail ("Up 30% on last week").
- **Extras.** `icon` puts a glyph in a box on the left; `meter` draws a Meter
  under the value, named by the label (give the tile an `id` and the meter
  points at the label's `<id>-label`); the `spark` slot or `sparkHTML` takes a
  sparkline. `na` is a figure that does not exist here, dashed and in grey
  italics. `stale` dims the tile and adds a "stale" badge; `setStat(el, {
value, sub, tone, stale })` moves a tile already drawn, and `setMeter()` its
  meter.
- **Groups.** `StatGroup` / `statGroupHTML()` with `layout="grid"` holds
  separate tiles, as many per line as fit at `--stat-min` (two at 390px);
  `layout="flow"` wraps the same way but each row shares its whole width, so
  a tile left alone on the last row takes all of it (the headline rows of
  the Panel and Automations); `layout="strip"` puts them as cells of one box
  split by rules. A `label` names the group for a screen reader.
- **Figures stay whole.** The value breaks only between words, never inside
  a number ("≈$27.75" stays on one line); the label and the detail wrap
  anywhere.
- **Dashboards too.** A custom dashboard's panel is a Panel and its single
  number a StatTile; no area keeps cards or tiles of its own.

### Catalog pages and history

| Family    | Astro                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | TypeScript                                                                                                                                                                                                                                                                                                                                                           | CSS classes                                                                                                                                                                                                                                                                                                                                                                                          | Use it for                                                                                                                                                                                                | Status |
| --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------ |
| `catalog` | `CatalogLayout.astro` (`side`, `label`, `listId`, `mainId`; slots `toolbar`, `list`, `foot`, default = the editor); `CatalogItem.astro` (`name`, `description`, `mono`, `clamp`, `current`, `dim`; slots `badges`, `tags`); `EntityHead.astro` (`title`, `titleId`, `level`, `mono`, `meta`, `metaId`; slots `title`, `meta`, `badges`, `actions`); `MdEditor.astro` (`path`, `pathId`, `note`, `textId`, `value`, `readOnly`, `back`, `backLabel`, `backId`, `stateId`, `save`, `box`; slot `buttons`); `FileRow.astro` (`label`, `labelMono`, `status`, `tone`, `statusMono`, `variant`, `as`; slot `actions`); `SaveFoot.astro` (`stateId`, `dirty`; default slot = the buttons); `SaveState.astro` (`dirty`, `quiet`, `id`) | `catalogLayoutHTML()`, `catalogItemHTML()`, `entityHeadHTML()`, `mdEditorHTML()`, `fileRowHTML()`, `fileStatusHTML()`, `saveFootHTML()`, `saveStateHTML()`, `setSaveState()`, `bindItemList()`, `setCurrentItem()`; `catalogItemRow()` and `fileRowRow()` shared with the components, in `lib/ui/catalog.ts`. `wireMdEditor()` in `lib/mdeditor.ts` wires the editor | `.catalog-page`; `.catalog`, `.catalog-side-sm`, `.catalog-side`, `-toolbar`, `-list`, `-foot`, `.catalog-main`; `.entity-head`, `-text`, `-title`, `-title-mono`, `-meta`, `-badges`, `-actions`; `.md-editor`, `-box`, `-bar`, `-path`, `-note`, `-text`; `.file-row`, `-dashed`, `-deleted`, `.file-status`, `-mono`, `-text`, `[data-tone]`; `.save-foot`, `-actions`; `.save-state[data-dirty]` | A page that lists things at the left and edits the one picked at the right: agents and skills. The pieces work alone too: a project's per-stage files are FileRows, its hook editor an MdEditor in a box. | ready  |
| `history` | `HistoryView.astro` (`title`, `pick`, `bar`, `backLabel`, `boxed`)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `historyViewHTML()`, `openHistoryView()`, `fillHistoryList()`, `historyListError()`, `historyRowHTML()`, `historyPreviewHTML()`, `historyHeadingHTML()`, `historyNoteHTML()`, `historyChangeHTML()` in `lib/ui/history.ts`; `renderHistory()` (`lib/revisions.ts`) and `renderDefHistory()` (`lib/agent-def-history.ts`) are built on them                           | `.rev-view`, `-boxed`; `.rev-body`, `.rev-list`, `.rev-row-danger`; `.rev-preview`, `-head`, `.rev-when`; `.rev-text`, `.rev-facts`, `.rev-heading`, `.rev-note`, `.rev-before`, `.rev-after`                                                                                                                                                                                                        | The versions of one stored thing: who changed it and when at the left, what it said at the right, and the button that puts it back.                                                                       | ready  |

Notes on catalog pages:

- **The page.** `<main class="catalog-page">` instead of `.page`: a column a
  screen tall whose list and editor scroll on their own. The page head goes
  first, then CatalogLayout. On a phone (760px) the list goes above the
  editor, keeps `--catalog-side-h-phone`, and the page scrolls.
- **One list item.** CatalogItem is a ListRow drawn as a button: the name
  (`mono` for a slug), the description cut at `clamp` lines (2, or 1 for a
  list of slugs), `current` while it is open (the accent bar of every current
  row). Group a list with GroupHead. `bindItemList(list, { onPick })` marks
  the item clicked current and walks the items with ↑ ↓ Home End; the keys
  stop at the list, so a page-wide ↑ ↓ shortcut does not move twice.
- **The head.** EntityHead names what is open: the name in `--t-lg` (`mono`
  for a slug), the meta in `--t-xs`, a row of badges, and sm buttons at the
  right, each with its glyph (Delete too, in danger). It is a section of the
  page, so the heading is an h2.
- **One sign of unsaved.** SaveState is the dot and the words "Unsaved
  changes"; the save foot of a form and the bar of a file editor both carry
  it, and `setSaveState(el, dirty)` flips it. In the editor's bar it is
  `quiet`: hidden until there is something to save.
- **The file editor.** MdEditor's bar reads Back, the path (it names the
  textarea), the state, extra buttons (History) and Save, all sm; Save is
  primary with its ⌘S hint. `box` puts it in a bordered box whose text keeps
  `--md-editor-min-h`, for a dialog (the project's stage files) or a panel.
  `wireMdEditor({ …, stateEl })` flips the state. A reader gets the text
  `readOnly` and Save `hidden`; the page wires Back alone.
- **File rows.** A FileRow is a boxed ListRow: the label, the status under
  it (mono for a path, `statusMono={false}` for words, a dot first when it
  has a `tone`: ok exists, warn left over), and sm actions with text and a
  glyph. `dashed` is a fallback that stands in for others, `deleted` a file
  kept but gone (struck out).
- **Where.** Agents and skills are CatalogLayouts; the Workshop dialog
  lists its catalog with CatalogItems and heads the item with an EntityHead
  (level 3, Back only on a phone); a project's stage files are FileRows and
  open in a boxed MdEditor.

Notes on history:

- **Shared shell.** `openHistoryView(host, { title, pick, onBack })` draws
  the view and hands back the list and the preview; `fillHistoryList(list,
rows, { none, onPick })` writes the revisions (newest first) and calls
  `onPick(index)` with the one picked, which it marks current; the page
  writes the preview with `historyPreviewHTML({ when, body, action })`. A
  failed load is `historyListError()`.
- **Rows.** A revision is a small ListRow button: what it did ("Edited",
  "Revision 3"), who and when under it, a badge for an agent author.
  `tone: "danger"` for a deletion: the word says it, the colour repeats it.
- **Preview.** `body` is `{ text }` for a file (mono on `--surface-4`),
  `{ html }` for facts (sans; `historyHeadingHTML()`, `historyNoteHTML()` and
  `historyChangeHTML()` write what changed), or `{ empty }` when the revision
  has nothing to show. `action` is the primary sm button: Restore by default
  (`data-rev-restore`), or another label and hook (Undo delete, Load). None
  for the current revision or a reader.
- **Boxed.** `bar: false, boxed: true` for a view inside a panel whose own
  button goes back (the project's init script); list and text scroll within
  `--history-box-h`.
- **More than a text.** A revision that also set a configuration and carried
  files (the init script) passes `facts`, one escaped line each, listed
  above the text in `.rev-lead`, and `files`, each a code Disclosure named by
  its path under the text. `whenHTML` takes a when with a link in it (the
  task an agent wrote it from).

### Navigation and overlays

| Family      | Astro                                                                                                                                                                                                                                                                                                                      | TypeScript                                                                                                                                                                                                                                             | CSS classes                                                                                                                                                                                                                                                                                  | Use it for                                                                                              | Status |
| ----------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- | ------ |
| `toolbar`   | `FilterBar.astro` (`look`, `as`, `label`; `end` slot); `FilterField.astro` (`label`, `for`, `inline`, `grow`, `hideLabel`); `SearchInput.astro` (`id`, `placeholder`, `label`, `width`, `fluid`, `kbd`, `type`); `Chip.astro`; `FilterChips.astro` (`items`, `picked`, `attr`, `size`, `label`)                            | `filterBarHTML()`, `filterFieldHTML()`, `searchHTML()`, `chipHTML()`, `filterChipsHTML()`, `setChipsPressed()`; the `*Classes()` and `*A11y()` helpers shared with the components, in `lib/ui/toolbar.ts`                                              | `.filter-bar`, `-boxed`, `-ruled`, `-inset`, `.filter-bar-end`; `.field-filter`, `-inline`, `-grow`; `.search`, `.search-fluid`; `.chip`, `.chip-md`, `.chip-menu`, `.chip.is-active`, `.chip[aria-pressed]`, `.chip-count`, `.chip-group`                                                   | Filter bars: the row of filters above a list, its search box and its toggle chips.                      | ready  |
| `tabs`      | `Tabs.astro` (`items`, `active`, `label`, `variant`, `id`, `idPrefix`, `panels`)                                                                                                                                                                                                                                           | `tabsHTML()`, `bindTabs()`, `tabPanelAttrs()`; `tabsClasses()`, `tablistAttrs()`, `tabAttrs()`, `tabIds()` shared with the component, in `lib/ui/tabs.ts`                                                                                              | `.tabs`, `.tab`, `.tabs-pill`, `.tabs-vertical`, `.tab-flag`                                                                                                                                                                                                                                 | Switching between views of one thing.                                                                   | ready  |
| `segmented` | `Segmented.astro` (notes below)                                                                                                                                                                                                                                                                                            | `segmentedHTML()`, `segmentedEl()`, `segmentedValue()` (notes below)                                                                                                                                                                                   | `.segmented`, `-sm`, `-icons`, `-cards`, `-fill`                                                                                                                                                                                                                                             | One choice out of two to five, always visible.                                                          | ready  |
| `menu`      | `Menu.astro` (`items`, `id`, `label`, `labelledBy`, `density`, `align`, `floating`, `open`; named slots for `{ slot }` entries), `MenuItem.astro` (one item, the props of a `MenuItem`)                                                                                                                                    | `menuHTML()`, `menuItemsHTML()`, `menuItemHTML()`, `bindMenu()`, `dismissLayer()`, `menuStep()`, `menuPosition()`, `floatAt()`; `menuClasses()`, `menuAttrs()`, `menuItemClasses()`, `menuItemAttrs()` shared with the components, in `lib/ui/menu.ts` | `.menu`, `-rich`, `-start`, `-floating`; `.menu-item`, `-item-danger`, `.menu-icon`, `-body`, `-title`, `-sub`, `-hint`, `-kbd`, `-check`; `.menu-head`, `-note`, `-sep`, `-slot`                                                                                                            | Actions under a "more" button, a right click or a header button.                                        | ready  |
| `drawer`    | `Drawer.astro` (`id`, `title`, `sub`, `side`, `size`, `modal`, `closable`, `resizable`, `body`, `footNote`, `bodyClass`; slots `lead`, `eyebrow`, `actions`, `head`, `bar`, `foot`); `DrawerHead.astro` (`title`, `titleId`, `titleAttrs`, `sub`, `subId`, `closable`; slots `lead`, `eyebrow`, `sub`, `actions`, default) | `drawerHTML()`, `drawerEl()`, `drawerHeadHTML()`, `drawerBackButton()`, `drawerClasses()`, `drawerOverlayClasses()`, `drawerBodyClasses()` in `lib/ui/drawer.ts`; `bindDrawer()`, `bindDrawerResize()` in `lib/drawer.ts` (re-exported)                | `.drawer`, `.drawer-right`, `-left`, `-bottom`, `-sm`, `-md`, `-lg`, `-docked`; `.overlay-drawer`; `.drawer-head`, `-head-row`, `-lead`, `-heading`, `-eyebrow`, `-title`, `-sub`, `-actions`; `.drawer-body`, `-body-flush`, `-body-frame`; `.drawer-foot`, `-foot-note`; `.drawer-resizer` | A panel that comes in from an edge: a record beside the list, a long list of switches, a phone's sheet. | ready  |
| `kbd help`  | `KbdHelp.astro` (`rows`, `id`), a Modal with `data-kbd-help`                                                                                                                                                                                                                                                               | `lib/keyboard.ts`                                                                                                                                                                                                                                      | `.kbd-grid`, `.kbd-sep`, `.kbd-label` (in `kbd.css`)                                                                                                                                                                                                                                         | The shortcuts sheet "?" opens.                                                                          | ready  |

Notes on tabs:

- **One markup, three looks.** The default strip underlines the selected tab
  over a rule (a task's sections, a page's views); `pill` fills it in a row
  of pills (dashboards inside a card, the board's stages on a phone);
  `vertical` fills it in a side list (the Workshop), which becomes a row of
  equal tabs at 760px and below. The selected tab is the one with
  `aria-selected="true"`: no `.active` or `.tab-active`.
- **An item** is `{ id, label }` plus `icon` (14px) or `dot` (a status dot,
  true or a tone; `dotColor` takes a hex colour from data, such as a
  stage's), `badge` (`{ label, tone }`, a Badge after the label for the
  state of the view, such as a proposed dashboard), `count` (a Count pill
  hidden at zero, id `<tab>-count` for `setCount()`), `flag` (a warning
  glyph with words for a screen reader, id `<tab>-flag`; `flagHidden` draws
  it hidden), `labelId` (an id on the label's span, for a script that
  renames the tab) and `disabled`.
- **Nothing selected.** `active: null` selects no tab, for a strip of
  things to open where nothing is open yet (an environment's dashboards
  when only proposals exist); the first enabled tab keeps the tab stop.
- **Ids.** A tab is `tab-<id>` and, with `panels`, controls `panel-<id>`,
  both after `idPrefix`; `tabId` and `panelId` override them, and several
  tabs may name one panel whose content they swap. Spread
  `tabPanelAttrs(item, selected)` on each panel (Astro) or `attrsHTML()` it.
- **Behaviour.** `bindTabs(list, { hash, onChange, signal })` selects on a
  click and on the arrows, Home and End (Up and Down too on a vertical list),
  with only the selected tab in the tab order; it shows the selected tab's
  panel, hides the others' and scrolls the tab into view. With `hash` the
  selection lives in the URL (`#runs`; the first tab clears it, and a link
  to `#runs` selects it). It returns `select(id)` and `current()`; `onChange` says
  whether the reader made the change. `beforeChange(id)` is asked before a
  reader's pick lands and may answer with a promise: false keeps the tab
  that was on (a view with unsaved work that asks first). A change from
  code is not asked.
- **Never wider than the page.** A strip that does not fit scrolls sideways
  inside its own edge, with no visible scrollbar; the focus ring is drawn
  inside the tab so the scroll box does not clip it.
- **Where.** Tasks, the board on a phone, automations,
  infra, agents, Workshop, project, evidence and the environment
  dashboards draw Tabs. Settings has no strip: the sidebar picks its
  section through the hash (`lib/settings-sections.ts`). A toggle that
  looks like segments is the `segmented` family, not tabs.

Notes on the segmented row:

- **Real radios.** Each option is a radio inside its `<label>`, hidden but in
  the tab order, under a `role="radiogroup"` named by `label` or
  `labelledBy`. Tab reaches the chosen option, the arrow keys move the choice,
  and the look comes from `:checked`, so no script keeps `.on`, `.is-active`
  or `aria-pressed` in step. Give every group on a page its own `name`.
- **Events.** The page listens for `change` on the group or a parent that
  survives a re-render, and reads the choice with `segmentedValue(root)`.
  `setSegmentedValue(root, value)` chooses an option without firing
  `change`, for state that comes from the URL or a poll.
- **Value.** The option whose value is `value` starts chosen; when none
  matches, none is, so an unknown stored value is never saved back as
  another one.
- **Variants.** `size="sm"` (26px, filter bars and panel heads) or md
  (32px, the default), the heights of `.btn-sm` and `.btn`. `iconOnly` draws
  the glyphs (the chosen one in the accent) and keeps the words as the hidden
  name and the tooltip. `cards` draws separate bordered options that wrap,
  the chosen one in the accent tint (a choice on every row of a list). `fill`
  takes the width of its box and shares it evenly (a menu). Icons alone on
  cards, one option, or a value twice throw.
- **Where.** Infra's ranges, usage's presets and chart
  metric, the header's theme, the activity filter, the run logs' format,
  the column editor, the agent's model source, the Workshop's plan and the
  project and Evidence toggles are Segmented; the theme sits in a menu as items
  (`role="menuitemradio"` on each radio through `inputAttrs`,
  `role="group"` on the control), so the menu's arrows reach it.

Notes on menus:

- **Two densities.** `compact` (the default) is one line per item with a
  bare glyph, for the card, the header, a column and a toolbar; `rich` puts
  each glyph in a box and a line under the title, for menus whose items need
  explaining (the task's More actions).
- **Entries.** An item is `{ title, sub, icon | dot | leadHTML | indent,
hint, kbd, key, danger, disabled, checked, role, href, keepOpen }`; between
  items go `{ sep: true }`, `{ head, sub }` (a label; in `menuHTML()` it
  can carry a lead such as a back button), `{ note }` (why items are off) and `{ slot }` (swatches, a
  switch, the theme control: a named slot in Menu.astro, `html` in
  `menuHTML()`). A slot that holds a row of choices (the column menu's
  colour swatches) marks it `data-menu-row`: Up and Down stop on the row
  once, at its focused or checked choice, and Left and Right walk it.
  `leadHTML` is for `menuHTML()` only: in Menu.astro an
  item with a markup lead is a MenuItem with a `lead` slot, and an entry
  that carries `leadHTML` fails the build.
- **States.** `disabled` keeps the item in the arrow walk with
  `aria-disabled`, so its `sub` can say why, and it never runs. `checked`
  makes a `menuitemradio` (or `menuitemcheckbox` with `role`) with a check.
  `danger` is the item that deletes.
- **Behaviour.** `bindMenu(menu, trigger, opts)` gives the trigger
  `aria-haspopup`, `aria-expanded` and `aria-controls`; a click toggles,
  ArrowDown or ArrowUp opens on the first or last item. Inside, the arrows
  wrap, Home and End jump, an item's `key` runs it, Tab closes. Picking an
  item closes the menu and gives the focus back to the trigger before
  `onSelect` runs, unless the item has `keepOpen`. Escape closes the menu
  before any page handler sees it, so a drawer under it stays open. One
  menu or picker is open at a time (`dismissLayer()`).
- **Placing.** A menu hangs from a `position: relative` wrap under its
  trigger's end edge (`align="start"` for the start edge). `floating`
  lets `bindMenu` place it at the trigger or, with `open({ x, y })`, at the
  pointer of a right click, flipped to stay in the window. A menu is
  `--menu-w` to `--menu-w-max` wide (rich: `--menu-w-rich` to
  `--menu-w-rich-max`), so a long `sub` or note wraps instead of widening it.
- **One menu, many triggers.** When one menu serves a list that redraws
  (the board cards, the column heads), the page binds it to the button it
  opens from with a fresh `bindMenu()` per button, repaints the items in
  `onOpen`, and leaves a click on the bound button to its own listener;
  `lib/column-editor.ts` and `lib/boards-settings.ts` do it this way.
- **Where.** The header's New and account menus and the sidebar's workspace
  menu are Menus, as are the task, board, column and Evidence menus.
  `lib/card-menu.ts` re-exports `menuPosition()`.

Notes on filter bars:

- **One bar, four looks.** `plain` has no edge (a tab's toolbar: chips on
  the left, buttons at the end); `boxed` is a box on surface-1 (the filters
  above usage, a project's runs, the evidence lists); `ruled` has a line
  above and below (the board, the projects list); `inset` is the first row
  of a flush panel, with a line under it (the containers of /infra/). The
  `end` slot (`end` in `filterBarHTML()`) pushes Clear or Export to the end
  of the row, and on a phone to a line of its own.
- **One label type.** A filter is a `FilterField`: a Field with the label
  in `t-xs`, `fg-2`, sentence case, over a control as tall as the md chips
  and the buttons (32px). `inline` puts the label on the left (the project
  picker of /evidence/), `grow` lets the field take the room left (the
  search), `hideLabel` keeps the name for screen readers only. When a bar
  holds stacked fields, everything in it lines up on the controls' bottom
  edge.
- **The search.** `SearchInput` / `searchHTML()` is the box with the
  magnifier. The `/` cap is the default and is decoration: the input names
  the shortcut in `aria-keyshortcuts`, and the page wires it with
  `initKeyboard({ onSearch })`. `kbd={false}` for a box with no shortcut (a
  modal's), `type="search"` for the browser's clear button. `width` is a
  preferred width (`--search-w`) that still shrinks; `fluid` grows to fill
  the row. The cap fades while the box has focus or words, and goes on a
  phone, where the box takes a line of its own. Attributes beyond the props
  land on the input; `class` on the wrapper.
- **Chips.** A chip is a filter that is on or off (`pressed`, which writes
  `aria-pressed`) or the trigger of a menu of values (`menu`: chevron,
  `aria-haspopup`, `aria-expanded`, `controls`; `active` while its menu has
  a value in force). Never both. `sm` (26px, `t-xs`) for presets and metric
  switches, `md` (32px, `t-sm`) for the filters of a list and the board.
  A leading `icon` or `dot`, a `count` after the label (bold, the chip's
  colour when on; `countId` for `setCount()`, `hideZero`). `labelId` lets a
  script rewrite the label of a menu chip.
- **Chips from values.** `FilterChips` / `filterChipsHTML(items, picked,
{ attr })` writes a named `role="group"` with one chip per value; each
  carries its value in `data-<attr>` (`data-value` unless the listener reads
  another name, such as `status` or `env`). `picked` is one value or a
  list. After a click, `setChipsPressed(root, picked, attr)` moves the
  pressed state.
- **Touch.** A chip takes a 44px tap through `::before`, as a button does;
  the search and a filter's control grow to 44px where the pointer is
  coarse.
- **Where.** Tasks, projects, project, Evidence, infra's
  containers bar and usage's filters draw FilterBar, FilterField,
  FilterChips and SearchInput; no page keeps its own toolbar rules.

Notes on drawers:

- **Only the shell.** Drawer is the box, its head, a body that scrolls and a
  foot. What it holds keeps its own classes, and what a drawer does beyond
  opening and closing (the task panel's expand and siblings, the chat's
  views) stays in its page.
- **Modal or docked.** By default a drawer sits in the modal's `.overlay`
  (`.overlay-drawer`), over the scrim, with the id on the overlay:
  `bindDrawer()` hands it to `bindModal()`, so the backdrop, the ✕, Escape
  and the Tab trap work as in a modal. `modal={false}` docks it to its edge
  with no scrim (`.drawer-docked`) and the id on the box: the page behind
  stays in use, nothing traps the focus, and the page lists it in its
  `onEscape` (`escapeCloses()`) where it must close. Closed, a docked drawer
  is `visibility: hidden` once it has slid out, so nothing in it takes the
  focus. A page places it (`top` under the header, `left` past the rail, a
  `width`) and no more.
- **Sides and widths.** `right` (default) and `left` take `size`: sm 400px (a
  conversation), md 460px (a list of switches, the default), lg 780px (a
  record read in depth, with the larger title); the tokens are
  `--drawer-w-*`, and on a phone every one is the screen. `bottom` is the
  phone's sheet at full width, rounded on top; a size on it throws.
- **Head.** The `eyebrow` slot (`eyebrowHTML`) goes over the title: crumbs, a
  key and a state. `sub` is a line under the title and the drawer's
  description (`aria-describedby`); `""` writes it empty for a script to
  fill, as `title=""` does for the heading. `actions` (`actionsHTML`) are lg
  icon buttons before the ✕, which is lg too and on by default
  (`closable={false}` drops it). The `head` slot (`headHTML`) stacks under
  the row: meta, a bar of buttons. The head has a rule under it. `titleId`
  keeps a heading the page already names by its id, and `titleAttrs` puts
  attributes on it (the task panel's title, edited in place).
- **Bar.** The `bar` slot (`barHTML`) sits between the head and the body and
  does not scroll: the task panel's tab strip, which draws the head's rule
  in its place.
- **Body.** `padded` by default; `flush` for a list edge to edge; `frame` for
  a body whose content scrolls itself (a chat feed over its composer), so two
  scrollers do not fight.
- **Foot.** `footNote` is a short note on the left; the `foot` slot
  (`footHTML`) holds the buttons on the right. A button that only closes
  carries `data-modal-close`.
- **Resizable.** `resizable` writes `.drawer-resizer` on the inner edge, a
  focusable separator named "Resize panel". `bindDrawerResize(box, {
storageKey, min, keep })` makes it work: drag, the arrow keys (16px, 48px
  with Shift), Home and End to the narrowest and widest, a double click to
  go back to the page's width; the width is reported in `aria-valuenow` and
  remembered under `storageKey`. `keep` is how much of the page always stays
  visible. Hidden on a phone.
- **Motion.** A modal drawer comes in with one `@keyframes octopus-drawer-in`
  from its own side; a docked one slides. Both go still with reduced motion
  through the duration tokens.
- **Parity.** `parity-drawer.test.ts` renders Drawer with the Astro
  container and compares it with `drawerHTML()`.
- **Two views, two heads.** A drawer that switches between views writes a
  `DrawerHead` per view (`drawerHeadHTML()` from a script): the chat's box
  is a docked left drawer (`drawerClasses()`), its new-chat form and its
  conversation each have a head with a back button in the `lead` slot
  (`drawerBackButton()`, as tall as the ✕). A head with a lead centres its
  heading on the buttons' height. The `sub` slot takes markup (a badge and a
  link) where the `sub` prop takes text. The task panel is a docked Drawer,
  its properties on a phone a bottom one, and the Evidence and agents
  drawers are Drawers too.

### Charts

| Family  | Astro                                                                                                                | TypeScript                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | CSS classes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Use it for                                                                          | Status |
| ------- | -------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------- | ------ |
| `chart` | `Chart.astro` (`id`, `size`, `label`, `interactive`; default slot); `ChartLegend.astro` (`items`, `placement`, `id`) | `chartHTML()`, `chartLegendHTML()`, `chartLegendItemsHTML()`, `setChartLegend()`, `chartTipHTML()`, `chartEmptyHTML()`; engines `timeChart()` + `bindCrosshair()`, `barChart()` (and `stackedBarChart()`), `fluidChartHTML()` with `fluidLinesSVG()`/`fluidBandsSVG()`, `sparklineSVG()`, `chartBarsHTML()` (and `chartBarRowHTML()`); `chartPanelsHTML()`, `chartPanelClass()`, `chartRuns()`, `stackTotals()`; `chartTip()`, `placeChartTip()`, `niceMax()`, `countMax()`, `chartWindow()`, `timeTicks()`, `nearestIndex()`; the `*Classes()` and `chartA11y()` helpers, in `lib/ui/chart.ts` | `.chart`, `-sm`, `-lg`, `-xl`, `-auto`, `-fluid`; `.chart-plot`, `.chart-axis`, `-axis-y`, `-axis-x`; `.chart-bars`, `-bars-row`, `-bars-label`, `-bars-label-mono`, `-bars-value`, `-bars-sub`; `.chart-panels`, `.chart-panel-full`; marks `.chart-grid`, `-tick`, `-line`, `-wash`, `-band`, `-cross`, `-dot`, `-bar`/`-hit`, `-spark`/`-spark-line`; `.chart-tip`, `-tip-left`, `-tip-time`, `-tip-row`, `-tip-value`, `-tip-label`; `.chart-legend`, `-legend-head`, `-legend-body`, `-legend-item`, `-legend-value`; `.chart-key`, `-key-square` | Every chart: its frame, its marks, the one tooltip and the one legend. Notes below. | ready  |

Notes on charts:

- **Variants.** `line`: `timeChart()` draws a line per series over a time
  axis. `area`: the same with a wash under a lone series (`wash` forces it
  on or off). `bars`: `barChart()` takes one `ChartBar` per slot (`label`,
  `title`, `parts`), stacked bottom up or, with `layout: "grouped"`, side by
  side against the tallest part; `stackedBarChart()` is the stacked one.
  `counts: true` is for whole counts (tasks, installs): the axis top is
  even (`countMax()`), so 0, half and top read whole numbers.
  `sparkline`: `sparklineSVG()` draws the trace a stat tile holds in its
  `spark` slot, in the tile's colour, and breaks the line where a value is
  missing. `fluid`: `fluidChartHTML()` for a chart drawn before its width is
  known (a custom dashboard's panels): the axes are HTML, the plot is a
  1000×100 viewBox stretched to the frame with `preserveAspectRatio="none"`
  and strokes that keep their width, so text and lines stay the page's size
  at any width. `fluidLinesSVG()` draws a line per run of values (a lone
  value is a dot), `fluidBandsSVG()` stacked areas (`.chart-band`). A fluid
  frame needs a height: `size="auto"` throws. `bar list`: `chartBarsHTML()`
  is a name, a Meter and a figure per row, for shares that need no axis; a
  `tone` of `viz-N` paints the bar in that series' colour, and `mono` sets
  a name the daemon sends as an identifier (Usage's stop reasons) in the
  code face. The Meter reads the figure out; when the figure alone has
  no unit or share (a bare count), `valueText` gives the Meter its own
  words ("40 sessions (64.5%)"). A script that
  mixes rows with lines of its own (a total, a note) appends
  `chartBarRowHTML()` rows to a `.chart-bars` box (Infra's storage,
  Postgres, Docker and node lists).
- **Panel grid.** `chartPanelsHTML()` lays chart panels out as many per line
  as fit at `--chart-panel-min`, one at 390px; `chartPanelClass(true)` makes
  a panel take the whole row.
- **Frame.** The engine draws at the width the page measures; the frame's
  `size` sets the height (sm 140px, md 180px, lg 220px, xl 260px from the
  `--chart-h-*` tokens; `auto` follows the viewBox). The frame is
  `role="img"`: give it the chart's one-line summary as `label`, or set
  `aria-label` when the data arrives. `chartEmptyHTML(text, { icon })` is the
  small dashed EmptyState stretched to the frame's height, for "warming up"
  or a range with no data.
- **Tooltip.** One `.chart-tip` per frame (`chartTip(host)` makes it), filled
  with `chartTipHTML({ time, rows })` and placed with `placeChartTip()`,
  which flips it to the left of the crosshair on the frame's right half.
  `bindCrosshair()` does all of it for a `timeChart()`; `fmtTime` picks the
  tooltip's time line (a date for a chart of days). With `interactive`, the
  arrow keys walk the samples, Home and End jump to the ends, Escape hides
  the readout.
- **Legend.** `placement="head"` sits at the end of a panel's or a block's
  head row (Panel's `aside` slot); `body` (default) is a line under the
  plot. A key is a line for a series drawn as a line, `swatch: "square"` for
  bars, bands and areas; `value` adds a figure after the name. An empty
  legend takes no room until a script fills it. A table row that names a
  series puts `chartLegendItemsHTML()` with that one series in its cell
  (the dashboard's table panel), so the key and the name keep the legend's
  spacing.
- **Engines.** `lib/infra.ts` re-exports `timeChart`,
  `bindCrosshair` and the rest with its `Point`, `lib/infra-cache.ts`'s
  `stackedBars()` is `stackedBarChart()` over its days, `lib/charts.ts`
  draws every dashboard panel with the fluid engine, the bar list, a
  StatTile or a Table, and `lib/minicharts.ts`'s `flowChartSVG()` is a
  grouped `barChart()`. The project page's spend chart is the same
  `timeChart()` as Usage's.
- **Filling a legend.** A legend on the page (a `ChartLegend` with no
  items) takes its keys from a script with `setChartLegend(legend, items)`,
  which writes only when they change, so a poll that redraws the chart
  leaves it alone. Infra fills the cache areas' legend in its panel's foot
  and hides the foot while no area has a key.
- **Where.** Infra's four activity charts and the cache bars sit in
  `Chart`/`ChartLegend`;
  Usage draws its cost or tokens per day with `timeChart()` through
  `lib/usage-chart.ts`, which fills the days the daemon leaves out with
  zeros. The legends of `lib/charts.ts` and `lib/minicharts.ts` are
  `chartLegendHTML()`.

### Content

| Family         | Astro                                                                                                      | TypeScript                                                                                                                                                                                                                                 | CSS classes                                                                                                                                                                                                               | Use it for                                                                                                                                                                                                                          | Status |
| -------------- | ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------ |
| `code`         | `CodeBlock.astro` (`text`, `multiline`, `wrap`, `maxHeight`, `tone`, `copy`, `label`, `textId`)            | `codeBlockHTML()`, `codeBlockClasses()`, `codeTextAttrs()`, `codeCopyAttrs()`, `codeCopyButtonHTML()`; `bindCodeCopy()`, `copyCodeBlock()`, `copyText()` in `lib/ui/code.ts`                                                               | `.code-inline`; `.code-block`, `-line`, `-multi`, `-wrap`, `-h-sm`, `-h-lg`, `-danger`, `.code-block-text`, `.code-block-copy`                                                                                            | An identifier inside a sentence (`.code-inline`); a secret, a command, a snippet or a log in a box, with Copy. Notes below.                                                                                                         | ready  |
| `conversation` | notes                                                                                                      | `messageHTML()`, `composerHTML()` and notes                                                                                                                                                                                                | notes below                                                                                                                                                                                                               | Rendered markdown, chat bubbles, the box to write in.                                                                                                                                                                               | ready  |
| `steps`        | `StepList.astro` (`steps`, `compact`, `inline`)                                                            | `stepListHTML(steps, opts)`, `stepItemHTML()`, `stepFinishHTML()`, `stepListClasses()`, `stepItemClasses()`, `stepItemAttrs()`, `stepMark()` in `lib/ui/steps.ts`; a step is `{ title, state, href, detail, note, finish, action, attrs }` | `.step-list`, `-compact`, `-inline`; `.step-item`, `-done`, `-now`, `-pending`, `-blocked`, `-unknown`; `.step-mark`, `.step-body`, `.step-title`, `.step-action`, `.step-detail`, `.step-note`, `.step-finish`           | A checklist of steps with their state: onboarding (edge to edge in a flush panel) the first steps on the Panel (`compact`) and a process under way in one row (`inline`).                                                           | ready  |
| `disclosure`   | `Disclosure.astro` (`summary`, `variant`, `ruled`, `open`, `bodyId`; slots: default = the body, `summary`) | `disclosureHTML()` (`summaryHTML`), `disclosureClasses()`, `DISCLOSURE_CARET` in `lib/ui/disclosure.ts`                                                                                                                                    | `.disclosure`, `-help`, `-code`, `-fields`, `-row`, `-ruled`; `.disclosure-summary`, `.disclosure-caret`, `.disclosure-title`, `.disclosure-body`                                                                         | A details/summary with a sprite caret that turns: help under a panel (`help`), a raw payload or a log (`code`, its body a CodeBlock), a form's optional fields (`fields`), a line of a list that opens in place (`row`). No script. | ready  |
| `dropzone`     | `DropZone.astro` (`id`, `title`, `sub`, `icon`, `accept`, `overlay`)                                       | `dropZoneHTML()`, `dropZoneAttrs()`, `dropAccepts()`, `dropKind()`, `bindDropZone()`, `bindDropOverlay()`, `resetDropZone()`; `uploadPlanHTML()`, `uploadReportHTML()`, `uploadLeftOutHTML()` in `lib/ui/dropzone.ts`                      | `.drop-zone` (`.over`), `.drop-zone-overlay`, `.drop-ico`, `.drop-title`, `.drop-sub`, `.drop-actions`; `.upload-plan`, `.plan-line`, `.plan-note`, `.upload-paths`, `.upload-rules`, `.upload-progress`, `.upload-meter` | The box a project's folder, a .zip or a .json file is dropped on, and the plan under it: what goes up and what stays out. As an overlay, the sign that a container takes the file dragged over it.                                  | ready  |

Notes on code, steps, disclosures and the drop zone:

- **Copy answers once, on the button.** A CodeBlock's Copy button carries no
  value: `bindCodeCopy()` copies the text node of its own block, so a secret
  a flow clears from the page is gone for good. Layout.astro binds the
  document; login, which has no Layout, calls it and imports `code.css` and
  `button.css` itself. The button reads Copied for two seconds, in an
  `aria-live` label; there is no toast, on any page. Over plain http (a
  daemon on a LAN address) the Clipboard API is missing, so `copyText()`
  falls back to the copy command; if that fails too the text is left
  selected and the button says to press Ctrl+C (⌘C on a Mac). The attribute
  is `data-code-copy`: `data-copy` is the task drawer's branch button,
  which copies its own value.
- **Line or block.** One line (a key, a command) is a `<code>` that keeps
  its shape, scrolls sideways and selects whole on a click; `wrap` breaks it
  instead. `multiline` is a `<pre>` that wraps; `maxHeight` caps it (sm about
  eight lines, lg about twenty). A block that can scroll is a tab stop.
  `tone="danger"` is the log of what failed. A block a script fills later
  leaves `text` empty and names the text element with `textId`.
- **Steps.** Each mark is a sprite glyph (check, half circle, circle, lock,
  question mark) with the state's name for screen readers; the colour only
  repeats it. `unknown` is a step whose check failed, so nobody knows yet. A done step's
  title goes quiet, never struck through. `href` makes the title a link;
  `finish` is the way to finish it, `{ text, href, command }`, with the
  command after the link as `.code-inline`, never inside it. `setupStepItem()` in
  `lib/onboarding-steps.ts` turns a checklist step into a StepItem for
  onboarding, and `firstStepItems()` in `lib/dashboard.ts` does it for the
  Panel's compact list. `action` is a link button after the title
  (Button `link`): a page by `href`, or a hook the page binds by `attrs`,
  such as the agent editor's `data-check-tab`.
- **Under way.** `now` marks the step happening at this moment: a half
  circle in the accent that breathes (and holds still under reduced motion), and
  `aria-current="step"` on its `<li>`. `inline` lays the steps out in one
  wrapping row of small type, titles only, joined by a short rule, for a
  process shown beside the thing it happens to: `sandboxStepsHTML()` in
  `lib/evidence-html.ts` writes a Sandbox's bring-up this way. A pending step after the one under way waits
  its turn in grey; with no step under way, the list is a checklist in one
  row: a pending step is still to do and keeps the amber, and no rule joins
  the steps. That is how the agent editor shows what an agent needs before
  it runs, each title with its action on the same line. A title longer than the row wraps.
  `compact` and `inline` exclude each other.
- **Disclosures.** `help` is the quiet small type of a hint, and `ruled` sets
  it off from the panel above; `code` has a small summary over a CodeBlock;
  `fields` folds a form's optional fields and keeps the form's type (the
  GitHub App's More options, a new board's Advanced, a subtask's More
  options). `row` is a line of a list that opens in place, such as a task's
  test case: the summary is the whole line, padded like a ListRow, with its
  hover fill and an inset focus ring, and `summaryHTML` (the `summary` slot)
  writes its parts; a part with `.disclosure-title` keeps to two lines until
  the row opens. In a `.row-list` the rows share its rules. A row that is its own card (a
  pull request) keeps its markup and takes the caret:
  `icoSvg(DISCLOSURE_CARET)`, turned 90° while open.
  The callout's own `details.callout-detail`
  stays with the callout.
- **Drop zone.** A named group, not a tab stop: its pick buttons are the
  keyboard's way in. `bindDropZone()` opens the inputs, hands a picked
  folder (`onFolderPick`), a .zip picked or dropped (`onZip`) and a dropped
  folder (`onFolderDrop`, for `pickedFromDrop()`) to the page, and keeps
  `.over` while something is dragged over the zone, not while the pointer
  moves between its children. `uploadPlanHTML(plan, { zip, history })`
  fills the `.upload-plan` beside it: `history: "keep"` badges a new
  project's git history, `"ignore"` says a new version ignores the folder's
  `.git`. `uploadReportHTML(report, { lead })` writes what the daemon took.
- **One .json file.** `accept="json"` takes a single file, picked or
  dropped, and hands it to `onJson`; the handler parses it and says when it
  is not JSON, so a file named otherwise still reaches it.
- **Overlay.** `overlay` writes the zone with no buttons and no inputs, to
  cover a container whose own button picks the file. Write it `hidden`;
  `bindDropOverlay(container, zone, { enabled, onJson })` shows it while
  files are dragged over the container (counting dragenter and dragleave,
  which fire for every child) and hands the drop over. `enabled` keeps it
  out of the way while the person cannot write or an import runs. The
  Workshop's import uses it.

Notes on conversation (`styles/primitives/conversation.css`, `lib/ui/conversation.ts`):

- **Markdown.** `Markdown.astro` (`compact`; the rendered HTML in the slot)
  and `mdHTML(html, { compact, id, cls, attrs })` write `.md`, the one set of
  rules for every rendered markdown: descriptions, comments, reports, cases,
  chat bubbles. `compact` (`.md-compact`) is the smaller text and tighter
  spacing of a comment or a bubble. It styles what `lib/markdown.ts` and
  `lib/chat-markdown.ts` write (`.md-table`, `.md-task`, `.md-file`,
  `.md-media`, `.md-details`, `.md-empty`). The block has no box: the panel
  or the bubble around it draws one. What goes in is the renderer's
  sanitized output, never raw input.
- **Message.** `Message.astro` (`role`, `tone`, `author`, `text`, `lead`,
  `fold`, `meta`; slots: the body as markdown, `after`, `meta`) and
  `messageHTML()` with the same options, where `bodyHTML` is the body as
  markdown and `afterHTML` and `metaHTML` the slots; `messageEl()` is the
  message as an element, for a feed built with the DOM. Roles: `user`
  (right, the accent's tint), `agent` (left, a raised surface), `system` (a
  quiet italic line across the feed: why a message waited, how a session
  resumed). `tone: "error"` is red with the warning glyph. `fold` folds the
  body under a summary and draws the message dashed and full width, for
  context nobody typed (the brief of a mention, a recording's
  instructions); `lead` is its bold first line. Every message opens with a
  visually hidden speaker ("You:", "dev:", "Note from Thulu:", "Error:"),
  since the side and the colour only tell the eye. `afterHTML` holds what
  follows the body (the tools a reply used, a pending action); tool cards
  between messages (`.lv-tool`) stay the feed's own.
- **Composer.** `Composer.astro` and `composerHTML()` take `variant`
  (`comment`, `thread`, `chat`), `id`/`ids`, `label` or `labelledBy` (one is
  required), `placeholder`, `rows`, `value`, `audience`, `attach`, `cancel`,
  `submitLabel`, `submitTitle`, `ready`, `as`, `inputCls`, and markup for
  the start of the foot (the `extra` slot, `extraHTML`: the notify check,
  the interrupt check, the delivery note). A comment is one row that grows
  with Attach and Post; a thread reply adds Cancel; a chat is a form with
  the send glyph beside the box, a tap wide. The parts carry
  `composer-input`, `-audience`, `-error`, `-atts`, `-attach`, `-file`,
  `-cancel`, `-submit`; with an `id` they also get `<id>-<part>`. The
  textarea is described by the audience and the error lines.
- **Composer behaviour.** The page keeps what a composer does (mentions,
  drafts, uploads, sending). The shared pieces: `composerParts(root)` finds
  the parts; `composerSubmits(event, variant)` says whether a key sends it
  (Enter in a chat, Shift+Enter a new line; Cmd or Ctrl with Enter in a
  comment or a reply; never while an input method composes), and the
  submit names those keys in `aria-keyshortcuts`;
  `setComposerReady(root, on)` turns the submit primary once there is
  something to send; `growComposer(textarea, maxLines)` grows the box
  through `--composer-h`. A file dragged over the box: the page sets
  `.drag-over` on the composer.
- **Parity.** `src/components/parity-conversation.test.ts` renders the
  three components and compares them with their twins.

## Adding to a family

1. Write the classes in `styles/primitives/<family>.css`, with tokens only and
   hover, `focus-visible` and disabled states.
2. Give the Astro component the prop and `lib/ui/<family>.ts` the option, and
   make both emit the same markup. Test the function's HTML with vitest.
3. Move the call sites over and delete the page rules they replace.
4. Update the row above.
