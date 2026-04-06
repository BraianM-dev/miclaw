FROM golang:1.21 AS builder

WORKDIR /src
COPY main.go .
# Inicializa el módulo y descarga dependencias de SQLite y Telegram
RUN go mod init miclaw-gateway && \
    go get github.com/mattn/go-sqlite3 && \
    go get github.com/go-telegram-bot-api/telegram-bot-api/v5 && \
    go mod tidy

# Compila con CGO habilitado para SQLite
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o gateway main.go

# Imagen final super ligera
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates sqlite3 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /src/gateway /usr/local/bin/gateway
WORKDIR /app
VOLUME /app
EXPOSE 3000
CMD ["gateway"]