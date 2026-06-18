# Minimal Dockerfile for rke2-patcher
FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X github.com/rancher/rke2-patcher/internal/version.Version=${VERSION}" -o /rke2-patcher .

FROM registry.suse.com/bci/bci-busybox:16.0
COPY --from=builder /rke2-patcher /usr/local/bin/rke2-patcher
ENTRYPOINT ["sleep", "infinity"]
