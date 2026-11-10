# USER is numeric: PSA-restricted namespaces cannot verify runAsNonRoot for a named user.
# Docker mode overrides it with User 0:0 (types.Launch) so the entrypoint can chown mounts before
# gosu drops to node.
ARG BASE=ghcr.io/misterdonnuts/octopus-agent-base:latest

# Cross-compiled on the build platform: a multi-arch release would otherwise run Go under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS execagent-build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do go mod download && break; \
      [ "$attempt" = 3 ] && exit 1; echo "go mod download failed, retry $attempt/2" >&2; sleep $((attempt * 15)); \
    done
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/sandbox-execagent ./cmd/sandbox-execagent

FROM ${BASE}

# Each ENGINES entry needs an internal/engine/<name> engine and an images/snippets/engines/<name>.sh
# adapter.
ARG ENGINES="claude-code codex opencode"
ARG CLAUDE_CODE_VERSION=2.1.280
# first-run (default, what the release publishes): the image carries no copy of Claude Code; the
# shim images/snippets/claude-first-run.sh installs the pinned package from the npm registry on first
# use. build: install it now, for an image you build for your own machines (offline, air-gapped, or
# to skip the first-run download in every Kubernetes pod).
ARG CLAUDE_CODE_INSTALL=first-run
# internal/engine/codex/parse.go is locked to this version's `codex exec --json` format by
# TestParseStreamRealFixture; re-capture that fixture when bumping CODEX_VERSION.
ARG CODEX_VERSION=0.142.0
# codex speaks only stdio and streamable-HTTP; the native MCP servers use classic SSE, so codex needs
# mcp-remote as a bridge (images/snippets/engines/codex.sh, mcp-codex-args.js).
ARG MCP_REMOTE_VERSION=0.1.38
# internal/engine/opencode/parse.go reads this version's events (internal/engine/testdata/opencode);
# re-capture when bumping.
# Only the platform package: the opencode-ai launcher pulls two ~185 MB binaries.
# The x64 build needs AVX2 (else opencode-linux-x64-baseline).
ARG OPENCODE_VERSION=1.18.35
ARG NPM_CONFIG_FETCH_RETRIES=5
ARG NPM_CONFIG_FETCH_RETRY_MAXTIMEOUT=120000
COPY images/snippets/claude-first-run.sh /tmp/claude-first-run.sh
# The smoke test of opencode runs only on the native build: under QEMU a Bun binary can crash for
# reasons that say nothing about the image.
ARG BUILDPLATFORM
ARG TARGETPLATFORM
RUN set -eu; \
    for engine in ${ENGINES}; do \
      case "$engine" in \
        claude-code) \
          case "${CLAUDE_CODE_INSTALL}" in \
            build) npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" ;; \
            first-run) \
              sed "s/@CLAUDE_CODE_VERSION@/${CLAUDE_CODE_VERSION}/" /tmp/claude-first-run.sh >/usr/local/bin/claude; \
              chmod 755 /usr/local/bin/claude ;; \
            *) echo "CLAUDE_CODE_INSTALL must be first-run or build, not ${CLAUDE_CODE_INSTALL}" >&2; exit 1 ;; \
          esac ;; \
        codex) npm install -g "@openai/codex@${CODEX_VERSION}" "mcp-remote@${MCP_REMOTE_VERSION}" ;; \
        opencode) \
          case "$(dpkg --print-architecture)" in \
            amd64) oc=opencode-linux-x64 ;; \
            arm64) oc=opencode-linux-arm64 ;; \
            *) echo "no OpenCode build for $(dpkg --print-architecture)" >&2; exit 1 ;; \
          esac; \
          npm install -g "${oc}@${OPENCODE_VERSION}"; \
          ln -s "$(npm root -g)/${oc}/bin/opencode" /usr/local/bin/opencode; \
          if [ "$BUILDPLATFORM" = "$TARGETPLATFORM" ]; then HOME=/tmp/opencode-check opencode --version; fi ;; \
        *) echo "unknown engine in ENGINES: $engine" >&2; exit 1 ;; \
      esac; \
    done; \
    npm cache clean --force; \
    rm -rf /root/.npm /tmp/*

COPY --from=execagent-build /out/sandbox-execagent /usr/local/bin/sandbox-execagent

COPY images/entrypoint-base.sh      /workspace/entrypoint.sh
COPY images/kube-agent-entrypoint.sh /workspace/kube-entrypoint.sh
COPY images/snippets                /workspace/snippets

# /opt/thulu-engines is where Docker mounts the engine volume (internal/enginecache). Owned by node
# here so Docker seeds a new, empty volume with that owner, rootless agents included.
RUN mkdir -p /workspace/runs /workspace/instructions /workspace/skills /workspace/tools /opt/thulu-engines \
  && chmod +x /workspace/entrypoint.sh /workspace/kube-entrypoint.sh \
  && chown -R 1000:1000 /workspace /opt/thulu-engines

# The Kubernetes runtime reads these to decide whether to build an image with Claude Code installed
# (ee/kuberuntime/imagebuild.go): it builds only from first-run images.
LABEL io.thulu.claude-code.install="${CLAUDE_CODE_INSTALL}" \
      io.thulu.claude-code.version="${CLAUDE_CODE_VERSION}"

ENV HOME=/home/node
USER 1000:1000

# tini reaps orphans the engine leaves and handles SIGTERM. -g: the entrypoint never forwards
# signals, so the whole group gets them.
# No -s: tini is PID 1 (internal/dockerx/init.go adds no Docker init for images starting with tini).
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/bin/bash", "/workspace/kube-entrypoint.sh"]
