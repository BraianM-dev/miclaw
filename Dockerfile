# ─── Stage 1: build ───────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# CGO required for go-sqlite3
RUN apk add --no-cache gcc musl-dev

WORKDIR /src

# Cache dependencies first (layer-cache friendly)
COPY go.mod go.sum* ./
RUN go mod download

# Copy all source files (modular layout)
COPY . .

# Build fully-static binary
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -extldflags=-static" \
    -o /out/gateway ./...

# ─── Stage 2: minimal runtime ─────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/gateway /app/gateway
COPY configs/              /app/configs/

RUN mkdir -p /app/data /app/plugins /app/logs && \
    chmod 755 /app/gateway

# ─── Runtime configuration (override with -e or docker-compose env) ───────
ENV PORT=3000 \
    DB_PATH=/app/data/miclaw.db \
    QUEUE_DB=/app/data/queue.db \
    MANIFEST_PATH=/app/configs/manifest.json \
    DATA_DIR=/app/configs \
    PLUGINS_DIR=/app/plugins \
    OLLAMA_URL=http://ollama:11434 \
    OLLAMA_MODEL=phi4-mini:3.8b \
    MICLAW_AGENT_KEY=changeme \
    TZ=America/Argentina/Buenos_Aires

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://localhost:${PORT}/health || exit 1

ENTRYPOINT ["/app/gateway"]
