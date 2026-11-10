# The daemon's MCP proxy

Every MCP server an agent uses goes through the daemon at `/mcp-proxy/<name>`,
except for two cases explained below. The agent's container knows only the
proxy's URL and its own run key; the server's real URL and the credential it
authenticates with stay in the daemon.

- Code: `internal/mcpproxy` (forwarding, allowlist, credentials),
  `internal/api/handle_mcp_proxy.go` (identity and permissions),
  `internal/mcpoauth` and `internal/api/handle_credentials_oauth.go` (OAuth
  connections), `internal/mcp/launchenv.go` (what each run receives).
- General MCP wiring: [`mcp-integration.md`](mcp-integration.md).

## Contract

### What the container receives

The dispatcher and the chat runtime write a single variable,
`OCTOPUS_MCP_SERVERS`, a JSON array:

```json
[
  {"name": "octopus-api", "url": "http://octopus:8585/mcp-proxy/octopus-api", "transport": "sse",  "run_key": true},
  {"name": "jira",        "url": "http://octopus:8585/mcp-proxy/jira",        "transport": "http", "run_key": true},
  {"name": "thulu-sandbox", "url": "http://octopus:8585/sandbox/sse?task_id=…&task=…", "transport": "sse", "run_key": true}
]
```

- Only the servers that `MCP_TOOLS` grants to that agent on that stage and
  that the daemon knows show up. One that is granted but not registered is
  left out, and `mcp-config.sh` says so in the run's log.
- `run_key: true`: the client sends `X-API-Key: $OCTOPUS_API_KEY`. `false`: no
  credential header.
- Each engine translates it without deciding anything: `mcp-json.js` writes
  Claude Code's `.mcp.json` (`type` = transport), `mcp-codex-args.js` builds
  the Codex adapter's `-c mcp_servers.*` (HTTP with the native client and
  `env_http_headers`; SSE through `mcp-remote`, because Codex does not speak
  SSE), and the OpenCode writer does the same in its format. All three read
  the list with `images/snippets/mcp-servers.js`.
- For SSE servers the daemon also writes `MCP_<NAME>_URL` and `MCP_FORWARD_KEY`,
  pointing at the proxy.

### The endpoint

| Method | Path | For |
|---|---|---|
| `POST`, `GET`, `DELETE` | `/mcp-proxy/<name>` | Streamable HTTP, request by request |
| `GET` | `/mcp-proxy/<name>` | SSE: opens the stream |
| `POST` | `/mcp-proxy/<name>/messages?session=<id>` | SSE: one message of the session |

