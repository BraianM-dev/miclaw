# ─── Stage 1: build ───────────────────────────────────────────────────────
FROM node:20-alpine AS builder

WORKDIR /app

# Cache de dependencias (layer-friendly)
COPY package*.json ./
RUN npm install

# Código fuente
COPY . .

# Variables de build (se inyectan en tiempo de compilación como VITE_*)
# VITE_API_URL y VITE_API_KEY se sobreescriben en docker-compose si es necesario.
# Con nginx.conf /api/ → gateway, VITE_API_URL=/api es el default en producción.
ARG VITE_API_URL=/api
ARG VITE_API_KEY=changeme
ENV VITE_API_URL=$VITE_API_URL
ENV VITE_API_KEY=$VITE_API_KEY

RUN npm run build

# ─── Stage 2: runtime nginx ──────────────────────────────────────────────
FROM nginx:1.27-alpine

# Eliminar configuración default de nginx
RUN rm /etc/nginx/conf.d/default.conf

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -qO- http://localhost/health.txt 2>/dev/null || \
        wget -qO- http://localhost/ -S 2>&1 | grep -q "200 OK" && exit 0 || exit 1
