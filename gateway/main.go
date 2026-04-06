package main

import (
	"bytes"
	"context"
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
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ---------- ESTRUCTURAS ----------
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
	Status          string     `json:"status"` // pending, approved, executed, rejected, rolled_back
	PreTelemetry    string     `json:"pre_telemetry"`
	PostTelemetry   string     `json:"post_telemetry"`
	CreatedAt       time.Time  `json:"created_at"`
	ApprovedAt      *time.Time `json:"approved_at"`
	ExecutedAt      *time.Time `json:"executed_at"`
	Result          string     `json:"result"`
	Requester       string     `json:"requester"`
}

type PlannerTask struct {
	Target   string                 `json:"target"`   // ID del agente o "none"
	Command  string                 `json:"command"`  // Comando a ejecutar
	Params   map[string]interface{} `json:"params"`   // Parámetros adicionales
	Goal     string                 `json:"goal"`     // Propósito de la tarea
	Rollback string                 `json:"rollback"` // Comando de deshacer
}

type PlannerResponse struct {
	Tasks          []PlannerTask `json:"tasks"`
	DirectResponse string        `json:"direct_response"` // Si no requiere acciones
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

type DashboardStats struct {
	Equipos  int `json:"equipos"`
	Tickets  int `json:"tickets"`
	Acciones int `json:"acciones"`
}

type RegisterAgentRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

type ChatRequest struct {
	Message   string `json:"message"`
	AgentID   string `json:"agent_id"`   // Opcional, si se quiere ejecutar en un agente específico
	Requester string `json:"requester"`  // Usuario o PC
}

type ChatResponse struct {
	FinalAnswer string `json:"final_answer"`
	TicketID    int    `json:"ticket_id,omitempty"`
}

var (
	db          *sql.DB
	config      SystemConfig
	configMutex sync.RWMutex
	agentAPIKey string
	botAPI      *tgbotapi.BotAPI
)

// ---------- BASE DE DATOS ----------
func initDB() error {
	var err error
	db, err = sql.Open("sqlite3", "./miclaw.db")
	if err != nil {
		return err
	}
	db.Exec("PRAGMA journal_mode=WAL")

	// Tablas
	db.Exec(`CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS agents (
		id TEXT PRIMARY KEY, name TEXT, type TEXT, ip TEXT, port INTEGER, last_seen DATETIME, enabled BOOLEAN
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS actions (
		id TEXT PRIMARY KEY, agent_id TEXT, command TEXT, parameters TEXT, rollback_command TEXT,
		status TEXT, pre_telemetry TEXT, post_telemetry TEXT, created_at DATETIME,
		approved_at DATETIME, executed_at DATETIME, result TEXT, requester TEXT
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS permissions (
		command TEXT, agent_id TEXT, mode TEXT, PRIMARY KEY (command, agent_id)
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS eliza_tickets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, pc_name TEXT, user TEXT, timestamp DATETIME,
		message TEXT, category TEXT, telemetry TEXT
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS eliza_rules (
		version TEXT PRIMARY KEY, rules_json TEXT, updated_at DATETIME
	)`)

	// Cargar reglas iniciales desde archivo si existe
	loadRulesFromFile()
	return nil
}

func loadRulesFromFile() {
	data, err := os.ReadFile("eliza_rules.json")
	if err != nil {
		log.Println("No se pudo leer eliza_rules.json, usando reglas por defecto")
		return
	}
	var rules map[string]interface{}
	if err := json.Unmarshal(data, &rules); err != nil {
		log.Println("Error parseando eliza_rules.json:", err)
		return
	}
	version, _ := rules["version"].(string)
	if version == "" {
		version = "1.0"
	}
	rulesJSON, _ := json.Marshal(rules)
	db.Exec(`INSERT OR REPLACE INTO eliza_rules (version, rules_json, updated_at) VALUES (?, ?, ?)`,
		version, string(rulesJSON), time.Now())
	log.Println("Reglas ELIZA cargadas desde archivo, versión:", version)
}

func loadConfig() {
	configMutex.Lock()
	defer configMutex.Unlock()
	db.QueryRow("SELECT value FROM config WHERE key='system_prompt'").Scan(&config.SystemPrompt)
	db.QueryRow("SELECT value FROM config WHERE key='model_name'").Scan(&config.ModelName)
	db.QueryRow("SELECT value FROM config WHERE key='safety_model'").Scan(&config.SafetyModel)
	db.QueryRow("SELECT value FROM config WHERE key='ollama_url'").Scan(&config.OllamaURL)
	if config.ModelName == "" {
		config.ModelName = os.Getenv("OLLAMA_MODEL")
		if config.ModelName == "" {
			config.ModelName = "phi4-mini:3.8b"
		}
	}
	if config.OllamaURL == "" {
		config.OllamaURL = os.Getenv("OLLAMA_URL")
		if config.OllamaURL == "" {
			config.OllamaURL = "http://localhost:11434/api/chat"
		}
	}
	if config.SafetyModel == "" {
		config.SafetyModel = os.Getenv("SAFETY_MODEL")
		if config.SafetyModel == "" {
			config.SafetyModel = config.ModelName
		}
	}
}

func saveConfig() {
	configMutex.RLock()
	defer configMutex.RUnlock()
	db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, "system_prompt", config.SystemPrompt)
	db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, "model_name", config.ModelName)
	db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, "safety_model", config.SafetyModel)
	db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, "ollama_url", config.OllamaURL)
}

