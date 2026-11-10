Headless Chromium (Playwright) behind an MCP. Two modes: stateless and persistent sessions with full control of the browser.

## Typical usage flow

```
browser_new_session → browser_navigate → browser_get_state → browser_click / browser_type / ... → browser_screenshot → browser_close_session
```

If the flow was worth keeping and you want to reuse it:
```
browser_export_script → you save the .js in your repo → later runs use browser_run_script(name: "...")
```

---

## Sessions

### `browser_new_session`

Creates an isolated `BrowserContext` (cookies, localStorage, history separate from everything else).

**Always pass `instance_id` + `agent_type`**, needed to:
- Persist screenshots to the output dir
- Resolve scripts by name (convention `<agents_root>/<agent_type>/scripts/browser/`)

```
browser_new_session(instance_id=$INSTANCE_ID, agent_type=$AGENT_NAME)
→ session_id: "abc-123-..."
```

Sessions expire after 10 min of inactivity. Always call `browser_close_session` when you're done.

### `browser_close_session`

Closes the context and frees memory. The interaction log is lost; export it first if you need it.

---

## Navigation and reading

### `browser_navigate`

Navigates to a URL. `wait_until` default: `networkidle` (waits for the network to go quiet, good for SPAs).

**Critical rule:** if the URL points to a sandbox, **use the `url` field that `preview_url` returns** (it contains `host.docker.internal:<port>`), **never `localhost`**. The browser runs in its own container; `localhost` points to the browser's container.

### `browser_get_state`

The most important tool. Returns every interactive element on the current page, numbered:

```
[1] <button> "Sign in"
[2] <input type="email"> placeholder="you@example.com"
[3] <input type="password"> placeholder="Password"
[4] <a href="/signup"> "Create account"
```

Use the index for click/type/select/hover. **Call get_state after every navigation or interaction that changes the DOM**: the indices are recomputed over the current DOM.

### `browser_get_text`

Visible text of the current page (post-JS). Useful for extracting content after an interaction.

### `browser_screenshot`

Screenshot of the current page. If the session has `instance_id` + `agent_type`, it persists the PNG to the output dir and auto-attaches it to the task. Pass `filename` to name it; otherwise it uses a timestamp.

---

## Interaction

Every interaction tool accepts `index` (from get_state) **or** `selector` (CSS). Prefer the index, it's simpler. Use a selector when you know exactly what you're looking for and want it to keep working through layout changes.

### `browser_click`

Clicks an element. Returns the resulting URL (it may have navigated).

### `browser_type`

Types text into an input/textarea. `clear_first: true` by default (clears before typing). For forms, prefer `browser_type` + `browser_press_key("Enter")` over looking for the submit button.

For passwords and tokens pass `secret: "password"` (the name of what it is): the value stays out of the log, the response and any take.

### `browser_select`

Selects an option in a `<select>`. Pass the value or the visible label.

### `browser_scroll`

Scrolls the page. `down`/`up` move `amount` pixels (default 500). `top`/`bottom` go to the end. Useful for loading lazy-loaded content or for making elements outside the viewport show up in the next `get_state`.

### `browser_hover`

Hovers over an element. For dropdowns and menus that only open on hover. After hovering, call `browser_get_state` to see the newly revealed elements.

### `browser_press_key`

A keyboard key. The most used:
- `Enter`: submit a form without looking for the submit button
- `Escape`: close a modal
- `Tab`: move focus to the next field
- `ArrowDown` / `ArrowUp`: navigate lists/dropdowns

---

## Scripts

### `browser_run_script`

Runs a full Playwright script in the session. Three modes:

**By name** (directory convention):
```
browser_run_script(session_id, name="login", params={ url: "http://..." })
```
Loads `/agents-shared/<agent_type>/scripts/browser/login.js`. Requires `agent_type` on the session.

**By path** (explicit):
```
browser_run_script(session_id, path="/agents-shared/developer/scripts/browser/login.js")
```

**Inline**:
```
browser_run_script(session_id, code="await page.goto('http://...'); return await page.title();")
```

**Script format**, two variants:

```js
// description: Log in to the Thulu dashboard
// Variant 1: module with export default
export default async function run(page, params = {}) {
  await page.goto(params.url || 'http://host.docker.internal:4321');
  await page.fill('input[name="email"]', params.email || 'admin@example.com');
  await page.fill('input[name="password"]', params.password);
  await page.keyboard.press('Enter');
  return await page.title();
}
```

