//go:build windows
// +build windows

// actions_extra.go — Extensiones de Frank v2.1-beta
//
// Contiene:
//   1. formatResponse()  — aplica preferencias de emojis y personalidad
//   2. Nuevas funciones de acción (15+)
//   3. init() que extiende actionMap con los nuevos intents
//   4. Nuevas entradas para keywordMatch (llamadas desde main.go a través
//      de intentExamples y el switch en keywordMatch)
//
// Las keywords para los nuevos intents se registran en el init() de este
// archivo vía intentExamplesExtra, y se inyectan en intentExamples ANTES
// de que initializeNLU() construya el modelo Naive Bayes.
// El orden garantizado: vars de paquete → init() de cada archivo (alfabético)
// → main().  "actions_extra.go" < "main.go" alfabéticamente, pero como
// las keywords se AÑADEN al mapa (no reemplazan), el NLU las incorpora
// en el entrenamiento del modelo.

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ════════════════════════════════════════════════════════════════════════════
//  REGISTRO DE NUEVOS INTENTS Y ACCIONES
// ════════════════════════════════════════════════════════════════════════════

// extraIntentExamples contiene los ejemplos de entrenamiento de los intents
// adicionales definidos en este archivo. Se declara aquí como variable de
// paquete para que initializeNLU() en main.go pueda mergearla después de
// construir su propio mapa, evitando el panic por escritura en mapa nil
// que ocurriría si intentáramos escribir en intentExamples desde init().
var extraIntentExamples = map[string][]string{
	"ram_detalle": {
		"cuanta ram tengo", "detalle de memoria ram", "modulos de ram",
		"velocidad de la ram", "ver slots de memoria",
	},
	"gpu_info": {
		"que gpu tengo", "tarjeta de video", "info de la grafica",
		"controlador de video", "resolucion de pantalla",
	},
	"salud_disco": {
		"salud del disco fisico", "estado del disco duro",
		"disco con problemas fisicos",
	},
	"almacenamiento": {
		"cuanto espacio por unidad", "resumen de almacenamiento",
		"espacio en todas las unidades",
	},
	"uptime_sistema": {
		"cuanto lleva encendido el sistema", "tiempo de arranque",
		"desde cuando esta prendida la pc",
	},
	"archivos_recientes": {
		"que archivos modifique", "archivos recientes", "ultimos archivos editados",
	},
	"latencia_red": {
		"latencia de red", "tiempo de respuesta red", "ping promedio",
		"cuanto tarda la red",
	},
	"escaneo_red": {
		"escanear red local", "que equipos hay en la red", "hosts activos",
		"ver dispositivos en la lan",
	},
	"sesiones_activas": {
		"quien esta conectado", "sesiones activas", "usuarios conectados remotamente",
	},
	"estado_defender": {
		"estado del antivirus", "defender activo", "proteccion en tiempo real",
	},
	"actualizaciones_instaladas": {
		"que actualizaciones tengo instaladas", "hotfix instalados", "parches instalados",
	},
	"convertir": {
		"convertir unidades", "cuanto es en fahrenheit", "pasar gb a mb",
		"convertir celsius", "cuanto es en mb",
	},
	"nota": {
		"guardar nota", "anotar", "tomar nota", "recordar esto",
	},
	"ver_notas": {
		"ver notas", "mis notas", "que anote", "mostrar notas",
	},
	"historial_comandos": {
		"historial de comandos", "que hice", "comandos recientes",
	},
	"modo_concentracion": {
		"modo concentracion", "no interrumpir", "pausar notificaciones", "focus mode",
	},
	"no_molestar": {
		"no molestar", "activar no molestar", "dnd",
	},
	"limpieza_profunda": {
		"limpieza profunda", "limpiar todo", "borrar todo lo innecesario",
	},
	"exportar_config": {
		"exportar configuracion", "backup de ajustes", "guardar configuracion",
	},
}

