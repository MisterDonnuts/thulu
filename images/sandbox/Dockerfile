# Cross-compiled on the build platform: a multi-arch release would otherwise run Go under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do go mod download && break; \
      [ "$attempt" = 3 ] && exit 1; echo "go mod download failed, retry $attempt/2" >&2; sleep $((attempt * 15)); \
    done
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/sandbox-execagent ./cmd/sandbox-execagent

FROM node:22-trixie-slim@sha256:c5849ff9c9ebcd66615412f0b548ca5b8ecaef84003dc9ac2e077ebe46aaa3f6

RUN echo 'Acquire::Retries "5";' > /etc/apt/apt.conf.d/80-retries

ARG NPM_CONFIG_FETCH_RETRIES=5
ARG NPM_CONFIG_FETCH_RETRY_MAXTIMEOUT=120000
# The npm bundled with Node 22 vendors a `tar` with a CRITICAL advisory (fixed in 7.5.19).
ARG NPM_VERSION=11.20.0
ARG TINI_VERSION=0.19.0
RUN apt-get update && apt-get install -y --no-install-recommends \
    "tini=${TINI_VERSION}-*" \
    git \
    python3 \
    python3-pip \
    build-essential \
    curl \
    ca-certificates \
    procps \
    jq \
    less \
    iproute2 \
    netcat-openbsd \
    bash \
    zstd \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g "npm@${NPM_VERSION}" \
 && npm cache clean --force \
 && rm -rf /root/.npm /tmp/*

# TARGETARCH is empty under the legacy builder; dpkg names the same arch.
ARG MISE_VERSION=2026.10.3
ARG MISE_SHA256_AMD64=8d48bc510b7d844fad0bc7156c0855c2a1e7230c51e498a7f6b63b021f8b57c5
ARG MISE_SHA256_ARM64=357260e28904569a6e7124d33b65cef6d043846c07bb8f4906ddf22cc353d61d
ARG TARGETARCH
RUN arch="${TARGETARCH:-$(dpkg --print-architecture)}" \
 && case "$arch" in \
      amd64) asset=x64; sum="$MISE_SHA256_AMD64" ;; \
      arm64) asset=arm64; sum="$MISE_SHA256_ARM64" ;; \
      *) echo "mise: no pinned build for $arch" >&2; exit 1 ;; \
    esac \
 && curl -fsSL --retry 5 -o /usr/local/bin/mise \
      "https://github.com/jdx/mise/releases/download/v${MISE_VERSION}/mise-v${MISE_VERSION}-linux-${asset}" \
 && echo "${sum}  /usr/local/bin/mise" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/mise \
 && mise --version

# /etc/profile resets PATH in `bash -lc`, so this hook must put the mise shims first; pod env alone
# does not reach the command.
# Keep in sync with images/runtime.
RUN printf '%s\n' \
      '# Thulu: the workspace dependency cache (internal/sandbox/cache.go).' \
      'if [ -n "${OCTOPUS_CACHE_DIR:-}" ] && [ -d "$OCTOPUS_CACHE_DIR" ]; then' \
      '  for d in npm go/mod go/build pip mise snapshots; do' \
      '    [ -d "$OCTOPUS_CACHE_DIR/$d" ] && continue' \
      '    mkdir -p "$OCTOPUS_CACHE_DIR/$d" 2>/dev/null || continue' \
      '    if [ "$(id -u)" = 0 ]; then' \
      '      chown 1000:1000 "$OCTOPUS_CACHE_DIR" "$OCTOPUS_CACHE_DIR/${d%%/*}" "$OCTOPUS_CACHE_DIR/$d" 2>/dev/null' \
      '    fi' \
      '  done' \
      '  case ":$PATH:" in' \
      '    *":$OCTOPUS_CACHE_DIR/mise/shims:"*) ;;' \
      '    *) PATH="$OCTOPUS_CACHE_DIR/mise/shims:$PATH"; export PATH ;;' \
      '  esac' \
      'fi' \
      > /etc/profile.d/octopus-cache.sh \
 && mkdir -p /cache/npm /cache/go/mod /cache/go/build /cache/pip /cache/mise /cache/snapshots \
 && chown -R 1000:1000 /cache

COPY --from=build /out/sandbox-execagent /usr/local/bin/sandbox-execagent

RUN mkdir -p /workspace && chown 1000:1000 /workspace
WORKDIR /workspace

EXPOSE 8587

# Numeric uid 1000 so PSA can verify runAsNonRoot; must match the agent pod sharing the worktree
# volume (fsGroup 1000, internal/sandbox/kube.go).
USER 1000:1000

# tini reaps children of killed commands, which the exec agent does not. -g lets commands in its
# group get SIGTERM before SIGKILL.
# No -s: the pod does not set shareProcessNamespace, so tini is PID 1.
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/usr/local/bin/sandbox-execagent"]
