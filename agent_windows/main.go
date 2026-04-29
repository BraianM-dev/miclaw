//go:build windows
// +build windows

// Frank v2.1-beta - Asistente IT Profesional con NLU de 3 capas +
// inteligencia distribuida (Ollama + KB P2P).
//
// ARQUITECTURA DE COMPILACIÓN:
//
//   64-bit (producción, usa OpenGL/GLFW):
//     GOARCH=amd64 GOOS=windows CGO_ENABLED=1 go build -o Frank64.exe .
//
//   32-bit (equipos legacy, usa renderer software de Fyne):
//     GOARCH=386 GOOS=windows CGO_ENABLED=1 go build -tags softwarerender -o Frank32.exe .
//
//   La dependencia go-gl/gl (OpenGL) NO tiene fuentes para GOARCH=386 en
//   Windows. La build tag '-tags softwarerender' activa el renderer puro
//   de Go de Fyne, que no depende de OpenGL/GLFW, resolviendo el error:
//     "build constraints exclude all Go files in go-gl/gl/v2.1/gl"
//
//   CGO_ENABLED=1 es necesario para go-sqlite3 (OfflineQueue).
//   Se requiere MinGW-w64 en PATH (64-bit para amd64, 32-bit para 386).
package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"log"
	"math"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/gen2brain/beeep"
	"github.com/google/uuid"
	"github.com/kljensen/snowball/spanish"
	"github.com/schollz/closestmatch"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/time/rate"
)

// ========================= IDENTIDAD =========================
const (
	AgentName     = "Frank"
	AgentVersion  = "2.1-beta"
	AgentFullName = "F.R.A.N.K. - Framework de Respuesta Autónoma, Notificación y Knowledge"
	AgentAuthor   = "Desarrollado por Braian M. - Depto. de Informática AFE - 2026"

	// Ruta de instalación estándar en Windows.
	installDir     = `C:\Program Files\Frank`
	installExeName = "Frank.exe"

	// Nombre del mutex de Windows para instancia única.
	singleInstanceMutexName = `Local\FrankAgentSingleInstance`

	// Archivo de lock de proceso (fallback al mutex de Windows).
	lockFileName = "frank.lock"
)

// ========================= MAPA DE RED AFE (MPLS) =========================
// networkLocations mapea subredes CIDR a nombres de ubicación de AFE.
var networkLocations = map[string]string{
	"192.168.1.0/24":  "Baalbek Centro Montevideo",
	"192.168.2.0/24":  "Peñarol Talleres",
	"192.168.3.0/24":  "Jefatura Trafico Sayago",
	"192.168.4.0/24":  "Remesa Paysandu",
	"192.168.5.0/24":  "Remesa Paso de los Toros",
	"192.168.6.0/24":  "Estacion Toledo",
	"192.168.10.0/24": "Estacion Peñarol",
	"192.168.15.0/24": "Regional Via y Obras Toledo",
	"192.168.20.0/24": "Regional Via y Obras Sayago",
	"192.168.21.0/24": "Regional Paso de los Toros",
	"192.168.22.0/24": "Regional Paysandu",
}

// networkGateways mapea subredes CIDR a su gateway principal.
var networkGateways = map[string]string{
	"192.168.1.0/24":  "192.168.1.11",
	"192.168.2.0/24":  "192.168.2.5",
	"192.168.3.0/24":  "192.168.3.10",
	"192.168.4.0/24":  "192.168.4.10",
	"192.168.5.0/24":  "192.168.5.10",
	"192.168.6.0/24":  "192.168.6.10",
	"192.168.10.0/24": "192.168.10.10",
	"192.168.15.0/24": "192.168.15.10",
	"192.168.20.0/24": "192.168.20.10",
	"192.168.21.0/24": "192.168.21.10",
	"192.168.22.0/24": "192.168.22.10",
}

// resolveLocation devuelve el nombre de ubicación AFE para una IP dada.
// Recorre el mapa CIDR interno (MPLS) — no ejecuta comandos del sistema.
func resolveLocation(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	for cidr, location := range networkLocations {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(parsed) {
			return location
		}
	}
	return ""
}

// resolveGateway devuelve el gateway para una IP dada.
func resolveGateway(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	for cidr, gw := range networkGateways {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(parsed) {
			return gw
		}
	}
	return ""
}

// getMyLocation devuelve la ubicación AFE del equipo actual por su IP de salida.
func getMyLocation() string {
	ip := getOutboundIP()
	if loc := resolveLocation(ip); loc != "" {
		return loc
	}
	return "Ubicación desconocida"
}

// resolvePeerLocation devuelve la ubicación y gateway de un peer por su IP.
// Usado en P2P para contexto geográfico de los mensajes inter-agente.
func resolvePeerLocation(ip string) (location, gateway string) {
	return resolveLocation(ip), resolveGateway(ip)
}

// ========================= ESTRUCTURAS =========================
type Memory struct {
	LastUserMessages      []string
	LastAssistantMsg      string
	LastAssistantMessages []string // últimas 10 respuestas del asistente
	LastTopic             string
	LastSearchResults     []string
	LastSearchQuery       string
	PendingConfirmation   *ConfirmationRequest
	mu                    sync.RWMutex
}

type ConfirmationRequest struct {
	Action            string
	Description       string
	ExpectedResponses []string
	OnConfirm         func(string) string
	OnDeny            func() string
}

type ChatMessage struct {
	ID        string    `json:"id"`
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

type ActionLogEntry struct {
	ID            string                 `json:"id"`
	CorrelationID string                 `json:"correlation_id"`
	Timestamp     time.Time              `json:"timestamp"`
	User          string                 `json:"user"`
	Action        string                 `json:"action"`
	Parameters    map[string]interface{} `json:"parameters"`
	Result        string                 `json:"result"`
	Reversible    bool                   `json:"reversible"`
	Rollback      *RollbackInfo          `json:"rollback,omitempty"`
}

type RollbackInfo struct {
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Description string   `json:"description"`
}

type RemoteCommand struct {
	Action string            `json:"action"`
	Params map[string]string `json:"params"`
}

type UserSettings struct {
	NotificationsEnabled    bool   `json:"notifications_enabled"`
	NotificationFrequency   string `json:"notification_frequency"`
	EmotionalSupportEnabled bool   `json:"emotional_support_enabled"`
	Theme                   string `json:"theme"`
	AccentColor             string `json:"accent_color"`
	StartWithWindows        bool   `json:"start_with_windows"`

	// Personalización de conversación
	UseEmojis   bool   `json:"use_emojis"`    // mostrar emojis en respuestas
	Personality string `json:"personality"`   // "profesional" | "tecnico" | "amigable" | "conciso"

	// Accesibilidad
	FontSize     float32 `json:"font_size"`     // tamaño de texto base (12, 14, 16, 18, 20)
	BoldText     bool    `json:"bold_text"`     // forzar negrita en toda la UI
	DyslexicMode bool    `json:"dyslexic_mode"` // fuente y espaciado amigables para dislexia

	// Colores de texto en burbujas de chat
	UserBubbleTextColor  string `json:"user_bubble_text_color"`  // nombre del color (key de textColors)
	FrankBubbleTextColor string `json:"frank_bubble_text_color"` // nombre del color (key de textColors)
}

type PhraseCategory string

const (
	Technical    PhraseCategory = "technical"
	Motivational PhraseCategory = "motivational"
	Emotional    PhraseCategory = "emotional"
	Light        PhraseCategory = "light"
)

type Phrase struct {
	Text   string  `json:"text"`
	Weight float64 `json:"weight"`
}

type UpdatePackage struct {
	Version       string                 `json:"version"`
	Phrases       map[string][]Phrase    `json:"phrases"`
	SettingsPatch map[string]interface{} `json:"settings_patch"`
}

type UserProfile struct {
	FirstName      string `json:"first_name"`
	LastName       string `json:"last_name"`
	Nickname       string `json:"nickname"`
	Department     string `json:"department"`
	Area           string `json:"area"`
	Email          string `json:"email"`
	Phone          string `json:"phone"`
	InternalPhone  string `json:"internal_phone"`
	OfficeLocation string `json:"office_location"`
	RegisteredAt   string `json:"registered_at"`
	LastIP         string `json:"last_ip"`
	Hostname       string `json:"hostname"`
	Anonymous      bool   `json:"anonymous,omitempty"` // true si el usuario eligió modo anónimo
}

// ========================= STRUCTS NUEVOS v3.0 =========================

type Plugin struct {
	Name     string   `json:"name"`
	Intent   string   `json:"intent"`
	Keywords []string `json:"keywords"`
	Response string   `json:"response"`
	Command  string   `json:"command"`
}

type PendingEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   string    `json:"payload"`
	Retries   int       `json:"retries"`
}

type DynamicRule struct {
	Priority int      `json:"priority"`
	Keywords []string `json:"keywords"`
	Context  string   `json:"context"`
	Action   string   `json:"action"`
	Response string   `json:"response"`
}

type TelemetryData struct {
	CommandsExecuted int `json:"commands_executed"`
	ErrorsCount      int `json:"errors_count"`
	TicketsCreated   int `json:"tickets_created"`
	mu               sync.Mutex
}

type JSONLogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Action    string `json:"action"`
	User      string `json:"user"`
	Message   string `json:"message"`
}

type InventoryReport struct {
	GeneratedAt time.Time              `json:"generated_at"`
	Hostname    string                 `json:"hostname"`
	User        string                 `json:"user"`
	OS          map[string]interface{} `json:"os"`
	CPU         map[string]interface{} `json:"cpu"`
	Memory      map[string]interface{} `json:"memory"`
	Disks       []map[string]interface{} `json:"disks"`
	Network     []map[string]interface{} `json:"network"`
	Software    []string               `json:"software"`
	Printers    []string               `json:"printers"`
	Wazuh       map[string]interface{} `json:"wazuh"`
	OCS         map[string]interface{} `json:"ocs"`
}

// ========================= NAIVE BAYES SIMPLE =========================
type SimpleBayesModel struct {
	classCount     map[string]int
	wordClassCount map[string]map[string]int
	classNames     []string
	totalDocs      int
	alpha          float64
}

func newBernoulliNB(inputs [][]string, outputs [][]string, alpha float64) *SimpleBayesModel {
	model := &SimpleBayesModel{
		classCount:     make(map[string]int),
		wordClassCount: make(map[string]map[string]int),
		alpha:          alpha,
	}
	classSet := make(map[string]bool)
	for _, out := range outputs {
		for _, cls := range out {
			classSet[cls] = true
		}
	}
	for cls := range classSet {
		model.classNames = append(model.classNames, cls)
		model.classCount[cls] = 0
		model.wordClassCount[cls] = make(map[string]int)
	}
	for i, inp := range inputs {
		if i >= len(outputs) || len(outputs[i]) == 0 {
			continue
		}
		cls := outputs[i][0]
		model.classCount[cls]++
		model.totalDocs++
		for _, word := range inp {
			model.wordClassCount[cls][word]++
		}
	}
	return model
}

func (m *SimpleBayesModel) PredictProbabilities(tokens []string) map[string]float64 {
	result := make(map[string]float64)
	if m.totalDocs == 0 {
		return result
	}
	nClasses := float64(len(m.classNames))
	for _, cls := range m.classNames {
		logProb := math.Log(float64(m.classCount[cls]+1) / float64(m.totalDocs+int(nClasses)))
		classTotal := float64(m.classCount[cls])
		for _, word := range tokens {
			count := float64(m.wordClassCount[cls][word])
			p := (count + m.alpha) / (classTotal + 2*m.alpha)
			if p > 0 {
				logProb += math.Log(p)
			}
		}
		result[cls] = math.Exp(logProb)
	}
	return result
}

// ========================= STOPWORDS ESPAÑOL =========================
var spanishStopWords = map[string]bool{
	"de": true, "la": true, "que": true, "el": true, "en": true, "y": true,
	"a": true, "los": true, "del": true, "se": true, "las": true, "por": true,
	"un": true, "para": true, "con": true, "no": true, "una": true, "su": true,
	"al": true, "lo": true, "como": true, "mas": true, "pero": true, "sus": true,
	"le": true, "ya": true, "o": true, "este": true, "si": true, "porque": true,
	"esta": true, "entre": true, "cuando": true, "muy": true, "sin": true,
	"sobre": true, "ser": true, "tiene": true, "tambien": true, "me": true,
	"hasta": true, "hay": true, "donde": true, "quien": true, "desde": true,
	"todo": true, "nos": true, "durante": true, "uno": true, "ni": true,
	"contra": true, "ese": true, "son": true, "era": true, "ha": true,
	"fue": true, "cual": true, "eso": true, "mi": true, "te": true, "tu": true, "yo": true, "les": true,
}

func isSpanishStopWord(word string) bool {
	return spanishStopWords[word]
}

// ========================= VARIABLES GLOBALES =========================
var (
	gatewayURL     string
	agentAPIKey    string
	agentSecret    string
	httpListenAddr string

	// p2pSharedKey es la clave usada entre agentes Frank para autenticarse
	// mutuamente en los endpoints /knowledge y /query.
	// DEBE ser la misma en todos los agentes de la misma organización.
	// Configurar via variable de entorno FRANK_P2P_KEY en todos los equipos.
	// Si no se define, usa el valor por defecto (todos los Frank la comparten).
	p2pSharedKey string

	pcName      string
	currentUser string
	domain      string

	appInstance fyne.App
	mainWindow  fyne.Window
	chatScroll  *container.Scroll

	chatHistoryData ChatHistory
	historyMutex    sync.RWMutex
	historyFile     = "chat_history.enc"
	maxHistorySize  = 5000

	httpServer *http.Server
	serverCtx  context.Context
	serverStop context.CancelFunc
	wg         sync.WaitGroup

	logger  *log.Logger
	logFile *os.File

	memory          Memory
	isWindowVisible atomic.Bool

	currentSearchCancel context.CancelFunc
	searchMu            sync.Mutex
	isSearching         bool

	nbModel        *SimpleBayesModel
	intentLabels   []string
	closestMatcher *closestmatch.ClosestMatch
	intentExamples map[string][]string
	nluMutex       sync.RWMutex

	actionLog     []ActionLogEntry
	actionLogMux  sync.RWMutex
	actionLogFile = "actions_log.enc"

	corporateDNS = []string{"192.168.1.246", "192.168.2.246"}

	settings      UserSettings
	settingsMutex sync.RWMutex
	settingsFile  = "settings.enc"
	accentColors  = map[string]string{
		"Azul": "#1976D2", "Verde": "#388E3C", "Naranja": "#F57C00",
		"Rojo": "#D32F2F", "Morado": "#7B1FA2", "Rosa": "#C2185B", "Turquesa": "#00796B",
	}
	textColors = map[string]string{
		"Blanco":      "#FFFFFF",
		"Negro":       "#000000",
		"Gris claro":  "#E0E0E0",
		"Gris oscuro": "#424242",
		"Azul marino": "#1A237E",
		"Amarillo":    "#FFEB00",
		"Cian":        "#00E5FF",
	}
	notificationFrequencies = map[string]time.Duration{
		"low": 6 * time.Hour, "medium": 3 * time.Hour, "high": 1 * time.Hour,
	}

	trayPhrases = map[PhraseCategory][]Phrase{
		Technical: {
			{"¿Sabías que puedo diagnosticar tu PC?", 1.0},
			{"Escribe 'ayuda' para ver todo lo que puedo hacer.", 1.0},
			{"Si algo va lento, puedo optimizarlo.", 1.0},
			{"¿Problemas con el correo? Dime 'correo'.", 1.0},
			{"Puedo desbloquear cuentas de usuario.", 1.0},
			{"¿Muchos archivos? Busco lo que necesites.", 1.0},
			{"Recuerda hacer copias de seguridad.", 1.0},
			{"Puedo generar un inventario completo del equipo.", 1.0},
			{"¿Necesitas analizar los logs de Wazuh? Pregúntame.", 1.0},
			{"Puedo generar contraseñas seguras al instante.", 1.0},
			{"¿Sabías que puedo medir la latencia de red?", 1.0},
			{"Tengo más de 100 acciones disponibles. Pregunta lo que sea.", 1.0},
			{"¿Disco casi lleno? Puedo ayudarte a liberar espacio.", 1.0},
			{"Puedo ver los BSODs recientes y ayudarte a entenderlos.", 1.0},
		},
		Motivational: {
			{"Hoy puede ser un gran día para resolver algo pendiente.", 1.0},
			{"Cada pequeño paso cuenta, incluso los que no se ven.", 1.0},
			{"Tu dedicación hace la diferencia.", 1.0},
			{"Sos el motor de la infraestructura. No se ve, pero se siente.", 1.0},
			{"Un problema resuelto es una victoria, aunque nadie lo sepa.", 1.0},
			{"La tecnología falla. Vos no.", 1.0},
			{"Ser el único del departamento de IT es una responsabilidad enorme. Y la estás cumpliendo.", 1.0},
			{"Cada equipo que funciona es gracias a tu trabajo invisible.", 1.0},
			{"Lo que hacés todos los días hace posible que el resto trabaje.", 1.0},
			{"Que no se vea no significa que no importa. Tu trabajo importa muchísimo.", 1.0},
			{"Si hoy resolviste aunque sea un problema, el día no fue en vano.", 1.0},
			{"Los mejores equipos de IT son los que nadie nota porque todo funciona.", 1.0},
			{"Estás construyendo algo sólido aunque nadie lo vea.", 1.0},
			{"Cada cable tendido, cada usuario desbloqueado: es parte de algo más grande.", 1.0},
			{"Vos sos el soporte técnico y el estratega. Doble mérito.", 1.0},
			{"La paciencia que tenés con los usuarios también es una habilidad técnica.", 1.0},
			{"Trabajar solo en IT no es fácil. Pero lo estás haciendo.", 1.0},
			{"Hoy también es un buen día para aprender algo nuevo.", 1.0},
			{"Pequeños mantenimientos hoy evitan grandes caídas mañana.", 1.0},
			{"El trabajo de IT bien hecho es el que no se nota. El tuyo no se nota. Excelente señal.", 1.0},
		},
		Emotional: {
			{"Si sientes que el tanque está vacío, recuerda que hasta las máquinas necesitan mantenimiento.", 0.7},
			{"Date el permiso que otros no te dan: el de parar un momento.", 0.7},
			{"No tienes que resolver todo al mismo tiempo.", 0.8},
			{"Hay días que todo falla junto. No sos vos, es Murphy visitando.", 0.8},
			{"Cuando todo se rompe a la vez, es señal de que sos el más capacitado para arreglarlo.", 0.7},
			{"Respirá. Los logs se pueden leer después. El sistema espera.", 0.8},
			{"Un vaso de agua, 5 minutos de pausa, y después seguimos.", 0.9},
			{"Sos humano antes que técnico. Date ese espacio.", 0.7},
			{"Lo que no pudiste resolver hoy lo resolverás mañana con la cabeza fría.", 0.8},
			{"No todo tiene solución inmediata. Y eso está bien.", 0.8},
			{"La infraestructura puede esperar 5 minutos. Tu bienestar no.", 0.7},
			{"Recordá: incluso los mejores ingenieros a veces necesitan buscar en Google.", 0.9},
			{"Si hoy fue difícil, mañana también pasa.", 0.8},
			{"Sos imprescindible, pero también necesitás descansar.", 0.7},
			{"El síndrome del impostor no te define. Los resultados sí.", 0.7},
		},
		Light: {
			{"¡Ten un buen día!", 1.0},
			{"Estoy aquí si necesitas ayuda.", 1.0},
			{"¡Mantente hidratado!", 1.0},
			{"¿Tomaste el café? El día empieza mejor con cafeína.", 1.0},
			{"Tip del día: Ctrl+Z es tu mejor amigo.", 1.0},
			{"¿Todo funcionando? Buena señal.", 1.0},
			{"La semana avanza. Vos también.", 1.0},
			{"Chequea el espacio en disco de vez en cuando. Yo te recuerdo.", 1.0},
			{"¿Probaste apagarlo y prender de nuevo? (Clásico pero funciona)", 1.0},
			{"Frank v3.0 a tu disposición.", 1.0},
		},
	}

	proactiveTicker      *time.Ticker
	proactiveTickerMutex sync.Mutex
	proactiveTickerStop  chan struct{}

	// v3.0 nuevas variables
	agentStartTime       = time.Now()
	lastInventoryFile    string
	lastInventoryFileMu  sync.Mutex
	telemetry         = &TelemetryData{}
	debugMode         = os.Getenv("FRANK_DEBUG") == "1"
	loadedPlugins     []Plugin
	dynamicRules      []DynamicRule
	pendingEvents     []PendingEvent
	pendingEventsMu   sync.Mutex
	pendingEventsFile = "pending_events.enc"

	userProfile      UserProfile
	userProfileMutex sync.RWMutex
	userProfileFile  = "user_profile.enc"
	profileCompleted atomic.Bool
)

// ========================= INICIALIZACIÓN =========================
func init() {
	// Cargar configuración desde frank.env (si existe junto al ejecutable)
	// ANTES de leer env vars, para que OS env vars tengan precedencia.
	loadLocalConfig()

	gatewayURL  = getEnv("MICLAW_GATEWAY",  "http://192.168.1.246:3001")
	// Debe coincidir con MICLAW_AGENT_KEY del gateway.
	// Prioridad: OS env var → frank.env → "changeme"
	agentAPIKey = getEnv("AGENT_API_KEY", "changeme")
	// agentSecret debe ser estable entre reinicios para que los archivos
	// encriptados (settings.enc, user_profile.enc, etc.) se puedan leer.
	// Usamos el MachineGuid de Windows como base; si falla, el hostname.
	// AGENT_SECRET como variable de entorno permite override manual.
	agentSecret = getEnv("AGENT_SECRET", stableMachineSecret())
	httpListenAddr = getEnv("HTTP_LISTEN_ADDR", ":8081")
	// Clave P2P compartida: misma en todos los agentes Frank de la org.
	// Diferente a agentAPIKey (que es única por equipo) para separar acceso
	// externo (gateway) de acceso entre peers (LAN interna).
	p2pSharedKey = getEnv("FRANK_P2P_KEY", "frank-p2p-afe-2025")

	pcName, _ = os.Hostname()
	if runtime.GOOS == "windows" {
		currentUser = os.Getenv("USERNAME")
		domain = os.Getenv("USERDOMAIN")
	} else {
		currentUser = os.Getenv("USER")
	}
	if currentUser == "" {
		currentUser = "unknown"
	}

	os.MkdirAll("logs", 0755)
	logFile, _ = os.OpenFile("logs/agent.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	logger = log.New(io.MultiWriter(os.Stdout, logFile), "[FRANK] ", log.Ldate|log.Ltime|log.Lshortfile)
	logger.SetFlags(log.LstdFlags | log.Lshortfile)
	logger.Printf("[INFO] %s %s — %s", AgentName, AgentVersion, AgentAuthor)
	initializeNLU()
	setupGracefulShutdown()
}

// ========================= FUNCIONES AUXILIARES =========================
// ========================= HELPERS v3.0 =========================

// createNoWindow evita que los procesos hijos abran una ventana de consola.
// Se usa en combinación con HideWindow: true para máxima compatibilidad.
// 0x08000000 = CREATE_NO_WINDOW (Win32 API).
const createNoWindow = 0x08000000

// psRun ejecuta PowerShell completamente oculto y devuelve la salida (DRY).
// Usa tres capas de ocultamiento: -WindowStyle Hidden (PowerShell),
// HideWindow (STARTUPINFO SW_HIDE) y CREATE_NO_WINDOW (creación de proceso).
func psRun(cmd string) string {
	c := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", cmd)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	out, err := c.Output()
	if err != nil && debugMode {
		jsonLog("WARN", "psRun", fmt.Sprintf("cmd=%q err=%v", cmd, err))
	}
	return strings.TrimSpace(string(out))
}

// psRunCtx igual que psRun pero con contexto cancelable.
func psRunCtx(ctx context.Context, cmd string) string {
	c := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", cmd)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	out, err := c.Output()
	if err != nil && ctx.Err() == nil && debugMode {
		jsonLog("WARN", "psRunCtx", fmt.Sprintf("cmd=%q err=%v", cmd, err))
	}
	return strings.TrimSpace(string(out))
}

// hiddenCmd crea un exec.Cmd con consola completamente oculta.
// Usar para herramientas del sistema (netsh, sc, net, etc.) que no son PowerShell.
// NO usar para programas que el usuario quiere ver abiertos (Word, Chrome, etc.).
func hiddenCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return c
}

// confirmDo configura una confirmación pendiente (DRY)
func confirmDo(mem *Memory, action, desc string, onConfirm func(string) string, onDeny func() string) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            action,
		Description:       desc,
		ExpectedResponses: []string{"si", "sí", "yes", "dale", "ok", "claro"},
		OnConfirm:         onConfirm,
		OnDeny:            onDeny,
	}
	return desc
}

// reMatch1 retorna el primer grupo capturado o ""
func reMatch1(pattern, input string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(input)
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// telemetryInc incrementa un contador de telemetría
func telemetryInc(which string) {
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	switch which {
	case "commands":
		telemetry.CommandsExecuted++
	case "errors":
		telemetry.ErrorsCount++
	case "tickets":
		telemetry.TicketsCreated++
	}
}

// jsonLog escribe un log estructurado JSON
func jsonLog(level, action, message string) {
	entry := JSONLogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Level:     level,
		Action:    action,
		User:      currentUser,
		Message:   message,
	}
	data, _ := json.Marshal(entry)
	logger.Println(string(data))
}

// loadPlugins carga plugins JSON desde la carpeta /plugins
func loadPlugins() {
	pluginsDir := "plugins"
	files, err := os.ReadDir(pluginsDir)
	if err != nil {
		return
	}
	loadedPlugins = nil
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pluginsDir, f.Name()))
		if err != nil {
			continue
		}
		var p Plugin
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		if p.Intent == "" || p.Name == "" {
			continue
		}
		loadedPlugins = append(loadedPlugins, p)
		jsonLog("INFO", "plugin_load", fmt.Sprintf("Cargado plugin: %s (intent: %s)", p.Name, p.Intent))
	}
}

// matchPlugin busca si la entrada activa algún plugin
func matchPlugin(input string) *Plugin {
	lower := strings.ToLower(input)
	for i := range loadedPlugins {
		for _, kw := range loadedPlugins[i].Keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				return &loadedPlugins[i]
			}
		}
	}
	return nil
}

// executePlugin ejecuta un plugin
func executePlugin(p *Plugin, input string) string {
	if p.Command != "" {
		out, err := exec.Command("cmd", "/c", p.Command).Output()
		if err != nil {
			return fmt.Sprintf("❌ Plugin '%s' falló: %v", p.Name, err)
		}
		return fmt.Sprintf("🔌 [%s]\n%s", p.Name, strings.TrimSpace(string(out)))
	}
	if p.Response != "" {
		return fmt.Sprintf("🔌 [%s] %s", p.Name, p.Response)
	}
	return fmt.Sprintf("🔌 Plugin '%s' ejecutado.", p.Name)
}

// loadDynamicRules carga reglas JSON desde rules.json
func loadDynamicRules() {
	data, err := os.ReadFile("rules.json")
	if err != nil {
		return
	}
	var rules []DynamicRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Priority > rules[j].Priority })
	// dynamicRulesMu protege dynamicRules frente a reloadDynamicRules (refactor.go)
	// que puede ejecutarse en background desde GatewaySync.
	dynamicRulesMu.Lock()
	dynamicRules = rules
	dynamicRulesMu.Unlock()
	jsonLog("INFO", "rules_load", fmt.Sprintf("Cargadas %d reglas dinámicas", len(rules)))
}

// matchDynamicRule evalúa reglas dinámicas antes del NLU.
// Lee dynamicRules bajo dynamicRulesMu (declarado en refactor.go) para
// evitar data races con el hot-reload de GatewaySync.
func matchDynamicRule(input string, mem *Memory) string {
	lower := strings.ToLower(input)
	dynamicRulesMu.RLock()
	rules := make([]DynamicRule, len(dynamicRules))
	copy(rules, dynamicRules)
	dynamicRulesMu.RUnlock()
	for _, rule := range rules {
		matched := false
		for _, kw := range rule.Keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if rule.Context != "" {
			mem.mu.RLock()
			topic := mem.LastTopic
			mem.mu.RUnlock()
			if !strings.Contains(strings.ToLower(topic), strings.ToLower(rule.Context)) {
				continue
			}
		}
		if rule.Response != "" {
			return rule.Response
		}
		if rule.Action != "" {
			if fn, ok := actionMap[rule.Action]; ok {
				return fn(input, mem)
			}
		}
	}
	return ""
}

// savePendingEvents persiste la cola offline
func savePendingEvents() {
	pendingEventsMu.Lock()
	defer pendingEventsMu.Unlock()
	data, err := json.MarshalIndent(pendingEvents, "", "  ")
	if err != nil {
		logger.Printf("[ERROR] savePendingEvents: marshal falló: %v", err)
		return
	}
	enc, err := protectData(data)
	if err != nil {
		logger.Printf("[ERROR] savePendingEvents: cifrado falló: %v", err)
		return
	}
	if err := os.WriteFile(pendingEventsFile, enc, 0600); err != nil {
		logger.Printf("[ERROR] savePendingEvents: escritura falló: %v", err)
	}
}

// loadPendingEvents carga la cola offline
func loadPendingEvents() {
	data, err := os.ReadFile(pendingEventsFile)
	if err != nil {
		return
	}
	plain, err := unprotectData(data)
	if err != nil {
		return
	}
	if err := json.Unmarshal(plain, &pendingEvents); err != nil {
		logger.Printf("[ERROR] loadPendingEvents: unmarshal falló: %v", err)
	}
}

// queueEvent encola un evento para envío posterior
func queueEvent(evType, payload string) {
	pendingEventsMu.Lock()
	pendingEvents = append(pendingEvents, PendingEvent{
		ID:        uuid.New().String(),
		Timestamp: time.Now(),
		Type:      evType,
		Payload:   payload,
		Retries:   0,
	})
	pendingEventsMu.Unlock()
	savePendingEvents()
}

// retryPendingEvents reintenta enviar eventos con backoff exponencial
func retryPendingEvents(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pendingEventsMu.Lock()
				if len(pendingEvents) == 0 {
					pendingEventsMu.Unlock()
					continue
				}
				var remaining []PendingEvent
				for _, ev := range pendingEvents {
					if ev.Retries >= 5 {
						jsonLog("WARN", "queue_drop", fmt.Sprintf("Evento %s descartado tras 5 intentos", ev.ID))
						continue
					}
					backoff := time.Duration(1<<uint(ev.Retries)) * time.Second
					if time.Since(ev.Timestamp) < backoff {
						remaining = append(remaining, ev)
						continue
					}
					sent := trySendEvent(ev)
					if !sent {
						ev.Retries++
						remaining = append(remaining, ev)
					} else {
						jsonLog("INFO", "queue_sent", fmt.Sprintf("Evento %s enviado", ev.ID))
					}
				}
				pendingEvents = remaining
				pendingEventsMu.Unlock()
				savePendingEvents()
			}
		}
	}()
}

func trySendEvent(ev PendingEvent) bool {
	if gatewayURL == "" {
		return false
	}
	payload, _ := json.Marshal(ev)
	resp, err := http.Post(gatewayURL+"/events", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadLocalConfig carga variables de entorno desde frank.env ubicado junto al ejecutable.
// Formato: clave=valor por línea, comentarios con #.
// Las variables ya presentes en el entorno del SO tienen precedencia.
// Esto permite distribuir un frank.env con cada instalación del agente
// sin depender de variables de entorno del sistema operativo.
func loadLocalConfig() {
	// Buscar frank.env junto al ejecutable
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	cfgPath := filepath.Join(filepath.Dir(exePath), "frank.env")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		// Intentar también en el directorio de trabajo actual
		data, err = os.ReadFile("frank.env")
		if err != nil {
			return // no existe, usar defaults
		}
	}

	loaded := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// Eliminar comillas opcionales del valor
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		// Solo setear si NO está ya en el entorno del OS
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
			loaded++
		}
	}
	if loaded > 0 {
		log.Printf("[FRANK] Configuración cargada desde frank.env (%d variables)", loaded)
	}
}

func generateRandomKey(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)[:length]
}

// stableMachineSecret devuelve un secreto derivado del MachineGuid de Windows.
// Es estable entre reinicios en el mismo equipo, lo que permite que los archivos
// encriptados (settings.enc, user_profile.enc, etc.) sean legibles en cada inicio.
func stableMachineSecret() string {
	// Intentar leer el MachineGuid del registro de Windows.
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if guid, _, err2 := k.GetStringValue("MachineGuid"); err2 == nil && guid != "" {
			// Mezclar con una sal fija para que no sea el GUID en crudo.
			h := sha256.Sum256([]byte("frank-v2-" + guid))
			return base64.URLEncoding.EncodeToString(h[:])
		}
	}
	// Fallback: hostname (menos único, pero mejor que aleatorio).
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "frank-default-host"
	}
	h := sha256.Sum256([]byte("frank-v2-" + hostname))
	return base64.URLEncoding.EncodeToString(h[:])
}

// ========================= INSTANCIA ÚNICA =========================

// acquireLockFile crea/abre un archivo de lock en installDir con el PID actual.
// Si el archivo existe y el PID referenciado sigue vivo, devuelve false.
// Es un mecanismo de respaldo al mutex de Windows — más visible y portátil.
func acquireLockFile() bool {
	lockPath := filepath.Join(installDir, lockFileName)
	// Si el directorio no existe aún (primera ejecución fuera de Program Files),
	// no bloqueamos por lock file — el mutex de Windows es suficiente.
	if _, err := os.Stat(installDir); os.IsNotExist(err) {
		return true
	}
	// Leer lock existente
	if data, err := os.ReadFile(lockPath); err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidStr); err == nil && pid != os.Getpid() {
			// Verificar si el proceso sigue vivo usando OpenProcess
			h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
			if err == nil {
				// El proceso existe — otra instancia está corriendo.
				windows.CloseHandle(h)
				return false
			}
			// Proceso muerto — lock stale, continuar.
			logger.Printf("[INFO] lock file stale (PID %d ya no existe), reclamando", pid)
		}
	}
	// Escribir nuestro PID
	_ = os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())), 0600)
	return true
}

// releaseLockFile elimina el archivo de lock del proceso actual.
func releaseLockFile() {
	lockPath := filepath.Join(installDir, lockFileName)
	// Solo eliminar si el lock es nuestro.
	if data, err := os.ReadFile(lockPath); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid == os.Getpid() {
			os.Remove(lockPath)
		}
	}
}

