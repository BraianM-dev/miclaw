package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"miclaw-gateway/ai"
	"miclaw-gateway/db"
	"miclaw-gateway/network"
	"miclaw-gateway/queue"
	"miclaw-gateway/rules"
)

// ── Deps & Server ──────────────────────────────────────────────────────────

// Deps holds all dependencies injected into the API layer.
type Deps struct {
	DB     *db.DB
	Hub    *Hub
	Rules  *rules.Engine
	AI     *ai.Client
	Queue  *queue.Queue
	APIKey string
}

// Server is the HTTP server with all dependencies.
type Server struct {
	deps Deps
}

// NewServer creates a new API server.
func NewServer(deps Deps) *Server {
	return &Server{deps: deps}
}

// Routes builds the HTTP mux.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /health", s.health)

	// Agents
	mux.HandleFunc("POST /agents/register", s.auth(s.registerAgent))
	mux.HandleFunc("POST /agents/heartbeat", s.auth(s.heartbeat))
	mux.HandleFunc("GET /agents", s.auth(s.listAgents))
	mux.HandleFunc("GET /agents/{id}", s.auth(s.getAgent))
	mux.HandleFunc("DELETE /agents/{id}", s.auth(s.deleteAgent))

	// Commands
	mux.HandleFunc("POST /commands", s.auth(s.createCommand))
	mux.HandleFunc("GET /commands", s.auth(s.listCommands))
	mux.HandleFunc("GET /commands/{id}", s.auth(s.getCommand))

	// Alerts
	mux.HandleFunc("POST /alerts", s.auth(s.createAlert))
	mux.HandleFunc("GET /alerts", s.auth(s.listAlerts))
	mux.HandleFunc("PATCH /alerts/{id}", s.auth(s.updateAlert))

	// Tickets
	mux.HandleFunc("POST /tickets", s.auth(s.createTicket))
	mux.HandleFunc("GET /tickets", s.auth(s.listTickets))
	mux.HandleFunc("GET /tickets/{id}", s.auth(s.getTicket))
	mux.HandleFunc("PATCH /tickets/{id}", s.auth(s.updateTicket))
	mux.HandleFunc("POST /tickets/{id}/messages", s.auth(s.addMessage))
	mux.HandleFunc("GET /tickets/{id}/messages", s.auth(s.getMessages))

	// AI
	mux.HandleFunc("POST /ai/query", s.auth(s.aiQuery))

	// Dashboard
	mux.HandleFunc("GET /dashboard/stats", s.auth(s.dashboardStats))

	// Network
	mux.HandleFunc("GET /network/locations", s.auth(s.networkLocations))

	// Settings
	mux.HandleFunc("GET /settings", s.auth(s.getSettings))
	mux.HandleFunc("PUT /settings", s.auth(s.saveSettings))

	// Rules (read-only inspection)
	mux.HandleFunc("GET /rules", s.auth(s.listRules))

	// Knowledge
	mux.HandleFunc("POST /knowledge", s.auth(s.upsertKnowledge))
	mux.HandleFunc("GET /knowledge", s.auth(s.listKnowledge))

	// Queue
	mux.HandleFunc("GET /queue/stats", s.auth(s.queueStats))

	// SSE stream
	mux.HandleFunc("GET /events", s.auth(s.deps.Hub.handleSSE))

	// Legacy compat
	mux.HandleFunc("GET /updates/manifest", s.auth(s.manifest))
	mux.HandleFunc("GET /knowledge/sync", s.auth(s.knowledgeSync))

	return cors(logger(mux))
}

// ── Helpers ────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func intParam(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// ── Handlers ───────────────────────────────────────────────────────────────

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"status":   "ok",
		"ts":       time.Now().UTC().Format(time.RFC3339),
		"version":  "2.0.0",
		"ollama":   s.deps.AI.Healthy(),
		"clients":  s.deps.Hub.ConnectedClients(),
	})
}

// ── Agents ─────────────────────────────────────────────────────────────────

