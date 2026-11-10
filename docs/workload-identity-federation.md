# Workload identity federation (Claude Code)

A `claude-code` profile can authenticate without storing any key. The daemon
acts as an OIDC issuer: for each run it signs an identity token (ES256 JWT)
and the CLI exchanges it at Anthropic for a short-lived access token, under a
federation rule configured in the Claude Console. Someone who reads a
container's environment gets a token that expires with the run and is valid
only under that rule.

## What the daemon needs

A public https address, which is the `iss` of every token:

- `OCTOPUS_WORKLOAD_IDENTITY_ISSUER=https://thulu.example.com`, or
- `OCTOPUS_DOMAIN=thulu.example.com` (the issuer becomes `https://` + the
  domain).

Anthropic compares `iss` byte for byte with the registered issuer, so once it
is registered it should not change. With neither variable set, federated
profiles are refused on save and their runs fail before launch.

The daemon publishes, without authentication and with public keys only:

- `GET /.well-known/openid-configuration`
- `GET /.well-known/jwks.json`

The signing key is generated the first time someone asks for it and stored
encrypted in the secrets store (table `workload_identity_keys`, rotated by
`OCTOPUS_SECRETBOX_*` like every other secret).

## Steps in the Claude Console

1. Register an issuer with the URL above. If Anthropic can reach the daemon
   over the internet (443, public DNS), discovery is enough. If not, choose
   inline JWKS and paste the key set that Settings → AI engines shows when you
   edit a federated profile.
2. Create a service account and a federation rule that uses it. In the rule:
   - audience: `https://api.anthropic.com`;
   - subject: the prefix the form shows, `octopus:workspace:<id>:`, so that
     the rule admits only that workspace's runs;
   - maximum lifetime of the issuer's token: at least the daemon's run
     timeout (see below).
3. In Thulu, Settings → AI engines → new Claude Code profile, credential
   "Workload identity federation", and fill in the rule ID (`fdrl_…`), the
   organization ID (UUID), the service account ID (`svac_…`) and, only if the
   rule spans several Anthropic workspaces, the workspace ID (`wrkspc_…`).

## What each token carries

| Claim | Value |
|---|---|
| `iss` | the issuer above |
| `sub` | `octopus:workspace:<ws>:task:<task>`, or `:chat:<chat>` for a chat with no task |
| `aud` | `https://api.anthropic.com` |
| `workspace_id`, `task_id` / `chat_id`, `run_id` | the run |
| `iat`, `nbf`, `exp` | `exp` is the run's timeout; with no timeout, 24 h |

It carries no `jti`: with a `jti` Anthropic accepts the token only once, and
the CLI presents it again on every exchange. A chat signs a new one-hour token
on every turn.

The default lifetime of an issuer at Anthropic is 1 hour. A run with a longer
timeout needs that maximum raised in the Console; otherwise the exchange fails
once the token has been issued.

## How it reaches the CLI

The dispatcher removes every Anthropic credential from the environment
(`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, the
gateway URL) because any of them, even empty, wins over federation. It passes
`ANTHROPIC_FEDERATION_RULE_ID`, `ANTHROPIC_ORGANIZATION_ID`,
`ANTHROPIC_SERVICE_ACCOUNT_ID`, `ANTHROPIC_WORKSPACE_ID` if there is one, and
the token in `OCTOPUS_ANTHROPIC_IDENTITY_JWT`. The adapter
(`images/snippets/engines/claude-code.sh`) writes it to
`~/.config/octopus/anthropic-identity.jwt` with mode 0600, exports
`ANTHROPIC_IDENTITY_TOKEN_FILE` and removes the copy from the environment. It
works the same in Docker and in Kubernetes because everything travels as run
environment. These runs do not go through the LLM gateway.

## Rotation

`POST /workload-identity/rotate` (installation owner only) adds a key that
starts signing 15 minutes later; the previous one stays published for 25 hours
so the tokens it already signed still validate. With JWKS through discovery
there is nothing to do. With inline JWKS, paste the new set into the Console
within those 15 minutes.

## Claude OAuth token (Pro/Max)

The subscription token (`sk-ant-oat…`) is reserved for the installation's
owner, for testing: the owner role in the workspace named by
`OCTOPUS_OPERATOR_WORKSPACE`. `PUT /llm-key` and `POST`/`PATCH /llm-profiles`
answer 403 to anyone else, and `GET /engines` does not show them that option.
`scripts/setup-auth.sh`
creates one with `claude setup-token`.

When agents run in Kubernetes the token is off for everyone, the owner
included: a shared cluster would run other people's work on one person's
subscription. `GET /engines` does not offer it, storing one answers 403, and
a run whose credential resolves to one (a stored key, a profile or the
daemon's own env) does not start and says why. The operator turns it back on
for the owner with `OCTOPUS_KUBE_ALLOW_CLAUDE_OAUTH_TOKEN=1` in the daemon's
env (`extraEnv` in the chart); the owner-only rule above still applies.