// ensureSingleInstance crea un mutex nombrado de Windows para garantizar que
// solo una instancia de Frank se ejecuta a la vez.
// Devuelve el handle del mutex (debe mantenerse abierto durante toda la vida del
// proceso) y true si esta es la primera instancia.
// Si ya existe otra instancia, devuelve (0, false).
func ensureSingleInstance() (windows.Handle, bool) {
	namePtr, err := windows.UTF16PtrFromString(singleInstanceMutexName)
	if err != nil {
		logger.Printf("[WARN] ensureSingleInstance: UTF16PtrFromString: %v", err)
		return 0, true // permitir arranque ante fallo inesperado
	}
	h, err := windows.CreateMutex(nil, false, namePtr)
	if err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			if h != 0 {
				windows.CloseHandle(h)
			}
			return 0, false // otra instancia está corriendo
		}
		logger.Printf("[WARN] ensureSingleInstance: CreateMutex: %v", err)
		return 0, true // permitir arranque ante fallo inesperado
	}
	return h, true
}

// ========================= MODO PORTABLE / AUTO-INSTALACIÓN =========================

// selfInstallIfNeeded comprueba si el ejecutable corre desde installDir.
// Si no, intenta copiarse allí. Si falla por permisos, se relanza elevado (UAC)
// para que el proceso con privilegios realice la instalación.
// Devuelve true si el proceso actual debe salir (instalación en curso o relanzado).
func selfInstallIfNeeded() bool {
	exe, err := os.Executable()
	if err != nil {
		logger.Printf("[ERROR] selfInstall: os.Executable: %v", err)
		return false
	}
	exe, _ = filepath.EvalSymlinks(exe)
	exe = filepath.Clean(exe)

	installPath := filepath.Clean(filepath.Join(installDir, installExeName))

	// ¿Ya corremos desde el directorio de instalación?
	if strings.EqualFold(exe, installPath) {
		return false
	}

	logger.Printf("[INFO] selfInstall: ejecutable en %s, instalando en %s", exe, installDir)

	// Intentar crear el directorio. Si falla por permisos, solicitar elevación UAC.
	if err := os.MkdirAll(installDir, 0755); err != nil {
		logger.Printf("[INFO] selfInstall: sin permisos de admin, solicitando elevación UAC: %v", err)
		if relaunchElevated(exe) {
			return true // el proceso elevado hará la instalación
		}
		// Si la elevación falló (usuario canceló UAC), continuar desde ubicación actual.
		logger.Printf("[WARN] selfInstall: elevación cancelada, continuando desde %s", exe)
		return false
	}

	// Copiar el ejecutable.
	if err := copyFile(exe, installPath); err != nil {
		logger.Printf("[WARN] selfInstall: no se pudo copiar el binario: %v", err)
		return false
	}
	logger.Printf("[INFO] selfInstall: binario copiado a %s", installPath)

	// Copiar todos los archivos de datos al directorio de instalación.
	srcDir := filepath.Dir(exe)
	copyInstallDataFiles(srcDir, installDir)

	// Crear acceso directo en el escritorio del usuario.
	createDesktopShortcut(installPath)

	// Relanzar desde la ubicación instalada con el directorio de trabajo correcto.
	cmd := exec.Command(installPath)
	cmd.Dir = installDir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	if err := cmd.Start(); err != nil {
		logger.Printf("[ERROR] selfInstall: no se pudo relanzar %s: %v", installPath, err)
		return false
	}

	logger.Printf("[INFO] selfInstall: relanzando desde %s, cerrando instancia actual", installPath)
	return true
}

// copyInstallDataFiles copia los archivos de datos de Frank desde srcDir a dstDir.
// Copia archivos .enc, bases de datos SQLite, rules.json y el directorio plugins/.
// Los errores individuales se loguean pero no detienen el proceso.
func copyInstallDataFiles(srcDir, dstDir string) {
	// Archivos individuales conocidos.
	dataFiles := []string{
		"settings.enc",
		"user_profile.enc",
		"chat_history.enc",
		"knowledge.enc",
		"actions_log.enc",
		"agent_queue.db",
		"agent_queue.db-shm",
		"agent_queue.db-wal",
		"rules.json",
	}
	for _, name := range dataFiles {
		src := filepath.Join(srcDir, name)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue // el archivo todavía no existe, omitir
		}
		dst := filepath.Join(dstDir, name)
		if err := copyFile(src, dst); err != nil {
			logger.Printf("[WARN] selfInstall: no se pudo copiar %s: %v", name, err)
		} else {
			logger.Printf("[INFO] selfInstall: copiado %s", name)
		}
	}

	// Directorio plugins/ (si existe).
	srcPlugins := filepath.Join(srcDir, "plugins")
	if info, err := os.Stat(srcPlugins); err == nil && info.IsDir() {
		dstPlugins := filepath.Join(dstDir, "plugins")
		if err := os.MkdirAll(dstPlugins, 0755); err != nil {
			logger.Printf("[WARN] selfInstall: no se pudo crear plugins/: %v", err)
			return
		}
		entries, _ := os.ReadDir(srcPlugins)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			src := filepath.Join(srcPlugins, entry.Name())
			dst := filepath.Join(dstPlugins, entry.Name())
			if err := copyFile(src, dst); err != nil {
				logger.Printf("[WARN] selfInstall: plugins/%s: %v", entry.Name(), err)
			} else {
				logger.Printf("[INFO] selfInstall: copiado plugins/%s", entry.Name())
			}
		}
	}
}

// relaunchElevated relanza el mismo ejecutable con privilegios de administrador
// usando el verbo "runas" de ShellExecuteW. Devuelve true si el proceso elevado
// se inició correctamente (el llamante debe salir).
func relaunchElevated(exe string) bool {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	shellExecuteW := shell32.NewProc("ShellExecuteW")

	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)

	r, _, _ := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0,
		0,
		1, // SW_NORMAL
	)
	// ShellExecuteW devuelve > 32 si tuvo éxito.
	if r > 32 {
		logger.Printf("[INFO] selfInstall: proceso elevado iniciado correctamente")
		return true
	}
	logger.Printf("[WARN] selfInstall: ShellExecuteW retornó %d (UAC cancelado o error)", r)
	return false
}

// copyFile copia src a dst (sobrescribe si existe).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("abrir origen: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("crear destino: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copiar datos: %w", err)
	}
	return out.Sync()
}

// createDesktopShortcut crea un acceso directo (.lnk) en el escritorio del
// usuario actual apuntando a targetPath. Usa PowerShell/WScript.Shell.
func createDesktopShortcut(targetPath string) {
	userProfile := os.Getenv("USERPROFILE")
	if userProfile == "" {
		logger.Println("[WARN] createDesktopShortcut: USERPROFILE no definido")
		return
	}
	shortcutPath := filepath.Join(userProfile, "Desktop", "Frank.lnk")
	workDir := filepath.Dir(targetPath)

	// Usar comillas dobles dentro del script para evitar conflictos con
	// rutas que pudieran contener caracteres especiales.
	script := fmt.Sprintf(
		`$ws = New-Object -ComObject WScript.Shell; `+
			`$sc = $ws.CreateShortcut("%s"); `+
			`$sc.TargetPath = "%s"; `+
			`$sc.WorkingDirectory = "%s"; `+
			`$sc.Description = "Asistente IT Frank - AFE"; `+
			`$sc.Save()`,
		shortcutPath, targetPath, workDir,
	)

	c := exec.Command("powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script,
	)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := c.Run(); err != nil {
		logger.Printf("[WARN] createDesktopShortcut: %v", err)
	} else {
		logger.Printf("[INFO] createDesktopShortcut: acceso directo en %s", shortcutPath)
	}
}

func deriveKey(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

func protectData(data []byte) ([]byte, error) {
	key := deriveKey(agentSecret)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, nil), nil
}

func unprotectData(encrypted []byte) ([]byte, error) {
	key := deriveKey(agentSecret)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(encrypted) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ciphertext := encrypted[:nonceSize], encrypted[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func isAdmin() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	token := windows.Token(0)
	member, err := token.IsMember(sid)
	return err == nil && member
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeAccents(s string) string {
	replacer := strings.NewReplacer(
		"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u",
		"Á", "A", "É", "E", "Í", "I", "Ó", "O", "Ú", "U",
		"ü", "u", "Ü", "U", "ñ", "n", "Ñ", "N",
	)
	return replacer.Replace(s)
}

func jaccardSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0.0
	}
	setA := make(map[rune]bool)
	for _, r := range a {
		setA[r] = true
	}
	setB := make(map[rune]bool)
	for _, r := range b {
		setB[r] = true
	}
	intersection := 0
	for r := range setA {
		if setB[r] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0.0
	}
	return float64(intersection) / float64(union)
}

// ========================= NLU DE 3 CAPAS =========================
func initializeNLU() {
	// Capa 1: Mapa de palabras clave -> intención (coincidencia directa)
	// Nota: Este mapa se usará en classifyIntent antes del modelo estadístico.

	// Capa 2: Entrenamiento del modelo Naive Bayes
	intentExamples = map[string][]string{
		"problema_zimbra": {
			"no me anda el correo", "no puedo entrar al correo", "no abre el webmail",
			"mail afe no funciona", "zimbra no carga", "correo electronico no funciona",
		},
		"problema_usb": {
			"mi pendrive no funciona", "no detecta el pendrive", "no reconoce usb",
			"puerto usb no anda", "memoria usb no aparece",
		},
		"problema_mouse": {
			"el mouse no funciona", "cursor congelado", "mouse no se mueve",
		},
		"problema_teclado": {
			"el teclado no escribe", "teclado no responde", "teclas no funcionan",
		},
		"problema_impresora": {
			"la impresora no imprime", "impresora offline", "impresora atascada",
		},
		"problema_internet": {
			"no tengo internet", "wifi no conecta", "red caida",
		},
		"problema_pc_lenta": {
			"pc lenta", "se congela la pc", "computadora muy lenta",
		},
		"problema_pantalla_azul": {
			"pantalla azul", "pantalla congelada", "error azul", "bsod",
		},
		"problema_video": {
			"no da video", "monitor sin señal", "pantalla negra",
		},
		"problema_audio": {
			"no se escucha audio", "parlantes no funcionan", "sin sonido",
		},
		"problema_microfono": {
			"microfono no funciona", "no me escuchan",
		},
		"problema_apagado": {
			"la pc se apaga sola", "reinicio inesperado",
		},
		"problema_bateria": {
			"bateria no carga", "cargador no funciona",
		},
		"solicitud_it_local": {
			"necesito un pendrive", "requiero un pendrive", "necesito un mouse",
			"necesito un teclado", "necesito un monitor", "necesito una pc",
			"necesito una computadora", "necesito una fuente", "necesito un cable",
			"necesito un adaptador",
			// variantes con solicitar / quiero / pedir
			"solicitar un mouse", "quiero un mouse", "pedir un mouse",
			"solicitar un teclado", "quiero un teclado", "pedir un teclado",
			"solicitar un monitor", "quiero un monitor",
			"solicitar un pendrive", "pedir un pendrive",
			"solicitar una computadora", "quiero una pc", "necesito una notebook",
			"me falta un mouse", "me falta un teclado",
			"me hace falta un mouse", "me hace falta un teclado",
			"quisiera solicitar un mouse", "quisiera un teclado",
			"preciso un mouse", "preciso un teclado",
		},
		"solicitud_almacen": {
			"necesito toner", "me falta papel", "necesito una impresora",
			"necesito un disco duro", "necesito memoria ram", "requiero toner",
			"solicitar toner", "quiero papel", "pedir cartuchos",
			"solicitar memoria ram", "necesito un cartucho",
		},
		// Tickets
		"ver_tickets": {
			"ver mis tickets", "mostrar tickets", "ver historial de tickets",
			"que tickets tengo", "listar tickets", "mis solicitudes de soporte",
			"ver todas las solicitudes", "tickets generados",
		},
		"ver_tickets_abiertos": {
			"tickets abiertos", "que tickets tengo pendientes", "solicitudes sin resolver",
			"hay algún ticket abierto", "ver tickets pendientes", "tickets en curso",
			"solicitudes activas", "cuales son mis tickets abiertos",
		},
		"ver_tickets_cerrados": {
			"tickets cerrados", "tickets resueltos", "historial de solicitudes cerradas",
			"solicitudes completadas", "ver tickets resueltos", "historial soporte",
		},
		"nuevo_ticket": {
			"abrir ticket", "crear ticket", "nuevo ticket", "quiero abrir una solicitud",
			"generar un ticket de soporte", "reportar un problema formal",
		},
		"buscar_archivo": {
			"busca factura.pdf", "buscar contrato", "encuentra el archivo",
		},
		"abrir_programa": {
			"abre word", "abrir excel", "abre chrome", "iniciar calculadora",
		},
		"calcular": {
			"calcula 12*8", "cuanto es 5+3", "multiplica 7 por 9",
		},
		"corregir_hora": {
			"corrige la hora", "hora incorrecta", "actualizar hora",
		},
		"desbloquear_usuario": {
			"desbloquea usuario", "reactiva cuenta", "activa usuario",
		},
		"cambiar_password": {
			"cambia contraseña de", "cambiar password", "resetear clave",
		},
		"crear_usuario": {
			"crea usuario", "crear cuenta", "nuevo usuario",
		},
		"eliminar_usuario": {
			"eliminar usuario", "borrar cuenta",
		},
		"listar_usuarios": {
			"listar usuarios", "ver usuarios",
		},
		"grupos_usuario": {
			"grupos del usuario", "a que grupos pertenece",
		},
		"agregar_grupo": {
			"agregar a grupo", "añadir a grupo",
		},
		"problema_disco_lleno": {
			"disco lleno", "poco espacio", "no hay espacio",
		},
		"consultar_espacio": {
			"cuanto espacio tengo", "espacio libre", "ver capacidad disco",
		},
		"liberar_espacio": {
			"liberar espacio", "limpiar disco", "borrar temporales",
		},
		"buscar_archivos_grandes": {
			"archivos grandes", "buscar archivos pesados",
		},
		"analizar_disco": {
			"analizar disco", "chkdsk",
		},
		"desfragmentar": {
			"desfragmentar", "defrag",
		},
		"formatear": {
			"formatear disco", "formatear unidad",
		},
		"escanear_virus": {
			"analizar virus", "windows defender", "escanear pc",
		},
		"problema_firewall": {
			"firewall bloquea", "cortafuegos no deja",
		},
		"reparar_permisos": {
			"permisos de carpeta", "reparar permisos",
		},
		"bitlocker": {
			"bitlocker", "cifrar disco",
		},
		"limpiar_cola_impresion": {
			"limpiar cola de impresión", "trabajos atascados",
		},
		"problema_spooler": {
			"spooler detenido", "reiniciar spooler",
		},
		"agregar_impresora": {
			"agregar impresora en red", "instalar impresora",
		},
		"compartir_impresora": {
			"compartir impresora",
		},
		"impresora_predeterminada": {
			"impresora predeterminada", "establecer impresora por defecto",
		},
		"listar_impresoras": {
			"ver impresoras", "listar impresoras",
		},
		"diagnostico_proxy": {
			"proxy no funciona", "diagnostico proxy",
		},
		"flush_dns": {
			"dns no resuelve", "flush dns", "vaciar dns",
		},
		"reset_red": {
			"reiniciar red", "resetear red",
		},
		"cambiar_dns": {
			"cambiar dns", "poner dns",
		},
		"ver_wifi_password": {
			"wifi contraseña", "clave wifi",
		},
		"conexiones_activas": {
			"conexiones activas", "netstat",
		},
		"ping": {
			"ping a", "hacer ping",
		},
		"tracert": {
			"tracert a", "traceroute",
		},
		"buscar_actualizaciones": {
			"actualizaciones windows", "instalar actualizaciones",
		},
		"reparar_sistema": {
			"reparar sistema", "sfc scannow",
		},
		"comprobar_disco": {
			"chkdsk", "errores de disco",
		},
		"diagnostico_ram": {
			"memoria ram", "diagnostico de ram",
		},
		"gestionar_inicio": {
			"programas inicio", "programas que arrancan",
		},
		"info_sistema": {
			"informacion del sistema", "versión windows", "que pc tengo",
		},
		"variables_entorno": {
			"variables de entorno", "env",
		},
		"regedit": {
			"registro de windows", "regedit",
		},
		"gpupdate": {
			"politicas de grupo", "gpupdate",
		},
		"tareas_programadas": {
			"tareas programadas", "scheduled tasks",
		},
		"listar_servicios": {
			"servicios", "listar servicios",
		},
		"listar_drivers": {
			"drivers", "listar drivers",
		},
		"restaurar_driver": {
			"restaurar driver", "rollback driver",
		},
		"mantenimiento": {
			"limpiar pc", "optimizar", "hacer mantenimiento",
		},
		"informe_bateria": {
			"informe de bateria", "battery report",
		},
		"informe_energia": {
			"informe de energia", "energy report",
		},
		"listar_procesos": {
			"listar procesos", "ver procesos",
		},
		"finalizar_proceso": {
			"finalizar proceso", "matar proceso",
		},
		"iniciar_servicio": {
			"iniciar servicio", "arrancar servicio",
		},
		"detener_servicio": {
			"detener servicio", "parar servicio",
		},
		"reiniciar_servicio": {
			"reiniciar servicio",
		},
		"cambiar_inicio_servicio": {
			"cambiar tipo de inicio de servicio",
		},
		"listar_programas": {
			"listar programas instalados",
		},
		"desinstalar_programa": {
			"desinstalar programa",
		},
		"instalar_programa": {
			"instalar programa",
		},
		"crear_restore_point": {
			"crear punto de restauracion",
		},
		"restaurar_sistema": {
			"restaurar sistema",
		},
		"auditoria": {
			"ver registro de acciones", "que hiciste", "historial de acciones",
		},
		"rollback": {
			"deshacer ultima accion", "revertir cambio", "volver atras",
		},
		"configurar_outlook": {
			"configurar correo en outlook",
		},
		"temperatura": {
			"temperatura de la pc", "cpu temp",
		},
		"smart_disco": {
			"salud del disco", "smart disco",
		},
		"rdp": {
			"activar escritorio remoto", "rdp",
		},
		"compartir_carpeta": {
			"compartir carpeta", "compartir archivo",
		},
		"bloquear_pc": {
			"bloquear pc", "lock workstation",
		},
		"apagar": {
			"apagar equipo", "shutdown",
		},
		"reiniciar": {
			"reiniciar pc", "reboot",
		},
		"suspender": {
			"suspender", "sleep",
		},
		"uptime": {
			"hora de inicio", "cuanto lleva encendida",
		},
		"ip_address": {
			"ver ip", "direccion ip", "cual es mi ip",
		},
		"abrir_puerto": {
			"abrir puerto", "abrir puerto firewall",
		},
		"cerrar_puerto": {
			"cerrar puerto", "cerrar puerto firewall",
		},
		"ayuda": {
			"ayuda", "help", "que puedes hacer", "comandos",
		},
	}

	var allInputs [][]string
	var allOutputs [][]string
	intentLabels = []string{}

	for intent, examples := range intentExamples {
		intentLabels = append(intentLabels, intent)
		for _, ex := range examples {
			tokens := preprocessText(ex)
			if len(tokens) > 0 {
				allInputs = append(allInputs, tokens)
				allOutputs = append(allOutputs, []string{intent})
			}
		}
	}

	// Mergear intents adicionales de actions_extra.go
	for k, v := range extraIntentExamples {
		intentExamples[k] = v
		intentLabels = append(intentLabels, k)
		for _, ex := range v {
			tokens := preprocessText(ex)
			if len(tokens) > 0 {
				allInputs = append(allInputs, tokens)
				allOutputs = append(allOutputs, []string{k})
			}
		}
	}

	if len(allInputs) > 0 {
		nbModel = newBernoulliNB(allInputs, allOutputs, 1.0)
		logger.Printf("[INFO] Modelo Naive Bayes entrenado con %d ejemplos (%d intents)",
			len(allInputs), len(intentLabels))
	}

	allPhrases := []string{}
	for _, examples := range intentExamples {
		allPhrases = append(allPhrases, examples...)
	}
	bagSizes := []int{2, 3}
	closestMatcher = closestmatch.New(allPhrases, bagSizes)
	logger.Printf("[INFO] ClosestMatch inicializado con %d frases", len(allPhrases))
}

func preprocessText(text string) []string {
	text = strings.ToLower(text)
	text = removeAccents(text)
	re := regexp.MustCompile(`[^\p{L}\p{N}]+`)
	words := strings.Fields(re.ReplaceAllString(text, " "))
	var tokens []string
	for _, word := range words {
		if len(word) <= 1 || isSpanishStopWord(word) {
			continue
		}
		stemmed := spanish.Stem(word, false)
		if len(stemmed) > 0 {
			tokens = append(tokens, stemmed)
		} else {
			tokens = append(tokens, word)
		}
	}
	return tokens
}

// kwContains es helper para simplificar múltiples Contains
func kwContains(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func kwAll(s string, words ...string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// Capa 1: Keyword matching directo — 120+ patrones
func keywordMatch(input string) string {
	lower := strings.ToLower(removeAccents(input))
	has := func(w ...string) bool { return kwContains(lower, w...) }
	all := func(w ...string) bool { return kwAll(lower, w...) }

	switch {
	// ——— IP ———
	case has("cual es mi ip", "dame la ip", "dame mi ip", "que ip tengo", "ver ip", "mostrar ip", "mi direccion ip", "mi ip", "dirección ip"):
		return "ip_address"
	case all("ip") && !has("abrir", "cerrar", "puerto"):
		return "ip_address"

	// ——— PC lenta ———
	case has("pc lenta", "computadora lenta", "laptop lenta", "va lento", "anda lento", "anda lenta",
		"tarda mucho", "se traba", "se cuelga", "muy lenta", "super lenta", "demasiado lenta",
		"va muy lento", "esta lenta", "lentisima", "funciona lento", "computador lento"):
		return "problema_pc_lenta"

	// ——— Abrir programas ———
	case has("abre ", "abrir ", "abri ", "ejecuta ", "lanzar ", "iniciar ", "inicia ") &&
		has("word", "excel", "chrome", "firefox", "notepad", "bloc", "calculadora", "calc", "powershell",
			"cmd", "terminal", "paint", "outlook", "teams", "zoom", "acrobat", "pdf", "explorer",
			"archivos", "vlc", "winrar", "7zip", "notepad++", "edge", "brave", "opera", "code", "vscode"):
		return "abrir_programa"

	// ——— Calcular ———
	case has("calcula", "calculá", "cuanto es", "cuánto es", "cuanto da", "resuelve", "dame el resultado",
		"raiz de", "raíz de", "al cuadrado", "elevado a", "porcentaje", "seno de", "coseno de",
		"tangente de", "logaritmo", "log de", "potencia", "multiplicá", "multiplica"):
		return "calcular"

	// ——— Correo Zimbra ———
	case has("correo", "mail", "zimbra", "email", "webmail") &&
		has("no", "falla", "error", "problema", "no anda", "no abre", "no carga", "no funciona"):
		return "problema_zimbra"
	case has("configurar correo", "configurar outlook", "configurar zimbra", "agregar cuenta"):
		return "configurar_outlook"

	// ——— Disco ———
	case has("disco lleno", "sin espacio", "poco espacio", "no hay espacio", "falta espacio", "espacio agotado"):
		return "problema_disco_lleno"
	case has("cuanto espacio", "espacio libre", "espacio disponible", "ver capacidad", "cuanto queda"):
		return "consultar_espacio"
	case has("liberar espacio", "limpiar disco", "borrar temporales", "limpiar temporales", "limpiar cache"):
		return "liberar_espacio"
	case has("archivos grandes", "archivos pesados", "que ocupa espacio"):
		return "buscar_archivos_grandes"
	case has("analizar disco", "chkdsk", "errores de disco", "reparar disco"):
		return "comprobar_disco"
	case has("desfragmentar", "defrag", "optimizar disco"):
		return "desfragmentar"
	case has("formatear"):
		return "formatear"
	case has("smart disco", "salud del disco", "estado del disco", "disco sano"):
		return "smart_disco"

	// ——— Usuarios ———
	case has("desbloquea", "reactiva", "desbloquear") && has("usuario", "cuenta", "user"):
		return "desbloquear_usuario"
	case has("cambia contraseña", "cambiar contraseña", "resetear clave", "reset password", "nueva clave"):
		return "cambiar_password"
	case has("crea usuario", "crear usuario", "nuevo usuario", "agregar usuario", "añadir usuario"):
		return "crear_usuario"
	case has("eliminar usuario", "borrar usuario", "borrar cuenta", "eliminar cuenta"):
		return "eliminar_usuario"
	case has("listar usuarios", "ver usuarios", "lista de usuarios", "usuarios del sistema"):
		return "listar_usuarios"
	case has("grupos del usuario", "que grupos tiene", "a que grupo pertenece"):
		return "grupos_usuario"
	case has("agregar a grupo", "anadir a grupo", "añadir al grupo"):
		return "agregar_grupo"

	// ——— Red ———
	case has("no tengo internet", "sin internet", "sin red", "red caida", "internet no anda",
		"no hay internet", "wifi no conecta", "no se conecta", "perdí la red"):
		return "problema_internet"
	case has("flush dns", "vaciar dns", "limpiar dns", "limpiar cache dns"):
		return "flush_dns"
	case has("reiniciar red", "resetear red", "reiniciar pila", "reset tcp", "winsock"):
		return "reset_red"
	case has("cambiar dns", "poner dns", "configurar dns"):
		return "cambiar_dns"
	case has("wifi") && has("contraseña", "clave", "password"):
		return "ver_wifi_password"
	case has("conexiones activas", "netstat", "puertos abiertos", "que conexiones tengo"):
		return "conexiones_activas"
	case has("ping "):
		return "ping"
	case has("tracert", "traceroute"):
		return "tracert"
	case has("proxy"):
		return "diagnostico_proxy"
	case has("escritorio remoto", "rdp", "acceso remoto", "remote desktop"):
		return "rdp"
	case has("compartir carpeta", "compartir una carpeta"):
		return "compartir_carpeta"

	// ——— Impresoras ———
	case has("impresora") && has("no imprime", "no funciona", "error", "problema", "offline"):
		return "problema_impresora"
	case has("impresora") && has("cola", "trabajos atascados", "limpia"):
		return "limpiar_cola_impresion"
	case has("spooler"):
		return "problema_spooler"
	case has("agregar impresora", "instalar impresora", "nueva impresora"):
		return "agregar_impresora"
	case has("compartir impresora"):
		return "compartir_impresora"
	case has("impresora predeterminada", "impresora por defecto"):
		return "impresora_predeterminada"
	case has("listar impresoras", "ver impresoras", "que impresoras"):
		return "listar_impresoras"

	// ——— Sistema ———
	case has("informacion del sistema", "info del sistema", "que pc tengo", "version windows",
		"que windows tengo", "cual es mi sistema", "datos del sistema"):
		return "info_sistema"
	case has("temperatura", "cpu temp", "cuanto calienta", "sobrecalentamiento"):
		return "temperatura"
	case has("variables de entorno", "variables del sistema", "env vars"):
		return "variables_entorno"
	case has("registro de windows", "regedit", "editor de registro"):
		return "regedit"
	case has("gpupdate", "politicas de grupo", "actualizar politicas", "group policy"):
		return "gpupdate"
	case has("tareas programadas", "scheduled tasks", "tarea programada"):
		return "tareas_programadas"
	case has("listar servicios", "ver servicios", "servicios del sistema"):
		return "listar_servicios"
	case has("listar drivers", "ver drivers", "controladores", "listar controladores"):
		return "listar_drivers"
	case has("restaurar driver", "revertir driver", "rollback driver"):
		return "restaurar_driver"
	case has("actualizaciones", "windows update", "instalar actualizaciones", "buscar actualizaciones"):
		return "buscar_actualizaciones"
	case has("sfc", "reparar sistema", "reparar archivos", "system file checker"):
		return "reparar_sistema"
	case has("punto de restauracion", "restore point", "crear restauracion"):
		return "crear_restore_point"
	case has("restaurar sistema", "system restore", "volver al estado anterior"):
		return "restaurar_sistema"

	// ——— Procesos/Servicios ———
	case has("listar procesos", "ver procesos", "que procesos", "que esta corriendo"):
		return "listar_procesos"
	case has("finalizar proceso", "matar proceso", "cerrar proceso", "kill proceso"):
		return "finalizar_proceso"
	case has("iniciar servicio", "arrancar servicio", "start servicio"):
		return "iniciar_servicio"
	case has("detener servicio", "parar servicio", "stop servicio"):
		return "detener_servicio"
	case has("reiniciar servicio", "restart servicio"):
		return "reiniciar_servicio"
	case has("cambiar inicio") && has("servicio"):
		return "cambiar_inicio_servicio"

	// ——— Programas instalados ———
	case has("programas instalados", "listar programas", "ver programas", "software instalado"):
		return "listar_programas"
	case has("desinstalar programa", "quitar programa", "eliminar programa"):
		return "desinstalar_programa"
	case has("instalar programa", "ejecutar instalador", "instalar msi"):
		return "instalar_programa"

	// ——— Hardware ———
	case has("usb", "pendrive", "memoria usb") && has("no detecta", "no reconoce", "no funciona", "no anda"):
		return "problema_usb"
	case has("mouse", "raton", "ratón") && has("no funciona", "no se mueve", "congelado", "no anda"):
		return "problema_mouse"
	case has("teclado") && has("no funciona", "no escribe", "no responde", "no anda"):
		return "problema_teclado"
	case has("pantalla azul", "bsod", "error azul", "pantalla de la muerte"):
		return "problema_pantalla_azul"
	case has("no da video", "sin video", "monitor sin señal", "pantalla negra", "sin imagen"):
		return "problema_video"
	case has("no se escucha", "sin sonido", "sin audio", "audio no funciona", "parlantes no"):
		return "problema_audio"
	case has("microfono", "micrófono") && has("no funciona", "no me escuchan", "no anda"):
		return "problema_microfono"
	case has("se apaga sola", "se apaga solo", "reinicio inesperado", "apagado solo"):
		return "problema_apagado"
	case has("bateria", "batería") && has("no carga", "cargador", "poca bateria"):
		return "problema_bateria"

	// ——— Seguridad ———
	case has("firewall") && !has("puerto"):
		return "problema_firewall"
	case has("bitlocker", "cifrar disco", "encriptar disco"):
		return "bitlocker"
	case has("analizar virus", "escanear virus", "windows defender", "antivirus", "malware"):
		return "escanear_virus"
	case has("reparar permisos", "permisos de carpeta", "arreglar permisos"):
		return "reparar_permisos"

	// ——— Potencia ———
	case has("bloquear pc", "bloquear equipo", "lock", "bloquear pantalla"):
		return "bloquear_pc"
	case all("apagar") && !has("no apaga"):
		return "apagar"
	case has("reiniciar pc", "reiniciar equipo", "reiniciar computadora", "reboot"):
		return "reiniciar"
	case has("suspender", "modo sleep", "hibernar", "sleep"):
		return "suspender"
	case has("cuanto lleva encendida", "tiempo encendida", "uptime", "hora de inicio"):
		return "uptime"
	case has("modo rendimiento", "alto rendimiento", "high performance"):
		return "modo_rendimiento"
	case has("modo ahorro", "balanced", "ahorro de energia"):
		return "modo_ahorro"
	case has("informe de bateria", "battery report", "reporte de bateria"):
		return "informe_bateria"
	case has("informe de energia", "energy report", "reporte de energia"):
		return "informe_energia"

	// ——— Puertos ———
	case has("abrir puerto") && !has("usb"):
		return "abrir_puerto"
	case has("cerrar puerto"):
		return "cerrar_puerto"

	// ——— Red / P2P ———
	case has("agentes en la red", "ver agentes", "listar agentes", "qué agentes hay", "que agentes hay", "peers", "otros frank"):
		return "ver_agentes"
	case has("app activa", "aplicacion activa", "que estoy usando", "qué estoy usando", "aplicaciones usadas", "tiempo en apps"):
		return "app_activa"

	// ——— Wazuh/OCS ———
	case has("analizar wazuh", "ver logs wazuh", "wazuh", "ossec"):
		return "analizar_wazuh"
	case has("analizar ocs", "ver logs ocs", "ocs inventory", "inventario ocs"):
		return "analizar_ocs"
	case has("analizar logs frank", "ver logs frank", "log del agente"):
		return "analizar_logs_frank"

	// ——— Inventario ———
	case has("generar inventario", "crear inventario", "inventario completo", "exportar inventario"):
		return "generar_inventario"
	case has("ver inventario", "mostrar inventario", "ultimo inventario"):
		return "ver_inventario"

	// ——— Acciones especiales ———
	case has("velocidad de red", "test de velocidad", "velocidad internet", "cuanto mide la red"):
		return "velocidad_red"
	case has("eventos del sistema", "event viewer", "ver eventos", "visor de eventos"):
		return "ver_eventos_sistema"
	case has("contrasena segura", "contraseña segura", "generar contrasena", "generar contraseña"):
		return "generar_contrasena"
	case has("numero de serie", "serial del equipo", "nro de serie"):
		return "ver_numero_serie"
	case has("vaciar papelera", "papelera de reciclaje", "limpiar papelera"):
		return "vaciar_papelera"
	case has("arp", "tabla arp", "ver arp"):
		return "ver_arp"
	case has("tabla de rutas", "routing table", "rutas de red"):
		return "ver_tablas_rutas"
	case has("hash del archivo", "md5", "sha256", "checksum"):
		return "ver_hash_archivo"
	case has("pantalla azul reciente", "bsod reciente", "crashes recientes", "crashdump"):
		return "ver_bsod_recientes"
	case has("diagnostico completo", "diagnostico general", "analizar todo"):
		return "diagnostico_completo"
	case has("politica de contraseñas", "politica de claves", "password policy"):
		return "ver_politica_contrasenas"
	case has("mapear unidad", "mapear disco de red", "map drive"):
		return "mapear_unidad"
	case has("desconectar unidad", "desmap", "quitar unidad de red"):
		return "desconectar_unidad"
	case has("reparar winsock", "winsock reset", "reset winsock"):
		return "reparar_winsock"
	case has("uso de disco por carpeta", "que carpeta ocupa mas", "carpetas grandes"):
		return "uso_disco_por_carpeta"
	case has("historial de red", "historial red"):
		return "ver_historial_red"
	case has("certificados", "ver certificados", "listar certificados"):
		return "ver_certificados"
	case has("activacion windows", "activar windows", "licencia windows"):
		return "activacion_windows"
	case has("gpresult", "resultado de politicas", "politicas aplicadas"):
		return "resultado_politicas"
	case has("activar hibernacion", "habilitar hibernacion"):
		return "activar_hibernacion"
	case has("desactivar hibernacion", "deshabilitar hibernacion"):
		return "desactivar_hibernacion"

	// ——— Social ———
	case has("como estas", "como andas", "como te va", "que tal estas"):
		return "como_estas"
	case has("contame un chiste", "un chiste", "chiste de it", "hazme reir"):
		return "chiste"
	case has("frase motivacional", "dame una frase", "motivame", "algo motivador"):
		return "motivacion"
	case has("cuanto tiempo llevas", "cuanto llevas corriendo", "hace cuanto estas"):
		return "cuanto_llevas_corriendo"
	case has("quien eres", "que eres", "presentate", "describete"):
		return "quien_eres"

	// ——— Búsqueda / registro ———
	case has("busca", "buscar", "encuentra") && has("archivo", "documento", "fichero"):
		return "buscar_archivo"
	case has("programas de inicio", "arranque", "startup"):
		return "gestionar_inicio"
	case has("registro de acciones", "que hiciste", "historial de acciones", "que hiciste"):
		return "auditoria"
	case has("deshacer", "rollback", "revertir"):
		return "rollback"
	case has("ayuda", "help", "que puedes hacer", "comandos disponibles", "que sabes hacer"):
		return "ayuda"
	case has("gracias", "muchas gracias", "grax"):
		return "gracias"
	case has("hola", "buenas", "buenos dias", "buenas tardes", "saludos"):
		return "saludo"
	case has("ram") && !has("programa", "ram", "descargar"):
		return "diagnostico_ram"
	case has("diagnostico de memoria", "memoria ram", "test de ram"):
		return "diagnostico_ram"
	// Consultas de tickets
	case has("ticket") && has("abierto", "pendiente", "activo", "sin resolver", "en curso"):
		return "ver_tickets_abiertos"
	case has("ticket") && has("cerrado", "resuelto", "completado", "finalizado", "historial"):
		return "ver_tickets_cerrados"
	case has("mis ticket", "ver ticket", "listar ticket", "historial ticket", "todas mis solicitud"):
		return "ver_tickets"
	case has("abrir ticket", "crear ticket", "nuevo ticket", "generar ticket", "abrir solicitud"):
		return "nuevo_ticket"
	case has("solicitud") && has("it", "informatica", "soporte"):
		return "solicitud_it_local"
	// Solicitud de hardware/periférico con verbos de pedido
	case has("necesito", "quiero", "solicito", "solicitar", "requiero", "pedir", "pedido", "preciso") &&
		has("mouse", "raton", "ratón", "teclado", "monitor", "pantalla", "pendrive", "usb",
			"computadora", "pc ", " pc", "laptop", "notebook", "cable", "adaptador",
			"fuente", "disco", "memoria", "ram", "auricular", "webcam", "camara", "microfono"):
		return "solicitud_it_local"
	case has("toner", "papel impresora", "cartucho", "tinta"):
		return "solicitud_almacen"
	case has("necesito", "quiero", "solicito", "solicitar", "requiero", "pedir") &&
		has("toner", "papel", "cartucho", "tinta", "impresora", "disco duro", "memoria ram"):
		return "solicitud_almacen"
	case has("puerto usb") && has("abrir", "habilitar"):
		return "abrir_puerto_usb"
	}

	// Delegar a acciones extendidas (actions_extra.go)
	if intent := extraKeywordMatch(lower); intent != "" {
		return intent
	}

	return ""
}

func classifyIntent(input string) (string, float64) {
	// Capa 1: Keyword Matching
	if intent := keywordMatch(input); intent != "" {
		return intent, 0.95
	}

	// Capa 2: Naive Bayes + ClosestMatch
	nluMutex.RLock()
	defer nluMutex.RUnlock()

	if nbModel == nil {
		return "", 0.0
	}

	tokens := preprocessText(input)
	if len(tokens) == 0 {
		return "", 0.0
	}

	probs := nbModel.PredictProbabilities(tokens)
	var bestIntent string
	var maxProb float64
	for intent, prob := range probs {
		if prob > maxProb {
			maxProb = prob
			bestIntent = intent
		}
	}

	closest := closestMatcher.Closest(strings.ToLower(input))
	simCM := jaccardSimilarity(input, closest)
	finalScore := (maxProb * 0.6) + (simCM * 0.4)

	if finalScore > 0.25 { // Umbral reducido para mayor sensibilidad
		return bestIntent, finalScore
	}
	return "", finalScore
}
// ========================= CONFIGURACIÓN =========================
func loadSettings() {
	settingsMutex.Lock()
	defer settingsMutex.Unlock()

	settings = UserSettings{
		NotificationsEnabled:    true,
		NotificationFrequency:   "medium",
		EmotionalSupportEnabled: true,
		Theme:                   "light",
		AccentColor:             "Azul",
		StartWithWindows:        false,
		UseEmojis:               true,
		Personality:             "profesional",
		FontSize:                14,
		BoldText:                false,
		DyslexicMode:            false,
		UserBubbleTextColor:     "Blanco",
		FrankBubbleTextColor:    "Azul marino",
	}

	data, err := os.ReadFile(settingsFile)
	if err != nil {
		saveSettingsLocked()
		return
	}
	plain, err := unprotectData(data)
	if err != nil {
		// Clave cambió o archivo corrupto — descartar y guardar defaults.
		logger.Printf("[WARN] loadSettings: decrypt falló, reseteando ajustes: %v", err)
		os.Remove(settingsFile)
		saveSettingsLocked()
		return
	}
	if err := json.Unmarshal(plain, &settings); err != nil {
		logger.Printf("[WARN] loadSettings: json inválido, reseteando ajustes: %v", err)
		os.Remove(settingsFile)
		saveSettingsLocked()
		return
	}
}

func saveSettingsLocked() {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		logger.Printf("[ERROR] saveSettings: json.Marshal falló: %v", err)
		return
	}
	encrypted, err := protectData(data)
	if err != nil {
		logger.Printf("[ERROR] saveSettings: protectData falló: %v", err)
		return
	}
	if err := os.WriteFile(settingsFile, encrypted, 0600); err != nil {
		logger.Printf("[ERROR] saveSettings: WriteFile falló: %v", err)
	}
}

func saveSettings() {
	settingsMutex.Lock()
	defer settingsMutex.Unlock()
	saveSettingsLocked()
}

// ─── Tema personalizado de Fyne ────────────────────────────────────────────
//
// La API theme.DarkTheme() / theme.LightTheme() está deprecada en Fyne v2.4+.
// La forma correcta es implementar fyne.Theme y controlar la variante (dark/light)
// pasando theme.VariantDark / theme.VariantLight a theme.DefaultTheme().Color().
// Esto garantiza que TODOS los widgets (botones, inputs, fondos) respetan el modo.

type customTheme struct {
	primary      color.Color
	dark         bool
	highContrast bool
	textSize     float32 // 0 = usar default
	boldText     bool
	dyslexicMode bool
}

// newCustomTheme crea un tema con color de acento, modo claro/oscuro y opciones de accesibilidad.
func newCustomTheme(dark bool, primaryColor color.Color, highContrast bool, textSize float32, boldText bool, dyslexicMode bool) fyne.Theme {
	return &customTheme{
		dark:         dark,
		primary:      primaryColor,
		highContrast: highContrast,
		textSize:     textSize,
		boldText:     boldText,
		dyslexicMode: dyslexicMode,
	}
}

func (t *customTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if t.highContrast {
		// Paleta de alto contraste: fondo negro, texto blanco, acento amarillo.
		switch name {
		case theme.ColorNameBackground, theme.ColorNameOverlayBackground:
			return color.Black
		case theme.ColorNameForeground:
			return color.White
		case theme.ColorNamePrimary, theme.ColorNameFocus:
			return color.RGBA{R: 255, G: 235, B: 0, A: 255} // amarillo alto contraste
		case theme.ColorNameButton:
			return color.RGBA{R: 30, G: 30, B: 30, A: 255}
		case theme.ColorNameDisabled:
			return color.RGBA{R: 128, G: 128, B: 128, A: 255}
		case theme.ColorNameInputBackground:
			return color.RGBA{R: 20, G: 20, B: 20, A: 255}
		case theme.ColorNameSeparator:
			return color.RGBA{R: 200, G: 200, B: 200, A: 255}
		case theme.ColorNameScrollBar:
			return color.RGBA{R: 200, G: 200, B: 200, A: 200}
		default:
			return theme.DefaultTheme().Color(name, theme.VariantDark)
		}
	}

	// Color primario/foco siempre usa el acento elegido por el usuario.
	if name == theme.ColorNamePrimary || name == theme.ColorNameFocus {
		return t.primary
	}
	// Forzar la variante según la preferencia guardada, ignorando el sistema.
	if t.dark {
		variant = theme.VariantDark
	} else {
		variant = theme.VariantLight
	}
	return theme.DefaultTheme().Color(name, variant)
}

func (t *customTheme) Font(style fyne.TextStyle) fyne.Resource {
	if t.boldText || t.dyslexicMode {
		// Forzar negrita — mejora legibilidad para dislexia y texto negrita.
		style.Bold = true
	}
	return theme.DefaultTheme().Font(style)
}

func (t *customTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (t *customTheme) Size(name fyne.ThemeSizeName) float32 {
	base := t.textSize
	if base <= 0 {
		base = 14
	}
	// Modo dislexia: incremento adicional de tamaño y espaciado.
	if t.dyslexicMode {
		base += 2
	}
	defaultSize := theme.DefaultTheme().Size(name)
	defaultText := theme.DefaultTheme().Size(theme.SizeNameText)
	if defaultText <= 0 {
		defaultText = 13
	}
	scale := base / defaultText
	switch name {
	case theme.SizeNameText:
		return base
	case theme.SizeNameHeadingText:
		return base * 1.3
	case theme.SizeNameSubHeadingText:
		return base * 1.1
	case theme.SizeNameCaptionText:
		return base * 0.85
	case theme.SizeNameInnerPadding, theme.SizeNamePadding:
		if t.dyslexicMode {
			return defaultSize * scale * 1.2
		}
		return defaultSize
	default:
		return defaultSize
	}
}

func applyTheme() {
	settingsMutex.RLock()
	isDark := settings.Theme == "dark"
	isHighContrast := settings.Theme == "high_contrast"
	accentName := settings.AccentColor
	fontSize := settings.FontSize
	boldText := settings.BoldText
	dyslexicMode := settings.DyslexicMode
	settingsMutex.RUnlock()

	col := parseHexColor("#1976D2") // azul por defecto
	if hex, ok := accentColors[accentName]; ok {
		col = parseHexColor(hex)
	}

	// Aplicar tema a Fyne — esto propaga a todos los widgets vía listeners internos.
	appInstance.Settings().SetTheme(newCustomTheme(isDark || isHighContrast, col, isHighContrast, fontSize, boldText, dyslexicMode))

	// Forzar refresh global del canvas para que TODOS los widgets (botones,
	// inputs, separadores) reciban el nuevo tema inmediatamente.
	if mainWindow != nil && mainWindow.Content() != nil {
		mainWindow.Content().Refresh()
	}

	// Reconstruir burbujas directamente: canvas.Rectangle graba el color
	// en el momento de creación, Refresh() no lo actualiza — hay que recrear.
	if chatVBox != nil {
		rebuildChatBubbles()
	}
}

func parseHexColor(s string) color.Color {
	s = strings.TrimPrefix(s, "#")
	if len(s) == 6 {
		r, _ := strconv.ParseUint(s[0:2], 16, 8)
		g, _ := strconv.ParseUint(s[2:4], 16, 8)
		b, _ := strconv.ParseUint(s[4:6], 16, 8)
		return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
	}
	return color.RGBA{R: 25, G: 118, B: 210, A: 255}
}

func setStartWithWindows(enabled bool) {
	exePath, err := os.Executable()
	if err != nil {
		logger.Printf("[ERROR] setStartWithWindows: no se pudo obtener ruta: %v", err)
		return
	}
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.SET_VALUE)
	if err != nil {
		logger.Printf("[ERROR] setStartWithWindows: no se pudo abrir clave: %v", err)
		return
	}
	defer key.Close()

	if enabled {
		if err := key.SetStringValue("AFE Assistant", exePath); err != nil {
			logger.Printf("[ERROR] setStartWithWindows: no se pudo establecer valor: %v", err)
		}
	} else {
		if err := key.DeleteValue("AFE Assistant"); err != nil && !errors.Is(err, registry.ErrNotExist) {
			logger.Printf("[ERROR] setStartWithWindows: no se pudo eliminar valor: %v", err)
		}
	}
}

// ========================= ACTUALIZACIONES =========================
func importUpdatePackage(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no se pudo leer update.pkg: %v", err)
	}
	var pkg UpdatePackage
	if err := json.Unmarshal(data, &pkg); err != nil {
		return fmt.Errorf("formato inválido: %v", err)
	}
	if len(pkg.Phrases) > 0 {
		for cat, phrases := range pkg.Phrases {
			if _, ok := trayPhrases[PhraseCategory(cat)]; ok {
				trayPhrases[PhraseCategory(cat)] = phrases
			}
		}
		logger.Printf("[INFO] Frases actualizadas.")
	}
	settingsMutex.Lock()
	for k, v := range pkg.SettingsPatch {
		switch k {
		case "EmotionalSupportEnabled":
			if b, ok := v.(bool); ok {
				settings.EmotionalSupportEnabled = b
			}
		case "NotificationsEnabled":
			if b, ok := v.(bool); ok {
				settings.NotificationsEnabled = b
			}
		case "NotificationFrequency":
			if s, ok := v.(string); ok {
				settings.NotificationFrequency = s
			}
		case "UseEmojis":
			if b, ok := v.(bool); ok {
				settings.UseEmojis = b
			}
		case "Personality":
			if s, ok := v.(string); ok {
				settings.Personality = s
			}
		case "Theme":
			if s, ok := v.(string); ok {
				settings.Theme = s
			}
		case "AccentColor":
			if s, ok := v.(string); ok {
				settings.AccentColor = s
			}
		case "StartWithWindows":
			if b, ok := v.(bool); ok {
				settings.StartWithWindows = b
			}
		}
	}
	settingsMutex.Unlock()
	saveSettings()
	sendNotification(AgentName, "Actualización aplicada.")
	return nil
}

