# Thulu Enterprise (`ee/`)

Everything under `ee/` is licensed under the [Thulu Enterprise License](LICENSE),
not the AGPL-3.0 that covers the rest of the repository. You can read it,
build it and try it; running it in production needs a license from Mister.D.

## What lives here

| Package | Feature |
|---|---|
| `ee` | Reads `OCTOPUS_LICENSE_KEY` at startup and turns on the features the license names |
| `ee/licensing` | ed25519 license tokens: sign, verify, expiry with a 14-day grace |
| `ee/cmd/license` | The license issuing tool |
| `ee/auditexport` | Audit log export as CSV or NDJSON (`audit_export`) |
| `ee/billing` | Stripe webhooks that change a workspace's plan (`saas`) |
| `ee/kuberuntime` | Agents, chats and sandboxes as Kubernetes pods, one namespace per workspace (`kubernetes`) |

## How the core uses it

The core declares extension points (`internal/edition`, `audit.Exporter`,
`entitlements.BillingProvider`) and `ee/` fills them from an `init` function.
The daemon binary links `ee/` through `ee_build.go` at the repository root.

The Kubernetes runtime is wired into several core packages, so those packages
keep a thin layer of glue in files tagged `//go:build !foss` (`kube_ee.go`,
`kubelauncher.go`, `backend_kube.go` and the like). Only those files import
`ee/`; each has a `foss` counterpart that stands in when `ee/` is absent. The
names both sides share (pod labels, `RunningPod`) live in
`internal/kubeapi`.

Without a license the core runs as the Community Edition: no audit export, no
Stripe billing, and `OCTOPUS_AGENT_RUNTIME=kubernetes` refused at startup.
Workspaces have no limit in either edition.

Without `ee/`, or without a valid license, the daemon runs as the Community
Edition. An expired, tampered or not-yet-valid license does not stop the
daemon, except on the Kubernetes runtime: it logs why and runs as Community.

To build the AGPL core alone:

```sh
go build -tags foss .
```

`make vet-foss` copies the tree without `ee/` and vets it with `-tags foss`;
CI runs it on every change.

## Licenses

A license is a token signed with an ed25519 key. The daemon checks it
offline against the public key in `ee/licensing/licensing.go`; it never phones
home.

```sh
go run ./ee/cmd/license keygen
OCTOPUS_LICENSE_SIGNING_KEY=<private hex> go run ./ee/cmd/license \
  -licensee "Acme Corp" -features "*" -expires 2027-12-31
```

Features: `audit_export`, `kubernetes`, `saas`, or `*`.
