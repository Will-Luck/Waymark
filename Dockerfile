# syntax=docker/dockerfile:1

# The shipping Go. The workflows pin the same version ('1.27' in
# .github/workflows and .gitea/workflows): move them together.
ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3

# The build runs on the build machine's own platform and cross-compiles for
# the target (TARGETOS/TARGETARCH, set by BuildKit), so a multi-arch build
# never runs the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

# Stdlib plus yaml.v3 only, no cgo: a static binary for a bare Alpine stage.
ENV CGO_ENABLED=0

WORKDIR /src

# Dependency layer on its own so source edits do not re-download modules.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/waymark ./cmd/waymark

FROM alpine:${ALPINE_VERSION}

ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/Will-Luck/waymark" \
      org.opencontainers.image.title="Waymark" \
      org.opencontainers.image.description="A live status page for every project on your Gitea, Forgejo and GitHub accounts" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# ca-certificates for HTTPS to the forges, tzdata so site.timezone names
# resolve for the OS too (the binary also embeds time/tzdata).
RUN apk add --no-cache ca-certificates tzdata

# /data is created and chowned BEFORE the VOLUME declaration: Docker seeds a
# fresh named volume from the image path including its ownership, so skipping
# this leaves the volume root-owned and uid 1000 cannot write state.json.
# /config is where waymark.yml is read from: bind-mount a directory there
# read-only (no VOLUME, the image never writes to it).
RUN addgroup -g 1000 waymark \
 && adduser -D -H -u 1000 -G waymark waymark \
 && mkdir -p /data /config \
 && chown 1000:1000 /data

COPY --from=builder /out/waymark /usr/local/bin/waymark

VOLUME /data
EXPOSE 8080
USER 1000

# No ENV defaults here on purpose: internal/config owns every default, and a
# baked ENV would silently outrank it. Tokens come from docker run -e. The
# config file is /config/waymark.yml when it exists (WAYMARK_CONFIG names
# another path); without one the env-only quick start runs. The web layer
# embeds its own assets, so there is nothing else to copy.
#
# No HEALTHCHECK: add one in your own runtime if you want it. GET /healthz
# answers 200 once the server is up, with each source's state.

ENTRYPOINT ["/usr/local/bin/waymark"]