func checkForLocalUpdate() {
	updatePath := "update.pkg"
	if _, err := os.Stat(updatePath); err == nil {
		if err := importUpdatePackage(updatePath); err != nil {
			logger.Printf("[ERROR] %v", err)
		} else {
			os.Remove(updatePath)
			restartProactiveTicker()
			applyTheme()
		}
	}
}

// ========================= PERFIL DE USUARIO =========================
func loadUserProfile() {
	userProfileMutex.Lock()
	defer userProfileMutex.Unlock()

	data, err := os.ReadFile(userProfileFile)
	if err != nil {
		profileCompleted.Store(false)
		return
	}
	plain, err := unprotectData(data)
	if err != nil {
		// Archivo ilegible (clave diferente, corrupto). Eliminar para no
		// intentar leerlo en cada inicio — el wizard generará uno nuevo.
		logger.Printf("[WARN] loadUserProfile: decrypt falló, eliminando archivo: %v", err)
		os.Remove(userProfileFile)
		profileCompleted.Store(false)
		return
	}
	var tmp UserProfile
	if err := json.Unmarshal(plain, &tmp); err != nil {
		logger.Printf("[ERROR] Perfil corrupto: %v", err)
		os.Remove(userProfileFile)
		profileCompleted.Store(false)
		return
	}
	// Perfil anónimo (Anonymous=true) es válido aunque no tenga FirstName/Email.
	if !tmp.Anonymous && (tmp.FirstName == "" || tmp.Email == "") {
		logger.Println("[WARN] Perfil incompleto, ignorado")
		profileCompleted.Store(false)
		return
	}
	userProfile = tmp
	userProfile.LastIP = getOutboundIP()
	userProfile.Hostname, _ = os.Hostname()
	profileCompleted.Store(true)
}

func saveUserProfile() {
	userProfileMutex.RLock()
	data, err := json.MarshalIndent(userProfile, "", "  ")
	userProfileMutex.RUnlock()

	if err != nil {
		logger.Printf("[ERROR] saveUserProfile: marshal falló: %v", err)
		return
	}

	encrypted, err := protectData(data)
	if err != nil {
		logger.Printf("[ERROR] saveUserProfile: cifrado falló: %v", err)
		return
	}

	if err := os.WriteFile(userProfileFile, encrypted, 0600); err != nil {
		logger.Printf("[ERROR] saveUserProfile: escritura falló: %v", err)
	}
}

func isFirstRun() bool {
	return !profileCompleted.Load()
}

func getDisplayName() string {
	userProfileMutex.RLock()
	defer userProfileMutex.RUnlock()
	if userProfile.Nickname != "" {
		return userProfile.Nickname
	}
	if userProfile.FirstName != "" {
		if userProfile.LastName != "" {
			return userProfile.FirstName + " " + userProfile.LastName
		}
		return userProfile.FirstName
	}
	return "Usuario"
}

func showFirstRunWizard() {
	w := appInstance.NewWindow("Bienvenido a Frank")
	w.Resize(fyne.NewSize(500, 600))

	firstNameEntry := widget.NewEntry()
	firstNameEntry.SetPlaceHolder("Nombre")
	lastNameEntry := widget.NewEntry()
	lastNameEntry.SetPlaceHolder("Apellido")
	nicknameEntry := widget.NewEntry()
	nicknameEntry.SetPlaceHolder("Apodo (opcional)")
	departmentEntry := widget.NewEntry()
	departmentEntry.SetPlaceHolder("Departamento")
	areaEntry := widget.NewEntry()
	areaEntry.SetPlaceHolder("Área / Gerencia")
	emailEntry := widget.NewEntry()
	emailEntry.SetPlaceHolder("Correo electrónico")
	emailEntry.SetText(os.Getenv("USERNAME") + "@afe.com.uy")
	phoneEntry := widget.NewEntry()
	phoneEntry.SetPlaceHolder("Teléfono")
	internalPhoneEntry := widget.NewEntry()
	internalPhoneEntry.SetPlaceHolder("Interno (opcional)")
	officeEntry := widget.NewEntry()
	officeEntry.SetPlaceHolder("Oficina / Ubicación")

	infoLabel := widget.NewLabel("Para personalizar tu experiencia y generar un inventario,\npor favor completa los siguientes datos (puedes omitir los opcionales).")
	infoLabel.Wrapping = fyne.TextWrapWord

	confirmCheck := widget.NewCheck("Acepto que estos datos se utilicen para personalizar el asistente y fines de inventario", nil)
	confirmCheck.SetChecked(false)

	skipBtn := widget.NewButton("Omitir (modo anónimo)", func() {
		userProfileMutex.Lock()
		userProfile = UserProfile{
			Nickname:     "Usuario",
			Anonymous:    true,
			RegisteredAt: time.Now().Format(time.RFC3339),
			LastIP:       getOutboundIP(),
			Hostname:     pcName,
		}
		userProfileMutex.Unlock()
		saveUserProfile()
		profileCompleted.Store(true)
		w.Close()
		mainWindow.Hide()
	})

	saveBtn := widget.NewButton("Guardar y continuar", func() {
		if !confirmCheck.Checked {
			sendNotification(AgentName, "Debes aceptar el uso de tus datos para continuar.")
			return
		}
		if firstNameEntry.Text == "" || emailEntry.Text == "" {
			sendNotification(AgentName, "Nombre y correo son obligatorios.")
			return
		}
		userProfileMutex.Lock()
		userProfile = UserProfile{
			FirstName:      firstNameEntry.Text,
			LastName:       lastNameEntry.Text,
			Nickname:       nicknameEntry.Text,
			Department:     departmentEntry.Text,
			Area:           areaEntry.Text,
			Email:          emailEntry.Text,
			Phone:          phoneEntry.Text,
			InternalPhone:  internalPhoneEntry.Text,
			OfficeLocation: officeEntry.Text,
			RegisteredAt:   time.Now().Format(time.RFC3339),
			LastIP:         getOutboundIP(),
			Hostname: func() string { h, _ := os.Hostname(); return h }(),
		}
		userProfileMutex.Unlock()
		saveUserProfile()
		profileCompleted.Store(true)
		w.Close()
		mainWindow.Hide()
		sendNotification(AgentName, fmt.Sprintf("¡Bienvenido/a, %s!", getDisplayName()))
	})

	saveBtn.Importance = widget.HighImportance

	form := container.NewVBox(
		infoLabel,
		widget.NewSeparator(),
		widget.NewLabel("Nombre *"),
		firstNameEntry,
		widget.NewLabel("Apellido *"),
		lastNameEntry,
		widget.NewLabel("Apodo (cómo quieres que te llame)"),
		nicknameEntry,
		widget.NewLabel("Departamento *"),
		departmentEntry,
		widget.NewLabel("Área / Gerencia *"),
		areaEntry,
		widget.NewLabel("Correo electrónico *"),
		emailEntry,
		widget.NewLabel("Teléfono *"),
		phoneEntry,
		widget.NewLabel("Interno"),
		internalPhoneEntry,
		widget.NewLabel("Oficina / Ubicación *"),
		officeEntry,
		widget.NewSeparator(),
		confirmCheck,
		container.NewHBox(skipBtn, saveBtn),
	)

	scroll := container.NewVScroll(form)
	w.SetContent(container.NewPadded(scroll))
	w.CenterOnScreen()
	w.Show()
}

func showEditProfileWindow() {
	w := appInstance.NewWindow("Editar Perfil")
	w.Resize(fyne.NewSize(500, 600))

	userProfileMutex.RLock()
	profile := userProfile
	userProfileMutex.RUnlock()

	firstNameEntry := widget.NewEntry()
	firstNameEntry.SetText(profile.FirstName)
	lastNameEntry := widget.NewEntry()
	lastNameEntry.SetText(profile.LastName)
	nicknameEntry := widget.NewEntry()
	nicknameEntry.SetText(profile.Nickname)
	departmentEntry := widget.NewEntry()
	departmentEntry.SetText(profile.Department)
	areaEntry := widget.NewEntry()
	areaEntry.SetText(profile.Area)
	emailEntry := widget.NewEntry()
	emailEntry.SetText(profile.Email)
	phoneEntry := widget.NewEntry()
	phoneEntry.SetText(profile.Phone)
	internalPhoneEntry := widget.NewEntry()
	internalPhoneEntry.SetText(profile.InternalPhone)
	officeEntry := widget.NewEntry()
	officeEntry.SetText(profile.OfficeLocation)

	saveBtn := widget.NewButton("Guardar cambios", func() {
		if firstNameEntry.Text == "" || lastNameEntry.Text == "" || emailEntry.Text == "" {
			sendNotification(AgentName, "Nombre, Apellido y Correo son obligatorios.")
			return
		}
		userProfileMutex.Lock()
		userProfile = UserProfile{
			FirstName:      firstNameEntry.Text,
			LastName:       lastNameEntry.Text,
			Nickname:       nicknameEntry.Text,
			Department:     departmentEntry.Text,
			Area:           areaEntry.Text,
			Email:          emailEntry.Text,
			Phone:          phoneEntry.Text,
			InternalPhone:  internalPhoneEntry.Text,
			OfficeLocation: officeEntry.Text,
			RegisteredAt:   profile.RegisteredAt,
			LastIP:         getOutboundIP(),
			Hostname:       pcName,
		}
		userProfileMutex.Unlock()
		saveUserProfile()
		w.Close()
		sendNotification(AgentName, "Perfil actualizado.")
	})

	cancelBtn := widget.NewButton("Cancelar", func() {
		w.Close()
	})

	form := container.NewVBox(
		widget.NewLabel("Nombre"),
		firstNameEntry,
		widget.NewLabel("Apellido"),
		lastNameEntry,
		widget.NewLabel("Apodo"),
		nicknameEntry,
		widget.NewLabel("Departamento"),
		departmentEntry,
		widget.NewLabel("Área / Gerencia"),
		areaEntry,
		widget.NewLabel("Correo electrónico"),
		emailEntry,
		widget.NewLabel("Teléfono"),
		phoneEntry,
		widget.NewLabel("Interno"),
		internalPhoneEntry,
		widget.NewLabel("Oficina / Ubicación"),
		officeEntry,
		container.NewHBox(cancelBtn, saveBtn),
	)

	scroll := container.NewVScroll(form)
	w.SetContent(container.NewPadded(scroll))
	w.CenterOnScreen()
	w.Show()
}
// ========================= SELECCIÓN DE FRASES =========================
func getTrayPhrase() string {
	settingsMutex.RLock()
	emotionalEnabled := settings.EmotionalSupportEnabled
	settingsMutex.RUnlock()

	hour := time.Now().Hour()
	var candidates []Phrase

	switch {
	case hour >= 8 && hour < 12:
		candidates = append(candidates, trayPhrases[Technical]...)
		candidates = append(candidates, trayPhrases[Light]...)
		if hour > 10 {
			candidates = append(candidates, trayPhrases[Motivational]...)
		}
	case hour >= 12 && hour < 17:
		candidates = append(candidates, trayPhrases[Technical]...)
		candidates = append(candidates, trayPhrases[Motivational]...)
		if emotionalEnabled && shouldShowEmotional() {
			candidates = append(candidates, trayPhrases[Emotional]...)
		}
	default:
		candidates = append(candidates, trayPhrases[Light]...)
		candidates = append(candidates, trayPhrases[Technical]...)
	}

	if len(candidates) == 0 {
		return "Estoy aquí para ayudarte."
	}
	return weightedRandom(candidates)
}

func shouldShowEmotional() bool {
	memory.mu.RLock()
	defer memory.mu.RUnlock()

	if len(memory.LastUserMessages) == 0 {
		return false
	}
	keywords := []string{
		"no puedo", "no anda", "error", "problema", "lento", "no funciona",
		"estresado", "cansado", "agotado", "frustrado", "no doy más",
		"ayuda", "socorro", "desesperado",
	}
	stressCount := 0
	start := 0
	if len(memory.LastUserMessages) > 5 {
		start = len(memory.LastUserMessages) - 5
	}
	for i := start; i < len(memory.LastUserMessages); i++ {
		msg := strings.ToLower(memory.LastUserMessages[i])
		for _, k := range keywords {
			if strings.Contains(msg, k) {
				stressCount++
				break
			}
		}
	}
	return stressCount >= 2
}

func weightedRandom(phrases []Phrase) string {
	if len(phrases) == 0 {
		return "Estoy aquí para ayudarte."
	}
	total := 0.0
	for _, p := range phrases {
		total += p.Weight
	}
	r := mathrand.Float64() * total
	acc := 0.0
	for _, p := range phrases {
		acc += p.Weight
		if r <= acc {
			return p.Text
		}
	}
	return phrases[0].Text
}

// ========================= MONITOREO PROACTIVO (WAZUH/OCS) =========================
func restartProactiveTicker() {
	proactiveTickerMutex.Lock()
	defer proactiveTickerMutex.Unlock()

	if proactiveTicker != nil {
		proactiveTicker.Stop()
		proactiveTicker = nil
	}
	if proactiveTickerStop != nil {
		close(proactiveTickerStop)
		proactiveTickerStop = nil
	}

	settingsMutex.RLock()
	enabled := settings.NotificationsEnabled
	freqKey := settings.NotificationFrequency
	settingsMutex.RUnlock()

	if !enabled {
		return
	}

	proactiveTickerStop = make(chan struct{})
	duration, ok := notificationFrequencies[freqKey]
	if !ok {
		duration = 3 * time.Hour
	}
	proactiveTicker = time.NewTicker(duration)

	go func(t *time.Ticker, stop chan struct{}) {
		for {
			select {
			case <-t.C:
				if !isWindowVisible.Load() {
					phrase := getTrayPhrase()
					sendNotification(AgentName, phrase)
				}
			case <-stop:
				return
			}
		}
	}(proactiveTicker, proactiveTickerStop)
}

func proactiveMonitoring(ctx context.Context) {
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return
	}
	restartProactiveTicker()

	analysisTicker := time.NewTicker(12 * time.Hour)
	defer analysisTicker.Stop()
	for {
		select {
		case <-analysisTicker.C:
			go performSilentSystemCheck()
		case <-ctx.Done():
			return
		}
	}
}

func performSilentSystemCheck() {
	defer func() {
		if r := recover(); r != nil {
			logger.Printf("[ERROR] performSilentSystemCheck panic: %v", r)
		}
	}()
	logger.Println("[INFO] Análisis proactivo del sistema...")
	var issues []string

	// Espacio en disco
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-PSDrive -PSProvider FileSystem | Where-Object {$_.Used -gt 0 -and $_.Free -lt 5GB} | Select-Object -ExpandProperty Name")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	lowDisks := strings.TrimSpace(string(out))
	if lowDisks != "" {
		issues = append(issues, fmt.Sprintf("⚠️ Espacio bajo en disco(s): %s", lowDisks))
	}

	// Actualizaciones pendientes
	cmd2 := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"(New-Object -ComObject Microsoft.Update.Session).CreateUpdateSearcher().Search('IsInstalled=0').Updates.Count")
	cmd2.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out2, _ := cmd2.Output()
	updates, _ := strconv.Atoi(strings.TrimSpace(string(out2)))
	if updates > 0 {
		issues = append(issues, fmt.Sprintf("🔄 %d actualizaciones de Windows pendientes.", updates))
	}

	// Firewall
	cmd5 := exec.Command("netsh", "advfirewall", "show", "allprofiles")
	cmd5.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out5, _ := cmd5.Output()
	if !strings.Contains(string(out5), "State ON") {
		issues = append(issues, "🔥 Firewall de Windows desactivado.")
	}

	// Wazuh logs
	wazuhLogPath := filepath.Join(os.Getenv("ProgramFiles"), "ossec-agent", "ossec.log")
	if _, err := os.Stat(wazuhLogPath); err == nil {
		cmdW := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
			fmt.Sprintf("Get-Content '%s' -Tail 500 | Select-String -Pattern 'level ([8-9]|1[0-5])' | Measure-Object | Select-Object -ExpandProperty Count", wazuhLogPath))
		cmdW.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		outW, _ := cmdW.Output()
		if count, _ := strconv.Atoi(strings.TrimSpace(string(outW))); count > 0 {
			issues = append(issues, fmt.Sprintf("🛡️ Wazuh detectó %d eventos de alta severidad.", count))
		}
	}

	// OCS Inventory logs
	ocsLogPath := filepath.Join(os.Getenv("ProgramFiles(x86)"), "OCS Inventory Agent", "OCSInventory.log")
	if _, err := os.Stat(ocsLogPath); err == nil {
		cmdO := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
			fmt.Sprintf("Get-Content '%s' -Tail 50 | Select-String -Pattern 'ERROR|FAIL' | Measure-Object | Select-Object -ExpandProperty Count", ocsLogPath))
		cmdO.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		outO, _ := cmdO.Output()
		if count, _ := strconv.Atoi(strings.TrimSpace(string(outO))); count > 0 {
			issues = append(issues, "📋 OCS Inventory reportó errores recientes.")
		}
	}

	if len(issues) > 0 {
		msg := "📊 **Análisis proactivo:**\n" + strings.Join(issues, "\n")
		sendNotification(AgentName, msg)
		appendMessage(msg)
	}
}
// ========================= NÚCLEO INTELIGENTE =========================
func processUserMessage(input string, mem *Memory) string {
	_ = uuid.New().String() // correlationID (reservado)
	lowerInput := strings.ToLower(input)

	// Actualizar historial antes de cualquier procesamiento
	mem.mu.Lock()
	mem.LastUserMessages = append(mem.LastUserMessages, input)
	if len(mem.LastUserMessages) > 20 {
		mem.LastUserMessages = mem.LastUserMessages[len(mem.LastUserMessages)-20:]
	}
	mem.mu.Unlock()

	// Incrementar telemetría
	telemetryInc("commands")

	mem.mu.Lock()
	if mem.PendingConfirmation != nil {
		req := mem.PendingConfirmation
		matched := false
		for _, resp := range req.ExpectedResponses {
			if strings.Contains(lowerInput, strings.ToLower(resp)) {
				matched = true
				break
			}
		}
		if matched {
			action := req
			mem.PendingConfirmation = nil
			mem.mu.Unlock()
			return action.OnConfirm(lowerInput)
		}
		if strings.Contains(lowerInput, "no") || strings.Contains(lowerInput, "cancelar") {
			action := req
			mem.PendingConfirmation = nil
			mem.mu.Unlock()
			if action.OnDeny != nil {
				return action.OnDeny()
			}
			return "❌ Acción cancelada."
		}
	}
	mem.mu.Unlock()

	if strings.Contains(lowerInput, "cancelar") && isSearching {
		cancelCurrentSearch()
		return "🔍 Búsqueda cancelada."
	}

	maybeOfferTicket(input)
	mem.mu.Lock()
	if mem.PendingConfirmation != nil && mem.PendingConfirmation.Action == "ticket" {
		desc := mem.PendingConfirmation.Description
		mem.mu.Unlock()
		return desc
	}
	mem.mu.Unlock()

	// Capa 0: Plugins dinámicos
	if p := matchPlugin(input); p != nil {
		return executePlugin(p, input)
	}

	// Capa 0b: Reglas dinámicas
	if r := matchDynamicRule(input, mem); r != "" {
		return r
	}

	intent, confidence := classifyIntent(input)
	jsonLog("INFO", "classify", fmt.Sprintf("intent=%s conf=%.3f", intent, confidence))
	if confidence > 0.25 {
		if fn, ok := actionMap[intent]; ok {
			mem.mu.Lock()
			mem.LastTopic = intent
			mem.mu.Unlock()
			return fn(input, mem)
		}
	}

	// Si el input es ambiguo (corto, genérico) y hay un tema reciente, retomar
	// la acción anterior en lugar de mandar "sí" o "dale" a Ollama.
	if isAmbiguousInput(input) {
		mem.mu.RLock()
		lastTopic := mem.LastTopic
		mem.mu.RUnlock()
		if lastTopic != "" {
			if fn, ok := actionMap[lastTopic]; ok {
				return fn(input, mem)
			}
		}
	}

	if aiResponse := askGateway(input, mem); aiResponse != "" {
		return aiResponse
	}

	telemetryInc("errors")
	return defaultResponse(input, mem)
}

