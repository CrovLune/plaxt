# syntax=docker/dockerfile:1.6

ARG GO_VERSION=1.24.0
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG TARGETVARIANT
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

ENV CGO_ENABLED=0

RUN apk add --no-cache git ca-certificates tzdata && update-ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Install Node.js and build assets with esbuild
RUN apk add --no-cache nodejs npm

# Copy package files and install dependencies
COPY package.json package-lock.json* ./
RUN npm ci

# Build fingerprinted assets + manifest with esbuild
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    npm run build

# Ensure outputs exist (fail the build if not)
RUN test -s static/dist/manifest.json

# Ensure the static dir exists after build
RUN test -d static

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    <<'EOF'
set -eux
mkdir -p /out
if [ "${TARGETARCH}" = "arm" ] && [ -n "${TARGETVARIANT}" ]; then
  export GOARM="${TARGETVARIANT#v}"
fi
export GOOS="${TARGETOS}" GOARCH="${TARGETARCH}"
go build -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" -o /out/goplaxt .
mkdir -p /out/static
cp -r static/. /out/static/
mkdir -p /out/keystore
EOF

FROM scratch

LABEL maintainer="Plaxt contributors" \
      org.opencontainers.image.source="https://github.com/crovlune/plaxt" \
      org.opencontainers.image.title="Plaxt" \
      org.opencontainers.image.description="Plaxt – Plex webhook to Trakt scrobbler"

WORKDIR /app

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /out/goplaxt ./goplaxt
COPY --from=builder /out/static ./static
COPY --from=builder /out/keystore ./keystore

VOLUME ["/app/keystore"]

EXPOSE 8000

ENTRYPOINT ["/app/goplaxt"]
