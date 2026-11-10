# Security Policy

## Supported versions

The last two minor versions are supported. Security fixes land on `main` and
ship as a patch of the latest minor; the minor before it gets the fix as a
patch too while it is supported. An install on either can upgrade straight to
the latest release (each release manifest says so in `upgrade_from`); anything
older upgrades through the oldest supported minor first.

| Version | Supported |
| ------- | --------- |
| latest minor | Yes |
| the minor before it | Yes |
| older | No |

Preview builds (`-dev` versions, see [Preview builds](#preview-builds)) are not
supported. Both editions, the AGPL core and `ee/`, follow the same table.

When a release fixes a vulnerability, its manifest lists it under `advisories`
with the versions affected, and Settings → Updates shows it to an install
running an affected version.

## Reporting a vulnerability

Do not open a public GitHub issue, discussion or pull request for a security
vulnerability. Report it privately with the "Report a vulnerability" button
under the repository's Security tab
(https://github.com/MisterDonnuts/thulu/security/advisories/new). The
report, the discussion and the fix stay in one private advisory that only you
and the maintainers see.

Include:

- what the vulnerability is and what an attacker gains
- steps to reproduce, or a proof of concept
- affected versions or commit, and the deployment mode (Docker compose or
  Helm/Kubernetes)

## What to expect

Thulu is maintained by a small team. The times below are targets we work
to, not guarantees: this policy is not a contract or a service-level
agreement. Support terms for a paid subscription are set in its agreement.

| Step | Target |
| ---- | ------ |
| Acknowledgement | 3 business days |
| Triage (confirmed, not reproducible, or out of scope) with a severity | 7 days |
| Fix released, Critical (CVSS 9.0+) | 7 days after triage |
| Fix released, High (7.0–8.9) | 30 days after triage |
| Fix released, Medium (4.0–6.9) | 90 days after triage |
| Fix released, Low | next scheduled release |

If a fix will miss its target, we tell you why and when to expect it. We
publish a GitHub Security Advisory (with a CVE when one applies) when the fix
ships, and credit you in it and in the release notes unless you prefer to stay
anonymous.

We follow coordinated disclosure: please keep the details private until the
fix is released or 90 days have passed since your report, whichever comes
first, unless we agree on another date in the advisory.

## Safe harbor

In this section "we" means the copyright holder of Thulu named in
[NOTICE](NOTICE), and whoever holds those rights later.

Research is covered when you make a good-faith effort to follow this policy:

- test on an install you run yourself;
- touch only your own accounts and data, and use only as much of a
  vulnerability as you need to show it;
- stop and report as soon as you reach data that is not yours, and do not keep
  or share it;
- do not degrade service for others (no denial of service, spam or load
  tests), and no social engineering or physical attacks;
- report through the channel above and keep the details private as described
  in [What to expect](#what-to-expect).

For covered research, we consider it authorized, we will not bring or support
legal action against you for it, including a claim for working around
technical protections in Thulu. If someone else takes legal action against you for covered
research, we will make it known that it was authorized under this policy.

This covers only claims that are ours to bring. We cannot authorize testing of
an install that someone else operates, or of services that are not ours
(GitHub, a cloud provider, an LLM provider), and we do not bind them. You
remain responsible for following the law where you are. If you are not sure
whether something is covered, ask in a private report before you go further.

## Scope

This policy covers the code in this repository, `ee/` included, and what we
publish from it: release binaries, container images, the Helm chart and
`install.sh`. A problem that comes from how one install is configured belongs
to whoever runs that install.

In scope:

- authentication and authorization bypasses, including between workspaces
- token leakage or session fixation
- SQL injection, command injection, path traversal
- server-side request forgery (SSRF)
- sandbox or agent-container escape, and an agent reaching credentials or
  networks its task was not granted
- secrets exposed in logs, API responses or images
- supply-chain issues in what we publish: release binaries, container images,
  the Helm chart, `install.sh`

Out of scope:

- vulnerabilities requiring physical access to the server
- self-XSS
- social engineering
- denial of service via large payloads (covered by request size limits)
- findings in third-party dependencies with no demonstrated impact on
  Thulu (report those upstream; we track them with govulncheck, npm audit
  and trivy)

## Verifying releases

Every release is built by `.github/workflows/octopus-release.yml` from a tag
and signed twice: with the release key, and keylessly with cosign (Sigstore).

### The release key

An ECDSA P-256 key held in a cloud KMS. It never
leaves KMS; only the release workflow's `release` environment, which admits
only `v*` tags, can ask KMS to sign with it. Its public half is
`deploy/octopus-release.pub`, embedded in the `thulu` binary:

```
-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE9JHJX8TIxbHcnDB7Ub5eEQTU5ey2
nrAyap5TlylCe0Z7wOj87b08g3hPQoaG1je8uIIugWVYOpeFpLDGeYLPFg==
-----END PUBLIC KEY-----
```

It signs every image, the Helm chart (`oci://ghcr.io/misterdonnuts/charts/octopus`)
and `octopus-release.json`, the release manifest: every image and the chart by
digest, the deploy bundle and every binary by sha256, which versions can
upgrade to this one and which one it rolls back to without a restore.
`thulu update` verifies the manifest and every image with the embedded key
before it changes anything, with no network beyond the registry, so an
air-gapped install verifies the same way. By hand:

```bash
cosign verify-blob --key deploy/octopus-release.pub \
  --signature octopus-release.json.sig octopus-release.json
cosign verify --key deploy/octopus-release.pub \
  ghcr.io/misterdonnuts/octopus@sha256:<digest from the manifest>
```

A key rotation ships the new public key in a release signed with the old one;
the binary trusts both until the old one is retired.

### Keyless signatures

The keyless signing identity is that workflow at a `v*` tag:

```
https://github.com/MisterDonnuts/thulu/.github/workflows/octopus-release.yml@refs/tags/v<version>
```

Binaries: the release carries `octopus_<version>_checksums.txt` and its
Sigstore bundle. `install.sh` checks both when cosign is installed. By hand:

```bash
cosign verify-blob octopus_<version>_checksums.txt \
  --bundle octopus_<version>_checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/MisterDonnuts/thulu/\.github/workflows/octopus-release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c octopus_<version>_checksums.txt
```

Container images (`ghcr.io/misterdonnuts/octopus`, `octopus-agent`,
`octopus-agent-claude-code`, `octopus-agent-codex`, `octopus-agent-opencode`,
`octopus-agent-base`, `octopus-sandbox`, `octopus-runtime` and
`octopus-mcp-*`): each is signed by digest and carries an SPDX SBOM as a signed
attestation. Each image was scanned before it was pushed; none ships with a
known CRITICAL vulnerability that had a fix available at release time.

```bash
cosign verify ghcr.io/misterdonnuts/octopus:<version> \
  --certificate-identity-regexp '^https://github\.com/MisterDonnuts/thulu/\.github/workflows/octopus-release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation --type spdxjson ghcr.io/misterdonnuts/octopus:<version> \
  --certificate-identity-regexp '^https://github\.com/MisterDonnuts/thulu/\.github/workflows/octopus-release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

SLSA build provenance is published as a GitHub artifact attestation
(`gh attestation verify`).

To enforce image signatures at admission in Kubernetes, see
`deploy/k8s-data-plane/kyverno-verify-images.template.yaml`. It accepts either
signature; the release-key one needs no access to Sigstore.

### Preview builds

`.github/workflows/octopus-preview.yml` builds a branch on demand as
`<version>-dev.g<sha>`, published as a GitHub prerelease tagged
`preview-v<version>`. Previews are signed only keylessly, with the identity
`https://github.com/MisterDonnuts/thulu/.github/workflows/octopus-preview.yml@refs/heads/main`,
never with the release key. Nothing that trusts releases accepts them:
`thulu update` installs one only when `OCTOPUS_UPDATE_CHANNELS` includes
`preview`, and the Kyverno policy and `install.sh` refuse them.
