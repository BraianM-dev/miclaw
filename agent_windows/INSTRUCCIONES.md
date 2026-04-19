# Frank v2.1-beta — Instrucciones de Compilación e Inicio

## Requisitos previos

| Herramienta | Versión mínima | Descarga |
|---|---|---|
| Go | 1.21+ | https://go.dev/dl/ |
| MinGW-w64 64-bit | cualquiera | MSYS2 o TDM-GCC |
| MinGW-w64 32-bit | cualquiera | solo si necesitas Frank32.exe |
| Ollama (opcional) | cualquiera | https://ollama.com |

### Verificar requisitos

```cmd
go version
gcc --version
```

### Instalar MinGW-w64 con MSYS2 (recomendado)

```bash
# 64-bit (necesario para Frank64.exe)
pacman -S mingw-w64-x86_64-gcc

# 32-bit (necesario SOLO para Frank32.exe)
pacman -S mingw-w64-i686-gcc
```

Agregar a PATH de Windows:
- 64-bit: `C:\msys64\mingw64\bin`
- 32-bit: `C:\msys64\mingw32\bin` (solo al compilar Frank32)

---

## Compilar

### Opción A — Script automatizado (recomendado)

```cmd
cd agent_windows

build.bat          :: compila Frank64.exe y Frank32.exe
build.bat 64       :: solo Frank64.exe (más rápido)
build.bat 32       :: solo Frank32.exe
build.bat clean    :: elimina binarios anteriores
```

### Opción B — Comandos manuales

**Frank64.exe** (64-bit, OpenGL, equipos modernos):
```cmd
set GOARCH=amd64
set GOOS=windows
set CGO_ENABLED=1
go build -ldflags="-H=windowsgui -s -w" -o Frank64.exe .
```

**Frank32.exe** (32-bit, renderer software, equipos legacy):
```cmd
:: Cambiar PATH al compilador 32-bit primero
set PATH=C:\msys64\mingw32\bin;%PATH%
set GOARCH=386
set GOOS=windows
set CGO_ENABLED=1
go build -tags softwarerender -ldflags="-H=windowsgui -s -w" -o Frank32.exe .
```

### Por qué `-tags softwarerender` en 32-bit

`go-gl/gl` (dependencia de Fyne/GLFW) no tiene fuentes para `GOARCH=386`
en Windows. Sin el flag, el compilador excluye todos sus archivos y arroja:

```
github.com/go-gl/gl/v2.1/gl: build constraints exclude all Go files
```

Con `-tags softwarerender`, Fyne usa su propio renderer en Go puro
que no depende de OpenGL ni GLFW, eliminando el error completamente.

---

## Configuración del entorno (archivo .env o variables del sistema)

```env
# Ollama — servidor de inferencia IA
OLLAMA_URL=http://192.168.1.246:11434/api/generate
OLLAMA_MODEL=phi4-mini:3.8b

# Gateway central (opcional, Frank funciona sin él)
MICLAW_GATEWAY=http://192.168.1.246:3001
AGENT_API_KEY=tu-clave-api-aqui

# Seguridad — clave para cifrar datos locales (chat, settings, KB)
# Si no se define, se genera una aleatoria cada inicio (pierde datos cifrados)
AGENT_SECRET=clave-secreta-larga-y-aleatoria

# Red del agente
HTTP_LISTEN_ADDR=:8081

# Debug — "1" para ver logs detallados de comandos PowerShell
FRANK_DEBUG=0
```

**Importante:** si `AGENT_SECRET` cambia entre ejecuciones, los archivos
`.enc` (chat_history.enc, knowledge.enc, settings.enc) no podrán
descifrarse. Define esta variable de forma persistente en el sistema.

---

## Iniciar Frank

### Inicio manual

```cmd
cd agent_windows
Frank64.exe        :: 64-bit (equipos modernos)
Frank32.exe        :: 32-bit (equipos legacy)
```

Frank aparece en la bandeja del sistema. Si es la primera vez, muestra
el wizard de registro de perfil de usuario.

