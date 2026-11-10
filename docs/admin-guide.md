# Operator & Admin Guide

A runbook for operators administering a Thulu deployment: first-run setup,
identity/SSO, MFA (enforced at your identity provider), user lifecycle, audit,
performance tuning, and cleanup.

Related: [security-hardening.md](security-hardening.md) ·
[upgrade-guide.md](upgrade-guide.md) ·
[troubleshooting.md](troubleshooting.md) ·
[auth.md](auth.md) · [deploy.md](deploy.md)

---

## 1. First-run setup

### First user becomes owner

Thulu has no registration endpoint. Accounts are provisioned on first sign-in
through the configured identity provider, and the **first** person in gets a
personal workspace and the **`owner`** role on it.

Everyone after that lands with the default role and no workspace of their own —
an `admin`/`owner` adds them to a workspace (see below).

> Bootstrap the instance immediately after deploy by signing in as the operator
> account, before exposing the URL more widely. Whoever signs in first owns it.

### Role model

Four roles, in ascending privilege (enforced by `RequireRole`):

| Role | Capabilities |
|------|--------------|
| `viewer` | Read-only: list tasks, agents, projects, usage, audit. |
| `developer` | viewer + create/update tasks, comment, relaunch agents. |
| `admin` | developer + create/delete projects, manage webhooks, settings, **users and members**. |
| `owner` | admin + workspace management; **granting the `owner` role is reserved to the workspace owner**. |

A higher role inherits all lower-role permissions. Roles are stored per
workspace in `workspace_members`; a user's effective role is re-derived from
that table on each login (the membership table is the source of truth).

### Editing the agent catalog

Each workspace owns its agent catalog: agent definitions, prompts, per-stage
settings, skills and their edit history. An `admin` of the workspace edits it
from `/agents/` and `/skills/`, or with a key minted in that workspace. An edit
changes what that workspace's runs are told and nobody else's; a second
workspace answers `404` for an agent it does not have.

```bash
# thulu keys mint -workspace <id> -role admin -permissions full  →  KEY
curl -X PUT https://thulu.example.com/types/my-agent/files/prompts/main.md \
  -H "X-API-Key: $KEY" \
  --data-binary @main.md
```

Every write lands in the revision log and the audit log. `GET
/types/{name}/revisions` and `GET /skills/{name}/revisions` list the history;
`POST …/revisions/{id}/restore` puts a version back. The dashboard shows both
under **History** in the file editor.

From the CLI, `thulu agents import -workspace <id> <tree>` loads a tree into
one workspace, and `thulu agents export -workspace <id>` writes it out. With
a single workspace the flag can be left out.

**System skills** are the exception: they ship inside the binary
(`internal/builtin/system-skills/`), reach every workspace, and only the
operator writes them. Each one names the MCP server it travels with, and every
run that has that MCP wired on its stage receives it without a grant. The
daemon copies them into the database at boot when the embedded `version` is
higher than the stored one. For a hotfix between releases an admin of
`OCTOPUS_OPERATOR_WORKSPACE` calls `PUT /system/skills/{name}`; it keeps the
stored version, so the next release that raises the version replaces it.
Workspaces cannot create a skill named `octopus-*` or reuse a system skill's
name.

A workspace admin can turn a system skill off for one agent on one stage. In
`/agents/`, the agent's **Skills** tab lists the system skills under each
stage: "Every run" for the ones tied to no MCP, "Included by <mcp>" for the
rest, and a disabled row reading "Needs <mcp> on this stage" when the stage
does not wire that server. Untick a row and save: that agent's runs on that
stage stop receiving the skill, and every other stage and agent keeps it.
`octopus-run` can be turned off too, and the tab warns about it, since without
it the agent does not learn where the repos and the output dir are, who
commits, or what to leave when it finishes. Through the API the same switch is
the `system_skills_off` field of `PUT /types/{name}/settings`:

```bash
curl -X PUT https://thulu.example.com/types/my-agent/settings \
  -H "X-API-Key: $KEY" -H 'Content-Type: application/json' \
  -d '{ …the rest of the settings…, "system_skills_off": {"review": ["octopus-browser-evidence"]} }'
```

Leaving the field out keeps what is stored, and `{}` turns every system skill
back on. A name that is not a system skill is kept and does nothing, so an
opt-out for a skill a later release removes is harmless.

### Inviting members

Members are added by email to a workspace by an `admin` or `owner`:

```bash
curl -X POST https://thulu.example.com/auth/workspaces/{workspaceId}/members \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"email":"alice@example.com","role":"developer"}'
```

Notes:
- An email that has never signed in becomes a **pending invitation** (the
  response says `"status": "invited"`). The first sign-in with a
  provider-verified copy of that email joins the workspace with the role you
  chose, and that person gets no personal workspace of their own. Thulu
  sends no email: tell them to sign in.
- In `allowlist` signup mode the invitation does not get anybody past the
  allowlist. Until the operator adds the email, the invitation shows as
  waiting for access, and the operator sees it under **Settings → Team → Access
  list**.
- Pending invitations hold seats under the plan's seat limit.
- List, re-role or cancel them with `GET /auth/workspaces/{id}/invitations`,
  `PATCH` or `DELETE /auth/workspaces/{id}/invitations/{invitationId}`.
- `role` defaults to `developer` if omitted.
- Granting `owner` requires the caller to **be** the workspace owner.

### Terms and privacy pages

The dashboard serves `/legal/terms/` and `/legal/privacy/`, linked from the
login page, the app footer and Settings. They are templates: review them before
relying on them. Four
variables name who operates the install and where it runs:

| Variable | Shown as | Example |
|----------|----------|---------|
| `OCTOPUS_LEGAL_ENTITY` | The operator's legal name | `Example SRL` |
| `OCTOPUS_LEGAL_CONTACT` | Where privacy and legal requests go | `privacy@example.com` |
| `OCTOPUS_LEGAL_HOSTING` | The company whose servers run the install, listed as a subprocessor | `Example Cloud Inc.` |
| `OCTOPUS_LEGAL_HOSTING_REGION` | Where those servers are | `EU` |

All four are read at request time through `GET /auth/config` (field `legal`).
Unset, the pages show "not configured" in their place instead of a made-up
name.

---

## 2. SSO setup

Thulu supports **OIDC** (one issuer per install) and **GitHub OAuth**. Configure providers with environment variables;
restart the daemon to pick up changes.

Common SSO behaviour:

- **Email verification is mandatory.** A login is rejected unless the provider
  asserts `email_verified: true`. A missing `email_verified` claim is treated as
  unverified. GitHub logins require a verified primary email on the GitHub
  account.
- **Auto-link is off by default** (`OCTOPUS_ALLOW_SSO_AUTOLINK`, see below).
- **Where new SSO users land** is controlled by `OCTOPUS_SSO_DEFAULT_WORKSPACE`
  and `OCTOPUS_ALLOW_PERSONAL_WORKSPACES` (see below).
