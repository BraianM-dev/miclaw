package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ---------- Estructuras ----------
type SystemConfig struct {
	SystemPrompt string `json:"system_prompt"`
	ModelName    string `json:"model_name"`
	SafetyModel  string `json:"safety_model"`
	OllamaURL    string `json:"ollama_url"`
}

type Agent struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	IP       string    `json:"ip"`
	Port     int       `json:"port"`
	LastSeen time.Time `json:"last_seen"`
	Enabled  bool      `json:"enabled"`
}

type Action struct {
	ID              string     `json:"id"`
	AgentID         string     `json:"agent_id"`
	Command         string     `json:"command"`
	Parameters      string     `json:"parameters"`
	RollbackCommand string     `json:"rollback_command"`
	Status          string     `json:"status"`
	PreTelemetry    string     `json:"pre_telemetry"`
	PostTelemetry   string     `json:"post_telemetry"`
	CreatedAt       time.Time  `json:"created_at"`
	ApprovedAt      *time.Time `json:"approved_at"`
	ExecutedAt      *time.Time `json:"executed_at"`
	Result          string     `json:"result"`
	Requester       string     `json:"requester"`
}

type PlannerTask struct {
	Target   string                 `json:"target"`
	Command  string                 `json:"command"`
	Params   map[string]interface{} `json:"params"`
	Goal     string                 `json:"goal"`
	Rollback string                 `json:"rollback"`
}

type PlannerResponse struct {
	Tasks          []PlannerTask `json:"tasks"`
	DirectResponse string        `json:"direct_response"`
}

type ElizaTicket struct {
	ID        int    `json:"id"`
	PCName    string `json:"pc_name"`
	User      string `json:"user"`
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
	Category  string `json:"category"`
	Telemetry string `json:"telemetry"`
}

type OllamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OllamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []OllamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   string          `json:"format"`
}

type OllamaChatResponse struct {
	Model     string        `json:"model"`
	CreatedAt string        `json:"created_at"`
	Message   OllamaMessage `json:"message"`
	Done      bool          `json:"done"`
}

var (
	db          *sql.DB
	config      SystemConfig
	configMutex sync.RWMutex
	agents      map[string]Agent
	agentsMutex sync.RWMutex
	agentAPIKey string
)

// ---------- Base de Datos ----------
func initDB() error {
	var err error
	db, err = sql.Open("sqlite3", "./miclaw.db")
	if err != nil { return err }
	db.Exec("PRAGMA journal_mode=WAL")

	db.Exec(`CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS agents (id TEXT PRIMARY KEY, name TEXT, type TEXT, ip TEXT, port INTEGER, last_seen DATETIME, enabled BOOLEAN)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS actions (id TEXT PRIMARY KEY, agent_id TEXT, command TEXT, parameters TEXT, rollback_command TEXT, status TEXT, pre_telemetry TEXT, post_telemetry TEXT, created_at DATETIME, approved_at DATETIME, executed_at DATETIME, result TEXT, requester TEXT)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS permissions (command TEXT, agent_id TEXT, mode TEXT, PRIMARY KEY (command, agent_id))`)
	db.Exec(`CREATE TABLE IF NOT EXISTS eliza_tickets (id INTEGER PRIMARY KEY AUTOINCREMENT, pc_name TEXT, user TEXT, timestamp DATETIME, message TEXT, category TEXT, telemetry TEXT)`)
	
	return nil
}

func loadConfig() {
	configMutex.Lock()
	defer configMutex.Unlock()
	db.QueryRow("SELECT value FROM config WHERE key='system_prompt'").Scan(&config.SystemPrompt)
	db.QueryRow("SELECT value FROM config WHERE key='model_name'").Scan(&config.ModelName)
	db.QueryRow("SELECT value FROM config WHERE key='safety_model'").Scan(&config.SafetyModel)
	db.QueryRow("SELECT value FROM config WHERE key='ollama_url'").Scan(&config.OllamaURL)
	if config.ModelName == "" { config.ModelName = "phi4-mini:3.8b" }
	if config.OllamaURL == "" { config.OllamaURL = "http://localhost:11434/api/chat" }
}

func getAllAgents() []Agent {
	rows, _ := db.Query("SELECT id, name, type, ip, port, last_seen, enabled FROM agents")
	defer rows.Close()
	var list []Agent
	for rows.Next() {
		var a Agent
		var lastSeenStr string
		rows.Scan(&a.ID, &a.Name, &a.Type, &a.IP, &a.Port, &lastSeenStr, &a.Enabled)
		a.LastSeen, _ = time.Parse(time.RFC3339, lastSeenStr)
		list = append(list, a)
	}
	return list
}

