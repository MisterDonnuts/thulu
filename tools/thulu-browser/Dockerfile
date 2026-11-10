# Only Chromium's headless shell is installed because lib/browser.js launches headless only; headed
# or other browsers need this changed.
FROM node:22-trixie-slim@sha256:c5849ff9c9ebcd66615412f0b548ca5b8ecaef84003dc9ac2e077ebe46aaa3f6

RUN echo 'Acquire::Retries "5";' > /etc/apt/apt.conf.d/80-retries

ARG NPM_CONFIG_FETCH_RETRIES=5
ARG NPM_CONFIG_FETCH_RETRY_MAXTIMEOUT=120000

WORKDIR /app

ARG TINI_VERSION=0.19.0
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg "tini=${TINI_VERSION}-*" \
 && rm -rf /var/lib/apt/lists/*

ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts \
 && node node_modules/playwright/cli.js install --with-deps --only-shell chromium \
 && rm -rf /var/lib/apt/lists/* \
 && npm cache clean --force \
 && rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
           /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
           /root/.npm /root/.cache /tmp/*

COPY server.js i18n.js ./
COPY locales ./locales
COPY lib ./lib
COPY evidence ./evidence

# Numeric uid so runAsNonRoot can verify it; must match runAsUser 1000 in
# mcp.podSecurityContext of deploy/helm/octopus/values.yaml.
USER 1000:1000

EXPOSE 3000

# tini as PID 1 reaps Chromium's reparented child processes; node as PID 1 does not.
# No -g: server.js handles SIGTERM to close browsers and let ffmpeg finish. No -s: tini is always
# PID 1 here.
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["node", "server.js"]
