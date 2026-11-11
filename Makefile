.PHONY: build build-go build-web build-host chart-push vet-foss \
        image sandbox-image kube-agent-image sbom helm-lint k8s-lint k8s-dev conformance \
        release \
        test test-shard test-web test-tools test-pg test-pg-down test-cover coverage-merge coverage-gate vet \
        lint lint-go lint-fix lint-web \
        fmt fmt-check \
        secrets semgrep vuln trivy trivy-config trivy-fs audit \
        typecheck \
        check check-security check-ci check-source \
        migrations-lock migrations-release migrations-check migrations-lint compat \
        up down logs restart dev \
        agent-images runtime-image release-images release-scan scan-image docker-gc \
        install-tools \
        clean

SHELL       := /bin/bash
GO          = go
# Gates `go install` a tool and then call it by name; GOPATH/bin is not on PATH on every machine.
TOOLS_BIN   ?= $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/octopus/tools-bin
export PATH := $(TOOLS_BIN):$(shell $(GO) env GOPATH 2>/dev/null)/bin:$(PATH)
BINARY      = octopus
WEB_DIR     = web
COV_MIN     = 55

ifeq ($(origin VERSION),undefined)
VERSION    := $(shell scripts/dev-version.sh 2>/dev/null || echo dev)
endif
# `thulu update` reads Version back from /info to confirm the new daemon; an unstamped build
# reports "dev".
VERSION_PKG = github.com/MisterDonnuts/thulu/internal/buildinfo
GO_LDFLAGS = -s -w -X $(VERSION_PKG).Version=$(VERSION)
# install.sh's binary: host commands only. hostcli_test.go fails if it links the daemon again.
HOST_TAGS   = hostcli
IMAGE      ?= ghcr.io/misterdonnuts/octopus

# CI calls these same targets. To bump: change the version, then replace each checksum from the
# release's checksums file.
TOOL_OS       := $(shell uname -s | tr '[:upper:]' '[:lower:]')
TOOL_ARCH     := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
TOOL_PLATFORM := $(TOOL_OS)_$(TOOL_ARCH)
SHA256_CHECK  := $(shell command -v sha256sum >/dev/null 2>&1 && echo "sha256sum -c -" || echo "shasum -a 256 -c -")

GOLANGCI_LINT_VERSION = v2.13.2
GOVULNCHECK_VERSION   = v1.8.0
GOIMPORTS_VERSION     = v0.50.0

GITLEAKS_VERSION = 8.30.1
GITLEAKS_ASSET_linux_amd64   = linux_x64
GITLEAKS_ASSET_linux_arm64   = linux_arm64
GITLEAKS_ASSET_darwin_amd64  = darwin_x64
GITLEAKS_ASSET_darwin_arm64  = darwin_arm64
GITLEAKS_SHA256_linux_amd64  = 551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
GITLEAKS_SHA256_linux_arm64  = e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080
GITLEAKS_SHA256_darwin_amd64 = dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709
GITLEAKS_SHA256_darwin_arm64 = b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5

TRIVY_VERSION = 0.70.0
TRIVY_ASSET_linux_amd64   = Linux-64bit
TRIVY_ASSET_linux_arm64   = Linux-ARM64
TRIVY_ASSET_darwin_amd64  = macOS-64bit
TRIVY_ASSET_darwin_arm64  = macOS-ARM64
TRIVY_SHA256_linux_amd64  = 8b4376d5d6befe5c24d503f10ff136d9e0c49f9127a4279fd110b727929a5aa9
TRIVY_SHA256_linux_arm64  = 2f6bb988b553a1bbac6bdd1ce890f5e412439564e17522b88a4541b4f364fc8d
TRIVY_SHA256_darwin_amd64 = 52d531452b19e7593da29366007d02a810e1e0080d02f9cf6a1afb46c35aaa93
TRIVY_SHA256_darwin_arm64 = 68e543c51dcc96e1c344053a4fde9660cf602c25565d9f09dc17dd41e13b838a

SYFT_VERSION = 1.52.0
SYFT_SHA256_linux_amd64  = caeedb81fb0491615f1ebd1761e4145d41ee86dd2cc7bf80669f9f5ad9d6133d
SYFT_SHA256_linux_arm64  = c46d5e4c28e12aa4c5becfaa343ef1c7f89045b6b895f2c21d471c62db09c706
SYFT_SHA256_darwin_amd64 = 56975f5d7ffa9846a1eaf64330647841b878097bc7e3730cb9325f93add96917
SYFT_SHA256_darwin_arm64 = 014d561b6d13059124155f74a6c5a9a99501f5e209313638dd884f39eb418ee6

HELM_VERSION = v3.16.2
HELM_SHA256_linux_amd64  = 9318379b847e333460d33d291d4c088156299a26cd93d570a7f5d0c36e50b5bb
HELM_SHA256_linux_arm64  = 1888301aeb7d08a03b6d9f4d2b73dcd09b89c41577e80e3455c113629fc657a4
HELM_SHA256_darwin_amd64 = 33efd48492f2358a49a231873e8baf41f702b5ab059333ae9c31e5517633c16e
HELM_SHA256_darwin_arm64 = 56413c7fbb496d2789881039cab61d849727c7b35db00826fae7a2685a403344

define install_verified
	@test -n "$(2)" || { echo "ERROR: no pinned checksum for $(notdir $(3)) on $(TOOL_PLATFORM); install it yourself"; exit 1; }
	@echo "==> Installing $(notdir $(3)) from $(1)"
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	  curl -sSfL -o "$$tmp/asset.tgz" "$(1)" \
	  && echo "$(2)  $$tmp/asset.tgz" | $(SHA256_CHECK) >/dev/null \
	  && tar -xzf "$$tmp/asset.tgz" -C "$$tmp" "$(3)" \
	  && mkdir -p "$(TOOLS_BIN)" && install -m 0755 "$$tmp/$(3)" "$(TOOLS_BIN)/$(notdir $(3))" \
	  || { echo "ERROR: $(notdir $(3)) download failed or its checksum did not match"; exit 1; }
endef

