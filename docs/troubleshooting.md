# Troubleshooting

Common production issues, each as **symptom → cause → fix**.

Related: [admin-guide.md](admin-guide.md) ·
[security-hardening.md](security-hardening.md) ·
[upgrade-guide.md](upgrade-guide.md) ·
[deploy.md](deploy.md) · [auth.md](auth.md)

---

## Daemon refuses to start

**Symptom:** the container exits immediately; logs say
`OCTOPUS_JWT_SECRET is required` (or `...too short`).

**Cause:** Thulu **fails closed**. Outside dev mode it will not start without a
signing secret of at least 16 bytes (32 hex chars).

**Fix:**
```bash
openssl rand -hex 32   # set as OCTOPUS_JWT_SECRET, then restart
```
For a local, loopback-only dev box you may instead set `OCTOPUS_DEV_MODE=1` and
bind to `127.0.0.1` — never on a networked deployment.

---

## Dev mode: every request is 401

**Symptom:** on a no-login install (`OCTOPUS_DEV_MODE=1`) the dashboard lands
on a page that says to open the link, and `curl` gets 401.

**Cause:** dev mode lets in only the browser holding the dev-session cookie,
which it gets from the link with the token.

**Fix:** run `thulu open` from the install directory (it reads
`OCTOPUS_DEV_TOKEN` from `deploy/.env`), or copy the link the daemon prints on
start. Scripts send `OCTOPUS_API_KEY` as `Authorization: Bearer …`.

---

## Dev mode refuses to start

**Symptom:** with `OCTOPUS_DEV_MODE=1`, startup is refused with a message about
binding to loopback.

**Cause:** dev mode has no logins and only the dev token guards the owner, so
the daemon only allows it when bound to loopback.

**Fix:** bind to loopback (`-addr 127.0.0.1:8585`) for dev, or unset
`OCTOPUS_DEV_MODE` and provide a real `OCTOPUS_JWT_SECRET` for any networked use.

Inside Kubernetes the message is "OCTOPUS_DEV_MODE=1 is refused inside
Kubernetes". There is no override: a pod's loopback is reachable through
`kubectl port-forward` and any sidecar, so the loopback interlock does not hold.
Turn off `config.devMode` in the Helm values and configure a JWT secret.

---

## Redirect loop between `/login` and `/`

**Symptom:** the browser bounces between `/login` and `/`; you can log in but get
kicked back; sessions break after every restart.

**Cause:** the JWT signing secret is **not stable across restarts** — either
`OCTOPUS_JWT_SECRET` is unset (the daemon then generates an ephemeral key each
boot) or it changes between restarts. All previously issued tokens become invalid
on the new key.

**Fix:** set a fixed `OCTOPUS_JWT_SECRET` from your secret store and keep it
constant across restarts/replicas. Clear the browser's stored tokens and log in
again after fixing it.

---

## Postgres connectivity / pool exhaustion

**Symptom:** `pq: too many connections for role` or `connection refused`
under load; `/health/ready` flips to `not_ready`.

**Cause:** every store shares one pooled `*sql.DB` capped at 25 open + 5
idle connections (`internal/db/db.go`). If the Postgres server's
`max_connections` is set lower than that times the number of daemon
replicas, you'll exhaust the server before the pool.

**Fix:**
- Raise `max_connections` on the Postgres side (or run pgbouncer in front).
- Verify `OCTOPUS_DATABASE_URL` resolves to a reachable host (`psql
  "$OCTOPUS_DATABASE_URL" -c "SELECT 1"`).
- Make sure backups don't lock long-running transactions — `pg_dump`'s
  consistent snapshot is fine, but ad-hoc `psql` sessions left open with
  `BEGIN` aren't.

---

## Docker socket / daemon unreachable

**Symptom:** sandboxes and agents never start; `/health` shows the `docker`
check failing; logs mention the Docker socket or `DOCKER_HOST`.

**Cause:** the daemon can't reach the Docker API. In the bundled compose it
mounts `/var/run/docker.sock` and sets
`DOCKER_HOST=unix:///var/run/docker.sock`.

**Fix:**
- Verify the socket is mounted into the container and the path matches
  `DOCKER_HOST`.
- On **Colima/macOS**, the Docker Desktop default socket path does **not** work —
  point `DOCKER_HOST` at the Colima socket
  (`unix://$HOME/.colima/default/docker.sock`).
- Check the runtime user can read the socket (group/permissions on
  `/var/run/docker.sock`).

---

## TLS / Let's Encrypt failures

**Symptom:** browser TLS errors; Caddy logs show ACME challenge failures; no
certificate is issued.

**Cause:** Caddy can't complete the ACME challenge — usually DNS or port
reachability, or a wrong domain.

**Fix:**
- `OCTOPUS_DOMAIN` must be a real domain whose **A/AAAA record points at this
  host**.
- Ports **80 and 443** must be reachable from the internet (80 is required for
  the HTTP-01 challenge).
- Don't hammer issuance while debugging — Let's Encrypt rate-limits. Inspect
  `docker compose logs caddy` for the specific ACME error.

---

## Daemon TLS (`OCTOPUS_TLS_CERT_FILE`)

**"OCTOPUS_TLS_CERT_FILE and OCTOPUS_TLS_KEY_FILE must be set together":** only
one is set. Set both, or neither to serve plain HTTP behind a proxy.