// ---------- AGENTES ----------
func registerAgent(reg RegisterAgentRequest) (Agent, error) {
	id := reg.Name + "-" + reg.IP
	agent := Agent{
		ID:       id,
		Name:     reg.Name,
		Type:     reg.Type,
		IP:       reg.IP,
		Port:     reg.Port,
		LastSeen: time.Now(),
		Enabled:  true,
	}
	_, err := db.Exec(`INSERT OR REPLACE INTO agents (id, name, type, ip, port, last_seen, enabled) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		agent.ID, agent.Name, agent.Type, agent.IP, agent.Port, agent.LastSeen.Format(time.RFC3339), agent.Enabled)
	return agent, err
}

func getAgent(id string) (Agent, bool) {
	var a Agent
	var lastSeenStr string
	err := db.QueryRow(`SELECT id, name, type, ip, port, last_seen, enabled FROM agents WHERE id=?`, id).
		Scan(&a.ID, &a.Name, &a.Type, &a.IP, &a.Port, &lastSeenStr, &a.Enabled)
	if err != nil {
		return a, false
	}
	a.LastSeen, _ = time.Parse(time.RFC3339, lastSeenStr)
	return a, true
}

func getAllAgents() []Agent {
	rows, err := db.Query(`SELECT id, name, type, ip, port, last_seen, enabled FROM agents`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var agents []Agent
	for rows.Next() {
		var a Agent
		var lastSeenStr string
		rows.Scan(&a.ID, &a.Name, &a.Type, &a.IP, &a.Port, &lastSeenStr, &a.Enabled)
		a.LastSeen, _ = time.Parse(time.RFC3339, lastSeenStr)
		agents = append(agents, a)
	}
	return agents
}

func executeOnAgent(agent Agent, cmd string, params map[string]interface{}) (string, error) {
	payload := map[string]interface{}{"command": cmd, "parameters": params}
	payloadBytes, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 30 * time.Second}
	url := fmt.Sprintf("http://%s:%d/execute", agent.IP, agent.Port)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", agentAPIKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

// ---------- LLAMADA A OLLAMA ----------
func callOllama(model, prompt string) (string, error) {
	configMutex.RLock()
	url := config.OllamaURL
	configMutex.RUnlock()

	reqBody := OllamaChatRequest{
		Model: model,
		Messages: []OllamaMessage{
			{Role: "user", Content: prompt},
		},
		Stream: false,
		Format: "json",
	}
	jsonReq, _ := json.Marshal(reqBody)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonReq))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var oResp OllamaChatResponse
	if err := json.Unmarshal(body, &oResp); err != nil {
		return "", err
	}
	return oResp.Message.Content, nil
}

// ---------- TRIBUNAL DE SEGURIDAD ----------
func inferPermissionMode(agent Agent, command string) (string, string) {
	var savedMode string
	db.QueryRow(`SELECT mode FROM permissions WHERE command = ? AND (agent_id = ? OR agent_id = 'all')`,
		command, agent.ID).Scan(&savedMode)
	if savedMode != "" {
		return savedMode, "Regla guardada en BD."
	}

	configMutex.RLock()
	model := config.SafetyModel
	configMutex.RUnlock()

	promptSec := fmt.Sprintf(`Eres un auditor de seguridad IT. Clasifica el siguiente comando que se ejecutará en un equipo %s:
Comando: "%s"
Responde ÚNICAMENTE con un JSON: {"mode": "bypass"} si solo lee información o telemetría.
{"mode": "strict"} si formatea, borra archivos del sistema o hace cambios peligrosos.
{"mode": "default"} para cualquier otra acción (reiniciar servicios, cambiar configuraciones no críticas).`,
		agent.Type, command)

	res, err := callOllama(model, promptSec)
	if err != nil {
		log.Println("Error llamando tribunal:", err)
		return "default", "Error en inferencia, se requiere aprobación."
	}
	if strings.Contains(res, "bypass") {
		db.Exec(`INSERT OR REPLACE INTO permissions (command, agent_id, mode) VALUES (?, 'all', 'bypass')`, command)
		return "bypass", "Tribunal: comando inocuo (bypass)."
	}
	if strings.Contains(res, "strict") {
		return "strict", "Tribunal: comando destructivo (bloqueado)."
	}
	db.Exec(`INSERT OR REPLACE INTO permissions (command, agent_id, mode) VALUES (?, 'all', 'default')`, command)
	return "default", "Tribunal: requiere aprobación humana."
}

// ---------- PLANIFICADOR ----------
func planificador(problem string, agentID string) (*PlannerResponse, error) {
	configMutex.RLock()
	model := config.ModelName
	systemPrompt := config.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "Eres un planificador de tareas de soporte IT. Divide el problema del usuario en pasos concretos (comandos PowerShell o acciones). Devuelve JSON."
	}
	configMutex.RUnlock()

	prompt := fmt.Sprintf(`%s

Problema del usuario: "%s"
Agente disponible: %s

Debes responder con un JSON que tenga una de estas dos formas:

1. Si no se requiere ninguna acción técnica (solo información o chat):
{
  "tasks": [],
  "direct_response": "Tu respuesta amigable al usuario"
}

2. Si se requieren acciones:
{
  "tasks": [
    {
      "target": "id_del_agente",
      "command": "comando a ejecutar",
      "params": {},
      "goal": "propósito de esta tarea",
      "rollback": "comando para deshacer (si aplica)"
    }
  ],
  "direct_response": ""
}

Solo genera comandos PowerShell válidos para Windows. No inventes comandos inexistentes.`, systemPrompt, problem, agentID)

	resp, err := callOllama(model, prompt)
	if err != nil {
		return nil, err
	}
	var plan PlannerResponse
	if err := json.Unmarshal([]byte(resp), &plan); err != nil {
		// Si falla el parseo, asumimos respuesta directa
		return &PlannerResponse{DirectResponse: resp}, nil
	}
	return &plan, nil
}

// ---------- EJECUTOR CON TRIBUNAL ----------
func ejecutarTarea(actionID string, task PlannerTask, agent Agent, requester string) (string, error) {
	// Verificar permiso
	mode, motivo := inferPermissionMode(agent, task.Command)
	if mode == "strict" {
		db.Exec(`UPDATE actions SET status='rejected', result=? WHERE id=?`, motivo, actionID)
		return "", fmt.Errorf("comando bloqueado: %s", motivo)
	}
	if mode == "default" {
		// Ya está en estado pending, espera aprobación manual
		return "", fmt.Errorf("comando requiere aprobación: %s", motivo)
	}
	// bypass: ejecutar directamente
	output, err := executeOnAgent(agent, task.Command, task.Params)
	status := "executed"
	result := output
	if err != nil {
		status = "failed"
		result = err.Error()
	}
	now := time.Now()
	db.Exec(`UPDATE actions SET status=?, result=?, executed_at=? WHERE id=?`, status, result, now.Format(time.RFC3339), actionID)
	return output, err
}

// ---------- SINTETIZADOR ----------
func sintetizador(originalProblem string, resultados []string) (string, error) {
	configMutex.RLock()
	model := config.ModelName
	configMutex.RUnlock()

	prompt := fmt.Sprintf(`Eres un asistente de soporte IT. El usuario reportó: "%s"
Resultados de las acciones ejecutadas: %v
Genera una respuesta amigable, clara y útil para el usuario. No menciones tecnicismos innecesarios.`, originalProblem, resultados)

	resp, err := callOllama(model, prompt)
	if err != nil {
		return "Las acciones se ejecutaron correctamente. Si persiste el problema, contacta a soporte.", nil
	}
	return resp, nil
}

// ---------- HANDLERS API ----------

// Health check
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// Registrar agente
func registerAgentHandler(w http.ResponseWriter, r *http.Request) {
	var reg RegisterAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	agent, err := registerAgent(reg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agent)
}

// Listar agentes
func agentsListHandler(w http.ResponseWriter, r *http.Request) {
	agents := getAllAgents()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agents)
}

// Ejecutar comando en agente (endpoint interno para dashboard)
func executeCommandHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID   string                 `json:"agent_id"`
		Command   string                 `json:"command"`
		Params    map[string]interface{} `json:"params"`
		Requester string                 `json:"requester"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	agent, ok := getAgent(req.AgentID)
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	actionID := fmt.Sprintf("%s-%d", req.AgentID, time.Now().UnixNano())
	mode, motivo := inferPermissionMode(agent, req.Command)
	if mode == "strict" {
		http.Error(w, fmt.Sprintf("Comando bloqueado: %s", motivo), http.StatusForbidden)
		return
	}
	if mode == "default" {
		// Guardar acción pendiente
		now := time.Now()
		_, err := db.Exec(`INSERT INTO actions (id, agent_id, command, parameters, rollback_command, status, created_at, requester)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			actionID, req.AgentID, req.Command, "{}", "", "pending", now.Format(time.RFC3339), req.Requester)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"status": "pending", "id": actionID, "message": motivo})
		return
	}
	// bypass
	output, err := executeOnAgent(agent, req.Command, req.Params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write([]byte(output))
}

// Aprobar acción pendiente
func approveActionHandler(w http.ResponseWriter, r *http.Request) {
	actionID := r.URL.Query().Get("id")
	if actionID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	var action Action
	var agentID, command, paramsStr, rollback, status string
	var createdAtStr string
	err := db.QueryRow(`SELECT agent_id, command, parameters, rollback_command, status, created_at, requester FROM actions WHERE id=?`, actionID).
		Scan(&agentID, &command, &paramsStr, &rollback, &status, &createdAtStr, &action.Requester)
	if err != nil {
		http.Error(w, "Action not found", http.StatusNotFound)
		return
	}
	if status != "pending" {
		http.Error(w, "Action already processed", http.StatusBadRequest)
		return
	}
	agent, ok := getAgent(agentID)
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	var params map[string]interface{}
	json.Unmarshal([]byte(paramsStr), &params)
	output, err := executeOnAgent(agent, command, params)
	now := time.Now()
	newStatus := "executed"
	result := output
	if err != nil {
		newStatus = "failed"
		result = err.Error()
	}
	db.Exec(`UPDATE actions SET status=?, result=?, approved_at=?, executed_at=? WHERE id=?`,
		newStatus, result, now.Format(time.RFC3339), now.Format(time.RFC3339), actionID)
	w.Write([]byte(`{"status":"ok"}`))
}

// Rollback
func rollbackHandler(w http.ResponseWriter, r *http.Request) {
	actionID := r.URL.Query().Get("id")
	var agentID, rollbackCmd string
	db.QueryRow(`SELECT agent_id, rollback_command FROM actions WHERE id=?`, actionID).Scan(&agentID, &rollbackCmd)
	if rollbackCmd == "" || agentID == "" {
		http.Error(w, "No rollback command", http.StatusBadRequest)
		return
	}
	agent, ok := getAgent(agentID)
	if !ok {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	output, err := executeOnAgent(agent, rollbackCmd, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	db.Exec(`UPDATE actions SET status='rolled_back', result=? WHERE id=?`, output, actionID)
	w.Write([]byte(`{"status":"rolled_back"}`))
}

// Chat principal (planificador -> ejecutor -> sintetizador)
func chatHandler(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if req.Requester == "" {
		req.Requester = "anonymous"
	}
	// Guardar ticket inicial
	ticket := ElizaTicket{
		PCName:    req.AgentID,
		User:      req.Requester,
		Timestamp: time.Now().Format(time.RFC3339),
		Message:   req.Message,
		Category:  "chat",
		Telemetry: "",
	}
	res, err := db.Exec(`INSERT INTO eliza_tickets (pc_name, user, timestamp, message, category, telemetry) VALUES (?, ?, ?, ?, ?, ?)`,
		ticket.PCName, ticket.User, ticket.Timestamp, ticket.Message, ticket.Category, ticket.Telemetry)
	if err != nil {
		log.Println("Error guardando ticket:", err)
	}
	ticketID, _ := res.LastInsertId()

	// Planificar
	plan, err := planificador(req.Message, req.AgentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if plan.DirectResponse != "" {
		json.NewEncoder(w).Encode(ChatResponse{FinalAnswer: plan.DirectResponse, TicketID: int(ticketID)})
		return
	}
	// Ejecutar tareas secuencialmente
	var resultados []string
	for _, task := range plan.Tasks {
		targetAgent := task.Target
		if targetAgent == "" {
			targetAgent = req.AgentID
		}
		agent, ok := getAgent(targetAgent)
		if !ok {
			resultados = append(resultados, fmt.Sprintf("Error: agente %s no encontrado", targetAgent))
			continue
		}
		actionID := fmt.Sprintf("%s-%d", targetAgent, time.Now().UnixNano())
		now := time.Now()
		paramsJSON, _ := json.Marshal(task.Params)
		_, err := db.Exec(`INSERT INTO actions (id, agent_id, command, parameters, rollback_command, status, created_at, requester)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			actionID, targetAgent, task.Command, string(paramsJSON), task.Rollback, "pending", now.Format(time.RFC3339), req.Requester)
		if err != nil {
			resultados = append(resultados, fmt.Sprintf("Error al crear acción: %v", err))
			continue
		}
		// Verificar permiso y ejecutar o dejar pendiente
		mode, _ := inferPermissionMode(agent, task.Command)
		if mode == "strict" {
			db.Exec(`UPDATE actions SET status='rejected', result='Bloqueado por tribunal' WHERE id=?`, actionID)
			resultados = append(resultados, fmt.Sprintf("Comando bloqueado: %s", task.Command))
		} else if mode == "default" {
			resultados = append(resultados, fmt.Sprintf("Comando requiere aprobación: %s (ID: %s)", task.Command, actionID))
		} else {
			output, err := executeOnAgent(agent, task.Command, task.Params)
			execStatus := "executed"
			resText := output
			if err != nil {
				execStatus = "failed"
				resText = err.Error()
			}
			db.Exec(`UPDATE actions SET status=?, result=?, executed_at=? WHERE id=?`, execStatus, resText, time.Now().Format(time.RFC3339), actionID)
			resultados = append(resultados, fmt.Sprintf("Comando %s ejecutado: %s", task.Command, resText))
		}
	}
	// Sintetizar
	finalAnswer, err := sintetizador(req.Message, resultados)
	if err != nil {
		finalAnswer = "Se ejecutaron acciones, pero no se pudo generar un resumen. Revisa el dashboard."
	}
	json.NewEncoder(w).Encode(ChatResponse{FinalAnswer: finalAnswer, TicketID: int(ticketID)})
}

// Obtener reglas ELIZA para agentes
func getElizaRulesHandler(w http.ResponseWriter, r *http.Request) {
	var version, rulesJSON string
	err := db.QueryRow(`SELECT version, rules_json FROM eliza_rules ORDER BY updated_at DESC LIMIT 1`).Scan(&version, &rulesJSON)
	if err != nil {
		http.Error(w, "No rules", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(rulesJSON))
}

// Actualizar reglas ELIZA (desde dashboard)
func updateElizaRulesHandler(w http.ResponseWriter, r *http.Request) {
	var rules map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	version, _ := rules["version"].(string)
	if version == "" {
		version = time.Now().Format("20060102150405")
		rules["version"] = version
	}
	rulesJSON, _ := json.Marshal(rules)
	_, err := db.Exec(`INSERT OR REPLACE INTO eliza_rules (version, rules_json, updated_at) VALUES (?, ?, ?)`,
		version, string(rulesJSON), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// También guardar en archivo para persistencia
	os.WriteFile("eliza_rules.json", rulesJSON, 0644)
	w.WriteHeader(http.StatusOK)
}

// Estadísticas dashboard
func statsHandler(w http.ResponseWriter, r *http.Request) {
	var stats DashboardStats
	db.QueryRow(`SELECT COUNT(DISTINCT pc_name) FROM eliza_tickets`).Scan(&stats.Equipos)
	db.QueryRow(`SELECT COUNT(*) FROM eliza_tickets`).Scan(&stats.Tickets)
	db.QueryRow(`SELECT COUNT(*) FROM actions WHERE date(created_at) = date('now', 'localtime')`).Scan(&stats.Acciones)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// Tickets list
func ticketsListHandler(w http.ResponseWriter, r *http.Request) {
	rows, _ := db.Query(`SELECT id, pc_name, user, timestamp, message, category, telemetry FROM eliza_tickets ORDER BY timestamp DESC LIMIT 50`)
	defer rows.Close()
	var tickets []ElizaTicket
	for rows.Next() {
		var t ElizaTicket
		rows.Scan(&t.ID, &t.PCName, &t.User, &t.Timestamp, &t.Message, &t.Category, &t.Telemetry)
		tickets = append(tickets, t)
	}
	json.NewEncoder(w).Encode(tickets)
}

// Actions list
func actionsListHandler(w http.ResponseWriter, r *http.Request) {
	rows, _ := db.Query(`SELECT id, agent_id, command, status, result, created_at, rollback_command FROM actions ORDER BY created_at DESC LIMIT 50`)
	defer rows.Close()
	var actions []Action
	for rows.Next() {
		var a Action
		var caStr sql.NullString
		rows.Scan(&a.ID, &a.AgentID, &a.Command, &a.Status, &a.Result, &caStr, &a.RollbackCommand)
		if caStr.Valid {
			a.CreatedAt, _ = time.Parse(time.RFC3339, caStr.String)
		}
		actions = append(actions, a)
	}
	json.NewEncoder(w).Encode(actions)
}

// Config get
func configGetHandler(w http.ResponseWriter, r *http.Request) {
	configMutex.RLock()
	defer configMutex.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config)
}

// Config update
func configUpdateHandler(w http.ResponseWriter, r *http.Request) {
	var newConfig SystemConfig
	if err := json.NewDecoder(r.Body).Decode(&newConfig); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	configMutex.Lock()
	config = newConfig
	configMutex.Unlock()
	saveConfig()
	w.WriteHeader(http.StatusOK)
}

// ---------- UI (Tailwind) ----------
const htmlUI = `<!DOCTYPE html>
<html lang="es" class="dark">
<head>
<meta charset="UTF-8"><title>Miclaw OS Command Center</title>
<script src="https://cdn.tailwindcss.com"></script>
<script>
  tailwind.config = { darkMode: 'class', theme: { extend: { colors: { gray: { 800: '#1e293b', 900: '#0f172a' }, primary: '#10b981' } } } }
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
        <button onclick="nav('agents')" id="btn-agents" class="w-full text-left px-4 py-3 rounded-lg text-slate-400 hover:bg-slate-700 hover:text-white transition-all">🖥️ Agentes</button>
    </nav>
</aside>

<main class="flex-1 p-8 overflow-y-auto">
    <div id="view-dash" class="view block">
        <h2 class="text-3xl font-bold mb-6 text-white">Vista General</h2>
        <div class="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
            <div class="bg-gray-800 p-6 rounded-xl border border-slate-700 shadow-lg"><h3 class="text-slate-400 text-sm">Equipos Detectados</h3><p id="stat-equipos" class="text-4xl font-bold text-primary mt-2">0</p></div>
            <div class="bg-gray-800 p-6 rounded-xl border border-slate-700 shadow-lg"><h3 class="text-slate-400 text-sm">Tickets Recibidos</h3><p id="stat-tickets" class="text-4xl font-bold text-yellow-500 mt-2">0</p></div>
            <div class="bg-gray-800 p-6 rounded-xl border border-slate-700 shadow-lg"><h3 class="text-slate-400 text-sm">Acciones IA Hoy</h3><p id="stat-acciones" class="text-4xl font-bold text-blue-400 mt-2">0</p></div>
        </div>
    </div>

    <div id="view-tickets" class="view hidden">
        <h2 class="text-3xl font-bold mb-6 text-white">Bandeja de Entrada (Usuarios)</h2>
        <div class="bg-gray-800 rounded-xl border border-slate-700 overflow-hidden shadow-lg">
            <table class="w-full text-left"><thead class="bg-slate-700 text-slate-300"><tr><th class="p-4">Fecha</th><th class="p-4">Usuario / PC</th><th class="p-4">Problema</th><th class="p-4">Telemetría</th></tr></thead>
            <tbody id="tickets-body" class="divide-y divide-slate-700"></tbody></table>
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
            <input type="text" id="conf-model" class="w-full bg-slate-900 border border-slate-600 rounded p-3 text-white mb-4">
            <label class="block text-slate-400 mb-2">Modelo Seguridad</label>
            <input type="text" id="conf-safety" class="w-full bg-slate-900 border border-slate-600 rounded p-3 text-white mb-6">
            <button id="save-config" class="bg-primary hover:bg-emerald-400 text-slate-900 font-bold py-3 px-6 rounded-lg shadow-lg">Guardar Cambios</button>
        </div>
    </div>

    <div id="view-agents" class="view hidden">
        <h2 class="text-3xl font-bold mb-6 text-white">Agentes Registrados</h2>
        <div class="bg-gray-800 rounded-xl border border-slate-700 overflow-hidden shadow-lg">
            <table class="w-full text-left"><thead class="bg-slate-700 text-slate-300"><tr><th class="p-4">ID</th><th class="p-4">Nombre</th><th class="p-4">Tipo</th><th class="p-4">IP:Puerto</th><th class="p-4">Última vez</th></tr></thead>
            <tbody id="agents-body" class="divide-y divide-slate-700"></tbody></table>
        </div>
    </div>
</main>

<script>
function nav(target) {
    document.querySelectorAll('.view').forEach(v => v.classList.add('hidden'));
    document.getElementById('view-'+target).classList.remove('hidden');
    document.querySelectorAll('aside button').forEach(b => {
        b.classList.remove('bg-slate-700','text-white');
        b.classList.add('text-slate-400');
    });
    document.getElementById('btn-'+target).classList.add('bg-slate-700','text-white');
    if(target==='dash') loadStats();
    if(target==='tickets') loadTickets();
    if(target==='timeline') loadTimeline();
    if(target==='agents') loadAgents();
    if(target==='config') loadConfig();
}

async function loadStats() {
    let r=await fetch('/api/stats'); let d=await r.json();
    document.getElementById('stat-equipos').innerText=d.equipos;
    document.getElementById('stat-tickets').innerText=d.tickets;
    document.getElementById('stat-acciones').innerText=d.acciones;
}
async function loadTickets() {
    let r=await fetch('/api/eliza/tickets_list'); let data=await r.json();
    let html='';
    for(let t of data) {
        html+='<tr><td class="p-4">'+new Date(t.timestamp).toLocaleString()+'</td><td class="p-4"><b>'+t.user+'</b><br><span class="text-xs">'+t.pc_name+'</span></td><td class="p-4">'+t.message+'</td><td class="p-4"><span class="bg-slate-900 px-2 py-1 rounded text-xs">'+(t.telemetry||'-')+'</span></td></tr>';
    }
    document.getElementById('tickets-body').innerHTML=html||'<tr><td colspan="4" class="p-4 text-center">No hay tickets</td></tr>';
}
async function loadTimeline() {
    let r=await fetch('/api/actions'); let data=await r.json();
    let html='';
    for(let a of data) {
        let statusColor=a.status==='executed'?'bg-primary':(a.status==='rolled_back'?'bg-yellow-500':'bg-red-500');
        html+='<div class="flex items-start gap-4 p-4 bg-gray-800 rounded-xl border border-slate-700"><div class="w-3 h-3 mt-2 rounded-full '+statusColor+'"></div><div class="flex-1"><p class="text-sm text-slate-400">'+new Date(a.created_at).toLocaleString()+' | PC: '+a.agent_id+'</p><p class="text-lg font-bold text-white mt-1">'+a.command+'</p><p class="text-sm text-slate-300">'+(a.result||a.status)+'</p></div>';
        if(a.rollback_command && a.status==='executed') html+='<button onclick="doRollback(\''+a.id+'\')" class="px-4 py-2 bg-slate-700 hover:bg-yellow-600 text-white rounded-lg">↩ Deshacer</button>';
        if(a.status==='pending') html+='<button onclick="approveAction(\''+a.id+'\')" class="px-4 py-2 bg-green-700 hover:bg-green-600 text-white rounded-lg">✅ Aprobar</button>';
        html+='</div>';
    }
    document.getElementById('timeline-body').innerHTML=html||'<p class="text-slate-500">No hay acciones.</p>';
}
async function loadAgents() {
    let r=await fetch('/api/agents'); let data=await r.json();
    let html='';
    for(let a of data) {
        html+='<tr><td class="p-4">'+a.id+'</td><td class="p-4">'+a.name+'</td><td class="p-4">'+a.type+'</td><td class="p-4">'+a.ip+':'+a.port+'</td><td class="p-4">'+new Date(a.last_seen).toLocaleString()+'</td></tr>';
    }
    document.getElementById('agents-body').innerHTML=html||'<tr><td colspan="5" class="p-4 text-center">No hay agentes</td></tr>';
}
async function loadConfig() {
    let r=await fetch('/api/config'); let c=await r.json();
    document.getElementById('conf-url').value=c.ollama_url||'';
    document.getElementById('conf-model').value=c.model_name||'';
    document.getElementById('conf-safety').value=c.safety_model||'';
}
document.getElementById('save-config')?.addEventListener('click',async()=>{
    let body={ollama_url:document.getElementById('conf-url').value,model_name:document.getElementById('conf-model').value,safety_model:document.getElementById('conf-safety').value};
    await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    alert('Configuración guardada');
});
async function doRollback(id){ if(confirm('¿Deshacer esta acción?')){ await fetch('/api/rollback?id='+id); loadTimeline(); } }
async function approveAction(id){ if(confirm('¿Aprobar ejecución?')){ await fetch('/api/actions/approve?id='+id); loadTimeline(); } }
loadStats();
</script>
</body>
</html>`

func webUIRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, htmlUI)
}

// ---------- MAIN ----------
func main() {
	if err := initDB(); err != nil {
		log.Fatal("DB init error:", err)
	}
	loadConfig()
	agentAPIKey = os.Getenv("MICLAW_AGENT_KEY")
	if agentAPIKey == "" {
		agentAPIKey = "ClaveSuperSecretaAFE2026"
	}

	// Telegram bot opcional
	if token := os.Getenv("TELEGRAM_TOKEN"); token != "" {
		bot, err := tgbotapi.NewBotAPI(token)
		if err == nil {
			botAPI = bot
			go startTelegramBot()
		}
	}

	http.HandleFunc("/", webUIRoot)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/api/agents/register", registerAgentHandler)
	http.HandleFunc("/api/agents", agentsListHandler)
	http.HandleFunc("/api/agents/execute", executeCommandHandler)
	http.HandleFunc("/api/actions/approve", approveActionHandler)
	http.HandleFunc("/api/rollback", rollbackHandler)
	http.HandleFunc("/api/chat", chatHandler)
	http.HandleFunc("/api/eliza/rules", getElizaRulesHandler)
	http.HandleFunc("/api/eliza/rules/update", updateElizaRulesHandler)
	http.HandleFunc("/api/stats", statsHandler)
	http.HandleFunc("/api/eliza/ticket", func(w http.ResponseWriter, r *http.Request) {
		var t ElizaTicket
		json.NewDecoder(r.Body).Decode(&t)
		db.Exec(`INSERT INTO eliza_tickets (pc_name, user, timestamp, message, category, telemetry) VALUES (?,?,?,?,?,?)`,
			t.PCName, t.User, t.Timestamp, t.Message, t.Category, t.Telemetry)
		w.WriteHeader(http.StatusOK)
	})
	http.HandleFunc("/api/eliza/tickets_list", ticketsListHandler)
	http.HandleFunc("/api/actions", actionsListHandler)
	http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configGetHandler(w, r)
		} else if r.Method == http.MethodPost {
			configUpdateHandler(w, r)
		}
	})

	log.Println("🌐 Miclaw Gateway iniciado en puerto 3000")
	log.Fatal(http.ListenAndServe(":3000", nil))
}