# Call pinned binaries by full path: GNU make 3.81 (macOS) resolves simple recipe commands with the
# PATH make started with.
STAMP = $(TOOLS_BIN)/.stamp
GOLANGCI_LINT_BIN = $(STAMP)-golangci-lint-$(GOLANGCI_LINT_VERSION)
GOVULNCHECK_BIN   = $(STAMP)-govulncheck-$(GOVULNCHECK_VERSION)
GOIMPORTS_BIN     = $(STAMP)-goimports-$(GOIMPORTS_VERSION)
GITLEAKS_BIN      = $(STAMP)-gitleaks-$(GITLEAKS_VERSION)
TRIVY_BIN         = $(STAMP)-trivy-$(TRIVY_VERSION)
SYFT_BIN          = $(STAMP)-syft-$(SYFT_VERSION)
HELM_BIN          = $(STAMP)-helm-$(HELM_VERSION)

# Built from source so it matches the module's Go; a prebuilt binary older than go.mod's go
# directive refuses to load packages.
$(GOLANGCI_LINT_BIN):
	@echo "==> Installing golangci-lint $(GOLANGCI_LINT_VERSION)"
	GOBIN=$(TOOLS_BIN) GOFLAGS= $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@touch $@
$(GOVULNCHECK_BIN):
	GOBIN=$(TOOLS_BIN) GOFLAGS= $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@touch $@
$(GOIMPORTS_BIN):
	GOBIN=$(TOOLS_BIN) GOFLAGS= $(GO) install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)
	@touch $@
