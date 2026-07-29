# syntax=docker/dockerfile:1

# The console bundle is a build artifact and is not committed, so the image
# builds it from source. Without this stage the Go build still succeeds (the
# webdist placeholder keeps the embed resolvable) but the image would ship with
# no console and silently serve the JSON descriptor at "/".
FROM node:20-alpine AS web
WORKDIR /web
RUN corepack enable
# Cache dependency resolution separately from sources.
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
# vite writes to ../internal/httpapi/webdist; create the target so the relative
# outDir resolves inside this stage.
RUN mkdir -p /internal/httpapi/webdist && pnpm build

FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache module resolution.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overlay the freshly built console over the committed placeholder.
COPY --from=web /internal/httpapi/webdist/ ./internal/httpapi/webdist/
ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/rpsg/oneops/pkg/version.Version=${VERSION} -X github.com/rpsg/oneops/pkg/version.Commit=${COMMIT}" \
    -o /out/controlplane ./cmd/controlplane

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/controlplane /controlplane
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/controlplane"]