func startTelegramBot() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := botAPI.GetUpdatesChan(u)
	for update := range updates {
		if update.Message != nil && update.Message.Text != "" {
			go func(chatID int64, text string) {
				resp, _ := chatHandlerInternal(text, "")
				msg := tgbotapi.NewMessage(chatID, resp)
				botAPI.Send(msg)
			}(update.Message.Chat.ID, update.Message.Text)
		}
	}
}

func chatHandlerInternal(message, agentID string) (string, error) {
	plan, err := planificador(message, agentID)
	if err != nil {
		return "Error en el planificador", err
	}
	if plan.DirectResponse != "" {
		return plan.DirectResponse, nil
	}
	var resultados []string
	for _, task := range plan.Tasks {
		target := task.Target
		if target == "" {
			target = agentID
		}
		agent, ok := getAgent(target)
		if !ok {
			resultados = append(resultados, fmt.Sprintf("Agente %s no disponible", target))
			continue
		}
		mode, _ := inferPermissionMode(agent, task.Command)
		if mode == "strict" {
			resultados = append(resultados, fmt.Sprintf("Comando bloqueado: %s", task.Command))
		} else if mode == "default" {
			resultados = append(resultados, fmt.Sprintf("Comando requiere aprobación manual: %s", task.Command))
		} else {
			out, err := executeOnAgent(agent, task.Command, task.Params)
			if err != nil {
				resultados = append(resultados, fmt.Sprintf("Error: %v", err))
			} else {
				resultados = append(resultados, out)
			}
		}
	}
	return sintetizador(message, resultados)
}
