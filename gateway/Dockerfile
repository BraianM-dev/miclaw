FROM golang:1.21 AS builder

WORKDIR /src

# Copiar todos los archivos necesarios
COPY go.mod go.sum* ./
COPY main.go .
COPY eliza_rules.json .

# Descargar dependencias
RUN go mod download || go mod init miclaw-gateway && go get github.com/mattn/go-sqlite3 && go mod tidy

# Compilar
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
