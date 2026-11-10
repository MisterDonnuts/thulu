# Authentication & Authorization

## Roles

Four roles, in ascending order of privilege:

| Role | Can do |
|------|--------|
| `viewer` | Read-only: list tasks, agents, projects, usage, audit |
| `developer` | viewer + create/update tasks, add comments, relaunch agents |
| `admin` | developer + create/delete projects, manage webhooks, settings, users |
| `owner` | admin + workspace management, member management |

The `RequireRole(minRole, handler)` middleware enforces the minimum role. A user with `admin` can access `viewer` and `developer` routes.

---

## JWT tokens

- Algorithm: **HS256**
- Secret: env `OCTOPUS_JWT_SECRET` (required outside dev mode; the daemon fails closed if unset or shorter than 16 bytes)
- **Access token** TTL: 30 minutes (override with `OCTOPUS_ACCESS_TTL`)
- **Refresh token** TTL: 7 days, with an absolute session cap of `OCTOPUS_SESSION_MAX_LIFETIME` (default 720h)
- Access tokens carry a `token_version` claim re-checked against the DB on every request, so they are **revocable**: a role change or member removal bumps the version and instantly evicts outstanding access tokens.

### Token lifecycle

```
GET /auth/oidc/callback  →  { access_token, refresh_token }
   (30m)                        (7d, absolute cap 30d)

POST /auth/refresh { refresh_token }  →  { access_token, refresh_token }
   (rotates: old token revoked, new one issued in the same family)
   A browser session that refreshes with its cookie gets new cookies and a
   body of only { expires_at, token_type }: tokens in the body would be
   readable by any script on the page, which the HttpOnly cookie is there to
   prevent. Callers that send the refresh token in the body get the tokens.

POST /auth/logout  { refresh_token }  →  revokes the token
```

**Refresh reuse detection:** refresh tokens are grouped into families. Replaying an already-rotated token is treated as theft — the entire family is revoked, the token version is bumped, and an `auth.refresh_reuse_detected` audit event is recorded.

One exception covers two tabs of a browser refreshing at once: for 10 seconds after a token rotates, presenting that same token again returns the pair its rotation issued, as long as that successor is still live. Nothing older is forgiven: with T0 → T1 → T2, presenting T0 is reuse. A token never rotates twice (the update is guarded on `revoked_at IS NULL`), so a race cannot leave a family with two live successors. The grace record lives in the daemon's memory; with several replicas, a repeat that lands on another replica gets a plain 401 instead. The dashboard also serializes refreshes across tabs (`navigator.locks`, or a localStorage lease where the Locks API is missing), and a tab that waited reuses the refresh another tab just finished.

### Sessions

```
GET    /auth/sessions               # list the caller's active sessions (families)
DELETE /auth/sessions/{familyId}    # sign out one device
POST   /auth/sessions/revoke-all    # sign out all other devices (keeps current)
```

### Workspaces and the account

```
GET    /auth/workspaces                     # the caller's workspaces: role, member_count, current
POST   /auth/switch-workspace               # {workspace_id}: move this session into another membership
POST   /auth/workspaces/{id}/transfer       # {user_id}: owner hands the workspace to a member
GET    /auth/workspaces/{id}/invitations    # pending invitations, with awaiting_access
PATCH  /auth/workspaces/{id}/invitations/{invitationId}  # {role}
DELETE /auth/workspaces/{id}/invitations/{invitationId}  # cancel an invitation
DELETE /auth/me                             # {confirm: "<my email>"}: delete my account
```

All three writes need a signed-in person: any API key (an owner key included)
and the no-login dev session get 403.

`GET /auth/me` lists the keys of the identity providers the account is linked
to in `identity_providers` (oldest link first); Settings, Account maps them to
display names through `/auth/config`. `can_create_workspace` says whether
`POST /auth/workspaces` would let this account found a workspace now (personal
workspaces allowed and under `OCTOPUS_MAX_OWNED_WORKSPACES`, or an operator
admin); the sidebar's workspace menu shows "Create workspace" unavailable
when it is false.

- **Switching** rotates the presented refresh token inside its own family, so
  the session keeps its start time and its absolute lifetime cap; switching
  back and forth cannot extend it. The refresh token comes from the cookie
  (dashboard) or the body (CLI); tokens go back in the body only to a client
  that sent one there. The workspace is remembered in
  `users.last_workspace_id` and the next sign-in lands there while the person
  is still a member, otherwise in their oldest membership. An IdP group role
  (`OCTOPUS_OIDC_GROUP_ROLE_MAP`) still applies to the oldest membership only,
  never to the workspace the person switched to. Audited as
  `auth.workspace_switched`.