**Probes fail after enabling TLS:** the listener now speaks only HTTPS. Switch
the liveness/readiness probes (`scheme: HTTPS`) and any `http://` client,
including the CLI's daemon URL, to `https://`.

**Renewed certificate not served:** the daemon checks the files at most every
30 seconds, and keeps the previous pair if the new one does not load; the log
says why ("new key pair on disk does not load").

---

## The provider says the account is locked

**Symptom:** a user can't sign in and the provider says the account is locked.

**Cause:** the lock is at the identity provider. Thulu never sees a credential
and has no lockout of its own — nothing here counts failed attempts.

**Fix:** clear it in the provider's admin console (Okta, Entra, Keycloak…).
Thulu's audit log will show no failed sign-ins for this, because the failures
never reached it.

---

## SSO callback errors

### "SSO session not found / mismatch — start the login again"

**Symptom:** OIDC login fails at the callback with a session-not-found or
mismatch message.

**Cause:** the login uses a **browser-binding cookie** tied to the signed
`state`. The cookie is missing or doesn't match — typically because the login was
**started in one browser/device and finished in another**, the cookie was cleared
mid-flow, the flow took longer than 10 minutes (cookie/`state` expiry), or the
browser blocks the cookie.

**Fix:** start the SSO login fresh **in the same browser** that will complete it.
Ensure the site is served over HTTPS (the bind cookie is `Secure`) and that
third-party-cookie blocking isn't stripping a same-site `Lax` cookie on the IdP
redirect.

### "redirect to an untrusted origin" / tokens not delivered after login

**Symptom:** SSO succeeds at the IdP but the post-login redirect is rejected, or
lands on the wrong origin.

**Cause:** the post-login redirect target is validated against an allowlist so a
spoofed `Host` can't divert freshly issued tokens. The target origin isn't on the
allowlist.

**Fix:** set `OCTOPUS_FRONTEND_ORIGIN` (and/or `OCTOPUS_ALLOWED_ORIGINS`) to the
exact origin the dashboard is served from. If the UI and daemon share one origin,
leaving both unset uses same-origin and "just works."

### "an account with this email already exists"

**Symptom:** an SSO login is rejected because the email already belongs to an
account from another provider.

**Cause:** SSO auto-link is **off by default** (`OCTOPUS_ALLOW_SSO_AUTOLINK=0`)
to prevent account takeover.

**Fix:** sign in with the original provider instead, or — only if you trust the IdP's
email verification — set `OCTOPUS_ALLOW_SSO_AUTOLINK=1` (linking still requires a
provider-verified email). See [admin-guide.md](admin-guide.md#octopus_allow_sso_autolink).

### "not allowed to sign in to this install"

**Cause:** `OCTOPUS_SSO_ALLOWED_DOMAINS` or `OCTOPUS_SSO_ALLOWED_GROUPS` refused
the identity. The message says which: a domain outside the list, an email the
provider does not mark verified, or no allowed group. GitHub sign-in is always
refused while a group list is set, because GitHub sends no groups.

**Fix:** add the domain or group, or add the user to the group in the IdP. If
groups are configured but never match, check that the IdP puts them in the
token under the claim named by `OCTOPUS_OIDC_GROUPS_CLAIM`.

### "your account has no workspace yet"

**Cause:** the sign-in worked, but nothing placed the user in a workspace:
`OCTOPUS_SSO_DEFAULT_WORKSPACE` is unset and personal workspaces are off (the
default with OIDC). The audit log has a `user.pending_access` entry.

**Fix:** an admin adds the user by email from **Settings → Members**; their
next sign-in lands there. To route every newcomer automatically, set
`OCTOPUS_SSO_DEFAULT_WORKSPACE`.

### "email unverified" on SSO login

**Symptom:** SSO login rejected with an unverified-email message.

**Cause:** Thulu requires the provider to assert `email_verified: true`; a
missing claim counts as unverified. GitHub requires a verified primary email.

**Fix:** verify the email with the identity provider, then retry.

---

## Webhook or automation request "blocked by the egress policy"

**Symptom:** a webhook delivery or an automation `http` step fails with
"destination blocked by the egress policy: 10.0.3.7 is a private address".

**Cause:** outbound requests to loopback, private, link-local and cloud
metadata addresses are refused, on the resolved address, to stop the daemon
being used to reach internal services (SSRF).

**Fix:** if the receiver really is internal, add its range:
`OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS=10.0.3.0/24`. Metadata addresses cannot be
allowed. `OCTOPUS_EGRESS_GUARD=off` turns the guard off for the whole daemon.

---

## Stored secrets fail to decrypt

**"stored secret was sealed with a key this install does not have":** the
value was encrypted with an `OCTOPUS_SECRETBOX_KEY` that is neither the current
key nor in `OCTOPUS_SECRETBOX_PREVIOUS_KEYS`. Put the old key back in the list.

**"stored secret is not encrypted":** a row holds plaintext, and the startup
rewrite is off (`OCTOPUS_SECRETBOX_REWRAP=0`) or has not run yet. Re-enable it
and restart, which encrypts the row; `OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT=1` reads
it as-is in the meantime.

**Everything stopped decrypting after rotating `OCTOPUS_JWT_SECRET`:** without a
dedicated key the stored secrets were encrypted with a key derived from the JWT
secret. Restore the old JWT secret, set `OCTOPUS_SECRETBOX_KEY`, restart so the
rewrite moves every value to it, then rotate the JWT secret.
