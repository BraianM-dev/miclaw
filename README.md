# Miclaw IT Command Center & ELIZA Agent 🚀

Miclaw es un ecosistema de soporte IT centralizado y autónomo. Transforma la gestión de infraestructura combinando inteligencia artificial local (Ollama) con ejecución de comandos seguros, telemetría silenciosa y auditoría completa.

## ✨ Características Principales

- **Orquestación de Sub-Agentes:** La IA no ejecuta a ciegas. Un Planificador divide los problemas complejos en pasos lógicos, un Ejecutor los corre y un Sintetizador genera el reporte final.
- **Tribunal de Seguridad (Inferencia ML):** Cada comando es evaluado antes de salir. Se clasifica en `bypass` (seguro), `strict` (bloqueado) o `default` (requiere aprobación humana).
- **Rollback y Línea de Tiempo:** Todas las acciones de soporte se registran en una interfaz cristalina (Tailwind CSS) con un botón de "Deshacer" para revertir cambios críticos.
- **Agente ELIZA Autónomo (Fyne):** Un cliente de Windows que vive en la bandeja del sistema, captura telemetría silenciosa (CPU/RAM/Disco) mediante PowerShell y tiene contingencia SMTP si el servidor principal cae.

## 🏗️ Estructura del Proyecto

```text
miclaw-project/
├── .env                  # Variables globales (Tokens, Claves API)
├── docker-compose.yml    # Orquestador de contenedores
├── README.md
├── gateway/              # Servidor Central (Go)
│   ├── main.go           # Motor de IA, Base de Datos y Dashboard UI
│   ├── Dockerfile        # Build multicapa
│   └── eliza_rules.json  # Reglas de chat actualizables
└── agent_windows/        # Cliente Desktop (Go + Fyne)
    ├── eliza_agent_gui.go
    ├── go.mod
    └── go.sum