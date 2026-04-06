FROM golang:1.21 AS builder

WORKDIR /src

# Copia todo el contenido (incluyendo main.go y posibles archivos)
COPY . .

# Si no existe go.mod, lo inicializamos
RUN if [ ! -f go.mod ]; then \
        go mod init miclaw-gateway && \
        go get github.com/mattn/go-sqlite3 && \
        go get github.com/go-telegram-bot-api/telegram-bot-api/v5 && \
        go mod tidy; \
    else \
        go mod download; \
    fi

# Compilar con CGO (para SQLite)
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o gateway main.go

# Imagen final
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates sqlite3 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /src/gateway /usr/local/bin/gateway
COPY --from=builder /src/eliza_rules.json /app/eliza_rules.json
WORKDIR /app
VOLUME /app
EXPOSE 3000
CMD ["gateway"]