Authentication: the run key, in `X-API-Key` or `Authorization: Bearer`. Any
other credential (a person's session, a workspace key) gets 403. The key needs
the developer role and the `internal:use` permission, like `/sandbox/sse`.

The task, chat, agent, stage and workspace come from the key, never from the
query. A server that was not granted and one that does not exist answer the
same (403), so a run cannot list what it was not given.

### What the proxy decides on each request

1. **Grant.** It looks up the server's entry in what was granted to the run at
   launch (`mcp_run_grants`, keyed by chat or task + agent + stage). If there
   is no record, it uses the agent's definition
   for that stage. Same rules as `MCP_TOOLS`: `server`, `server@read`,
   `server@write`, `server@tool+tool`.
2. **Allowlist.** With an explicit list of tools (`jira@search+get_issue`), a
   `tools/call` to another tool gets a JSON-RPC error (-32602) and never
   reaches the server; a batch with a rejected tool is rejected whole. A
   `tools/list` comes back filtered. With such a list the proxy reads the body
   key by key and fails closed: a body that is not strict JSON-RPC 2.0
   (repeated keys in any case, BOM, invalid UTF-8, `jsonrpc` other than
   `"2.0"`) gets 400 and is not forwarded, and upstream always gets
   `Content-Type: application/json` with no charset. That way what the proxy
   checks is what the server's `JSON.parse` reads. With `@read`, `@write` or a
   list that includes those words, the server filters, and gets `scope`/`tools`
   in the query as always. A third-party server (`auth: credential`) does not
   get that query, so there only the explicit list counts: `jira@read` grants
   all of `jira`.
3. **Upstream credential.** Depending on the registration:
   - `auth: none` and `forwards_credential`: it adds the run key
     (`octopus-api`, `thulu-github`, shared `thulu-browser`).
   - `auth: none` without the flag: no header.
   - `auth: credential`: it looks up the credential by name
     (`credential_ref`) in the run's workspace and uses it only if it is
     granted to the task, to one of its projects, to an environment of those
     projects or to the workspace. Otherwise 403 with the reason, and the
     decision stays in the credentials panel. A chat with no task reaches only
     credentials granted to the workspace. If the credential is an OAuth
     connection, it sets `Authorization: Bearer` with its access token (see
     [OAuth connections](#oauth-connections)).
4. **Query.** To servers with `auth: none` it adds `task`, `locale` and
   `scope`/`tools`, which they always read. To the `thulu-browser` of a run on
   a runner (`on_runner` in its grant) it adds `host_loopback=1`, so its pages
   open the machine's `localhost`. To one with `auth: credential` (a third
   party) it sends nothing: it has no reason to see task ids.

The client headers that pass are `Accept`, `Content-Type`, `Mcp-Session-Id`,
`Mcp-Protocol-Version`, `Last-Event-Id` and `User-Agent`. The client's key,
`Authorization` and cookies never pass.

A 401 or 403 from the server reaches the agent as 502: if the 401 passed, the
MCP client would attempt an OAuth login against the daemon. The fix belongs to
the operator (registration or credential), not to the agent. The exception is
an OAuth connection: on a 401 the proxy refreshes the token once and resends
the request; if the refresh fails because a person is needed, the agent gets
403 with the reason.

### Transports

- **Streamable HTTP**: the proxy forwards each request and filters the
  response, whether JSON or `text/event-stream`.
- **SSE**: the proxy opens the stream against the server and rewrites the
  `endpoint` event, so the agent posts to
  `/mcp-proxy/<name>/messages?session=<id>` and never sees the server's
  session URL. Only the key that opened the session can post to it (another
  gets 404). An `endpoint` that points at another host cuts the stream: the
  proxy posts with the credential and does not send it wherever the server
  says.

The proxy does not translate between transports: an SSE server is offered as
SSE.

### What does not go through the proxy

- **`thulu-sandbox`**: the daemon itself serves it at `/sandbox/sse` and
  validates the run key on its own. Going through the proxy would be the
  daemon calling itself.
- **Per-run `thulu-browser`**: it listens on the agent container's loopback,
  which the daemon cannot reach, and accepts only that run's key. It goes
  direct with `run_key: true`. In shared mode it does go through the proxy,
  and in per-run mode the shared one is withheld from the proxy too.

## Probe

`POST /mcps/probe {"url": "…"}` (operator, like registering) tries Streamable
HTTP first (`POST initialize`) and then SSE (`GET` with
`Accept: text/event-stream`), without credentials:

```json
{"transport": "http", "http_status": 200, "sse_status": 0,
 "auth_required": false, "oauth": false}
```

`auth_required` is a 401; `oauth` is a 401 whose `WWW-Authenticate` carries
`resource_metadata`, the MCP specification's pointer to the server's OAuth
metadata. The `/mcps` form uses it to preselect the transport.

## OAuth connections

A server that asks for OAuth (Slack, Atlassian) does not accept a pasted
token. The workspace connects once from the browser and the daemon stores the
tokens as one more credential, provider *OAuth connection (MCP server)*, with
the same grants as any other.

1. The operator registers the server in `/mcps` with authentication *Stored
   credential*. **Detect** preselects it when the server asks for OAuth and,
   if the name is already typed, uses it as the credential's name.
2. An admin of each workspace presses **Connect** on the server's row (or in
   **Credentials → Add**, provider *OAuth connection (MCP server)*), picks who
   it is granted to and signs in at the server. They come back to the page
   they left.
3. In **Agents → MCP**, grant the server to the stage that uses it.

What the daemon does:

- **Discovery.** It sends `initialize` without credentials, reads the
  `resource_metadata` of the 401 (RFC 9728) or tries
  `/.well-known/oauth-protected-resource`, and from there the authorization
  server's metadata (RFC 8414, then OIDC). Without resource metadata it uses
  the MCP server's origin as the issuer, as older servers do.
- **Network.** Only the registered server's origin is reached under the
  internal policy (private addresses allowed, no redirects to another origin).
  Every URL that came out of a response and sits on another origin (the
  `resource_metadata`, the authorization server and its token, registration
  and revocation endpoints) goes under the egress policy: nothing private,
  loopback or cloud metadata unless `OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS` lists
  it. A private authorization server on another host is allowed there.
- **Client.** In this order: the client id entered by hand, dynamic
  registration (RFC 7591) if the server offers it, or the client metadata
  document (`/credentials/oauth/client`, only if the daemon is on https) if
  the server announces it. If none applies, the form asks for the client id.
- **Authorization.** Code flow with PKCE S256 and `resource` (RFC 8707). A
  server that lists PKCE methods without S256 is refused; one that lists none
  (Slack) is accepted. The `state` is valid for 15 minutes, once, and only in
  the browser that started: the callback (`/credentials/oauth/callback`,
  public) also requires a cookie from that browser.
- **Tokens.** Encrypted like every other credential. They are refreshed two
  minutes before expiry or on a 401, one refresh per credential at a time. If
  the server rejects the refresh token, the credential becomes invalid with
  *Needs a reconnect* and the proxy answers 403 until someone reconnects.
  **Disconnect** revokes (RFC 7009, if the server offers it) and deletes the
  tokens; the credential and its grants stay, ready to reconnect.
- The tokens do not leave the daemon: they cannot be entered by hand, rotated
  or requested through `/internal/credentials/resolve`. They are only sent to
  URLs the token's resource covers (same origin and path).

The redirect URL is `<dashboard origin>/credentials/oauth/callback`, with the
origin as the browser sees it (behind a proxy, the scheme comes from
`X-Forwarded-Proto`). The Connect form shows it.

### Slack

Slack offers no dynamic registration: you need an app of your own.

1. At api.slack.com/apps, **Create New App → From scratch**, in the Slack
   workspace you will use.
2. **OAuth & Permissions → Redirect URLs**: add exactly the redirect URL that
   Connect shows, for example
   `https://thulu.example.com/credentials/oauth/callback`. Slack compares
   the whole URL.
3. **User Token Scopes**: the ones the tools you will use need, for example
   `search:read.public`, `channels:history`, `chat:write` and `users:read`.
   The agent acts as the person who connects.
4. Turn on the app's MCP access (in the app's configuration, agents or MCP
   section; Slack has renamed it more than once).