### Inicio automático con Windows

Dentro de Frank: **icono de bandeja → Ajustes → Iniciar con Windows → activar**

O manualmente via registro:
```cmd
reg add "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" ^
    /v "AFE Assistant" /t REG_SZ ^
    /d "C:\ruta\completa\Frank64.exe" /f
```

### Inicio como servicio (avanzado, requiere NSSM)

```cmd
nssm install Frank "C:\ruta\Frank64.exe"
nssm set Frank AppDirectory "C:\ruta\"
nssm start Frank
```

---

## Iniciar Ollama (IA local)

### Instalar y correr Ollama

```cmd
:: 1. Instalar desde https://ollama.com
:: 2. Descargar el modelo phi4-mini
ollama pull phi4-mini:3.8b

:: 3. Iniciar el servidor (queda escuchando en :11434)
ollama serve
```

### Verificar que Ollama está activo

```cmd
curl http://localhost:11434/api/tags
```

Si Ollama está en otro equipo (servidor dedicado), configurar:
```env
OLLAMA_URL=http://192.168.1.x:11434/api/generate
```

Frank detecta automáticamente si Ollama está disponible al iniciar.
Si no lo está, continúa funcionando con lógica local (actionMap + KB).

---

## Estructura de archivos en ejecución

```
agent_windows/
├── Frank64.exe          ← binario 64-bit
├── Frank32.exe          ← binario 32-bit
├── main.go              ← lógica principal, NLU, actionMap, UI, P2P, Ollama, KB
├── refactor.go          ← OfflineQueue (SQLite), GatewaySync, dynamicRulesMu
├── doc.go               ← documentación de arquitectura (godoc)
├── build.bat            ← script de compilación
├── go.mod / go.sum      ← dependencias
├── logs/
│   └── agent.log        ← log estructurado JSON
├── settings.enc         ← configuración cifrada (AES-256-GCM)
├── chat_history.enc     ← historial de conversaciones cifrado
├── knowledge.enc        ← base de conocimiento cifrada
├── user_profile.enc     ← perfil del usuario cifrado
├── agent_queue.db       ← cola SQLite de tickets/telemetría
├── actions_log.enc      ← auditoría de acciones ejecutadas
└── plugins/             ← plugins JSON opcionales
    └── ejemplo.json
```

---

## Verificar que el sistema distribuido funciona

Con Frank corriendo, desde otro equipo en la misma LAN:

```cmd
:: Verificar que el agente responde al ping
curl http://192.168.1.x:8081/ping

:: Consultar la base de conocimiento
curl -H "X-API-Key: TU_API_KEY" http://192.168.1.x:8081/knowledge

:: Buscar en la KB remota
curl -H "X-API-Key: TU_API_KEY" "http://192.168.1.x:8081/query?q=como+reiniciar+spooler"
```

Respuesta esperada de `/ping`:
```json
{
  "agent": "Frank@EQUIPO",
  "version": "2.1-beta",
  "ip": "192.168.1.x",
  "uptime": "2h30m0s"
}
```

---

## Troubleshooting rápido

| Problema | Causa probable | Solución |
|---|---|---|
| `build constraints exclude all Go files` | Compilando en 32-bit sin flag | Usar `-tags softwarerender` para Frank32 |
| `cgo: C compiler not found` | gcc no está en PATH | Agregar MinGW a PATH |
| `missing go.sum entry for go-sqlite3` | go.sum desactualizado | `go get github.com/mattn/go-sqlite3` |
| Frank no responde preguntas abiertas | Ollama no disponible | Verificar `ollama serve` y `OLLAMA_URL` |
| Los `.enc` no se descifran | `AGENT_SECRET` cambió | Restaurar la clave original o borrar `.enc` |
| Puerto 47890 en uso (P2P) | Otro servicio usa ese puerto | P2P se deshabilita automáticamente, Frank sigue funcionando |
| Puerto 8081 en uso | Otro agente o servicio | Cambiar `HTTP_LISTEN_ADDR=:8082` en el entorno |