- **Transfer** sets `owner_id` and both roles in one transaction: the target
  becomes `owner`, the old owner `admin`. The target must already be a member.
  The old owner's API keys in that workspace that carry the `owner` role drop
  to `admin` in the same transaction (a key keeps the role it was minted with,
  and an owner key can erase the workspace). Both people's `token_version` is
  bumped, so access tokens stamped with the old roles stop working and the next
  refresh picks up the new ones; the caller's own browser session gets a fresh
  cookie pair in the response. Audited as `workspace.ownership_transferred`.
- **Account deletion** erases every workspace the account owns alone through
  the same purge as `DELETE /workspaces/{id}` (audited `workspace.deleted` in
  each). If it owns a workspace with other members, the request is refused
  with 409 and a `workspaces` list; the owner transfers or deletes those first.
  Then memberships elsewhere end, refresh tokens and IdP links are deleted, the
  user row goes, cookies are cleared and `user.deleted` is recorded as an
  install-level event (no workspace). API keys the person minted in other
  workspaces, and keys of chats they delegated, are **revoked, not
  reassigned**: a key copies its author's authority, and handing it to another
  member would give them a credential they never chose. The revoked rows stay
  in those workspaces' key lists. Signing in again through the IdP afterwards
  creates a new, empty account.

### Usage

```
Authorization: Bearer <access_token>
```

---

## API keys

API keys are an alternative to JWTs — useful for automation and agent-to-daemon calls.

Every key states three things, and none of them can be left blank:

| | |
|---|---|
| **Workspace** | The one it acts inside, stamped from the caller who minted it. There is no cross-workspace key. |
| **Role** | `viewer` / `developer` / `admin` / `owner`, never above the minter's own. |
| **Permissions** | The resource groups it may touch: `tasks:read`, `tasks:write`, `flows:*`, `projects:*`, `agents:*`, `runs:*`, `credentials:*`, `settings:*`, `members:*`, `keys:write`, `internal:use`. Presets: `read-only`, `ci`, `agent`, `full`. Never more than the minter holds. |
| **Expiry** | Required in effect: at most `OCTOPUS_API_KEY_MAX_TTL` (default `90d`, `0` = no cap), and never later than the key that minted it. |

Role and permissions are ANDed: the role says how much authority, the
permissions say over which part of the API.

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"CI","role":"developer","permissions":["ci"],"expires_in_days":30}' \
  https://thulu.example.com/auth/api-keys
