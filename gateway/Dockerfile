# Etapa 1: build
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Instalar git (necesario para go mod)
RUN apk add --no-cache git

# Copiar archivos de dependencias primero (mejora cache)
COPY go.mod go.sum ./

# Descargar dependencias
RUN go mod download

# Copiar el resto del código
COPY . .

# Compilar binario
RUN go build -ldflags="-s -w" -o gateway

# Etapa 2: runtime (liviana)
FROM alpine:latest

WORKDIR /app

# Copiar binario desde builder
COPY --from=builder /app/gateway .

# Puerto (ajustalo si usás otro)
EXPOSE 8080

# Ejecutar
CMD ["./gateway"]
