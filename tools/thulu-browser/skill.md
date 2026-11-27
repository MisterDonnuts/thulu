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

To see the page the way a slow phone or a dark-mode user sees it:

```
browser_new_session(task_id=$TASK_ID, network="slow-4g", cpu_throttle=4, color_scheme="dark")
```

- `network`: `fast-4g`, `slow-4g`, `3g` or `offline`, Chrome DevTools' presets. The latency applies to every request, so a page that fires ten requests in a row pays it ten times.
- `cpu_throttle`: slowdown factor from 1 to 20, relative to the machine Chromium runs on. Numbers from different machines don't compare.
- `color_scheme`: `light` or `dark`, the `prefers-color-scheme` the page sees.
- `ignore_errors`: regular expressions for page errors that are not yours (a third-party script, an analytics host). They are not collected.

A take filmed in the session keeps the profile, and every replay of it applies it again.

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

## Timing and page errors

Every navigation and interaction is timed, and the answer of each one says how it went: `Response 420 ms; settled at 910 ms (2 requests).` The response is the time from the input to the last DOM change before the page settles (no new request for 500 ms and no DOM change for 300 ms). A click that changes nothing and sends no request comes back as `No response`: the button is dead, or the handler failed. Each answer also lists the page errors the action caused.

### `browser_metrics`

```
browser_metrics(session_id, format="json")
→ { profile: { network: "slow-4g", cpu_throttle: 4, viewport: {...} },
    page: { url, ttfb, fcp, lcp, cls, inp },
    steps: [{ step: 3, tool: "browser_click", what: "#pay", response_ms: 640, settled_ms: 1210, requests: 2, dead: false, lcp: 1830, ... }],
    findings: { http: 1, failed: 0, console: 2, pageerror: 0 } }
```

TTFB, FCP, LCP, CLS and INP come from the `web-vitals` library injected before any page script. A full navigation resets them; a client-side route change usually does not. INP shows as `under 16 ms` when there were interactions and all of them were faster than what the browser reports. Without `format` the same comes as text for reading, the last 20 steps.

These are lab numbers from one run in this browser, not what users see in the field. To say a change made the page slower, measure main and the branch the same way (same profile, same flow) and compare; an absolute threshold against web.dev means little at `cpu_throttle=4` on a shared machine.

### `browser_findings`

Console errors, uncaught exceptions (`pageerror`), responses 4xx/5xx and requests that failed, grouped by the step that was running when they arrived. Repeats fold into one line with `×N`; at most 50 per step and 200 per session. URLs lose their query string and typed secrets come out masked.

A finding never fails a check on its own. A 500 after "Pay" is worth reporting; a 404 for a tracking pixel usually isn't (add it to `ignore_errors`).

---

## Accessibility

### `a11y_scan`

```
a11y_scan(session_id, include=["#checkout"], exclude=["#chat-widget"], fail_on="serious")
→ Accessibility of https://shop.example/cart (axe 4.14.0, wcag2a, wcag2aa, wcag21a, wcag21aa, wcag22aa)
  Violations: 1 critical · 2 serious · 0 moderate · 3 minor
  Fails at serious or worse: 2 rules.
  - FAIL [critical] label: Form elements must have labels. 1 element(s): #coupon. https://dequeuniversity.com/...
```

Runs axe-core on the page as it is now, WCAG 2.2 AA by default (`tags` changes it). `fail_on` is the lowest impact that fails: `serious` by default, so critical and serious fail and moderate and minor are listed below the threshold. `disable_rules` skips a rule (say why in the report). `task_id` attaches the full axe JSON; `shot=true` adds a capture with up to six failing elements framed in red.

Inside a take, `a11y_scan` is a step (`{ "kind": "a11y", "case": "TC-5", "include": ["@form"] }` in the JSON): it shows a pass/fail card in the video and runs again on every replay. Scan after the page has the state you care about (form open, error shown), not only on load.

axe finds part of what WCAG asks: names, labels, contrast, roles, structure. A clean scan is not a certification; keyboard use and a screen reader still need a person, and the report should say so.

---

## Blind user

`browser_new_session(blind=true)` gives a session for a tester that must find its way as a person would, without reading the code:

- `browser_get_state` lists what a screen reader announces: `[3] button "Save" (disabled)`, `[4] link "Help" (goes to docs.example.com)`. No ids, names, classes or paths. Click by index.
- `browser_navigate` works once, for the start URL. After that, move through the page or with `browser_go_back`.
- `browser_run_script`, `browser_list_scripts`, `browser_export_script` and `browser_get_session_log` are refused.
- `evidence_note(level="think")` says what you expect and what confuses you, as you go. It shows as a subtitle in the video and in the report's Think-aloud section with its second.
- `browser_metrics` returns `journey`: actions, navigations, backs (`browser_go_back` or coming back to a page already seen), dead clicks, page errors and time on task. Comparing it with the shortest path is the job of whoever has the spec.

`browser_go_back` also works in a normal session. It cannot be recorded in a take.

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
- `evidence_verify(case="TC-4", check="perf", metric="lcp", expected="2500")` passes when the metric is at most `expected` (ms; CLS without unit). `metric="response"` reads the last action's response time. The value is measured again on every replay, with the take's profile, and in a before/after the report shows the difference.
- The report lists the page errors by step and, when the take is throttled or has a perf check, a timing table per run. `evidence_render(network=..., cpu_throttle=..., color_scheme=...)` overrides the take's profile for one replay (`network="none"` or `cpu_throttle=1` clears it).
- The app's waits (a slow navigation, a spinner, a check that polled until it passed) are cut from the video and marked: a ⏱ pill on the next frame, `· ⏱ 3.2 s` on the chapter and in the VTT, and the total on the report's first line. `evidence_start(cut_waits=false)` (or `"cut_waits": false` in the take, or `evidence_render(cut_waits=false)`) films them in real time, for a take about how slow the flow feels.
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