// actionMap: mapa funcional de intenciones → handlers (DRY, reemplaza switch)
var actionMap = map[string]func(string, *Memory) string{
	// Hardware
	"problema_usb":           checkUSB,
	"problema_mouse":         checkMouse,
	"problema_teclado":       checkKeyboard,
	"problema_impresora":     fixPrinter,
	"problema_video":         checkVideo,
	"problema_audio":         checkAudio,
	"problema_microfono":     checkMicrophone,
	"problema_apagado":       checkShutdown,
	"problema_bateria":       checkBattery,
	"problema_pantalla_azul": criticalTicket,
	"temperatura":            getCPUTemperature,

	// Red
	"problema_internet":   fixNetwork,
	"flush_dns":           flushDNS,
	"reparar_winsock":     repairWinsock,
	"reset_red":           requireAdmin(resetNetworkStack),
	"cambiar_dns":         requireAdmin(changeDNS),
	"ver_wifi_password":   getWiFiPassword,
	"conexiones_activas":  netstat,
	"ping":                pingHost,
	"tracert":             tracertHost,
	"diagnostico_proxy":   diagnoseProxy,
	"rdp":                 requireAdmin(enableRDP),
	"compartir_carpeta":   shareFolder,
	"velocidad_red":       networkSpeedTest,
	"ver_arp":             getARPTable,
	"ver_tablas_rutas":    getRoutingTable,
	"mapear_unidad":       mapNetworkDrive,
	"desconectar_unidad":  unmapNetworkDrive,
	"ver_historial_red":   getNetworkHistory,

	// Disco
	"problema_disco_lleno":   diagnoseDiskSpace,
	"consultar_espacio":       getDiskSpaceInfo,
	"liberar_espacio":         requestDiskCleanup,
	"buscar_archivos_grandes": findLargeFiles,
	"analizar_disco":          analyzeDisk,
	"desfragmentar":           defragDisk,
	"formatear":               requireAdmin(formatDrive),
	"smart_disco":             getDiskSMART,
	"comprobar_disco":         requireAdmin(scheduleChkdsk),
	"uso_disco_por_carpeta":   getDiskUsageByFolder,
	"ver_hash_archivo":        getFileHash,
	"limpiar_minidumps":       clearMiniDumps,
	"vaciar_papelera":         emptyRecycleBin,

	// Usuarios
	"desbloquear_usuario":  requireAdmin(unlockUserAccount),
	"cambiar_password":     requireAdmin(changeUserPassword),
	"crear_usuario":        requireAdmin(createLocalUser),
	"eliminar_usuario":     requireAdmin(deleteUser),
	"listar_usuarios":      listUsers,
	"grupos_usuario":       userGroups,
	"agregar_grupo":        requireAdmin(addUserToGroup),
	"ver_politica_contrasenas": getPasswordPolicy,
	"generar_contrasena":   generateSecurePasswordFn,

	// Correo/Zimbra
	"problema_zimbra":    fixZimbra,
	"configurar_outlook": configureOutlookZimbra,

	// Sistema
	"info_sistema":        getSystemInfo,
	"variables_entorno":   listEnvVars,
	"regedit":             requireAdmin(registryQuery),
	"gpupdate":            requireAdmin(forceGPUpdate),
	"tareas_programadas":  listScheduledTasks,
	"listar_servicios":    listServices,
	"listar_drivers":      listDrivers,
	"restaurar_driver":    requireAdmin(rollbackDriver),
	"buscar_actualizaciones": checkWindowsUpdates,
	"reparar_sistema":     requireAdmin(runSFC),
	"crear_restore_point": requireAdmin(createRestorePoint),
	"restaurar_sistema":   requireAdmin(openSystemRestore),
	"corregir_hora":       fixTime,
	"activacion_windows":  checkWindowsActivation,
	"ver_numero_serie":    getSerialNumbers,
	"resultado_politicas": getGPResult,
	"ver_bsod_recientes":  getRecentBSODs,
	"ver_certificados":    listCertificates,
	"activar_hibernacion": enableHibernation,
	"desactivar_hibernacion": disableHibernation,
	"diagnostico_completo": runFullDiagnostic,

	// Procesos/Servicios
	"listar_procesos":        listProcesses,
	"finalizar_proceso":      killProcess,
	"iniciar_servicio":       requireAdmin(startService),
	"detener_servicio":       requireAdmin(stopService),
	"reiniciar_servicio":     requireAdmin(restartService),
	"cambiar_inicio_servicio": requireAdmin(setServiceStartup),

	// Programas
	"listar_programas":   listInstalledPrograms,
	"desinstalar_programa": uninstallProgram,
	"instalar_programa":  installProgram,
	"abrir_programa":     openAnyProgram,
	"gestionar_inicio":   manageStartupPrograms,

	// Seguridad
	"escanear_virus":    runDefenderScan,
	"problema_firewall": diagnoseFirewall,
	"reparar_permisos":  requireAdmin(repairFolderPermissions),
	"bitlocker":         checkBitLocker,
	"abrir_puerto":      requireAdmin(openFirewallPort),
	"cerrar_puerto":     requireAdmin(closeFirewallPort),
	"abrir_puerto_usb":  openUSBPort,

	// Impresoras
	"limpiar_cola_impresion": clearPrintQueue,
	"problema_spooler":       restartSpooler,
	"agregar_impresora":      addNetworkPrinter,
	"compartir_impresora":    sharePrinter,
	"impresora_predeterminada": setDefaultPrinter,
	"listar_impresoras":      listPrinters,

	// Batería/Energía
	"informe_bateria":     batteryReport,
	"informe_energia":     energyReport,
	"modo_rendimiento":    setHighPerformancePlan,
	"modo_ahorro":         setBalancedPlan,

	// Potencia
	"bloquear_pc": lockWorkstation,
	"apagar":      shutdownPC,
	"reiniciar":   rebootPC,
	"suspender":   suspendPC,
	"uptime":      getUptime,
	"ip_address":  getIPAddress,

	// Acciones/Audit
	"auditoria": requireAdmin(showActionLog),
	"rollback":  requireAdmin(rollbackLastAction),
	"mantenimiento": startMaintenance,
	"diagnostico_ram": requireAdmin(diagnoseRAM),
	"buscar_archivo": searchFile,

	// Wazuh/OCS/Inventario
	"analizar_wazuh":     analyzeWazuhDeep,
	"analizar_ocs":       analyzeOCSDeep,
	"analizar_logs_frank": analyzeFrankLogs,
	"generar_inventario": generateFullInventory,
	"ver_inventario":     viewLastInventory,

	// Calcular
	"calcular": calculateAdvanced,

	// Solicitudes
	"solicitud_it_local": handleITLocalRequest,
	"solicitud_almacen":  handleWarehouseRequest,

	// Tickets
	"ver_tickets":          handleVerTickets,
	"ver_tickets_abiertos": handleVerTicketsAbiertos,
	"ver_tickets_cerrados": handleVerTicketsCerrados,
	"nuevo_ticket":         handleNuevoTicket,

	// PC lenta
	"problema_pc_lenta": handleSlowPC,

	// Social
	"ayuda": showHelp,
	"saludo":               greetUser,
	"gracias":              thankUser,
	"como_estas":           howAreYou,
	"chiste":               tellJoke,
	"motivacion":           getMotivation,
	"cuanto_llevas_corriendo": agentUptime,
	"quien_eres":           whoAmI,
	"ver_eventos_sistema":  getSystemEvents,

	// Red / P2P
	"ver_agentes": listKnownPeers,
	"app_activa":  showActiveAppStats,
}

func requireAdmin(fn func(string, *Memory) string) func(string, *Memory) string {
	return func(input string, mem *Memory) string {
		if !isAdmin() {
			return "⛔ No tienes permisos para realizar esta acción."
		}
		return fn(input, mem)
	}
}

// ========================= REGISTRO DE ACCIONES =========================
func logActionWithCorrID(correlationID, action string, params map[string]interface{}, result string, reversible bool, rollback *RollbackInfo) {
	entry := ActionLogEntry{
		ID:            uuid.New().String(),
		CorrelationID: correlationID,
		Timestamp:     time.Now(),
		User:          currentUser,
		Action:        action,
		Parameters:    params,
		Result:        result,
		Reversible:    reversible,
		Rollback:      rollback,
	}
	actionLogMux.Lock()
	defer actionLogMux.Unlock()
	actionLog = append(actionLog, entry)
	if len(actionLog) > 1000 {
		actionLog = actionLog[len(actionLog)-1000:]
	}
	saveActionLog()
}

func logAction(action string, params map[string]interface{}, result string, reversible bool, rollback *RollbackInfo) {
	logActionWithCorrID(uuid.New().String(), action, params, result, reversible, rollback)
}

func saveActionLog() {
	data, err := json.MarshalIndent(actionLog, "", "  ")
	if err != nil {
		logger.Printf("[ERROR] saveActionLog: marshal falló: %v", err)
		return
	}
	encrypted, err := protectData(data)
	if err != nil {
		logger.Printf("[ERROR] saveActionLog: cifrado falló: %v", err)
		return
	}
	if err := os.WriteFile(actionLogFile, encrypted, 0600); err != nil {
		logger.Printf("[ERROR] saveActionLog: escritura falló: %v", err)
	}
}

func loadActionLog() {
	data, err := os.ReadFile(actionLogFile)
	if err != nil {
		actionLog = []ActionLogEntry{}
		return
	}
	plain, err := unprotectData(data)
	if err != nil {
		actionLog = []ActionLogEntry{}
		return
	}
	if err := json.Unmarshal(plain, &actionLog); err != nil {
		logger.Printf("[ERROR] loadActionLog: unmarshal falló: %v", err)
		actionLog = []ActionLogEntry{}
	}
}

func showActionLog(input string, mem *Memory) string {
	actionLogMux.RLock()
	defer actionLogMux.RUnlock()
	if len(actionLog) == 0 {
		return "No hay acciones registradas."
	}
	var sb strings.Builder
	sb.WriteString("📋 **Últimas 20 acciones:**\n")
	start := 0
	if len(actionLog) > 20 {
		start = len(actionLog) - 20
	}
	for i := start; i < len(actionLog); i++ {
		entry := actionLog[i]
		sb.WriteString(fmt.Sprintf("%s - %s: %s (%s)\n",
			entry.Timestamp.Format("15:04:05"), entry.User, entry.Action, entry.Result))
		if entry.Reversible {
			sb.WriteString("   ↪️ Reversible: 'deshacer " + entry.ID[:8] + "'\n")
		}
	}
	return sb.String()
}

func rollbackLastAction(input string, mem *Memory) string {
	re := regexp.MustCompile(`deshacer\s+([a-f0-9-]+)`)
	matches := re.FindStringSubmatch(strings.ToLower(input))
	actionLogMux.Lock()
	defer actionLogMux.Unlock()
	var target *ActionLogEntry
	if len(matches) >= 2 {
		idPrefix := matches[1]
		for i := len(actionLog) - 1; i >= 0; i-- {
			if strings.HasPrefix(actionLog[i].ID, idPrefix) {
				target = &actionLog[i]
				break
			}
		}
	} else {
		for i := len(actionLog) - 1; i >= 0; i-- {
			if actionLog[i].Reversible && actionLog[i].Rollback != nil {
				target = &actionLog[i]
				break
			}
		}
	}
	if target == nil {
		return "No hay acciones reversibles recientes."
	}
	cmd := exec.Command(target.Rollback.Command, target.Rollback.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("[ERROR] Rollback fallido: %v - %s", err, string(out))
		return "❌ No se pudo deshacer la acción."
	}
	target.Result = "REVERTIDO - " + target.Result
	saveActionLog()
	return "✅ Acción deshecha: " + target.Rollback.Description
}

// ========================= GESTIÓN DE RECURSOS =========================
func handleITLocalRequest(input string, mem *Memory) string {
	go sendTicket(input, "hardware")
	return "🖥️ Solicitud registrada. El área de IT recibirá el ticket y se contactará contigo a la brevedad."
}

// ── Consultas de tickets al gateway ──────────────────────────────────────────

func queryTicketsFromGateway(status string) string {
	if gatewayURL == "" {
		return "❌ Frank no tiene conexión con el gateway ahora mismo."
	}
	endpoint := gatewayURL + "/tickets?limit=20"
	if status != "" {
		endpoint += "&status=" + status
	}
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return "❌ Error interno al armar la consulta."
	}
	req.Header.Set("X-API-Key", agentAPIKey)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "❌ No se pudo conectar al gateway: " + err.Error()
	}
	defer resp.Body.Close()

	var tickets []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tickets); err != nil || len(tickets) == 0 {
		labels := map[string]string{
			"":         "tickets",
			"open":     "tickets abiertos",
			"closed":   "tickets cerrados",
			"resolved": "tickets resueltos",
		}
		return "✅ No hay " + labels[status] + " en el sistema."
	}

	labelTitle := map[string]string{
		"":         "📋 Todos tus tickets",
		"open":     "🔴 Tickets abiertos",
		"closed":   "🟢 Tickets cerrados",
		"resolved": "🟢 Tickets resueltos",
	}
	statusIcon := map[string]string{
		"open":        "🔴",
		"in_progress": "🟡",
		"closed":      "🟢",
		"resolved":    "🟢",
	}

	var sb strings.Builder
	sb.WriteString(labelTitle[status] + ":\n\n")
	for _, t := range tickets {
		id, _ := t["id"].(float64)
		msg, _ := t["message"].(string)
		st, _ := t["status"].(string)
		cat, _ := t["category"].(string)
		icon := statusIcon[st]
		if icon == "" {
			icon = "⚪"
		}
		if len(msg) > 70 {
			msg = msg[:70] + "…"
		}
		sb.WriteString(fmt.Sprintf("%s #%d [%s] %s — %s\n", icon, int(id), cat, st, msg))
	}
	sb.WriteString(fmt.Sprintf("\nTotal: %d ticket(s)", len(tickets)))
	return sb.String()
}

func handleVerTickets(input string, mem *Memory) string     { return queryTicketsFromGateway("") }
func handleVerTicketsAbiertos(input string, mem *Memory) string { return queryTicketsFromGateway("open") }
func handleVerTicketsCerrados(input string, mem *Memory) string { return queryTicketsFromGateway("closed") }

func handleNuevoTicket(input string, mem *Memory) string {
	return confirmDo(mem, "nuevo_ticket",
		"📝 ¿Querés que abra un nuevo ticket de soporte con tu descripción? (Sí / No)\n"+
			"Descripción: \""+input+"\"",
		func(answer string) string {
			go sendTicket(input, "soporte")
			return "✅ Ticket creado y enviado al equipo de IT."
		},
		func() string { return "❌ Ticket cancelado. Cuando quieras, decime cuál es el problema." },
	)
}

func handleWarehouseRequest(input string, mem *Memory) string {
	go sendTicket(input, "almacenes")
	return "📦 Solicitud enviada a Almacenes. Recibirás una notificación cuando esté listo."
}
// ========================= DIAGNÓSTICO HARDWARE =========================
func checkUSB(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-WmiObject -Class Win32_USBControllerDevice | ForEach-Object { [wmi]$_.Dependent } | Select-Object Name, Status")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return "❌ No se detectaron dispositivos USB."
	}
	if strings.Contains(strings.ToLower(string(out)), "ok") {
		return "✅ Dispositivos USB funcionando."
	}
	return "⚠️ Problemas con USB. Revisa controladores."
}

func checkMouse(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-WmiObject -Class Win32_PointingDevice | Select-Object Name, Status")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return "❌ No se detectó mouse."
	}
	if strings.Contains(strings.ToLower(string(out)), "ok") {
		return "✅ Mouse funciona correctamente."
	}
	return "⚠️ El mouse presenta problemas."
}

func checkKeyboard(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-WmiObject -Class Win32_Keyboard | Select-Object Name, Status")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return "❌ No se detectó teclado."
	}
	if strings.Contains(strings.ToLower(string(out)), "ok") {
		return "✅ Teclado funciona correctamente."
	}
	return "⚠️ El teclado presenta problemas."
}

func checkVideo(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-WmiObject -Class Win32_VideoController | Select-Object Name, Status")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	if strings.Contains(strings.ToLower(string(out)), "ok") {
		return "✅ Video funcionando."
	}
	return "⚠️ Revisa controladores de video."
}

func checkAudio(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-WmiObject -Class Win32_SoundDevice | Select-Object Name, Status")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	if strings.Contains(strings.ToLower(string(out)), "ok") {
		return "✅ Audio funcionando."
	}
	return "⚠️ Revisa controladores de audio."
}

func checkMicrophone(input string, mem *Memory) string {
	exec.Command("control", "mmsys.cpl", ",1").Run()
	return "🎤 Abriendo configuración de micrófono."
}

func checkShutdown(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-WinEvent -FilterHashtable @{LogName='System'; ID=41,1074,6008} -MaxEvents 5 | Select-Object TimeCreated, Message | ConvertTo-Json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "📋 Últimos apagados:\n" + string(out)
}

func checkBattery(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-WmiObject -Class Win32_Battery | Select-Object Name, EstimatedChargeRemaining, BatteryStatus")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "🔋 Estado de batería:\n" + string(out)
}

func fixPrinter(input string, mem *Memory) string {
	go func() {
		if err := psRun("Restart-Service -Name Spooler -Force"); err != "" && strings.Contains(err, "Error") {
			logger.Printf("[WARN] Restart Spooler: %s", err)
		}
		psRun("Remove-Item -Path 'C:\\Windows\\System32\\spool\\PRINTERS\\*' -Force -ErrorAction SilentlyContinue")
		sendNotification("Impresora", "Spooler reiniciado y cola limpiada.")
	}()
	logAction("fix_printer", nil, "Spooler reiniciado", false, nil)
	return "🖨️ Reiniciando servicio de impresión..."
}

func fixNetwork(input string, mem *Memory) string {
	go func() {
		for _, args := range [][]string{{"/release"}, {"/renew"}, {"/flushdns"}} {
			if err := hiddenCmd("ipconfig", args...).Run(); err != nil {
				logger.Printf("[WARN] ipconfig %s: %v", args[0], err)
			}
		}
		sendNotification("Red", "IP renovada y DNS vaciado.")
	}()
	logAction("fix_network", nil, "Renovación IP", false, nil)
	return "🌐 Diagnosticando red..."
}

func handleSlowPC(input string, mem *Memory) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "mantenimiento",
		Description:       "¿Qué nivel de limpieza deseas?\n🔹 'superficial' (solo temporales)\n🔸 'profundo' (cierra apps pesadas)\nEscribe 'cancelar' para salir.",
		ExpectedResponses: []string{"superficial", "profundo"},
		OnConfirm: func(resp string) string {
			if strings.Contains(resp, "profundo") {
				return performMaintenance(true)
			}
			return performMaintenance(false)
		},
		OnDeny: func() string {
			return "❌ Mantenimiento cancelado."
		},
	}
	return mem.PendingConfirmation.Description
}

func startMaintenance(input string, mem *Memory) string {
	lower := strings.ToLower(input)
	if strings.Contains(lower, "superficial") {
		return performMaintenance(false)
	} else if strings.Contains(lower, "profundo") {
		return performMaintenance(true)
	}
	return "No entendí. Responde 'superficial' o 'profundo'."
}

func performMaintenance(deep bool) string {
	go func() {
		tempPath := os.Getenv("TEMP")
		psRun(fmt.Sprintf("Remove-Item -Path '%s\\*' -Recurse -Force -ErrorAction SilentlyContinue", tempPath))
		psRun("Clear-RecycleBin -Force -ErrorAction SilentlyContinue")
		if deep {
			psRun("Get-Process | Where-Object {$_.CPU -gt 100 -and $_.Name -notin @('System','Idle','explorer','csrss','winlogon')} | Stop-Process -Force")
			if err := hiddenCmd("defrag", "C:", "/O").Run(); err != nil {
				logger.Printf("[WARN] defrag: %v", err)
			}
			sendNotification("Mantenimiento profundo", "Recursos liberados y disco optimizado.")
		} else {
			sendNotification("Mantenimiento superficial", "Archivos temporales y papelera limpiados.")
		}
	}()
	logAction("maintenance", map[string]interface{}{"deep": deep}, "Mantenimiento ejecutado", false, nil)
	if deep {
		return "🔧 Iniciando mantenimiento profundo."
	}
	return "🧹 Iniciando limpieza superficial."
}

func criticalTicket(input string, mem *Memory) string {
	go sendTicket("Pantalla azul - "+input, "crítica")
	return "⚠️ Ticket de emergencia creado."
}

func fixTime(input string, mem *Memory) string {
	cmd := hiddenCmd("w32tm", "/resync")
	if err := cmd.Run(); err != nil {
		logger.Printf("[WARN] w32tm /resync: %v", err)
		return "⚠️ No se pudo sincronizar la hora (¿ejecutando como administrador?)."
	}
	return "✅ Hora sincronizada."
}

// calculate es alias de calculateAdvanced para compatibilidad
func calculate(input string, mem *Memory) string { return calculateAdvanced(input, mem) }

// openProgram es alias de openAnyProgram para compatibilidad
func openProgram(input string, mem *Memory) string { return openAnyProgram(input, mem) }

func unlockUserAccount(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?i)(?:desbloquea|reactiva|activa)\s+(?:usuario|cuenta\s+)?(\w+)`)
	matches := re.FindStringSubmatch(input)
	if len(matches) < 2 {
		return "Ejemplo: 'desbloquea usuario juan'"
	}
	username := matches[1]
	cmd := exec.Command("net", "user", username, "/active:yes")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("❌ Error: %s", out)
	}
	logAction("unlock_user", map[string]interface{}{"username": username}, "Cuenta desbloqueada", true, &RollbackInfo{
		Command: "net", Args: []string{"user", username, "/active:no"}, Description: "Bloquear cuenta",
	})
	return fmt.Sprintf("✅ Usuario %s desbloqueado.", username)
}

func changeUserPassword(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:cambia|contraseña)\s+(?:de\s+)?(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Ej: 'cambia contraseña de juan'"
	}
	username := match[1]
	newPass := generateRandomKey(12)
	cmd := hiddenCmd("net", "user", username, newPass)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Sprintf("❌ No se pudo cambiar la contraseña: %s", strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("✅ Nueva contraseña: %s", newPass)
}

func createLocalUser(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:crea|crear)\s+(?:usuario|cuenta)\s+(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Ej: 'crea usuario juan'"
	}
	username := match[1]
	password := generateRandomKey(12)
	cmdAdd := hiddenCmd("net", "user", username, password, "/add")
	if out, err := cmdAdd.CombinedOutput(); err != nil {
		return fmt.Sprintf("❌ No se pudo crear el usuario: %s", strings.TrimSpace(string(out)))
	}
	cmdGrp := hiddenCmd("net", "localgroup", "Usuarios", username, "/add")
	if err := cmdGrp.Run(); err != nil {
		logger.Printf("[WARN] localgroup add %s: %v", username, err)
	}
	logAction("create_user", map[string]interface{}{"username": username}, "Usuario creado", true, &RollbackInfo{
		Command: "net", Args: []string{"user", username, "/delete"}, Description: "Eliminar usuario",
	})
	return fmt.Sprintf("✅ Usuario %s creado. Contraseña: %s", username, password)
}

func deleteUser(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:eliminar|borrar)\s+(?:usuario|cuenta)\s+(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Ej: 'eliminar usuario juan'"
	}
	username := match[1]
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "delete_user",
		Description:       fmt.Sprintf("¿Eliminar permanentemente al usuario '%s'? (SI/NO)", username),
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("net", "user", username, "/delete").Run()
			logAction("delete_user", map[string]interface{}{"username": username}, "Usuario eliminado", false, nil)
			return "✅ Usuario eliminado."
		},
	}
	return mem.PendingConfirmation.Description
}

func listUsers(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-LocalUser | Select-Object Name, Enabled | ConvertTo-Json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "👥 **Usuarios locales:**\n" + string(out)
}

func userGroups(input string, mem *Memory) string {
	re := regexp.MustCompile(`grupos del usuario\s+(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'grupos del usuario nombre'"
	}
	username := match[1]
	cmd := exec.Command("net", "user", username)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "📋 Grupos de " + username + ":\n" + string(out)
}

func addUserToGroup(input string, mem *Memory) string {
	re := regexp.MustCompile(`agregar a grupo\s+(\w+)\s+(?:al grupo\s+)?(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 3 {
		return "Uso: 'agregar a grupo usuario grupo'"
	}
	user, group := match[1], match[2]
	exec.Command("net", "localgroup", group, user, "/add").Run()
	logAction("add_to_group", map[string]interface{}{"user": user, "group": group}, "Agregado", true, &RollbackInfo{
		Command: "net", Args: []string{"localgroup", group, user, "/delete"}, Description: "Quitar del grupo",
	})
	return fmt.Sprintf("✅ %s agregado a %s.", user, group)
}

func fixZimbra(input string, mem *Memory) string {
	correctURL := "https://mail.afe.com.uy"
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Test-NetConnection -ComputerName mail.afe.com.uy -Port 443 -InformationLevel Quiet")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "True" {
		exec.Command("ipconfig", "/flushdns").Run()
		exec.Command("netsh", "winsock", "reset").Run()
		return "❌ No se puede alcanzar mail.afe.com.uy. Reinicia y prueba.\n📌 URL: " + correctURL
	}
	return "✅ Servidor de correo accesible. Usa " + correctURL
}

func diagnoseDiskSpace(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-PSDrive -PSProvider FileSystem | Where-Object {$_.Used -gt 0} | Select-Object Name, @{Name='Free(GB)';Expression={[math]::Round($_.Free/1GB,2)}} | ConvertTo-Json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "📊 Espacio en discos:\n" + string(out)
}

func getDiskSpaceInfo(input string, mem *Memory) string { return diagnoseDiskSpace(input, mem) }

func requestDiskCleanup(input string, mem *Memory) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "limpiar_disco",
		Description:       "¿Ejecutar limpieza de disco? (SI/NO)",
		ExpectedResponses: []string{"si", "sí", "no"},
		OnConfirm: func(resp string) string {
			go exec.Command("cleanmgr", "/verylowdisk").Run()
			return "🧹 Limpiando disco..."
		},
	}
	return mem.PendingConfirmation.Description
}

func findLargeFiles(input string, mem *Memory) string {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Printf("[ERROR] findLargeFiles panic: %v", r)
			}
		}()
		cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
			"Get-ChildItem -Path C:\\ -Recurse -ErrorAction SilentlyContinue -File | Sort-Object Length -Descending | Select-Object -First 10 | ForEach-Object { $_.FullName + ' [' + [math]::Round($_.Length/1MB,2) + ' MB]' }")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, _ := cmd.Output()
		appendMessage("📁 Archivos más grandes:\n" + string(out))
	}()
	return "🔍 Buscando archivos grandes..."
}

func analyzeDisk(input string, mem *Memory) string {
	drive := "C:"
	re := regexp.MustCompile(`analizar disco\s+([A-Za-z]:)`)
	if matches := re.FindStringSubmatch(strings.ToLower(input)); len(matches) == 2 {
		drive = strings.ToUpper(matches[1])
	}
	cmd := exec.Command("chkdsk", drive)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "📀 Análisis de " + drive + ":\n" + string(out)
}

func defragDisk(input string, mem *Memory) string {
	drive := "C:"
	re := regexp.MustCompile(`desfragmentar\s+([A-Za-z]:)`)
	if matches := re.FindStringSubmatch(strings.ToLower(input)); len(matches) == 2 {
		drive = strings.ToUpper(matches[1])
	}
	go func() {
		exec.Command("defrag", drive, "/O").Run()
		sendNotification("Desfragmentación", drive+" completada.")
	}()
	return fmt.Sprintf("🔄 Desfragmentando %s...", drive)
}

func formatDrive(input string, mem *Memory) string {
	re := regexp.MustCompile(`formatear\s+([A-Za-z]:)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'formatear D:'"
	}
	drive := strings.ToUpper(match[1])
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "format",
		Description:       fmt.Sprintf("⚠️ ¿Formatear %s? ¡SE PERDERÁN DATOS! (SI/NO)", drive),
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("format", drive, "/FS:NTFS", "/Q", "/Y").Run()
			return "✅ Unidad formateada."
		},
	}
	return mem.PendingConfirmation.Description
}

func runDefenderScan(input string, mem *Memory) string {
	go exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Start-MpScan -ScanType QuickScan").Run()
	return "🛡️ Iniciando análisis rápido de Defender."
}

func diagnoseFirewall(input string, mem *Memory) string {
	cmd := exec.Command("netsh", "advfirewall", "show", "allprofiles")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	if strings.Contains(string(out), "State ON") {
		return "🔥 Firewall activado."
	}
	return "🔥 Firewall desactivado."
}

func repairFolderPermissions(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:permisos de|carpeta)\s+(.+)`)
	match := re.FindStringSubmatch(input)
	if len(match) < 2 {
		return "¿Qué carpeta?"
	}
	folder := strings.TrimSpace(match[1])
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "repair_perms",
		Description:       fmt.Sprintf("¿Restablecer permisos de '%s'? (SI/NO)", folder),
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("icacls", folder, "/grant", "Everyone:(OI)(CI)F", "/T").Run()
			return "✅ Permisos restablecidos."
		},
	}
	return mem.PendingConfirmation.Description
}

func checkBitLocker(input string, mem *Memory) string {
	cmd := exec.Command("manage-bde", "-status")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "🔐 Estado de BitLocker:\n" + string(out)
}

func clearPrintQueue(input string, mem *Memory) string {
	exec.Command("net", "stop", "spooler").Run()
	exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Remove-Item -Path 'C:\\Windows\\System32\\spool\\PRINTERS\\*' -Force -ErrorAction SilentlyContinue").Run()
	exec.Command("net", "start", "spooler").Run()
	return "🧹 Cola de impresión limpiada."
}

func restartSpooler(input string, mem *Memory) string {
	exec.Command("net", "stop", "spooler").Run()
	exec.Command("net", "start", "spooler").Run()
	return "🖨️ Spooler reiniciado."
}

func addNetworkPrinter(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:agregar|instalar)\s+impresora\s+(.+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'agregar impresora \\\\servidor\\nombre'"
	}
	path := strings.TrimSpace(match[1])
	exec.Command("rundll32", "printui.dll,PrintUIEntry", "/in", "/n", path).Start()
	return "✅ Agregando impresora..."
}

func sharePrinter(input string, mem *Memory) string {
	exec.Command("control", "printers").Run()
	return "🖨️ Abriendo carpeta de impresoras. Click derecho > Propiedades > Compartir."
}

func setDefaultPrinter(input string, mem *Memory) string {
	exec.Command("control", "printers").Run()
	return "🖨️ Click derecho sobre la impresora > Establecer como predeterminada."
}

func listPrinters(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-Printer | Select-Object Name, PrinterStatus, Shared | ConvertTo-Json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "🖨️ **Impresoras:**\n" + string(out)
}

func diagnoseProxy(input string, mem *Memory) string {
	cmd := exec.Command("netsh", "winhttp", "show", "proxy")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "🌐 Proxy:\n" + string(out)
}

func flushDNS(input string, mem *Memory) string {
	exec.Command("ipconfig", "/flushdns").Run()
	return "✅ Caché DNS vaciada."
}

func resetNetworkStack(input string, mem *Memory) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "reset_red",
		Description:       "¿Reiniciar pila TCP/IP? (SI/NO)",
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("netsh", "winsock", "reset").Run()
			exec.Command("netsh", "int", "ip", "reset").Run()
			return "🔄 Red reiniciada. Reinicia el PC."
		},
	}
	return mem.PendingConfirmation.Description
}

func changeDNS(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:cambiar dns a|poner dns)\s+([\d\.]+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'cambiar dns 8.8.8.8'"
	}
	newDNS := match[1]
	for _, corp := range corporateDNS {
		if newDNS == corp {
			return "⚠️ No se recomienda cambiar a DNS corporativos."
		}
	}
	exec.Command("netsh", "interface", "ip", "set", "dns", "Ethernet", "static", newDNS).Run()
	logAction("change_dns", map[string]interface{}{"dns": newDNS}, "DNS cambiado", true, &RollbackInfo{
		Command: "netsh", Args: []string{"interface", "ip", "set", "dns", "Ethernet", "dhcp"}, Description: "Restaurar DHCP",
	})
	return "✅ DNS cambiado a " + newDNS
}

func getWiFiPassword(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:contraseña|clave)\s+(?:wifi|wi-fi)?\s*(.+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'contraseña wifi NombreRed'"
	}
	ssid := strings.TrimSpace(match[1])
	cmd := exec.Command("netsh", "wlan", "show", "profile", ssid, "key=clear")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	rePass := regexp.MustCompile(`Contenido de la clave\s+:\s+(.+)`)
	if m := rePass.FindStringSubmatch(string(out)); len(m) == 2 {
		return fmt.Sprintf("🔑 Contraseña de '%s': %s", ssid, m[1])
	}
	return "❌ No se encontró el perfil WiFi."
}

func netstat(input string, mem *Memory) string {
	cmd := exec.Command("netstat", "-an")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	if len(out) > 1000 {
		out = out[:1000]
	}
	return "🌐 Conexiones activas:\n" + string(out)
}

func pingHost(input string, mem *Memory) string {
	re := regexp.MustCompile(`ping\s+([\w\.\-]+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'ping google.com'"
	}
	host := match[1]
	cmd := exec.Command("ping", "-n", "4", host)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "📡 " + string(out)
}