func (s *Server) registerAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		IP       string `json:"ip"`
		Port     int    `json:"port"`
		Hostname string `json:"hostname"`
		Version  string `json:"version"`
		AgentKey string `json:"agent_key"`
		Type     string `json:"type"`
	}
	if err := decode(r, &req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.IP == "" {
		jsonError(w, "name and ip are required", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		req.Port = 8081
	}
	if req.Type == "" {
		req.Type = "frank"
	}

	loc, gw := network.Resolve(req.IP)
	id := fmt.Sprintf("frank-%s", req.IP)

	agent := db.Agent{
		ID:       id,
		Name:     req.Name,
		Type:     req.Type,
		IP:       req.IP,
		Port:     req.Port,
		Hostname: req.Hostname,
		Version:  req.Version,
		AgentKey: req.AgentKey,
		Location: loc,
		Gateway:  gw,
		Status:   "ok",
		LastSeen: time.Now().UTC(),
		Enabled:  true,
	}
	if err := s.deps.DB.UpsertAgent(agent); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.deps.Hub.Broadcast(EvAgentUpdate, agent)
	slog.Info("agent registered", "id", id, "ip", req.IP, "location", loc)
	jsonOK(w, map[string]any{
		"id":       id,
		"location": loc,
		"gateway":  gw,
	})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string  `json:"agent_id"`
		Name    string  `json:"name"`
		IP      string  `json:"ip"`
		CPU     float64 `json:"cpu_pct"`
		Mem     float64 `json:"mem_pct"`
		Disk    float64 `json:"disk_pct"`
		Status  string  `json:"status"`
		Version string  `json:"version"`
	}
	if err := decode(r, &req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.AgentID == "" {
		if req.IP != "" {
			req.AgentID = "frank-" + req.IP
		} else {
			jsonError(w, "agent_id required", http.StatusBadRequest)
			return
		}
	}
	if req.Status == "" {
		req.Status = "ok"
	}

	// Update agent status
	s.deps.DB.SetAgentStatus(req.AgentID, req.Status)

	// Insert heartbeat
	hb := db.Heartbeat{
		AgentID: req.AgentID,
		IP:      req.IP,
		CPU:     req.CPU,
		Mem:     req.Mem,
		Disk:    req.Disk,
		Status:  req.Status,
		TS:      time.Now().UTC(),
	}
	s.deps.DB.InsertHeartbeat(hb)

	// Auto-alert on high resource usage
	settings, _ := s.deps.DB.GetSettings()
	if req.CPU >= float64(settings.AlertCPUThreshold) {
		s.deps.DB.CreateAlert(db.Alert{
			AgentID: req.AgentID,
			Level:   "warning",
			Source:  "agent",
			Message: fmt.Sprintf("CPU alto: %.1f%%", req.CPU),
			Details: fmt.Sprintf("Agente %s reportó CPU al %.1f%%", req.AgentID, req.CPU),
		})
	}
	if req.Mem >= float64(settings.AlertMemThreshold) {
		s.deps.DB.CreateAlert(db.Alert{
			AgentID: req.AgentID,
			Level:   "warning",
			Source:  "agent",
			Message: fmt.Sprintf("Memoria alta: %.1f%%", req.Mem),
			Details: fmt.Sprintf("Agente %s reportó memoria al %.1f%%", req.AgentID, req.Mem),
		})
	}
	if req.Disk >= float64(settings.AlertDiskThreshold) {
		s.deps.DB.CreateAlert(db.Alert{
			AgentID: req.AgentID,
			Level:   "critical",
			Source:  "agent",
			Message: fmt.Sprintf("Disco lleno: %.1f%%", req.Disk),
			Details: fmt.Sprintf("Agente %s reportó disco al %.1f%%", req.AgentID, req.Disk),
		})
	}

	s.deps.Hub.Broadcast(EvHeartbeat, hb)

	// Return pending commands for this agent
	cmds, _ := s.deps.DB.ListCommands(req.AgentID, 10)
	var pending []db.Command
	for _, c := range cmds {
		if c.Status == "pending" {
			pending = append(pending, c)
		}
	}
	jsonOK(w, map[string]any{"commands": pending})
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.deps.DB.ListAgents()
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if agents == nil {
		agents = []db.Agent{}
	}
	jsonOK(w, agents)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, err := s.deps.DB.GetAgent(id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	heartbeats, _ := s.deps.DB.GetHeartbeats(id, intParam(r, "limit", 50))
	if heartbeats == nil {
		heartbeats = []db.Heartbeat{}
	}
	jsonOK(w, map[string]any{
		"agent":      agent,
		"heartbeats": heartbeats,
	})
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deps.DB.DeleteAgent(id); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	s.deps.Hub.Broadcast(EvAgentUpdate, map[string]any{"id": id, "deleted": true})
	jsonOK(w, map[string]string{"status": "deleted"})
}

// ── Commands ───────────────────────────────────────────────────────────────

func (s *Server) createCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID   string `json:"agent_id"`
		Command   string `json:"command"`
		Params    any    `json:"params"`
		Requester string `json:"requester"`
	}
	if err := decode(r, &req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.AgentID == "" || req.Command == "" {
		jsonError(w, "agent_id and command are required", http.StatusBadRequest)
		return
	}

	params, _ := json.Marshal(req.Params)
	cmdID := uuid.New().String()
	cmd := db.Command{
		ID:        cmdID,
		AgentID:   req.AgentID,
		Command:   req.Command,
		Params:    string(params),
		Status:    "pending",
		Requester: req.Requester,
	}
	if err := s.deps.DB.CreateCommand(cmd); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	// Deliver to agent asynchronously
	go s.deliverCommand(cmd, req.Params)

	jsonOK(w, map[string]string{"id": cmdID, "status": "pending"})
}

func (s *Server) deliverCommand(cmd db.Command, params any) {
	agent, err := s.deps.DB.GetAgent(cmd.AgentID)
	if err != nil {
		s.deps.DB.UpdateCommand(cmd.ID, "failed", "agent not found")
		return
	}

	body, _ := json.Marshal(map[string]any{
		"action": cmd.Command,
		"params": params,
	})

	url := fmt.Sprintf("http://%s:%d/execute", agent.IP, agent.Port)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", agent.AgentKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.deps.DB.UpdateCommand(cmd.ID, "failed", err.Error())
		s.deps.Hub.Broadcast(EvCommandResult, map[string]any{
			"id": cmd.ID, "status": "failed", "result": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	result, _ := io.ReadAll(resp.Body)
	status := "done"
	if resp.StatusCode >= 400 {
		status = "failed"
	}
	s.deps.DB.UpdateCommand(cmd.ID, status, string(result))
	s.deps.Hub.Broadcast(EvCommandResult, map[string]any{
		"id": cmd.ID, "status": status, "result": string(result),
	})
}

func (s *Server) listCommands(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	limit := intParam(r, "limit", 50)
	cmds, err := s.deps.DB.ListCommands(agentID, limit)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if cmds == nil {
		cmds = []db.Command{}
	}
	jsonOK(w, cmds)
}

func (s *Server) getCommand(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cmd, err := s.deps.DB.GetCommand(id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	jsonOK(w, cmd)
}

// ── Alerts ─────────────────────────────────────────────────────────────────

func (s *Server) createAlert(w http.ResponseWriter, r *http.Request) {
	var a db.Alert
	if err := decode(r, &a); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if a.Message == "" {
		jsonError(w, "message is required", http.StatusBadRequest)
		return
	}
	id, err := s.deps.DB.CreateAlert(a)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	a.ID = id
	a.TS = time.Now().UTC()
	s.deps.Hub.Broadcast(EvAlert, a)
	jsonOK(w, map[string]any{"id": id})
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	limit := intParam(r, "limit", 100)
	alerts, err := s.deps.DB.ListAlerts(level, limit)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if alerts == nil {
		alerts = []db.Alert{}
	}
	jsonOK(w, alerts)
}

func (s *Server) updateAlert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := decode(r, &req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.deps.DB.UpdateAlertStatus(id, req.Status); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": req.Status})
}

// ── Tickets ────────────────────────────────────────────────────────────────

func (s *Server) createTicket(w http.ResponseWriter, r *http.Request) {
	var t db.Ticket
	if err := decode(r, &t); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if t.Message == "" {
		jsonError(w, "message is required", http.StatusBadRequest)
		return
	}

	// Apply rules engine
	result := s.deps.Rules.Evaluate(map[string]string{
		"message":  t.Message,
		"category": t.Category,
		"priority": t.Priority,
		"pc_name":  t.PCName,
		"username": t.Username,
	})
	if result.Matched {
		if result.Category != "" {
			t.Category = result.Category
		}
		if result.Priority != "" {
			t.Priority = result.Priority
		}
	}

	id, err := s.deps.DB.CreateTicket(t)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	t.ID = id
	t.CreatedAt = time.Now().UTC()
	t.UpdatedAt = t.CreatedAt

	// Add auto-response from rules if available
	if result.Response != "" {
		s.deps.DB.AddMessage(db.TicketMessage{
			TicketID: id,
			Author:   "Sistema",
			Content:  result.Response,
		})
	}

	s.deps.Hub.Broadcast(EvTicketUpdate, t)
	slog.Info("ticket created", "id", id, "category", t.Category, "priority", t.Priority)
	jsonOK(w, map[string]any{
		"id":       id,
		"category": t.Category,
		"priority": t.Priority,
		"response": result.Response,
	})
}

func (s *Server) listTickets(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit := intParam(r, "limit", 50)
	tickets, err := s.deps.DB.ListTickets(status, limit)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if tickets == nil {
		tickets = []db.Ticket{}
	}
	jsonOK(w, tickets)
}

func (s *Server) getTicket(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	ticket, err := s.deps.DB.GetTicket(id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	messages, _ := s.deps.DB.GetMessages(id)
	if messages == nil {
		messages = []db.TicketMessage{}
	}
	jsonOK(w, map[string]any{"ticket": ticket, "messages": messages})
}

func (s *Server) updateTicket(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := decode(r, &req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.deps.DB.UpdateTicket(id, req.Status); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	ticket, _ := s.deps.DB.GetTicket(id)
	s.deps.Hub.Broadcast(EvTicketUpdate, ticket)
	jsonOK(w, map[string]string{"status": req.Status})
}

func (s *Server) addMessage(w http.ResponseWriter, r *http.Request) {
	ticketID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct {
		Author  string `json:"author"`
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		jsonError(w, "content is required", http.StatusBadRequest)
		return
	}
	msg := db.TicketMessage{
		TicketID: ticketID,
		Author:   req.Author,
		Content:  req.Content,
	}
	id, err := s.deps.DB.AddMessage(msg)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"id": id})
}

func (s *Server) getMessages(w http.ResponseWriter, r *http.Request) {
	ticketID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	messages, err := s.deps.DB.GetMessages(ticketID)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []db.TicketMessage{}
	}
	jsonOK(w, messages)
}

// ── AI ─────────────────────────────────────────────────────────────────────

func (s *Server) aiQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt  string `json:"prompt"`
		Context string `json:"context"`
		Format  string `json:"format"`
	}
	if err := decode(r, &req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Prompt == "" {
		jsonError(w, "prompt is required", http.StatusBadRequest)
		return
	}

	system := "Eres un asistente de soporte IT para una empresa. Responde en español de forma clara y concisa. " +
		"Si no estás seguro, dilo claramente. Máximo 3 párrafos."
	if req.Context != "" {
		system += "\n\nContexto adicional:\n" + req.Context
	}

	response, err := s.deps.AI.Query(req.Prompt, system)
	if err != nil {
		slog.Warn("ollama error", "error", err)
		jsonOK(w, map[string]any{
			"response": "El servicio de IA no está disponible en este momento.",
			"source":   "fallback",
		})
		return
	}
	jsonOK(w, map[string]any{"response": response, "source": "ollama"})
}

// ── Dashboard ──────────────────────────────────────────────────────────────

func (s *Server) dashboardStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.deps.DB.GetStats()
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, stats)
}

