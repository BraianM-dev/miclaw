# ─── Stage 1: build ───────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# CGO necesario para go-sqlite3.
# sqlite-dev provee las librerías estáticas de SQLite en Alpine.
RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /src

# Copiar go.mod y go.sum antes del código (cache de capas de Docker).
COPY go.mod go.sum ./
RUN go mod download

# Copiar todo el código fuente.
COPY . .

# Crear directorio de salida y compilar.
# Usamos dynamic linking contra musl (que existe en alpine:3.20).
# -extldflags=-static se elimina porque falla con CGO cuando faltan
# las versiones estáticas de libc/libm en el builder.
RUN mkdir -p /out && \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" \
    -o /out/gateway ./...

# ─── Stage 2: runtime mínimo ──────────────────────────────────────────────
# Usamos alpine (no scratch) porque el binario linkea dinámicamente musl.
FROM alpine:3.20

# ca-certificates: para llamadas HTTPS a Ollama / externos.
# tzdata: para manejo de timezone correcto.
# sqlite-libs: librería compartida de SQLite requerida en runtime.
RUN apk add --no-cache ca-certificates tzdata sqlite-libs

WORKDIR /app

COPY --from=builder /out/gateway /app/gateway
COPY configs/              /app/configs/

RUN mkdir -p /app/data /app/plugins /app/logs && \
    chmod 755 /app/gateway

# ─── Variables de entorno (override con -e o docker-compose) ──────────────
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
