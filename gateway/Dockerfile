FROM golang:1.21 AS builder

WORKDIR /src
COPY go.mod main.go eliza_rules.json ./
RUN go mod download
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o gateway main.go

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates sqlite3 && rm -rf /var/lib/apt/lists/*
COPY --from=builder /src/gateway /usr/local/bin/gateway
COPY --from=builder /src/eliza_rules.json /app/eliza_rules.json
WORKDIR /app
VOLUME /app
EXPOSE 3000
CMD ["gateway"]
