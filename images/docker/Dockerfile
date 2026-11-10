# Slim base has no python3 or build toolchain; runtime snippets must not depend on them.
FROM node:22-trixie-slim@sha256:c5849ff9c9ebcd66615412f0b548ca5b8ecaef84003dc9ac2e077ebe46aaa3f6

RUN echo 'Acquire::Retries "5";' > /etc/apt/apt.conf.d/80-retries

ARG TINI_VERSION=0.19.0
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl git ca-certificates gnupg gosu "tini=${TINI_VERSION}-*" \
    && rm -rf /var/lib/apt/lists/*

ARG DOCKER_REPO_FINGERPRINT=9DC858229FC7DD38854AE2D88D81803C0EBFCD88
ARG DOCKER_CLI_VERSION=29.8.1
ARG DOCKER_COMPOSE_VERSION=5.5.1
RUN set -eux; \
    install -m 0755 -d /etc/apt/keyrings; \
    curl -fsSL --retry 5 --retry-connrefused https://download.docker.com/linux/debian/gpg -o /tmp/docker.asc; \
    fprs="$(gpg --batch --quiet --no-default-keyring --show-keys --with-colons /tmp/docker.asc \
            | awk -F: '/^pub/{want=1; next} /^fpr/ && want {print $10; want=0}')"; \
    if [ "$fprs" != "$DOCKER_REPO_FINGERPRINT" ]; then \
      echo "Docker apt key fingerprint mismatch: got '$fprs', want $DOCKER_REPO_FINGERPRINT" >&2; exit 1; \
    fi; \
    gpg --batch --dearmor -o /etc/apt/keyrings/docker.gpg /tmp/docker.asc; \
    chmod a+r /etc/apt/keyrings/docker.gpg; \
    rm -f /tmp/docker.asc; \
    . /etc/os-release; \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian ${VERSION_CODENAME} stable" \
      > /etc/apt/sources.list.d/docker.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      "docker-ce-cli=5:${DOCKER_CLI_VERSION}-1~debian.${VERSION_ID}~${VERSION_CODENAME}" \
      "docker-compose-plugin=${DOCKER_COMPOSE_VERSION}-1~debian.${VERSION_ID}~${VERSION_CODENAME}"; \
    rm -rf /var/lib/apt/lists/*

ARG NPM_CONFIG_FETCH_RETRIES=5
ARG NPM_CONFIG_FETCH_RETRY_MAXTIMEOUT=120000

# Node 22's bundled npm vendors a `tar` with a CRITICAL advisory (fixed in 7.5.19), and npm stays on
# PATH for agents.
ARG NPM_VERSION=11.20.0
RUN npm install -g "npm@${NPM_VERSION}" \
    && npm cache clean --force \
    && rm -rf /root/.npm /tmp/*

# Pinned: internal/engine/opencode/parse.go reads this version's events
# (internal/engine/testdata/opencode).
# Only the platform package is installed; the opencode-ai launcher pulls two ~185 MB binaries. x64
# build needs AVX2.
RUN mkdir -p /workspace
WORKDIR /workspace
