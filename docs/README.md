# Thulu Documentation

Thulu runs AI agents on a governed board: a Go daemon with an embedded Astro dashboard, Postgres, and agents launched per stage in Docker or Kubernetes. Each workspace defines its boards, stages, approvals and agents.

- **Editions**: the AGPL core runs as the Community Edition; `ee/` adds licensed features (see [ee-licensing.md](ee-licensing.md))
- **Port**: 8585 (API + embedded frontend, single port)
- **Module**: `github.com/MisterDonnuts/thulu`

## Documents

### How it works

| File | Contents |
|------|----------|
| [architecture.md](architecture.md) | System overview, component diagram, data flow |
| [tasks-and-board.md](tasks-and-board.md) | Task lifecycle, boards, stages and their flags, types, approvals, dependencies, threads |
| [task-sections.md](task-sections.md) | Structured body sections and the Definition of Done checklist |
| [test-cases.md](test-cases.md) | Test cases as rows, runs and evidence |
| [evidence.md](evidence.md) | Evidence: test cases that film themselves, environments, batch runs |
| [skills-authoring.md](skills-authoring.md) | Skills: the workspace catalog, the `octopus-*` system skills, MCP manuals |
| [engines.md](engines.md) | How an engine plugs in: Claude Code, Codex and OpenCode |
| [opencode.md](opencode.md) | OpenCode with any OpenAI-compatible API (DeepSeek, OpenRouter, vLLM, Ollama) |
| [personal-login.md](personal-login.md) | A person's own Claude account for the runs they start, on Docker installs |
| [workload-identity-federation.md](workload-identity-federation.md) | Claude Code without stored keys: the daemon as OIDC issuer |
| [automations.md](automations.md) | Automations: triggers, the 8 step kinds, expressions, guard rails, runs, API |
| [projects.md](projects.md) | Project system: git repos, worktrees, GitHub access |
| [sandbox.md](sandbox.md) | Sandbox containers: MCP protocol, exec tool, preview_url |
| [metrics.md](metrics.md) | The daemon's own Prometheus metric families and labels |
| [api-reference.md](api-reference.md) | All REST endpoints with method, path, auth level, description |
| [auth.md](auth.md) | Roles, identity delegated to the IdP, API keys, env vars |
| [i18n.md](i18n.md) | Locale catalogs (Go, web, MCP, shell) and their guards |
| [frontend.md](frontend.md) | Astro pages, reactive.ts pattern, reconcile rule |
| [development.md](development.md) | Build from source, the development stack, tests, cutting a release |
| [migrations.md](migrations.md) | Writing schema migrations: the expand/contract rule and its CI gates |

### Operator & security

| File | Contents |
|------|----------|
| [deploy.md](deploy.md) | Full deploy guide: Docker Compose, env vars, Caddy TLS, backup |
| [admin-guide.md](admin-guide.md) | Operator runbook: first-run, roles, SSO, offboarding, BYOK, audit, tuning |
| [ee-licensing.md](ee-licensing.md) | Editions and license format |
| [security-hardening.md](security-hardening.md) | Post-install hardening checklist + SOC 2 / ISO 27001 control mapping |
| [upgrade-guide.md](upgrade-guide.md) | Version upgrade, schema migrations, backup-before-upgrade, rollback |
| [troubleshooting.md](troubleshooting.md) | Common issues as symptom → cause → fix |

### MCP integration

| File | Contents |
|------|----------|
| [enterprise-setup.md](enterprise-setup.md) | `thulu init` step by step, MCP selection, images to mirror for an air-gapped install, env vars |
| [mcp-integration.md](mcp-integration.md) | Cross-pattern guide to add an MCP server: wiring model, 3 deploy patterns, generic recipe |
| [mcp-proxy.md](mcp-proxy.md) | How every agent MCP call goes through the daemon at `/mcp-proxy/<name>` |
| [mcp-server-deploy.md](mcp-server-deploy.md) | Runbook for the in-cluster MCPs (octopus-api, thulu-github, thulu-browser, thulu-cloud, thulu-figma) from the Helm chart |
| [thulu-figma.md](thulu-figma.md) | Read-only Figma MCP: tools, the `figma` credential, what Figma does not report, how to turn it on |

## Getting started

- From source: [development.md](development.md#run-it) (`make build-web`, then
  the development stack).
- Deploying it: [deploy.md](deploy.md) for Docker Compose and every required
  variable, and [enterprise-setup.md](enterprise-setup.md) for `thulu init`.
