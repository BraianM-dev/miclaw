# ════════════════════════════════════════════════════════════════
#  miclaw2 — stack completo v2
#
#  Las imágenes gateway y frontend se construyen automáticamente
#  en GitHub Actions y se publican en GHCR.
#
#  SETUP INICIAL:
#  1. Hacer push a GitHub → Actions construye y publica las imágenes
#  2. En Portainer → Environments → Variables (o .env en el servidor):
#       GHCR_USER=tu-usuario-github
#       MICLAW_AGENT_KEY=una-clave-segura
#  3. Deploy del stack → Portainer descarga las imágenes de GHCR
#
#  VARIABLES REQUERIDAS:
#    GHCR_USER          — usuario de GitHub (dueño del repo)
#    MICLAW_AGENT_KEY   — clave compartida entre agentes y gateway
#
#  VARIABLES OPCIONALES:
#    GATEWAY_PORT    (default: 3001)
#    DASHBOARD_PORT  (default: 8080)
#    WEBUI_PORT      (default: 3000)
#    OLLAMA_MODEL    (default: phi4-mini:3.8b)
#    TAG             (default: latest)
#    TZ              (default: America/Argentina/Buenos_Aires)
# ════════════════════════════════════════════════════════════════

services:

  # ── Ollama (LLM local) ────────────────────────────────────────
  ollama:
    image: ollama/ollama:latest
    container_name: ollama
    restart: unless-stopped
    ports:
      - "11434:11434"
    volumes:
      - ollama_data:/root/.ollama
    # GPU: descomentar si el host tiene GPU NVIDIA
    # deploy:
    #   resources:
    #     reservations:
    #       devices:
    #         - driver: nvidia
    #           count: 1
    #           capabilities: [gpu]
    environment:
      - OLLAMA_KEEP_ALIVE=24h
      - OLLAMA_NUM_PARALLEL=2
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:11434/api/tags"]
      interval: 30s
      timeout: 10s
      retries: 5
      start_period: 30s
    networks:
      - miclaw-net

  # ── Pull del modelo al iniciar (job único) ────────────────────
  ollama-pull:
    image: ollama/ollama:latest
    container_name: ollama-pull
    restart: "no"
    depends_on:
      ollama:
        condition: service_healthy
    entrypoint: >
      sh -c "ollama pull ${OLLAMA_MODEL:-phi4-mini:3.8b}"
    environment:
      - OLLAMA_HOST=http://ollama:11434
    networks:
      - miclaw-net

  # ── Gateway (imagen pre-construida en GHCR) ───────────────────
  miclaw-gateway:
    image: ghcr.io/${GHCR_USER}/miclaw-gateway:${TAG:-latest}
    container_name: miclaw-gateway
    restart: unless-stopped
    depends_on:
      ollama:
        condition: service_healthy
    ports:
      - "${GATEWAY_PORT:-3001}:3000"
    volumes:
      - gateway_data:/app/data
      - gateway_logs:/app/logs
    environment:
      - PORT=3000
      - DB_PATH=/app/data/miclaw.db
      - QUEUE_DB=/app/data/queue.db
      - OLLAMA_URL=http://ollama:11434
      - OLLAMA_MODEL=${OLLAMA_MODEL:-phi4-mini:3.8b}
      - MICLAW_AGENT_KEY=${MICLAW_AGENT_KEY:-changeme}
      - TZ=${TZ:-America/Argentina/Buenos_Aires}
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:3000/health"]
      interval: 20s
      timeout: 5s
      retries: 3
      start_period: 10s
    networks:
      - miclaw-net

  # ── Dashboard Frontend (imagen pre-construida en GHCR) ────────
  miclaw-frontend:
    image: ghcr.io/${GHCR_USER}/miclaw-frontend:${TAG:-latest}
    container_name: miclaw-frontend
    restart: unless-stopped
    depends_on:
      miclaw-gateway:
        condition: service_healthy
    ports:
      - "${DASHBOARD_PORT:-8080}:80"
    networks:
      - miclaw-net

  # ── Open-WebUI (interfaz Ollama — opcional) ───────────────────
  open-webui:
    image: ghcr.io/open-webui/open-webui:main
    container_name: open-webui
    restart: unless-stopped
    depends_on:
      ollama:
        condition: service_healthy
    ports:
      - "${WEBUI_PORT:-3000}:8080"
    volumes:
      - openwebui_data:/app/backend/data
    environment:
      - OLLAMA_BASE_URL=http://ollama:11434
      - WEBUI_AUTH=False
    networks:
      - miclaw-net

# ════════════════════════════════════════════════════════════════
#  Volumes
# ════════════════════════════════════════════════════════════════
volumes:
  ollama_data:
    name: miclaw_ollama_data
  gateway_data:
    name: miclaw_gateway_data
  gateway_logs:
    name: miclaw_gateway_logs
  openwebui_data:
    name: miclaw_openwebui_data

# ════════════════════════════════════════════════════════════════
#  Red interna
# ════════════════════════════════════════════════════════════════
networks:
  miclaw-net:
    name: miclaw-net
    driver: bridge