func init() {
	// Extender actionMap con los nuevos handlers.
	// actionMap es var = map{...} en main.go → ya está inicializado aquí.
	extraActions := map[string]func(string, *Memory) string{
		// Hardware / sistema
		"ram_detalle":             getDetailedRAM,
		"gpu_info":                getGPUInfo,
		"salud_disco":             checkDiskHealth,
		"almacenamiento":          getStorageSpaces,
		"uptime_sistema":          getSystemUptime,
		"archivos_recientes":      listRecentFiles,
		// Red
		"latencia_red":            checkNetworkLatency,
		"escaneo_red":             scanLocalNetwork,
		"sesiones_activas":        listActiveSessions,
		// Seguridad
		"estado_defender":         checkWindowsDefenderStatus,
		"actualizaciones_instaladas": getInstalledUpdates,
		// Productividad
		"convertir":               convertUnits,
		"nota":                    quickNote,
		"ver_notas":               viewNotes,
		"historial_comandos":      getCommandHistory,
		"modo_concentracion":      focusMode,
		"no_molestar":             manageFocusAssist,
		// Mantenimiento
		"limpieza_profunda":       systemCleanupDeep,
		"exportar_config":         exportConfiguration,
	}
	for k, v := range extraActions {
		actionMap[k] = v
	}
	// intentExamples se mergea desde initializeNLU() en main.go usando
	// extraIntentExamples — no se toca aquí para evitar escritura en mapa nil.
}

// ════════════════════════════════════════════════════════════════════════════
//  FORMAT RESPONSE — emojis y personalidad
// ════════════════════════════════════════════════════════════════════════════

// emojiRegex detecta emoji de los rangos Unicode más comunes.
// Cubre emoticons, símbolos misceláneos, dingbats, flechas decorativas y
// los bloques de emoji principales (U+1F000-U+1FFFF).
var emojiRegex = regexp.MustCompile(
	`[\x{1F000}-\x{1FFFF}` + // emoji principales (caras, animales, objetos, banderas)
		`\x{2600}-\x{26FF}` + // símbolos varios (sol, nube, ✅ ⚠️ ❌ etc.)
		`\x{2700}-\x{27BF}` + // dingbats (✂ ✈ etc.)
		`\x{FE00}-\x{FE0F}` + // selectores de variante (evita ?  extra)
		`\x{1F1E0}-\x{1F1FF}` + // banderas (letras indicadores regionales)
		`]`,
)

// formatResponse aplica las preferencias del usuario sobre emojis y
// personalidad a cada respuesta antes de mostrarla en el chat.
//
// Personalidades:
//   - profesional: tono formal, respuesta completa (sin cambios de texto)
//   - tecnico:     sin frases de cortesía, solo el dato técnico
//   - amigable:    agrega saludos informales al inicio de respuestas cortas
//   - conciso:     trunca a la primera oración / 200 chars si es muy larga
func formatResponse(text string) string {
	settingsMutex.RLock()
	useEmojis := settings.UseEmojis
	personality := settings.Personality
	settingsMutex.RUnlock()

	// 1. Manejo de emojis
	if !useEmojis {
		text = emojiRegex.ReplaceAllString(text, "")
		// Limpiar espacios dobles que quedan al eliminar emojis
		spaceRe := regexp.MustCompile(`  +`)
		text = spaceRe.ReplaceAllString(text, " ")
		// Limpiar líneas que quedaron vacías
		lines := strings.Split(text, "\n")
		var clean []string
		for _, l := range lines {
			if trimmed := strings.TrimSpace(l); trimmed != "" {
				clean = append(clean, trimmed)
			} else {
				clean = append(clean, "")
			}
		}
		text = strings.TrimSpace(strings.Join(clean, "\n"))
	}

	// 2. Ajuste de personalidad
	switch personality {
	case "tecnico":
		// Eliminar frases de cortesía comunes al inicio
		courtesy := []string{
			"Hola! ", "Hola, ", "Por supuesto! ", "Claro que sí, ",
			"Con gusto te ayudo. ", "Entendido. Aquí va: ",
		}
		lower := strings.ToLower(text)
		for _, c := range courtesy {
			if strings.HasPrefix(lower, strings.ToLower(c)) {
				text = text[len(c):]
				text = strings.TrimSpace(text)
				// Capitalizar primera letra
				if len(text) > 0 {
					r := []rune(text)
					r[0] = unicode.ToUpper(r[0])
					text = string(r)
				}
				break
			}
		}

	case "amigable":
		// Solo para respuestas de Ollama (no para comandos técnicos largos)
		if len(text) < 300 && !strings.Contains(text, "\n") {
			hour := time.Now().Hour()
			var prefix string
			switch {
			case hour < 12:
				prefix = "Buen dia! "
			case hour < 18:
				prefix = "Buenas tardes! "
			default:
				prefix = "Buenas noches! "
			}
			// Solo agregar si la respuesta no empieza ya con saludo
			lowerText := strings.ToLower(text)
			if !strings.HasPrefix(lowerText, "buen") && !strings.HasPrefix(lowerText, "hola") {
				text = prefix + text
			}
		}

	case "conciso":
		// Truncar a la primera oración si el texto es largo
		if len([]rune(text)) > 220 {
			// Buscar punto, signo de exclamación o salto de línea
			for i, r := range text {
				if (r == '.' || r == '!' || r == '\n') && i > 40 {
					text = strings.TrimSpace(text[:i+1])
					break
				}
			}
		}
	}

	return text
}

