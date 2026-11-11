FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:fe4f6d74e20370acb04ad889077d14d0f64a9c8ea6a42bc60cddf1e844699914 AS web-builder
ARG NPM_CONFIG_FETCH_RETRIES=5
ARG NPM_CONFIG_FETCH_RETRY_MAXTIMEOUT=120000
WORKDIR /build/web
# verify-native-deps.mjs runs as postinstall, so it must be copied before npm ci.
COPY web/package*.json ./
COPY web/scripts/verify-native-deps.mjs ./scripts/
RUN npm ci --prefer-offline
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS go-builder
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /build
COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do go mod download && break; \
      [ "$attempt" = 3 ] && exit 1; echo "go mod download failed, retry $attempt/2" >&2; sleep $((attempt * 15)); \
    done
COPY . .
COPY --from=web-builder /build/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X github.com/MisterDonnuts/thulu/internal/buildinfo.Version=${VERSION}" \
      -o octopus .

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
ARG VERSION=dev
LABEL org.opencontainers.image.title="octopus" \
      org.opencontainers.image.description="Thulu daemon: a board where agents work each stage of your delivery process" \
      org.opencontainers.image.source="https://github.com/MisterDonnuts/thulu" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="AGPL-3.0-only WITH AdditionRef-Thulu-Enterprise-Modules-Exception-1.0 AND LicenseRef-Thulu-Enterprise"
# git-daemon provides git-http-backend for the /git transport (internal/gitremote); alpine's git
# package lacks it.
ARG TINI_VERSION=0.19.0
RUN for attempt in 1 2 3; do apk add --no-cache ca-certificates curl tzdata git git-daemon "tini~${TINI_VERSION}" && break; \
      [ "$attempt" = 3 ] && exit 1; echo "apk add failed, retry $attempt/2" >&2; sleep $((attempt * 15)); \
    done
# uid/gid 10001 must match the Helm chart's runAsUser/fsGroup; runAsNonRoot needs a numeric USER.
# The binary stays root-owned so the daemon cannot rewrite its own executable.
WORKDIR /app
RUN addgroup -S -g 10001 octopus && adduser -S -D -H -u 10001 -G octopus -h /app octopus \
  && mkdir -p /data \
  && chown 10001:10001 /app /data
COPY --from=go-builder /build/octopus .
# The licenses travel with the binary (AGPL §4-6, and the notices the third-party licenses ask for).
COPY LICENSE LICENSE-EXCEPTION NOTICE THIRD_PARTY_NOTICES /usr/share/doc/thulu/
COPY ee/LICENSE /usr/share/doc/thulu/ee/LICENSE
ENV OCTOPUS_DATA_DIR=/data
# Do not add a system safe.directory override: the daemon runs git with an explicit GIT_DIR and
# GIT_CONFIG_NOSYSTEM=1.
USER 10001:10001
EXPOSE 8585
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD curl -sf http://localhost:8585/health/live || exit 1
# tini reaps git helpers orphaned when a git call is killed; Go never reaps them as PID 1.
# No -g: the daemon handles SIGTERM itself, and group signalling would kill an in-flight git push.
ENTRYPOINT ["/sbin/tini", "--", "./octopus", "serve", "--data", "/data", "--agents-dir", "/workspace"]