5. **Basic Information**: copy the Client ID and Client Secret.
6. In `/mcps`, register `slack` with URL `https://mcp.slack.com/mcp`,
   Streamable HTTP, stored credential `slack`.
7. **Connect**, paste the Client ID and Client Secret, and sign in at Slack.

### Atlassian (Jira and Confluence)

Atlassian's remote server offers dynamic registration, so registering it and
connecting is enough:

1. In `/mcps`, register `atlassian` with URL `https://mcp.atlassian.com/v1/mcp`
   (Streamable HTTP; the SSE endpoint `…/v1/sse` works too), stored credential
   `atlassian`.
2. **Connect** with no client id, sign in at Atlassian and pick the site.

If the site limits which domains may use the MCP server, an Atlassian admin
has to allow the dashboard's. For an API token instead of OAuth, see the next
section.

### Figma

Figma's remote server (`mcp.figma.com`) accepts only clients on its own list
and does not let you register a new one. For Figma, use `thulu-figma`.

## Registering Jira with an Atlassian API token

1. In Atlassian, create an API token (id.atlassian.com → Security → API
   tokens).
2. In each workspace that will use it, **Credentials → Add**: provider *HTTP
   header (API token)*, type *User and API token (Basic)*, user = the
   Atlassian account's email, token = the API token. Grant it to the project
   (or the workspace) whose tasks use it. Name it, for example, `jira-token`.
3. In `/mcps` (operator), **Register MCP**:
   - Name: `jira`.
   - URL: the Jira MCP server's. **Detect** should say Streamable HTTP. If it
     says the server asks for OAuth, it does not accept static tokens and this
     path does not work.
   - Transport: Streamable HTTP. Authentication: stored credential,
     `jira-token`. Empty template: the Basic credential already builds
     `Authorization: Basic base64(email:token)`.
4. In **Agents → MCP**, grant `jira` (or `jira@search+get_issue`) to the stage
   that needs it.

The same registration serves every workspace: each one resolves its own
`jira-token`. One that does not have it gets 403 when calling, and the rest
keep working.

For a server that expects another header, the template builds it from the
credential's fields, one `Name: value` line per header: `{field}` inserts the
field and `{base64:a:b}` the base64 of `a:b`. Example for a token in a custom
header: an HTTP header credential of type *Custom header* (the header's name
and value), or a *Bearer token* with `X-Token: {token}` in the template. The
proxy refuses templates that touch `Host`, `Content-Type`, `Accept`,
`Mcp-Session-Id` and the other headers it manages.

## Security model

- No agent container receives an MCP credential. It receives its run key,
  which dies with the run and opens only what its task reaches.
- Identity comes from the key. The query the agent sends does not count; the
  query the server sees is set by the proxy.
- The grant is checked on every request against what was given to the run at
  launch, so a `.mcp.json` edited by the agent does not open a new server for
  it.
- The credential is resolved on every request: revoking a grant or disabling
  the credential cuts the next call.
- The proxy enforces an explicit allowlist even if the server ignores it.
- The daemon calls servers under the internal network policy (private
  addresses allowed, cloud metadata not). A registration that sends the run
  key or a credential needs https, or http to a private, loopback or cluster
  address.
- An OAuth connection belongs to the workspace that made it and is used by the
  proxy, never by the agent. Token refresh runs in the daemon's process, which
  is a single replica (SSE sessions already require that).
- Known limits: Streamable HTTP session ids are set by the server and the
  proxy does not bind them to the key (it does with SSE sessions); automations
  and the `/tools` probe still call servers directly and without
  `auth: credential` credentials.
