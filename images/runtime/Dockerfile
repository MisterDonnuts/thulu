FROM node:22-trixie-slim@sha256:c5849ff9c9ebcd66615412f0b548ca5b8ecaef84003dc9ac2e077ebe46aaa3f6

RUN echo 'Acquire::Retries "5";' > /etc/apt/apt.conf.d/80-retries

ARG NPM_CONFIG_FETCH_RETRIES=5
ARG NPM_CONFIG_FETCH_RETRY_MAXTIMEOUT=120000

# No USER on purpose: `apt-get install` via exec needs root; the daemon drops to 1000:1000 in
# rootless mode (internal/sandbox/docker.go).
# npm is upgraded: Node 22's bundled npm vendors a `tar` with a CRITICAL advisory (fixed in 7.5.19).
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
    zstd \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g "npm@${NPM_VERSION}" \
 && npm cache clean --force \
 && rm -rf /root/.npm /tmp/*

# Same mise install and cache hook as images/sandbox; keep the two blocks in sync.
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

RUN mkdir -p /workspace
WORKDIR /workspace

# The daemon sets `sleep infinity` as the command and never an entrypoint
# (internal/sandbox/docker.go), so tini stays PID 1 and reaps.
# No -s: internal/dockerx/init.go adds no Docker init for images starting with tini.
ENTRYPOINT ["/usr/bin/tini", "-g", "--"]
CMD ["sleep", "infinity"]