func tracertHost(input string, mem *Memory) string {
	re := regexp.MustCompile(`tracert\s+([\w\.\-]+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'tracert google.com'"
	}
	host := match[1]
	go func() {
		cmd := exec.Command("tracert", "-h", "15", host)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, _ := cmd.Output()
		appendMessage("🛜 Traza a " + host + ":\n" + string(out))
	}()
	return "🔍 Ejecutando tracert..."
}

func checkWindowsUpdates(input string, mem *Memory) string {
	go func() {
		exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
			"Install-Module PSWindowsUpdate -Force -AllowClobber; Import-Module PSWindowsUpdate; Get-WindowsUpdate -Install -AcceptAll -AutoReboot").Run()
		sendNotification("Actualizaciones", "Instalación completada.")
	}()
	return "⬇️ Buscando actualizaciones..."
}

func runSFC(input string, mem *Memory) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "sfc",
		Description:       "¿Ejecutar SFC /scannow? (SI/NO)",
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			go exec.Command("sfc", "/scannow").Run()
			return "🔧 Ejecutando SFC..."
		},
	}
	return mem.PendingConfirmation.Description
}

func scheduleChkdsk(input string, mem *Memory) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "chkdsk",
		Description:       "¿Programar CHKDSK para el próximo reinicio? (SI/NO)",
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("chkdsk", "C:", "/f").Run()
			return "✅ CHKDSK programado."
		},
	}
	return mem.PendingConfirmation.Description
}

func diagnoseRAM(input string, mem *Memory) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "ram",
		Description:       "¿Ejecutar diagnóstico de memoria? El PC se reiniciará. (SI/NO)",
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("mdsched.exe").Run()
			return "🔄 Reiniciando..."
		},
	}
	return mem.PendingConfirmation.Description
}

func manageStartupPrograms(input string, mem *Memory) string {
	exec.Command("taskmgr", "/7", "/startup").Run()
	return "📂 Abriendo Administrador de tareas > Inicio."
}

func getSystemInfo(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-ComputerInfo | Select-Object WindowsProductName, WindowsVersion, OsArchitecture, CsProcessors, CsTotalPhysicalMemory | ConvertTo-Json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "🖥️ Información del sistema:\n" + string(out)
}

func listEnvVars(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-ChildItem Env: | Select-Object Name, Value | ConvertTo-Json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "📋 Variables de entorno:\n" + string(out)
}

func registryQuery(input string, mem *Memory) string {
	exec.Command("regedit.exe").Start()
	return "📂 Abriendo Editor del Registro."
}

func forceGPUpdate(input string, mem *Memory) string {
	go exec.Command("gpupdate", "/force").Run()
	return "🔄 Actualizando directivas de grupo..."
}

func listScheduledTasks(input string, mem *Memory) string {
	exec.Command("taskschd.msc").Start()
	return "📅 Abriendo Programador de tareas."
}

func listServices(input string, mem *Memory) string {
	exec.Command("services.msc").Start()
	return "⚙️ Abriendo Servicios."
}

func listDrivers(input string, mem *Memory) string {
	exec.Command("devmgmt.msc").Start()
	return "🖥️ Abriendo Administrador de dispositivos."
}

func rollbackDriver(input string, mem *Memory) string {
	exec.Command("devmgmt.msc").Start()
	return "🖥️ Selecciona dispositivo > Controlador > Revertir."
}

func batteryReport(input string, mem *Memory) string {
	reportPath := filepath.Join(os.Getenv("TEMP"), "battery-report.html")
	exec.Command("powercfg", "/batteryreport", "/output", reportPath).Run()
	exec.Command("cmd", "/c", "start", reportPath).Run()
	return "🔋 Generando informe de batería."
}

func energyReport(input string, mem *Memory) string {
	reportPath := filepath.Join(os.Getenv("TEMP"), "energy-report.html")
	exec.Command("powercfg", "/energy", "/output", reportPath, "/duration", "60").Run()
	exec.Command("cmd", "/c", "start", reportPath).Run()
	return "⚡ Generando informe de energía."
}

func listProcesses(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-Process | Sort-Object CPU -Descending | Select-Object -First 15 Name, CPU | ConvertTo-Json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "📊 Procesos:\n" + string(out)
}

func killProcess(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:finaliza|mata)\s+proceso\s+(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'finalizar proceso chrome'"
	}
	proc := match[1]
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "kill",
		Description:       fmt.Sprintf("¿Finalizar %s? (SI/NO)", proc),
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("taskkill", "/IM", proc+".exe", "/F").Run()
			return "✅ Proceso finalizado."
		},
	}
	return mem.PendingConfirmation.Description
}

func startService(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:inicia|iniciar)\s+servicio\s+(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'iniciar servicio Spooler'"
	}
	exec.Command("net", "start", match[1]).Run()
	return "✅ Servicio iniciado."
}

func stopService(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:deten|para)\s+servicio\s+(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'detener servicio Spooler'"
	}
	exec.Command("net", "stop", match[1]).Run()
	return "✅ Servicio detenido."
}

func restartService(input string, mem *Memory) string {
	re := regexp.MustCompile(`reiniciar servicio\s+(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'reiniciar servicio Spooler'"
	}
	svc := match[1]
	exec.Command("net", "stop", svc).Run()
	exec.Command("net", "start", svc).Run()
	return "✅ Servicio reiniciado."
}

func setServiceStartup(input string, mem *Memory) string {
	re := regexp.MustCompile(`cambiar inicio de servicio\s+(\w+)\s+a\s+(\w+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 3 {
		return "Uso: 'cambiar inicio de servicio Spooler a Automatic'"
	}
	svc, startType := match[1], match[2]
	var startup string
	switch startType {
	case "automatic", "auto": startup = "auto"
	case "manual", "demand": startup = "demand"
	case "disabled", "disable": startup = "disabled"
	default: return "Tipo no válido. Use Automatic, Manual o Disabled."
	}
	exec.Command("sc", "config", svc, "start=", startup).Run()
	return fmt.Sprintf("✅ Servicio %s configurado como %s.", svc, startType)
}

func listInstalledPrograms(input string, mem *Memory) string {
	go func() {
		cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
			"Get-ItemProperty HKLM:\\Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\*, HKLM:\\Software\\Wow6432Node\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\* | Where-Object {$_.DisplayName} | Select-Object DisplayName | ConvertTo-Json")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, _ := cmd.Output()
		appendMessage("📦 Programas:\n" + string(out))
	}()
	return "🔍 Buscando programas instalados..."
}

func uninstallProgram(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:desinstala|desinstalar)\s+programa\s+(.+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'desinstalar programa Nombre'"
	}
	programName := strings.TrimSpace(match[1])
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		fmt.Sprintf(`$app = Get-ItemProperty HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*, HKLM:\Software\Wow6432Node\Microsoft\Windows\CurrentVersion\Uninstall\* | Where-Object {$_.DisplayName -like '*%s*'} | Select-Object -First 1; if($app.UninstallString) { $app.UninstallString } else { '' }`, programName))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	uninstallString := strings.TrimSpace(string(out))
	if uninstallString == "" {
		return fmt.Sprintf("❌ No se encontró '%s'.", programName)
	}
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "uninstall",
		Description:       fmt.Sprintf("¿Desinstalar '%s'? (SI/NO)", programName),
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("cmd", "/c", uninstallString).Start()
			return "✅ Desinstalador iniciado."
		},
	}
	return mem.PendingConfirmation.Description
}

func installProgram(input string, mem *Memory) string {
	re := regexp.MustCompile(`instalar programa\s+(.+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'instalar programa C:\\ruta\\instalador.msi'"
	}
	installer := strings.TrimSpace(match[1])
	if _, err := os.Stat(installer); err != nil {
		return "❌ No se encuentra el instalador."
	}
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "install",
		Description:       fmt.Sprintf("¿Ejecutar instalador '%s'? (SI/NO)", installer),
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("msiexec", "/i", installer).Start()
			return "✅ Instalador iniciado."
		},
	}
	return mem.PendingConfirmation.Description
}

func createRestorePoint(input string, mem *Memory) string {
	desc := "Frank_" + time.Now().Format("20060102_150405")
	exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		fmt.Sprintf("Checkpoint-Computer -Description '%s' -RestorePointType MODIFY_SETTINGS", desc)).Run()
	return "✅ Punto de restauración creado: " + desc
}

func openSystemRestore(input string, mem *Memory) string {
	exec.Command("rstrui.exe").Start()
	return "♻️ Abriendo Restaurar Sistema."
}

func configureOutlookZimbra(input string, mem *Memory) string {
	exec.Command("outlook.exe", "/profile").Start()
	return "📧 Configuración Zimbra: IMAP mail.afe.com.uy SSL 993, SMTP mail.afe.com.uy SSL 465."
}

func getCPUTemperature(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-WmiObject MSAcpi_ThermalZoneTemperature -Namespace root/wmi | Select-Object -ExpandProperty CurrentTemperature")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	if len(out) == 0 {
		return "❌ No se pudo leer temperatura."
	}
	t, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	tempC := (t/10.0 - 273.15)
	return fmt.Sprintf("🌡️ Temperatura CPU: %.1f °C", tempC)
}

func getDiskSMART(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Get-PhysicalDisk | Select-Object FriendlyName, HealthStatus, OperationalStatus | ConvertTo-Json")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "📀 Estado SMART:\n" + string(out)
}

func enableRDP(input string, mem *Memory) string {
	exec.Command("reg", "add", "HKLM\\SYSTEM\\CurrentControlSet\\Control\\Terminal Server", "/v", "fDenyTSConnections", "/t", "REG_DWORD", "/d", "0", "/f").Run()
	exec.Command("netsh", "advfirewall", "firewall", "set", "rule", "name=\"Escritorio remoto\"", "new", "enable=yes").Run()
	return "✅ Escritorio Remoto habilitado."
}

func shareFolder(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:compartir|carpeta)\s+(.+)`)
	match := re.FindStringSubmatch(input)
	if len(match) < 2 {
		return "Uso: 'compartir carpeta C:\\Ruta'"
	}
	folder := strings.TrimSpace(match[1])
	shareName := filepath.Base(folder)
	exec.Command("net", "share", shareName+"="+folder, "/grant:Everyone,FULL").Run()
	return fmt.Sprintf("✅ Compartida como \\\\%s\\%s", pcName, shareName)
}

func lockWorkstation(input string, mem *Memory) string {
	exec.Command("rundll32.exe", "user32.dll,LockWorkStation").Run()
	return "🔒 Bloqueando PC."
}

func shutdownPC(input string, mem *Memory) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "apagar",
		Description:       "¿Apagar el equipo? (SI/NO)",
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("shutdown", "/s", "/t", "30", "/c", "Frank: Apagado en 30s").Run()
			return "⏳ Apagando en 30 segundos."
		},
	}
	return mem.PendingConfirmation.Description
}

func rebootPC(input string, mem *Memory) string {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	mem.PendingConfirmation = &ConfirmationRequest{
		Action:            "reiniciar",
		Description:       "¿Reiniciar el equipo? (SI/NO)",
		ExpectedResponses: []string{"si", "sí"},
		OnConfirm: func(resp string) string {
			exec.Command("shutdown", "/r", "/t", "30", "/c", "Frank: Reinicio en 30s").Run()
			return "🔄 Reiniciando en 30 segundos."
		},
	}
	return mem.PendingConfirmation.Description
}

func suspendPC(input string, mem *Memory) string {
	exec.Command("rundll32.exe", "powrprof.dll,SetSuspendState", "0", "1", "0").Run()
	return "💤 Suspendiendo..."
}

func getUptime(input string, mem *Memory) string {
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"(Get-Date) - (Get-CimInstance -ClassName Win32_OperatingSystem).LastBootUpTime | Select-Object Days, Hours, Minutes")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.Output()
	return "⏱️ Tiempo encendido:\n" + string(out)
}

func getIPAddress(input string, mem *Memory) string {
	addrs, _ := net.InterfaceAddrs()
	var ips []string
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			ips = append(ips, ipnet.IP.String())
		}
	}
	return "🌐 IPs: " + strings.Join(ips, ", ")
}

func openFirewallPort(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:abrir|abre)\s+puerto\s+(\d+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'abrir puerto 8080'"
	}
	port := match[1]
	ruleName := "Frank_Rule_" + port
	exec.Command("netsh", "advfirewall", "firewall", "add", "rule", "name="+ruleName, "dir=in", "action=allow", "protocol=TCP", "localport="+port).Run()
	return fmt.Sprintf("✅ Puerto %s abierto.", port)
}

func closeFirewallPort(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:cerrar|cierra)\s+puerto\s+(\d+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 2 {
		return "Uso: 'cerrar puerto 8080'"
	}
	port := match[1]
	ruleName := "Frank_Rule_" + port
	exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+ruleName).Run()
	return fmt.Sprintf("✅ Puerto %s cerrado.", port)
}

func openUSBPort(input string, mem *Memory) string {
	exec.Command("devmgmt.msc").Start()
	return "🔌 Abriendo Administrador de dispositivos."
}

// ========================= BÚSQUEDA DE ARCHIVOS =========================
func searchFile(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:buscar|busca|encuentra) (.*?)(?:$|\.)`)
	match := re.FindStringSubmatch(input)
	if len(match) < 2 {
		return "Dime el nombre, ej: 'busca factura.pdf'"
	}
	query := strings.TrimSpace(match[1])
	cancelCurrentSearch()
	ctx, cancel := context.WithCancel(context.Background())
	searchMu.Lock()
	currentSearchCancel = cancel
	isSearching = true
	searchMu.Unlock()
	appendMessage(fmt.Sprintf("🔍 Buscando '%s'...", query))
	go performSearchWithProgress(ctx, query, mem)
	return ""
}

func performSearchWithProgress(ctx context.Context, query string, mem *Memory) {
	defer func() {
		searchMu.Lock()
		isSearching = false
		currentSearchCancel = nil
		searchMu.Unlock()
	}()
	var drives []string
	for _, d := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		path := string(d) + ":\\"
		if _, err := os.Stat(path); err == nil {
			drives = append(drives, path)
		}
	}
	if len(drives) == 0 {
		appendMessage("❌ No se encontraron discos.")
		return
	}
	var results []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, root := range drives {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			psCmd := fmt.Sprintf(`Get-ChildItem -Path '%s' -Recurse -ErrorAction SilentlyContinue -Filter '*%s*' | Select-Object -ExpandProperty FullName | Select-Object -First 10`, root, query)
			cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", psCmd)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			out, _ := cmd.Output()
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			mu.Lock()
			for _, l := range lines {
				if l != "" && !contains(results, l) {
					results = append(results, l)
				}
			}
			mu.Unlock()
		}(root)
	}
	wg.Wait()
	if len(results) == 0 {
		appendMessage(fmt.Sprintf("❌ No encontré '%s'.", query))
		return
	}
	if len(results) > 10 {
		results = results[:10]
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ %d archivo(s):\n", len(results)))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, r))
	}
	appendMessage(sb.String())
	mem.mu.Lock()
	mem.LastSearchResults = results
	mem.mu.Unlock()
}

func cancelCurrentSearch() {
	searchMu.Lock()
	defer searchMu.Unlock()
	if currentSearchCancel != nil {
		currentSearchCancel()
		currentSearchCancel = nil
	}
	isSearching = false
}

// ========================= TICKETS DE SOPORTE =========================
func openSupportEmail(subject, body string) {
	// Formato específico para Zimbra
	zimbraURL := fmt.Sprintf(
		"https://mail.afe.com.uy/zimbra/mail?view=compose&to=soporte.it@afe.com.uy&subject=%s&body=%s",
		url.QueryEscape(subject),
		url.QueryEscape(body),
	)
	exec.Command("rundll32", "url.dll,FileProtocolHandler", zimbraURL).Start()
}

func enforceMaxSize(data string, maxBytes int) string {
	if len(data) <= maxBytes {
		return data
	}
	return data[:maxBytes] + "\n\n[TRUNCADO POR TAMAÑO]"
}

func collectSystemInfoForTicket() string {
	hostname, _ := os.Hostname()
	user := os.Getenv("USERNAME")
	osVersion := runtime.GOOS
	uptimeInfo := getUptime("", &memory)
	ipInfo := getIPAddress("", &memory)
	diskInfo := diagnoseDiskSpace("", &memory)

	userProfileMutex.RLock()
	profileInfo := fmt.Sprintf(
		"Nombre: %s %s\nApodo: %s\nDepartamento: %s\nÁrea: %s\nCorreo: %s\nTeléfono: %s\nInterno: %s\nUbicación: %s",
		userProfile.FirstName, userProfile.LastName, userProfile.Nickname,
		userProfile.Department, userProfile.Area, userProfile.Email,
		userProfile.Phone, userProfile.InternalPhone, userProfile.OfficeLocation,
	)
	userProfileMutex.RUnlock()

	fullData := fmt.Sprintf(
		"=== INFORMACIÓN DEL SISTEMA ===\n"+
			"Equipo: %s\nUsuario: %s\nOS: %s\nArquitectura: %s\n"+
			"Agente: %s %s\nFecha: %s\n\n"+
			"=== PERFIL DE USUARIO ===\n%s\n\n"+
			"%s\n\n%s\n\n%s\n",
		hostname, user, osVersion, runtime.GOARCH,
		AgentName, AgentVersion, time.Now().Format(time.RFC1123),
		profileInfo,
		uptimeInfo, ipInfo, diskInfo,
	)
	return enforceMaxSize(fullData, 20*1024*1024)
}

func generateDiagnosticFile() string {
	filePath := filepath.Join(os.Getenv("TEMP"), "frank_diagnostico.txt")
	content := collectSystemInfoForTicket()
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		logger.Printf("[ERROR] No se pudo generar archivo: %v", err)
		return ""
	}
	return filePath
}
func createSystemReportFile() (string, error) {
	content := collectSystemInfoForTicket()
	tmpFile := filepath.Join(os.TempDir(), "frank_report_"+time.Now().Format("20060102_150405")+".txt")
	err := os.WriteFile(tmpFile, []byte(content), 0600)
	if err != nil {
		return "", err
	}
	return tmpFile, nil
}

func createSupportTicket(userMessage string) {
	reportPath, err := createSystemReportFile()
	if err != nil {
		sendNotification(AgentName, "No se pudo generar el informe técnico.")
		return
	}
	diagFile := generateDiagnosticFile()
	subject := fmt.Sprintf("[%s %s] Soporte IT - %s", AgentName, AgentVersion, time.Now().Format("02/01/2006 15:04"))
	body := fmt.Sprintf(
		"Problema reportado:\n%s\n\n"+
			"Informe técnico generado en:\n%s\n",
		userMessage, reportPath,
	)
	if diagFile != "" && diagFile != reportPath {
		body += fmt.Sprintf("\nArchivo de diagnóstico adicional:\n%s\n", diagFile)
	}
	body += "\nPor favor adjunta los archivos antes de enviar."
	openSupportEmail(subject, body)
	// Abrir la carpeta con los archivos para facilitar el adjunto
	exec.Command("explorer", filepath.Dir(reportPath)).Start()
	sendNotification(AgentName, "Cliente de correo abierto. Carpeta con archivos adjuntos abierta.")
}

func maybeOfferTicket(userInput string) {
	lower := strings.ToLower(userInput)
	keywords := []string{
		"no funciona", "error", "no anda", "roto", "falla", "problema",
		"no puedo", "no carga", "lento", "congelado", "no responde",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			memory.mu.Lock()
			if memory.PendingConfirmation == nil {
				memory.PendingConfirmation = &ConfirmationRequest{
					Action:            "ticket",
					Description:       "¿Quieres que genere un ticket de soporte con esta información? (SI/NO)",
					ExpectedResponses: []string{"si", "sí"},
					OnConfirm: func(resp string) string {
						createSupportTicket(userInput)
						return "📧 Abriendo cliente de correo..."
					},
					OnDeny: func() string {
						return "Ok, no se generará el ticket."
					},
				}
			}
			memory.mu.Unlock()
			return
		}
	}
}

// ========================= RESPUESTAS SOCIALES =========================
func greetUser(input string, mem *Memory) string {
	name := getDisplayName()
	variants := []string{
		fmt.Sprintf("¡Hola %s! Soy Frank. ¿En qué puedo ayudarte?", name),
		fmt.Sprintf("¡Buen día, %s! Cuéntame tu problema o solicitud.", name),
		fmt.Sprintf("¡Saludos, %s! Dime qué necesitas.", name),
	}
	return variants[mathrand.Intn(len(variants))]
}

func thankUser(input string, mem *Memory) string {
	name := getDisplayName()
	variants := []string{
		fmt.Sprintf("¡De nada, %s! ¿Algo más?", name),
		"¡Un placer!",
		fmt.Sprintf("¡Gracias a ti, %s!", name),
	}
	return variants[mathrand.Intn(len(variants))]
}

func showHelp(input string, mem *Memory) string {
	return fmt.Sprintf(`🤖 **%s %s** — Habla naturalmente, ejemplos:

💬 "mi pc va lenta" / "no tengo internet" / "dame mi ip"
💬 "abrir chrome" / "calculá 45*7" / "raíz de 144"
💬 "analizar wazuh" / "generar inventario" / "generar contraseña"

📋 CATEGORÍAS:
  🖥️  Hardware:    USB, mouse, teclado, video, audio, temperatura
  🌐  Red:         IP, ping, DNS, WiFi, tracert, velocidad
  💾  Disco:       espacio, limpieza, SMART, defrag, grandes archivos
  🖨️  Impresoras:  cola, spooler, agregar, listar
  👤  Usuarios:    crear, eliminar, desbloquear, contraseña, grupos
  🔒  Seguridad:   firewall, BitLocker, Defender, certificados
  ⚙️  Sistema:     info, registro, GPO, servicios, drivers
  📊  Diagnóstico: RAM, CPU, BSOD, eventos, completo
  📦  Inventario:  generar inventario, ver inventario
  🛡️  Logs:        analizar wazuh, analizar ocs, logs frank
  🧮  Calcular:    45*7, raíz de 144, seno 30, 15%%
  🎯  Social:      cómo estás, chiste, frase motivacional

Escribe 'ayuda' en cualquier momento para ver esto.`, AgentName, AgentVersion)
}

func defaultResponse(input string, mem *Memory) string {
	// Si hay contexto reciente y el input es ambiguo, orientar al usuario.
	if isAmbiguousInput(input) {
		if ctx := getRecentContext(); ctx != "" {
			mem.mu.RLock()
			lastTopic := mem.LastTopic
			mem.mu.RUnlock()
			if lastTopic != "" {
				return fmt.Sprintf("No entendí a qué te referís. Veníamos hablando de \"%s\". ¿Querés continuar con eso o necesitás otra cosa?", lastTopic)
			}
			return "No entendí bien. ¿Podés ser más específico? Escribe 'ayuda' para ver ejemplos."
		}
	}
	// Buscar intenciones similares y sugerirlas.
	suggestions := findSimilarIntents(input, 3)
	if suggestions != "" {
		return "No entendí exactamente. ¿Querías decir:\n" + suggestions + "\nEscribe 'ayuda' para ver todos los comandos."
	}
	return "No entendí. Escribe 'ayuda' para ver ejemplos de lo que puedo hacer."
}

func findSimilarIntents(input string, n int) string {
	examples := map[string]string{
		"pc lenta":          "\"mi pc va lenta\"",
		"no tengo internet": "\"no tengo internet\"",
		"dame mi ip":        "\"dame mi ip\"",
		"abrir chrome":      "\"abrir chrome\"",
		"calcula":           "\"calculá 10*5\"",
		"wazuh":             "\"analizar wazuh\"",
		"inventario":        "\"generar inventario\"",
		"disco lleno":       "\"mi disco está lleno\"",
		"usuario":           "\"desbloquear usuario juan\"",
		"impresora":         "\"la impresora no imprime\"",
	}
	var suggestions []string
	lower := strings.ToLower(input)
	for kw, suggestion := range examples {
		if jaccardSimilarity(lower, kw) > 0.2 {
			suggestions = append(suggestions, "  • "+suggestion)
			if len(suggestions) >= n {
				break
			}
		}
	}
	return strings.Join(suggestions, "\n")
}

// getRecentContext devuelve un resumen de las últimas interacciones de la sesión.
// Lee desde chatHistoryData (historial persistente) para incluir también mensajes
// de sesiones anteriores cargadas al inicio.
func getRecentContext() string {
	historyMutex.RLock()
	msgs := chatHistoryData.Messages
	historyMutex.RUnlock()

	start := len(msgs) - 10
	if start < 0 {
		start = 0
	}
	recent := msgs[start:]
	if len(recent) == 0 {
		return ""
	}
	var parts []string
	for _, m := range recent {
		content := m.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		switch m.Role {
		case "user":
			parts = append(parts, "Usuario dijo: "+content)
		case "assistant":
			parts = append(parts, "Frank respondió: "+content)
		}
	}
	return strings.Join(parts, ", luego ")
}

// isAmbiguousInput detecta si el input es demasiado corto o genérico para
// procesarse sin contexto conversacional previo.
func isAmbiguousInput(s string) bool {
	s = strings.TrimSpace(strings.ToLower(removeAccents(s)))
	ambiguous := []string{
		"si", "sí", "ok", "dale", "bueno", "eso", "eso mismo", "lo mismo",
		"que", "que si", "y eso", "anda", "ya", "listo", "bien", "claro",
		"obvio", "a ver", "veamos", "continua", "seguí", "sigue", "mas",
		"otro", "otra", "lo anterior", "repite", "repetir",
	}
	for _, a := range ambiguous {
		if s == a {
			return true
		}
	}
	return len(strings.Fields(s)) <= 1 && len(s) <= 5
}

// ========================= RESPUESTAS SOCIALES v3.0 =========================

func howAreYou(input string, mem *Memory) string {
	uptime := time.Since(agentStartTime).Round(time.Minute)
	return fmt.Sprintf("¡Estoy excelente! Llevo %s activo, %d comandos procesados, %d errores. ¿En qué puedo ayudarte?",
		uptime, telemetry.CommandsExecuted, telemetry.ErrorsCount)
}

func tellJoke(input string, mem *Memory) string {
	jokes := []string{
		"¿Por qué el programador dejó su trabajo? Porque no le daban vacaciones... solo strings vacíos. 😄",
		"¿Cómo sabes que un técnico de IT estuvo en tu casa? Porque el WiFi funciona pero el DVD está al revés.",
		"Un usuario llama: 'El CD no entra en la computadora'. El técnico: '¿Lo giró?' Usuario: '¿Cuántas veces?'",
		"¿Qué le dice un bit al otro? Nos vemos en el bus.",
		"Mi contraseña es 'incorrecto'. Así cuando me dicen que está mal, me dicen la respuesta.",
	}
	return jokes[mathrand.Intn(len(jokes))]
}

func getMotivation(input string, mem *Memory) string {
	phrases := []Phrase{}
	for _, cat := range trayPhrases {
		phrases = append(phrases, cat...)
	}
	if len(phrases) == 0 {
		return "¡Vas muy bien! Cada problema resuelto es un logro."
	}
	return "💪 " + weightedRandom(phrases)
}

func agentUptime(input string, mem *Memory) string {
	uptime := time.Since(agentStartTime).Round(time.Second)
	h := int(uptime.Hours())
	m := int(uptime.Minutes()) % 60
	s := int(uptime.Seconds()) % 60
	return fmt.Sprintf("⏱️ Llevo %dh %dm %ds activo. Comandos: %d | Errores: %d | Tickets: %d",
		h, m, s, telemetry.CommandsExecuted, telemetry.ErrorsCount, telemetry.TicketsCreated)
}

func whoAmI(input string, mem *Memory) string {
	return fmt.Sprintf(`🤖 **%s v%s**
%s

Soy el asistente IT autónomo de tu organización.
Funciono sin conexión a gateway, proceso lenguaje natural en español,
tengo NLU de 3 capas (keywords + Naive Bayes + ClosestMatch),
y puedo ejecutar más de 100 acciones técnicas directamente en Windows.

Activo desde: %s
Equipo: %s | Usuario: %s`,
		AgentName, AgentVersion, AgentFullName,
		agentStartTime.Format("02/01/2006 15:04"),
		pcName, currentUser)
}

