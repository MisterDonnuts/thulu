# Editions and licensing

Thulu is one repository with two licenses:

- Everything outside `ee/` is the core, under the
  [GNU Affero General Public License v3.0](../LICENSE). It runs on its own as
  the Community Edition.
- `ee/` holds the Enterprise features, under the
  [Thulu Enterprise License](../ee/LICENSE). You can read, build and test
  that code; running it in production needs a license from Mister.D.

The released binaries and images include `ee/`. Without a license they behave
exactly like the Community Edition.

## What a license turns on

| Feature | Without it | With it |
|---|---|---|
| `audit_export` | `GET /audit/export` answers 403. The audit log in the UI and `GET /audit` work. | CSV and NDJSON export |
| `kubernetes` | `OCTOPUS_AGENT_RUNTIME=kubernetes` is refused at startup | Agents, chats and sandboxes run as pods, one namespace per workspace |
| `saas` | The Stripe webhook answers 503 | Stripe subscriptions change workspace plans |

A license with `*` covers every feature.

`OCTOPUS_SAAS_MODE` works without a license: everything it does tightens the
install (sign-up allowlist, run limits, refused settings), so an expired
license must never turn it off.

Everything else is the same in both editions, with no limit on workspaces,
people or runs: sign-in through one OIDC issuer or GitHub OAuth, the four
roles, boards, projects, agents, the Docker runtime and sandbox, evidence,
metrics and the audit log.

## Plans

The core is AGPL: anyone can run it, for any number of people, without paying.
Teams of 5 or more that find it useful are asked to pay, and a license is what
turns on `ee/`. Prices are on https://thulu.dev; to buy a license,
[contact sales](https://thulu.dev/contact/).

The Docker runtime is free for any team. The Kubernetes runtime needs a
license, whatever the team's size. A license includes every feature in the
table above and email support. Keys last 12 months.

People are those who signed in at least once and belong to a workspace;
pending invitations and API keys don't count. Settings → Members tells owners
and admins when an install has more people than the plan covers: more than 4
without a license, or more than the license's `seats`. The notice blocks
nothing, hides for 30 days, and sends nothing anywhere.

## Checking the edition

```
GET /info   (anonymous)
→ { "edition": "ce" }

GET /info   (signed in or with an API key)
→ { "edition": "ee", "version": "1.0.0", "licensee": "Acme Corp",
    "features": ["*"], "license_expires_at": "2027-12-31T00:00:00Z" }
```

`license_expires_at` is left out for a license that never expires.

The daemon also logs one `[license]` line at startup saying which edition it
runs and why.

In Go, the core asks `internal/edition`:

```go
edition.Has(edition.AuditExport) // bool
edition.Current()                // Status{Edition, Licensee, Features, ExpiresAt}
```

## Setting a license

Put the token in `OCTOPUS_LICENSE_KEY` (in Helm, `secrets.licenseKey`) and
restart the daemon. The daemon checks it offline against the public key built
into the binary; it never phones home.

Except for the Kubernetes runtime (next paragraph), the daemon does not refuse
to start over a license. A token that is malformed, tampered with, not yet
valid, or expired for more than 14 days makes it run as the Community Edition
and log the reason. In the 14 days after expiry the features stay on and the
log warns every start.

The Kubernetes runtime is the exception: with `OCTOPUS_AGENT_RUNTIME=kubernetes`
and no `kubernetes` in the license, the daemon does not start, because running
the agents in Docker instead would put them somewhere the operator did not
choose. That includes a license that expired more than 14 days ago: the
running daemon keeps going, and the next restart (a pod reschedule counts)
fails. Renew before the grace period ends, or switch the runtime to `docker`.

## The token

A license is `base64url(payload).base64url(signature)`, unpadded. The payload
is JSON:

| Field | Meaning |
|---|---|
| `lid` | License id |
| `org` | Licensee |
| `edition` | `enterprise` by default |
| `features` | Feature names, or `["*"]` |
| `seats` | People the license covers (optional) |
| `iat` | Issued at (Unix seconds) |
| `nbf` | Not valid before (optional) |
| `exp` | Expires at (optional; absent means never) |

The signature is ed25519 over the encoded payload. The public key lives in
`ee/licensing/licensing.go` (`VendorPublicKeyHex`). For a local build with a key
of your own, see
[development.md](development.md#minting-an-ee-license-key-dev).

## Building the core alone

```sh
go build -tags foss .
```

builds the daemon without `ee/`. `make vet-foss` copies the tree without `ee/`
and vets it with `-tags foss`; CI runs it on every change. See
[ee/README.md](../ee/README.md) for how the core and `ee/` connect.
