# Frontend

The Thulu dashboard is an **Astro** application (static build, no SSR). It is compiled to `web/dist/` and embedded into the Go binary via `//go:embed all:dist` in `web/embed.go`. The SPA handler in `internal/api/static.go` serves it at `/`.

---

## Pages

| File | Route | Description |
|------|-------|-------------|
| `index.astro` | `/` | Landing page / dashboard home |
| `tasks.astro` | `/tasks` | Kanban board — one column per stage of the selected board (`GET /boards/{id}/stages`), with a board picker that appears once a second board exists; drag-and-drop |
| `agents.astro` | `/agents` | Running agent containers, logs, files |
| `projects.astro` | `/projects` | Project list + the "new project" form. Clicking a card opens the project's own page — this one never edits |
| `project.astro` | `/project/?id=…` | One project: flow & spend metrics, settings, GitHub access, stage files, worktrees. The static build has no dynamic routes, so the id travels in the query string |
| `sandboxes.astro` | `/sandboxes` | Active sandboxes, queue visibility |
| `settings.astro` | `/settings` | Workspace settings key-value editor |
| `login.astro` | `/login` | Sign-in buttons for the configured providers (OIDC, GitHub OAuth) |
| `auth/` | `/auth/*` | Auth callback pages (OIDC, GitHub OAuth) |

---

## `src/lib/api.ts`

REST client. All API calls go through here. Requests carry the session cookie (`credentials: "include"`), and writes add the `X-CSRF-Token` header from the `octopus_csrf` cookie.

```typescript
import { api } from '../lib/api';

// GET with type
const tasks = await api.get<Task[]>('/tasks');

// PATCH
await api.patch(`/tasks/${id}`, { stage: 'in_progress' });

// DELETE
await api.del(`/webhooks/${id}`);
```

On 401, the client refreshes the session once (`POST /auth/refresh`) and retries; if that fails it redirects to `/login/`. On a dev-mode install with no login the 401 is returned to the caller.

---

## `src/lib/auth.ts`

Session helpers. There is no password or token handling in the browser: the
session lives in HttpOnly cookies set by the daemon after the IdP sign-in, and
the page only reads the CSRF cookie.

```typescript
import { requireAuth, sessionInfo, refreshOnUnauthorized, logout } from '../lib/auth';

requireAuth();                   // no session: redirect to /login/?next=… (unless dev mode has no login)
const me = await sessionInfo();  // GET /auth/me: user, workspace_id, role; null without a session
await refreshOnUnauthorized();   // POST /auth/refresh, serialized across tabs; true when it worked
location.href = await logout();  // POST /auth/logout; returns the IdP logout URL or /login/
```

`authConfig()` reads `GET /auth/config` and `authDisabled()` tells whether this
browser holds the dev-mode session of a no-login install.

---

## `src/lib/reactive.ts`

Micro reactive layer for vanilla Astro pages (~230 lines, zero dependencies).

### `Store<T>`

Typed pub/sub for client-side state.

```typescript
import { Store } from '../lib/reactive';

const tasks = new Store<Task[]>([]);

// Subscribe
const unsub = tasks.subscribe((next, prev) => renderBoard(next));

// Update
tasks.set(await api.get<Task[]>('/tasks'));

// Functional update
tasks.set(prev => prev.filter(t => t.id !== deletedId));
```

### `startPoller`

One timer per page. Pauses automatically when the tab is hidden (via `visibilitychange`). Uses exponential backoff (doubles on error, cap configurable, default 60 s).

```typescript
import { startPoller } from '../lib/reactive';

const stop = startPoller({
  interval: 3000,        // poll every 3s on success
  maxBackoff: 30_000,    // cap at 30s on repeated errors
  firstDelay: 0,         // fire immediately
  skipWhen: () => dirty.hasOpenModal() || dirty.isDragging(),
  fetch: async () => {
    const data = await api.get<Task[]>('/tasks');
    taskStore.set(data);
  },
});

// Call stop() to cancel (e.g. on page unload)
```

### `reconcile<T>`

**The critical rule**: never use `innerHTML` on a list that gets polled. Use `reconcile` instead.

`reconcile` is a keyed DOM patcher. It moves/updates existing nodes in place instead of destroying and recreating them. This preserves:
- Focus state (user typing in a textarea)
- Event listeners
- Drag-and-drop state (`.dragging` elements)
- Inline forms (unkeyed nodes are left in place)

```typescript
import { reconcile } from '../lib/reactive';

reconcile<Task>({
  container: document.getElementById('task-list')!,
  items: tasks,
  key: t => t.id,
  create: (t) => {
    const el = document.createElement('div');
    el.className = 'task-card';
    el.innerHTML = renderTaskCard(t);
    return el;
  },
  update: (el, t, prev) => {
    // Only patch what changed — don't replace innerHTML
    setText(el.querySelector('.task-title'), t.title);
    toggleClass(el, 'is-blocked', t.is_blocked);
  },
  skip: (el, t) => el.classList.contains('dragging'),
  remove: (el, t) => el.remove(),
});
```

`skip` is called for dragged cards — they are left alone even if the poll returns updated data.

### `dirty` guards

Synchronous checks to decide whether a DOM region is safe to patch:

```typescript
import { dirty } from '../lib/reactive';

// Is any input/textarea/select focused?
dirty.isFocusInEditable()

// Is a modal open? (looks for .overlay.open)
dirty.hasOpenModal()

// Is a card being dragged? (looks for .dragging)
dirty.isDragging()
```

Usage pattern:

```typescript
taskStore.subscribe((tasks) => {
  if (dirty.isDragging() || dirty.isFocusInEditable()) return;
  reconcile({ container, items: tasks, ... });
});
```

### DOM helpers

```typescript
setText(el, value)              // el.textContent if changed
setAttr(el, name, value)        // setAttribute / removeAttribute if changed
toggleClass(el, name, on)       // classList.toggle
```

---

## EE badge

The main layout (`Layout.astro`) calls `GET /info` once on load. If `edition === "ee"`, it renders a small "EE" badge in the nav bar next to the version number.

---

## Build output

```bash
cd web
npm install
npm run build
# output: web/dist/   (static HTML + JS + CSS)
```

The Go binary embeds `web/dist/` at build time. The SPA handler serves `index.html` for any path that doesn't match an API route, enabling client-side routing.

---

## Auth page flow

1. `login.astro` calls `GET /auth/config` for the configured providers
2. It renders one button per provider — there is no password form; Thulu
   authenticates nobody itself
3. The button is a full-page redirect to `/auth/oidc/authorize?provider=…`
4. The provider sends the browser back to `/auth/oidc/callback`, which sets the
   session cookies and redirects to `next` (default `/`)
5. With no provider configured the page says so instead of showing a dead button

---

## Kanban drag-and-drop (tasks.astro)

- Cards use the HTML5 Drag and Drop API
- `.dragging` class is added to the card while dragging
- The `reconcile` `skip` function checks for `.dragging` to leave the card alone during polls
- Drop on a column calls `PATCH /tasks/{id} { stage: <targetStage> }`
- On drop: optimistically update local store, then re-poll to confirm
