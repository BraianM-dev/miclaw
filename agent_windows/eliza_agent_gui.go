package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// ======================== ESTRUCTURAS ========================
type Rule struct {
	Patrones   []string `json:"patrones"`
	Respuestas []string `json:"respuestas"`
}

type Rules struct {
	Version string `json:"version"`
	Reglas  []Rule `json:"reglas"`
}

type Ticket struct {
	PCName    string `json:"pc_name"`
	User      string `json:"user"`
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
	Category  string `json:"category"`
	Telemetry string `json:"telemetry"`
}

type ChatMessage struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type ChatHistory struct {
	PCName    string        `json:"pc_name"`
	User      string        `json:"user"`
	Messages  []ChatMessage `json:"messages"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// ======================== CONFIGURACIÓN GLOBAL ========================
var (
	gatewayURL     string
	agentAPIKey    string
	httpListenAddr string
	smtpHost       string
	smtpPort       string
	smtpUser       string
	smtpPass       string
	smtpTo         string

	pcName      string
	currentUser string

	rules      Rules
	rulesMutex sync.RWMutex
	rulesFile  = "eliza_rules.json"

	chatHistoryLabel *widget.Label
	chatScroll       *container.Scroll
	appInstance      fyne.App
	mainWindow       fyne.Window
	uiMutex          sync.Mutex

	chatHistoryData ChatHistory
	historyMutex    sync.RWMutex
	historyFile     = "chat_history.json"

	httpServer *http.Server
	serverCtx  context.Context
	serverStop context.CancelFunc
	wg         sync.WaitGroup

	logger  *log.Logger
	logFile *os.File
)

// ======================== INICIALIZACIÓN ========================
func init() {
	var err error
	logFile, err = os.OpenFile("agent.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal("No se pudo abrir archivo de log:", err)
	}
	logger = log.New(io.MultiWriter(os.Stdout, logFile), "IT-AGENT: ", log.Ldate|log.Ltime|log.Lshortfile)

	gatewayURL = os.Getenv("MICLAW_GATEWAY")
	if gatewayURL == "" {
		gatewayURL = "http://192.168.1.246:3001"
		logger.Println("⚠️ MICLAW_GATEWAY no definido, usando:", gatewayURL)
	}
	agentAPIKey = os.Getenv("AGENT_API_KEY")
	if agentAPIKey == "" {
		agentAPIKey = "ClaveSuperSecretaAFE2026"
		logger.Println("⚠️ AGENT_API_KEY no definido, usando valor por defecto inseguro")
	}
	httpListenAddr = os.Getenv("HTTP_LISTEN_ADDR")
	if httpListenAddr == "" {
		httpListenAddr = ":8081"
	}
	smtpHost = os.Getenv("SMTP_HOST")
	if smtpHost == "" {
		smtpHost = "smtp.gmail.com"
	}
	smtpPort = os.Getenv("SMTP_PORT")
	if smtpPort == "" {
		smtpPort = "587"
	}
	smtpUser = os.Getenv("SMTP_USER")
	smtpPass = os.Getenv("SMTP_PASS")
	smtpTo = os.Getenv("SMTP_TO")
	if smtpTo == "" {
		smtpTo = "soporte@tuempresa.com"
	}

	pcName, _ = os.Hostname()
	if runtime.GOOS == "windows" {
		currentUser = os.Getenv("USERNAME")
	} else {
		currentUser = os.Getenv("USER")
	}
	if currentUser == "" {
		currentUser = "desconocido"
	}
}

func main() {
	logger.Println("🚀 Iniciando Asistente IT AFE v3.0")

	loadChatHistory()
	loadLocalRules()
	go syncRulesPeriodically()

	appInstance = app.NewWithID("com.afe.itassistant")
	mainWindow = appInstance.NewWindow("Soporte IT AFE")
	mainWindow.Resize(fyne.NewSize(550, 700))

	if desk, ok := appInstance.(desktop.App); ok {
		trayMenu := fyne.NewMenu("Soporte IT",
			fyne.NewMenuItem("Mostrar", func() { mainWindow.Show() }),
			fyne.NewMenuItem("Actualizar reglas", manualRuleSync),
			fyne.NewMenuItem("Telemetría ahora", showTelemetryDialog),
			fyne.NewMenuItem("Exportar historial", exportChatHistory),
			fyne.NewMenuItem("Acerca de", showAboutDialog),
		)
		desk.SetSystemTrayMenu(trayMenu)
		mainWindow.SetCloseIntercept(func() { mainWindow.Hide() })
	} else {
		mainWindow.SetCloseIntercept(func() { mainWindow.Hide() })
	}

	buildUI()
	startCommandListener()

	mainWindow.Show()
	logger.Println("✅ Asistente listo")
	appInstance.Run()
	gracefulExit()
}

// ======================== UI ========================
func buildUI() {
	chatHistoryLabel = widget.NewLabel(buildInitialChatText())
	chatHistoryLabel.Wrapping = fyne.TextWrapWord
	chatScroll = container.NewVScroll(chatHistoryLabel)
	chatScroll.SetMinSize(fyne.NewSize(520, 550))

	inputField := widget.NewEntry()
	inputField.SetPlaceHolder("Escribe tu problema aquí...")

	sendButton := widget.NewButtonWithIcon("Enviar", theme.MailSendIcon(), nil)
	sendButton.OnTapped = func() { processMessage(inputField.Text, inputField, sendButton) }
	inputField.OnSubmitted = func(s string) { processMessage(s, inputField, sendButton) }

	clearButton := widget.NewButtonWithIcon("Limpiar pantalla", theme.DeleteIcon(), func() {
		uiMutex.Lock()
		// Limpia la pantalla sin mostrar mensaje adicional
		chatHistoryLabel.SetText(buildInitialChatText() + "\n")
		uiMutex.Unlock()
		scrollToBottom()
		logger.Println("Pantalla limpiada visualmente")
	})

	// 8 botones rápidos
	btnLento := widget.NewButtonWithIcon("PC Lenta", theme.WarningIcon(), func() {
		processMessage("La PC está muy lenta", inputField, sendButton)
	})
	btnImpresora := widget.NewButtonWithIcon("Impresora", theme.DocumentPrintIcon(), func() {
		processMessage("Falla la impresora", inputField, sendButton)
	})
	btnRed := widget.NewButtonWithIcon("Sin Red", theme.ComputerIcon(), func() {
		processMessage("No tengo internet", inputField, sendButton)
	})
	btnBSOD := widget.NewButtonWithIcon("Pantalla Azul", theme.ErrorIcon(), func() {
		processMessage("Tengo pantalla azul", inputField, sendButton)
	})
	btnCorreo := widget.NewButtonWithIcon("Correo", theme.MailComposeIcon(), func() {
		processMessage("Problemas con el correo electrónico", inputField, sendButton)
	})
	btnArchivos := widget.NewButtonWithIcon("Archivos", theme.FolderIcon(), func() {
		processMessage("No puedo abrir o guardar archivos", inputField, sendButton)
	})
	btnSoftware := widget.NewButtonWithIcon("Software", theme.SettingsIcon(), func() {
		processMessage("Un programa no funciona", inputField, sendButton)
	})
	btnApagar := widget.NewButtonWithIcon("Apagar", theme.CancelIcon(), func() {
		processMessage("El equipo no apaga correctamente", inputField, sendButton)
	})

	quickRow1 := container.NewGridWithColumns(4, btnLento, btnImpresora, btnRed, btnBSOD)
	quickRow2 := container.NewGridWithColumns(4, btnCorreo, btnArchivos, btnSoftware, btnApagar)
	quickContainer := container.NewVBox(quickRow1, quickRow2)

	inputContainer := container.NewBorder(nil, nil, nil, container.NewHBox(clearButton, sendButton), inputField)

	mainLayout := container.NewBorder(quickContainer, inputContainer, nil, nil, chatScroll)
	mainWindow.SetContent(mainLayout)
}

func buildInitialChatText() string {
	// Sin indicación de administrador ni modo usuario
	return fmt.Sprintf("🤖 **Asistente IT AFE v3.0**\n✅ Sistema: %s\n👤 Usuario: %s\n💻 Equipo: %s\n📅 Iniciado: %s\n\n¿En qué puedo ayudarte hoy?\n",
		runtime.GOOS, currentUser, pcName, time.Now().Format("02/01/2006 15:04:05"))
}

func processMessage(rawText string, inputField *widget.Entry, sendBtn *widget.Button) {
	text := strings.TrimSpace(rawText)
	if text == "" {
		return
	}

	addToHistory("user", text)

	uiMutex.Lock()
	chatHistoryLabel.SetText(chatHistoryLabel.Text + fmt.Sprintf("\n**Tú (%s):** %s", time.Now().Format("15:04:05"), text))
	uiMutex.Unlock()
	inputField.SetText("")
	inputField.Disable()
	sendBtn.Disable()
	scrollToBottom()

	go func(msg string) {
		time.Sleep(1 * time.Second)
		var respuesta, categoria string

		// Comandos ocultos (no se anuncia su existencia en la UI)
		if strings.HasPrefix(msg, "!") {
			respuesta, categoria = procesarComandoOculto(msg)
		} else {
			respuesta, categoria = getResponse(msg)
		}

		addToHistory("assistant", respuesta)

		fyne.Do(func() {
			uiMutex.Lock()
			chatHistoryLabel.SetText(chatHistoryLabel.Text + fmt.Sprintf("\n🤖 **Soporte (%s):** %s", time.Now().Format("15:04:05"), respuesta))
			uiMutex.Unlock()
			scrollToBottom()
			inputField.Enable()
			sendBtn.Enable()
		})
		go sendTicket(msg, categoria)
	}(text)
}

func scrollToBottom() {
	fyne.Do(func() {
		chatScroll.ScrollToBottom()
	})
}

// ======================== COMANDOS OCULTOS ========================
func procesarComandoOculto(cmd string) (string, string) {
	parts := strings.SplitN(cmd, " ", 2)
	switch parts[0] {
	case "!telemetry", "!telemetria":
		return "📊 **Telemetría del equipo:**\n" + getTelemetry(), "comando"
	case "!clear", "!limpiar":
		fyne.Do(func() {
			uiMutex.Lock()
			chatHistoryLabel.SetText(buildInitialChatText() + "\n")
			uiMutex.Unlock()
		})
		return "Pantalla limpiada.", "comando"
	case "!help", "!ayuda":
		// Verificar estado de conexión con el cerebro (gateway)
		gatewayStatus := "❌ Desconectado"
		if isGatewayReachable() {
			gatewayStatus = "✅ Conectado"
		}
		help := fmt.Sprintf(`**Comandos disponibles:**
!comando <powershell>   → Ejecuta comandos
!telemetry              → Muestra telemetría
!clear                  → Limpia la pantalla
!help                   → Esta ayuda

**Cerebro (Gateway):** %s
URL: %s`, gatewayStatus, gatewayURL)
		return help, "comando"
	case "!cmd", "!comando":
		if len(parts) < 2 {
			return "Uso: !comando <comando PowerShell>", "comando"
		}
		// Confirmación para comandos peligrosos
		confirm := dialog.NewConfirm("Confirmación", "¿Ejecutar comando en PowerShell? Esto puede ser peligroso.", func(ok bool) {
			if ok {
				out, err := ejecutarPowerShell(parts[1])
				fyne.Do(func() {
					if err != nil {
						addToHistory("assistant", fmt.Sprintf("❌ Error: %v\n%s", err, out))
						uiMutex.Lock()
						chatHistoryLabel.SetText(chatHistoryLabel.Text + fmt.Sprintf("\n**🤖 Soporte:** ❌ Error: %v\n%s", err, out))
						uiMutex.Unlock()
					} else {
						if len(out) > 1500 {
							out = out[:1500] + "\n... (truncado)"
						}
						addToHistory("assistant", fmt.Sprintf("✅ Comando ejecutado:\n%s", out))
						uiMutex.Lock()
						chatHistoryLabel.SetText(chatHistoryLabel.Text + fmt.Sprintf("\n**🤖 Soporte:** ✅ Comando ejecutado:\n%s", out))
						uiMutex.Unlock()
					}
					scrollToBottom()
				})
			} else {
				fyne.Do(func() {
					addToHistory("assistant", "Comando cancelado.")
					uiMutex.Lock()
					chatHistoryLabel.SetText(chatHistoryLabel.Text + "\n**🤖 Soporte:** Comando cancelado.")
					uiMutex.Unlock()
					scrollToBottom()
				})
			}
		}, mainWindow)
		confirm.SetDismissText("Cancelar")
		confirm.SetConfirmText("Ejecutar")
		confirm.Show()
		return "Solicitud de confirmación enviada.", "comando"
	default:
		return "Comando no reconocido. Usa !help", "comando"
	}
}

// isGatewayReachable verifica si el gateway está accesible (endpoint /health)
func isGatewayReachable() bool {
	if gatewayURL == "" {
		return false
	}
	client := http.Client{Timeout: 3 * time.Second}
	url := strings.TrimRight(gatewayURL, "/") + "/health"
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func ejecutarPowerShell(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ======================== HISTORIAL PERSISTENTE ========================
func loadChatHistory() {
	historyMutex.Lock()
	defer historyMutex.Unlock()
	data, err := os.ReadFile(historyFile)
	if err != nil {
		chatHistoryData = ChatHistory{
			PCName:    pcName,
			User:      currentUser,
			Messages:  []ChatMessage{},
			UpdatedAt: time.Now(),
		}
		saveChatHistoryLocked()
		return
	}
	if err := json.Unmarshal(data, &chatHistoryData); err != nil {
		logger.Println("Error cargando historial, creando nuevo:", err)
		chatHistoryData = ChatHistory{
			PCName:    pcName,
			User:      currentUser,
			Messages:  []ChatMessage{},
			UpdatedAt: time.Now(),
		}
		saveChatHistoryLocked()
	}
}

func saveChatHistoryLocked() {
	data, err := json.MarshalIndent(chatHistoryData, "", "  ")
	if err != nil {
		logger.Println("Error serializando historial:", err)
		return
	}
	if err := os.WriteFile(historyFile, data, 0644); err != nil {
		logger.Println("Error guardando historial:", err)
	} else {
		chatHistoryData.UpdatedAt = time.Now()
	}
}

func addToHistory(role, content string) {
	historyMutex.Lock()
	defer historyMutex.Unlock()
	chatHistoryData.Messages = append(chatHistoryData.Messages, ChatMessage{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
	if len(chatHistoryData.Messages) > 10000 {
		chatHistoryData.Messages = chatHistoryData.Messages[len(chatHistoryData.Messages)-10000:]
	}
	saveChatHistoryLocked()
}

func exportChatHistory() {
	historyMutex.RLock()
	defer historyMutex.RUnlock()
	data, err := json.MarshalIndent(chatHistoryData, "", "  ")
	if err != nil {
		dialog.ShowError(fmt.Errorf("error exportando: %v", err), mainWindow)
		return
	}
	exportFile := fmt.Sprintf("chat_export_%s.json", time.Now().Format("20060102_150405"))
	if err := os.WriteFile(exportFile, data, 0644); err != nil {
		dialog.ShowError(fmt.Errorf("error guardando exportación: %v", err), mainWindow)
		return
	}
	dialog.ShowInformation("Exportación completa", fmt.Sprintf("Historial exportado a %s", exportFile), mainWindow)
	logger.Println("Historial exportado a", exportFile)
}

// ======================== CEREBRO ELIZA ========================
func loadLocalRules() {
	data, err := os.ReadFile(rulesFile)
	if err != nil {
		setDefaultRules()
		saveRulesToFile()
		return
	}
	var newRules Rules
	if err := json.Unmarshal(data, &newRules); err != nil {
		setDefaultRules()
		saveRulesToFile()
		return
	}
	rulesMutex.Lock()
	rules = newRules
	rulesMutex.Unlock()
	logger.Println("Reglas ELIZA cargadas, versión:", rules.Version)
}

func setDefaultRules() {
	rulesMutex.Lock()
	defer rulesMutex.Unlock()
	rules = Rules{
		Version: "v3.0_extended",
		Reglas: []Rule{
			{
				Patrones:   []string{"hola", "buenas", "buenos dias", "buenas tardes", "que tal", "saludos", "hey", "holi"},
				Respuestas: []string{"¡Hola! Soy el asistente virtual de IT. Cuéntame tu problema técnico.", "¡Buen día! Estoy aquí para ayudarte con tu equipo. ¿Qué sucede?", "¡Saludos! Dime, ¿con qué puedo asistirte hoy?", "Hola, ¿cómo estás? Cuéntame tu inconveniente."},
			},
			{
				Patrones:   []string{"lento", "tranca", "demora", "congeló", "pesada", "lag", "memoria", "cpu", "tarda", "no responde"},
				Respuestas: []string{"Entiendo, es frustrante cuando el equipo no responde. Estoy analizando la telemetría de CPU/RAM. El ticket ya está en el dashboard.", "He registrado el problema de rendimiento. Un técnico lo revisará en breve. Mientras, intenta cerrar programas no esenciales.", "Revisando procesos en segundo plano... A veces el antivirus o actualizaciones consumen muchos recursos. ¿Has notado cuándo empezó la lentitud?", "Te entiendo perfectamente. He priorizado tu ticket por lentitud extrema. Por favor, no forces el apagado."},
			},
			{
				Patrones:   []string{"impresora", "imprime", "tinta", "papel", "atasco", "hoja", "no imprime", "escáner"},
				Respuestas: []string{"Las impresoras de red suelen dar estos problemas. He notificado a soporte para reiniciar el spooler de impresión.", "Verificando cola de impresión... Por favor, revisa si hay papel atascado. El ticket ya está registrado.", "¿Has probado a encender y apagar la impresora? A veces eso soluciona. De todas formas, ya reporté la incidencia.", "Entiendo que la impresión es crítica. Un técnico revisará el servidor de impresión en los próximos minutos."},
			},
			{
				Patrones:   []string{"internet", "wifi", "red", "conexión", "navegar", "sin red", "ethernet", "cable"},
				Respuestas: []string{"Verificando interfaces de red... Si es un corte general, ya estamos trabajando en el enlace. Tu ticket fue enviado.", "Reinicia el router si tienes acceso. Mientras tanto, he reportado el problema al área de infraestructura.", "¿Has probado con otro navegador o dispositivo? A veces es un problema de DNS. He abierto un ticket para revisar la configuración.", "Lamento la falta de conectividad. Ya estamos alertados. Por favor, intenta usar datos móviles si es urgente."},
			},
			{
				Patrones:   []string{"contraseña", "clave", "usuario", "bloqueado", "login", "acceso", "no puedo entrar"},
				Respuestas: []string{"Para resetear credenciales debemos seguir un protocolo de seguridad. Un administrador se pondrá en contacto contigo.", "He notificado al área de seguridad. Por favor, espera asistencia humana para este caso.", "No te preocupes, las cuentas bloqueadas son comunes. Ya generé un ticket prioritario para que te restablezcan el acceso.", "Por seguridad, no compartas tu contraseña con nadie. Un técnico te llamará para verificar tu identidad y ayudarte."},
			},
			{
				Patrones:   []string{"pantalla azul", "bsod", "se reinicia", "crash", "error crítico", "kernel", "dump"},
				Respuestas: []string{"Pantalla azul indica un error grave del sistema. Estoy recopilando el volcado de memoria. Se abrirá un ticket urgente.", "Lo siento mucho. Por favor, anota el código de error si aparece. Ya notifiqué a soporte con prioridad máxima.", "Esto puede deberse a controladores o hardware defectuoso. He enviado toda la telemetría al equipo de ingeniería. No te preocupes, te ayudaremos.", "Por favor, no apagues el equipo a la fuerza si ves la pantalla azul. Deja que recolecte el volcado. Ya estamos en ello."},
			},
			{
				Patrones:   []string{"virus", "malware", "archivo extraño", "popup", "ransomware", "spyware", "publicidad"},
				Respuestas: []string{"Parece que podrías tener software malicioso. Desconecta el equipo de la red si es posible. Ya se generó un ticket de seguridad.", "Importante: No ingreses contraseñas. El equipo de ciberseguridad ya fue alertado.", "Ejecuta un análisis con Windows Defender mientras tanto. He registrado este incidente como crítico.", "Mantén la calma. No pagues ningún rescate si ves mensajes sospechosos. Un especialista se comunicará contigo."},
			},
			{
				Patrones:   []string{"actualización", "windows update", "parche", "update", "reiniciar para actualizar"},
				Respuestas: []string{"Las actualizaciones a veces causan lentitud temporal. Déjalas terminar. Si persiste, avísame y escalaré el caso.", "He registrado tu incidencia con actualizaciones. Puedes revisar el historial de updates desde Configuración.", "¿Se quedó atascada la actualización? A veces forzar un reinicio ayuda. Pero cuidado con los datos abiertos. Ticket registrado.", "Microsoft suele lanzar parches los martes. Si hoy es miércoles y tienes problemas, puede ser un bug conocido. Ya lo reportamos."},
			},
			{
				Patrones:   []string{"sonido", "audio", "altavoz", "micrófono", "no se oye", "auriculares"},
				Respuestas: []string{"Revisando controladores de audio... A veces se silencia solo. Verifica los dispositivos de reproducción. Ticket generado.", "Problema de sonido reportado. Si usas audífonos, prueba conectarlos en otro puerto.", "¿Has probado a ejecutar el solucionador de problemas de audio de Windows? Te ayudo: Configuración > Sonido > Solucionar problemas.", "He notado que algunos modelos tienen conflictos con actualizaciones de audio. Ya enviamos tu caso al fabricante."},
			},
			{
				Patrones:   []string{"teclado", "mouse", "no responde", "periférico", "ratón", "no funciona"},
				Respuestas: []string{"¿El dispositivo está conectado? Prueba cambiarlo de puerto USB. He enviado un aviso a soporte técnico.", "Si es inalámbrico, revisa las pilas. Mientras tanto, ticket registrado.", "A veces el controlador se desinstala. Ve a Administrador de dispositivos y busca cambios de hardware. ¿Necesitas ayuda con eso?", "Te entiendo, es molesto quedarse sin mouse. He registrado tu ticket como prioridad media."},
			},
			{
				Patrones:   []string{"correo", "email", "outlook", "gmail", "no me llegan", "enviar", "recibir"},
				Respuestas: []string{"¿Problemas con el correo? A veces la bandeja está llena o la configuración SMTP falla. He abierto un ticket.", "Verifica tu conexión a internet primero. Si todo está bien, prueba reiniciar Outlook. Ya notifiqué al equipo de Exchange.", "He registrado el incidente de correo. Un administrador revisará los logs del servidor. Mientras, puedes usar webmail si es urgente."},
			},
			{
				Patrones:   []string{"archivo", "documento", "no se abre", "corrupto", "guardar", "pdf", "word", "excel"},
				Respuestas: []string{"¿El archivo no se abre? Podría estar dañado o ser de un formato incompatible. He reportado tu caso.", "Intenta abrirlo con otro programa o desde la nube. Ticket registrado para recuperación de datos si es necesario.", "A veces los antivirus bloquean ciertos archivos. Desactívalo temporalmente bajo tu propio riesgo. He notado tu incidencia."},
			},
			{
				Patrones:   []string{"programa", "aplicación", "software", "no arranca", "se cierra", "error al abrir"},
				Respuestas: []string{"¿Qué programa específico? Puede faltar una DLL o requerir reinstalación. Ya generé el ticket.", "Prueba a ejecutarlo como administrador. Si no funciona, te ayudaré a reinstalarlo remotamente.", "He visto este error antes. Suele ser por una actualización de Windows. El equipo de desarrollo ya está informado."},
			},
			{
				Patrones:   []string{"apagar", "encender", "no apaga", "no prende", "botón de encendido"},
				Respuestas: []string{"Si no apaga, mantén presionado el botón de encendido 10 segundos. Pero primero guarda tu trabajo. Ticket registrado.", "¿Problemas al encender? Podría ser la fuente de poder. He notificado a soporte de hardware.", "No fuerces el apagado repetidamente porque puede dañar el disco. Un técnico revisará tu equipo presencialmente."},
			},
			{
				Patrones:   []string{"*"},
				Respuestas: []string{"Comprendo. Estoy adjuntando los datos técnicos de tu equipo y enviando este reporte al administrador. Aguarda unos minutos.", "Registrado. El equipo técnico procesará esta solicitud en breve. ¿Hay algún otro detalle que deba agregar al reporte?", "Gracias por reportar. He creado un ticket con tu descripción. Un especialista te contactará a la brevedad.", "Entiendo tu situación. Por favor, proporciona más detalles si es posible para agilizar la asistencia."},
			},
		},
	}
}

func saveRulesToFile() {
	rulesMutex.RLock()
	data, err := json.MarshalIndent(rules, "", "  ")
	rulesMutex.RUnlock()
	if err != nil {
		logger.Println("Error serializando reglas:", err)
		return
	}
	os.WriteFile(rulesFile, data, 0644)
}

func getResponse(input string) (string, string) {
	inputLower := strings.ToLower(input)
	rulesMutex.RLock()
	defer rulesMutex.RUnlock()

	for _, rule := range rules.Reglas {
		for _, pat := range rule.Patrones {
			if pat == "*" {
				continue
			}
			if strings.Contains(inputLower, pat) {
				idx := time.Now().UnixNano() % int64(len(rule.Respuestas))
				return rule.Respuestas[idx], pat
			}
		}
	}
	for _, rule := range rules.Reglas {
		for _, pat := range rule.Patrones {
			if pat == "*" {
				idx := time.Now().UnixNano() % int64(len(rule.Respuestas))
				return rule.Respuestas[idx], "comodin"
			}
		}
	}
	return "Ticket generado. Un técnico revisará tu caso.", "default"
}

func syncRulesPeriodically() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		checkAndUpdateRules()
	}
}

func checkAndUpdateRules() {
	client := http.Client{Timeout: 10 * time.Second}
	url := strings.TrimRight(gatewayURL, "/") + "/api/eliza/rules"
	resp, err := client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	var remoteRules Rules
	if err := json.Unmarshal(body, &remoteRules); err != nil {
		return
	}
	rulesMutex.RLock()
	localVersion := rules.Version
	rulesMutex.RUnlock()
	if remoteRules.Version != localVersion {
		if err := os.WriteFile(rulesFile, body, 0644); err == nil {
			rulesMutex.Lock()
			rules = remoteRules
			rulesMutex.Unlock()
			logger.Println("Reglas actualizadas a versión", remoteRules.Version)
		}
	}
}

func manualRuleSync() {
	dialog.ShowInformation("Actualizando reglas", "Buscando nuevas reglas...", mainWindow)
	go func() {
		checkAndUpdateRules()
		fyne.Do(func() {
			dialog.ShowInformation("Sincronización completa", "Reglas actualizadas.", mainWindow)
		})
	}()
}

// ======================== TELEMETRÍA ========================
func getTelemetry() string {
	if runtime.GOOS != "windows" {
		return "Telemetría solo disponible en Windows"
	}
	psScript := `
$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
$ram = Get-CimInstance Win32_OperatingSystem
$disk = Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='C:'"
$uptime = (Get-Date) - (Get-CimInstance Win32_OperatingSystem).LastBootUpTime
$procs = Get-Process | Sort-Object CPU -Descending | Select-Object -First 5 | ForEach-Object { "$($_.ProcessName) ($($_.CPU))" }
$cpuLoad = if ($cpu.LoadPercentage) { $cpu.LoadPercentage } else { (Get-Counter "\Processor(_Total)\% Processor Time").CounterSamples.CookedValue }
$ramTotal = [math]::Round($ram.TotalVisibleMemorySize/1MB, 2)
$ramFree = [math]::Round($ram.FreePhysicalMemory/1MB, 2)
$ramUsed = $ramTotal - $ramFree
$ramPercent = [math]::Round(($ramUsed / $ramTotal) * 100, 2)
$diskFreeGB = [math]::Round($disk.FreeSpace/1GB, 2)
$diskTotalGB = [math]::Round($disk.Size/1GB, 2)
$uptimeHours = [math]::Round($uptime.TotalHours, 2)
"[CPU: ${cpuLoad}%] [RAM: ${ramPercent}% (${ramUsed}/${ramTotal} GB)] [DISCO C: ${diskFreeGB}/${diskTotalGB} GB libre] [UPTIME: ${uptimeHours}h] [TOP5: $($procs -join ', ')]"
`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("Telemetría fallida: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func showTelemetryDialog() {
	tele := getTelemetry()
	dialog.ShowInformation("Telemetría del equipo", tele, mainWindow)
}

// ======================== TICKETS ========================
func sendTicket(message, category string) {
	telemetry := getTelemetry()
	ticket := Ticket{
		PCName:    pcName,
		User:      currentUser,
		Timestamp: time.Now().Format(time.RFC3339),
		Message:   message,
		Category:  category,
		Telemetry: telemetry,
	}
	jsonData, _ := json.Marshal(ticket)
	url := strings.TrimRight(gatewayURL, "/") + "/api/eliza/ticket"
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil || resp.StatusCode != http.StatusOK {
		go sendEmailFallback(ticket)
	}
	if resp != nil {
		defer resp.Body.Close()
	}
}

func sendEmailFallback(ticket Ticket) {
	if smtpUser == "" || smtpPass == "" {
		return
	}
	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	subject := "[CONTINGENCIA IT] Ticket desde " + ticket.PCName
	body := fmt.Sprintf("PC: %s\nUsuario: %s\nFecha: %s\nCategoría: %s\nProblema: %s\nTelemetría: %s",
		ticket.PCName, ticket.User, ticket.Timestamp, ticket.Category, ticket.Message, ticket.Telemetry)
	msg := fmt.Sprintf("Subject: %s\n\n%s", subject, body)
	addr := smtpHost + ":" + smtpPort
	smtp.SendMail(addr, auth, smtpUser, []string{smtpTo}, []byte(msg))
}

// ======================== SERVIDOR REMOTO ========================
func startCommandListener() {
	serverCtx, serverStop = context.WithCancel(context.Background())
	mux := http.NewServeMux()

	mux.HandleFunc("/execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != agentAPIKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var payload struct {
			Command    string                 `json:"command"`
			Parameters map[string]interface{} `json:"parameters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		cmdStr := payload.Command
		if len(payload.Parameters) > 0 {
			paramsJSON, _ := json.Marshal(payload.Parameters)
			cmdStr = fmt.Sprintf("$params = '%s' | ConvertFrom-Json; %s", paramsJSON, payload.Command)
		}
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", cmdStr)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.CombinedOutput()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("Error: %s - %v", string(out), err)))
			return
		}
		w.Write(out)
	})

	mux.HandleFunc("/send_message", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != agentAPIKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		if payload.Message == "" {
			http.Error(w, "Message required", http.StatusBadRequest)
			return
		}
		addToHistory("system", payload.Message)
		fyne.Do(func() {
			uiMutex.Lock()
			chatHistoryLabel.SetText(chatHistoryLabel.Text + fmt.Sprintf("\n🔧 **Soporte remoto (%s):** %s", time.Now().Format("15:04:05"), payload.Message))
			uiMutex.Unlock()
			scrollToBottom()
		})
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/chat_history", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != agentAPIKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		historyMutex.RLock()
		defer historyMutex.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatHistoryData)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"alive"}`))
	})

	httpServer = &http.Server{Addr: httpListenAddr, Handler: mux}
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Println("Servidor remoto escuchando en", httpListenAddr)
		httpServer.ListenAndServe()
	}()
	go func() {
		<-serverCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(ctx)
	}()
}

// ======================== CIERRE ========================
func gracefulExit() {
	logger.Println("Cerrando asistente...")
	historyMutex.Lock()
	saveChatHistoryLocked()
	historyMutex.Unlock()
	if serverStop != nil {
		serverStop()
	}
	wg.Wait()
	logFile.Close()
	os.Exit(0)
}

func showAboutDialog() {
	aboutMsg := fmt.Sprintf("Asistente IT AFE v3.0\n\nPlataforma: %s/%s\nPC: %s\nUsuario: %s\n\nCerebro ELIZA con reglas dinámicas\nSincronización con gateway\nTelemetría avanzada\nEjecución remota segura\nHistorial persistente\nConsulta remota de conversaciones\n\n© 2026 - Soporte Técnico", runtime.GOOS, runtime.GOARCH, pcName, currentUser)
	dialog.ShowInformation("Acerca de", aboutMsg, mainWindow)
}