// ════════════════════════════════════════════════════════════════════════════
//  NUEVAS FUNCIONES DE ACCIÓN
// ════════════════════════════════════════════════════════════════════════════

// getDetailedRAM muestra información detallada de los módulos de RAM.
func getDetailedRAM(input string, mem *Memory) string {
	total := psRun(`[math]::Round((Get-WmiObject Win32_ComputerSystem).TotalPhysicalMemory/1GB, 1)`)
	free := psRun(`[math]::Round((Get-WmiObject Win32_OperatingSystem).FreePhysicalMemory/1MB, 1)`)
	modules := psRun(`Get-WmiObject Win32_PhysicalMemory | ForEach-Object { "  Slot: $($_.DeviceLocator) | $([math]::Round($_.Capacity/1GB,1)) GB | $($_.Speed) MHz | $($_.Manufacturer)" }`)
	if total == "" {
		return "No se pudo obtener informacion de RAM."
	}
	return fmt.Sprintf("RAM total: %s GB | Libre: %s GB\n\nModulos:\n%s", total, free, modules)
}

// getGPUInfo muestra informacion del adaptador de video.
func getGPUInfo(input string, mem *Memory) string {
	out := psRun(`Get-WmiObject Win32_VideoController | ForEach-Object { "  $($_.Name) | Driver: $($_.DriverVersion) | RAM: $([math]::Round($_.AdapterRAM/1MB))MB | $($_.CurrentHorizontalResolution)x$($_.CurrentVerticalResolution)" }`)
	if out == "" {
		return "No se detectaron adaptadores de video."
	}
	return "Adaptadores de video:\n" + out
}

// checkDiskHealth muestra el estado fisico de los discos.
func checkDiskHealth(input string, mem *Memory) string {
	out := psRun(`Get-PhysicalDisk | ForEach-Object { "  $($_.FriendlyName) | $($_.HealthStatus) | $($_.OperationalStatus) | $([math]::Round($_.Size/1GB))GB" }`)
	if out == "" {
		return "No se pudo consultar el estado fisico de los discos."
	}
	return "Estado fisico de los discos:\n" + out
}

// getStorageSpaces muestra uso de almacenamiento por unidad logica.
func getStorageSpaces(input string, mem *Memory) string {
	out := psRun(`Get-PSDrive -PSProvider FileSystem | Where-Object {$_.Used -ne $null} | ForEach-Object { $total=[math]::Round(($_.Used+$_.Free)/1GB,1); $used=[math]::Round($_.Used/1GB,1); $free=[math]::Round($_.Free/1GB,1); "$($_.Name): | Total: ${total}GB | Usado: ${used}GB | Libre: ${free}GB" }`)
	if out == "" {
		return "No se pudo obtener informacion de almacenamiento."
	}
	return "Almacenamiento por unidad:\n" + out
}

// getSystemUptime muestra cuanto lleva encendido el sistema y Frank.
func getSystemUptime(input string, mem *Memory) string {
	hoursStr := psRun(`[math]::Round(((Get-Date)-(gcim Win32_OperatingSystem).LastBootUpTime).TotalHours, 1)`)
	hours, _ := strconv.ParseFloat(strings.TrimSpace(hoursStr), 64)
	days := int(hours / 24)
	hrs := int(math.Mod(hours, 24))
	frank := time.Since(agentStartTime).Round(time.Minute)
	return fmt.Sprintf("Sistema encendido hace: %d dias %d horas\nFrank activo hace: %s\nComandos procesados: %d | Errores: %d",
		days, hrs, frank, telemetry.CommandsExecuted, telemetry.ErrorsCount)
}

