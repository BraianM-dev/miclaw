# Update Packages — Frank v2.x

Los **update packages** son archivos JSON con extensión `.pkg` que permiten
actualizar frases de bandeja, configuraciones y en el futuro reglas NLU,
**sin recompilar ni modificar el código fuente**.

---

## Cómo funciona

Al iniciar, Frank busca automáticamente `update.pkg` en su directorio de trabajo.
Si lo encuentra:

1. Aplica los cambios en memoria.
2. Guarda la configuración actualizada en `settings.enc`.
3. **Elimina** el archivo `update.pkg` (evita reaplicar en cada inicio).
4. Muestra una notificación de bandeja: _"Actualización aplicada."_

Para reaplicar, simplemente vuelve a copiar el archivo `.pkg` como `update.pkg`.

---

## Formato del archivo

```json
{
  "version": "2.2",
  "phrases": {
    "<categoria>": [
      { "text": "Texto de la frase", "weight": 1.0 },
      { "text": "Frase alternativa",  "weight": 0.8 }
    ]
  },
  "settings_patch": {
    "<campo>": <valor>
  }
}
```

### Campo `version`

Identificador libre del paquete. Solo informativo (se loguea, no se valida).

---

### Campo `phrases`

Actualiza las frases que Frank muestra en las notificaciones de bandeja.

| Categoría    | Cuándo aparece                              |
|--------------|---------------------------------------------|
| `morning`    | Frases de buenos días (6:00–11:59)          |
| `afternoon`  | Frases de buenas tardes (12:00–18:59)       |
| `evening`    | Frases de buenas noches (19:00–5:59)        |
| `idle`       | Frank lleva mucho tiempo sin actividad       |

Cada ítem tiene:
- `text`: el texto de la frase (sin emojis si `UseEmojis` está desactivado).
- `weight`: peso relativo para la selección aleatoria ponderada (`0.0–1.0`).

**Las categorías no definidas en el paquete no se modifican.**

---

### Campo `settings_patch`

Aplica cambios parciales a la configuración del agente.

| Clave                    | Tipo    | Descripción                                              |
|--------------------------|---------|----------------------------------------------------------|
| `UseEmojis`              | bool    | Activa/desactiva emojis en respuestas                    |
| `Personality`            | string  | `profesional`, `tecnico`, `amigable`, `conciso`          |
| `Theme`                  | string  | `dark` / `light`                                         |
| `AccentColor`            | string  | Color hexadecimal, ej. `"#2196F3"`                       |
| `NotificationsEnabled`   | bool    | Activa/desactiva notificaciones de bandeja               |
| `NotificationFrequency`  | string  | `low`, `medium`, `high`                                  |
| `EmotionalSupportEnabled`| bool    | Activa/desactiva modo de soporte emocional               |
| `StartWithWindows`       | bool    | Inicia Frank con Windows (modifica el registro)          |

Los campos omitidos **no se tocan**.

---

## Flujo de despliegue recomendado

```
servidor/admin
    └─ crea update_v2.2.pkg
    └─ copia a \\equipo\c$\ruta\Frank\update.pkg
         (o lo distribuye via script, GPO, robocopy, etc.)

equipo destino
    └─ Frank detecta update.pkg al iniciar (o en próximo reinicio)
    └─ aplica cambios → elimina update.pkg
    └─ muestra notificación al usuario
```

Para múltiples equipos simultáneos se puede usar un script:

```powershell
# deploy_update.ps1
$source  = "\\servidor\updates\update.pkg"
$targets = @("PC-001", "PC-002", "PC-003")

foreach ($pc in $targets) {
    $dest = "\\$pc\c$\Program Files\Frank\update.pkg"
    Copy-Item $source $dest -Force
    Write-Host "Enviado a $pc"
}
```

---

## Ejemplo completo

Ver `update_example.pkg` en este directorio. Para aplicarlo:

```cmd
:: Desde el directorio de Frank
copy update_example.pkg update.pkg
:: Al próximo inicio de Frank, o reinicia Frank para aplicarlo ahora
```

O sin reiniciar (si Frank ya está corriendo), simplemente copia el archivo —
Frank no vigila el filesystem en tiempo real. El paquete se aplicará en el
**próximo inicio**.

> **Truco:** Si necesitas aplicar el paquete sin reiniciar el equipo,
> reinicia solo el proceso Frank desde la bandeja:
> _icono → clic derecho → Salir_ y luego inicia `Frank64.exe` de nuevo.

---

## Reglas dinámicas (rules.json) — alternativa para intents

Para modificar el comportamiento de reconocimiento de intents sin recompilar,
usa `rules.json` sincronizado desde el gateway (`MICLAW_GATEWAY`).

Frank recarga `rules.json` cada 15 minutos automáticamente (hot-reload).

Formato de `rules.json`:
```json
[
  {
    "keywords": ["reiniciar spooler", "impresora atascada"],
    "context":  "",
    "action":   "reiniciar_spooler",
    "response": ""
  },
  {
    "keywords": ["modo zen"],
    "context":  "",
    "action":   "",
    "response": "Activando modo zen. Silenciando notificaciones por 1 hora."
  }
]
```

Si `action` apunta a una función existente en el `actionMap`, Frank la ejecuta.
Si `response` está definido y `action` vacío, devuelve el texto directamente.

---

## Plugins JSON — extensión de acciones sin código

Los plugins en `plugins/*.json` permiten agregar keywords y respuestas fijas
o comandos de sistema sin tocar el código Go.

```json
{
  "name": "Plugin Empresa",
  "version": "1.0",
  "keywords": ["soporte nivel 2", "escalate"],
  "command":  "powershell -Command \"Start-Process 'https://helpdesk.empresa.com'\"",
  "response": "Abriendo portal de soporte nivel 2..."
}
```

Frank carga todos los `.json` en `plugins/` al iniciar.
Agregar un nuevo plugin = copiar el archivo + reiniciar Frank.
