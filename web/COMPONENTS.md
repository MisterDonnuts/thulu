# Components and shared CSS

> The current catalog of components, with the TypeScript twin and the CSS
> class of each, is [`src/components/README.md`](src/components/README.md).
> This file holds the rules behind it: where a style lives and why.

The dashboard is Astro with no UI framework: one inline
`<script type="module">` per page, shared styles in `src/styles/`, and
components in `src/components/`.

## The two-layer rule

Every shared style is in one of two places, and which one is not a matter of
taste:

- **`src/styles/*.css`, global.** Anything a class name can land on from
  JavaScript. The pollers on `mcps`, `sandboxes`, `credentials`, `agents`,
  `automations` and `usage` rebuild rows, badges, buttons and panel bodies with
  `innerHTML`. Astro scopes a bare `<style>` by stamping `data-astro-cid-*` on
  the elements it finds _in the template_; markup a poller creates at runtime
  never carries that attribute, so a scoped `.badge` would style the static
  half of the page and miss the half the poller draws.
- **The component's own `<style>`, scoped.** Anything whose markup is written
  in the component and nowhere else: `PageHead`, `Field`, `SearchInput`. Astro
  scopes these for free.

Slotted content is the exception inside a component: it carries the _page's_
scope, not the component's, so reaching it needs `:global(...)`. `PageHead`
does that for the `<code>` chips pages put in their subtitle; `Field` does it
for the control it wraps.

## The layer contract

`Layout.astro` pulls the four stylesheets in as cascade layers:

```css
@layer octopus.tokens, octopus.base, octopus.primitives, octopus.page;
@import "../styles/tokens.css" layer(octopus.tokens);
...
```

A layered rule always loses to an unlayered one, whatever the source order.
That matters because the order is not ours to choose: Astro emits a large
page's CSS chunk _before_ the Layout's chunk, so `settings.css` is linked ahead
of `Layout.css` and a plain `.page { max-width: 1180px }` in the shared sheet
would have overridden settings' own. With layers, every page's
`<style is:global>` beats the shared base by construction.

The four sheets, in layer order:

| File | What it holds |
| ---- | ------------- |
| `styles/tokens.css` | Three layers. Theme: every colour, shadow and glow by role (`--color-*`, `--shadow-*`, `--glow-*`, `--tag-*`, `--brand-*`, `--viz-*`), dark is the base and the light theme redefines the whole block (twice: `data-theme="light"` and a light OS). Scale: type, space, radii, motion, z-index, fonts. Aliases: `--surface-*`, `--fg-*`, `--line*`, `--accent*`, the status names and the compatibility block, all pointing at the theme. Plus the `prefers-reduced-motion` block. |
| `styles/base.css` | Reset, `html`/`body`, the mono face, focus ring, selection, scrollbars, the `octopus-*` keyframes, `.ico`, `.muted`, `.mono`, `.truncate`, `.hidden`. |
| `styles/primitives.css` | Buttons, panel/card/row, badge, chips, inputs, overlay/modal, confirm dialog, toast. |
| `styles/page.css` | `.page` (width via `--page-max`), `.list-table`, `.row-empty`. |

`Layout.astro` keeps only the header's styles, which is Layout's own markup.

## Behaviour: component script or lib module

Two components need JavaScript. Both are `lib/` modules rather than
component `<script>` blocks, for different reasons.

**`lib/modal.ts` — `openModal` / `closeModal` / `bindModal`.** A component
script runs once per component instance and owns its element; that fits a
widget that opens itself. These modals never open themselves. `project.astro`
holds 5 of them and the page code already knows when each should appear, what
to prefill, and which field to focus. A script inside `Modal.astro` would have
to expose that back to the page through events or globals, which is more
machinery than `bindModal(overlay)` returning `{ open, close, isOpen }`.

`bindModal` handles outside-click and the `✕`/`[data-modal-close]` buttons.
**It deliberately does not bind Escape.** `lib/keyboard.ts` owns the page-wide
Esc cascade (drawer, then modal, then search box, then selection); a second
listener inside the modal would close two layers on one keypress.

`openModal` defers focus with `requestAnimationFrame` because the overlay is
`display: none` until the class lands, and focusing a hidden input does
nothing.

**Badges are strings, not components.** `lib/badges.ts` writes the same markup
and the same tone classes that `Badge.astro` renders, because almost every
badge in the dashboard is built by a poller. `Badge.astro` takes its tone type
from that module so the two cannot drift.

## Class naming

One convention, applied to everything touched: lowercase, hyphenated,
`block-element` with the block first — `.page-head`, `.page-head-actions`,
`.modal-foot`, `.field-label`, `.search-kbd`. Variants are a modifier class
beside the base, not a compound: `.btn .btn-primary`, `.badge .badge-ok`,
`.modal .modal-plain`. No BEM double underscores, no utility soup, no
`!important`.

## Verifying a change here

Everything below runs from the repo root:

```
npm --prefix web run build
npm --prefix web run typecheck
npm --prefix web run test
make lint-web
cd web && npx prettier --check src
```

For visual regressions, the build's own output is the thing to diff: for each
page in `web/dist`, concatenate the stylesheets it links in link order, flatten
to last-wins declarations per selector (applying layered rules before unlayered
ones), and compare against the same flattening from before the change. Note
that this catches a _changed_ declaration but not a _shadowed_ one: a page
rule that still exists but now loses to a more specific shared selector.
