# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache module resolution.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
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
