# Changelog

All notable changes to Thulu are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Fixed

- Release images no longer carry the SLSA provenance as a registry referrer. cosign 2.6 and later verified that referrer instead of the image signature, so `cosign verify --key` and `cosign verify-attestation --type spdxjson` failed on correctly signed 1.0.0 images. The provenance stays in GitHub's attestation store: `gh attestation verify oci://ghcr.io/misterdonnuts/<image>:<version> -R MisterDonnuts/thulu`.

## [1.0.0] - 2026-11-11

First public release.

### Core

- **Stages with their own agent.** Each stage of a board runs the agent
  assigned to it. Your team creates or imports its agents; none come with the
  install.
- **An auditor that can kick back or block.** Turned on per agent and stage
  (none is on in a new install), an auditor reviews a run and passes it, sends
  the task back to another stage or blocks it. Each stage can pick its engine
  and model, and an auditor can use a different engine from its agent.
- **Approvals only a person grants.** A stage can require approvals, an agent
  can request one and a person can add one; any of them holds the task. Only
  a person with the developer role or above decides; an agent never approves.
  The factory board ships with none.
- **Agent questions.** `task_ask` lets an agent ask a person and wait. A
  blocking question parks the task in `awaiting_input` with no container
  running, and the agent resumes the same session.
- **Hash-chained audit log.** One per workspace, verifiable. The external copy
  goes to stdout, stderr or an NDJSON file; there is no connector to a SIEM.
- **LLM gateway.** On by default in Docker Compose and on Kubernetes, the agent's
  container never sees the real model key: it talks to the daemon, which adds it. A Claude Pro/Max OAuth token does
  not go through the gateway.

### License and editions

- The core is AGPL-3.0-only. What lives in `ee/` is under the Thulu
  Enterprise License and turns on with a license in `OCTOPUS_LICENSE_KEY`
  (`secrets.licenseKey` in Helm). Without one, the same binaries and images run
  as Community Edition.
- A license is needed for the Kubernetes runtime (`kubernetes`),
  `GET /audit/export` (`audit_export`) and the Stripe webhook (`saas`). An
  install with `OCTOPUS_AGENT_RUNTIME=kubernetes` and no license that includes
  `kubernetes` does not start.
- Workspaces, people and runs have no limit in either edition.
- Settings → Members tells owners and admins when an install has more people
  than its plan covers (4 without a license, or the license's `seats`). It
  blocks nothing and sends nothing out of the install.
- The license is verified offline against the public key built into the
  binary. An expired license keeps its features for 14 days, then the daemon
  continues as Community and says so in the log.
- `go build -tags foss .` builds the core without `ee/`. See
  `docs/ee-licensing.md`.

### Also included

- **Boards and stages.** Tasks move through customizable stages, each with its
  own agent, MCP servers and approval gates. Task detail with activity,
  subtasks, comment threads, pull requests and test cases.
- **Agent runs.** Claude Code is the main engine; Codex and OpenCode also run
  agents and auditors. Live messages to a running agent and the runner need
  Claude Code. Runs happen in Docker or (with a license) in Kubernetes pods
  with a sandbox per task, on linux/amd64 and linux/arm64 images. Each workspace
  has its own provider keys: Anthropic, Amazon Bedrock or Google Vertex, plus
  OpenAI and OpenAI-compatible APIs for Codex and OpenCode.
- **Runner.** `thulu runner` runs a project's agents on a person's own
  machine with its git, tools and login, on its own clone of the repository.
  Off by default on Kubernetes (`OCTOPUS_RUNNERS`); no UI.
- **`thulu init`.** Interactive setup with forms, a quick mode and the
  runner as a service. `install.sh` installs only the CLI, and
  `thulu init` downloads what the chosen mode needs.
- **MCP proxy.** Agents reach MCP servers through the daemon proxy, with
  per-tool permissions per stage.
- **Bundled MCP servers.** `octopus-api` (61 tools: tasks, boards, comments,
  approvals), `thulu-github` (42: PRs, reviews, Actions), `thulu-browser` (29:
  browsing, screenshots and recorded video), `thulu-sandbox` (7: commands,
  preview URL, init script), `thulu-cloud` (6: read-only cloud observability)
  and `thulu-figma` (6: read-only Figma files).
- **Workshop.** Install agents and skills from the catalog, or import and
  export them as JSON. Each workspace has its own catalog.
- **Projects.** Clone from a Git host or create one by uploading a folder.
  Init scripts with a setup phase, tool versions through mise and a
  per-workspace dependency cache.
- **Cost and limits.** Cost per run, per tool and per delegation; a time limit
  per run for each workspace; a per-workspace quota on what pods reserve, with
  a queue when there is no room (Kubernetes runtime).
- **Workspaces and auth.** Sign-in delegated to an OIDC provider or GitHub;
  roles, workspace switcher and invitations by email (Thulu gives you the
  link to share; it sends no mail); API keys scoped to one
  workspace with explicit permissions.
- **Delegated chat.** A chat can act for the person for a few hours with a
  key limited to tasks, and asks before deleting anything.
- **Updates.** `thulu update` reads a signed release manifest; migrations
  follow expand/contract so the previous release runs on the new schema
  (`docs/migrations.md`).
- **Languages.** Dashboard, CLI, MCP servers and scripts in English and
  Spanish (`OCTOPUS_LOCALE`).
