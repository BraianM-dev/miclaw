FROM golang:1.22-alpine AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download || (echo "ERROR: go mod download falló" && exit 1)

COPY . .

# Imprimir estructura para depuración
RUN echo "=== Archivos Go encontrados ===" && find . -name "*.go"

# Intentar compilar con salida detallada
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go build -v -ldflags="-s -w" -o /out/gateway ./... \
    || (echo "=== ERROR DE COMPILACIÓN ===" && exit 1)

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata sqlite-libs
WORKDIR /app
COPY --from=builder /out/gateway /app/gateway
COPY configs/ /app/configs/
RUN mkdir -p /app/data /app/plugins /app/logs && chmod 755 /app/gateway
ENV PORT=3000 ...
EXPOSE 3000
ENTRYPOINT ["/app/gateway"]
