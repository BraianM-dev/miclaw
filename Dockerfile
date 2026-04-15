FROM golang:1.22-alpine AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /src

# Copiar archivos de módulo primero
COPY go.mod go.sum* ./

# Verificar que go.mod y go.sum sean válidos
RUN go mod verify && go mod download

# Copiar todo el código fuente
COPY . .

# ─── Diagnóstico previo a la compilación ─────────────────────────────
RUN echo "=== Estructura del proyecto ===" && \
    find . -type f -name "*.go" | head -20 && \
    echo "=== Módulos y dependencias ===" && \
    go list -m all && \
    echo "=== Paquetes a compilar ===" && \
    go list ./... && \
    echo "=== Ejecutando go vet ===" && \
    go vet ./... || echo "go vet encontró problemas (no bloquea build)"

# Intentar compilación con salida detallada
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -v -ldflags="-s -w" -o /out/gateway ./...

# ─── Stage 2: runtime ────────────────────────────────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata sqlite-libs
WORKDIR /app
COPY --from=builder /out/gateway /app/gateway
COPY configs/ /app/configs/
RUN mkdir -p /app/data /app/plugins /app/logs && chmod 755 /app/gateway
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
