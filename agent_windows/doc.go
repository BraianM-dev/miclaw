//go:build windows
// +build windows

/*
Package main implementa Frank v2.1-beta — asistente IT autónomo para Windows.

═══════════════════════════════════════════════════════════════════════════════
 SISTEMA DISTRIBUIDO — CÓMO FUNCIONA
═══════════════════════════════════════════════════════════════════════════════

Frank puede operar de forma completamente autónoma (sin red) o en modo
distribuido cuando hay otros agentes Frank en la misma LAN.

───────────────────────────────────────────────────────────────────────────────
 1. DESCUBRIMIENTO AUTOMÁTICO (UDP BROADCAST)
───────────────────────────────────────────────────────────────────────────────

Al iniciar, Frank envía y escucha broadcasts UDP en el puerto 47890.
Cada 60 segundos emite un mensaje de anuncio:

    { "type": "announce", "agent_ip": "192.168.1.x",
      "port": 8081, "name": "Frank@EQUIPO", "version": "2.1-beta" }

Cualquier otro agente Frank en la misma subred que escuche ese puerto
registra al emisor como peer conocido (AgentPeer). Los peers se expiran
automáticamente si no responden en 5 minutos.

Sin configuración manual. Si el puerto está ocupado, el modo P2P se
deshabilita silenciosamente y Frank funciona en modo standalone.

───────────────────────────────────────────────────────────────────────────────
 2. BASE DE CONOCIMIENTO COMPARTIDA (KB)
───────────────────────────────────────────────────────────────────────────────

Cada agente mantiene una base de conocimiento local (KnowledgeBase):

    - Almacena pares { Question, Answer, Timestamp, Source, Hash }
    - Hash SHA-256 de la pregunta normalizada → evita duplicados exactos
    - TTL de 7 días: los ítems expirados se purgan cada 6 horas
    - Persistencia cifrada en knowledge.enc (AES-256-GCM)
    - Capacidad máxima: 500 ítems (FIFO al superar el límite)

Cuando Ollama responde a una consulta abierta, la respuesta se guarda
automáticamente en KB con Source = nombre del equipo.

ENDPOINTS HTTP del agente (puerto 8081 por defecto):

    GET  /ping         → identidad del agente (sin auth)
    GET  /knowledge    → exportar todos los ítems de KB (requiere X-API-Key)
    POST /knowledge    → importar un ítem de otro agente
    GET  /query?q=...  → buscar en KB local (devuelve la respuesta más similar)

───────────────────────────────────────────────────────────────────────────────
 3. CONSULTA ENTRE PEERS
───────────────────────────────────────────────────────────────────────────────

Cuando el usuario hace una pregunta abierta (no cubierta por actionMap):

    [1] KB local     → búsqueda Jaccard instantánea, sin red
    [2] Peers LAN    → GET http://{peer_ip}:{port}/query?q=...
                       timeout 2 s por peer, en paralelo
    [3] Ollama       → solo si [1] y [2] no encontraron respuesta

Si un peer responde, su respuesta se guarda en la KB local para consultas
futuras sin red.

───────────────────────────────────────────────────────────────────────────────
 4. COLA OFFLINE (OfflineQueue)
───────────────────────────────────────────────────────────────────────────────

Tickets y telemetría que no pudieron entregarse al gateway se encolan en
SQLite (agent_queue.db). Un worker en background reintenta cada 10 s con
back-off exponencial (15s → 30s → 60s… hasta 30 min), máximo 5 intentos.
Si el job falla definitivamente, se marca como "failed" sin perder datos.

───────────────────────────────────────────────────────────────────────────────
 5. SINCRONIZACIÓN DE REGLAS (GatewaySync)
───────────────────────────────────────────────────────────────────────────────

GatewaySync descarga rules.json e intents.json desde el gateway central
cada 15 minutos. Si el gateway no está disponible, el agente usa la última
versión local. Cuando rules.json se actualiza, las reglas se recarga en
memoria sin reiniciar (hot-reload), protegido por dynamicRulesMu.

═══════════════════════════════════════════════════════════════════════════════
 CUÁNDO USA OLLAMA vs LÓGICA LOCAL — FLUJO COMPLETO
═══════════════════════════════════════════════════════════════════════════════

Por cada mensaje del usuario, Frank aplica este pipeline en orden estricto.
La primera capa que produce una respuesta detiene el pipeline.

    ┌─────────────────────────────────────────────────────────────────┐
    │  MENSAJE DE USUARIO                                             │
    └────────────────────────────┬────────────────────────────────────┘
                                 │
    ┌────────────────────────────▼────────────────────────────────────┐
    │  CAPA 0a: Confirmación pendiente                                │
    │  ¿Hay una acción esperando confirmación (sí/no)?                │
    │  → SÍ: ejecutar/cancelar acción y devolver resultado            │
    └────────────────────────────┬────────────────────────────────────┘
                                 │ no
    ┌────────────────────────────▼────────────────────────────────────┐
    │  CAPA 0b: Plugins JSON (/plugins/*.json)                        │
    │  ¿Alguna keyword del plugin coincide con el input?              │
    │  → SÍ: ejecutar command/response del plugin                     │
    └────────────────────────────┬────────────────────────────────────┘
                                 │ no
    ┌────────────────────────────▼────────────────────────────────────┐
    │  CAPA 0c: Reglas dinámicas (rules.json, hot-reload)             │
    │  ¿Alguna keyword+contexto de DynamicRule coincide?              │
    │  → SÍ: ejecutar action del actionMap o devolver response fijo   │
    └────────────────────────────┬────────────────────────────────────┘
                                 │ no
    ┌────────────────────────────▼────────────────────────────────────┐
    │  CAPA 1: Keyword Matching directo (120+ patrones)               │
    │  Comparación exacta de substrings normalizados.                 │
    │  Confidence fija = 0.95                                         │
    │  → HIT: ir a actionMap (NUNCA usa Ollama aquí)                  │
    └────────────────────────────┬────────────────────────────────────┘
                                 │ no hit
    ┌────────────────────────────▼────────────────────────────────────┐
    │  CAPA 2: Naive Bayes + ClosestMatch                             │
    │  NB predice probabilidades sobre intents entrenados.            │
    │  ClosestMatch busca la frase más similar en el corpus.          │
    │  Score combinado = NB×0.6 + Jaccard×0.4                        │
    │  → score > 0.25: ir a actionMap (NUNCA usa Ollama aquí)         │
    └────────────────────────────┬────────────────────────────────────┘
                                 │ score ≤ 0.25 (intent no reconocido)
    ┌────────────────────────────▼────────────────────────────────────┐
    │  CAPA 3: askOllama() — IA distribuida                           │
    │                                                                 │
    │  Solo se activa si:                                             │
    │    • El input tiene ≥ 3 palabras                               │
    │    • El input tiene ≥ 10 caracteres                             │
    │    • No contiene patrones de inyección de prompt               │
    │    • No hay handler en actionMap para el intent                 │
    │                                                                 │
    │  Pipeline interno de askOllama:                                 │
    │                                                                 │
    │  3a. KB LOCAL → búsqueda Jaccard en knowledge.enc              │
    │      → respuesta instantánea si similitud > 0.35               │
    │                                                                 │
    │  3b. PEERS LAN → GET /query a cada agente conocido             │
    │      → timeout 2s, usa primera respuesta útil                  │
    │      → guarda respuesta en KB local para futuras consultas     │
    │                                                                 │
    │  3c. OLLAMA REMOTO → POST http://{OLLAMA_URL}/api/generate     │
    │      Modelo: phi4-mini:3.8b (configurable via OLLAMA_MODEL)    │
    │      Lanza 3 llamadas concurrentes con temperaturas 0.4/0.7/0.9│
    │      Elige la respuesta con mayor consenso Jaccard entre ellas  │
    │      Timeout total: 45 segundos                                 │
    │      Rate limit: 1 req cada 3 segundos                         │
    │      Cache: 30 minutos (SHA-256 del prompt)                    │
    │      Respuesta guardada en KB local automáticamente            │
    │      Respuesta filtrada: patrones peligrosos bloqueados        │
    │      Máximo 600 caracteres en respuesta al usuario             │
    │                                                                 │
    │  → Si todo falla: capa 4                                       │
    └────────────────────────────┬────────────────────────────────────┘
                                 │ sin respuesta
    ┌────────────────────────────▼────────────────────────────────────┐
    │  CAPA 4: defaultResponse()                                      │
    │  Sugiere intents similares por Jaccard.                         │
    │  "No entendí. Escribe 'ayuda'..."                               │
    └─────────────────────────────────────────────────────────────────┘

RESUMEN: Ollama se usa SOLO para preguntas abiertas que no tienen
handler técnico definido. Comandos IT concretos (usuarios, disco, red,
impresoras, etc.) SIEMPRE usan el actionMap local — sin latencia de red,
sin dependencia de Ollama.

VARIABLES DE ENTORNO RELEVANTES:
    OLLAMA_URL      URL del servidor Ollama (default: http://192.168.1.246:11434/api/generate)
    OLLAMA_MODEL    Modelo a usar     (default: phi4-mini:3.8b)
    MICLAW_GATEWAY  URL del gateway   (default: http://192.168.1.246:3001)
    AGENT_API_KEY   API key del agente (generada aleatoriamente si no se define)
    AGENT_SECRET    Clave AES para cifrado de datos locales
    HTTP_LISTEN_ADDR Puerto HTTP del agente (default: :8081)
    FRANK_DEBUG     "1" para logs detallados de comandos PowerShell
    QUEUE_DB        Ruta a la base SQLite de la cola offline (default: agent_queue.db)
*/
package main