```

- Stored as a SHA-256 hash in the shared DB — plaintext returned only at creation
- Format: `oct_<64-hex-chars>` (256-bit); the list shows only the `oct_`+8-char prefix
- Sent as: `X-API-Key: <key>` or `Authorization: Bearer <key>`
- **Agent containers never receive an operator key** — the dispatcher mints a per-run key scoped to one task, capped at developer, allowed only what a run needs, auto-expiring and revoked when the run exits
- `GET /auth/api-keys` lists only the calling workspace's keys; `DELETE /auth/api-keys/{id}` revokes one of them (another workspace's id answers 404)
- `OCTOPUS_API_KEY` in the daemon environment is a **perimeter gate, not an identity**: a request presenting it still needs a session or a workspace key to reach anything scoped
- Each key has its own rate-limit bucket (`OCTOPUS_RATE_LIMIT_PER_KEY`, default 50 req/s, burst `OCTOPUS_RATE_LIMIT_PER_KEY_BURST` 100); over it the answer is `429` with `Retry-After`

---

## Identity is delegated

Thulu authenticates nobody. There is no password to set, no TOTP secret to
store, no passkey to enrol — the daemon has no credential for a person at all.
Sign-in happens at an identity provider over OIDC, and Thulu starts at the
point the IdP hands back a verified identity.

What that buys: deactivating someone in the IdP removes their Thulu access
with it, there is no credential database here to leak, and the phishing surface
belongs to one system that can be hardened once instead of every app.

What Thulu still owns, keyed by the OIDC `sub`:

| Concern | Owner |
|---|---|
| Who you are (credentials, MFA, recovery, lockout) | The identity provider |
| Workspaces, roles, membership, seat limits | Thulu |
| Session tokens after sign-in (JWT + refresh families) | Thulu |
| API keys for agents, MCPs and CI | Thulu |
| Task-scoped git tokens | Thulu |

Roles are **not** taken from the IdP by default: an admin sets them in the
members panel (`PATCH /auth/workspaces/{id}/members/{userId}`). Orgs that would
rather drive roles from directory groups can wire `OCTOPUS_OIDC_GROUP_ROLE_MAP`
— see [Group → role mapping](#group--role-mapping) — which requires the provider
to emit a groups claim.

The first person to sign in becomes the workspace owner. After that, an admin
adds members.

Membership rules the daemon enforces:

- A role is one of `viewer`, `developer`, `admin`, `owner`. Anything else gets
  a 400, on add and on `PATCH`.
- `POST /auth/workspaces/{id}/members` never grants `owner` and never touches
  the owner's own row: re-adding the owner with another role is a 403, for an
  admin and for the owner alike.
- An email with no account yet is stored as an invitation instead of failing.
  The first sign-in with that email (provider-verified) turns every invitation
  for it into a membership, oldest first, and a newcomer lands in the oldest
  inviting workspace with the invited role instead of getting a personal
  workspace. An existing account picks invitations up at its next sign-in. In
  `allowlist` mode the signup allowlist still decides who may sign in at all.
  Invitations count against the seat limit, and inviting the same email again
  changes its role.
- Re-adding an existing member with a different role is a role change. As with
  `PATCH`, it revokes their refresh tokens and bumps their token version, so
  the new role applies within minutes instead of when the old access token
  expires.

Thulu has no password, registration or MFA endpoints.

---

## OIDC SSO

Configure the OIDC provider via environment variables. An installation has one OIDC issuer, plus GitHub sign-in if you set it up.

### Env vars

| Variable | Description |
|----------|-------------|
| `OCTOPUS_OIDC_ISSUER` | OIDC issuer URL (e.g. `https://accounts.google.com`) |
| `OCTOPUS_OIDC_CLIENT_ID` | OAuth2 client ID |
| `OCTOPUS_OIDC_CLIENT_SECRET` | OAuth2 client secret |
| `OCTOPUS_OIDC_NAME` | Display name shown on login button (default `SSO`) |
| `OCTOPUS_OIDC_REDIRECT_URI` | Callback URL (must match provider config) |
| `OCTOPUS_FRONTEND_ORIGIN` | Frontend origin the post-login redirect targets (e.g. `https://app.example.com`). Set this when the UI is served from a different origin than the daemon (dev, split deploy). |
| `OCTOPUS_ALLOWED_ORIGINS` | Comma-separated extra origins permitted as the post-login redirect target. |
| `OCTOPUS_MAX_OWNED_WORKSPACES` | Most workspaces one account may own through `POST /auth/workspaces`. Default `2` with `OCTOPUS_SAAS_MODE=1`, unlimited otherwise; `0` is no cap. Operator admins are exempt. Past it the call answers 403. |
| `OCTOPUS_OIDC_IDP_LOGOUT` | `1` makes sign-out also end the provider's session; `0` turns that off. Default on with `OCTOPUS_SAAS_MODE=1`, off otherwise. |
| `OCTOPUS_OIDC_POST_LOGOUT_REDIRECT_URI` | Where the provider sends the browser after that sign-out. Default `<frontend origin>/login/`. Register it on the client exactly as written. |

### Signing out of the provider

Signing out revokes Thulu's tokens and clears its cookies. The provider keeps
its own browser session, so without more the next **Sign in** resumes the same
account with no password. With `OCTOPUS_OIDC_IDP_LOGOUT` on and an
`end_session_endpoint` in the provider's discovery document,
`POST /auth/logout` also returns `logout_url`: that endpoint with `client_id`
and `post_logout_redirect_uri` (no `id_token_hint`; Thulu does not keep ID
tokens). The dashboard sends the browser there, and the provider sends it back
to the login page. Google publishes no `end_session_endpoint`, so with Google
nothing changes. A provider that has not registered the redirect URI shows an
error page instead, which is why the default is off outside SaaS mode.

### Redirect-origin allowlist

After a successful login the callback sets the session as **HttpOnly cookies**
(not tokens in the URL) and redirects the browser to the frontend. That redirect
target is validated against an allowlist so a spoofed `Host` header cannot divert
the user to an attacker origin:

- If `OCTOPUS_FRONTEND_ORIGIN` and/or `OCTOPUS_ALLOWED_ORIGINS` are set, **only**
  those origins are accepted — the request `Host` is ignored entirely.
- If neither is set, the redirect falls back to **same-origin** (the host the IdP
  redirected back to), which keeps embedded single-origin deployments working.
