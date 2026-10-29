# syntax=docker/dockerfile:1

# Static ohgo binary: CGO is disabled end to end, so the runtime image needs no
# libc, shell, or package manager.

ARG GO_VERSION=1.27
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# Dependencies change far less often than source, so this layer survives most
# rebuilds instead of re-downloading the module graph.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# Stamp the release version so `ohgo version` and the self-update check compare
# the running image against the release it was built from. The default keeps a
# local build honest instead of claiming a release it is not.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ohgo ./cmd/ohgo

# Copied below so /work exists in the runtime image owned by the non-root user.
RUN mkdir -p /out/work

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="ohgo" \
      org.opencontainers.image.description="A coding agent with the IDE wired in" \
      org.opencontainers.image.source="https://github.com/PizenLabs/ohgo" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

# Distroless ships the CA bundle the provider clients need, but it does not set
# HOME. Without this, os.UserHomeDir fails and ohgo cannot create ~/.ohgo.
ENV HOME=/home/nonroot

COPY --from=build /out/ohgo /ohgo
COPY LICENSE /licenses/LICENSE

# ohgo creates <root>/.ohgo on startup, so the working directory must be
# writable by the non-root user. COPY --chown creates it with that owner; a
# bind mount over it still follows the host's permissions.
COPY --from=build --chown=nonroot:nonroot /out/work /work
WORKDIR /work
USER nonroot:nonroot

ENTRYPOINT ["/ohgo"]