- **Who may sign in at all** is controlled by `OCTOPUS_SSO_ALLOWED_DOMAINS` and
  `OCTOPUS_SSO_ALLOWED_GROUPS` (see below). Without them, anyone the provider
  can authenticate gets an account.

### Google (OIDC)

1. In Google Cloud Console create an **OAuth 2.0 Client ID** (type: Web
   application).
2. Add the authorized redirect URI:
   `https://<your-domain>/auth/oidc/callback`
3. Set:

   ```bash
   OCTOPUS_OIDC_ISSUER=https://accounts.google.com
   OCTOPUS_OIDC_CLIENT_ID=<client-id>
   OCTOPUS_OIDC_CLIENT_SECRET=<client-secret>
   OCTOPUS_OIDC_NAME=Google
   OCTOPUS_OIDC_REDIRECT_URI=https://<your-domain>/auth/oidc/callback
   ```

### Okta / Microsoft Entra ID (OIDC)

1. Create an OIDC **Web** application in Okta / Entra.
2. Set the sign-in redirect URI to
   `https://<your-domain>/auth/oidc/callback`.
3. Use the provider's issuer URL:
   - Okta: `https://<your-org>.okta.com` (or the custom auth-server issuer).
   - Entra: `https://login.microsoftonline.com/<tenant-id>/v2.0`.
4. Set:

   ```bash
   OCTOPUS_OIDC_ISSUER=<issuer-url>
   OCTOPUS_OIDC_CLIENT_ID=<client-id>
   OCTOPUS_OIDC_CLIENT_SECRET=<client-secret>
   OCTOPUS_OIDC_NAME=Okta        # or "Microsoft"
   OCTOPUS_OIDC_REDIRECT_URI=https://<your-domain>/auth/oidc/callback
   ```

Thulu discovers endpoints via the provider's `/.well-known/openid-configuration`
and uses PKCE plus an HMAC-signed `state`.