```js
// description: Take a screenshot of the dashboard
// Variant 2: plain body (no export)
await page.goto(params.url);
await page.waitForLoadState('networkidle');
return await page.title();
```

The first line must have `// description: ...`; `browser_list_scripts` uses it.

### `browser_list_scripts`

Lists the available scripts. Resolves the directory on its own if the session has `agent_type`.

---

## Recording

### `browser_get_session_log`

JSON log of every interaction in the session:
```json
[
  { "t": 1234567890, "tool": "browser_navigate", "args": { "url": "..." } },
  { "t": 1234567891, "tool": "browser_click", "args": { "index": 3 }, "elementInfo": { "tag": "button", "selector": "#login-btn", "text": "Log in" } }
]
```

### `browser_export_script`

Generates a reusable `.js` script from the log. It uses the CSS selectors captured at the moment of each interaction.

```
browser_export_script(session_id, path="/agents-shared/developer/scripts/browser/login.js", description="Log in to the app under test")
```

If there are elements with no stable selector (clicks by index with no id/name/class), the export adds `// TODO:` comments; review and fill in those cases before using the script.

**Full reuse loop:**
1. The agent does the interactive flow once with the browser_* tools
2. `browser_export_script` → script saved in the agent's repo
3. Later runs: `browser_run_script(name: "script-name")`

---

## Filmed evidence

To show a person that something works (or that it doesn't), film a take instead of taking loose screenshots or recording the whole session with `record_video`.

```
browser_new_session(task_id=$TASK_ID) → browser_navigate
evidence_start(title="SHOP-123 · the coupon lowers the total")
  browser_type / browser_click / ... with label="what it shows, in the ticket's words"
  evidence_verify(case="TC-1", expected="$90", check="text", target="#total")
  evidence_shot(name="total", spotlight=[{target: "#total", note: "10% off"}])
evidence_finish → you look at the contact sheet → evidence_publish(render_id)
```

- While the take is recording, each `browser_*` is filmed with a cursor, a ring and a subtitle, and annotated with a stable selector. The time you spend thinking between calls is not filmed.
- `evidence_verify` with `check` + `target` is measured by the tool; `observed` is for what only you can judge.
- `evidence_finish` returns a draft: nothing reaches the task until `evidence_publish`. If the contact sheet doesn't read well, fix it and film again.
- `evidence_publish` records each verdict whose `case` is the key of one of the task's test cases (`TC-1`) as a run of that case, with the video, the take, the captures and the report it published in the Reports tab. Several verdicts for the same case make one run, `pass` only if all of them pass; in a before/after the after counts. The response lists the recorded runs and the keys the task doesn't have. `record_runs=false` records nothing.
- A take is JSON (`evidence_get_take`). Saved in the repo (`.octopus/evidence/takes/`), `evidence_render` films it again in a clean browser, and with `compare_base_url` it films the before and the after side by side.
- If the flow creates or modifies data, `evidence_start(changes_data=true)`: the take is flagged and a replay doesn't repeat it unless `allow_side_effects`.
- In a recording chat of the Evidence library: `evidence_recording` says which case to record and in which environment, `browser_type(secret="NAME")` without `text` types a test login you never see, and `evidence_save_case(take_id=...)` saves the take as the case's recording. The `octopus-evidence-library` skill walks through it.

---

## Typical errors

- `ERR_CONNECTION_REFUSED` → you passed `localhost` instead of `host.docker.internal`. The browser runs in its own container.
- `ERR_NAME_NOT_RESOLVED` → the host doesn't exist or DNS is broken.
- `No element at index N` → the DOM changed. Call `browser_get_state` again.
- `Session 'x' not found` → the session expired (10 min of inactivity) or was already closed.
- Timeout → the page is slow to load. Try `wait_until: "load"` instead of `networkidle`.

## Operating rules

1. **Always close the session** when you're done (`browser_close_session`). They are resources of the shared Chromium.
2. **Call `get_state` after every interaction** that may change the DOM: the indices are positional.
3. **Don't repeat calls in a loop without changing something**: if there's an error, read the message and act differently.
4. **Scripts for repetitive flows**: if you do the same thing more than once, export it as a script.