// listRecentFiles muestra los ultimos archivos modificados por el usuario.
func listRecentFiles(input string, mem *Memory) string {
	out := psRun(`Get-ChildItem -Path "$env:USERPROFILE\Documents","$env:USERPROFILE\Desktop" -Recurse -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 10 | ForEach-Object { "$($_.LastWriteTime.ToString('dd/MM HH:mm')) $($_.Name)" }`)
	if out == "" {
		return "No se encontraron archivos modificados recientemente."
	}
	return "Ultimos archivos modificados:\n" + out
}

// checkNetworkLatency hace ping a hosts clave y muestra la latencia promedio.
func checkNetworkLatency(input string, mem *Memory) string {
	// Si mencionan una IP específica, usarla
	hosts := []string{"8.8.8.8", "1.1.1.1"}
	if ip := reMatch1(`(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})`, input); ip != "" {
		hosts = []string{ip}
	}
	var sb strings.Builder
	sb.WriteString("Latencia de red:\n")
	for _, host := range hosts {
		avg := psRun(fmt.Sprintf(
			`$r=Test-Connection -ComputerName %s -Count 3 -ErrorAction SilentlyContinue; if($r){[math]::Round(($r|Measure-Object ResponseTime -Average).Average)}else{"sin respuesta"}`,
			host,
		))
		sb.WriteString(fmt.Sprintf("  %-15s : %s ms\n", host, strings.TrimSpace(avg)))
	}
	return sb.String()
}

// scanLocalNetwork hace un ping sweep de la subred local.
func scanLocalNetwork(input string, mem *Memory) string {
	go func() {
		appendMessage("Escaneando red local (puede tardar hasta 30 segundos)...")
		myIP := getOutboundIP()
		parts := strings.Split(myIP, ".")
		if len(parts) != 4 {
			appendMessage("No se pudo determinar la subred.")
			return
		}
		subnet := strings.Join(parts[:3], ".") + "."
		out := psRun(fmt.Sprintf(
			`$subnet="%s"; 1..254 | ForEach-Object -ThrottleLimit 50 -Parallel { if(Test-Connection "$using:subnet$_" -Count 1 -Quiet -TimeoutSeconds 1){"  $using:subnet$_ activo"} }`,
			subnet,
		))
		if out == "" {
			appendMessage("No se encontraron hosts activos (o el firewall bloquea ICMP).")
			return
		}
		appendMessage("Hosts activos en la LAN:\n" + out)
	}()
	return "Iniciando escaneo de red..."
}

// listActiveSessions muestra las sesiones de usuario activas en el equipo.
func listActiveSessions(input string, mem *Memory) string {
	out := psRun(`query session 2>&1`)
	if out == "" {
		out = psRun(`Get-WmiObject Win32_LogonSession | Where-Object {$_.LogonType -in 2,10,11} | ForEach-Object { "  ID: $($_.LogonId) | Tipo: $($_.LogonType) | Inicio: $((Get-Date $_.StartTime).ToString('dd/MM HH:mm'))" }`)
	}
	if out == "" {
		return "No se encontraron sesiones activas o sin permisos suficientes."
	}
	return "Sesiones activas:\n" + out
}

// checkWindowsDefenderStatus muestra el estado de Windows Defender.
func checkWindowsDefenderStatus(input string, mem *Memory) string {
	cmd := "$s=Get-MpComputerStatus -ErrorAction SilentlyContinue; if($s){" +
		"\"  Servicio:       $($s.AMServiceEnabled)`n" +
		"  Antivirus:      $($s.AntivirusEnabled)`n" +
		"  Antispyware:    $($s.AntispywareEnabled)`n" +
		"  Tiempo real:    $($s.RealTimeProtectionEnabled)`n" +
		"  Ultima firma:   $($s.AntivirusSignatureLastUpdated.ToString('dd/MM/yyyy'))\"}"
	out := psRun(cmd)
	if out == "" {
		return "No se pudo consultar Windows Defender (requiere permisos o PS 5.1+)."
	}
	return "Estado de Windows Defender:\n" + out
}

