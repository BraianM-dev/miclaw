# ─── Stage 1: build ───────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Instalar herramientas de compilación y cabeceras SQLite
RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /src

# Copiar archivos de dependencias primero (para mejor caching)
COPY go.mod go.sum* ./
RUN go mod download

# Copiar el resto del código fuente
COPY . .

# Compilar el binario (enlace dinámico contra musl)
# NOTA: Se omite "-static" para evitar errores de enlazado con SQLite
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /out/gateway ./...

# ─── Stage 2: runtime mínimo ───────────────────────────────────────────────
FROM alpine:3.20

# Instalar dependencias de ejecución:
# - ca-certificates: para conexiones TLS/HTTPS
# - tzdata: para manejo de zonas horarias
# - sqlite-libs: biblioteca dinámica de SQLite requerida por el binario
RUN apk add --no-cache ca-certificates tzdata sqlite-libs

WORKDIR /app

# Copiar el binario desde la etapa de construcción
COPY --from=builder /out/gateway /app/gateway

# Copiar la configuración base (puede ser sobrescrita por volumen en docker-compose)
COPY configs/ /app/configs/

# Crear directorios necesarios para datos, plugins y logs
RUN mkdir -p /app/data /app/plugins /app/logs && \
    chmod 755 /app/gateway

# Variables de entorno por defecto
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

# Healthcheck para el orquestador
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://localhost:${PORT}/health || exit 1

ENTRYPOINT ["/app/gateway"]