// ── Network ────────────────────────────────────────────────────────────────

func (s *Server) networkLocations(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, network.All())
}

// ── Settings ───────────────────────────────────────────────────────────────

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.deps.DB.GetSettings()
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, settings)
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var settings db.GatewaySettings
	if err := decode(r, &settings); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.deps.DB.SaveSettings(settings); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": "saved"})
}

// ── Rules ──────────────────────────────────────────────────────────────────

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.deps.Rules.Rules())
}

// ── Knowledge ──────────────────────────────────────────────────────────────

func (s *Server) upsertKnowledge(w http.ResponseWriter, r *http.Request) {
	var k db.Knowledge
	if err := decode(r, &k); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if k.ID == "" {
		k.ID = uuid.New().String()
	}
	if err := s.deps.DB.UpsertKnowledge(k); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"id": k.ID})
}

func (s *Server) listKnowledge(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	items, err := s.deps.DB.ListKnowledge(category)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []db.Knowledge{}
	}
	jsonOK(w, items)
}

// ── Queue ──────────────────────────────────────────────────────────────────

func (s *Server) queueStats(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, s.deps.Queue.Stats())
}

// ── Legacy compat ──────────────────────────────────────────────────────────

func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"version":    "2.0.0",
		"updated_at": time.Now().UTC().Format(time.RFC3339),
		"files":      []any{},
	})
}

func (s *Server) knowledgeSync(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	items, err := s.deps.DB.ListKnowledge(category)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []db.Knowledge{}
	}
	jsonOK(w, items)
}
