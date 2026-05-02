# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25
ARG SERVICE=api

FROM golang:${GO_VERSION}-alpine AS deps

WORKDIR /build

RUN apk add --no-cache ca-certificates tzdata git

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

FROM deps AS builder

ARG SERVICE
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

WORKDIR /build

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
      -o /app/service \
      ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:debug AS scanner-support

COPY --from=builder /app/service /service

FROM gcr.io/distroless/static-debian12:nonroot AS runtime

WORKDIR /app

COPY --from=builder /app/service /service
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY deployments/migrations /app/deployments/migrations

ARG SERVICE=api
ARG VERSION=dev
ARG COMMIT=unknown

LABEL org.opencontainers.image.title="urlshortener-${SERVICE}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.vendor="URL Shortener Platform"

EXPOSE 8080 8081 9090 9091

USER nonroot:nonroot

ENTRYPOINT ["/service"]