// listKnownPeers devuelve la lista de agentes Frank descubiertos en la red LAN.
func listKnownPeers(_ string, _ *Memory) string {
	p2pPeersMu.RLock()
	snapshot := make([]*AgentPeer, 0, len(p2pPeers))
	for _, p := range p2pPeers {
		snapshot = append(snapshot, p)
	}
	p2pPeersMu.RUnlock()

	if len(snapshot) == 0 {
		return "No hay agentes Frank detectados en la red local en este momento.\n" +
			"El descubrimiento P2P está activo — los equipos se anuncian cada 30 s."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🌐 **Agentes Frank en la red** (%d)\n\n", len(snapshot)))
	for i, p := range snapshot {
		loc := p.Location
		if loc == "" {
			loc = "ubicación desconocida"
		}
		age := time.Since(p.LastSeen).Round(time.Second)
		sb.WriteString(fmt.Sprintf("%d. **%s** — %s (v%s)\n", i+1, p.Name, loc, p.Version))
		sb.WriteString(fmt.Sprintf("   IP: %s:%d | Visto hace: %s\n", p.IP, p.Port, age))
		if p.Gateway != "" {
			sb.WriteString(fmt.Sprintf("   Gateway: %s\n", p.Gateway))
		}
	}
	return sb.String()
}

// showActiveAppStats devuelve las apps más usadas según GetForegroundWindow.
func showActiveAppStats(_ string, _ *Memory) string {
	fgTrackerMu.Lock()
	current := fgCurrentApp
	usage := make(map[string]time.Duration, len(fgAppUsage))
	for k, v := range fgAppUsage {
		usage[k] = v
	}
	fgTrackerMu.Unlock()

	if len(usage) == 0 {
		if current != "" {
			return fmt.Sprintf("Ventana activa ahora: **%s**\n(Estadísticas de uso disponibles después de unos minutos.)", current)
		}
		return "Todavía no hay datos de uso de aplicaciones. Volvé a preguntar en unos minutos."
	}

	type kv struct {
		app string
		dur time.Duration
	}
	var sorted []kv
	for k, v := range usage {
		sorted = append(sorted, kv{k, v})
	}
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].dur > sorted[i].dur {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("📊 **Uso de aplicaciones (hoy)**\n\n")
	if current != "" {
		sb.WriteString(fmt.Sprintf("▶ Ahora: **%s**\n\n", current))
	}
	limit := 8
	if len(sorted) < limit {
		limit = len(sorted)
	}
	for i, item := range sorted[:limit] {
		mins := int(item.dur.Minutes())
		bar := strings.Repeat("█", mins/2)
		if bar == "" {
			bar = "▏"
		}
		sb.WriteString(fmt.Sprintf("%d. %s — %dm %s\n", i+1, item.app, mins, bar))
	}
	sb.WriteString("\n_Solo se mide el título de la ventana activa. No se registra contenido._")
	return sb.String()
}

// ========================= FUNCIONES NUEVAS v3.0 =========================

func calculateAdvanced(input string, mem *Memory) string {
	lower := strings.ToLower(removeAccents(input))

	// Raíz cuadrada
	if m := reMatch1(`(?:raiz|raíz) de (\d+(?:\.\d+)?)`, lower); m != "" {
		n, _ := strconv.ParseFloat(m, 64)
		return fmt.Sprintf("🧮 √%.0f = %.4f", n, math.Sqrt(n))
	}
	// Potencia
	if m := reMatch1(`(\d+(?:\.\d+)?) (?:al cuadrado|cuadrado)`, lower); m != "" {
		n, _ := strconv.ParseFloat(m, 64)
		return fmt.Sprintf("🧮 %.0f² = %.0f", n, n*n)
	}
	if re := regexp.MustCompile(`(\d+(?:\.\d+)?) \^ (\d+(?:\.\d+)?)`); re.MatchString(lower) {
		mm := re.FindStringSubmatch(lower)
		a, _ := strconv.ParseFloat(mm[1], 64)
		b, _ := strconv.ParseFloat(mm[2], 64)
		return fmt.Sprintf("🧮 %.0f^%.0f = %.4f", a, b, math.Pow(a, b))
	}
	// Seno/coseno/tangente
	for _, fn := range []string{"seno", "coseno", "tangente"} {
		if m := reMatch1(fn+` (?:de )?(\d+(?:\.\d+)?)`, lower); m != "" {
			deg, _ := strconv.ParseFloat(m, 64)
			rad := deg * math.Pi / 180
			var result float64
			var symbol string
			switch fn {
			case "seno":
				result, symbol = math.Sin(rad), "sin"
			case "coseno":
				result, symbol = math.Cos(rad), "cos"
			case "tangente":
				result, symbol = math.Tan(rad), "tan"
			}
			return fmt.Sprintf("🧮 %s(%.0f°) = %.6f", symbol, deg, result)
		}
	}
	// Log
	if m := reMatch1(`(?:log|logaritmo) (?:de )?(\d+(?:\.\d+)?)`, lower); m != "" {
		n, _ := strconv.ParseFloat(m, 64)
		return fmt.Sprintf("🧮 log(%.0f) = %.6f", n, math.Log10(n))
	}
	// Porcentaje: N% de M
	if re := regexp.MustCompile(`(\d+(?:\.\d+)?)%? (?:de|porciento de) (\d+(?:\.\d+)?)`); re.MatchString(lower) {
		mm := re.FindStringSubmatch(lower)
		pct, _ := strconv.ParseFloat(mm[1], 64)
		total, _ := strconv.ParseFloat(mm[2], 64)
		return fmt.Sprintf("🧮 %.0f%% de %.0f = %.2f", pct, total, pct/100*total)
	}
	// Operación básica via PowerShell
	re := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*([+\-*/])\s*(\d+(?:\.\d+)?)`)
	if match := re.FindStringSubmatch(input); len(match) >= 4 {
		expr := match[1] + match[2] + match[3]
		result := psRun(expr)
		return fmt.Sprintf("🧮 %s = %s", expr, result)
	}
	return "Ejemplo: 'calculá 12*8', 'raíz de 144', 'seno de 30', '15% de 200'"
}

func openAnyProgram(input string, mem *Memory) string {
	lower := strings.ToLower(input)
	// Mapa expandido de programas conocidos
	knownPrograms := map[string][]string{
		"word":        {"winword", "WINWORD.EXE"},
		"excel":       {"excel", "EXCEL.EXE"},
		"powerpoint":  {"powerpnt", "POWERPNT.EXE"},
		"outlook":     {"outlook", "OUTLOOK.EXE"},
		"access":      {"msaccess", "MSACCESS.EXE"},
		"chrome":      {"chrome", "google chrome"},
		"firefox":     {"firefox"},
		"edge":        {"msedge", "MicrosoftEdge"},
		"brave":       {"brave"},
		"opera":       {"opera"},
		"calculadora": {"calc"},
		"calc":        {"calc"},
		"notepad":     {"notepad"},
		"bloc":        {"notepad"},
		"paint":       {"mspaint"},
		"cmd":         {"cmd"},
		"powershell":  {"powershell"},
		"terminal":    {"wt", "WindowsTerminal"},
		"explorador":  {"explorer"},
		"archivos":    {"explorer"},
		"vlc":         {"vlc"},
		"winrar":      {"winrar", "WinRAR"},
		"7zip":        {"7zFM", "7z"},
		"acrobat":     {"Acrobat", "AcroRd32"},
		"pdf":         {"Acrobat", "AcroRd32"},
		"teams":       {"Teams"},
		"zoom":        {"Zoom"},
		"vscode":      {"code"},
		"code":        {"code"},
		"notepad++":   {"notepad++"},
		"taskmgr":     {"taskmgr"},
		"disco":       {"diskmgmt.msc"},
		"dispositivos": {"devmgmt.msc"},
		"servicios":   {"services.msc"},
	}
	var progName, progCmd string
	for keyword, cmds := range knownPrograms {
		if strings.Contains(lower, keyword) {
			progName = keyword
			progCmd = cmds[0]
			break
		}
	}
	if progCmd == "" {
		// Extraer nombre del programa del input
		re := regexp.MustCompile(`(?:abre?|ejecuta|lanza|inicia)\s+(?:el\s+|la\s+|los\s+)?(.+)`)
		if m := re.FindStringSubmatch(lower); len(m) >= 2 {
			progCmd = strings.TrimSpace(m[1])
			progName = progCmd
		}
	}
	if progCmd == "" {
		return "¿Qué programa quieres abrir? Ej: 'abrir chrome', 'abrir word'"
	}
	// 1. Buscar en PATH
	if path, err := exec.LookPath(progCmd); err == nil {
		exec.Command(path).Start()
		return fmt.Sprintf("✅ Abriendo %s.", progName)
	}
	// 2. Buscar en App Paths del registro
	regPath := fmt.Sprintf(`SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\%s.exe`, progCmd)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, regPath, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if exePath, _, err := k.GetStringValue(""); err == nil && exePath != "" {
			exec.Command(exePath).Start()
			return fmt.Sprintf("✅ Abriendo %s.", progName)
		}
	}
	// 3. Buscar en directorios comunes
	searchDirs := []string{
		os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"),
		os.Getenv("LocalAppData"), os.Getenv("AppData"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application"),
	}
	for _, dir := range searchDirs {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, progCmd+".exe")
		if _, err := os.Stat(candidate); err == nil {
			exec.Command(candidate).Start()
			return fmt.Sprintf("✅ Abriendo %s.", progName)
		}
	}
	// 4. Usar ShellExecute como fallback
	exec.Command("cmd", "/c", "start", "", progCmd).Start()
	return fmt.Sprintf("🔍 Intentando abrir '%s'...", progName)
}

func analyzeWazuhDeep(input string, mem *Memory) string {
	paths := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "ossec-agent", "ossec.log"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "ossec-agent", "ossec.log"),
		`C:\ossec\logs\ossec.log`,
		`C:\Program Files (x86)\ossec-agent\ossec.log`,
	}
	var logPath string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			logPath = p
			break
		}
	}
	if logPath == "" {
		return "❌ No se encontró ossec.log. ¿Está instalado Wazuh?"
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return "❌ No se pudo leer " + logPath
	}
	lines := strings.Split(string(data), "\n")
	total := len(lines)
	levelCounts := make(map[int]int)
	ruleCounts := make(map[string]int)
	reLevel := regexp.MustCompile(`level (\d+)`)
	reRule := regexp.MustCompile(`Rule: (\d+)`)
	criticalLines := []string{}
	now := time.Now()
	last24h := 0

	for _, line := range lines {
		if reL := reLevel.FindStringSubmatch(line); len(reL) >= 2 {
			lvl, _ := strconv.Atoi(reL[1])
			levelCounts[lvl]++
			if lvl >= 12 && len(criticalLines) < 10 {
				criticalLines = append(criticalLines, line[:minInt(len(line), 120)])
			}
		}
		if reR := reRule.FindStringSubmatch(line); len(reR) >= 2 {
			ruleCounts[reR[1]]++
		}
		// Check timestamp in last 24h (simple heuristic)
		if strings.Contains(line, now.Format("2006 Jan")) || strings.Contains(line, now.Format("Jan")) {
			last24h++
		}
	}

	// Top 5 reglas
	type kv struct {
		Key   string
		Value int
	}
	var sorted []kv
	for k, v := range ruleCounts {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Value > sorted[j].Value })

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🛡️ **Análisis Wazuh** (%s)\n\n", logPath))
	sb.WriteString(fmt.Sprintf("📊 Total eventos: %d | Últimas ~24h: %d\n", total, last24h))
	sb.WriteString("\n📈 Por nivel:\n")
	info, warn, alert, crit := 0, 0, 0, 0
	for l, c := range levelCounts {
		switch {
		case l <= 5:
			info += c
		case l <= 7:
			warn += c
		case l <= 11:
			alert += c
		default:
			crit += c
		}
	}
	sb.WriteString(fmt.Sprintf("  ℹ️ Info (1-5): %d | ⚠️ Aviso (6-7): %d | 🔔 Alerta (8-11): %d | 🚨 Crítico (12+): %d\n", info, warn, alert, crit))

	if len(sorted) > 0 {
		sb.WriteString("\n🔝 Top 5 reglas:\n")
		for i, kv := range sorted {
			if i >= 5 {
				break
			}
			sb.WriteString(fmt.Sprintf("  Rule %s: %d veces\n", kv.Key, kv.Value))
		}
	}
	if len(criticalLines) > 0 {
		sb.WriteString("\n🚨 Últimas alertas críticas:\n")
		for _, l := range criticalLines {
			sb.WriteString("  " + l + "\n")
		}
	}
	// Exportar
	outFile := fmt.Sprintf("logs/wazuh_analysis_%s.txt", time.Now().Format("20060102_150405"))
	os.WriteFile(outFile, []byte(sb.String()), 0644)
	sb.WriteString(fmt.Sprintf("\n💾 Guardado en: %s", outFile))
	return sb.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func analyzeOCSDeep(input string, mem *Memory) string {
	paths := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "OCS Inventory Agent", "OCSInventory.log"),
		filepath.Join(os.Getenv("ProgramFiles"), "OCS Inventory Agent", "OCSInventory.log"),
		`C:\ProgramData\OCS Inventory NG\Agent\OCSInventory.log`,
	}
	var logPath string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			logPath = p
			break
		}
	}
	if logPath == "" {
		return "❌ No se encontró OCSInventory.log. ¿Está instalado OCS Inventory Agent?"
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return "❌ No se pudo leer " + logPath
	}
	lines := strings.Split(string(data), "\n")
	errors, warnings := 0, 0
	lastSync := ""
	var recentErrors []string

	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") {
			errors++
			if len(recentErrors) < 5 {
				recentErrors = append(recentErrors, line[:minInt(len(line), 100)])
			}
		}
		if strings.Contains(lower, "warn") {
			warnings++
		}
		if strings.Contains(lower, "inventory") && strings.Contains(lower, "success") {
			lastSync = line[:minInt(len(line), 80)]
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 **Análisis OCS Inventory** (%s)\n\n", logPath))
	sb.WriteString(fmt.Sprintf("Total líneas: %d | Errores: %d | Warnings: %d\n", len(lines), errors, warnings))
	if lastSync != "" {
		sb.WriteString(fmt.Sprintf("✅ Última sincronización exitosa: %s\n", lastSync))
	} else {
		sb.WriteString("⚠️ No se encontró sincronización exitosa reciente.\n")
	}
	if len(recentErrors) > 0 {
		sb.WriteString("\n❌ Errores recientes:\n")
		for _, e := range recentErrors {
			sb.WriteString("  " + e + "\n")
		}
	}
	return sb.String()
}

func analyzeFrankLogs(input string, mem *Memory) string {
	data, err := os.ReadFile("logs/agent.log")
	if err != nil {
		return "❌ No se encontró logs/agent.log"
	}
	lines := strings.Split(string(data), "\n")
	errors, infos, warns := 0, 0, 0
	for _, l := range lines {
		switch {
		case strings.Contains(l, "[ERROR]"):
			errors++
		case strings.Contains(l, "[WARN]"):
			warns++
		case strings.Contains(l, "[INFO]"):
			infos++
		}
	}
	start := 0
	if len(lines) > 20 {
		start = len(lines) - 20
	}
	recent := strings.Join(lines[start:], "\n")
	return fmt.Sprintf("📄 **Logs de Frank**\nTotal: %d líneas | INFO: %d | WARN: %d | ERROR: %d\n\n**Últimas 20 líneas:**\n%s",
		len(lines), infos, warns, errors, recent)
}

func generateFullInventory(input string, mem *Memory) string {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Printf("[ERROR] generateFullInventory panic: %v", r)
				appendMessage("❌ Error interno al generar inventario.")
			}
		}()
		appendMessage("📦 Generando inventario completo, por favor espera...")
		inv := InventoryReport{
			GeneratedAt: time.Now(),
			Wazuh:       make(map[string]interface{}),
			OCS:         make(map[string]interface{}),
		}
		inv.Hostname, _ = os.Hostname()
		inv.User = currentUser

		// OS
		inv.OS = map[string]interface{}{
			"version":  psRun("(Get-WmiObject Win32_OperatingSystem).Caption"),
			"build":    psRun("(Get-WmiObject Win32_OperatingSystem).BuildNumber"),
			"arch":     psRun("(Get-WmiObject Win32_OperatingSystem).OSArchitecture"),
			"serial":   psRun("(Get-WmiObject Win32_OperatingSystem).SerialNumber"),
			"install":  psRun("(Get-WmiObject Win32_OperatingSystem).InstallDate"),
		}
		// CPU
		inv.CPU = map[string]interface{}{
			"name":  psRun("(Get-WmiObject Win32_Processor).Name"),
			"cores": psRun("(Get-WmiObject Win32_Processor).NumberOfCores"),
			"clock": psRun("(Get-WmiObject Win32_Processor).MaxClockSpeed"),
		}
		// RAM
		totalRAM := psRun("[math]::Round((Get-WmiObject Win32_ComputerSystem).TotalPhysicalMemory/1GB,2)")
		freeRAM := psRun("[math]::Round((Get-WmiObject Win32_OperatingSystem).FreePhysicalMemory/1MB,2)")
		inv.Memory = map[string]interface{}{"total_gb": totalRAM, "free_mb": freeRAM}

		// Discos
		disksRaw := psRun("Get-WmiObject Win32_LogicalDisk | Where-Object {$_.DriveType -eq 3} | ForEach-Object { $_.DeviceID + '|' + [math]::Round($_.Size/1GB,1) + '|' + [math]::Round($_.FreeSpace/1GB,1) }")
		for _, d := range strings.Split(disksRaw, "\n") {
			parts := strings.Split(strings.TrimSpace(d), "|")
			if len(parts) == 3 {
				inv.Disks = append(inv.Disks, map[string]interface{}{"drive": parts[0], "size_gb": parts[1], "free_gb": parts[2]})
			}
		}
		// Red
		ifacesRaw := psRun("Get-WmiObject Win32_NetworkAdapterConfiguration | Where-Object {$_.IPEnabled} | ForEach-Object { $_.Description + '|' + ($_.IPAddress -join ',') + '|' + ($_.MACAddress) }")
		for _, iface := range strings.Split(ifacesRaw, "\n") {
			parts := strings.SplitN(strings.TrimSpace(iface), "|", 3)
			if len(parts) == 3 {
				inv.Network = append(inv.Network, map[string]interface{}{"name": parts[0], "ip": parts[1], "mac": parts[2]})
			}
		}
		// Software (top 50 para no saturar)
		softwareRaw := psRun("Get-WmiObject Win32_Product | Select-Object -First 50 -ExpandProperty Name")
		inv.Software = strings.Split(strings.TrimSpace(softwareRaw), "\n")
		// Impresoras
		inv.Printers = strings.Split(strings.TrimSpace(psRun("Get-WmiObject Win32_Printer | Select-Object -ExpandProperty Name")), "\n")

		// Wazuh status
		wazuhSvc := psRun("(Get-Service OssecSvc -ErrorAction SilentlyContinue).Status")
		if wazuhSvc == "" {
			wazuhSvc = "No instalado"
		}
		inv.Wazuh["service"] = wazuhSvc

		// OCS status
		ocsSvc := psRun("(Get-Service OCS Inventory Service -ErrorAction SilentlyContinue).Status")
		if ocsSvc == "" {
			ocsSvc = "No instalado"
		}
		inv.OCS["service"] = ocsSvc

		// Serializar y guardar
		data, _ := json.MarshalIndent(inv, "", "  ")
		exeDir, _ := os.Executable()
		outFile := filepath.Join(filepath.Dir(exeDir), fmt.Sprintf("inventario_%s_%s.json", inv.Hostname, time.Now().Format("20060102_150405")))
		if err := os.WriteFile(outFile, data, 0644); err != nil {
			appendMessage("❌ No se pudo guardar inventario: " + err.Error())
			return
		}
		lastInventoryFileMu.Lock()
		lastInventoryFile = outFile
		lastInventoryFileMu.Unlock()
		// Abrir explorador en la carpeta
		exec.Command("explorer", filepath.Dir(outFile)).Start()
		appendMessage(fmt.Sprintf("✅ Inventario generado: %s\n💾 JSON guardado (%d bytes)", outFile, len(data)))
	}()
	return "🔍 Generando inventario completo en segundo plano..."
}

func viewLastInventory(input string, mem *Memory) string {
	lastInventoryFileMu.Lock()
	f := lastInventoryFile
	lastInventoryFileMu.Unlock()
	if f == "" {
		return "No hay inventario generado. Escribe 'generar inventario' primero."
	}
	if _, err := os.Stat(f); err != nil {
		return "❌ El inventario " + f + " ya no existe."
	}
	exec.Command("cmd", "/c", "start", "", f).Start()
	return "📂 Abriendo último inventario: " + f
}

func networkSpeedTest(input string, mem *Memory) string {
	go func() {
		// Medir latencia a DNS corporativo y externo
		dnsResults := []string{}
		for _, dns := range append(corporateDNS, "8.8.8.8", "1.1.1.1") {
			start := time.Now()
			conn, err := net.DialTimeout("tcp", dns+":53", 3*time.Second)
			if err == nil {
				conn.Close()
				dnsResults = append(dnsResults, fmt.Sprintf("  %s: %dms", dns, time.Since(start).Milliseconds()))
			} else {
				dnsResults = append(dnsResults, fmt.Sprintf("  %s: ❌ sin respuesta", dns))
			}
		}
		appendMessage("🌐 **Test de velocidad de red (latencia DNS):**\n" + strings.Join(dnsResults, "\n"))
	}()
	return "📡 Midiendo latencia de red..."
}

func getARPTable(input string, mem *Memory) string {
	out := psRun("arp -a")
	if len(out) > 2000 {
		out = out[:2000] + "\n[truncado...]"
	}
	return "🔗 **Tabla ARP:**\n" + out
}

func getRoutingTable(input string, mem *Memory) string {
	out := psRun("route print")
	if len(out) > 2000 {
		out = out[:2000] + "\n[truncado...]"
	}
	return "🛣️ **Tabla de rutas:**\n" + out
}

func mapNetworkDrive(input string, mem *Memory) string {
	re := regexp.MustCompile(`(?:mapear|mapea)\s+([A-Za-z]:)\s+(?:a|en|->)?\s*(\\\\[^\s]+)`)
	match := re.FindStringSubmatch(strings.ToLower(input))
	if len(match) < 3 {
		return "Uso: 'mapear Z: \\\\servidor\\carpeta'"
	}
	drive, path := strings.ToUpper(match[1]), match[2]
	exec.Command("net", "use", drive, path).Run()
	return fmt.Sprintf("✅ Unidad %s mapeada a %s", drive, path)
}

func unmapNetworkDrive(input string, mem *Memory) string {
	drive := reMatch1(`(?:desconectar|quitar)\s+(?:unidad\s+)?([A-Za-z]:)`, strings.ToLower(input))
	if drive == "" {
		return "Uso: 'desconectar unidad Z:'"
	}
	exec.Command("net", "use", strings.ToUpper(drive), "/delete").Run()
	return "✅ Unidad desconectada."
}

func getNetworkHistory(input string, mem *Memory) string {
	out := psRun("Get-WmiObject Win32_NetworkAdapterConfiguration | Where-Object {$_.IPEnabled} | Select-Object Description, IPAddress, DefaultIPGateway | ConvertTo-Json")
	return "🌐 **Historial de red:**\n" + out
}

func repairWinsock(input string, mem *Memory) string {
	return confirmDo(mem, "winsock", "¿Reparar Winsock y TCP/IP? (requiere reinicio) (SI/NO)",
		func(r string) string {
			go func() {
				exec.Command("netsh", "winsock", "reset").Run()
				exec.Command("netsh", "int", "ip", "reset").Run()
				sendNotification("Winsock", "Reparación completada. Reinicia el PC.")
			}()
			return "🔧 Reparando Winsock..."
		}, nil)
}

func getDiskUsageByFolder(input string, mem *Memory) string {
	go func() {
		out := psRun(`Get-ChildItem C:\ -ErrorAction SilentlyContinue | Where-Object {$_.PSIsContainer} | ForEach-Object { $size = (Get-ChildItem $_.FullName -Recurse -ErrorAction SilentlyContinue | Measure-Object -Property Length -Sum).Sum; [PSCustomObject]@{Folder=$_.Name; SizeGB=[math]::Round($size/1GB,2)} } | Sort-Object SizeGB -Descending | Select-Object -First 10 | ConvertTo-Json`)
		appendMessage("📁 **Uso de disco por carpeta (C:\\):**\n" + out)
	}()
	return "🔍 Analizando uso de disco por carpeta..."
}

func getFileHash(input string, mem *Memory) string {
	path := reMatch1(`(?:hash|md5|sha|checksum)\s+(?:de\s+|del?\s+)?(.+)`, strings.ToLower(input))
	if path == "" {
		return "Uso: 'hash de C:\\archivo.exe'"
	}
	path = strings.Trim(path, `"'`)
	if _, err := os.Stat(path); err != nil {
		return "❌ No se encontró el archivo."
	}
	md5 := psRun(fmt.Sprintf("(Get-FileHash '%s' -Algorithm MD5).Hash", path))
	sha := psRun(fmt.Sprintf("(Get-FileHash '%s' -Algorithm SHA256).Hash", path))
	return fmt.Sprintf("🔑 **Hash de %s:**\n  MD5:    %s\n  SHA256: %s", path, md5, sha)
}

func clearMiniDumps(input string, mem *Memory) string {
	return confirmDo(mem, "minidumps", "¿Eliminar todos los minidumps de C:\\Windows\\Minidump? (SI/NO)",
		func(r string) string {
			exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
				"Remove-Item 'C:\\Windows\\Minidump\\*' -Force -ErrorAction SilentlyContinue").Run()
			return "✅ Minidumps eliminados."
		}, nil)
}

func emptyRecycleBin(input string, mem *Memory) string {
	return confirmDo(mem, "papelera", "¿Vaciar papelera de reciclaje? (SI/NO)",
		func(r string) string {
			exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
				"Clear-RecycleBin -Force -ErrorAction SilentlyContinue").Run()
			return "🗑️ Papelera vaciada."
		}, nil)
}

func getPasswordPolicy(input string, mem *Memory) string {
	out := psRun("net accounts")
	return "🔐 **Política de contraseñas:**\n" + out
}

func generateSecurePasswordFn(input string, mem *Memory) string {
	length := 16
	if m := reMatch1(`(\d+) (?:caracteres|chars|letras)`, strings.ToLower(input)); m != "" {
		if n, err := strconv.Atoi(m); err == nil && n >= 8 && n <= 64 {
			length = n
		}
	}
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	result := make([]byte, length)
	for i := range result {
		b := make([]byte, 1)
		rand.Read(b)
		result[i] = chars[int(b[0])%len(chars)]
	}
	return fmt.Sprintf("🔑 Contraseña generada (%d chars):\n%s", length, string(result))
}

func checkWindowsActivation(input string, mem *Memory) string {
	out := psRun("(Get-WmiObject SoftwareLicensingProduct | Where-Object {$_.Name -like '*Windows*' -and $_.LicenseStatus -eq 1}).Name")
	if out == "" {
		return "⚠️ Windows no está activado o no se pudo verificar."
	}
	return "✅ Windows activado: " + out
}

func getSerialNumbers(input string, mem *Memory) string {
	bios := psRun("(Get-WmiObject Win32_BIOS).SerialNumber")
	board := psRun("(Get-WmiObject Win32_BaseBoard).SerialNumber")
	disk := psRun("(Get-WmiObject Win32_DiskDrive | Select-Object -First 1).SerialNumber")
	return fmt.Sprintf("🔢 **Números de serie:**\n  BIOS: %s\n  Placa madre: %s\n  Disco: %s", bios, board, disk)
}

func getGPResult(input string, mem *Memory) string {
	go func() {
		out := psRun("gpresult /r 2>&1")
		if len(out) > 3000 {
			out = out[:3000] + "\n[truncado...]"
		}
		appendMessage("📋 **Resultado de directivas de grupo:**\n" + out)
	}()
	return "⏳ Ejecutando gpresult /r..."
}

func getRecentBSODs(input string, mem *Memory) string {
	out := psRun(`Get-WinEvent -FilterHashtable @{LogName='System'; Id=41,1001,6008} -MaxEvents 5 -ErrorAction SilentlyContinue | Select-Object TimeCreated, Message | ConvertTo-Json`)
	if out == "" {
		return "✅ No se encontraron eventos de BSOD recientes."
	}
	return "💥 **BSODs recientes:**\n" + out
}

func listCertificates(input string, mem *Memory) string {
	out := psRun("Get-ChildItem Cert:\\LocalMachine\\My | Select-Object Subject, NotAfter | ConvertTo-Json")
	if out == "" {
		return "No se encontraron certificados o no hay acceso."
	}
	return "🔒 **Certificados (LocalMachine):**\n" + out
}

func enableHibernation(input string, mem *Memory) string {
	exec.Command("powercfg", "/hibernate", "on").Run()
	return "✅ Hibernación activada."
}

func disableHibernation(input string, mem *Memory) string {
	exec.Command("powercfg", "/hibernate", "off").Run()
	return "✅ Hibernación desactivada."
}

func setHighPerformancePlan(input string, mem *Memory) string {
	exec.Command("powercfg", "/setactive", "8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c").Run()
	return "⚡ Plan de alto rendimiento activado."
}

func setBalancedPlan(input string, mem *Memory) string {
	exec.Command("powercfg", "/setactive", "381b4222-f694-41f0-9685-ff5bb260df2e").Run()
	return "⚖️ Plan equilibrado activado."
}

func getSystemEvents(input string, mem *Memory) string {
	go func() {
		out := psRun(`Get-WinEvent -LogName System -MaxEvents 20 -ErrorAction SilentlyContinue | Select-Object TimeCreated, LevelDisplayName, Message | ConvertTo-Json`)
		if len(out) > 3000 {
			out = out[:3000] + "\n[truncado...]"
		}
		appendMessage("📋 **Eventos del sistema (últimos 20):**\n" + out)
	}()
	return "⏳ Consultando eventos del sistema..."
}

func runFullDiagnostic(input string, mem *Memory) string {
	go func() {
		appendMessage("🔍 Iniciando diagnóstico completo...")
		results := []string{
			"🖥️ " + getSystemInfo("", mem),
			"💾 " + diagnoseDiskSpace("", mem),
			"🌐 " + getIPAddress("", mem),
			"🌡️ " + getCPUTemperature("", mem),
			"📀 " + getDiskSMART("", mem),
			"🔥 " + diagnoseFirewall("", mem),
		}
		report := strings.Join(results, "\n\n---\n\n")
		outFile := fmt.Sprintf("logs/diagnostico_%s.txt", time.Now().Format("20060102_150405"))
		os.WriteFile(outFile, []byte(report), 0644)
		appendMessage("✅ Diagnóstico completo guardado en: " + outFile)
	}()
	return "⏳ Diagnóstico completo en progreso..."
}

// ========================= GATEWAY IA =========================
// askGateway es el fallback de último recurso antes de defaultResponse.
// Usa Ollama (si disponible) con fallback a KB local y peers LAN.
// No se llama para intenciones con handler en actionMap.
func askGateway(input string, mem *Memory) string {
	return askOllama(input)
}

// ========================= TICKETS =========================
func sendTicket(message, category string) {
	hostname, _ := os.Hostname()
	username := os.Getenv("USERNAME")
	// Usar el sistema de cola con reintentos de refactor.go
	EnqueueTicket(hostname, username, message, category)
	logger.Printf("[INFO] Ticket encolado: category=%s message=%q", category, message)
}
// ========================= UI =========================
func buildUI() {
	winTitle := fmt.Sprintf("%s %s", AgentName, AgentVersion)
	mainWindow.SetTitle(winTitle)

	chatScroll = initBubbleChat()

	statusLabel := widget.NewLabel(distributedStatusLine())
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fyne.Do(func() { statusLabel.SetText(distributedStatusLine()) })
			case <-serverCtx.Done():
				return
			}
		}
	}()

	inputField := widget.NewEntry()
	inputField.SetPlaceHolder("Escribe tu mensaje...")

	var sendBtn *widget.Button
	sendBtn = widget.NewButtonWithIcon("Enviar", theme.MailSendIcon(), func() {
		processMessage(inputField.Text, inputField, sendBtn)
	})
	// HighImportance aplica el color de acento del tema al botón,
	// haciendo visible el cambio de AccentColor en ajustes.
	sendBtn.Importance = widget.HighImportance
	inputField.OnSubmitted = func(s string) { processMessage(s, inputField, sendBtn) }

	settingsButton := widget.NewButtonWithIcon("", theme.SettingsIcon(), func() {
		showSettingsWindow()
	})
	clearButton := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		// OnTapped ya corre en el hilo principal de Fyne — llamar directo.
		if chatVBox != nil {
			chatVBox.RemoveAll()
			addBubble("system", fmt.Sprintf("🤖 %s %s — escribe 'ayuda' para ver comandos.", AgentName, AgentVersion))
		}
	})

	topBar := container.NewBorder(nil, nil, nil, container.NewHBox(settingsButton, clearButton), statusLabel)
	inputArea := container.NewBorder(nil, nil, nil, sendBtn, inputField)
	mainContainer := container.NewBorder(topBar, inputArea, nil, nil, chatScroll)
	mainWindow.SetContent(container.NewPadded(mainContainer))

	if desk, ok := appInstance.(desktop.App); ok {
		desk.SetSystemTrayMenu(fyne.NewMenu("Frank",
			fyne.NewMenuItem("Mostrar", func() {
				mainWindow.Show()
				isWindowVisible.Store(true)
				positionWindowBottomRight(winTitle)
			}),
			fyne.NewMenuItem("Ajustes", func() { showSettingsWindow() }),
			fyne.NewMenuItem("Salir", func() { gracefulExit() }),
		))
		mainWindow.SetCloseIntercept(func() { mainWindow.Hide(); isWindowVisible.Store(false) })
	}
}


func processMessage(text string, input *widget.Entry, btn *widget.Button) {
	if text == "" {
		return
	}
	// Called from Fyne main goroutine — safe to call UI directly.
	addBubble("user", text)
	input.SetText("")
	input.Disable()
	btn.Disable()

	// ── Animated thinking bubble ──────────────────────────────────────────
	thinkingLbl := widget.NewLabel("🤔 Pensando")
	thinkingLbl.Importance = widget.LowImportance
	thinkingBubble := container.NewHBox(thinkingLbl)
	chatVBox.Add(thinkingBubble)
	chatVBox.Refresh()
	if chatScrollNew != nil {
		chatScrollNew.ScrollToBottom()
	}

	// stopAnim is closed by the processing goroutine to stop the animation.
	stopAnim := make(chan struct{})

	go func() {
		// Cycle through phases (4 ticks each) and dots (1 tick each).
		phases := []string{
			"🤔 Pensando",
			"🔍 Analizando",
			"⚙️  Procesando",
			"💡 Buscando respuesta",
			"🧠 Consultando base de conocimiento",
		}
		dots := []string{"", " .", " . .", " . . ."}
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-ticker.C:
				i++
				phase := phases[(i/4)%len(phases)]
				dot   := dots[i%len(dots)]
				label := phase + dot
				fyne.Do(func() { thinkingLbl.SetText(label) })
			case <-stopAnim:
				return
			}
		}
	}()

	go func(msg string) {
		defer func() {
			// Always stop animation and re-enable input, even on panic.
			close(stopAnim)
			fyne.Do(func() {
				chatVBox.Remove(thinkingBubble)
				input.Enable()
				btn.Enable()
				mainWindow.Canvas().Focus(input)
			})
		}()

		resp := processUserMessage(msg, &memory)

		// Guardar respuesta en memoria conversacional de sesión.
		memory.mu.Lock()
		memory.LastAssistantMessages = append(memory.LastAssistantMessages, resp)
		if len(memory.LastAssistantMessages) > 10 {
			memory.LastAssistantMessages = memory.LastAssistantMessages[1:]
		}
		memory.LastAssistantMsg = resp
		memory.mu.Unlock()

		// Verificar si Frank quedó esperando confirmación del usuario
		memory.mu.RLock()
		hasPending := memory.PendingConfirmation != nil
		memory.mu.RUnlock()

		displayed := formatResponse(resp)
		fyne.Do(func() {
			if hasPending {
				// Mostrar burbuja con botones SI/NO clickeables
				addConfirmationBubble(displayed, input, btn)
			} else {
				addBubble("frank", displayed)
			}
		})

		addToHistory("user", msg)
		addToHistory("assistant", resp)
	}(text)
}

func appendMessage(msg string) {
	appendBubbleFromGoroutine("frank", msg)
}

func scrollToBottom() {
	if chatScrollNew != nil {
		chatScrollNew.ScrollToBottom()
	}
}

