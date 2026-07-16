# Multi-stage build → a static, ~single-binary image.
#
# demiplane uses a pure-Go SQLite driver, so CGO is disabled and the binary is
# fully static; it runs on a distroless "static" base with no libc, no shell,
# and a non-root user. Pin the base images by digest in production builds.

# Base images pinned by digest (immutable). Tags kept in comments for humans /
# dependabot; bump both together.
FROM golang:1.26-bookworm@sha256:5f68ec6805843bd3981a951ffada82a26a0bd2631045c8f7dba483fa868f5ec5 AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/demiplane ./cmd/demiplane

# Stage an empty data dir owned by the nonroot uid (65532). A named volume
# inherits this directory's ownership when first created, so the nonroot process
# can write the store out of the box.
RUN mkdir -p /data

FROM gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240
COPY --from=build /out/demiplane /usr/local/bin/demiplane
COPY --from=build --chown=65532:65532 /data /var/lib/demiplane

# Content + metadata live here; mount a volume for persistence.
VOLUME ["/var/lib/demiplane"]
# 8080 = control plane (/publish, /list, DELETE); 8081 = isolated artifact
# content origin (GET /{slug}). Both must be mapped to serve + fetch (ADR 0003).
EXPOSE 8080 8081

# Bind all interfaces inside the container; restrict exposure at the publish/
# network layer (the container's port is only reachable where you map it).
ENTRYPOINT ["demiplane"]
CMD ["serve", "--bind", "0.0.0.0:8080", "--store", "/var/lib/demiplane"]
