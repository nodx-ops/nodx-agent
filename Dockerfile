# Worker-pattern Dockerfile for the R26 agent (stdlib-only Go 1.26).
# Build the whole module: COPY . . + `go build ./cmd/...` ships every source
# file — file-level COPY silently drops siblings (the worker's evaluate.go
# lesson). Static binary: -trimpath -ldflags="-s -w" -tags timetzdata.
# Run: the operator MUST inject the five claim values (AGENT_ID,
# AGENT_AUTH_TOKEN, NODX_SERVER, NODX_AGENT_SECRET, DATACENTER_ID) and mount
# a writable dir for NODX_AGENT_STATE + NODX_AGENT_SPOOL_DIR — the defaults
# target /var/lib, which is read-only for UID 65534.
FROM golang:1.26-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c AS builder
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -tags timetzdata -o nodx-agent ./cmd/nodx-agent

FROM alpine:3.21@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507
RUN apk add --no-cache ca-certificates tzdata ipmitool
WORKDIR /app
COPY --from=builder /src/nodx-agent .
# State + spool dirs exist and are owned by the runtime UID so default boot
# works with no mounts; mount volumes over them for persistence across restarts.
RUN mkdir -p /var/lib/nodx-agent/spool && chown -R 65534:65534 /var/lib/nodx-agent
# ipmitool needs no setuid for dcmi/sdr reads; nvidia-smi (when present)
# comes from a host GPU-driver mount, never from this image.
USER 65534:65534
ENTRYPOINT ["./nodx-agent"]
