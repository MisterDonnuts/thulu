# thulu-figma

A read-only MCP server over the Figma REST API (`tools/thulu-figma/`). It
exists because Figma's remote MCP admits only allowlisted clients and refuses
personal tokens: an agent in a Thulu container cannot get in that way.

## What it does

| Tool | Figma endpoint | Token scope |
|---|---|---|
| `file_get` | `GET /v1/files/:key?depth=` | `file_content:read` |
| `nodes_get` | `GET /v1/files/:key/nodes` | `file_content:read` |
| `images_render` | `GET /v1/images/:key` | `file_content:read` |
| `comments_list` | `GET /v1/files/:key/comments` | `file_comments:read` |
| `team_projects_list` | `GET /v1/teams/:id/projects` | `projects:read` |
| `project_files_list` | `GET /v1/projects/:id/files` | `projects:read` |

All of them have scope `read` and none writes to Figma. `file_key` accepts the
bare key or any figma.com link (`/design/`, `/file/`, `/proto/`, `/board/`,
branches); if the link carries `?node-id=1-2`, `nodes_get` and `images_render`
use it when they get no `node_ids`.

Responses come back summarized, not as Figma returns them. `file_get` gives the
outline (pages and frames by id, name and type, up to 500 nodes) plus the names
of components and styles. `nodes_get` gives the box `[x, y, w, h]`,
auto-layout, sizing, colors in hex, radii, effects, text with its font and the
names of shared styles, up to 400 nodes. A response over 60 KB is cut and says
so. `images_render` returns PNG/JPG inline (up to 4, 1.5 MB each) and the Figma
URL, which expires after 30 days; with `attach: true` it uploads each render to
the task with the run's key.

## Credential

Provider `figma` in `/credentials`, type "Personal access token". Thulu
checks it with `GET /v1/me`, which needs the `current_user:read` scope, and
stores the account's email as its identity. Figma reports neither a token's
scopes nor its expiry, so the permissions stay "not discovered" and no tool is
blocked on permissions: the `figma:*` entries in the catalog
(`internal/credentials/catalog.go`) only say which scope to tick when creating
the token.

It is granted like any other credential: to the workspace, or to a project.
With two Figma credentials reachable from the same task the call fails with
`ambiguous_credential` and both names; the tool does not pick one.

The server asks `/internal/credentials/resolve` for the token per tool with the
run's key and caches it for 45 s per (key, tool). The token never reaches the
agent.

## Turning it on

It ships off. In `thulu init` it is the `thulu-figma` checkbox; in Helm,
`mcp.thuluFigma.enabled: true`. It goes out to `api.figma.com` and Figma's
image CDN on 443, which the MCP NetworkPolicy already allows. In development,
`tools/docker-compose.yml` publishes it on `127.0.0.1:3016`.

`FIGMA_API_URL` changes the API base (the tests point it at a fake Figma). The
caps are set with `OUTPUT_CAP_BYTES`, `MAX_TREE_NODES`, `MAX_NODE_SUMMARIES`
and `MAX_INLINE_BYTES`.

Tests: `cd tools/thulu-figma && npm ci && node --test`.
