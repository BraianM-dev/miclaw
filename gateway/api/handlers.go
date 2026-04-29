package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
	go s.notifyAgentOfStatusChange(id, req.Status)
	jsonOK(w, map[string]string{"status": req.Status})
}

// notifyAgentOfStatusChange envía al agente una notificación cuando el técnico cambia el estado del ticket.
func (s *Server) notifyAgentOfStatusChange(ticketID int64, newStatus string) {
	ticket, err := s.deps.DB.GetTicket(ticketID)
	if err != nil || ticket.AgentID == "" {
		return
	}
	agent, err := s.deps.DB.GetAgent(ticket.AgentID)
	if err != nil {
		slog.Warn("notifyAgentOfStatusChange: agente no encontrado", "agent_id", ticket.AgentID)
		return
	}

	statusLabel := map[string]string{
		"open":        "Abierto",
		"in_progress": "En progreso",
		"resolved":    "Resuelto",
		"closed":      "Cerrado",
	}
	label := statusLabel[newStatus]
	if label == "" {
		label = newStatus
	}

	notification := map[string]string{
		"type":      "status_change",
		"message":   fmt.Sprintf("El estado del Ticket #%d fue actualizado a: %s", ticketID, label),
		"ticket_id": fmt.Sprintf("%d", ticketID),
		"status":    newStatus,
	}
	body, _ := json.Marshal(notification)

	url := fmt.Sprintf("http://%s:%d/send_message", agent.IP, agent.Port)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", agent.AgentKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("notifyAgentOfStatusChange: fallo al notificar", "agent", ticket.AgentID, "err", err)
		return
	}
	resp.Body.Close()
	slog.Info("notifyAgentOfStatusChange: agente notificado", "ticket", ticketID, "status", newStatus)
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
	// Broadcast SSE para que el dashboard actualice el hilo del ticket en tiempo real
	s.deps.Hub.Broadcast(EvTicketUpdate, map[string]any{
		"ticket_id": ticketID,
		"message_id": id,
		"author":    req.Author,
	})

	// Si el mensaje es del equipo de soporte, notificar al agente en tiempo real
	go s.notifyAgentOfTicketReply(ticketID, req.Author, req.Content)

	jsonOK(w, map[string]any{"id": id})
}

// notifyAgentOfTicketReply envía el mensaje de soporte al agente del equipo afectado.
// Se ejecuta en background para no bloquear la respuesta HTTP.
func (s *Server) notifyAgentOfTicketReply(ticketID int64, author, content string) {
	// No notificar mensajes generados por el propio usuario o el sistema automático
	lower := strings.ToLower(author)
	if lower == "" || lower == "sistema" || lower == "usuario" || lower == "frank" {
		return
	}

	ticket, err := s.deps.DB.GetTicket(ticketID)
	if err != nil || ticket.AgentID == "" {
		return
	}

	agent, err := s.deps.DB.GetAgent(ticket.AgentID)
	if err != nil {
		slog.Warn("notifyAgentOfTicketReply: agente no encontrado", "agent_id", ticket.AgentID)
		return
	}

	notification := map[string]string{
		"type":      "reply",
		"message":   fmt.Sprintf("%s: %s", author, content),
		"ticket_id": fmt.Sprintf("%d", ticketID),
		"author":    author,
	}
	body, _ := json.Marshal(notification)

	url := fmt.Sprintf("http://%s:%d/send_message", agent.IP, agent.Port)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", agent.AgentKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("notifyAgentOfTicketReply: fallo al notificar", "agent", ticket.AgentID, "err", err)
		return
	}
	resp.Body.Close()
	slog.Info("notifyAgentOfTicketReply: agente notificado", "ticket", ticketID, "agent", ticket.AgentID)
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

// ── AI structured-response types ───────────────────────────────────────────

// aiResponseType is the discriminator for the safe-action model.
type aiResponseType string

const (
	aiRespMessage       aiResponseType = "message"
	aiRespActionRequest aiResponseType = "action_request"
)

