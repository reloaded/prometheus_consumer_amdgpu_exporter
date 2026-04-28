# syntax=docker/dockerfile:1.7

# Multi-stage build: tiny runtime image (scratch + ca-certs + tzdata).
# Note: the optional amd-smi backend shells out to a binary that is
# *not* in this image — when enabled, mount it from the host or use a
# sibling sidecar container.

ARG GO_VERSION=1.23

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags "-s -w \
        -X main.version=${VERSION} \
        -X main.commit=${COMMIT} \
        -X main.date=${DATE}" \
      -o /out/prometheus_consumer_amdgpu_exporter \
      ./cmd/prometheus_consumer_amdgpu_exporter

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/prometheus_consumer_amdgpu_exporter /usr/local/bin/prometheus_consumer_amdgpu_exporter

USER 65534:65534
EXPOSE 9504

ENTRYPOINT ["/usr/local/bin/prometheus_consumer_amdgpu_exporter"]
