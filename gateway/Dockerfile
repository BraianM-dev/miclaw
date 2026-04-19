# ─── Stage 1: build ───────────────────────────────────────────────────────
# modernc.org/sqlite es pure-Go: CGO_ENABLED=0, sin gcc, sin musl-dev.
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Cache de dependencias (layer-friendly).
COPY go.mod go.sum ./
RUN go mod download

# Código fuente.
COPY . .

# Build estático puro — CGO=0 permite binario 100% estático sin libc externa.
RUN mkdir -p /out && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" \
    -o /out/gateway ./...

# ─── Stage 2: runtime mínimo ──────────────────────────────────────────────
# scratch sería posible (binario estático puro), pero usamos alpine para:
# - wget (necesario por el HEALTHCHECK)
# - ca-certificates (HTTPS a Ollama)
# - tzdata (timezone)
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget

WORKDIR /app

COPY --from=builder /out/gateway /app/gateway
COPY configs/              /app/configs/

RUN mkdir -p /app/data /app/plugins /app/logs && \
    chmod 755 /app/gateway

# ─── Variables de entorno ──────────────────────────────────────────────────
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
