# ─── Stage 1: build ───────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Instalar dependencias necesarias para CGO + SQLite estático
RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /src

# Cache de dependencias Go (aprovecha las capas)
COPY go.mod go.sum* ./
RUN go mod download

# Copiar todo el código fuente
COPY . .

# Construir binario completamente estático con soporte para SQLite
# NOTA: -linkmode external y -extldflags '-static' son obligatorios en Alpine
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build \
    -ldflags="-s -w -linkmode external -extldflags '-static'" \
    -tags "sqlite_omit_load_extension" \
    -o /out/gateway ./...

# ─── Stage 2: runtime mínimo ───────────────────────────────────────────────
FROM alpine:3.20

# Instalar certificados y zona horaria (para TLS y logs)
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copiar binario desde la etapa de construcción
COPY --from=builder /out/gateway /app/gateway

# Copiar configuración base (será sobrescrita por volumen en desarrollo)
COPY configs/ /app/configs/

# Crear directorios para datos, plugins y logs
RUN mkdir -p /app/data /app/plugins /app/logs && \
    chmod 755 /app/gateway

# Variables de entorno por defecto (pueden sobrescribirse en compose)
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

# Healthcheck para orquestadores
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://localhost:${PORT}/health || exit 1

ENTRYPOINT ["/app/gateway"]