func addToHistory(role, content string) {
	historyMutex.Lock()
	defer historyMutex.Unlock()
	chatHistoryData.Messages = append(chatHistoryData.Messages, ChatMessage{
		ID:        uuid.New().String(),
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
	if len(chatHistoryData.Messages) > maxHistorySize {
		chatHistoryData.Messages = chatHistoryData.Messages[len(chatHistoryData.Messages)-maxHistorySize:]
	}
	saveChatHistoryLocked()
}

func saveChatHistoryLocked() {
	data, err := json.MarshalIndent(chatHistoryData, "", "  ")
	if err != nil {
		logger.Printf("[ERROR] saveChatHistory: marshal falló: %v", err)
		return
	}
	enc, err := protectData(data)
	if err != nil {
		logger.Printf("[ERROR] saveChatHistory: cifrado falló: %v", err)
		return
	}
	if err := os.WriteFile(historyFile, enc, 0600); err != nil {
		logger.Printf("[ERROR] saveChatHistory: escritura falló: %v", err)
	}
}

func loadChatHistory() {
	historyMutex.Lock()
	defer historyMutex.Unlock()
	chatHistoryData = ChatHistory{PCName: pcName, User: currentUser, Messages: []ChatMessage{}}
	data, err := os.ReadFile(historyFile)
	if err != nil {
		return
	}
	plain, err := unprotectData(data)
	if err != nil {
		logger.Printf("[ERROR] loadChatHistory: descifrado falló: %v", err)
		return
	}
	if err := json.Unmarshal(plain, &chatHistoryData); err != nil {
		logger.Printf("[ERROR] loadChatHistory: unmarshal falló: %v", err)
		chatHistoryData = ChatHistory{PCName: pcName, User: currentUser, Messages: []ChatMessage{}}
	}
}

// ========================= VENTANA DE CONFIGURACIÓN =========================
// settingsWinRef garantiza que solo exista una ventana de ajustes abierta a la vez.
var (
	settingsWinRef fyne.Window
	settingsWinMu  sync.Mutex
)

func showSettingsWindow() {
	// Singleton: si ya está abierta, enfocarla en lugar de crear otra.
	settingsWinMu.Lock()
	if settingsWinRef != nil {
		w := settingsWinRef
		settingsWinMu.Unlock()
		w.RequestFocus()
		return
	}
	settingsWinMu.Unlock()

	settingsMutex.RLock()
	currentSettings := settings
	settingsMutex.RUnlock()

	w := appInstance.NewWindow("Ajustes")
	settingsWinMu.Lock()
	settingsWinRef = w
	settingsWinMu.Unlock()
	w.SetOnClosed(func() {
		settingsWinMu.Lock()
		settingsWinRef = nil
		settingsWinMu.Unlock()
	})
	w.Resize(fyne.NewSize(450, 600))

	notificationsCheck := widget.NewCheck("Activar notificaciones proactivas", func(b bool) {
		currentSettings.NotificationsEnabled = b
	})
	notificationsCheck.SetChecked(currentSettings.NotificationsEnabled)

	emotionalCheck := widget.NewCheck("Activar frases de apoyo emocional", func(b bool) {
		currentSettings.EmotionalSupportEnabled = b
	})
	emotionalCheck.SetChecked(currentSettings.EmotionalSupportEnabled)

	startWithWindowsCheck := widget.NewCheck("Iniciar con Windows", func(b bool) {
		currentSettings.StartWithWindows = b
	})
	startWithWindowsCheck.SetChecked(currentSettings.StartWithWindows)

	freqOptions := []string{"Baja (cada 6h)", "Media (cada 3h)", "Alta (cada 1h)"}
	freqMap := map[string]string{"Baja (cada 6h)": "low", "Media (cada 3h)": "medium", "Alta (cada 1h)": "high"}
	reverseFreqMap := map[string]string{"low": "Baja (cada 6h)", "medium": "Media (cada 3h)", "high": "Alta (cada 1h)"}
	freqSelect := widget.NewSelect(freqOptions, func(s string) {
		currentSettings.NotificationFrequency = freqMap[s]
	})
	freqSelect.SetSelected(reverseFreqMap[currentSettings.NotificationFrequency])

	themeOptions := []string{"Claro", "Oscuro", "Alto contraste"}
	themeMap := map[string]string{"Claro": "light", "Oscuro": "dark", "Alto contraste": "high_contrast"}
	reverseThemeMap := map[string]string{"light": "Claro", "dark": "Oscuro", "high_contrast": "Alto contraste"}
	themeSelect := widget.NewSelect(themeOptions, func(s string) {
		currentSettings.Theme = themeMap[s]
	})
	themeSelect.SetSelected(reverseThemeMap[currentSettings.Theme])

	var colorNames []string
	for name := range accentColors {
		colorNames = append(colorNames, name)
	}
	sort.Strings(colorNames)
	var previewContainer *fyne.Container
	previewWrapper := container.NewVBox()
	rebuildPreview := func() {
		hex := accentColors[currentSettings.AccentColor]
		previewContainer = container.New(&maxWidthLayout{max: 380},
			liveThemePreview(currentSettings.Theme, hex))
		previewWrapper.RemoveAll()
		previewWrapper.Add(previewContainer)
		previewWrapper.Refresh()
	}

	colorSelect := widget.NewSelect(colorNames, func(s string) {
		currentSettings.AccentColor = s
		rebuildPreview()
	})
	colorSelect.SetSelected(currentSettings.AccentColor)

	themeSelect.OnChanged = func(s string) {
		currentSettings.Theme = themeMap[s]
		rebuildPreview()
	}

	rebuildPreview()

	// ── Accesibilidad ─────────────────────────────────────────────────────
	fontSizeOptions := []string{"12 (pequeño)", "14 (normal)", "16 (grande)", "18 (muy grande)", "20 (extra grande)"}
	fontSizeValues := map[string]float32{
		"12 (pequeño)":     12,
		"14 (normal)":      14,
		"16 (grande)":      16,
		"18 (muy grande)":  18,
		"20 (extra grande)": 20,
	}
	reverseFontSizeMap := map[float32]string{
		12: "12 (pequeño)",
		14: "14 (normal)",
		16: "16 (grande)",
		18: "18 (muy grande)",
		20: "20 (extra grande)",
	}
	fontSizeSelect := widget.NewSelect(fontSizeOptions, func(s string) {
		if v, ok := fontSizeValues[s]; ok {
			currentSettings.FontSize = v
		}
	})
	selectedFontLabel := reverseFontSizeMap[currentSettings.FontSize]
	if selectedFontLabel == "" {
		selectedFontLabel = "14 (normal)"
	}
	fontSizeSelect.SetSelected(selectedFontLabel)

	boldCheck := widget.NewCheck("Texto en negrita (mayor legibilidad)", func(b bool) {
		currentSettings.BoldText = b
	})
	boldCheck.SetChecked(currentSettings.BoldText)

	dyslexicCheck := widget.NewCheck("Modo dislexia (fuente redondeada + espaciado extra)", func(b bool) {
		currentSettings.DyslexicMode = b
	})
	dyslexicCheck.SetChecked(currentSettings.DyslexicMode)

	// ── Colores de texto en burbujas ──────────────────────────────────────
	var textColorNames []string
	for name := range textColors {
		textColorNames = append(textColorNames, name)
	}
	sort.Strings(textColorNames)

	userTextSelect := widget.NewSelect(textColorNames, func(s string) {
		currentSettings.UserBubbleTextColor = s
	})
	if currentSettings.UserBubbleTextColor == "" {
		currentSettings.UserBubbleTextColor = "Blanco"
	}
	userTextSelect.SetSelected(currentSettings.UserBubbleTextColor)

	frankTextSelect := widget.NewSelect(textColorNames, func(s string) {
		currentSettings.FrankBubbleTextColor = s
	})
	if currentSettings.FrankBubbleTextColor == "" {
		currentSettings.FrankBubbleTextColor = "Azul marino"
	}
	frankTextSelect.SetSelected(currentSettings.FrankBubbleTextColor)

	// ── Emojis ────────────────────────────────────────────────────────────
	emojiCheck := widget.NewCheck("Mostrar emojis en las respuestas", func(b bool) {
		currentSettings.UseEmojis = b
	})
	emojiCheck.SetChecked(currentSettings.UseEmojis)

	// ── Personalidad ──────────────────────────────────────────────────────
	personalityOptions := []string{
		"Profesional",
		"Tecnico",
		"Amigable",
		"Conciso",
	}
	personalityMap := map[string]string{
		"Profesional": "profesional",
		"Tecnico":     "tecnico",
		"Amigable":    "amigable",
		"Conciso":     "conciso",
	}
	reversePersonalityMap := map[string]string{
		"profesional": "Profesional",
		"tecnico":     "Tecnico",
		"amigable":    "Amigable",
		"conciso":     "Conciso",
	}
	personalityDesc := map[string]string{
		"Profesional": "Tono formal, respuestas completas y estructuradas.",
		"Tecnico":     "Directo al punto, sin rodeos, datos precisos.",
		"Amigable":    "Calido, usa frases de apoyo, menos formal.",
		"Conciso":     "Respuestas minimas, solo lo esencial.",
	}
	personalityLabel := widget.NewLabel(personalityDesc[reversePersonalityMap[currentSettings.Personality]])
	personalityLabel.Wrapping = fyne.TextWrapWord

	personalitySelect := widget.NewSelect(personalityOptions, func(s string) {
		currentSettings.Personality = personalityMap[s]
		personalityLabel.SetText(personalityDesc[s])
	})
	personalitySelect.SetSelected(reversePersonalityMap[currentSettings.Personality])

	// ── Perfil y botones ──────────────────────────────────────────────────
	editProfileBtn := widget.NewButton("Editar mi perfil", func() {
		showEditProfileWindow()
	})

	saveBtn := widget.NewButton("Guardar", func() {
		// Validar antes de guardar — evitar estados inválidos.
		validThemes := map[string]bool{"light": true, "dark": true, "high_contrast": true}
		if !validThemes[currentSettings.Theme] {
			currentSettings.Theme = "light"
		}
		if currentSettings.FontSize <= 0 {
			currentSettings.FontSize = 14
		}
		if _, ok := accentColors[currentSettings.AccentColor]; !ok {
			currentSettings.AccentColor = "Azul"
		}
		validFreq := map[string]bool{"low": true, "medium": true, "high": true}
		if !validFreq[currentSettings.NotificationFrequency] {
			currentSettings.NotificationFrequency = "medium"
		}
		validPersonality := map[string]bool{
			"profesional": true, "tecnico": true, "amigable": true, "conciso": true,
		}
		if !validPersonality[currentSettings.Personality] {
			currentSettings.Personality = "profesional"
		}

		settingsMutex.Lock()
		settings = currentSettings
		settingsMutex.Unlock()
		saveSettings()
		applyTheme()
		setStartWithWindows(settings.StartWithWindows)
		restartProactiveTicker()
		w.Close()
	})
	saveBtn.Importance = widget.HighImportance

	cancelBtn := widget.NewButton("Cancelar", func() {
		w.Close()
	})

	aboutLbl := widget.NewLabel(AgentAuthor)
	aboutLbl.Importance = widget.LowImportance

	form := container.NewVBox(
		widget.NewLabelWithStyle("Notificaciones", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		notificationsCheck,
		widget.NewLabel("Frecuencia"),
		freqSelect,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Soporte emocional", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		emotionalCheck,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Apariencia", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Tema"),
		themeSelect,
		widget.NewLabel("Color de acento"),
		colorSelect,
		widget.NewLabel("Vista previa:"),
		previewWrapper,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Accesibilidad", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Tamanio de texto"),
		fontSizeSelect,
		boldCheck,
		dyslexicCheck,
		widget.NewLabel("Color de texto en burbuja de usuario"),
		userTextSelect,
		widget.NewLabel("Color de texto en burbuja de Frank"),
		frankTextSelect,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Conversacion", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		emojiCheck,
		widget.NewLabel("Personalidad del asistente"),
		personalitySelect,
		personalityLabel,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Sistema", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		startWithWindowsCheck,
		widget.NewSeparator(),
		editProfileBtn,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Acerca de", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		aboutLbl,
	)

	scroll := container.NewVScroll(form)
	buttons := container.NewHBox(cancelBtn, saveBtn)
	content := container.NewBorder(nil, buttons, nil, nil, scroll)
	w.SetContent(container.NewPadded(content))
	w.Show()
}

// ========================= SERVIDOR HTTP SEGURO =========================
var limiter = rate.NewLimiter(1, 5)

func startCommandListener() {
	mux := http.NewServeMux()
	addP2PEndpoints(mux)

	mux.HandleFunc("/execute", func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		if r.Header.Get("X-API-Key") != agentAPIKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var cmd RemoteCommand
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		var response string
		p := cmd.Params // shorthand
		switch cmd.Action {

		// ── Diagnóstico / información ────────────────────────────────────────
		case "info_sistema":
			response = getSystemInfo("", &memory)
		case "diagnostico":
			response = getSystemInfo("", &memory)
		case "espacio_disco":
			response = diagnoseDiskSpace("", &memory)
		case "listar_procesos":
			response = listProcesses("", &memory)
		case "ver_logs_frank":
			response = analyzeFrankLogs("", &memory)
		case "generar_inventario":
			response = generateFullInventory("", &memory)
		case "uptime_sistema":
			response = getSystemUptime("", &memory)
		case "ram_detalle":
			response = getDetailedRAM("", &memory)
		case "gpu_info":
			response = getGPUInfo("", &memory)
		case "salud_disco":
			response = checkDiskHealth("", &memory)
		case "ver_eventos_sistema":
			response = getSystemEvents("", &memory)

		// ── Servicios ────────────────────────────────────────────────────────
		case "ver_servicios":
			response = psRun(`Get-Service | Where-Object {$_.Status -eq 'Running'} | Select-Object -First 40 Name,Status,DisplayName | Format-Table -AutoSize | Out-String`)
		case "reiniciar_servicio":
			nombre := p["nombre"]
			if nombre == "" {
				response = "❌ Falta el parámetro 'nombre' del servicio."
			} else {
				out := psRun(fmt.Sprintf(`Restart-Service -Name '%s' -Force -PassThru | Select-Object Name,Status | Format-Table | Out-String`, nombre))
				response = "🔄 Servicio '" + nombre + "' reiniciado:\n" + out
			}

		// ── Procesos ─────────────────────────────────────────────────────────
		case "matar_proceso":
			nombre := p["nombre"]
			if nombre == "" {
				response = "❌ Falta el parámetro 'nombre' del proceso."
			} else {
				out := psRun(fmt.Sprintf(`Stop-Process -Name '%s' -Force -ErrorAction SilentlyContinue; "Proceso '%s' terminado."`, nombre, nombre))
				response = out
			}

		// ── Red ──────────────────────────────────────────────────────────────
		case "estado_red":
			response = fixNetwork("", &memory)
		case "velocidad_red":
			response = networkSpeedTest("", &memory)
		case "flush_dns":
			response = flushDNS("", &memory)
		case "conexiones_activas":
			response = netstat("", &memory)
		case "latencia_red":
			response = checkNetworkLatency("", &memory)
		case "escaneo_red":
			response = scanLocalNetwork("", &memory)

		// ── Mantenimiento ────────────────────────────────────────────────────
		case "mantenimiento":
			response = startMaintenance("", &memory)
		case "reiniciar_spooler":
			response = fixPrinter("", &memory)
		case "reparar_winsock":
			response = repairWinsock("", &memory)
		case "limpiar_temporales":
			response = systemCleanupDeep("", &memory)

		// ── Control remoto / UI ───────────────────────────────────────────────
		case "bloquear_pantalla":
			response = lockWorkstation("", &memory)
		case "popup_mensaje":
			msg := p["mensaje"]
			if msg == "" {
				msg = p["message"]
			}
			if msg == "" {
				response = "❌ Falta el parámetro 'mensaje'."
			} else {
				titulo := p["titulo"]
				if titulo == "" {
					titulo = "MicLaw IT Support"
				}
				psRun(fmt.Sprintf(`Add-Type -AssemblyName PresentationFramework; [System.Windows.MessageBox]::Show('%s','%s','OK','Information')`, msg, titulo))
				response = "✅ Mensaje mostrado al usuario."
			}
		case "abrir_taskmanager":
			exec.Command("taskmgr.exe").Start()
			response = "✅ Administrador de tareas abierto."
		case "abrir_aplicacion":
			nombre := p["nombre"]
			if nombre == "" {
				response = "❌ Falta el parámetro 'nombre'."
			} else {
				if err := exec.Command("cmd", "/c", "start", nombre).Start(); err != nil {
					response = "❌ No se pudo abrir: " + err.Error()
				} else {
					response = "✅ Aplicación '" + nombre + "' iniciada."
				}
			}

		// ── Seguridad ─────────────────────────────────────────────────────────
		case "estado_defender":
			response = checkWindowsDefenderStatus("", &memory)
		case "actualizaciones_instaladas":
			response = getInstalledUpdates("", &memory)
		case "ver_usuarios_activos":
			response = psRun(`query user 2>&1 || query session 2>&1`)

		// ── Legacy / compatibilidad ───────────────────────────────────────────
		case "restart_spooler":
			response = fixPrinter("", &memory)
		case "get_system_info":
			response = getSystemInfo("", &memory)

		default:
			logger.Printf("[WARN] /execute: acción desconocida %q", cmd.Action)
			http.Error(w, "Invalid action: "+cmd.Action, http.StatusBadRequest)
			return
		}
		if _, err := w.Write([]byte(response)); err != nil {
			logger.Printf("[ERROR] /execute: write response falló: %v", err)
		}
	})

	mux.HandleFunc("/send_message", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != agentAPIKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var payload struct {
			Message  string `json:"message"`
			TicketID string `json:"ticket_id"`
			Author   string `json:"author"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if payload.Message == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Mostrar en el chat
		appendBubbleFromGoroutine("frank", "🔧 Soporte: "+payload.Message)

		// Si la ventana está minimizada/oculta, lanzar notificación del sistema
		if !isWindowVisible.Load() {
			title := AgentName + " — Soporte"
			if payload.Author != "" {
				title = AgentName + " — " + payload.Author
			}
			go sendNotification(title, payload.Message)

			// Mostrar la ventana y traerla al frente
			fyne.Do(func() {
				mainWindow.Show()
				isWindowVisible.Store(true)
			})
		}
		w.WriteHeader(http.StatusNoContent)
	})

	httpServer = &http.Server{
		Addr:         httpListenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("[ERROR] HTTP server: %v", err)
		}
	}()
}

// ── Gateway sync ──────────────────────────────────────────────────────────────

// registerWithGateway registra este agente en el gateway central.
// Devuelve true si el registro fue exitoso (HTTP 200).
func registerWithGateway() bool {
	if gatewayURL == "" {
		return false
	}
	ip := getOutboundIP()
	hostname, _ := os.Hostname()

	body, _ := json.Marshal(map[string]any{
		"name":      hostname,
		"ip":        ip,
		"port":      8081,
		"hostname":  hostname,
		"version":   AgentVersion,
		"agent_key": agentAPIKey,
		"type":      "frank",
	})

	req, err := http.NewRequest("POST", gatewayURL+"/agents/register", bytes.NewReader(body))
	if err != nil {
		logger.Printf("[WARN] Gateway register request build error: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", agentAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Printf("[WARN] Gateway register failed (connection): %v", err)
		return false
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		logger.Printf("[WARN] Gateway register rejected — status %d: %s", resp.StatusCode, string(respBody))
		if resp.StatusCode == http.StatusUnauthorized {
			logger.Printf("[ERROR] 401 Unauthorized: AGENT_API_KEY no coincide con MICLAW_AGENT_KEY del gateway. Key usada: %q", agentAPIKey)
		}
		return false
	}
	logger.Printf("[INFO] Registrado en gateway %s — IP=%s", gatewayURL, ip)
	return true
}

// sendHeartbeat envía métricas al gateway cada 60 segundos.
func sendHeartbeat() {
	if gatewayURL == "" {
		return
	}
	ip := getOutboundIP()
	hostname, _ := os.Hostname()
	agentID := "frank-" + ip

	// CPU %
	cpuStr := psRun(`(Get-WmiObject Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average`)
	cpu, _ := strconv.ParseFloat(strings.TrimSpace(cpuStr), 64)

	// RAM %
	memStr := psRun(`$os=(Get-WmiObject Win32_OperatingSystem); [math]::Round(($os.TotalVisibleMemorySize-$os.FreePhysicalMemory)/$os.TotalVisibleMemorySize*100,1)`)
	mem, _ := strconv.ParseFloat(strings.TrimSpace(memStr), 64)

	// Disco C: %
	diskStr := psRun(`$d=Get-WmiObject Win32_LogicalDisk -Filter "DriveType=3 AND DeviceID='C:'"; if($d.Size -gt 0){[math]::Round(($d.Size-$d.FreeSpace)/$d.Size*100,1)}else{0}`)
	disk, _ := strconv.ParseFloat(strings.TrimSpace(diskStr), 64)

	body, _ := json.Marshal(map[string]any{
		"agent_id": agentID,
		"name":     hostname,
		"ip":       ip,
		"cpu_pct":  cpu,
		"mem_pct":  mem,
		"disk_pct": disk,
		"status":   "ok",
		"version":  AgentVersion,
	})

	req, err := http.NewRequest("POST", gatewayURL+"/agents/heartbeat", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", agentAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Printf("[WARN] Heartbeat failed: %v", err)
		return
	}
	defer resp.Body.Close()
}

// startGatewaySync registra el agente y luego envía heartbeats periódicos.
// Reintenta el registro hasta 5 veces con backoff exponencial si el gateway
// no está disponible o rechaza la petición.
func startGatewaySync(ctx context.Context) {
	if gatewayURL == "" {
		logger.Printf("[INFO] startGatewaySync: MICLAW_GATEWAY no configurado, sync desactivado")
		return
	}
	logger.Printf("[INFO] startGatewaySync: conectando a %s con key=%q", gatewayURL, agentAPIKey)

	// Esperar a que el resto del sistema esté listo
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return
	}

	// Registro con reintentos (backoff: 10s, 20s, 40s, 80s)
	registered := false
	for attempt := 1; attempt <= 5; attempt++ {
		if registerWithGateway() {
			registered = true
			break
		}
		wait := time.Duration(10*attempt) * time.Second
		logger.Printf("[WARN] Registro fallido (intento %d/5). Reintentando en %s...", attempt, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}

	if !registered {
		logger.Printf("[ERROR] No se pudo registrar en el gateway después de 5 intentos. Heartbeats desactivados.")
		return
	}

	// Heartbeat cada 60 segundos; si falla, lo reintenta en el próximo tick
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			sendHeartbeat()
		case <-ctx.Done():
			return
		}
	}
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

// ========================= CIERRE =========================
func setupGracefulShutdown() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() { <-c; gracefulExit() }()
}

func gracefulExit() {
	logger.Println("[INFO] Cerrando...")

	if serverStop != nil {
		serverStop()
	}

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

	if httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("[ERROR] gracefulExit: HTTP shutdown: %v", err)
		}
	}

	wg.Wait()

	historyMutex.Lock()
	saveChatHistoryLocked()
	historyMutex.Unlock()

	if logFile != nil {
		logFile.Close()
	}
	releaseLockFile()
	os.Exit(0)
}

// sendNotification muestra una notificación del sistema con deduplicación.
// Ignora mensajes idénticos enviados dentro de los últimos 5 segundos para
// evitar spam cuando el mismo evento se dispara varias veces seguidas.
var (
	lastNotifKey  string
	lastNotifTime time.Time
	notifMu       sync.Mutex
)

func sendNotification(title, message string) {
	key := title + "|" + message
	notifMu.Lock()
	if key == lastNotifKey && time.Since(lastNotifTime) < 5*time.Second {
		notifMu.Unlock()
		return
	}
	lastNotifKey = key
	lastNotifTime = time.Now()
	notifMu.Unlock()
	beeep.Notify(title, message, "")
}

// ========================= MAIN =========================
func main() {
	// ── Instancia única ──────────────────────────────────────────────────────
	// Crear mutex de Windows con nombre único. Si ya existe → otra instancia
	// está corriendo → salir silenciosamente.
	mutexHandle, isFirst := ensureSingleInstance()
	if !isFirst {
		logger.Println("[INFO] Frank ya está en ejecución. Cerrando instancia duplicada.")
		os.Exit(0)
	}
	// mutexHandle se mantiene abierto durante toda la vida del proceso.
	// El OS lo libera automáticamente al salir (incluso con os.Exit).
	_ = mutexHandle

	// ── Auto-instalación portable ─────────────────────────────────────────
	// Si el ejecutable no corre desde C:\Program Files\Frank\, instalarlo
	// allí, crear el acceso directo y relanzar desde la nueva ubicación.
	if selfInstallIfNeeded() {
		// Liberar el mutex antes de salir para que la nueva instancia pueda
		// adquirirlo sin esperar a que el OS limpie los recursos del proceso.
		windows.CloseHandle(mutexHandle)
		os.Exit(0)
	}

	// ── Lock file (segunda barrera, basada en PID) ────────────────────────
	// Complementa el mutex de Windows: detecta procesos zombie y garantiza
	// que solo una instancia corra desde el directorio de instalación.
	if !acquireLockFile() {
		logger.Println("[INFO] Frank ya está en ejecución (lock file). Cerrando.")
		os.Exit(0)
	}

	logger.Printf("[INFO] 🚀 Iniciando %s %s — %s", AgentName, AgentVersion, AgentAuthor)

	// Inicializar contexto antes de cualquier goroutine que lo use (buildUI, p2p, etc.)
	serverCtx, serverStop = context.WithCancel(context.Background())

	appInstance = app.NewWithID("com.afe.assistant")
	winTitle := fmt.Sprintf("%s %s", AgentName, AgentVersion)
	mainWindow = appInstance.NewWindow(winTitle)
	mainWindow.Resize(fyne.NewSize(float32(chatWinWidth), float32(chatWinHeight)))

	// Persistencia y configuración
	loadSettings()
	loadUserProfile()
	loadChatHistory()
	loadActionLog()
	loadPendingEvents()
	loadPlugins()
	loadDynamicRules()
	applyTheme()
	checkForLocalUpdate()

	// Base de conocimiento
	globalKB.Load()

	// Cola offline SQLite (entrega diferida de tickets y telemetría)
	initOfflineQueue()

	// Interfaz
	buildUI()

	// Servidores y goroutines principales
	startCommandListener()
	go retryPendingEvents(serverCtx)
	go proactiveMonitoring(serverCtx)

	// Gateway sync (registro + heartbeat)
	go startGatewaySync(serverCtx)

	// Sistema distribuido (P2P + KB maintenance)
	startP2P(serverCtx)
	startKBMaintenance(serverCtx)

	// Telemetría ligera: sondeo de ventana activa sin invadir privacidad.
	startForegroundTracker(serverCtx)

	// Ollama (no bloquea si no está disponible)
	initOllamaClient()

	if isFirstRun() {
		showFirstRunWizard()
		// Primera ejecución: notificar para que el usuario sepa que Frank está activo.
		sendNotification(AgentName, fmt.Sprintf("%s %s listo. Escribe 'ayuda' para comenzar.", AgentName, AgentVersion))
	} else {
		mainWindow.Hide()
		// Inicio silencioso: posicionar y aparecer en bandeja sin spam de notificaciones.
		positionWindowBottomRight(winTitle)
	}
	appInstance.Run()
	gracefulExit()
}
// ========================= BASE DE CONOCIMIENTO =========================

const (
	knowledgeTTL      = 7 * 24 * time.Hour
	knowledgeFile     = "knowledge.enc"
	maxKBItems        = 500
	kbSimilarityThreshold = 0.35
)

// KnowledgeItem representa un par pregunta/respuesta aprendido.
type KnowledgeItem struct {
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	Hash      string    `json:"hash"`
}

// KnowledgeBase es la base de conocimiento local thread-safe.
type KnowledgeBase struct {
	items []KnowledgeItem
	mu    sync.RWMutex
}

var globalKB = &KnowledgeBase{}

func normalizeQuestion(q string) string {
	return strings.ToLower(strings.TrimSpace(removeAccents(q)))
}

func (kb *KnowledgeBase) computeHash(q string) string {
	h := sha256.Sum256([]byte(normalizeQuestion(q)))
	return fmt.Sprintf("%x", h[:10])
}

// Add agrega o actualiza un ítem en la base de conocimiento.
func (kb *KnowledgeBase) Add(item KnowledgeItem) {
	if item.Question == "" || item.Answer == "" {
		return
	}
	item.Hash = kb.computeHash(item.Question)
	if item.Source == "" {
		item.Source = pcName
	}

	kb.mu.Lock()
	for i, existing := range kb.items {
		if existing.Hash == item.Hash {
			kb.items[i] = item
			kb.mu.Unlock()
			go kb.save()
			return
		}
	}
	kb.items = append(kb.items, item)
	if len(kb.items) > maxKBItems {
		kb.items = kb.items[len(kb.items)-maxKBItems:]
	}
	kb.mu.Unlock()
	go kb.save()
}

// Search busca la respuesta más similar a la consulta usando Jaccard.
// Devuelve "" si no encuentra nada sobre el umbral de similitud.
func (kb *KnowledgeBase) Search(query string) string {
	normalized := normalizeQuestion(query)
	now := time.Now()

	kb.mu.RLock()
	defer kb.mu.RUnlock()

	var best string
	bestScore := kbSimilarityThreshold

	for _, item := range kb.items {
		if now.Sub(item.Timestamp) > knowledgeTTL {
			continue
		}
		score := jaccardSimilarity(normalized, normalizeQuestion(item.Question))
		if score > bestScore {
			bestScore = score
			best = item.Answer
		}
	}
	return best
}

// Evict elimina ítems cuyo TTL expiró.
func (kb *KnowledgeBase) Evict() {
	now := time.Now()
	kb.mu.Lock()
	valid := kb.items[:0]
	for _, item := range kb.items {
		if now.Sub(item.Timestamp) <= knowledgeTTL {
			valid = append(valid, item)
		}
	}
	kb.items = valid
	kb.mu.Unlock()
	jsonLog("INFO", "kb_evict", fmt.Sprintf("Items válidos tras evict: %d", len(kb.items)))
}

// All devuelve una copia de todos los ítems (para compartir con peers).
func (kb *KnowledgeBase) All() []KnowledgeItem {
	kb.mu.RLock()
	defer kb.mu.RUnlock()
	result := make([]KnowledgeItem, len(kb.items))
	copy(result, kb.items)
	return result
}

// Len devuelve el número de ítems actuales.
func (kb *KnowledgeBase) Len() int {
	kb.mu.RLock()
	defer kb.mu.RUnlock()
	return len(kb.items)
}

func (kb *KnowledgeBase) save() {
	kb.mu.RLock()
	data, err := json.MarshalIndent(kb.items, "", "  ")
	kb.mu.RUnlock()
	if err != nil {
		logger.Printf("[ERROR] knowledge.save: marshal: %v", err)
		return
	}
	enc, err := protectData(data)
	if err != nil {
		logger.Printf("[ERROR] knowledge.save: cifrado: %v", err)
		return
	}
	if err := os.WriteFile(knowledgeFile, enc, 0600); err != nil {
		logger.Printf("[ERROR] knowledge.save: escritura: %v", err)
	}
}

// Load carga la base de conocimiento desde disco.
func (kb *KnowledgeBase) Load() {
	data, err := os.ReadFile(knowledgeFile)
	if err != nil {
		return
	}
	plain, err := unprotectData(data)
	if err != nil {
		logger.Printf("[ERROR] knowledge.Load: descifrado: %v", err)
		return
	}
	kb.mu.Lock()
	defer kb.mu.Unlock()
	if err := json.Unmarshal(plain, &kb.items); err != nil {
		logger.Printf("[ERROR] knowledge.Load: unmarshal: %v", err)
		kb.items = nil
		return
	}
	jsonLog("INFO", "kb_load", fmt.Sprintf("Cargados %d ítems de conocimiento", len(kb.items)))
}

// MergeFrom incorpora ítems de otro agente, evitando duplicados.
func (kb *KnowledgeBase) MergeFrom(items []KnowledgeItem) {
	for _, item := range items {
		if time.Since(item.Timestamp) > knowledgeTTL {
			continue
		}
		kb.Add(item)
	}
}

// startKBMaintenance inicia un ticker que hace evict periódico.
func startKBMaintenance(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				globalKB.Evict()
			case <-ctx.Done():
				globalKB.save()
				return
			}
		}
	}()
}

// ========================= P2P LAN =========================

const (
	p2pUDPPort           = 47890
	p2pBroadcastInterval = 60 * time.Second
	p2pPeerTTL           = 5 * time.Minute
	p2pQueryTimeout      = 2 * time.Second
	p2pMaxQueryLen       = 512
)

// AgentPeer representa un agente Frank descubierto en la LAN.
type AgentPeer struct {
	IP       string    `json:"ip"`
	Port     int       `json:"port"`
	Name     string    `json:"name"`
	Version  string    `json:"version"`
	Location string    `json:"location"` // ubicación AFE resuelta por IP
	Gateway  string    `json:"gateway"`  // gateway MPLS de esa subred
	LastSeen time.Time `json:"last_seen"`
}

type p2pAnnounce struct {
	Type     string `json:"type"`
	AgentIP  string `json:"agent_ip"`
	Port     int    `json:"port"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Location string `json:"location"` // ubicación AFE del agente emisor
	Gateway  string `json:"gateway"`  // gateway MPLS del agente emisor
}

var (
	p2pPeers   = make(map[string]*AgentPeer)
	p2pPeersMu sync.RWMutex
)

// startP2P inicia el broadcast UDP y la escucha en segundo plano.
// Si el puerto está ocupado, el P2P se deshabilita silenciosamente.
func startP2P(ctx context.Context) {
	go p2pListen(ctx)
	go p2pBroadcastLoop(ctx)
}

func p2pListen(ctx context.Context) {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf(":%d", p2pUDPPort))
	if err != nil {
		logger.Printf("[ERROR] p2p: resolve UDP: %v", err)
		return
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		logger.Printf("[WARN] p2p: puerto %d ocupado, P2P deshabilitado", p2pUDPPort)
		return
	}
	defer conn.Close()

	// Cerrar conexión cuando el contexto se cancela
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	jsonLog("INFO", "p2p_listen", fmt.Sprintf("escuchando UDP :%d", p2pUDPPort))
	buf := make([]byte, 2048)
	myIP := getOutboundIP()

	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if remote.IP.String() == myIP {
			continue // ignorar propio broadcast
		}
		var msg p2pAnnounce
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}
		if msg.Type != "announce" || msg.AgentIP == "" {
			continue
		}
		// Si el anuncio no trae ubicación, resolver por IP.
		loc := msg.Location
		gw := msg.Gateway
		if loc == "" {
			loc, gw = resolvePeerLocation(msg.AgentIP)
		}
		p2pPeersMu.Lock()
		p2pPeers[msg.AgentIP] = &AgentPeer{
			IP:       msg.AgentIP,
			Port:     msg.Port,
			Name:     msg.Name,
			Version:  msg.Version,
			Location: loc,
			Gateway:  gw,
			LastSeen: time.Now(),
		}
		p2pPeersMu.Unlock()
		locStr := ""
		if loc != "" {
			locStr = " [" + loc + "]"
		}
		jsonLog("INFO", "p2p_peer", fmt.Sprintf("Agente detectado: %s @ %s:%d%s", msg.Name, msg.AgentIP, msg.Port, locStr))
	}
}

func p2pBroadcastLoop(ctx context.Context) {
	p2pSendBroadcast()
	ticker := time.NewTicker(p2pBroadcastInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p2pSendBroadcast()
			p2pEvictStalePeers()
		case <-ctx.Done():
			return
		}
	}
}

func p2pSendBroadcast() {
	myIP := getOutboundIP()

	// Determinar el puerto HTTP del agente (desde la variable global)
	port := 8081
	if httpListenAddr != "" {
		if _, portStr, err := net.SplitHostPort(httpListenAddr); err == nil {
			if p, err := net.LookupPort("tcp", portStr); err == nil {
				port = p
			}
		}
	}

	myLoc, myGW := resolvePeerLocation(myIP)
	msg := p2pAnnounce{
		Type:     "announce",
		AgentIP:  myIP,
		Port:     port,
		Name:     AgentName + "@" + pcName,
		Version:  AgentVersion,
		Location: myLoc,
		Gateway:  myGW,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	bcastAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", p2pUDPPort))
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp4", nil, bcastAddr)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(1 * time.Second))
	conn.Write(data)
}

func p2pEvictStalePeers() {
	threshold := time.Now().Add(-p2pPeerTTL)
	p2pPeersMu.Lock()
	defer p2pPeersMu.Unlock()
	for ip, peer := range p2pPeers {
		if peer.LastSeen.Before(threshold) {
			delete(p2pPeers, ip)
		}
	}
}

// addP2PEndpoints registra los endpoints P2P en el mux HTTP.
// Llamado desde startCommandListener antes de iniciar el servidor.
func addP2PEndpoints(mux *http.ServeMux) {
	// /ping — sin autenticación, solo identifica al agente
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		myIP := getOutboundIP()
		loc, gw := resolvePeerLocation(myIP)
		data, _ := json.Marshal(map[string]interface{}{
			"agent":    AgentName + "@" + pcName,
			"version":  AgentVersion,
			"ip":       myIP,
			"location": loc,
			"gateway":  gw,
			"uptime":   time.Since(agentStartTime).Round(time.Second).String(),
		})
		if _, err := w.Write(data); err != nil {
			logger.Printf("[ERROR] /ping write: %v", err)
		}
	})

	// /knowledge — exportar/importar ítems de KB
	// Acepta tanto agentAPIKey (gateway) como p2pSharedKey (peers LAN).
	mux.HandleFunc("/knowledge", func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key != agentAPIKey && key != p2pSharedKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			items := globalKB.All()
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(items); err != nil {
				logger.Printf("[ERROR] /knowledge encode: %v", err)
			}
		case http.MethodPost:
			var item KnowledgeItem
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}
			item.Source = r.RemoteAddr
			globalKB.Add(item)
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	})

	// /query?q=... — busca en KB local y devuelve la mejor respuesta
	// Acepta tanto agentAPIKey como p2pSharedKey.
	mux.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key != agentAPIKey && key != p2pSharedKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		q := r.URL.Query().Get("q")
		if q == "" || len(q) > p2pMaxQueryLen {
			http.Error(w, "invalid q param", http.StatusBadRequest)
			return
		}
		answer := globalKB.Search(q)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"answer": answer}); err != nil {
			logger.Printf("[ERROR] /query encode: %v", err)
		}
	})

	// /status — estado del agente: telemetría, ubicación, app activa.
	// Accesible con p2pSharedKey (inter-agente) o agentAPIKey (gateway).
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key != agentAPIKey && key != p2pSharedKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		myIP := getOutboundIP()
		loc, gw := resolvePeerLocation(myIP)
		telemetry.mu.Lock()
		cmds := telemetry.CommandsExecuted
		errs := telemetry.ErrorsCount
		tickets := telemetry.TicketsCreated
		telemetry.mu.Unlock()

		appStats := getForegroundStats()

		p2pPeersMu.RLock()
		peerCount := len(p2pPeers)
		p2pPeersMu.RUnlock()

		payload := map[string]interface{}{
			"agent":   AgentName + "@" + pcName,
			"version": AgentVersion,
			"ip":      myIP,
			"location": loc,
			"gateway": gw,
			"uptime":  time.Since(agentStartTime).Round(time.Second).String(),
			"telemetry": map[string]int{
				"commands": cmds,
				"errors":   errs,
				"tickets":  tickets,
			},
			"active_apps": appStats,
			"peers":       peerCount,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			logger.Printf("[ERROR] /status encode: %v", err)
		}
	})

	// /peers — lista de agentes conocidos en la red LAN.
	mux.HandleFunc("/peers", func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		key := r.Header.Get("X-API-Key")
		if key != agentAPIKey && key != p2pSharedKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		p2pPeersMu.RLock()
		snapshot := make([]*AgentPeer, 0, len(p2pPeers))
		for _, p := range p2pPeers {
			snapshot = append(snapshot, p)
		}
		p2pPeersMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(snapshot); err != nil {
			logger.Printf("[ERROR] /peers encode: %v", err)
		}
	})
}

// queryPeers consulta todos los peers conocidos y devuelve la primera respuesta útil.
func queryPeers(query string) string {
	if len(query) > p2pMaxQueryLen {
		return ""
	}
	p2pPeersMu.RLock()
	myIP := getOutboundIP()
	snapshot := make([]*AgentPeer, 0, len(p2pPeers))
	for _, p := range p2pPeers {
		if p.IP != myIP {
			snapshot = append(snapshot, p)
		}
	}
	p2pPeersMu.RUnlock()

	for _, peer := range snapshot {
		if answer := queryOnePeer(peer, query); answer != "" {
			return answer
		}
	}
	return ""
}

func queryOnePeer(peer *AgentPeer, query string) string {
	endpoint := fmt.Sprintf("http://%s:%d/query?q=%s",
		peer.IP, peer.Port, url.QueryEscape(query))

	client := &http.Client{Timeout: p2pQueryTimeout}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	// Usar p2pSharedKey para autenticarse con otros agentes Frank.
	// agentAPIKey es única por equipo y no sirve para peers.
	req.Header.Set("X-API-Key", p2pSharedKey)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	answer := strings.TrimSpace(result["answer"])
	return answer
}

