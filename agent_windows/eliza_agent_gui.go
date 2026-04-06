package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// --- Estructuras ELIZA ---
type Rule struct {
	Patrones   []string `json:"patrones"`
	Respuestas []string `json:"respuestas"`
}

type Rules struct {
	Version string `json:"version"`
	Reglas  []Rule `json:"reglas"`
}

// --- CONFIGURACIÓN ---
const (
	smtpHost = "smtp.gmail.com"
	smtpPort = "587"
	smtpUser = "tu_correo@gmail.com"
	smtpPass = "tu_app_password"
	smtpTo   = "soporte@tuempresa.com"
)

var (
	gatewayURL  string
	pcName      string
	currentUser string
	chatHistory *widget.Label
	rulesFile   = "eliza_rules.json"
	rules       Rules
)

func main() {
	pcName, _ = os.Hostname()
	currentUser = os.Getenv("USERNAME")
	
	gatewayURL = os.Getenv("MICLAW_GATEWAY")
	if gatewayURL == "" { gatewayURL = "http://192.168.1.100:3000" } // IP de tu servidor

	// Cargar el cerebro conversacional
	loadLocalRules()
	go syncRulesPeriodically()

	a := app.New()
	w := a.NewWindow("Soporte IT AFE")
	w.Resize(fyne.NewSize(450, 600))

	if desk, ok := a.(desktop.App); ok {
		m := fyne.NewMenu("Soporte IT",
			fyne.NewMenuItem("Abrir", func() { w.Show() }),
			fyne.NewMenuItem("Salir", func() { os.Exit(0) }),
		)
		desk.SetSystemTrayMenu(m)
		w.SetCloseIntercept(func() { w.Hide() })
	}

	chatHistory = widget.NewLabel("🤖 Asistente IT Inicializado.\n¿En qué te puedo ayudar hoy?\n")
	chatHistory.Wrapping = fyne.TextWrapWord
	scrollArea := container.NewVScroll(chatHistory)
	scrollArea.SetMinSize(fyne.NewSize(400, 450))

	inputField := widget.NewEntry()
	inputField.SetPlaceHolder("Describe tu problema...")

	processMessage := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" { return }
		
		chatHistory.SetText(chatHistory.Text + "\nTú: " + text + "\n")
		inputField.SetText("")
		inputField.Disable()

		go func(msg string) {
			// Pausa para dar la sensación de "estar pensando y escribiendo"
			time.Sleep(2 * time.Second)
			
			// ELIZA analiza el texto y busca la mejor respuesta
			respuesta, categoria := getResponse(msg)
			
			chatHistory.SetText(chatHistory.Text + "\nSoporte: " + respuesta + "\n")
			inputField.Enable()
			
			// Mientras charla, mandamos la telemetría al servidor por detrás
			sendTicket(msg, categoria)
		}(text)
	}

	sendButton := widget.NewButtonWithIcon("Enviar", theme.MailSendIcon(), func() { processMessage(inputField.Text) })
	inputField.OnSubmitted = func(s string) { processMessage(s) }

	btnLento := widget.NewButton("PC Lenta", func() { processMessage("La PC está muy lenta") })
	btnImp := widget.NewButton("Impresora", func() { processMessage("Falla la impresora") })
	btnRed := widget.NewButton("Sin Red", func() { processMessage("No tengo internet") })
	quickButtons := container.NewGridWithColumns(3, btnLento, btnImp, btnRed)

	mainLayout := container.NewBorder(quickButtons, container.NewBorder(nil, nil, nil, sendButton, inputField), nil, nil, scrollArea)
	w.SetContent(mainLayout)

	a.Run()
}

// ---------- Cerebro ELIZA (Conversacional) ----------
func loadLocalRules() {
	data, err := os.ReadFile(rulesFile)
	if err != nil {
		setDefaultRules()
		return
	}
	if err := json.Unmarshal(data, &rules); err != nil {
		setDefaultRules()
	}
}