$(GITLEAKS_BIN):
	$(call install_verified,https://github.com/gitleaks/gitleaks/releases/download/v$(GITLEAKS_VERSION)/gitleaks_$(GITLEAKS_VERSION)_$(GITLEAKS_ASSET_$(TOOL_PLATFORM)).tar.gz,$(GITLEAKS_SHA256_$(TOOL_PLATFORM)),gitleaks)
	@touch $@
$(TRIVY_BIN):
	$(call install_verified,https://github.com/aquasecurity/trivy/releases/download/v$(TRIVY_VERSION)/trivy_$(TRIVY_VERSION)_$(TRIVY_ASSET_$(TOOL_PLATFORM)).tar.gz,$(TRIVY_SHA256_$(TOOL_PLATFORM)),trivy)
	@touch $@
$(SYFT_BIN):
	$(call install_verified,https://github.com/anchore/syft/releases/download/v$(SYFT_VERSION)/syft_$(SYFT_VERSION)_$(TOOL_PLATFORM).tar.gz,$(SYFT_SHA256_$(TOOL_PLATFORM)),syft)
	@touch $@
$(HELM_BIN):
	$(call install_verified,https://get.helm.sh/helm-$(HELM_VERSION)-$(TOOL_OS)-$(TOOL_ARCH).tar.gz,$(HELM_SHA256_$(TOOL_PLATFORM)),$(TOOL_OS)-$(TOOL_ARCH)/helm)
	@touch $@

DAEMON_COMPOSE = deploy/docker-compose.daemon.yml
TOOLS_COMPOSE  = tools/docker-compose.yml

up:
	docker compose -f $(TOOLS_COMPOSE) up -d --build
	OCTOPUS_BUILD_VERSION=$(VERSION) docker compose -f $(DAEMON_COMPOSE) up -d --build
	@echo "==> Thulu up — API http://localhost:8585 (make logs to follow)"

down:
	docker compose -f $(DAEMON_COMPOSE) down
	docker compose -f $(TOOLS_COMPOSE) down

logs:
	docker compose -f $(DAEMON_COMPOSE) logs -f octopus

restart:
	OCTOPUS_BUILD_VERSION=$(VERSION) docker compose -f $(DAEMON_COMPOSE) up -d --build octopus
	@echo "==> daemon restarted"

dev:
	@test -f deploy/.env || { echo "deploy/.env is missing: run 'go run . init -stack deploy/docker-compose.dev.yml' first"; exit 1; }
	docker compose -f deploy/docker-compose.dev.yml up --build

build: build-web
	@echo "==> Building daemon..."
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(GO_LDFLAGS)" -o $(BINARY) .

build-host:
	CGO_ENABLED=0 $(GO) build -tags $(HOST_TAGS) -trimpath -ldflags="-s -w -X $(VERSION_PKG).Version=$(VERSION)" -o $(BINARY) .

build-go:
	CGO_ENABLED=0 $(GO) build ./...

# npm writes node_modules/.package-lock.json on every install, so a fresh clone or a changed lockfile
# reinstalls and an up-to-date tree skips it. go build needs web/dist for its go:embed.
WEB_DEPS = $(WEB_DIR)/node_modules/.package-lock.json
$(WEB_DEPS): $(WEB_DIR)/package-lock.json
	cd $(WEB_DIR) && npm ci

build-web: $(WEB_DEPS)
	@echo "==> Building frontend..."
	cd $(WEB_DIR) && npm run build

# The legacy builder leaves intermediate containers behind on a failed step unless given --force-rm.
BUILDX_OK = $(eval BUILDX_OK := $(shell docker buildx version >/dev/null 2>&1 && echo 1))$(BUILDX_OK)
DOCKER_BUILD = $(if $(BUILDX_OK),DOCKER_BUILDKIT=1 docker build,DOCKER_BUILDKIT=0 docker build --force-rm)
BUILDKIT_NO_ATTEST = $(if $(BUILDX_OK),--provenance=false --sbom=false)

# --no-cache: the legacy builder can resolve FROM octopus-docker to a stale base. No --pull:
# octopus-docker is local-only.
# CODEX_VERSION must match the parser fixture (internal/engine/codex/parse.go).
CODEX_VERSION ?= 0.142.0

# The sandbox image comes along: a source build has no other way to get octopus-runtime:latest.
agent-images: runtime-image
	@echo "==> Building base image octopus-docker..."
	$(DOCKER_BUILD) --no-cache -t octopus-docker images/docker/
	@echo "==> Building octopus-agent:local (the three engine CLIs, codex $(CODEX_VERSION))..."
	$(DOCKER_BUILD) --no-cache --build-arg BASE=octopus-docker --build-arg CODEX_VERSION=$(CODEX_VERSION) \
	  -f images/kube-agent/Dockerfile -t octopus-agent:local .
	@echo "==> Building shared agent image octopus-docker-agent:latest..."
	$(DOCKER_BUILD) --no-cache --build-arg BASE=octopus-agent:local -t octopus-docker-agent:latest images/agent/
	@echo "==> Verifying the engine CLIs landed in the agent image..."
	@fail=0; for cli in claude codex opencode; do \
	  v=$$(docker run --rm --user node -e HOME=/tmp --entrypoint sh octopus-docker-agent:latest -c "$$cli --version 2>/dev/null"); \
	  if [ -z "$$v" ]; then echo "   FAIL $$cli MISSING"; fail=1; else echo "   ok   $$cli: $$v"; fi; \
	done; \
	if [ "$$fail" = 1 ]; then echo "==> agent-images FAILED — a CLI is missing above"; exit 1; fi
	@echo "==> Agent images rebuilt. The daemon uses them on the next launch (NoBuild)."

runtime-image:
	@echo "==> Building octopus-runtime:latest (sandbox runtime)..."
	$(DOCKER_BUILD) -t octopus-runtime:latest images/runtime/
	@echo "==> Verifying the toolchain landed..."
	@docker run --rm --entrypoint sh octopus-runtime:latest -c \
	  'echo "   node $$(node --version)"; echo "   $$(python3 --version)"; echo "   jq $$(jq --version)"'

image:
	$(DOCKER_BUILD) \
	  --build-arg VERSION=$(VERSION) \
	  -f deploy/Dockerfile \
	  -t $(IMAGE):$(VERSION) \
	  .

SANDBOX_IMAGE ?= ghcr.io/misterdonnuts/octopus-sandbox
sandbox-image:
	$(DOCKER_BUILD) -f images/sandbox/Dockerfile -t $(SANDBOX_IMAGE):$(VERSION) .

# --provenance=false: BuildKit's attestation index breaks containerd platform resolution on `ctr
# images import`.
KUBE_AGENT_IMAGE ?= ghcr.io/misterdonnuts/octopus-agent
KUBE_AGENT_BASE  ?= octopus-docker:latest
kube-agent-image:
	$(DOCKER_BUILD) $(BUILDKIT_NO_ATTEST) \
	  --build-arg BASE=$(KUBE_AGENT_BASE) $(if $(ENGINE),--build-arg ENGINES=$(ENGINE)) \
	  $(if $(CLAUDE_CODE_INSTALL),--build-arg CLAUDE_CODE_INSTALL=$(CLAUDE_CODE_INSTALL)) \
	  -f images/kube-agent/Dockerfile -t $(KUBE_AGENT_IMAGE)$(if $(ENGINE),-$(ENGINE)):$(VERSION) .

# Same list as the image/sign-images matrices in octopus-release.yml and the images matrix in
# octopus-preview.yml; change all together.
# Build order matters: the agent images are built FROM octopus-agent-base.
REGISTRY ?= ghcr.io/misterdonnuts
RELEASE_IMAGES = \
  octopus|deploy/Dockerfile|. \
  octopus-agent-base|images/docker/Dockerfile|images/docker \
  octopus-agent|images/kube-agent/Dockerfile|. \
  octopus-agent-claude-code|images/kube-agent/Dockerfile|.|ENGINES=claude-code \
  octopus-agent-codex|images/kube-agent/Dockerfile|.|ENGINES=codex \
  octopus-agent-opencode|images/kube-agent/Dockerfile|.|ENGINES=opencode \
  octopus-sandbox|images/sandbox/Dockerfile|. \
  octopus-runtime|images/runtime/Dockerfile|images/runtime \
  octopus-mcp-octopus-api|tools/octopus-api/Dockerfile|tools/octopus-api \
  octopus-mcp-github|tools/thulu-github/Dockerfile|tools/thulu-github \
  octopus-mcp-browser|tools/thulu-browser/Dockerfile|tools/thulu-browser \
  octopus-mcp-cloud|tools/thulu-cloud/Dockerfile|tools/thulu-cloud \
  octopus-mcp-figma|tools/thulu-figma/Dockerfile|tools/thulu-figma

RELEASE_SPECS = $(foreach s,$(RELEASE_IMAGES),'$(s)')

release-images:
	@set -e; for spec in $(RELEASE_SPECS); do \
	  IFS='|' read -r name file ctx arg <<<"$$spec"; \
	  echo "==> $(REGISTRY)/$$name:$(VERSION)"; \
	  $(DOCKER_BUILD) --build-arg VERSION=$(VERSION) \
	    --build-arg BASE=$(REGISTRY)/octopus-agent-base:$(VERSION) $${arg:+--build-arg "$$arg"} \
	    -f "$$file" -t "$(REGISTRY)/$$name:$(VERSION)" "$$ctx"; \
	done
	$(MAKE) release-scan

# The release workflow runs `make scan-image` with these flags before every push.
IMAGE_SCAN_FLAGS = --scanners vuln --severity CRITICAL --ignore-unfixed --exit-code 1
release-scan:
	@set -e; for spec in $(RELEASE_SPECS); do \
	  $(MAKE) --no-print-directory scan-image SCAN_IMAGE="$(REGISTRY)/$${spec%%|*}:$(VERSION)"; \
	done

scan-image: $(TRIVY_BIN)
	@test -n "$(SCAN_IMAGE)" || { echo "usage: make scan-image SCAN_IMAGE=<image ref>"; exit 1; }
	@echo "==> Scanning $(SCAN_IMAGE) (CRITICAL with a fix available fails)"
	$(TOOLS_BIN)/trivy image $(IMAGE_SCAN_FLAGS) "$(SCAN_IMAGE)"

docker-gc:
	@test -f $(WEB_DIR)/dist/index.html || { echo "docker-gc runs the CLI from source, which embeds the dashboard: run make build-web first"; exit 1; }
	$(GO) run . gc $(if $(APPLY),-apply)
	$(GO) run ./cmd/dockergc-bases $(if $(APPLY),-apply) $(if $(KIND),-kind)

sbom: $(SYFT_BIN)
	$(TOOLS_BIN)/syft scan dir:. -o spdx-json=octopus-$(VERSION).spdx.json
	@echo "==> SBOM written to octopus-$(VERSION).spdx.json"


release:
	@# An unset VERSION arrives here as the dev stamp (0.1.1-dev.g<sha>), so
	@# its origin is what says whether the caller named one.
	@test "$(origin VERSION)" = "command line" -a -n "$(VERSION)" \
	  || { echo "usage: make release VERSION=1.0.0"; exit 1; }
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-(rc|beta|alpha)[.0-9]*)?$$' \
	  || { echo "ERROR: VERSION must be semver without the leading v (0.1.0, 1.2.0-rc1)"; exit 1; }
	@branch=$$(git rev-parse --abbrev-ref HEAD); \
	  test "$$branch" = "main" \
	  || { echo "ERROR: on branch $$branch — a release is cut from main"; exit 1; }
	@test -z "$$(git status --porcelain)" \
	  || { echo "ERROR: the working tree has uncommitted changes; the tag would not describe what gets built"; \
	       git status --short; exit 1; }
	@git fetch --quiet origin main; \
	  test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" \
	  || { echo "ERROR: local main and origin/main differ — push or pull first"; exit 1; }
	@git rev-parse -q --verify "refs/tags/v$(VERSION)" >/dev/null \
	  && { echo "ERROR: tag v$(VERSION) already exists"; exit 1; } || true
	@# The workflow reads the release body out of CHANGELOG.md with awk. No
	@# section means a release published with empty notes, which nobody notices
	@# until it is on the releases page.
	@# A chart installed from a clone pulls its appVersion's images.
	@grep -q '^appVersion: "$(VERSION)"$$' $(HELM_CHART)/Chart.yaml \
	  || { echo "ERROR: $(HELM_CHART)/Chart.yaml appVersion is not \"$(VERSION)\": set it in the release PR"; exit 1; }
	@grep -q "^## \[$(VERSION)\]" CHANGELOG.md \
	  || { echo "ERROR: CHANGELOG.md has no '## [$(VERSION)]' section — the release would ship with empty notes"; exit 1; }
	@# The release PR ran `make migrations-release`: every migration the tag
	@# ships is marked as shipped, so from here on none of them can change.
	@$(GO) run ./cmd/migrations ready -version $(VERSION) \
	  || { echo "ERROR: run 'make migrations-release VERSION=$(VERSION)' in the release PR first"; exit 1; }
	@echo "==> Release notes for $(VERSION):"
	@awk "/^## \[$(VERSION)\]/{f=1; next} f && /^## \[/{exit} f" CHANGELOG.md | head -20
	@echo "    [...]"
	@echo ""
	@if [ -n "$(DRY_RUN)" ]; then \
	  echo "==> DRY_RUN: every check passed. Nothing was tagged and nothing was pushed."; \
	  echo "    Drop DRY_RUN=1 to publish v$(VERSION)."; \
	  exit 0; \
	fi; \
	echo "==> Tagging v$(VERSION) and pushing. The release workflow takes it from here:"; \
	echo "    https://github.com/MisterDonnuts/thulu/actions/workflows/octopus-release.yml"; \
	git tag -a "v$(VERSION)" -m "Thulu v$(VERSION)" && \
	git push origin "v$(VERSION)"

HELM_CHART = deploy/helm/octopus

HELM_SET = --set secrets.jwtSecret=ci-placeholder \
           --set database.url=postgres://ci:ci@postgres:5432/octopus
helm-lint: $(HELM_BIN)
	$(TOOLS_BIN)/helm lint $(HELM_CHART) $(HELM_SET)
	$(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) >/dev/null
	$(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) \
	  --set agentRuntime.mode=docker --set dockerSocket.enabled=true >/dev/null
	$(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) \
	  --set networkPolicy.enabled=true --set mcp.networkPolicy.enabled=true --set ingress.enabled=true \
	  --set ingress.certManager.clusterIssuer=letsencrypt-prod --set caBundle.configMap=corp-ca \
	  --set image.digest=sha256:0000000000000000000000000000000000000000000000000000000000000000 >/dev/null
	$(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) \
	  --set tls.enabled=true --set tls.secretName=octopus-daemon-tls --set caBundle.configMap=corp-ca \
	  --set github.url=https://ghe.acme.example --set secrets.secretboxKey=ci-placeholder \
	  --set config.sso.allowedDomains=acme.example --set config.sso.allowPersonalWorkspaces=false >/dev/null
	$(TOOLS_BIN)/helm template octopus $(HELM_CHART) -f $(HELM_CHART)/values-production.example.yaml >/dev/null
	$(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) --set networkPolicy.enabled=true \
	  --set agentRuntime.imageBuild.registry=registry.example.com/thulu \
	  --set agentRuntime.imageBuild.pushSecret=registry-push >/dev/null
	@! $(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) \
	  --set agentRuntime.imageBuild.registry=registry.example.com/thulu >/dev/null 2>&1 \
	  || { echo "ERROR: chart rendered an external agent image registry without a push secret"; exit 1; }
	$(TOOLS_BIN)/helm lint $(HELM_CHART) -f $(HELM_CHART)/values-saas.example.yaml
	$(TOOLS_BIN)/helm template octopus $(HELM_CHART) -f $(HELM_CHART)/values-saas.example.yaml \
	  | grep -q 'name: octopus-octopus-blocked' \
	  || { echo "ERROR: values-saas rendered without the blocked-paths Ingress"; exit 1; }
	@! $(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) --set ingress.blockedPaths.enabled=true \
	  --set ingress.blockedPaths.traefik.middleware=false >/dev/null 2>&1 \
	  || { echo "ERROR: chart rendered a blocked-paths Ingress that denies nothing"; exit 1; }
	@! $(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) --set tls.enabled=true >/dev/null 2>&1 \
	  || { echo "ERROR: chart rendered daemon TLS without a certificate Secret"; exit 1; }
	@! $(TOOLS_BIN)/helm template octopus $(HELM_CHART) --set secrets.jwtSecret=ci-placeholder >/dev/null 2>&1 \
	  || { echo "ERROR: chart rendered without any database configured"; exit 1; }
	@! $(TOOLS_BIN)/helm template octopus $(HELM_CHART) $(HELM_SET) --set agentRuntime.mode=docker >/dev/null 2>&1 \
	  || { echo "ERROR: chart rendered docker mode without a Docker daemon to reach"; exit 1; }
	@echo "==> Helm chart lints and renders cleanly"

CHART_REPO ?= oci://ghcr.io/misterdonnuts/charts
CHART_REGISTRY = $(word 1,$(subst /, ,$(CHART_REPO:oci://%=%)))
chart-push: $(HELM_BIN)
	@test -n "$(VERSION)" || { echo "usage: make chart-push VERSION=x.y.z"; exit 2; }
	@mkdir -p dist
	@if [ -n "$$CHART_REGISTRY_PASSWORD" ]; then \
	  printf '%s' "$$CHART_REGISTRY_PASSWORD" | $(TOOLS_BIN)/helm registry login $(CHART_REGISTRY) \
	    -u "$$CHART_REGISTRY_USER" --password-stdin; \
	fi
	$(TOOLS_BIN)/helm package $(HELM_CHART) --version $(VERSION) --app-version $(VERSION) -d dist
	$(TOOLS_BIN)/helm push dist/octopus-$(VERSION).tgz $(CHART_REPO) 2>&1 | tee dist/chart-push.log
	@grep -Eo 'sha256:[0-9a-f]{64}' dist/chart-push.log | head -1 > dist/chart.digest
	@test -s dist/chart.digest || { echo "ERROR: helm push printed no digest"; exit 1; }
	@echo "==> $(CHART_REPO)/octopus:$(VERSION)@$$(cat dist/chart.digest)"

k8s-dev:
	@bash scripts/k8s-dev.sh $(or $(ARGS),up)

conformance:
	@test -n "$(KUBECONFIG)" || { echo "==> set KUBECONFIG to the cluster's kubeconfig"; exit 2; }
	OCTOPUS_CONFORMANCE_KUBECONFIG="$(KUBECONFIG)" \
	OCTOPUS_CONFORMANCE_RUNTIMECLASS="$(or $(RUNTIMECLASS),$(OCTOPUS_CONFORMANCE_RUNTIMECLASS))" \
	OCTOPUS_CONFORMANCE_POSTGRES_ADDR="$(or $(POSTGRES_ADDR),$(OCTOPUS_CONFORMANCE_POSTGRES_ADDR))" \
	  go test -count=1 -v -timeout 15m ./test/conformance/...

RENDER = deploy/k8s-data-plane/render.sh
k8s-lint:
	@python3 -c "import yaml" 2>/dev/null || python3 -m pip install --quiet pyyaml 2>/dev/null || python3 -m pip install --quiet --break-system-packages pyyaml
	@command -v envsubst >/dev/null 2>&1 || { echo "==> envsubst (gettext) required"; exit 1; }
	@python3 -c "import yaml,subprocess,os; \
	  R=lambda t,**e: subprocess.run(['sh','$(RENDER)',t],capture_output=True,text=True,check=True,env={**os.environ,**e}).stdout; \
	  list(yaml.safe_load_all(open('deploy/k8s-data-plane/runtimeclass-gvisor.yaml'))); \
	  list(yaml.safe_load_all(open('deploy/k8s-data-plane/runtimeclass-kata.yaml'))); \
	  vap=list(yaml.safe_load_all(open('deploy/k8s-data-plane/vap-runtimeclass.yaml'))); assert len(vap)==2 and vap[0]['kind']=='ValidatingAdmissionPolicy' and vap[1]['kind']=='ValidatingAdmissionPolicyBinding'; \
	  assert 'pods/ephemeralcontainers' in vap[0]['spec']['matchConstraints']['resourceRules'][0]['resources'], 'VAP must cover ephemeral containers'; \
	  docs=list(yaml.safe_load_all(R('tenant-namespace.template.yaml',TENANT_ID='sample',CONCURRENCY_LIMIT='5'))); \
	  kinds=[d['kind'] for d in docs]; assert kinds==['Namespace','NetworkPolicy','NetworkPolicy','NetworkPolicy','ResourceQuota','LimitRange'], kinds; \
	  ns=docs[0]; assert ns['metadata']['labels']['pod-security.kubernetes.io/enforce']=='restricted', 'namespace must enforce PSA restricted'; \
	  ex=docs[2]['spec']['egress'][1]['to'][0]['ipBlock']['except']; assert '169.254.0.0/16' in ex and '10.0.0.0/8' in ex, ex; \
	  cp=docs[3]['spec']; assert [p['port'] for p in cp['egress'][0]['ports']]==[8585,8586,3000] and cp['ingress'][0]['ports'][0]['port']==8587, cp; \
	  egd=list(yaml.safe_load_all(R('egress-strict.template.yaml',TENANT_ID='sample'))); assert len(egd)==1 and egd[0]['kind']=='NetworkPolicy' and egd[0]['spec']['policyTypes']==['Egress'], 'egress-strict must be a single Egress NetworkPolicy'; \
	  assert 8586 in [p['port'] for p in egd[0]['spec']['egress'][1]['ports']], 'egress-strict must allow the LLM gateway'; \
	  kv=list(yaml.safe_load_all(R('kyverno-verify-images.template.yaml'))); assert kv[0]['kind']=='ClusterPolicy' and kv[0]['spec']['validationFailureAction']=='Enforce', 'kyverno policy must Enforce'; \
	  ks=kv[0]['spec']['rules'][0]['verifyImages'][0]['attestors'][0]['entries'][0]['keyless']['subject']; assert ks.endswith('/.github/workflows/octopus-release.yml@refs/tags/v*'), ks; \
	  kk=kv[0]['spec']['rules'][0]['verifyImages'][0]['attestors'][0]['entries'][1]['keys']['publicKeys']; assert kk.strip()==open('deploy/octopus-release.pub').read().strip(), 'kyverno release key must be deploy/octopus-release.pub'; \
	  fr=yaml.safe_load(open('deploy/k8s-data-plane/falco-rules.yaml')); assert 'customRules' in fr, 'falco-rules must define customRules'; \
	  print('==> k8s data-plane manifests parse cleanly (gvisor, kata, VAP, tenant template + PSA/LimitRange, egress-strict, kyverno, falco)')"

# testpg silently t.Skips store tests when nothing listens on :5433; test-pg makes sure something
# does (testpg.DefaultDSN).
TEST_PG_CONTAINER = octopus-test-pg

test-pg:
	@if [ -n "$$OCTOPUS_TEST_POSTGRES_DSN" ]; then \
	  echo "==> test Postgres: using OCTOPUS_TEST_POSTGRES_DSN from env"; \
	elif (exec 3<>/dev/tcp/localhost/5433) 2>/dev/null; then \
	  echo "==> test Postgres: reusing the one listening on :5433"; \
	else \
	  echo "==> test Postgres: starting disposable $(TEST_PG_CONTAINER) on :5433"; \
	  docker run -d --rm --name $(TEST_PG_CONTAINER) -p 5433:5432 \
	    -e POSTGRES_USER=octopus -e POSTGRES_PASSWORD=octopus -e POSTGRES_DB=octopus \
	    postgres:16-alpine >/dev/null; \
	  ok=0; for i in $$(seq 1 30); do \
	    docker exec $(TEST_PG_CONTAINER) pg_isready -U octopus -d octopus >/dev/null 2>&1 && { ok=1; break; }; \
	    sleep 1; \
	  done; \
	  [ "$$ok" = "1" ] || { echo "ERROR: test Postgres did not become ready"; exit 1; }; \
	fi

test-pg-down:
	@docker rm -f $(TEST_PG_CONTAINER) >/dev/null 2>&1 || true

test: test-pg
	GO=$(GO) scripts/ci/go-test-shard.sh all

SHARD ?= rest
test-shard: test-pg
	GO=$(GO) scripts/ci/go-test-shard.sh run $(SHARD)

coverage-merge:
	GO=$(GO) scripts/ci/go-test-shard.sh merge

# .golangci.yml turns govet off for _test.go; this covers it, with CGO_ENABLED=0 as releases build.
vet:
	CGO_ENABLED=0 $(GO) vet ./...
	CGO_ENABLED=0 $(GO) vet -tags $(HOST_TAGS) ./...

# The AGPL core must build without ee/: copy the tree minus ee/ and build it with -tags foss.
vet-foss:
	@set -e; dir=$$(mktemp -d "$${TMPDIR:-/tmp}/octopus-foss.XXXXXX"); trap 'rm -rf "$$dir"' EXIT; \
	  rsync -a --exclude .git --exclude node_modules --exclude /ee/ ./ "$$dir"/; \
	  cd "$$dir" && CGO_ENABLED=0 $(GO) vet -tags foss ./...
	@echo "==> the core builds and vets without ee/"

test-cover: test
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "==> Coverage report: coverage.html"

coverage-gate:
	@if [ ! -f coverage.out ]; then echo "ERROR: coverage.out missing — run 'make test' first"; exit 1; fi
	@COVERAGE=$$($(GO) tool cover -func=coverage.out | grep "^total:" | awk '{print $$3}' | tr -d '%'); \
	echo "Coverage: $${COVERAGE}%  (min: $(COV_MIN)%)"; \
	if [ "$$(echo "$${COVERAGE} < $(COV_MIN)" | bc -l)" = "1" ]; then \
	  echo "ERROR: coverage $${COVERAGE}% is below minimum $(COV_MIN)%"; \
	  exit 1; \
	fi

# THIRD_PARTY_NOTICES lists the licenses of what the binary and web/dist ship; a dependency bump
# that changes them fails here until `make notices` rewrites the file.
# The script reads licenses offline from the module cache, and it needs every
# module in go.mod there: a CI runner only has the ones a Linux build pulled
# (go-winio, for one, is Windows-only). go mod download fills the rest.
notices: $(WEB_DEPS)
	@go mod download
	@scripts/third-party-notices.sh

notices-check: $(WEB_DEPS)
	@go mod download
	@tmp=$$(mktemp); trap 'rm -f "$$tmp"' EXIT; \
	scripts/third-party-notices.sh "$$tmp" || { echo "ERROR: scripts/third-party-notices.sh failed" >&2; exit 1; }; \
	cmp -s "$$tmp" THIRD_PARTY_NOTICES || { \
	  echo "ERROR: THIRD_PARTY_NOTICES is out of date: run make notices" >&2; \
	  diff -u THIRD_PARTY_NOTICES "$$tmp" | head -80 >&2; exit 1; }
	@echo "==> THIRD_PARTY_NOTICES is current."

# grep treats a file with a NUL as binary and reports no matches.
# Files .gitattributes marks binary (text unset) are skipped: an image is meant to hold NUL bytes.
check-source:
	@git ls-files -z | git check-attr -z --stdin text | python3 -c 'import os, sys; \
	f = sys.stdin.buffer.read().split(b"\0"); \
	names = [f[i] for i in range(0, len(f) - 2, 3) if f[i + 2] != b"unset" and os.path.isfile(f[i])]; \
	bad = [n.decode("utf-8", "replace") for n in names \
	       if b"\0" in open(n, "rb").read()]; \
	sys.exit(0) if not bad else \
	  (sys.stderr.write("ERROR: NUL byte in tracked source — grep silently reports zero matches in:\n" \
	                    + "".join("  " + b + "\n" for b in bad)), sys.exit(1))'
	@echo "==> No NUL bytes in tracked source."
	@missing=$$(git ls-files 'ee/*.go' | xargs grep -L 'SPDX-License-Identifier: LicenseRef-Thulu-Enterprise'); \
	if [ -n "$$missing" ]; then echo "ERROR: ee/ files without the Enterprise license header:"; echo "$$missing" | sed 's/^/  /'; exit 1; fi
	@echo "==> Every Go file in ee/ carries the Enterprise license header."

# Release N-1 must run on release N's schema, so a rollback only changes images.
MIGRATIONS_LOCK = internal/schema/migrations.lock
MIGRATIONS_BASE ?= origin/main

migrations-lock:
	$(GO) run ./cmd/migrations lock

migrations-release:
	@test "$(origin VERSION)" = "command line" \
	  || { echo "usage: make migrations-release VERSION=1.1.0"; exit 1; }
	$(GO) run ./cmd/migrations release -version $(VERSION)

# A push that creates a branch reports github.event.before as all zeros: nothing to compare against,
# so the lock is checked on its own.
migrations-check:
	@if echo "$(MIGRATIONS_BASE)" | grep -qE '^0+$$'; then \
	  echo "==> no base commit (first push of the branch): checking the lock on its own"; \
	  : | $(GO) run ./cmd/migrations check -base-stdin; \
	  exit $$?; \
	fi; \
	git rev-parse -q --verify "$(MIGRATIONS_BASE)^{commit}" >/dev/null \
	  || { echo "ERROR: base ref $(MIGRATIONS_BASE) not found: git fetch origin main, or set MIGRATIONS_BASE"; exit 1; }; \
	{ git show "$(MIGRATIONS_BASE):$(MIGRATIONS_LOCK)" 2>/dev/null || :; } \
	  | $(GO) run ./cmd/migrations check -base-stdin

migrations-lint:
	$(GO) run ./cmd/migrations lint

# Described from HEAD^: on a release commit HEAD's own tag is the release being made, whose image
# does not exist yet.
COMPAT_PREV_VERSION ?= $(shell git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' HEAD^ 2>/dev/null | sed 's/^v//')
COMPAT_PREV_IMAGE   ?= $(IMAGE):$(COMPAT_PREV_VERSION)
COMPAT_NEXT_IMAGE   ?= octopus-compat:$(VERSION)
# The previous release's image comes from the registry; when it is not there (a deleted package, a
# fork with no images) it is built from its tag, so the gate never depends on what is published.
# A repository with no v* tag yet has no previous release to run against: skip until the first
# tag. octopus-ci.yml's compat scope step applies the same rule.
compat:
	@if [ -z "$(COMPAT_PREV_VERSION)" ] && [ -z "$$(git tag -l 'v*')" ]; then \
	  echo "==> compat: no previous release in this repository: compat skipped"; \
	  exit 0; \
	fi; \
	mode=$$($(GO) run ./cmd/migrations mode); \
	  echo "==> compat: $(COMPAT_PREV_IMAGE) ⇄ $(COMPAT_NEXT_IMAGE) ($$mode mode)"; \
	  test -n "$(COMPAT_PREV_VERSION)" || { echo "ERROR: no v* tag to compare against (git fetch --tags)"; exit 1; }; \
	  rc=0; \
	  if ! docker image inspect $(COMPAT_PREV_IMAGE) >/dev/null 2>&1 && ! docker pull -q $(COMPAT_PREV_IMAGE) >/dev/null 2>&1; then \
	    echo "==> compat: $(COMPAT_PREV_IMAGE) is not published: building it from v$(COMPAT_PREV_VERSION)"; \
	    git archive v$(COMPAT_PREV_VERSION) | $(DOCKER_BUILD) --build-arg VERSION=$(COMPAT_PREV_VERSION) \
	      -f deploy/Dockerfile -t $(COMPAT_PREV_IMAGE) - || exit $$?; \
	  fi; \
	  { $(DOCKER_BUILD) --build-arg VERSION=$(VERSION) -f deploy/Dockerfile -t $(COMPAT_NEXT_IMAGE) . \
	    && COMPAT_PREV_IMAGE=$(COMPAT_PREV_IMAGE) COMPAT_NEXT_IMAGE=$(COMPAT_NEXT_IMAGE) scripts/compat.sh; } || rc=$$?; \
	  if [ "$$rc" -ne 0 ] && [ "$$mode" = report ]; then \
	    echo "==> compat FAILED (exit $$rc), reported only"; \
	    if [ "$$GITHUB_ACTIONS" = true ]; then echo "::warning title=compat::$(COMPAT_PREV_IMAGE) ⇄ HEAD failed (report mode)"; fi; \
	    exit 0; \
	  fi; \
	  exit $$rc

lint: lint-go lint-web

lint-go: $(GOLANGCI_LINT_BIN)
	$(TOOLS_BIN)/golangci-lint run --timeout 10m
	$(TOOLS_BIN)/golangci-lint run --timeout 10m --build-tags $(HOST_TAGS) . ./internal/cli/...

lint-fix: $(GOLANGCI_LINT_BIN)
	$(TOOLS_BIN)/golangci-lint run --fix --timeout 5m
	cd $(WEB_DIR) && npm run lint:fix

lint-web:
	cd $(WEB_DIR) && npm run lint

test-web:
	cd $(WEB_DIR) && npm test

TOOLS_WITH_TESTS = tools/octopus-api tools/thulu-browser tools/thulu-github tools/thulu-cloud tools/thulu-figma
test-tools:
	@for d in $(TOOLS_WITH_TESTS); do \
	  echo "==> $$d"; \
	  (cd $$d && npm ci --ignore-scripts >/dev/null && node --test --test-timeout=30000) || exit 1; \
	done
	@echo "==> images/snippets"
	node --test --test-timeout=30000 images/snippets/*.test.js

fmt: $(GOIMPORTS_BIN)
	$(TOOLS_BIN)/goimports -w -local github.com/MisterDonnuts/thulu .
	cd $(WEB_DIR) && npm run format

fmt-check: $(GOIMPORTS_BIN)
	@DIFF=$$($(TOOLS_BIN)/goimports -l -local github.com/MisterDonnuts/thulu .); \
	if [ -n "$$DIFF" ]; then echo "$$DIFF"; echo "Run 'make fmt' to fix."; exit 1; fi
	cd $(WEB_DIR) && npm run format:check

secrets: $(GITLEAKS_BIN)
	$(TOOLS_BIN)/gitleaks detect --source . --config .gitleaks.toml --verbose

# Flags must match the `Semgrep scan` step in .github/workflows/octopus-ci.yml; SEMGREP_VERSION
# mirrors its pinned image.
SEMGREP_VERSION = 1.163.0
semgrep:
	@command -v semgrep >/dev/null 2>&1 || { \
	  echo "==> Installing semgrep $(SEMGREP_VERSION)..."; \
	  pip3 install "semgrep==$(SEMGREP_VERSION)" 2>/dev/null || brew install semgrep; \
	}
	@have=$$(semgrep --version 2>/dev/null | head -1); \
	if [ "$$have" != "$(SEMGREP_VERSION)" ]; then \
	  echo "==> NOTE: local semgrep $$have ≠ CI-pinned $(SEMGREP_VERSION); findings may differ. 'pip3 install semgrep==$(SEMGREP_VERSION)' to match."; \
	fi
	semgrep scan \
	  --config p/golang \
	  --config p/owasp-top-ten \
	  --config p/jwt \
	  --config p/secrets \
	  --config .semgrep/ \
	  --exclude-rule go.lang.security.audit.net.cookie-missing-httponly.cookie-missing-httponly \
	  --exclude-rule go.lang.security.injection.open-redirect.open-redirect \
	  --exclude-rule dockerfile.security.missing-user.missing-user \
	  --exclude-rule dockerfile.security.missing-user-entrypoint.missing-user-entrypoint \
	  --exclude-rule go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure \
	  --metrics off \
	  --error \
	  .

vuln: $(GOVULNCHECK_BIN)
	@command -v jq >/dev/null 2>&1 || { echo "==> jq required for vuln allow-list filtering"; exit 1; }
	@set -e; \
	grep -oE '^GO-[0-9]{4}-[0-9]+' .github/govulncheck-allow.txt | sort -u > .govulncheck-allow.tmp; \
	set +e; $(TOOLS_BIN)/govulncheck -format json ./... > govulncheck.json; rc=$$?; set -e; \
	if [ "$$rc" -ne 0 ] && [ "$$rc" -ne 3 ]; then \
	  echo "govulncheck failed to run (exit $$rc)"; rm -f .govulncheck-allow.tmp; exit "$$rc"; \
	fi; \
	jq -r 'select(.finding != null and .finding.trace[0].function != null) | .finding.osv' govulncheck.json | sort -u > .govulncheck-affected.tmp; \
	unexpected=$$(comm -23 .govulncheck-affected.tmp .govulncheck-allow.tmp | sed '/^$$/d'); \
	affected=$$(cat .govulncheck-affected.tmp); \
	rm -f .govulncheck-allow.tmp .govulncheck-affected.tmp; \
	if [ -n "$$unexpected" ]; then \
	  echo "Unaccepted Go vulnerabilities affecting code:"; echo "$$unexpected"; \
	  echo "Fix them, or (if not reachable) add to .github/govulncheck-allow.txt with a reason."; \
	  exit 1; \
	fi; \
	echo "govulncheck OK — affected advisories are all allow-listed: $${affected:-(none)}"

trivy: trivy-config trivy-fs

# Scan the repo root: .trivyignore.yaml matches by path.
TRIVY_HELM_SET = secrets.jwtSecret=ci-placeholder,database.url=postgres://ci:ci@postgres:5432/octopus
trivy-config: $(TRIVY_BIN)
	@echo "==> Scanning Dockerfiles + Helm chart (severity: CRITICAL,HIGH)..."
	$(TOOLS_BIN)/trivy config --severity CRITICAL,HIGH --exit-code 1 \
	  --misconfig-scanners dockerfile,helm --helm-set $(TRIVY_HELM_SET) \
	  --skip-dirs .claude --skip-dirs web/node_modules .

TRIVY_FS_ARGS ?= --format table --exit-code 1
trivy-fs: $(TRIVY_BIN)
	@echo "==> Scanning filesystem (library vulns, severity: CRITICAL)..."
	$(TOOLS_BIN)/trivy fs --scanners vuln --pkg-types library --severity CRITICAL $(TRIVY_FS_ARGS) \
	  --skip-dirs web/node_modules --skip-dirs .claude .

# Keep in lockstep with the CI workflow's npm audit step.
audit:
	@command -v jq >/dev/null 2>&1 || { echo "==> jq required for audit allow-list filtering"; exit 1; }
	@set -e; \
	cd $(WEB_DIR) && npm ci --ignore-scripts >/dev/null; \
	allow=$$(grep -oE '^GHSA-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}$$' ../.github/npm-audit-allow.txt | sort -u); \
	set +e; npm audit --json > npm-audit.json; set -e; \
	affected=$$(jq -r '[.vulnerabilities[] | select(.severity=="high" or .severity=="critical") | .via[] | select(type=="object") | .url | capture("(?<id>GHSA-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4})").id] | unique | .[]' npm-audit.json | sort -u); \
	unexpected=$$(comm -23 <(echo "$$affected") <(echo "$$allow") | sed '/^$$/d'); \
	if [ -n "$$unexpected" ]; then \
	  echo "Unaccepted npm high+ advisories:"; echo "$$unexpected"; \
	  echo "Fix them, or (if not reachable) add to .github/npm-audit-allow.txt with a reason."; \
	  exit 1; \
	fi; \
	echo "npm audit OK — high+ advisories all allow-listed: $${affected:-(none)}"

# Blocking, like the "Astro type check" step in .github/workflows/octopus-ci.yml: an import in the
# wrong <script> block only fails at runtime in the browser.
typecheck:
	cd $(WEB_DIR) && npm run typecheck

# build-web runs first so web/dist exists for the go:embed in vet/test.
check: build-web vet vet-foss lint test test-web test-tools coverage-gate fmt-check typecheck helm-lint k8s-lint check-source notices-check migrations-check migrations-lint
	@echo ""
	@echo "==> All quality checks passed."

check-security: secrets semgrep vuln trivy audit
	@echo ""
	@echo "==> All security checks passed."

check-ci: check check-security compat
	@echo ""
	@echo "==> All CI gates passed locally. Safe to push."

install-tools: $(GOLANGCI_LINT_BIN) $(GOVULNCHECK_BIN) $(GOIMPORTS_BIN) $(GITLEAKS_BIN) $(TRIVY_BIN) $(SYFT_BIN) $(HELM_BIN)
	pip3 install "semgrep==$(SEMGREP_VERSION)"
	@echo "==> Installing frontend tools..."
	cd $(WEB_DIR) && npm ci
	@echo ""
	@echo "==> All tools installed in $(TOOLS_BIN)."

clean:
	rm -rf $(TOOLS_BIN)
	rm -f $(BINARY) coverage.out coverage.html golangci-lint-report.xml semgrep.sarif trivy-fs.sarif govulncheck.json .govulncheck-allow.tmp .govulncheck-affected.tmp octopus-*.spdx.json
	rm -rf $(WEB_DIR)/dist $(WEB_DIR)/node_modules $(WEB_DIR)/.astro
