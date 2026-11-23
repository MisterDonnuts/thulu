FROM node:22-trixie-slim@sha256:68c76cfad89921b927eae28ba21407475e29d0ce450afcdbe70c1532db042b64

ARG NPM_CONFIG_FETCH_RETRIES=5
ARG NPM_CONFIG_FETCH_RETRY_MAXTIMEOUT=120000

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts \
 && npm cache clean --force \
 && rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
           /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
           /root/.npm /tmp/*

COPY server.js i18n.js ./
COPY locales ./locales

# Numeric uid so runAsNonRoot can verify it; must match runAsUser 1000 in
# mcp.podSecurityContext of deploy/helm/octopus/values.yaml.
USER 1000:1000

EXPOSE 3000

CMD ["node", "server.js"]