// getInstalledUpdates muestra las ultimas actualizaciones instaladas.
func getInstalledUpdates(input string, mem *Memory) string {
	out := psRun(`Get-HotFix | Sort-Object InstalledOn -Descending -ErrorAction SilentlyContinue | Select-Object -First 10 | ForEach-Object { "  $($_.InstalledOn.ToString('dd/MM/yyyy'))  $($_.HotFixID)  $($_.Description)" }`)
	if out == "" {
		return "No se pudieron obtener las actualizaciones instaladas."
	}
	return "Ultimas 10 actualizaciones instaladas:\n" + out
}

// convertUnits convierte unidades de temperatura, almacenamiento y velocidad.
func convertUnits(input string, mem *Memory) string {
	lower := strings.ToLower(removeAccents(input))
	num := regexp.MustCompile(`[\d]+(?:[.,]\d+)?`)

	// Temperatura C → F
	if strings.Contains(lower, "celsius") || (strings.Contains(lower, " c ") && strings.Contains(lower, " f")) ||
		strings.Contains(lower, "grados") && strings.Contains(lower, "fahrenheit") {
		if m := num.FindString(lower); m != "" {
			c, _ := strconv.ParseFloat(strings.Replace(m, ",", ".", 1), 64)
			return fmt.Sprintf("%.1f C = %.1f F", c, c*9/5+32)
		}
	}
	// Temperatura F → C
	if strings.Contains(lower, "fahrenheit") && (strings.Contains(lower, "celsius") || strings.Contains(lower, " a c")) {
		if m := num.FindString(lower); m != "" {
			f, _ := strconv.ParseFloat(strings.Replace(m, ",", ".", 1), 64)
			return fmt.Sprintf("%.1f F = %.1f C", f, (f-32)*5/9)
		}
	}
	// Almacenamiento con conversión explícita "X GB a MB"
	storageRe := regexp.MustCompile(`([\d.,]+)\s*(tb|gb|mb|kb)\s+a\s+(tb|gb|mb|kb)`)
	if mm := storageRe.FindStringSubmatch(lower); len(mm) == 4 {
		val, _ := strconv.ParseFloat(strings.Replace(mm[1], ",", ".", 1), 64)
		units := map[string]float64{"kb": 1 << 10, "mb": 1 << 20, "gb": 1 << 30, "tb": 1 << 40}
		bytes := val * units[mm[2]]
		result := bytes / units[mm[3]]
		return fmt.Sprintf("%.2f %s = %.2f %s", val, strings.ToUpper(mm[2]), result, strings.ToUpper(mm[3]))
	}
	// Velocidad Mbps → MB/s
	if strings.Contains(lower, "mbps") && (strings.Contains(lower, "mb/s") || strings.Contains(lower, "megabyte")) {
		if m := num.FindString(lower); m != "" {
			mbps, _ := strconv.ParseFloat(strings.Replace(m, ",", ".", 1), 64)
			return fmt.Sprintf("%.0f Mbps = %.2f MB/s (megabytes por segundo)", mbps, mbps/8)
		}
	}

	return "Conversiones disponibles:\n" +
		"  Temperatura: '37 Celsius a Fahrenheit' / '98.6 Fahrenheit a Celsius'\n" +
		"  Almacenamiento: '100 GB a MB' / '1.5 TB a GB'\n" +
		"  Velocidad: '100 Mbps a MB/s'"
}

// quickNote guarda una nota rapida en el archivo local notes.txt.
func quickNote(input string, mem *Memory) string {
	noteRe := regexp.MustCompile(`(?i)(?:nota|anotar|guardar nota|tomar nota|recordar)[:\s,]+(.+)`)
	m := noteRe.FindStringSubmatch(input)
	if len(m) < 2 || strings.TrimSpace(m[1]) == "" {
		return "Que quieres anotar? Ej: 'nota: revisar servidor a las 15hs'"
	}
	note := strings.TrimSpace(m[1])
	entry := fmt.Sprintf("[%s] %s: %s\n", time.Now().Format("02/01/2006 15:04"), currentUser, note)
	f, err := os.OpenFile("notes.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "No se pudo guardar la nota: " + err.Error()
	}
	defer f.Close()
	f.WriteString(entry)
	return "Nota guardada: " + note
}