- The `next` path is forced to a same-site absolute path (no `//` or scheme), so
  it cannot be used for an open redirect either.

### Flow

```
GET /auth/oidc/authorize?provider=oidc&next=/
  → set an HttpOnly browser-binding cookie (carries the PKCE verifier),
    redirect to the provider with a HMAC-signed state (bound to the cookie) + PKCE challenge + nonce

GET /auth/oidc/callback?code=...&state=...
  → verify state signature + browser binding, exchange code with the PKCE verifier,
    verify the ID-token nonce, then provision/look up the user and set HttpOnly session cookies
```

Hardening: PKCE (S256), an OIDC nonce, and a browser-binding cookie together defeat OAuth login-CSRF / session fixation. The ID token's `email_verified` (and GitHub's `verified` email flag) are enforced.

### Account linking

By default an identity from one provider will **not** auto-attach to an existing account with the same email (an account-takeover vector with multi-tenant IdPs, and with OIDC and GitHub both configured). Set `OCTOPUS_ALLOW_SSO_AUTOLINK=1` to enable it — and even then, only with a provider-verified email.

### Group → role mapping

Set `OCTOPUS_OIDC_GROUP_ROLE_MAP` (e.g. `octopus-admins:admin,octopus-devs:developer`) to drive roles from IdP groups. When configured, the IdP is the source of truth — the mapped role (highest match wins) is re-applied on every SSO login. The groups claim name defaults to `groups`; override with `OCTOPUS_OIDC_GROUPS_CLAIM`.

### Who may sign in

`OCTOPUS_SSO_ALLOWED_DOMAINS` (exact email domains, verified email required) and `OCTOPUS_SSO_ALLOWED_GROUPS` (IdP groups from the groups claim) are checked on every sign-in, OIDC and GitHub alike. GitHub has no groups, so it is refused while a group list is set. Unset admits everyone the provider authenticates.