// Este es el cerebro de respaldo por si no hay internet. ¡Acá podés agregar lo que quieras!
func setDefaultRules() {
	rules = Rules{
		Version: "offline",
		Reglas: []Rule{
			{
				Patrones: []string{"hola", "buenas", "buenos dias", "buenas tardes", "que tal"},
				Respuestas: []string{"¡Hola! Soy el asistente virtual de IT. Contame, ¿con qué problema técnico te puedo ayudar hoy?", "¡Buenas! Estoy acá para darte una mano con la compu. ¿Qué andaría pasando?"},
			},
			{
				Patrones: []string{"lento", "tranca", "demora", "congeló", "pesada", "lag", "memoria"},
				Respuestas: []string{"Entiendo, es frustrante cuando la máquina no responde. Ya estoy analizando la telemetría de tu RAM y CPU. No la apagues a la fuerza, el ticket ya está en el Dashboard.", "Revisando consumo de procesos... A veces estos equipos requieren una optimización remota. He registrado el problema con prioridad alta."},
			},
			{
				Patrones: []string{"impresora", "imprime", "tinta", "papel", "atasco", "hoja"},
				Respuestas: []string{"Las impresoras compartidas suelen dar estos dolores de cabeza, lo sé. He notificado a Soporte para que revisemos el spooler de tu equipo a la brevedad."},
			},
			{
				Patrones: []string{"internet", "wifi", "red", "conexión", "navegar"},
				Respuestas: []string{"Verificando interfaces de red... Si hay un microcorte general, probablemente ya estemos trabajando en el enlace. De todas formas, tu ticket fue enviado."},
			},
			{
				Patrones: []string{"contraseña", "clave", "usuario", "bloqueado"},
				Respuestas: []string{"Para resetear credenciales tenemos que seguir un protocolo de seguridad humano. Ya le mandé el aviso al administrador para que se contacte con vos."},
			},
			{
				Patrones: []string{"*"}, // El comodín si no entiende la frase
				Respuestas: []string{"Comprendo. Estoy adjuntando los datos técnicos de tu equipo y enviando este reporte detallado al administrador. Aguardá unos minutos.", "Registrado. El equipo técnico procesará esta solicitud en breve. ¿Hay algún otro detalle que deba agregar al reporte?"},
			},
		},
	}
}

func getResponse(input string) (string, string) {
	inputLower := strings.ToLower(input)
	for _, rule := range rules.Reglas {
		for _, pat := range rule.Patrones {
			if pat == "*" || strings.Contains(inputLower, pat) {
				// Elige una respuesta al azar dentro de la categoría
				resp := rule.Respuestas[time.Now().UnixNano()%int64(len(rule.Respuestas))]
				return resp, pat
			}
		}
	}
	return "Ticket generado.", "general"
}

// Sincroniza con el Gateway para aprender respuestas nuevas sin tener que compilar de nuevo
func syncRulesPeriodically() {
	ticker := time.NewTicker(1 * time.Hour)
	for range ticker.C {
		checkAndUpdateRules()
	}
}

func checkAndUpdateRules() {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(gatewayURL + "/api/eliza/rules")
	if err != nil || resp.StatusCode != http.StatusOK { return }
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil { return }
	var remoteRules Rules
	if err := json.Unmarshal(body, &remoteRules); err == nil {
		if remoteRules.Version != rules.Version {
			os.WriteFile(rulesFile, body, 0644)
			json.Unmarshal(body, &rules)
		}
	}
}

// ---------- Telemetría Silenciosa ----------
func getTelemetry() string {
	psScript := `$cpu = Get-CimInstance Win32_Processor; $disk = Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='C:'"; "[CPU: $($cpu.LoadPercentage)%] [C: Libre $([math]::Round($disk.FreeSpace/1GB, 2))GB]"`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil { return "Telemetría fallida" }
	return strings.TrimSpace(string(out))
}

// ---------- Envío de Datos ----------
func sendTicket(message, category string) {
	telemetry := getTelemetry()
	
	ticket := map[string]interface{}{
		"pc_name":   pcName,
		"user":      currentUser,
		"timestamp": time.Now().Format(time.RFC3339),
		"message":   message,
		"category":  category,
		"telemetry": telemetry,
	}

	jsonData, _ := json.Marshal(ticket)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(gatewayURL+"/api/eliza/ticket", "application/json", bytes.NewBuffer(jsonData))
	
	if err != nil || resp.StatusCode != http.StatusOK {
		go sendEmailFallback(ticket)
		return
	}
	defer resp.Body.Close()
}

func sendEmailFallback(t map[string]interface{}) {
	if smtpUser == "" { return }
	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	body := fmt.Sprintf("Subject: [CONTINGENCIA IT] Ticket de %s\n\nPC: %s\nUsuario: %s\nProblema: %s\nTelemetría: %s", t["pc_name"], t["pc_name"], t["user"], t["message"], t["telemetry"])
	smtp.SendMail(smtpHost+":"+smtpPort, auth, smtpUser, []string{smtpTo}, []byte(body))
}


// ---------- Receptor de Comandos (Las "manos" del agente) ----------
func startCommandListener() {
	http.HandleFunc("/execute", func(w http.ResponseWriter, r *http.Request) {
		// Verificamos la clave de seguridad para que nadie más pueda mandarle comandos
		apiKey := r.Header.Get("X-API-Key")
		if apiKey != "ClaveSuperSecretaAFE2026" { // <-- Debe coincidir con MICLAW_AGENT_KEY de tu .env
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

		// Ejecuta el comando recibido en PowerShell silenciosamente
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", payload.Command)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		
		out, err := cmd.CombinedOutput()
		if err != nil {
			w.Write([]byte("Error: " + string(out) + " - " + err.Error()))
			return
		}
		w.Write(out)
	})

	// El agente se queda escuchando silenciosamente en el puerto 8081
	go http.ListenAndServe(":8081", nil)
}