// viewNotes muestra las ultimas notas guardadas.
func viewNotes(input string, mem *Memory) string {
	data, err := os.ReadFile("notes.txt")
	if err != nil || len(data) == 0 {
		return "No hay notas guardadas. Usa 'nota: [texto]' para guardar una."
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return "Ultimas notas:\n" + strings.Join(lines, "\n")
}

// getCommandHistory muestra el historial reciente de acciones de Frank.
func getCommandHistory(input string, mem *Memory) string {
	actionLogMux.RLock()
	defer actionLogMux.RUnlock()
	if len(actionLog) == 0 {
		return "No hay historial de comandos aun."
	}
	var sb strings.Builder
	sb.WriteString("Ultimos 15 comandos ejecutados:\n")
	start := 0
	if len(actionLog) > 15 {
		start = len(actionLog) - 15
	}
	for _, entry := range actionLog[start:] {
		result := entry.Result
		if len(result) > 40 {
			result = result[:40] + "..."
		}
		sb.WriteString(fmt.Sprintf("  %s  %-25s  %s\n",
			entry.Timestamp.Format("15:04"), entry.Action, result))
	}
	return sb.String()
}

// focusMode pausa las notificaciones proactivas por 2 horas.
func focusMode(input string, mem *Memory) string {
	proactiveTickerMutex.Lock()
	if proactiveTicker != nil {
		proactiveTicker.Stop()
		proactiveTicker = nil
	}
	if proactiveTickerStop != nil {
		close(proactiveTickerStop)
		proactiveTickerStop = nil
	}
	proactiveTickerMutex.Unlock()

	go func() {
		time.Sleep(2 * time.Hour)
		restartProactiveTicker()
		appendMessage("Modo concentracion finalizado. Notificaciones reactivadas.")
	}()
	return "Modo concentracion activado por 2 horas. Las notificaciones proactivas estan pausadas.\nEscribe 'activar notificaciones' para reactivarlas antes."
}

// manageFocusAssist activa o desactiva el modo No Molestar de Windows.
func manageFocusAssist(input string, mem *Memory) string {
	lower := strings.ToLower(input)
	if kwContains(lower, "activar", "encender", "habilitar", "on") {
		// Activar Focus Assist via PowerShell (requiere Windows 10+)
		psRun(`Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Notifications\Settings' -Name 'NOC_GLOBAL_SETTING_TOASTS_ENABLED' -Value 0 -Force -ErrorAction SilentlyContinue`)
		return "No molestar activado. Las notificaciones de Windows estan silenciadas."
	}
	psRun(`Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Notifications\Settings' -Name 'NOC_GLOBAL_SETTING_TOASTS_ENABLED' -Value 1 -Force -ErrorAction SilentlyContinue`)
	return "No molestar desactivado. Las notificaciones de Windows estan activas."
}

// systemCleanupDeep realiza una limpieza profunda del sistema.
func systemCleanupDeep(input string, mem *Memory) string {
	go func() {
		appendMessage("Limpieza profunda iniciada...")
		steps := []struct {
			desc string
			cmd  string
		}{
			{"Temporales del usuario", `Remove-Item -Path "$env:TEMP\*" -Recurse -Force -ErrorAction SilentlyContinue`},
			{"Temporales del sistema", `Remove-Item -Path "C:\Windows\Temp\*" -Recurse -Force -ErrorAction SilentlyContinue`},
			{"Cache DNS", `Clear-DnsClientCache`},
			{"Papelera de reciclaje", `Clear-RecycleBin -Force -ErrorAction SilentlyContinue`},
			{"Cache de miniaturas", `Remove-Item -Path "$env:LOCALAPPDATA\Microsoft\Windows\Explorer\thumbcache_*.db" -Force -ErrorAction SilentlyContinue`},
			{"Logs antiguos de Windows Update", `Remove-Item -Path "C:\Windows\SoftwareDistribution\Download\*" -Recurse -Force -ErrorAction SilentlyContinue`},
		}
		if isAdmin() {
			steps = append(steps, struct {
				desc string
				cmd  string
			}{"Prefetch (admin)", `Remove-Item -Path "C:\Windows\Prefetch\*" -Force -ErrorAction SilentlyContinue`})
			steps = append(steps, struct {
				desc string
				cmd  string
			}{"Minidumps", `Remove-Item -Path "C:\Windows\Minidump\*" -Force -ErrorAction SilentlyContinue`})
		}
		cleaned := []string{}
		for _, s := range steps {
			psRun(s.cmd)
			cleaned = append(cleaned, "  "+s.desc)
		}
		appendMessage("Limpieza profunda completada.\nLimpiado:\n" + strings.Join(cleaned, "\n"))
	}()
	return "Limpieza profunda en progreso..."
}

// exportConfiguration exporta la configuracion actual de Frank a un JSON.
func exportConfiguration(input string, mem *Memory) string {
	settingsMutex.RLock()
	s := settings
	settingsMutex.RUnlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "Error al exportar configuracion: " + err.Error()
	}
	filename := fmt.Sprintf("frank_config_%s.json", time.Now().Format("20060102_150405"))
	if err := os.WriteFile(filename, data, 0644); err != nil {
		return "No se pudo guardar el archivo: " + err.Error()
	}
	return "Configuracion exportada a: " + filename
}