A first sign-in creates an account only for an email the provider marks verified (`email_verified` in the ID token; GitHub's verified-email flag). Otherwise the login page tells the person to verify the address with their identity provider and try again, and no account is created.

### Signup allowlist

| Variable | Description |
|----------|-------------|
| `OCTOPUS_SIGNUP_MODE` | `open` or `allowlist`. Default `allowlist` when `OCTOPUS_SAAS_MODE=1`, `open` otherwise. Any other value stops the daemon at startup. |
| `OCTOPUS_SSO_ALLOWED_EMAILS` | Comma-separated emails that count as on the list without a row. Use it to let the operator in for the first time; only a restart changes it. |
| `OCTOPUS_WAITLIST_URL` | Where a stranger asks to join, e.g. a form. In `allowlist` mode the login page links it next to the "by invitation" line, and the not-invited screen shows it as "Join the waitlist". Only `http(s)` and `mailto:` URLs are shown. Empty (the default) shows no link. |

In `allowlist` mode every sign-in, not only the first, needs a provider-verified email on the list. A member of `OCTOPUS_OPERATOR_WORKSPACE` (any role, matched through their linked SSO identity, never by email alone) is always admitted, so editing the list cannot lock the operator out. Everyone else is refused with a message saying sign-up is by invitation.

The operator edits the list from **Settings → Team → Access list**, or through the API with an admin credential of the operator workspace (a key needs `members:read` / `members:write`):

```
GET    /auth/signup-allowlist            # {mode, entries: [{email, note, added_by, created_at, source}]}
POST   /auth/signup-allowlist            # {email, note}; adding an email again rewrites its note
DELETE /auth/signup-allowlist/{email}    # 204; 409 for an email that comes from OCTOPUS_SSO_ALLOWED_EMAILS
```

A person the list turns away gets a screen with **Request access**. It posts the signed token the refusal carried (valid one hour) to the public `POST /auth/access-request {token}`, and the email then shows under **Waiting for access** in the same panel, with **Give access** (adds it to the list) and **Dismiss** (`DELETE /auth/signup-requests/{email}`). Requests are audited as `auth.access_requested`.

`source` is `list` for a row and `env` for the seed. Removing an email also revokes that account's refresh tokens and bumps its token version, so the person is out within the access-token TTL instead of when their session family expires. Additions and removals are audited as `auth.signup_allowlist_added` / `auth.signup_allowlist_removed`.

### New-user workspace

`OCTOPUS_SSO_DEFAULT_WORKSPACE=<slug>` places new SSO users in that workspace with `OCTOPUS_SSO_DEFAULT_ROLE` (default `viewer`; `owner` is refused). Without it, a newcomer gets a personal workspace only with `OCTOPUS_ALLOW_PERSONAL_WORKSPACES=1` or in SaaS mode; otherwise the account waits for an admin (`user.pending_access`). The first user of an empty install always gets a workspace. See [admin-guide.md](admin-guide.md#octopus_sso_default_workspace-and-octopus_allow_personal_workspaces).

---

## SAML: federated at the identity provider

Thulu is not a SAML Service Provider. A
SAML-only IdP (ADFS, Shibboleth, a SAML app in Okta) federates into a broker
that speaks both protocols, such as Keycloak, Microsoft Entra ID or Okta's own
OIDC apps, and the broker speaks plain OIDC to Thulu: the same three env vars
as any other IdP.

The reason is containment. SAML's security lives entirely in XML signature
verification, which is the part attackers reach for (signature wrapping,
canonicalization tricks) and the part with the CVE history. Implementing it once
in the service that specializes in identity means one place to audit.

To connect a SAML IdP, add it to the broker as an identity provider, hand its
administrator the broker's SAML metadata, and point `OCTOPUS_OIDC_ISSUER` at
the broker. Nothing else changes on the Thulu side.

---

## GitHub OAuth

| Variable | Description |
|----------|-------------|
| `OCTOPUS_GITHUB_CLIENT_ID` | GitHub OAuth App client ID |
| `OCTOPUS_GITHUB_CLIENT_SECRET` | GitHub OAuth App client secret |

```
GET /auth/github/login     →  redirect to github.com/login/oauth/authorize
GET /auth/github/callback  →  exchange code, fetch user email, issue tokens
```

GitHub OAuth is a CE feature.

---

## Auth config endpoint

`GET /auth/config` returns which methods are available without requiring authentication. The frontend uses this to decide which login options to render:

```json
{
  "has_users": true,
  "jwt_configured": true,
  "api_key_configured": true,
  "sso_providers": [{ "key": "oidc", "name": "Okta", "kind": "oidc" }],
  "legal": {
    "entity": "Example SRL",
    "contact": "privacy@example.com",
    "hosting": "Example Cloud Inc.",
    "hosting_region": "EU"
  }
}
```

`legal` names who operates the install, for the `/legal/terms/` and
`/legal/privacy/` pages. It comes from `OCTOPUS_LEGAL_ENTITY`,
`OCTOPUS_LEGAL_CONTACT`, `OCTOPUS_LEGAL_HOSTING` and
`OCTOPUS_LEGAL_HOSTING_REGION`; unset fields are empty strings and the pages
say they are not configured.

`has_users` distinguishes a fresh install from a configured one — the login page uses it to say the first person in becomes the owner. An empty `sso_providers` on a daemon with auth enabled means nobody can sign in, and the page says so instead of showing a dead button.

---

## Middleware chain (auth portion)

```
JWTMiddleware        — extracts Bearer token, injects *auth.Claims into context
MiddlewareWithStore  — resolves an oct_ key to its workspace/role/permissions;
                       OCTOPUS_API_KEY only opens the perimeter gate
RequireRole(r, h)    — reads claims from context, returns 401/403 if insufficient
```

If both a JWT and an API key are present, the API key takes precedence for internal endpoints.

---

## Audit log integration

All auth events are written to the shared audit log. Key event kinds:

| Kind | Trigger |
|------|---------|
| `user.registered` | Account provisioned on first federated sign-in |
| `user.logged_in` | Successful sign-in through an identity provider |
| `auth.logout` | Logout |
| `auth.session_revoked` / `auth.sessions_revoked_all` | Session(s) signed out |
| `auth.refresh_reuse_detected` | Replayed refresh token — family revoked |
| `user.sso_identity_linked` | SSO identity attached to an account |
| `user.role_synced` | Role updated from IdP group mapping |
| `workspace.member_added` / `member_removed` / `member_role_changed` | Membership change |
| `auth.api_key_created` / `auth.api_key_revoked` | API key lifecycle |
| `user.pending_access` | Signed in, but no workspace to land in; waiting for an admin |

The full log is queryable via `GET /audit` (admin) and exportable as CSV/NDJSON via `GET /audit/export` (Enterprise Edition). Retention is bounded by the `audit.retention_days` setting (unset = retain forever). Each entry records the actor (`actor_type`, `actor_label`: a user, an API key by name, or an agent run) and the credential's workspace; the rows are hash-chained (`GET /audit/verify`) and can be streamed out with `OCTOPUS_AUDIT_SINK`.