// aiStructuredResponse is the canonical JSON shape returned by both the LLM
// and the /ai/query endpoint.  Fields are a superset of both sub-types.
type aiStructuredResponse struct {
	Type       aiResponseType `json:"type"`
	// For type=message
	Content    string         `json:"content,omitempty"`
	// For type=action_request
	Action     string         `json:"action,omitempty"`
	Target     string         `json:"target,omitempty"`
	Message    string         `json:"message,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	// Always present in the HTTP response
	Source     string         `json:"source"`
	// Convenience flat field so the frontend can use .response for plain text
	Response   string         `json:"response,omitempty"`
}

// availableActions is the canonical list of commands the agent understands.
var availableActions = []string{
	// Diagnóstico / información
	"info_sistema", "diagnostico", "espacio_disco", "listar_procesos",
	"ver_logs_frank", "generar_inventario", "ver_servicios", "ver_usuarios_activos",
	"ram_detalle", "gpu_info", "salud_disco", "uptime_sistema",
	// Red
	"estado_red", "velocidad_red", "flush_dns", "conexiones_activas",
	"latencia_red", "escaneo_red",
	// Mantenimiento / reparación
	"mantenimiento", "reiniciar_spooler", "reparar_winsock", "limpiar_temporales",
	// Control remoto
	"bloquear_pantalla", "popup_mensaje", "abrir_taskmanager",
	"matar_proceso", "reiniciar_servicio", "abrir_aplicacion",
	// Seguridad
	"estado_defender", "actualizaciones_instaladas",
}

// aiSystemPromptJSON instructs the LLM to return ONLY structured JSON
// and to never execute actions — it must propose and wait for confirmation.
const aiSystemPromptJSON = `Eres un asistente de soporte IT para MicLaw/AFE (Ferrocarriles del Estado - Uruguay).

REGLA CRÍTICA — MODELO DE ACCIONES SEGURAS:
NUNCA ejecutes acciones directamente. Cuando el usuario pide ejecutar algo,
SIEMPRE propone la acción y espera confirmación explícita del usuario.

Responde SOLO con JSON válido, sin texto fuera del JSON.

FORMATO A — Respuesta informativa (sin acción):
{"type":"message","content":"tu respuesta en español rioplatense, máximo 3 oraciones"}

FORMATO B — Propuesta de acción (requiere confirmación del usuario):
{"type":"action_request","action":"nombre_accion","target":"ID_exacto_del_agente","message":"explicación clara de qué hará y por qué","confidence":0.85}

Acciones disponibles (SOLO estas):
info_sistema, flush_dns, diagnostico, mantenimiento, espacio_disco,
listar_procesos, reiniciar_spooler, estado_red, ver_logs_frank,
velocidad_red, generar_inventario

REGLAS:
1. Solo respondés sobre soporte IT empresarial. Fuera de ese dominio:
   {"type":"message","content":"Solo puedo ayudarte con soporte IT empresarial."}
2. El campo "target" DEBE ser el ID exacto de la lista de agentes registrados.
3. Si hay varios agentes y no se especifica cuál, pedí clarificación con tipo "message".
4. "confidence" entre 0.0 y 1.0.
5. Responde en español rioplatense, conciso y técnico.
6. Si la intención no está clara, pedí aclaración con tipo "message".`

// itDomainKeywords are terms that clearly indicate an IT-related query.
var itDomainKeywords = []string{
	"agente", "alert", "ticket", "error", "windows", "red ", " red", "redes", "ip ", " ip",
	"dns", "cpu", "ram", "disco", "impresora", "wifi", "wi-fi", "vpn", "antivirus", "usuario",
	"contraseña", "office", "outlook", "teams", "servidor", "laptop", "monitor", "hardware",
	"software", "instalar", "configura", "actualiz", "wazuh", "frank", "sistema", "proceso",
	"memoria", "internet", "conexion", "conexión", "ping", "firewall", "backup", "servicio",
	"reinici", "diagnos", "inventario", "soporte", "helpdesk", "analiz", "estado", "online",
	"offline", "heartbeat", "puerto", "switch", "router", "cable", "parche", "driver",
	"pantalla", "teclado", "mouse", "ratón", "usb", "bluetooth", "imprim", "escanear",
	"correo", "mail", "carpeta", "archivo", "permi", "dominio", "active directory", "ad ",
	"virus", "malware", "ransom", "phishing", "spam", "certific", "ssl", "https", "proxy",
	"velocidad", "banda ancha", "latencia", "pérdida", "paquete", "vlan", "dhcp", "gateway",
	// greetings and meta-queries that are always OK
	"hola", "gracias", "buenas", "ayuda", "qué puedes", "que puedes", "cómo funciona",
	"como funciona", "capacidades", "funciones", "comandos",
}

// isITRelated returns true when the prompt is plausibly IT-related.
// Short prompts (<= 6 words) are allowed through to avoid over-blocking.
func isITRelated(prompt string) bool {
	lower := strings.ToLower(prompt)
	words := strings.Fields(lower)
	if len(words) <= 6 {
		return true // let the LLM judge short/ambiguous queries
	}
	for _, kw := range itDomainKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

const aiSystemPrompt = `Eres un asistente de soporte IT EXCLUSIVAMENTE para MicLaw / AFE (Administración de Ferrocarriles del Estado - Uruguay).

DOMINIOS PERMITIDOS — solo respondés sobre:
• Soporte técnico Windows: errores, BSOD, actualizaciones, activación
• Redes: conectividad, DNS, DHCP, VPN, Wi-Fi, switches, routers, VLAN
• Impresoras, escáneres y periféricos de oficina
• Microsoft Office, Outlook, Teams, OneDrive y apps empresariales
• Active Directory: usuarios, contraseñas, permisos, GPO
• Antivirus, seguridad, malware, ransomware, phishing
• Monitoreo Wazuh, agentes Frank, alertas y eventos del sistema
• Hardware: PC, laptops, servidores, monitores, UPS
• Tickets de soporte: creación, seguimiento, resolución
• Inventario y activos IT de la organización

REGLAS ESTRICTAS:
1. Si la consulta NO trata sobre IT empresarial → respondé ÚNICAMENTE: "Solo puedo ayudarte con temas de soporte IT empresarial. ¿Tenés algún problema técnico?"
2. Nunca respondas sobre recetas, cocina, geografía, historia, programación personal ni ningún otro tema fuera del soporte IT.
3. Responde SIEMPRE en español rioplatense, conciso y técnico.
4. Máximo 3 párrafos. Sin markdown complejo, sin listas largas.
5. Si no sabés la respuesta, decilo claramente sin inventar información.
6. No menciones que eres un modelo de lenguaje ni des información sobre tu entrenamiento.`

func (s *Server) aiQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt  string `json:"prompt"`
		Context string `json:"context"`
	}
	if err := decode(r, &req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Prompt == "" {
		jsonError(w, "prompt is required", http.StatusBadRequest)
		return
	}

	// Pre-filter: reject clearly off-domain queries before hitting the LLM
	if !isITRelated(req.Prompt) {
		slog.Info("ai query rejected (off-domain)", "prompt_len", len(req.Prompt))
		jsonOK(w, aiStructuredResponse{
			Type:     aiRespMessage,
			Content:  "Solo puedo ayudarte con temas de soporte IT empresarial. ¿Tenés algún problema técnico con algún equipo o sistema?",
			Response: "Solo puedo ayudarte con temas de soporte IT empresarial. ¿Tenés algún problema técnico con algún equipo o sistema?",
			Source:   "filter",
		})
		return
	}

	// Build system context: registered agents (IDs are needed for action targets)
	system := aiSystemPromptJSON
	agents, _ := s.deps.DB.ListAgents()
	if len(agents) > 0 {
		system += "\n\nAgentes registrados (usá el ID exacto como \"target\"):\n"
		for _, a := range agents {
			statusStr := "online"
			if a.Status != "ok" {
				statusStr = a.Status
			}
			system += fmt.Sprintf("  ID=%s  nombre=%s  ip=%s  [%s]\n", a.ID, a.Name, a.IP, statusStr)
		}
	}
	if req.Context != "" {
		system += "\nEstado actual del sistema:\n" + req.Context
	}

	// Call Ollama in JSON mode — forces valid JSON output
	raw, err := s.deps.AI.QueryJSON(req.Prompt, system)
	if err != nil {
		slog.Warn("ollama error", "error", err)
		jsonOK(w, aiStructuredResponse{
			Type:     aiRespMessage,
			Content:  "El servicio de IA no está disponible. Verificá que Ollama esté corriendo y tenga el modelo descargado.",
			Response: "El servicio de IA no está disponible. Verificá que Ollama esté corriendo y tenga el modelo descargado.",
			Source:   "fallback",
		})
		return
	}

	// Parse the structured LLM response
	parsed, parseErr := parseAIResponse(raw)
	if parseErr != nil {
		// LLM produced something that isn't valid JSON — return as plain message
		slog.Warn("ai response parse failed, falling back to plain text", "error", parseErr)
		jsonOK(w, aiStructuredResponse{
			Type:     aiRespMessage,
			Content:  raw,
			Response: raw,
			Source:   "ollama",
		})
		return
	}

	// Validate action_request fields
	if parsed.Type == aiRespActionRequest {
		if !isValidAction(parsed.Action) {
			slog.Warn("ai proposed unknown action, demoting to message", "action", parsed.Action)
			parsed.Type    = aiRespMessage
			parsed.Content = parsed.Message
		}
		if parsed.Target == "" {
			parsed.Type    = aiRespMessage
			parsed.Content = parsed.Message + "\n(No se pudo determinar el equipo destino. Especificá el nombre del equipo.)"
		}
	}

	// Populate the flat .response field for backwards-compat with older clients
	switch parsed.Type {
	case aiRespMessage:
		parsed.Response = parsed.Content
	case aiRespActionRequest:
		parsed.Response = parsed.Message
	}
	parsed.Source = "ollama"

	slog.Info("ai response", "type", parsed.Type, "action", parsed.Action, "target", parsed.Target)
	jsonOK(w, parsed)
}

// parseAIResponse attempts to unmarshal the raw LLM string into aiStructuredResponse.
func parseAIResponse(raw string) (aiStructuredResponse, error) {
	// Trim whitespace and possible markdown fences
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var r aiStructuredResponse
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	if r.Type == "" {
		return r, fmt.Errorf("missing type field")
	}
	return r, nil
}

// isValidAction returns true if the action name is in the allowed list.
func isValidAction(action string) bool {
	for _, a := range availableActions {
		if a == action {
			return true
		}
	}
	return false
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