To make sign-out end the provider's session as well, set
`OCTOPUS_OIDC_IDP_LOGOUT=1` (the default with `OCTOPUS_SAAS_MODE=1`) and register
`https://<your-domain>/login/` as a post-logout redirect URI on the client, or
set `OCTOPUS_OIDC_POST_LOGOUT_REDIRECT_URI` to the URI you registered. See
[Signing out of the provider](auth.md#signing-out-of-the-provider).

### GitHub OAuth

1. Create a **GitHub OAuth App** (Settings → Developer settings → OAuth Apps).
2. Authorization callback URL: `https://<your-domain>/auth/github/callback`.
3. Set:

   ```bash
   OCTOPUS_GITHUB_CLIENT_ID=<client-id>
   OCTOPUS_GITHUB_CLIENT_SECRET=<client-secret>
   ```

### `OCTOPUS_ALLOW_SSO_AUTOLINK`

When a sign-in presents an email that already belongs to an account created
through a different provider (for example, the account signed in with GitHub
and now arrives through your OIDC issuer):

- **Default (`0`/unset):** the login is **rejected**, and the user is told the
  email already exists from a different sign-in method. This prevents account
  takeover, where any IdP the operator trusts could assert a victim's email.
- **`1`:** the SSO identity is linked to the existing account, **but only if the
  provider reports the email as verified**. Enable this only when you trust the
  IdP's email verification for every account it can assert.

### `OCTOPUS_SSO_ALLOWED_DOMAINS` / `OCTOPUS_SSO_ALLOWED_GROUPS`

Checked on every sign-in, not only the first:

- `OCTOPUS_SSO_ALLOWED_DOMAINS=acme.com,acme.io` admits only emails in those
  exact domains (`eu.acme.com` must be listed separately), and only when the
  provider marks the email verified.
- `OCTOPUS_SSO_ALLOWED_GROUPS=octopus-users,octopus-admins` admits only members
  of one of those IdP groups, read from the groups claim
  (`OCTOPUS_OIDC_GROUPS_CLAIM`, default `groups`). GitHub sign-in carries no
  groups, so it is refused while this is set.

A refused sign-in returns to the login page with the reason and creates no
account. Both unset admits everyone the provider authenticates.

### `OCTOPUS_SIGNUP_MODE` and the signup allowlist

A hosted install (`OCTOPUS_SAAS_MODE=1`) defaults to `OCTOPUS_SIGNUP_MODE=allowlist`:
only invited emails may sign in, checked on every login. Invite people from
**Settings → Team → Access list** (visible to admins of `OCTOPUS_OPERATOR_WORKSPACE`);
removing someone ends their sessions within the access-token TTL. Members of the
operator workspace are always admitted.

Before the first login there is no operator workspace yet, so put your own email
in `OCTOPUS_SSO_ALLOWED_EMAILS=you@example.com`, sign in, set
`OCTOPUS_OPERATOR_WORKSPACE` to the workspace you landed in, and restart. A
self-hosted install defaults to `open`; set `allowlist` there too to require
invitations. See [auth.md](auth.md#signup-allowlist).

### What `OCTOPUS_SAAS_MODE=1` refuses at startup

A hosted install serves strangers from one daemon, so settings that are fine
when everybody who signs in is one organization are refused there. The daemon
does not start while any of these is on, and lists every one it found:

| Setting | Why |
|---------|-----|
| `OCTOPUS_DEV_MODE`, `OCTOPUS_DEV_MODE_CONTAINER` | Sign-in off: every visitor is the owner. |
| `OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL` | Runs without their own key use the operator's. |
| `OCTOPUS_LLM_AMBIENT_CLOUD_IDENTITY` | Bedrock/Vertex profiles without a credential run as the daemon's cloud identity. |
| `OCTOPUS_ALLOW_SSO_AUTOLINK` | A sign-in takes over an existing account by matching its email. |
| `OCTOPUS_SSO_DEFAULT_WORKSPACE` | Every newcomer joins one shared workspace. |
| `OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT` | Tenant secrets may be stored unencrypted. |
| `OCTOPUS_SECRETBOX_KEY` and `_FILE` both unset | Tenant secrets have no key of their own. |
| `OCTOPUS_GIT_ALLOW_LOCAL_REMOTES`, `OCTOPUS_GIT_LOCAL_REMOTE_ROOT` | Projects can clone paths on the daemon's disk. |
| `OCTOPUS_GIT_ALLOW_SSH` | SSH remotes run with the daemon host's SSH keys. |
| `OCTOPUS_OIDC_ISSUER` and `OCTOPUS_GITHUB_CLIENT_ID` both unset | Nobody can sign in. |

A switch counts as on for any value other than empty, `0`, `false`, `no` or
`off`. Three more are logged as warnings and do not stop the start: no
`OCTOPUS_OPERATOR_WORKSPACE`, `OCTOPUS_SIGNUP_MODE=open`, and
`OCTOPUS_ALLOW_PERSONAL_WORKSPACES` turned off.

In any mode, `OCTOPUS_DEV_MODE=1` next to `OCTOPUS_OIDC_ISSUER` is refused:
dev mode turns sign-in off, and a configured issuer says the install was meant
to have logins.

### `OCTOPUS_SSO_DEFAULT_WORKSPACE` and `OCTOPUS_ALLOW_PERSONAL_WORKSPACES`

Decide where a brand-new SSO user is provisioned, in this order:

1. **`OCTOPUS_SSO_DEFAULT_WORKSPACE=<slug>`:** new SSO users join that workspace
   with `OCTOPUS_SSO_DEFAULT_ROLE` (`viewer` unless set; `developer` and `admin`
   are accepted, `owner` fails the start). The slug is validated at
   provisioning time; if it matches no workspace, the login errors rather than
   silently creating an isolated workspace.
2. **Personal workspaces allowed:** the user gets their own workspace as
   `owner` on their first sign-in. That happens with
   `OCTOPUS_ALLOW_PERSONAL_WORKSPACES=1`, and by default with
   `OCTOPUS_SAAS_MODE=1`, where the signup allowlist decides who gets in. A
   self-hosted install without the variable does not do it: GitHub sign-in is
   open there, so only the first sign-in gets a workspace and later newcomers
   wait (step 3).
   Separately, the variable (default **off when OIDC is configured**, on
   otherwise) decides whether a signed-in member may create workspaces from
   the menu (an admin of `OCTOPUS_OPERATOR_WORKSPACE` always can); there is
   no limit on how many an install has.
   How many workspaces one account may own through `POST /auth/workspaces` is
   capped by `OCTOPUS_MAX_OWNED_WORKSPACES`: default `2` in SaaS mode (the
   personal one plus one more), unlimited otherwise, `0` for no cap. Admins of
   `OCTOPUS_OPERATOR_WORKSPACE` are not capped. A value that is not a whole
   number of 0 or more stops the daemon at startup.
3. **Otherwise the user waits.** The account exists but has no workspace, no
   session is issued, and the login page says to ask an administrator. The
   audit log records `user.pending_access`; add them by email from
   **Settings → Members**. The first sign-in on an install with no workspace
   at all still gets one, so a fresh install has an owner.

---

## 3. MFA

MFA is the identity provider's job, not Thulu's. Enforce it in Okta, Entra,
Keycloak or Google Workspace and it applies to Thulu automatically, along
with every other app behind the same provider.

Thulu has no enrollment UI, no TOTP secrets and no backup codes. Nothing here
can weaken the policy your IdP enforces, and nothing here needs a second
recovery path.

---

## 4. User lifecycle & offboarding

### Remove a member from a workspace

```bash
curl -X DELETE \
  https://thulu.example.com/auth/workspaces/{workspaceId}/members/{userId} \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

- Requires `admin` on the workspace.
- The **workspace owner cannot be removed** from their own workspace.
- Removal **immediately revokes all of that user's refresh tokens** and bumps
  their `token_version`, so eviction takes effect now — outstanding access
  tokens (30m default) are invalidated on their next request and cannot be
  refreshed.

### Transfer a workspace

The owner hands a workspace to one of its members from Settings, Account, or:

```bash
curl -X POST https://thulu.example.com/auth/workspaces/{workspaceId}/transfer \
  -H "Authorization: Bearer $OWNER_SESSION_TOKEN" -d '{"user_id":"<member id>"}'
```

The member becomes `owner` and the old owner `admin`; the old owner's
owner-role API keys in that workspace become admin keys. A session is required;
API keys cannot transfer.

### Account deletion

A person deletes their own account from Settings, Account (typing their email
to confirm), or with `DELETE /auth/me {"confirm":"<email>"}`. Workspaces they
own alone are erased with all their data; a workspace they own that other
people use blocks the deletion until they transfer or delete it. Keys they
minted in other workspaces are revoked. See
[auth.md](auth.md#workspaces-and-the-account).

### Cut off access

Deactivate the person in the identity provider. That is the whole offboarding
step for sign-in: without the IdP they cannot start a new session.

Their existing Thulu session survives until its refresh token expires, so for
an immediate cut also remove them from the workspace (above) — that revokes
their refresh tokens and bumps `token_version`, evicting outstanding access
tokens on the next request.

Lockout after repeated failures is the IdP's job too; Thulu does not count
failed attempts because it never sees a credential.

### API key rotation

Automation keys are managed via the API key endpoints, scoped to the calling
workspace:

```bash
POST   /auth/api-keys            # create — plaintext returned ONCE at creation
GET    /auth/api-keys            # list this workspace's keys (prefix + metadata, never the secret)
DELETE /auth/api-keys/{id}       # revoke one of them
```

Creating one states a role and an allow-list; there is no key that spans
workspaces. See [auth.md](auth.md#api-keys) for the permission groups and the
presets.

Rotate by creating a new key, updating the consumer, then revoking the old key.
Keys are stored only as SHA-256 hashes; the plaintext (`oct_<hex>`) is shown
once. Creation and revocation are audited
(`auth.api_key.created` / `auth.api_key.revoked`).

Limits on minting:

- A key can only grant permissions its minter holds, and a key minted by
  another key expires no later than its parent. Listing, minting and revoking
  keys needs the `keys:write` permission; agent-run keys cannot do it.
- Every key expires. `OCTOPUS_API_KEY_MAX_TTL` caps the lifetime (`90d` by
  default; a Go duration such as `2160h` also works). A mint without an expiry
  gets the cap; one asking for longer is refused. `0` removes the cap. This
  includes keys minted by `thulu keys mint` and the setup wizard, so the
  operator key has to be rotated before it expires.
- Member management through a key (`/auth/workspaces/{id}/members`) needs a
  key of that workspace with a sufficient role and the `members:read` /
  `members:write` permissions.

> Agent containers never receive an operator key — see the agent credential
> model in [security-hardening.md](security-hardening.md#6-agent-credential-model).

### Delegated chats and GitHub

A chat with **Act for me** on gets a key of its own, minted for the person
who delegated:

- Its role is the person's, capped at developer. An admin or owner delegates
  as developer. Only a developer or above can turn delegation on; if the
  person is later demoted to viewer or removed, the next key for that chat is
  minted as a viewer without `runs:write` or `internal:use`. A key already in
  the container keeps its role until it is revoked or expires (8 hours at most).
- Its `thulu-github` reaches the repository of any project in the workspace,
  looked up by `owner/name` (`GET /internal/github-token?repo=`). The token
  comes only from the GitHub App installation, with `metadata:read`,
  `contents:read`, `pull_requests:write` and `actions:read`, plus
  `actions:write` when the agent's `chat` stage grants `run_dispatch`,
  `run_rerun` or `run_cancel`, and `checks:read` when it grants `pr_checks`
  (any scope keyword does). A project whose only GitHub credential is a
  stored token (PAT) gives the chat a 403: that token's scopes cannot be
  narrowed. Each token handed out is audited as `chat.github_granted`, with
  the agent, the person, the project and the permissions.
- `pr_merge` and `deployment_approve` wait for the person: they appear in the
  chat with **Confirmar** / **Cancelar** (`chat.action_held`,
  `chat.action_confirmed`), and only the confirmation runs them, with a token
  minted for that one call. The `chat` stage has to grant the tool by name.
  `run_dispatch`, `run_rerun` and `run_cancel` do not wait.
- `commit`, `branch_push` and `branch_status` refuse in a chat: they work on a
  task's worktree and a chat has none.

What the confirmation does not cover, so grant the tools with it in mind:

- `pr_review_submit` with `APPROVE` (granted by `@write`) counts as an App
  approval on the pull request. If branch protection counts App approvals, the
  chat can supply one; it still cannot merge.
- With `actions:write`, a dispatched workflow runs with its own
  `GITHUB_TOKEN`. A workflow that merges or deploys does so without the
  person's confirmation unless its environment has required reviewers.
- Enabling auto-merge with `pull_requests:write` alone has not been verified
  against GitHub. If GitHub allows it, a pull request the chat enables it on
  merges when its checks pass.

---

## 5. BYOK — bring your own LLM key

Each workspace supplies its own LLM provider API key. The key is **encrypted at
rest** (AES-256-GCM with `OCTOPUS_SECRETBOX_KEY`, or derived from
`OCTOPUS_JWT_SECRET` when that is unset; see
[security-hardening.md](security-hardening.md#stored-secret-encryption-key-secretbox)), never logged, and
injected only into that workspace's agent runs — so each tenant's usage bills
against their own provider account. Managed via the `admin`-gated, workspace-
scoped endpoints (or the Settings UI):

```bash
PUT    /llm-key   { "provider": "anthropic", "key": "sk-ant-…" }
GET    /llm-key            # status only — returns provider + masked hint, never the key
DELETE /llm-key
```

The status endpoint only ever returns a masked hint (e.g. `…7777`); the
plaintext key is never returned after it is set.

### LLM profiles

A profile (`/llm-profiles`, Settings → AI engines) is a named credential plus
model settings for one engine. Beyond the key, a profile can set:

| Field | Applies to | What it does |
|---|---|---|
| `provider` | all | `anthropic` / `openai` (the engine's own API), `bedrock` / `vertex` for Claude Code, and `openai-compatible` / `anthropic` / `openai` for OpenCode. `GET /engines` lists what each engine accepts in `providers`. |
| `base_url` | Claude Code, Codex, OpenCode | A custom endpoint (LiteLLM, a corporate gateway, DeepSeek). https only; see `OCTOPUS_LLM_ALLOW_HTTP_HOSTS`. With a base_url the key is not checked against the provider's key prefixes. Required for `openai-compatible`, which has no default address. See [opencode.md](opencode.md). |
| `auth_header` | Claude Code with a base_url | `x-api-key` (default) or `bearer`, which sends the key as `Authorization: Bearer`. |
| `region`, `project_id`, `small_fast_model` | bedrock, vertex | Where the models run (`project_id` is Vertex only) and the cloud's id for the small model. |
| `aws_access_key_id`, `aws_secret_access_key`, `aws_session_token` | bedrock | An access key pair, stored encrypted. `key` may carry a Bedrock API key instead. |
| `gcp_service_account_json` | vertex | A service account key file, stored encrypted and written to a 0600 file in the agent container. |
| `clear_key` | bedrock, vertex (update) | Drops the stored credential. |

With the LLM gateway on, a Bedrock or Vertex run never holds the cloud
credential. Claude Code sends its calls to the gateway unsigned
(`CLAUDE_CODE_SKIP_BEDROCK_AUTH` / `CLAUDE_CODE_SKIP_VERTEX_AUTH`) with the
run's own token; the gateway checks that the call is a model invocation for
the profile's region (and, on Vertex, its project) and signs it: SigV4 with
the profile's key pair, the Bedrock API key as a bearer token, or an access
token minted from the service account. Any other Bedrock or Vertex API is
refused, whatever the credential itself may do.

A profile with no credential runs on this install's own cloud identity, which
is the operator's: the daemon's IRSA role, EKS Pod Identity, instance role or
GKE Workload Identity when the gateway serves the run, and the agent host's
instance role when it does not (agent pods on Kubernetes get no ServiceAccount
token and cannot reach the metadata endpoint). It is refused, when the profile
is saved and when a run starts, unless the operator sets
`OCTOPUS_LLM_AMBIENT_CLOUD_IDENTITY=1`. Annotate the daemon's ServiceAccount
(`serviceAccount.annotations` in the chart) for IRSA or Workload Identity.

With the gateway off, the credential goes into the agent's environment (the
Vertex key as a file only the agent's user can read), where every command the
agent runs can read it. Give such a key permission to invoke the models and
nothing else.

Secrets are write-only: responses carry `key_hint` (for Vertex, the service
account's email) and `has_key`.

### No host credential by default

When a run finds no profile and no workspace key for its engine, the dispatch
fails with a comment on the task that names the fix. The daemon's own
`ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` / `OPENAI_API_KEY` do not
reach agents: on a multi-tenant install they would bill every workspace to the
operator. A profile that fails to decrypt also fails the dispatch instead of
falling back.

A single-tenant install that keeps its key in the host `.env` sets
`OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1`.

### LLM gateway

With the gateway on, agents never see the provider key. Each run gets
`ANTHROPIC_BASE_URL` (or `OPENAI_BASE_URL`) pointing at the daemon and its own
run key as the credential; the daemon checks that the run is still live, looks
up the workspace's profile, injects the real key and forwards the call. A
profile's `base_url` is honored as the upstream. Bedrock and Vertex profiles
go through it too, signed by the daemon (see LLM profiles above). OAuth
(Claude subscription) tokens are bound to the Claude Code client and still
reach the run directly.

| Variable | Default | Meaning |
|---|---|---|
| `OCTOPUS_LLM_GATEWAY` | `auto` | `on`, `off`, or `auto` (on when `OCTOPUS_LLM_GATEWAY_ADDR` or `OCTOPUS_LLM_GATEWAY_URL` is set). Any other value stops the boot. |
| `OCTOPUS_LLM_GATEWAY_ADDR` | unset | Optional extra listener (e.g. `:8686`) that serves only the gateway. The gateway is always mounted at `/llm-gateway` on the API listener when on. |
| `OCTOPUS_LLM_GATEWAY_URL` | `<agent base URL>/llm-gateway` | The URL agents use to reach the gateway. |
| `OCTOPUS_LLM_UPSTREAM` | `https://api.anthropic.com` | Default Anthropic upstream. |
| `OCTOPUS_LLM_OPENAI_UPSTREAM` | `https://api.openai.com/v1` | Default OpenAI upstream. |

The gateway refuses to connect a profile's `base_url` to loopback, private,
link-local or CGNAT addresses, checked after DNS resolution, unless the host is
in `OCTOPUS_LLM_ALLOW_HTTP_HOSTS`. The daemon's own egress proxy is always
allowed.

### Agent egress

| Variable | Default | Meaning |
|---|---|---|
| `OCTOPUS_LLM_ALLOW_HTTP_HOSTS` | unset | Comma-separated hosts (exact, or `*.suffix` for subdomains) a profile `base_url` may reach over plain http, e.g. `*.svc.cluster.local`. Loopback is always allowed. |
| `OCTOPUS_AGENT_HTTP_PROXY`, `OCTOPUS_AGENT_HTTPS_PROXY`, `OCTOPUS_AGENT_NO_PROXY` | the daemon's own `HTTP(S)_PROXY` / `NO_PROXY` | Proxy settings passed to agents, in both cases. When a proxy is set, `NO_PROXY` also gets `localhost`, `127.0.0.1`, `::1`, `host.docker.internal`, `.svc`, `.cluster.local` and the daemon's and gateway's hosts. |
| `OCTOPUS_AGENT_PROXY_INHERIT` | `1` | `0` stops agents from inheriting the daemon's proxy variables; only the `OCTOPUS_AGENT_*` ones apply. |
| `OCTOPUS_EXTRA_CA_BUNDLE` | unset | When set, agents get `NODE_EXTRA_CA_CERTS=/etc/octopus/ca.crt`. The runtime must mount the bundle at that path. |
| `OCTOPUS_CLAUDE_CODE_NONESSENTIAL_TRAFFIC` | unset | Claude Code runs with `DISABLE_AUTOUPDATER`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, `DISABLE_TELEMETRY` and `DISABLE_ERROR_REPORTING` set, Codex with analytics, update checks and feedback off, and OpenCode with its update check, models catalog refresh, LSP downloads and sharing off. `1` turns that off. |
| `OCTOPUS_GITHUB_API_URL` | unset | Forwarded to agents as `GITHUB_API_URL` (GitHub Enterprise Server). |

### Credential validation endpoints

The credential manager validates AWS and GCP credentials against the public
endpoints. Regulated or air-gapped installs point them elsewhere; overrides must
be https.

| Variable | Default |
|---|---|
| `OCTOPUS_AWS_STS_ENDPOINT` | `https://sts.amazonaws.com/` |
| `OCTOPUS_AWS_IAM_ENDPOINT` | `https://iam.amazonaws.com/` |
| `OCTOPUS_AWS_SIGNING_REGION` | `us-east-1` (GovCloud: `us-gov-west-1`) |
| `OCTOPUS_GCP_OAUTH2_ENDPOINT` | `https://oauth2.googleapis.com/token` |
| `OCTOPUS_GCP_CRM_ENDPOINT` | `https://cloudresourcemanager.googleapis.com/v1/projects/` |

`OCTOPUS_CREDENTIAL_MONITOR=off` stops the scheduled re-validation, so the
daemon makes no unattended calls to AWS, GCP or GitHub. Expiry dates already
known are still tracked, and "Validate" in the dashboard still works.

## 6. Audit log

Every security-relevant action is recorded in the `audit_log` table. Query it from the
**Settings → Audit** view in the dashboard, or via the API (`admin` role):

```bash
GET /audit?limit=100&offset=0
```

Supported query parameters:

| Param | Meaning |
|-------|---------|
| `kind` | Filter by event kind (e.g. `auth.sso_denied`). |
| `actor_id` | Filter by the acting user/principal. |
| `resource_id` | Filter by affected resource. |
| `since` / `until` | RFC3339 timestamps for a time window. |
| `limit` / `offset` | Pagination. |

Audit results are scoped to the caller's workspace. Notable event kinds include
`user.logged_in`, `user.registered` (account provisioned on first sign-in),
`auth.logout`, `auth.api_key_created`, `auth.api_key_revoked`, `workspace.member_removed`, and,
for delegated chats, `chat.delegation_started/ended`, `chat.action_held`,
`chat.action_confirmed/canceled` and `chat.github_granted`, and
`engine_login.connected/disconnected` when a person connects or drops their own
engine login ([personal-login.md](personal-login.md)). A Prometheus
counter `octopus_auth_events_total{kind,result}` tracks the same events for
alerting.

Each entry carries `actor_type` (user, API key, agent run, system),
`actor_label` (an email, a key's name, a run's agent and task) and the
workspace of the credential that acted.

To keep a copy the database cannot rewrite, set `OCTOPUS_AUDIT_SINK`:

| Value | Effect |
|-------|--------|
| unset | Database only. |
| `stdout` / `stderr` | One JSON line per entry, for the container log collector. |
| `file:/var/log/octopus/audit.log` | Appended to that file (created `0600`), for a shipper to tail. |

An unknown value fails the start. The rows are also hash-chained:
`GET /audit/verify`, called by an admin of `OCTOPUS_OPERATOR_WORKSPACE`,
recomputes the chain and returns `ok`, the first broken row if any, and the
`head` hash. Store the head somewhere else from time to time; comparing against
it is how a deleted tail shows up.

---

## 7. Performance tuning

Thulu is a single-node daemon. The knobs that affect throughput and resource
use are environment variables (restart to apply):

### Rate limiting

Per-IP token-bucket limiter in front of the API:

| Variable | Default | Meaning |
|----------|---------|---------|
| `OCTOPUS_RATE_LIMIT_RPS` | `100` | Sustained requests/sec per client IP. |
| `OCTOPUS_RATE_LIMIT_BURST` | `200` | Burst capacity (token bucket size). |

Correct client-IP attribution depends on `OCTOPUS_TRUSTED_PROXIES` — without it,
all traffic behind the reverse proxy shares one bucket. The same list gates
`X-Forwarded-Proto`: behind a TLS-terminating proxy that is not listed, the
daemon takes the request as plain http, so the SSO callback it sends the IdP
starts with `http://` and the login fails with a redirect_uri mismatch. List
the proxy, or pin `OCTOPUS_OIDC_REDIRECT_URI` and `OCTOPUS_FRONTEND_ORIGIN`.

Requests authenticated with an API key also go through a per-key bucket, so one
integration cannot use up the limit of everyone behind the same IP:

| Variable | Default | Meaning |
|----------|---------|---------|
| `OCTOPUS_RATE_LIMIT_PER_KEY` | `50` | Sustained requests/sec per API key. |
| `OCTOPUS_RATE_LIMIT_PER_KEY_BURST` | `100` | Burst capacity per key. |

Both `0` disables the per-key limit. Over the limit the answer is `429` with
`Retry-After`.

### Sandbox memory

| Variable | Default | Meaning |
|----------|---------|---------|
| `OCTOPUS_SANDBOX_MEMORY_BYTES` | `6442450944` (6 GB) | Per-sandbox-container memory cap. Must be ≤ the Docker VM's RAM. Accepts a byte count (suffixes like `6GB` are supported in `.env`). |

### Agent concurrency & turns

Agents are launched per task as tasks move between board stages. Two limits
decide how many run at once:

- `agent.max_containers` (setting, `PUT /settings/agent.max_containers`) caps
  agent containers across the whole install. `0` means no cap on a
  self-hosted install. With `OCTOPUS_SAAS_MODE=1`, `0` means
  `max(2, CPUs − 2)`, and the daemon logs the number it picked at startup.
- Each workspace's plan `concurrency_limit` caps that workspace's agents.

When runs wait for a slot, workspaces take turns: a free slot goes to the
next workspace in rotation, never to whichever workspace queued the most
tasks. Mention runs (`@agent` in a comment) queue separately, against
`mention.max_containers` (default `3`) for the install and the same plan
`concurrency_limit` per workspace, so a busy board never makes the agents
unreachable from comments and a burst of questions never takes the slots the
board's runs wait for.

The per-run **turn budget** (`MAX_TURNS`) comes from the agent definition's
`thulu.turns` label (editable per stage), not from an operator env var. Size
the host CPU/RAM and `OCTOPUS_SANDBOX_MEMORY_BYTES` for the peak number of
simultaneous agent and sandbox containers you expect.

### Time limits

| Setting | Default | Meaning |
|---------|---------|---------|
| `agent.max_run_minutes` | `0` | Wall-clock limit on one agent run (stage, mention or auditor) in a workspace that has not chosen its own (see below). At the limit the daemon stops the container and posts a comment on the task naming the limit that run had. `0` is 240 minutes on a self-hosted install and 60 minutes with `OCTOPUS_SAAS_MODE=1`; set up to 1440 for longer runs. On Kubernetes the pod also gets `activeDeadlineSeconds` = limit + 60 s, so the kubelet stops it even if the daemon restarted. |
| `agent.max_run_minutes_ceiling` | `0` | The most a workspace admin may choose as the workspace's own run limit, 0 to 1440 minutes. `0` is 240 minutes with `OCTOPUS_SAAS_MODE=1` and 1440 self-hosted. Nothing goes past 1440 because a run's API key lives 24 hours. |
| `agent.max_queue_minutes` | `120` | How long a run waits for room in its workspace before it gives up (1 to 720). See [Workspace quota and the room line](#workspace-quota-and-the-room-line). |
| `sandbox.max_exec_timeout_ms` | `1800000` (30 min) | Longest a single sandbox command may run. A command that asks for no limit (`timeout_ms: 0`) or for more gets this value. Must be positive. |
| `sandbox.idle_evict_seconds` | `1800` | A sandbox unused this long is removed by the next GC pass, even when no other task is waiting for its slot. A sandbox with a command running is never removed. The task's files stay; its next command starts a fresh sandbox. `0` turns the sweep off. |

Each workspace's admins and owners can set the workspace's own run limit in
**Settings → AI engines → Run time limit**, or with
`PUT /workspace-settings/agent.max_run_minutes` (whole minutes, `null` to go
back to the install default; see the [API reference](api-reference.md#workspace-settings)).
The value goes from 1 minute to the ceiling: a workspace cannot choose
"unlimited". A run in that workspace gets the smaller of its value and the
ceiling, so lowering `agent.max_run_minutes_ceiling` below what a workspace
chose shortens its runs from the next launch and never refuses them. A
workspace without a value gets `agent.max_run_minutes`, even when
that is above the ceiling. Every change leaves a `workspace.setting_changed`
entry in the workspace's audit log, and erasing the workspace removes its
value.

### Chat containers

Each chat runs in its own container, capped by these settings (Docker):

| Setting | Default | Meaning |
|---------|---------|---------|
| `chat.memory_bytes` | `2147483648` (2 GiB) | Memory per chat container. `0` removes the cap. |
| `chat.nano_cpus` | `2000000000` (2 CPUs) | CPU per chat container, in billionths of a CPU. `0` removes the cap. |
| `chat.pids_limit` | `1024` | Processes per chat container. `0` removes the cap. |

When the host is full, a new chat first removes chats nobody has used past
the idle cutoff, then the caller's own workspace's least recently used chat.
It never removes another workspace's active chat; if nothing can go, the
request fails with a message saying the host is full. A workspace at its own
chat limit gets the same treatment within its own chats.

### Plan limits and quotas

With `OCTOPUS_SAAS_MODE=1` every workspace has a plan. Its
limits:

| Plan | Seats | Concurrent agents | Runs / month | Attachment storage |
|------|-------|-------------------|--------------|--------------------|
| Free | 4 | 1 | 100 | 2 GiB |
| Team | 20 | 5 | 1000 | 20 GiB |
| Business | unlimited | 15 | 5000 | 100 GiB |
| Enterprise | unlimited | unlimited | unlimited | unlimited |

- **Runs.** Every stage run and mention run counts when it launches; a run
  that fails to launch is refunded. Auditor runs and chat turns don't count.
  Once a workspace has used its month (UTC calendar months), the next stage
  run is not launched: the task is marked blocked with a comment saying when
  the quota resets. A mention gets the same answer in its thread.
- **Storage.** The total size of a workspace's task attachments: uploads,
  files an agent attaches from the sandbox, and screenshots it leaves in its
  output. An upload that doesn't fit is refused with `413` and a message that
  names the quota and what is left; agent output that doesn't fit is left out
  and logged.
- The workspace named by `OCTOPUS_OPERATOR_WORKSPACE` is unlimited unless the
  operator assigns it a plan explicitly.

`GET /entitlements` shows a workspace its limits plus its usage: `runs`
(`used`, `limit`, `remaining`, `resets_at`; `remaining` is `-1` when
unlimited) and `storage` (`used_bytes`, `quota_bytes`, `remaining_bytes`).

The operator changes a plan, or sets per-workspace overrides, with
`PUT /entitlements?workspace=<id>`:

```json
{"plan": "team", "storage_quota_bytes": 53687091200, "run_quota_monthly": 2000}
```

Each of `seat_limit`, `concurrency_limit`, `run_quota_monthly` and
`storage_quota_bytes` is optional and replaces the plan's default; `0` means
unlimited. Without `plan` the workspace keeps its current plan.

### Kubernetes pod sizing

Agent pods (chart values or env):

| Variable | Default | Meaning |
|----------|---------|---------|
| `OCTOPUS_KUBE_AGENT_CPU_REQUEST`, `OCTOPUS_KUBE_AGENT_CPU_LIMIT` | `250m`, `2` | Agent pod CPU. |
| `OCTOPUS_KUBE_AGENT_MEMORY_REQUEST`, `OCTOPUS_KUBE_AGENT_MEMORY_LIMIT` | `512Mi`, `6Gi` | Agent pod memory. |
| `OCTOPUS_KUBE_AGENT_EPHEMERAL_REQUEST`, `OCTOPUS_KUBE_AGENT_EPHEMERAL_LIMIT` | `1Gi`, `20Gi` | Agent pod ephemeral storage. A pod past its limit is evicted alone instead of filling the node's disk. |
| `OCTOPUS_KUBE_EMPTYDIR_SIZE_LIMIT` | `10Gi` | `sizeLimit` of the worktree `emptyDir` in agent pods. |
| `OCTOPUS_SANDBOX_EPHEMERAL_REQUEST`, `OCTOPUS_SANDBOX_EPHEMERAL_LIMIT` | `1Gi`, `20Gi` | Sandbox pod ephemeral storage. |
| `OCTOPUS_SANDBOX_CACHE_SIZE` | `20Gi` | The workspace's dependency cache claim (`octopus-cache`), mounted at `/cache` in every sandbox of the workspace. It is added on top of `OCTOPUS_TENANT_QUOTA_STORAGE`. `off` turns the cache off, in Docker mode too. |

A malformed value stops the daemon at startup for agent pods and, on
Kubernetes, for the cache size; for the other sandbox settings it falls back
to the default with a log line.

### Workspace quota and the room line

Each workspace's namespace gets a ResourceQuota (`octopus-tenant-quota`).
It admits pods on what they **reserve**, their requests: `requests.cpu`
(`OCTOPUS_TENANT_QUOTA_CPU_REQUESTS`, default `20`) and `requests.memory`
(`OCTOPUS_TENANT_QUOTA_MEMORY_REQUESTS`, default `64Gi`). A pod's limit is its
ceiling, and under Kata also the size of its microVM; pods rarely run at it,
so the quota does not sum CPU limits unless `OCTOPUS_TENANT_QUOTA_CPU` asks
for a ceiling. Memory limits are summed against `OCTOPUS_TENANT_QUOTA_MEMORY`
(default `128Gi`), because memory a pod cannot get is killed, not throttled.

A pod counts its sidecars and Kata's overhead. With the defaults, an agent
with its per-run browser reserves 1 CPU and about 1.7 GiB (250m agent, 500m
browser, 250m microVM), and a sandbox 0.5 CPU and about 0.7 GiB.

When the quota has no room for a pod, the run is not failed:

- A stage run turns `queued` and waits in its workspace's line. Mentions and
  auditor runs wait in the same line and say so in their thread or on the task.
- The line is first in, first out per workspace, and only the first run in it
  retries. Another workspace never waits behind it.
- It retries as soon as any pod of the workspace ends (agent, chat or sandbox:
  the daemon watches them) and otherwise every 5 to 30 seconds.
- While it waits, the run gives back its `agent.max_containers` slot, so a full
  workspace does not hold capacity other workspaces could use.
- Moving the task cancels the wait. After `agent.max_queue_minutes` the run
  gives up, the task turns `failed` and the comment names the last refusal.
- A daemon restart picks up queued stage runs again. A queued resume (an
  answer, a message, requested changes) cannot be picked up: the task gets its
  previous status back and a comment asks to send it again.
- A sandbox the quota refuses waits inside the agent's tool call, retrying
  every 5 seconds for up to 3 minutes, and fails with the quota's reason after
  that. Size `requests.cpu` so each running agent of the workspace also fits
  its sandbox (1.5 CPU per task with the defaults), or agents can hold the room
  their own sandboxes need.

A pod that asks for more than the whole quota (one CPU-hungry pod against a
small `requests.cpu`) fails at once with a message that names the setting to
raise: waiting would never help. The infrastructure page shows, per
workspace, what is reserved against the quota and, below it, the ceiling, and
counts the runs waiting for room under "Queued".

### Process limits and PID 1

Every long-lived Thulu container starts under `tini` as PID 1: agents,
auditors and chats (`octopus-agent`), sandboxes (`octopus-sandbox`,
`octopus-runtime`), the browser (`octopus-mcp-browser`) and the daemon
(`octopus`). tini reaps processes left orphaned when a command is killed (a
dev server's children after `init_run` restarts it, a bash tool the engine
abandoned), so they do not pile up as `<defunct>`, and it forwards the SIGTERM
of `docker stop` or the kubelet, so a stopped run exits with 143 within the
grace period instead of being killed with 137. On Docker the daemon adds
Docker's own init (`HostConfig.Init`) only for an image that does not start
with one, such as a custom `OCTOPUS_SANDBOX_IMAGE`.

On Docker each container has a PID cap: `agent.pids_limit` (1024),
`chat.pids_limit` (1024), `OCTOPUS_SANDBOX_PIDS_LIMIT` (4096) and 1024 for a
per-run browser. Kubernetes has no per-container equivalent; the cap is the
kubelet's `podPidsLimit`, unlimited by default. Set it on every node that runs
tenant pods. 4096 covers the largest pod Thulu starts (a sandbox) with room
for the agent pod's browser sidecar. On k3s, add to `/etc/rancher/k3s/config.yaml`
and restart k3s:

```yaml
kubelet-arg:
  - "pod-max-pids=4096"
```

With a kubelet config file, the field is `podPidsLimit: 4096`. Under Kata the
limit applies to the pod's cgroup on the host, which holds the VM's threads
rather than the processes inside the guest; there the pod's memory limit is
what bounds a fork bomb.

### Infrastructure inspector

Settings → Infrastructure (`/infra/`) shows what the machine has and what is
using it: host CPU, memory, disks and network; every container (Docker) or pod
(Kubernetes) with its kind, tenant, CPU and memory; the admission queues
against `agent.max_containers`, `mention.max_containers`, `sandbox.max` and
`chat.max_containers`; Postgres size and connections; the data directory by
part; and each workspace's load and LLM spend today (UTC). Only admins of
`OCTOPUS_OPERATOR_WORKSPACE` see it. Everyone else, a tenant's owner included,
gets `403` from `GET /infra/overview`, `/infra/history?range=15m|1h|2h` and
`/infra/containers`.

The daemon samples every 15 s and keeps two hours in memory, so the history
starts over on restart. `docker system df` and the data directory walk run
every 10 minutes. In Kubernetes mode live CPU and memory come from
metrics-server (`metrics.k8s.io`), which the Helm chart's ClusterRole may read;
without metrics-server the page lists the pods and quotas and says usage is
missing. The runtime card also has an *Agent image* row for each image the
install builds with Claude Code installed: checking, building (with the Job's
name), the image pods run, or why it was not built (see *Kubernetes: the
agent image the install builds* in `docs/enterprise-setup.md`).

| Variable | Default | Meaning |
|----------|---------|---------|
| `OCTOPUS_INFRA_PROC_ROOT` | `/proc` | Where to read procfs. Inside a container `/proc` reports the container's own network counters; bind-mount the host's `/proc` read-only (e.g. at `/host/proc`) and point this at it to see the host's. |
| `OCTOPUS_INFRA_DISK_PATHS` | empty | More paths whose filesystems to report, comma-separated, such as a host path mounted into the daemon's container. The data directory, `/`, the Docker root and the Postgres data directory are reported when the daemon can see them. |

---

## 8. Cleanup and retention

Thulu removes what it creates. Every container, pod, volume claim and
network it starts carries a Thulu label, and cleanup always selects by
those labels, never by name, so other projects on the same Docker host or
cluster are never touched.

### When things go away

| What | Removed when |
|------|--------------|
| Agent container | Right after its run is processed (`agent.reap_on_exit`, default true). The janitor removes any left behind 2 h after exit. |
| Chat container | After `chat.idle_evict_seconds` idle, on chat delete/reset, or 2 h after it exits. |
| Sandbox (container or pod, plus its Service and worktree claim) | When the task reaches a terminal stage, when the task is deleted, or 6 h after the task stopped in failed / blocked / out of turns / needs more turns. |
| Task worktree | Terminal stage or task delete. A task stuck in a non-terminal status for `retention.stuck_worktree_days` gets its worktree *parked*: pending changes are committed onto the task branch and the checkout is removed; the branch stays, so relaunching the task picks the work up again. |
| Task attachments, run logs, run outputs, usage sessions, run keys | Task delete. |
| Everything a workspace owns, including its Kubernetes tenant namespace | `DELETE /workspaces/{id}`. |
| Terminated agent pods (Kubernetes) | 2 h after exit, once the exit is recorded. |
| Old Thulu images | Every janitor sweep; see *Images* below. |
| Agent image build Jobs (Kubernetes) | 24 h after they finish (`ttlSecondsAfterFinished`); a failed one is replaced on the next attempt. The images they pushed to the bundled registry stay until you remove them. |

The janitor sweeps every hour, in both Docker and Kubernetes mode. It also
checks free space on the data directory every 5 minutes: below
`janitor.disk_free_min_percent` it sweeps at once, removes exited Thulu
containers that finished more than 10 minutes ago and prunes the whole unused
build cache. Pressure sweeps are at least 30 minutes apart.

### Retention settings

Set through `PUT /settings/{key}` (admin). Values are days; `0` keeps
forever.

| Key | Default | Covers |
|-----|---------|--------|
| `retention.run_logs_days` | 30 | Run logs under `<data>/logs`, run output dirs under `<data>/runs` (and `runs/` of directory-backed agents), chat transcripts |
| `retention.sessions_days` | 90 | Per-run usage rows (`sessions`, `session_tool_events`). Pruned whole days only; the daily cost rollup keeps their totals. |
| `retention.webhook_deliveries_days` | 30 | Webhook delivery log and finished outbox rows |
| `retention.run_keys_days` | 7 | Revoked or expired per-run and per-chat API keys. User and operator keys are never pruned. |
| `retention.sandbox_rows_days` | 30 | Rows of torn-down sandboxes and chat containers |
| `retention.stuck_worktree_days` | 14 | Parking of worktrees of stuck tasks (above) |
| `retention.cache_metrics_days` | 90 | The dependency cache's install log and cache measurements (`sandbox_setup_runs`, `sandbox_cache_usage`), charted under Infrastructure |
| `janitor.disk_free_min_percent` | 10 | Free-space floor that triggers a pressure sweep; `0` disables it |
| `audit.retention_days` | unset | Audit log. Unset keeps every entry. |

A run log goes after 30 days while its session row stays until 90, so the
log link of an older session stops working first.

### Container logs

Containers Thulu launches rotate their logs at 10 MB × 3 files when the
Docker daemon's default log driver is `json-file` or `local`
(`OCTOPUS_CONTAINER_LOG_MAX_SIZE`, `OCTOPUS_CONTAINER_LOG_MAX_FILE`;
`OCTOPUS_CONTAINER_LOG_MAX_SIZE=-1` turns rotation off). With another driver
(journald, fluentd…) Thulu leaves the options to it. The compose stacks set
the same rotation on their own services.

### Images

The janitor keeps every image the configuration names (`OCTOPUS_IMAGE`,
`OCTOPUS_AGENT_IMAGE`, the sandbox image, the native MCP servers at
`OCTOPUS_VERSION`), every image a stored agent definition names, every image
a container uses, and every image of the kept versions. It removes the other
images of Thulu's own repositories that were built before the newest image
it keeps of the same kind. One built after it stays: an update pulled but not
applied yet, or the image of another install on the same Docker engine (a
second compose stack, a test stack). An image family nothing claims at all
(an image someone built under a Thulu name) is left alone;
`thulu gc -unclaimed` removes those.

`thulu apply` keeps the last `OCTOPUS_KEEP_VERSIONS` versions (default 3,
the running one included) so `thulu update -rollback` and `-to` work without
a registry, which matters on an air-gapped host. It writes their images, by
digest, to `OCTOPUS_KEPT_IMAGES` in the managed block of `deploy/.env`, and
the janitor never touches those. `thulu update -keep X` pins a version;
`-kept` lists them.

Pre-update snapshots (`backups/*.sql` from the updater and `*.tar.gz` from
`deploy/backup.sh`) are kept by count: the newest `OCTOPUS_BACKUP_KEEP`
(default 5), both kinds together. The snapshot of each kept version is never
pruned. Where the script reads `/data` from (`OCTOPUS_DATA_DIR`,
`OCTOPUS_COMPOSE_FILE` or `OCTOPUS_BACKUP_SOURCE=kubernetes`) and the off-host
copy (`OCTOPUS_BACKUP_REMOTE`, `OCTOPUS_BACKUP_REMOTE_SSH`,
`OCTOPUS_BACKUP_REMOTE_KEEP`, `OCTOPUS_BACKUP_AGE_RECIPIENT`) are in
[deploy.md](deploy.md#backup-and-restore).

### Commands

```sh
thulu gc                 # list what Thulu left on this host and what it keeps
thulu gc -apply          # remove it
thulu uninstall          # list everything an uninstall removes
thulu uninstall -yes     # compose down + remove Thulu containers, images, network, config
thulu uninstall -yes -purge-data   # also the database and data volumes, deploy/.env, backups
```

`thulu uninstall` keeps the data volumes, `deploy/.env` (it holds the
database password and the key that encrypts stored credentials) and the
backups unless `-purge-data` is given. For a Helm install it prints the
`helm uninstall` and `kubectl delete namespace` commands instead of running
them. It does not delete the `thulu` binary; it prints its path.

On a development machine, `make docker-gc` runs `thulu gc` and lists base
images older than the ones the Dockerfiles pin (`APPLY=1` removes them;
`KIND=1` adds `kindest/node` images when no kind cluster is running). Image
builds use BuildKit when `docker buildx` is available and the legacy builder
with `--force-rm` otherwise, so a failed build leaves no containers behind.

Every Dockerfile step that downloads retries a transient network error: npm
up to 5 times (`NPM_CONFIG_FETCH_RETRIES`, a build arg), apt up to 5 times
(`Acquire::Retries`), and apk and `go mod download` up to three attempts with a
growing pause. npm skips an optional dependency whose download fails and still
reports success, so the frontend's postinstall
(`web/scripts/verify-native-deps.mjs`) fails `npm ci` naming the missing native
package (lightningcss, esbuild, rolldown). Running it again
usually works, because the cause is the network.