func getAgent(id string) (Agent, bool) {
	var a Agent
	var lastSeenStr string
	err := db.QueryRow("SELECT id, name, type, ip, port, last_seen, enabled FROM agents WHERE id=?", id).Scan(&a.ID, &a.Name, &a.Type, &a.IP, &a.Port, &lastSeenStr, &a.Enabled)
	if err != nil { return a, false }
	a.LastSeen, _ = time.Parse(time.RFC3339, lastSeenStr)
	return a, true
}

func executeOnAgent(agent Agent, cmd string, params map[string]interface{}) (string, error) {
	payload := map[string]interface{}{"command": cmd, "parameters": params}
	payloadBytes, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://%s:%d/execute", agent.IP, agent.Port), bytes.NewBuffer(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", agentAPIKey)
	resp, err := client.Do(req)
	if err != nil { return "", err }
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

// ---------- Inteligencia Artificial & Tribunal ----------
func callOllama(model, prompt string) (string, error) {
	configMutex.RLock()
	url := config.OllamaURL
	configMutex.RUnlock()

	reqBody := OllamaChatRequest{
		Model: model,
		Messages: []OllamaMessage{{Role: "user", Content: prompt}},
		Stream: false, Format: "json",
	}
	jsonReq, _ := json.Marshal(reqBody)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonReq))
	if err != nil { return "", err }
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var oResp OllamaChatResponse
	json.Unmarshal(body, &oResp)
	return oResp.Message.Content, nil
}

func inferPermissionMode(agent Agent, command string) (string, string) {
	var savedMode string
	db.QueryRow(`SELECT mode FROM permissions WHERE command = ? AND (agent_id = ? OR agent_id = 'all')`, command, agent.ID).Scan(&savedMode)
	if savedMode != "" { return savedMode, "Regla de ML cargada de BD." }

	configMutex.RLock()
	model := config.SafetyModel
	if model == "" { model = config.ModelName }
	configMutex.RUnlock()

	promptSec := fmt.Sprintf(`Eres Auditor IT. Clasifica este comando: "%s" en equipo "%s". Si solo lee datos/telemetría responde "{"mode":"bypass"}". Si formatea/borra el SO responde "{"mode":"strict"}". Si reinicia servicios o cambia config responde "{"mode":"default"}".`, command, agent.Type)
	
	res, _ := callOllama(model, promptSec)
	if strings.Contains(res, "bypass") {
		db.Exec(`INSERT INTO permissions (command, agent_id, mode) VALUES (?, 'all', 'bypass')`, command)
		return "bypass", "Tribunal infiere comando inocuo."
	}
	if strings.Contains(res, "strict") { return "strict", "Tribunal bloquea comando destructivo." }
	return "default", "Tribunal requiere aprobación humana."
}

// ---------- Handlers API ----------
func apiElizaTicketHandler(w http.ResponseWriter, r *http.Request) {
	var t ElizaTicket
	json.NewDecoder(r.Body).Decode(&t)
	db.Exec(`INSERT INTO eliza_tickets (pc_name, user, timestamp, message, category, telemetry) VALUES (?, ?, ?, ?, ?, ?)`,
		t.PCName, t.User, t.Timestamp, t.Message, t.Category, t.Telemetry)
	w.WriteHeader(http.StatusOK)
}

func apiTicketsListHandler(w http.ResponseWriter, r *http.Request) {
	rows, _ := db.Query(`SELECT id, pc_name, user, timestamp, message, category, telemetry FROM eliza_tickets ORDER BY timestamp DESC LIMIT 30`)
	defer rows.Close()
	var tickets []ElizaTicket
	for rows.Next() {
		var t ElizaTicket
		rows.Scan(&t.ID, &t.PCName, &t.User, &t.Timestamp, &t.Message, &t.Category, &t.Telemetry)
		tickets = append(tickets, t)
	}
	json.NewEncoder(w).Encode(tickets)
}

func apiActionsHandler(w http.ResponseWriter, r *http.Request) {
	rows, _ := db.Query(`SELECT id, agent_id, command, status, result, created_at, rollback_command FROM actions ORDER BY created_at DESC LIMIT 50`)
	defer rows.Close()
	var actions []Action
	for rows.Next() {
		var a Action
		var caStr sql.NullString
		rows.Scan(&a.ID, &a.AgentID, &a.Command, &a.Status, &a.Result, &caStr, &a.RollbackCommand)
		if caStr.Valid { a.CreatedAt, _ = time.Parse(time.RFC3339, caStr.String) }
		actions = append(actions, a)
	}
	json.NewEncoder(w).Encode(actions)
}

func apiRollbackHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var agentID, rollbackCmd string
	db.QueryRow(`SELECT agent_id, rollback_command FROM actions WHERE id=?`, id).Scan(&agentID, &rollbackCmd)
	
	if rollbackCmd != "" && agentID != "" {
		if agent, ok := getAgent(agentID); ok {
			executeOnAgent(agent, rollbackCmd, nil)
			db.Exec(`UPDATE actions SET status='rolled_back', result='Revertido exitosamente' WHERE id=?`, id)
		}
	}
	w.Write([]byte("OK"))
}

// ---------- HTML UI (Tailwind CSS) ----------
const htmlUI = `<!DOCTYPE html>
<html lang="es" class="dark">
<head>
<meta charset="UTF-8"><title>Miclaw OS Command Center</title>
<script src="https://cdn.tailwindcss.com"></script>
<script>
  tailwind.config = { darkMode: 'class', theme: { extend: { colors: {
    gray: { 800: '#1e293b', 900: '#0f172a' }, primary: '#10b981'
  }}}}
</script>
</head>
<body class="bg-gray-900 text-slate-200 flex h-screen overflow-hidden font-sans">

<aside class="w-64 bg-gray-800 border-r border-slate-700 flex flex-col shadow-2xl z-10">
    <div class="p-6"><h1 class="text-2xl font-bold text-primary tracking-wider">MICLAW<span class="text-white">.os</span></h1><p class="text-xs text-slate-400">IT Command Center</p></div>
    <nav class="flex-1 px-4 space-y-2">
        <button onclick="nav('dash')" id="btn-dash" class="w-full text-left px-4 py-3 rounded-lg bg-slate-700 text-white font-medium transition-all">📊 Dashboard</button>
        <button onclick="nav('tickets')" id="btn-tickets" class="w-full text-left px-4 py-3 rounded-lg text-slate-400 hover:bg-slate-700 hover:text-white transition-all">🎫 Tickets (Eliza)</button>
        <button onclick="nav('timeline')" id="btn-timeline" class="w-full text-left px-4 py-3 rounded-lg text-slate-400 hover:bg-slate-700 hover:text-white transition-all">⏱️ Timeline & Rollback</button>
        <button onclick="nav('config')" id="btn-config" class="w-full text-left px-4 py-3 rounded-lg text-slate-400 hover:bg-slate-700 hover:text-white transition-all">⚙️ Configuración</button>
    </nav>
</aside>

<main class="flex-1 p-8 overflow-y-auto">
    
    <div id="view-dash" class="view block">
        <h2 class="text-3xl font-bold mb-6 text-white">Vista General</h2>
        <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
            <div class="bg-gray-800 p-6 rounded-xl border border-slate-700 shadow-lg"><h3 class="text-slate-400 text-sm">Equipos Online</h3><p class="text-4xl font-bold text-primary mt-2">52</p></div>
            <div class="bg-gray-800 p-6 rounded-xl border border-slate-700 shadow-lg"><h3 class="text-slate-400 text-sm">Tickets Pendientes</h3><p class="text-4xl font-bold text-yellow-500 mt-2">3</p></div>
            <div class="bg-gray-800 p-6 rounded-xl border border-slate-700 shadow-lg"><h3 class="text-slate-400 text-sm">Acciones IA Hoy</h3><p class="text-4xl font-bold text-blue-400 mt-2">14</p></div>
        </div>
    </div>

    <div id="view-tickets" class="view hidden">
        <h2 class="text-3xl font-bold mb-6 text-white">Bandeja de Entrada (Usuarios)</h2>
        <div class="bg-gray-800 rounded-xl border border-slate-700 overflow-hidden shadow-lg">
            <table class="w-full text-left">
                <thead class="bg-slate-700 text-slate-300"><tr><th class="p-4">Fecha</th><th class="p-4">Usuario / PC</th><th class="p-4">Problema</th><th class="p-4">Telemetría Capturada</th></tr></thead>
                <tbody id="tickets-body" class="divide-y divide-slate-700"></tbody>
            </table>
        </div>
    </div>

    <div id="view-timeline" class="view hidden">
        <h2 class="text-3xl font-bold mb-6 text-white">Auditoría y Rollback</h2>
        <div class="space-y-4" id="timeline-body"></div>
    </div>

    <div id="view-config" class="view hidden">
        <h2 class="text-3xl font-bold mb-6 text-white">Configuración del Gateway</h2>
        <div class="bg-gray-800 p-6 rounded-xl border border-slate-700 max-w-2xl">
            <label class="block text-slate-400 mb-2">Ollama URL</label>
            <input type="text" id="conf-url" class="w-full bg-slate-900 border border-slate-600 rounded p-3 text-white mb-4">
            <label class="block text-slate-400 mb-2">Modelo Principal</label>
            <input type="text" id="conf-model" class="w-full bg-slate-900 border border-slate-600 rounded p-3 text-white mb-6">
            <button class="bg-primary hover:bg-emerald-400 text-slate-900 font-bold py-3 px-6 rounded-lg shadow-lg">Guardar Cambios</button>
        </div>
    </div>

</main>

<script>
function nav(target) {
    document.querySelectorAll('.view').forEach(v => v.classList.add('hidden'));
    document.getElementById('view-' + target).classList.remove('hidden');
    document.querySelectorAll('aside button').forEach(b => {
        b.classList.remove('bg-slate-700', 'text-white');
        b.classList.add('text-slate-400');
    });
    document.getElementById('btn-' + target).classList.add('bg-slate-700', 'text-white');
    
    if(target === 'tickets') loadTickets();
    if(target === 'timeline') loadTimeline();
}

function loadTickets() {
    fetch('/api/eliza/tickets_list').then(r=>r.json()).then(data => {
        let html = '';
        data.forEach(t => {
            html += '<tr class="hover:bg-slate-750 transition-colors"><td class="p-4 text-sm text-slate-400">'+new Date(t.timestamp).toLocaleString()+'</td><td class="p-4"><p class="font-bold text-white">'+t.user+'</p><p class="text-xs text-slate-400">'+t.pc_name+'</p></td><td class="p-4 text-white">'+t.message+'</td><td class="p-4"><span class="bg-slate-900 text-xs px-3 py-1 rounded text-primary border border-primary/30">'+(t.telemetry || 'Sin datos')+'</span></td></tr>';
        });
        document.getElementById('tickets-body').innerHTML = html;
    });
}

function loadTimeline() {
    fetch('/api/actions').then(r=>r.json()).then(data => {
        let html = '';
        data.forEach(a => {
            let statusColor = a.status === 'executed' ? 'bg-primary' : (a.status === 'rolled_back' ? 'bg-yellow-500' : 'bg-red-500');
            html += '<div class="flex items-start gap-4 p-4 bg-gray-800 rounded-xl border border-slate-700">';
            html += '<div class="w-3 h-3 mt-2 rounded-full shadow-[0_0_10px_currentColor] '+statusColor+'"></div>';
            html += '<div class="flex-1"><p class="text-sm text-slate-400">'+new Date(a.created_at).toLocaleString()+' | PC: '+a.agent_id+'</p>';
            html += '<p class="text-lg font-bold text-white mt-1">'+a.command+'</p><p class="text-sm text-slate-300 mt-1">'+(a.result || a.status)+'</p></div>';
            if(a.rollback_command && a.status === 'executed') {
                html += '<button onclick="doRollback(\''+a.id+'\')" class="px-4 py-2 bg-slate-700 hover:bg-yellow-600 text-white rounded-lg transition-colors text-sm font-medium">↩ Deshacer</button>';
            }
            html += '</div>';
        });
        document.getElementById('timeline-body').innerHTML = html;
    });
}

function doRollback(id) {
    if(confirm("¿Estás seguro de deshacer esta acción en la PC del usuario?")) {
        fetch('/api/rollback?id='+id).then(() => loadTimeline());
    }
}
</script>
</body>
</html>`

func webUIRoot(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, htmlUI) }

func main() {
	initDB()
	loadConfig()
	agentAPIKey = os.Getenv("MICLAW_AGENT_KEY")
	if agentAPIKey == "" { agentAPIKey = "ClaveSuperSecreta" }

	http.HandleFunc("/", webUIRoot)
	http.HandleFunc("/api/eliza/ticket", apiElizaTicketHandler)
	http.HandleFunc("/api/eliza/tickets_list", apiTicketsListHandler)
	http.HandleFunc("/api/actions", apiActionsHandler)
	http.HandleFunc("/api/rollback", apiRollbackHandler)

	log.Println("🌐 Miclaw Web Dashboard activo en puerto 3000")
	http.ListenAndServe(":3000", nil)
}
