FROM --platform=$BUILDPLATFORM golang:1.25.13-alpine3.23@sha256:42fc3368d1c50170a452f2bf4a1dfd292a065870c3f258d799aad4316671cb69 AS builder
ARG VERSION=docker
ARG GIT_COMMIT=unknown
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN mkdir ./bin
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-X github.com/validaoxyz/hyperliquid-exporter/internal/metrics.BuildVersion=${VERSION} -X github.com/validaoxyz/hyperliquid-exporter/internal/metrics.BuildCommit=${GIT_COMMIT}" \
    -o ./bin/hl_exporter ./cmd/hl-exporter

FROM ubuntu:26.04@sha256:678c6550cc43645e08669028bc177f50be4e7c5b8cca677067b1914d4afc7a03
WORKDIR /app
COPY --from=builder /app/bin/hl_exporter /bin/hl_exporter

ENV NODE_HOME="/hl/"
ENV BINARY_HOME="/bin"

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

EXPOSE 8086
ENTRYPOINT ["/bin/hl_exporter"]