// PeerCount devuelve el número de agentes activos en la LAN.
func PeerCount() int {
	p2pPeersMu.RLock()
	defer p2pPeersMu.RUnlock()
	return len(p2pPeers)
}

// ========================= OLLAMA + CONSENSO 3 CEREBROS =========================

const (
	ollamaDefaultURL        = "http://192.168.1.246:11434/api/generate"
	ollamaDefaultModel      = "phi4-mini:3.8b"
	ollamaCacheTTL          = 30 * time.Minute
	ollamaRateInterval      = 3 * time.Second
	ollamaMaxCacheItems     = 200
	ollamaMaxResponseLen    = 600            // caracteres máximos en respuesta al usuario
	ollamaMinQueryWords     = 3             // palabras mínimas para activar Ollama
	ollamaHealthTimeout     = 3 * time.Second // timeout para el ping de disponibilidad
	ollamaCircuitResetAfter = 2 * time.Minute // tiempo mínimo entre reintentos tras fallo
)

// ollamaRequestTimeout controla SOLO el ping de disponibilidad (GET /api/tags).
// ollamaInferTimeout controla la inferencia real — configurable via OLLAMA_INFER_SECS (default 30).
// ollamaConsensusTimeout = ollamaInferTimeout * 1.5 + 5s (tiempo total de los 3 cerebros en paralelo).
var (
	ollamaRequestTimeout   = 3 * time.Second  // health-check únicamente
	ollamaInferTimeout     = 30 * time.Second // inferencia LLM
	ollamaConsensusTimeout = 50 * time.Second // 30s*1.5 + 5s
)

// ========================= FILTROS =========================

// dangerousResponsePatterns bloquea respuestas con contenido peligroso.
var dangerousResponsePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)net\s+user\s+\S+\s+\S+\s+/add`),
	regexp.MustCompile(`(?i)net\s+localgroup\s+administrators`),
	regexp.MustCompile(`(?i)format\s+[a-zA-Z]:`),
	regexp.MustCompile(`(?i)shutdown\s+/[rfRF]`),
	regexp.MustCompile(`(?i)rm\s+-[rR][fF]\s+/`),
	regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[=:]\s*\S{4,}`),
	regexp.MustCompile(`(?i)reg\s+(delete|add)\s+HKL[A-Z]`),
	regexp.MustCompile(`(?i)bcdedit|bootmgr|mbr\s+fix`),
}

// injectionPatterns detecta intentos de manipular el comportamiento del LLM.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(ignore|forget|override)\s+(previous|prior|all)\s+instructions`),
	regexp.MustCompile(`(?i)jailbreak|DAN\s+mode|pretend\s+you\s+(are|have\s+no)`),
	regexp.MustCompile(`(?i)(system\s+prompt|<\s*system\s*>)`),
	regexp.MustCompile(`(?i)act\s+as\s+(an?\s+)?(evil|unfiltered|unrestricted|hacker)`),
	regexp.MustCompile(`(?i)new\s+instructions?\s*:`),
}

// validateQuery devuelve error si la consulta no debe enviarse a Ollama.
func validateQuery(input string) error {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) < 10 {
		return fmt.Errorf("consulta demasiado corta (%d chars)", len(trimmed))
	}
	if len(strings.Fields(trimmed)) < ollamaMinQueryWords {
		return fmt.Errorf("mínimo %d palabras requeridas", ollamaMinQueryWords)
	}
	for _, pat := range injectionPatterns {
		if pat.MatchString(trimmed) {
			jsonLog("WARN", "ollama_inject", fmt.Sprintf("intento de inyección bloqueado: %q", trimmed[:minInt(40, len(trimmed))]))
			return fmt.Errorf("consulta bloqueada por filtro de seguridad")
		}
	}
	return nil
}

// sanitizeResponse filtra y recorta la respuesta de Ollama.
// Devuelve (respuestaLimpia, true) o ("", false) si debe bloquearse.
func sanitizeResponse(resp string) (string, bool) {
	for _, pat := range dangerousResponsePatterns {
		if pat.MatchString(resp) {
			jsonLog("WARN", "ollama_safety", "respuesta bloqueada por patrón peligroso")
			return "", false
		}
	}
	runes := []rune(strings.TrimSpace(resp))
	if len(runes) > ollamaMaxResponseLen {
		resp = string(runes[:ollamaMaxResponseLen]) + "…"
	}
	return strings.TrimSpace(resp), true
}

// ========================= CLIENTE =========================

type ollamaCacheEntry struct {
	response  string
	expiresAt time.Time
}

// OllamaClient gestiona la comunicación con el servidor Ollama remoto.
type OllamaClient struct {
	url             string
	model           string
	httpClient      *http.Client
	cache           map[string]ollamaCacheEntry
	cacheMu         sync.RWMutex
	lastCall        time.Time
	rateMu          sync.Mutex
	reqTimeout      time.Duration
	consensusTimeout time.Duration
	// Circuit breaker
	available   atomic.Bool
	lastFailure time.Time
	failMu      sync.Mutex
}

var globalOllama *OllamaClient

func initOllamaClient() {
	// OLLAMA_INFER_SECS controla el timeout de inferencia (default 30s).
	inferSecs := 30
	if v := os.Getenv("OLLAMA_INFER_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			inferSecs = n
		}
	}
	ollamaInferTimeout = time.Duration(inferSecs) * time.Second
	// Consenso: 3 cerebros en paralelo, damos margen de 1.5× + 5s overhead.
	ollamaConsensusTimeout = time.Duration(float64(inferSecs)*1.5+5) * time.Second

	globalOllama = &OllamaClient{
		url:   getEnv("OLLAMA_URL", ollamaDefaultURL),
		model: getEnv("OLLAMA_MODEL", ollamaDefaultModel),
		httpClient: &http.Client{
			// Usa ollamaInferTimeout para las llamadas de inferencia.
			// checkHealth() impone su propio context de 3s (ollamaHealthTimeout).
			Timeout: ollamaInferTimeout,
		},
		cache:            make(map[string]ollamaCacheEntry),
		reqTimeout:       ollamaInferTimeout,
		consensusTimeout: ollamaConsensusTimeout,
	}
	// Optimista: asumir Ollama disponible; el primer fallo real abrirá el circuit.
	globalOllama.available.Store(true)
	jsonLog("INFO", "ollama_init", fmt.Sprintf("url=%s model=%s infer_timeout=%ds consensus_timeout=%s",
		globalOllama.url, globalOllama.model, inferSecs, ollamaConsensusTimeout))

	// Health check en background — confirma disponibilidad real sin bloquear el arranque.
	go globalOllama.runHealthCheck(serverCtx)
}

func (c *OllamaClient) cacheKey(prompt string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(prompt))))
	return fmt.Sprintf("%x", h[:12])
}

func (c *OllamaClient) getFromCache(key string) (string, bool) {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	entry, ok := c.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.response, true
}

func (c *OllamaClient) setCache(key, response string) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if len(c.cache) >= ollamaMaxCacheItems {
		for k, v := range c.cache {
			if time.Now().After(v.expiresAt) {
				delete(c.cache, k)
				break
			}
		}
	}
	c.cache[key] = ollamaCacheEntry{
		response:  response,
		expiresAt: time.Now().Add(ollamaCacheTTL),
	}
}

func (c *OllamaClient) checkRateLimit() bool {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	if time.Since(c.lastCall) < ollamaRateInterval {
		return false
	}
	c.lastCall = time.Now()
	return true
}

// checkHealth hace un GET /api/tags con ollamaHealthTimeout (3s).
// Devuelve true si Ollama responde con 200 OK.
func (c *OllamaClient) checkHealth() bool {
	healthURL := strings.TrimSuffix(strings.TrimRight(c.url, "/"), "/api/generate") + "/api/tags"
	ctx, cancel := context.WithTimeout(context.Background(), ollamaHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// runHealthCheck comprueba la disponibilidad de Ollama cada 30 s y actualiza
// c.available para que el circuit breaker en Ask() actúe sin latencia.
func (c *OllamaClient) runHealthCheck(ctx context.Context) {
	check := func() {
		ok := c.checkHealth()
		if ok != c.available.Load() {
			jsonLog("INFO", "ollama_health", fmt.Sprintf("disponibilidad cambió: available=%v", ok))
		}
		c.available.Store(ok)
	}
	check() // verificación inmediata al arrancar
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			check()
		case <-ctx.Done():
			return
		}
	}
}

// ========================= LLAMADA HTTP ÚNICA =========================

type ollamaRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Stream  bool   `json:"stream"`
	Options struct {
		NumPredict  int     `json:"num_predict"`
		Temperature float64 `json:"temperature"`
	} `json:"options"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

// doRequest es la llamada HTTP de bajo nivel sin caché ni rate limit.
// Llamado exclusivamente por askThreeBrains en goroutines concurrentes.
func (c *OllamaClient) doRequest(ctx context.Context, prompt string, temperature float64) (string, error) {
	req := ollamaRequest{
		Model:  c.model,
		Prompt: prompt,
		Stream: false,
	}
	req.Options.NumPredict = 256
	req.Options.Temperature = temperature

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama HTTP %d", resp.StatusCode)
	}
	var or ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if or.Error != "" {
		return "", fmt.Errorf("ollama: %s", or.Error)
	}
	r := strings.TrimSpace(or.Response)
	if r == "" {
		return "", fmt.Errorf("respuesta vacía")
	}
	return r, nil
}

// ========================= CONSENSO 3 CEREBROS =========================

// askThreeBrains lanza 3 llamadas concurrentes con temperaturas 0.4 / 0.7 / 0.9
// impersonando el mismo rol, y elige por consenso de similitud Jaccard.
func (c *OllamaClient) askThreeBrains(ctx context.Context, userInput string) (string, error) {
	prompt := buildOllamaPrompt(userInput)
	temps := [3]float64{0.4, 0.7, 0.9}

	var (
		results [3]string
		errs    [3]error
		wg      sync.WaitGroup
	)
	for i := range temps {
		wg.Add(1)
		go func(idx int, temp float64) {
			defer wg.Done()
			results[idx], errs[idx] = c.doRequest(ctx, prompt, temp)
		}(i, temps[i])
	}
	wg.Wait()

	// Recoger respuestas que pasan el filtro de seguridad
	var valid []string
	for i := range results {
		if errs[i] != nil {
			jsonLog("WARN", "ollama_brain", fmt.Sprintf("cerebro %d falló: %v", i, errs[i]))
			continue
		}
		safe, ok := sanitizeResponse(results[i])
		if !ok {
			jsonLog("WARN", "ollama_brain", fmt.Sprintf("cerebro %d bloqueado por filtro de seguridad", i))
			continue
		}
		valid = append(valid, safe)
	}

	switch len(valid) {
	case 0:
		return "", fmt.Errorf("los 3 cerebros fallaron o fueron filtrados")
	case 1:
		jsonLog("INFO", "ollama_consensus", "1/3 válidos — sin consenso, usando única respuesta")
		return valid[0], nil
	default:
		chosen := consensusPick(valid)
		jsonLog("INFO", "ollama_consensus", fmt.Sprintf("%d/3 válidos — consenso elegido", len(valid)))
		return chosen, nil
	}
}

// consensusPick elige la respuesta con mayor similitud Jaccard media respecto al resto.
// Con 2 respuestas elige la más corta (más directa) si la similitud es igual.
func consensusPick(responses []string) string {
	if len(responses) == 1 {
		return responses[0]
	}
	scores := make([]float64, len(responses))
	for i := range responses {
		normI := normalizeQuestion(responses[i])
		for j := range responses {
			if i != j {
				scores[i] += jaccardSimilarity(normI, normalizeQuestion(responses[j]))
			}
		}
	}
	best := 0
	for i, s := range scores {
		if s > scores[best] || (s == scores[best] && len(responses[i]) < len(responses[best])) {
			best = i
		}
	}
	return responses[best]
}

// ========================= API PÚBLICA =========================

// Ask es el punto de entrada del cliente: circuit breaker → caché → rate limit → 3 cerebros.
func (c *OllamaClient) Ask(ctx context.Context, userInput string) (string, error) {
	// Circuit breaker: evitar esperas largas si Ollama estuvo caído recientemente.
	if !c.available.Load() {
		c.failMu.Lock()
		sinceFailure := time.Since(c.lastFailure)
		c.failMu.Unlock()
		if sinceFailure < ollamaCircuitResetAfter {
			return "", fmt.Errorf("circuit abierto: Ollama no disponible (reintento en %s)",
				(ollamaCircuitResetAfter - sinceFailure).Round(time.Second))
		}
		// Pasó tiempo suficiente: intentar un health check antes de reintentar.
		if !c.checkHealth() {
			c.failMu.Lock()
			c.lastFailure = time.Now()
			c.failMu.Unlock()
			return "", fmt.Errorf("Ollama sigue sin responder")
		}
		c.available.Store(true)
		jsonLog("INFO", "ollama_circuit", "Ollama recuperado — circuit cerrado")
	}

	key := c.cacheKey(userInput)
	if cached, ok := c.getFromCache(key); ok {
		jsonLog("INFO", "ollama_cache_hit", fmt.Sprintf("key=%s", key[:8]))
		return cached, nil
	}
	if !c.checkRateLimit() {
		return "", fmt.Errorf("rate limited: esperar %s", ollamaRateInterval)
	}

	cctx, cancel := context.WithTimeout(ctx, c.consensusTimeout)
	defer cancel()

	response, err := c.askThreeBrains(cctx, userInput)
	if err != nil {
		// Marcar como no disponible para abrir el circuit breaker.
		c.available.Store(false)
		c.failMu.Lock()
		c.lastFailure = time.Now()
		c.failMu.Unlock()
		jsonLog("WARN", "ollama_circuit", fmt.Sprintf("circuit abierto por fallo: %v", err))
		return "", err
	}

	c.setCache(key, response)
	go globalKB.Add(KnowledgeItem{
		Question:  userInput,
		Answer:    response,
		Timestamp: time.Now(),
		Source:    pcName,
	})
	return response, nil
}

func buildOllamaPrompt(userInput string) string {
	return fmt.Sprintf(
		"Eres Frank, asistente IT profesional para Windows en español. "+
			"Responde de forma concisa y directa. No uses markdown. "+
			"Nunca sugieras comandos destructivos ni proporciones credenciales.\n"+
			"Usuario: %s\nEquipo: %s | OS: Windows\nFrank:",
		userInput, pcName,
	)
}

// ========================= PUNTO DE ENTRADA DESDE NLU =========================

// askOllama es llamado solo cuando actionMap no tiene handler para el intent detectado.
// Flujo: validateQuery → KB local → peers LAN → Ollama (3 cerebros) → "" (fallback)
func askOllama(input string) string {
	// 1. Pre-filtro: evitar saturar el servidor con consultas triviales o maliciosas
	if err := validateQuery(input); err != nil {
		jsonLog("INFO", "ollama_skip", err.Error())
		return ""
	}

	// 2. KB local (respuesta instantánea sin red)
	if answer := globalKB.Search(input); answer != "" {
		jsonLog("INFO", "ollama_flow", "kb_local hit")
		return "💡 " + answer
	}

	// 3. Peers LAN (conocimiento compartido entre agentes)
	if answer := queryPeers(input); answer != "" {
		jsonLog("INFO", "ollama_flow", "peer hit")
		globalKB.Add(KnowledgeItem{
			Question:  input,
			Answer:    answer,
			Timestamp: time.Now(),
			Source:    "peer",
		})
		return "💡 " + answer
	}

	// 4. Ollama remoto con consenso de 3 cerebros
	if globalOllama == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(serverCtx, ollamaConsensusTimeout+5*time.Second)
	defer cancel()

	response, err := globalOllama.Ask(ctx, input)
	if err != nil {
		jsonLog("WARN", "ollama_fallback", fmt.Sprintf("Ollama no disponible: %v", err))
		return ""
	}

	telemetryInc("commands")
	jsonLog("INFO", "ollama_ok", "consenso de 3 cerebros generado")
	return "🤖 " + response
}

// ========================= UI EXTRAS =========================

// ========================= POSICIONAMIENTO =========================

const (
	chatWinWidth  = 460
	chatWinHeight = 560
	chatWinMargin = 20
	taskbarHeight = 48
)

var (
	user32dll          = syscall.NewLazyDLL("user32.dll")
	procFindWindow     = user32dll.NewProc("FindWindowW")
	procSetWinPos      = user32dll.NewProc("SetWindowPos")
	procGetSysMetrics  = user32dll.NewProc("GetSystemMetrics")
	procGetForeground  = user32dll.NewProc("GetForegroundWindow")
	procGetWindowTextW = user32dll.NewProc("GetWindowTextW")
)

// ── Telemetría de ventana activa (GetForegroundWindow) ──────────────────────
// Sondea cada 10 s el proceso en primer plano.
// Solo guarda el nombre del ejecutable — sin contenido del usuario.

type activeAppSample struct {
	App  string    `json:"app"`
	Time time.Time `json:"time"`
}

var (
	fgTrackerMu      sync.Mutex
	fgCurrentApp     string
	fgAppUsage       = map[string]time.Duration{} // exe → tiempo acumulado
	fgLastSample     time.Time
	fgRecentSamples  []activeAppSample // circular, máx 60 muestras (~10 min)
)

// getForegroundExe devuelve el nombre del ejecutable de la ventana en primer plano.
// Usa solo GetForegroundWindow + GetWindowTextW — sin WMI ni comandos externos.
func getForegroundExe() string {
	hwnd, _, _ := procGetForeground.Call()
	if hwnd == 0 {
		return ""
	}
	buf := make([]uint16, 512)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title := syscall.UTF16ToString(buf)
	if title == "" {
		return ""
	}
	// Devolver solo los primeros 80 chars del título — no el contenido del usuario.
	if len(title) > 80 {
		title = title[:80]
	}
	return title
}

// startForegroundTracker inicia el loop de muestreo de ventana activa.
// Sondea cada 10 segundos; acumula tiempo por app y mantiene buffer circular.
func startForegroundTracker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		var lastApp string
		var lastTick time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				app := getForegroundExe()
				if app == "" {
					lastTick = t
					lastApp = app
					continue
				}
				fgTrackerMu.Lock()
				// Acumular tiempo para la app anterior.
				if lastApp != "" && !lastTick.IsZero() {
					fgAppUsage[lastApp] += t.Sub(lastTick)
				}
				fgCurrentApp = app
				fgLastSample = t
				// Buffer circular de 60 muestras.
				if len(fgRecentSamples) >= 60 {
					fgRecentSamples = fgRecentSamples[1:]
				}
				fgRecentSamples = append(fgRecentSamples, activeAppSample{App: app, Time: t})
				fgTrackerMu.Unlock()
				lastApp = app
				lastTick = t
			}
		}
	}()
}

// getForegroundStats devuelve un resumen de uso de apps (top 5 por tiempo).
func getForegroundStats() map[string]string {
	fgTrackerMu.Lock()
	defer fgTrackerMu.Unlock()
	type kv struct {
		app string
		dur time.Duration
	}
	var sorted []kv
	for k, v := range fgAppUsage {
		sorted = append(sorted, kv{k, v})
	}
	// Ordenar por tiempo desc.
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].dur > sorted[i].dur {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	result := make(map[string]string)
	limit := 5
	if len(sorted) < limit {
		limit = len(sorted)
	}
	for _, item := range sorted[:limit] {
		mins := int(item.dur.Minutes())
		result[item.app] = fmt.Sprintf("%dm", mins)
	}
	result["_current"] = fgCurrentApp
	return result
}

// positionWindowBottomRight mueve la ventana principal al rincón inferior derecho.
// Usa user32.dll porque Fyne no expone SetWindowPos en su API pública.
// Se llama con un delay tras Show() para que GLFW haya asignado el HWND.
func positionWindowBottomRight(title string) {
	go func() {
		time.Sleep(300 * time.Millisecond) // esperar que GLFW cree la ventana

		const SM_CXSCREEN, SM_CYSCREEN = 0, 1
		sw, _, _ := procGetSysMetrics.Call(uintptr(SM_CXSCREEN))
		sh, _, _ := procGetSysMetrics.Call(uintptr(SM_CYSCREEN))
		if sw == 0 || sh == 0 {
			return
		}

		titlePtr, err := syscall.UTF16PtrFromString(title)
		if err != nil {
			return
		}
		hwnd, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(titlePtr)))
		if hwnd == 0 {
			return
		}

		x := int(sw) - chatWinWidth - chatWinMargin
		y := int(sh) - chatWinHeight - chatWinMargin - taskbarHeight

		const swpNoZOrder = 0x0004
		const swpNoActivate = 0x0010
		procSetWinPos.Call(
			hwnd, 0,
			uintptr(x), uintptr(y),
			uintptr(chatWinWidth), uintptr(chatWinHeight),
			swpNoZOrder|swpNoActivate,
		)
	}()
}

// ========================= CHAT BURBUJA =========================

// chatBubbleColors define los colores para cada rol según el tema activo.
type chatBubbleColors struct {
	UserBG    color.Color
	UserFG    color.Color
	FrankBG   color.Color
	FrankFG   color.Color
	SystemFG  color.Color
}

func getBubbleColors() chatBubbleColors {
	settingsMutex.RLock()
	themeVal := settings.Theme
	accentName := settings.AccentColor
	userTextName := settings.UserBubbleTextColor
	frankTextName := settings.FrankBubbleTextColor
	settingsMutex.RUnlock()

	// Obtener color de acento elegido por el usuario.
	accentHex, ok := accentColors[accentName]
	if !ok {
		accentHex = "#1976D2"
	}
	accent := parseHexColor(accentHex)
	accentWithAlpha := func(a uint8) color.Color {
		r, g, b, _ := accent.RGBA()
		return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: a}
	}

	// Resolver colores de texto personalizados con fallbacks sensatos.
	resolveTextColor := func(name, fallbackHex string) color.Color {
		if hex, ok := textColors[name]; ok {
			return parseHexColor(hex)
		}
		return parseHexColor(fallbackHex)
	}

	switch themeVal {
	case "high_contrast":
		// Alto contraste: ignorar personalización de texto, usar blanco/negro fijo.
		return chatBubbleColors{
			UserBG:   color.RGBA{R: 255, G: 235, B: 0, A: 255},
			UserFG:   color.Black,
			FrankBG:  color.RGBA{R: 40, G: 40, B: 40, A: 255},
			FrankFG:  color.White,
			SystemFG: color.RGBA{R: 200, G: 200, B: 200, A: 255},
		}
	case "dark":
		return chatBubbleColors{
			UserBG:   accentWithAlpha(220),
			UserFG:   resolveTextColor(userTextName, "#FFFFFF"),
			FrankBG:  color.RGBA{R: 55, G: 55, B: 60, A: 220},
			FrankFG:  resolveTextColor(frankTextName, "#DCDCDC"),
			SystemFG: color.RGBA{R: 150, G: 150, B: 150, A: 255},
		}
	default: // light
		return chatBubbleColors{
			UserBG:   accentWithAlpha(230),
			UserFG:   resolveTextColor(userTextName, "#FFFFFF"),
			FrankBG:  color.RGBA{R: 235, G: 240, B: 255, A: 255},
			FrankFG:  resolveTextColor(frankTextName, "#1A237E"),
			SystemFG: color.RGBA{R: 70, G: 80, B: 100, A: 255},
		}
	}
}

// makeBubble construye solo la burbuja con fondo redondeado y texto.
// La alineación (izquierda/derecha) la decide addBubble con layout.NewSpacer().
// makeChatRow construye una fila de chat con fondo de color.
// Los items van directamente al VBox para que reciban el ancho completo
// disponible — así el label envuelve correctamente sin texto vertical.
// alignRight: el texto se alinea a la derecha (mensajes del usuario).
func makeChatRow(text string, bgColor color.Color, alignRight bool) fyne.CanvasObject {
	// rawText sin prefijos de rol para copiar al portapapeles
	rawText := text
	if len(rawText) > 4 && (rawText[:3] == "▶ T" || rawText[:3] == "🤖 ") {
		// quitar prefijos decorativos: "▶ Tú: " (6) ó "🤖 " (variable en bytes)
		if idx := strings.Index(rawText, ": "); idx > 0 && idx < 12 {
			rawText = rawText[idx+2:]
		} else if strings.HasPrefix(rawText, "🤖 ") {
			rawText = strings.TrimPrefix(rawText, "🤖 ")
		}
	}

	var lbl *widget.Label
	if alignRight {
		lbl = widget.NewLabelWithStyle(text, fyne.TextAlignTrailing, fyne.TextStyle{Bold: true})
	} else {
		lbl = widget.NewLabel(text)
	}
	lbl.Wrapping = fyne.TextWrapWord

	bg := canvas.NewRectangle(bgColor)
	bg.CornerRadius = 8

	// Botón de copiar — pequeño, bajo contraste, en la esquina superior derecha
	captured := rawText
	copyBtn := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
		mainWindow.Clipboard().SetContent(captured)
	})
	copyBtn.Importance = widget.LowImportance

	// Layout: fondo completo, texto con padding, botón superpuesto arriba-derecha
	content := container.NewBorder(nil, nil, nil, copyBtn, container.NewPadded(lbl))
	return container.NewStack(bg, content)
}

// maxWidthLayout queda como utilidad auxiliar para settings preview.
type maxWidthLayout struct {
	max float32
}

func (m *maxWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		w := size.Width
		if w > m.max {
			w = m.max
		}
		o.Resize(fyne.NewSize(w, size.Height))
	}
}

func (m *maxWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) == 0 {
		return fyne.NewSize(0, 0)
	}
	min := objects[0].MinSize()
	if min.Width > m.max {
		min.Width = m.max
	}
	return min
}

// ========================= CHAT VBOX (SISTEMA DE BURBUJAS) =========================

var (
	chatVBox      *fyne.Container
	chatScrollNew *container.Scroll
)

// initBubbleChat crea el área de chat basada en VBox con burbujas.
// Devuelve el scroll container para incluir en el layout.
func initBubbleChat() *container.Scroll {
	chatVBox = container.NewVBox()
	chatScrollNew = container.NewVScroll(chatVBox)
	chatScrollNew.SetMinSize(fyne.NewSize(float32(chatWinWidth-20), 300))

	// Mensaje de bienvenida
	addBubble("system", fmt.Sprintf("🤖 %s %s — escribe 'ayuda' para ver comandos.", AgentName, AgentVersion))
	return chatScrollNew
}

// addBubble agrega una burbuja al chat. role: "user" | "frank" | "system"
// Debe llamarse desde el hilo principal de Fyne (o dentro de fyne.Do).
func addBubble(role, text string) {
	if chatVBox == nil || text == "" {
		return
	}
	cols := getBubbleColors()

	var obj fyne.CanvasObject
	switch role {
	case "user":
		obj = makeChatRow("▶ Tú: "+text, cols.UserBG, true)
	case "frank":
		obj = makeChatRow("🤖 "+text, cols.FrankBG, false)
	default: // "system"
		lbl := widget.NewLabel(text)
		lbl.Wrapping = fyne.TextWrapWord
		// En modo oscuro usar LowImportance (texto suave); en modo claro usar
		// MediumImportance para garantizar contraste sobre fondo claro.
		settingsMutex.RLock()
		isDark := settings.Theme == "dark"
		settingsMutex.RUnlock()
		if isDark {
			lbl.Importance = widget.LowImportance
		} else {
			lbl.Importance = widget.MediumImportance
		}
		obj = lbl
	}

	chatVBox.Add(obj)
	chatVBox.Add(widget.NewSeparator())
	chatVBox.Refresh()

	if chatScrollNew != nil {
		chatScrollNew.ScrollToBottom()
	}
}

// appendBubbleFromGoroutine es la versión thread-safe de addBubble.
// Debe usarse desde goroutines.
func appendBubbleFromGoroutine(role, text string) {
	fyne.Do(func() {
		addBubble(role, text)
	})
}

// addConfirmationBubble muestra la respuesta de Frank con botones SI/NO clickeables.
// El usuario puede confirmar pulsando el botón O escribiendo "si"/"no" en el input.
// Debe llamarse desde el hilo principal de Fyne.
func addConfirmationBubble(frankText string, input *widget.Entry, sendBtn *widget.Button) {
	if chatVBox == nil {
		return
	}
	cols := getBubbleColors()

	// Texto de la pregunta
	lbl := widget.NewLabel("🤖 " + frankText)
	lbl.Wrapping = fyne.TextWrapWord
	bgRect := canvas.NewRectangle(cols.FrankBG)
	bgRect.CornerRadius = 8

	captured := frankText
	copyBtn := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
		mainWindow.Clipboard().SetContent(captured)
	})
	copyBtn.Importance = widget.LowImportance
	textContent := container.NewBorder(nil, nil, nil, copyBtn, container.NewPadded(lbl))
	textRow := container.NewStack(bgRect, textContent)

	// Botones SI / NO — se deshabilitan al responder (evita doble-clic)
	var yesBtn, noBtn *widget.Button
	responded := false

	respond := func(answer string) {
		if responded {
			return
		}
		responded = true
		yesBtn.Disable()
		noBtn.Disable()
		// Procesar como si el usuario hubiera escrito la respuesta
		processMessage(answer, input, sendBtn)
	}

	yesBtn = widget.NewButtonWithIcon("✅  Sí, confirmar", theme.ConfirmIcon(), func() {
		respond("si")
	})
	yesBtn.Importance = widget.SuccessImportance

	noBtn = widget.NewButtonWithIcon("❌  No, cancelar", theme.CancelIcon(), func() {
		respond("no")
	})
	noBtn.Importance = widget.DangerImportance

	btnRow := container.NewGridWithColumns(2, yesBtn, noBtn)

	hint := widget.NewLabel("También podés escribir \"si\" o \"no\" en el chat.")
	hint.Importance = widget.LowImportance

	bubble := container.NewVBox(textRow, btnRow, hint)
	chatVBox.Add(bubble)
	chatVBox.Add(widget.NewSeparator())
	chatVBox.Refresh()
	if chatScrollNew != nil {
		chatScrollNew.ScrollToBottom()
	}
}

// rebuildChatBubbles reconstruye todas las burbujas del chat usando los colores
// actuales del tema. Necesario porque canvas.NewRectangle graba el color en el
// momento de creación; chatVBox.Refresh() no actualiza los fondos de color.
//
// Construye todos los objetos de una sola vez y hace UN único Refresh() al final,
// evitando N redibujados intermedios.
//
// DEBE llamarse desde el hilo principal de Fyne (o vía fyne.Do).
func rebuildChatBubbles() {
	if chatVBox == nil {
		return
	}

	// Copiar historial bajo mutex, sin mantenerlo durante la construcción de UI.
	historyMutex.RLock()
	msgs := make([]ChatMessage, len(chatHistoryData.Messages))
	copy(msgs, chatHistoryData.Messages)
	historyMutex.RUnlock()

	// Últimos 50 mensajes para no sobrecargar la UI.
	start := len(msgs) - 50
	if start < 0 {
		start = 0
	}
	recent := msgs[start:]

	// Obtener colores y modo de tema una sola vez.
	cols := getBubbleColors()
	settingsMutex.RLock()
	isDark := settings.Theme == "dark"
	settingsMutex.RUnlock()

	chatVBox.RemoveAll()

	// Mensaje de bienvenida (widget de sistema, usa Fyne theme para el texto).
	welcomeLbl := widget.NewLabel(fmt.Sprintf("🤖 %s %s — escribe 'ayuda' para ver comandos.", AgentName, AgentVersion))
	welcomeLbl.Wrapping = fyne.TextWrapWord
	if isDark {
		welcomeLbl.Importance = widget.LowImportance
	} else {
		welcomeLbl.Importance = widget.MediumImportance
	}
	chatVBox.Add(welcomeLbl)
	chatVBox.Add(widget.NewSeparator())

	// Reconstruir mensajes del historial con los colores del tema actual.
	for _, m := range recent {
		var obj fyne.CanvasObject
		switch m.Role {
		case "user":
			obj = makeChatRow("▶ Tú: "+m.Content, cols.UserBG, true)
		case "assistant":
			obj = makeChatRow("🤖 "+m.Content, cols.FrankBG, false)
		}
		if obj != nil {
			chatVBox.Add(obj)
			chatVBox.Add(widget.NewSeparator())
		}
	}

	// Un único Refresh() al final en lugar de uno por burbuja.
	chatVBox.Refresh()
	if chatScrollNew != nil {
		chatScrollNew.ScrollToBottom()
	}
}

// rebuildChatBubblesAsync es la versión thread-safe para llamar desde goroutines.
func rebuildChatBubblesAsync() {
	fyne.Do(rebuildChatBubbles)
}

// ========================= SETTINGS PREVIEW EN VIVO =========================

// liveThemePreview devuelve un container con preview del tema seleccionado.
func liveThemePreview(themeName, accentHex string) fyne.CanvasObject {
	var bgColor, textColor color.Color
	switch themeName {
	case "dark":
		bgColor = color.RGBA{R: 40, G: 40, B: 45, A: 255}
		textColor = color.RGBA{R: 220, G: 220, B: 220, A: 255}
	case "high_contrast":
		bgColor = color.Black
		textColor = color.White
		accentHex = "#FFEB00" // amarillo alto contraste
	default: // light
		bgColor = color.RGBA{R: 245, G: 245, B: 250, A: 255}
		textColor = color.RGBA{R: 30, G: 30, B: 30, A: 255}
	}
	accentCol := parseHexColor(accentHex)

	bg := canvas.NewRectangle(bgColor)
	bg.CornerRadius = 6

	msgBg := canvas.NewRectangle(accentCol)
	msgBg.CornerRadius = 6

	msgText := canvas.NewText("Mensaje de prueba", color.White)
	msgText.TextSize = 12

	bubble := container.NewStack(msgBg, container.NewPadded(msgText))

	caption := canvas.NewText("Vista previa", textColor)
	caption.TextSize = 11

	previewContent := container.NewVBox(caption, container.NewHBox(bubble))
	return container.NewStack(bg, container.NewPadded(previewContent))
}

// ========================= ESTADO DEL SISTEMA DISTRIBUIDO =========================

// distributedStatusLine devuelve una línea de estado para la UI.
// El número de agentes conectados es información interna y no se expone al usuario.
func distributedStatusLine() string {
	peers := PeerCount()
	if peers > 0 {
		// Solo log interno — el usuario no necesita ver la topología de red
		logger.Printf("[INFO] p2p: %d agente(s) activos en LAN", peers)
	}

	kbLen := globalKB.Len()
	if kbLen > 0 {
		return fmt.Sprintf("🧠 %d ítems", kbLen)
	}
	return ""
}