// ════════════════════════════════════════════════════════════════════════════
//  KEYWORDS ADICIONALES para keywordMatch (llamadas desde el switch principal)
// ════════════════════════════════════════════════════════════════════════════

// extraKeywordMatch evalúa los nuevos intents.
// Se llama al FINAL del switch en keywordMatch() via la cláusula default
// extendida en main.go. Si retorna "" pasa al modelo Naive Bayes.
//
// NOTA: esta función debe registrarse en keywordMatch. Para no modificar el
// enorme switch, se invoca desde un hook al final del case default actual.
// Ver patch en main.go: return extraKeywordMatch(lower) si no hubo match.
func extraKeywordMatch(lower string) string {
	has := func(w ...string) bool { return kwContains(lower, w...) }

	switch {
	case has("ram detalle", "detalle de ram", "modulos de ram", "velocidad ram", "slots de memoria", "cuanta ram"):
		return "ram_detalle"
	case has("tarjeta de video", "gpu", "grafica", "adaptador de video", "controlador de video"):
		return "gpu_info"
	case has("salud del disco fisico", "estado fisico del disco", "disco duro fisico"):
		return "salud_disco"
	case has("almacenamiento por unidad", "espacio en todas las unidades", "resumen de almacenamiento"):
		return "almacenamiento"
	case has("cuanto lleva encendido", "uptime del sistema", "desde cuando prendida", "tiempo de arranque"):
		return "uptime_sistema"
	case has("archivos recientes", "ultimos archivos", "que archivos modifique"):
		return "archivos_recientes"
	case has("latencia de red", "tiempo de respuesta red", "ping promedio"):
		return "latencia_red"
	case has("escanear red", "que equipos hay", "hosts activos", "dispositivos en la lan", "escaneo de red"):
		return "escaneo_red"
	case has("sesiones activas", "quien esta conectado", "usuarios conectados remotamente"):
		return "sesiones_activas"
	case has("estado del antivirus", "defender activo", "proteccion en tiempo real", "estado defender"):
		return "estado_defender"
	case has("actualizaciones instaladas", "hotfix instalados", "parches instalados"):
		return "actualizaciones_instaladas"
	case has("convertir", "convertir unidades", "celsius", "fahrenheit", "pasar gb", "mbps a mb"):
		return "convertir"
	case has("guardar nota", "tomar nota", "anotar", "nota:"):
		return "nota"
	case has("ver notas", "mis notas", "mostrar notas", "que anote"):
		return "ver_notas"
	case has("historial de comandos", "que hice", "comandos recientes"):
		return "historial_comandos"
	case has("modo concentracion", "pausar notificaciones", "focus mode", "no interrumpir"):
		return "modo_concentracion"
	case has("no molestar", "activar no molestar", "desactivar no molestar", "dnd"):
		return "no_molestar"
	case has("limpieza profunda", "limpiar todo", "limpiar fondo"):
		return "limpieza_profunda"
	case has("exportar configuracion", "backup de ajustes", "guardar configuracion frank"):
		return "exportar_config"
	}
	return ""
}
