package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"miclaw-gateway/db"
	"miclaw-gateway/ollama"
	"miclaw-gateway/plugins"
	"miclaw-gateway/queue"
	"miclaw-gateway/rules"
	"miclaw-gateway/updates"
)

// ─── Deps & Server ─────────────────────────────────────────────────────────

type Deps struct {
	DB      *db.DB
	Ollama  *ollama.Client
	Rules   *rules.Engine
	Plugins *plugins.Loader
	Queue   *queue.Queue
	Updates *updates.Manager
	Hub     *Hub
	APIKey  string
}

type Server struct {
	deps    Deps
	limiter *ipLimiter
}

func NewServer(d Deps) *Server {
	return &Server{deps: d, limiter: newIPLimiter(200)}
}

// Routes devuelve el mux HTTP completamente configurado.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// ── Públicas ──────────────────────────────────────────────────────────
	mux.HandleFunc("GET /health", s.handleHealth)

	// ── Protegidas con API Key ─────────────────────────────────────────────
	protected := http.NewServeMux()

	// Agentes
	protected.HandleFunc("POST /agents/register", s.handleAgentsRegister)
	protected.HandleFunc("POST /agents/heartbeat", s.handleAgentsHeartbeat)
	protected.HandleFunc("GET /agents", s.handleAgentsList)
	protected.HandleFunc("GET /agents/{id}", s.handleAgentsGet)

	// Comandos remotos
	protected.HandleFunc("POST /commands", s.handleCommandCreate)
	protected.HandleFunc("GET /commands/{id}", s.handleCommandGet)
	protected.HandleFunc("GET /commands", s.handleCommandsList)

	// Alertas
	protected.HandleFunc("POST /alerts", s.handleAlertCreate)
	protected.HandleFunc("GET /alerts", s.handleAlertsList)
	protected.HandleFunc("PATCH /alerts/{id}", s.handleAlertAck)

	// Tickets
	protected.HandleFunc("POST /tickets", s.handleTicketsCreate)
	protected.HandleFunc("GET /tickets", s.handleTicketsList)
	protected.HandleFunc("GET /tickets/{id}", s.handleTicketsGet)
	protected.HandleFunc("PATCH /tickets/{id}", s.handleTicketsPatch)
	protected.HandleFunc("POST /tickets/{id}/messages", s.handleTicketMessageCreate)
	protected.HandleFunc("GET /tickets/{id}/messages", s.handleTicketMessagesList)

	// IA
	protected.HandleFunc("POST /ai/query", s.handleAIQuery)

	// Dashboard
	protected.HandleFunc("GET /dashboard/stats", s.handleDashboardStats)

	// Network (MPLS locations)
	protected.HandleFunc("GET /network/locations", s.handleNetworkLocations)

	// SSE — push events al frontend
	protected.HandleFunc("GET /events", s.handleSSE)

	// Legacy / compatibilidad
	protected.HandleFunc("GET /updates/manifest", s.handleUpdatesManifest)
	protected.HandleFunc("GET /knowledge/sync", s.handleKnowledgeSync)
	protected.HandleFunc("POST /knowledge", s.handleKnowledgeUpsert)
	protected.HandleFunc("POST /plugins/run", s.handlePluginRun)
	protected.HandleFunc("GET /queue/stats", s.handleQueueStats)

	mux.Handle("/", s.authMiddleware(protected))

	var h http.Handler = mux
	h = rateLimitMiddleware(s.limiter)(h)
	h = loggingMiddleware(h)
	h = recoveryMiddleware(h)
	h = corsMiddleware(h)
	return h
}

// ─── Health ────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, map[string]any{
		"status":  "ok",
		"time":    time.Now().UTC().Format(time.RFC3339),
		"clients": s.deps.Hub.ClientCount(),
	})
}

// ─── Dashboard ─────────────────────────────────────────────────────────────

func (s *Server) handleDashboardStats(w http.ResponseWriter, _ *http.Request) {
	stats, err := s.deps.DB.GetDashboardStats()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, stats)
}

// ─── Network ───────────────────────────────────────────────────────────────

func (s *Server) handleNetworkLocations(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, AllLocations())
}

// ─── Agents ────────────────────────────────────────────────────────────────

func (s *Server) handleAgentsRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		IP       string `json:"ip"`
		Port     int    `json:"port"`
		Hostname string `json:"hostname"`
		Version  string `json:"version"`
		AgentKey string `json:"agent_key"` // clave que el agente usa en su propio HTTP server
	}
	if !decodeJSON(w, r, &req) {
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

	loc, gw := ResolveRoute(req.IP)
	agentID := db.SanitizeID(req.Name, req.IP)

	agent := db.Agent{
		ID:       agentID,
		Name:     req.Name,
		Type:     req.Type,
		IP:       req.IP,
		Port:     req.Port,
		Hostname: req.Hostname,
		Location: loc,
		Gateway:  gw,
		Status:   "ok",
		Version:  req.Version,
		AgentKey: req.AgentKey,
		LastSeen: time.Now(),
		Enabled:  true,
	}
	if err := s.deps.DB.UpsertAgent(agent); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("agent registered", "id", agentID, "ip", req.IP, "location", loc)

	s.deps.Hub.Broadcast(EvAgentUpdate, agent.Public())
	jsonOK(w, map[string]any{
		"id":       agentID,
		"location": loc,
		"gateway":  gw,
	})
}

func (s *Server) handleAgentsHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID  string  `json:"agent_id"`
		Name     string  `json:"name"`
		IP       string  `json:"ip"`
		Hostname string  `json:"hostname"`
		Version  string  `json:"version"`
		CPUPct   float64 `json:"cpu_pct"`
		MemPct   float64 `json:"mem_pct"`
		DiskPct  float64 `json:"disk_pct"`
		Status   string  `json:"status"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.IP == "" {
		jsonError(w, "ip is required", http.StatusBadRequest)
		return
	}

	agentID := req.AgentID
	if agentID == "" && req.Name != "" {
		agentID = db.SanitizeID(req.Name, req.IP)
	}
	if req.Status == "" {
		req.Status = "ok"
	}

	// Actualizar last_seen + status en agents.
	_ = s.deps.DB.UpdateAgentHeartbeat(agentID, req.Status, time.Now())

	// Guardar muestra de métricas.
	_ = s.deps.DB.InsertHeartbeat(db.Heartbeat{
		AgentID: agentID,
		IP:      req.IP,
		CPUPct:  req.CPUPct,
		MemPct:  req.MemPct,
		DiskPct: req.DiskPct,
		Status:  req.Status,
	})

	s.deps.Hub.Broadcast(EvHeartbeat, map[string]any{
		"agent_id": agentID,
		"status":   req.Status,
		"cpu_pct":  req.CPUPct,
		"mem_pct":  req.MemPct,
		"disk_pct": req.DiskPct,
	})

	// Devolver comandos pendientes para este agente.
	cmds, _ := s.deps.DB.ListCommands(agentID, 5)
	var pending []db.Command
	for _, c := range cmds {
		if c.Status == "pending" {
			pending = append(pending, c)
		}
	}
	jsonOK(w, map[string]any{"status": "ok", "pending_commands": pending})
}

func (s *Server) handleAgentsList(w http.ResponseWriter, _ *http.Request) {
	agents, err := s.deps.DB.ListAgents()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Devolver vista pública (sin agent_key).
	pub := make([]db.AgentPublic, 0, len(agents))
	for _, a := range agents {
		pub = append(pub, a.Public())
	}
	jsonOK(w, pub)
}

func (s *Server) handleAgentsGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.deps.DB.GetAgent(id)
	if !ok {
		jsonError(w, "agent not found", http.StatusNotFound)
		return
	}
	heartbeats, _ := s.deps.DB.RecentHeartbeats(id, 20)
	jsonOK(w, map[string]any{
		"agent":      agent.Public(),
		"heartbeats": heartbeats,
	})
}

// ─── Remote Commands ───────────────────────────────────────────────────────

func (s *Server) handleCommandCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID   string `json:"agent_id"`
		Command   string `json:"command"`
		Params    string `json:"params"`
		Requester string `json:"requester"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.AgentID == "" || req.Command == "" {
		jsonError(w, "agent_id and command are required", http.StatusBadRequest)
		return
	}

	agent, ok := s.deps.DB.GetAgent(req.AgentID)
	if !ok {
		jsonError(w, "agent not found", http.StatusNotFound)
		return
	}

	if req.Requester == "" {
		req.Requester = "ui"
	}
	if req.Params == "" {
		req.Params = "{}"
	}

	cmdID := fmt.Sprintf("cmd-%d", time.Now().UnixNano())
	cmd := db.Command{
		ID:        cmdID,
		AgentID:   req.AgentID,
		Command:   req.Command,
		Params:    req.Params,
		Status:    "pending",
		Requester: req.Requester,
	}
	if err := s.deps.DB.InsertCommand(cmd); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Intentar entrega inmediata al agente en background.
	go s.deliverCommand(agent, cmd)

	jsonOK(w, map[string]any{"id": cmdID, "status": "pending"})
}

// deliverCommand intenta enviar un comando al agente Frank vía HTTP.
func (s *Server) deliverCommand(agent db.Agent, cmd db.Command) {
	_ = s.deps.DB.UpdateCommandResult(cmd.ID, "sent", "")

	agentURL := fmt.Sprintf("http://%s:%d/execute", agent.IP, agent.Port)
	payload := map[string]any{
		"action": cmd.Command,
		"params": map[string]string{},
	}
	if cmd.Params != "" && cmd.Params != "{}" {
		var p any
		if err := json.Unmarshal([]byte(cmd.Params), &p); err == nil {
			payload["params"] = p
		}
	}

	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, agentURL, bytes.NewReader(body))
	if err != nil {
		s.failCommand(cmd.ID, "request build failed: "+err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if agent.AgentKey != "" {
		httpReq.Header.Set("X-API-Key", agent.AgentKey)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		s.failCommand(cmd.ID, "agent unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	resultBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	status := "done"
	if resp.StatusCode >= 400 {
		status = "failed"
	}

	_ = s.deps.DB.UpdateCommandResult(cmd.ID, status, string(resultBytes))
	s.deps.Hub.Broadcast(EvCommandResult, map[string]any{
		"id":       cmd.ID,
		"agent_id": cmd.AgentID,
		"command":  cmd.Command,
		"status":   status,
		"result":   string(resultBytes),
	})
	slog.Info("command delivered", "id", cmd.ID, "agent", cmd.AgentID, "status", status)
}

func (s *Server) failCommand(id, reason string) {
	_ = s.deps.DB.UpdateCommandResult(id, "failed", reason)
	s.deps.Hub.Broadcast(EvCommandResult, map[string]any{
		"id": id, "status": "failed", "result": reason,
	})
}

func (s *Server) handleCommandGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cmd, ok := s.deps.DB.GetCommand(id)
	if !ok {
		jsonError(w, "command not found", http.StatusNotFound)
		return
	}
	jsonOK(w, cmd)
}

func (s *Server) handleCommandsList(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	cmds, err := s.deps.DB.ListCommands(agentID, limit)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, cmds)
}

// ─── Alerts ────────────────────────────────────────────────────────────────

func (s *Server) handleAlertCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
		Level   string `json:"level"`
		Source  string `json:"source"`
		Message string `json:"message"`
		Details string `json:"details"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Message == "" {
		jsonError(w, "message is required", http.StatusBadRequest)
		return
	}
	if req.Level == "" {
		req.Level = "info"
	}
	if req.Source == "" {
		req.Source = "agent"
	}

	alert := db.Alert{
		AgentID: req.AgentID,
		Level:   req.Level,
		Source:  req.Source,
		Message: req.Message,
		Details: req.Details,
		Status:  "open",
	}
	id, err := s.deps.DB.InsertAlert(alert)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	alert.ID = id

	slog.Warn("alert received", "level", req.Level, "agent", req.AgentID, "msg", req.Message)
	s.deps.Hub.Broadcast(EvAlert, alert)
	jsonOK(w, map[string]any{"id": id, "status": "open"})
}

func (s *Server) handleAlertsList(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	alerts, err := s.deps.DB.ListAlerts(limit, level)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, alerts)
}

func (s *Server) handleAlertAck(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct{ Status string `json:"status"` }
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status == "" {
		req.Status = "ack"
	}
	if err := s.deps.DB.AckAlert(id, req.Status); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": req.Status})
}

// ─── Tickets ───────────────────────────────────────────────────────────────

func (s *Server) handleTicketsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PCName    string `json:"pc_name"`
		Username  string `json:"username"`
		Message   string `json:"message"`
		Category  string `json:"category"`
		Priority  string `json:"priority"`
		AgentID   string `json:"agent_id"`
		Telemetry string `json:"telemetry"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Message == "" {
		jsonError(w, "message is required", http.StatusBadRequest)
		return
	}
	if req.Category == "" {
		req.Category = "general"
	}
	if req.Priority == "" {
		req.Priority = "normal"
	}

	t := db.Ticket{
		PCName:    req.PCName,
		Username:  req.Username,
		Message:   req.Message,
		Category:  req.Category,
		Priority:  req.Priority,
		AgentID:   req.AgentID,
		Telemetry: req.Telemetry,
		Status:    "open",
	}

	// Aplicar reglas.
	ctx := rules.Context{
		"pc_name":  req.PCName,
		"username": req.Username,
		"message":  req.Message,
		"category": req.Category,
	}
	if result, matched := s.deps.Rules.Evaluate(ctx); matched {
		if result.Category != "" {
			t.Category = result.Category
		}
	}

	id, err := s.deps.DB.InsertTicket(t)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("ticket created", "id", id, "pc", req.PCName, "category", t.Category)
	t.ID = id
	s.deps.Hub.Broadcast(EvTicketUpdate, t)
	jsonOK(w, map[string]any{"id": id, "status": "open", "category": t.Category})
}

func (s *Server) handleTicketsList(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	tickets, err := s.deps.DB.ListTickets(limit)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, tickets)
}

func (s *Server) handleTicketsGet(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	ticket, ok := s.deps.DB.GetTicket(id)
	if !ok {
		jsonError(w, "ticket not found", http.StatusNotFound)
		return
	}
	messages, _ := s.deps.DB.ListTicketMessages(id)
	jsonOK(w, map[string]any{"ticket": ticket, "messages": messages})
}

func (s *Server) handleTicketsPatch(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct{ Status string `json:"status"` }
	if !decodeJSON(w, r, &req) {
		return
	}
	validStatuses := map[string]bool{"open": true, "in_progress": true, "resolved": true, "closed": true}
	if !validStatuses[req.Status] {
		jsonError(w, "invalid status", http.StatusBadRequest)
		return
	}
	if err := s.deps.DB.UpdateTicketStatus(id, req.Status); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.deps.Hub.Broadcast(EvTicketUpdate, map[string]any{"id": id, "status": req.Status})
	jsonOK(w, map[string]string{"status": req.Status})
}

func (s *Server) handleTicketMessageCreate(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	ticketID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req struct {
		Author  string `json:"author"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Content == "" {
		jsonError(w, "content is required", http.StatusBadRequest)
		return
	}
	if req.Author == "" {
		req.Author = "system"
	}
	msgID, err := s.deps.DB.InsertTicketMessage(db.TicketMessage{
		TicketID: ticketID,
		Author:   req.Author,
		Content:  req.Content,
	})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.deps.Hub.Broadcast(EvTicketUpdate, map[string]any{
		"ticket_id": ticketID,
		"message":   map[string]any{"id": msgID, "author": req.Author, "content": req.Content},
	})
	jsonOK(w, map[string]any{"id": msgID})
}

func (s *Server) handleTicketMessagesList(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	ticketID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	messages, err := s.deps.DB.ListTicketMessages(ticketID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, messages)
}

// ─── AI Query ─────────────────────────────────────────────────────────────

func (s *Server) handleAIQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt    string `json:"prompt"`
		AgentID   string `json:"agent_id"`
		Requester string `json:"requester"`
		Context   string `json:"context"`  // contexto adicional (logs, estado de agentes, etc.)
		Format    string `json:"format"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Prompt == "" {
		jsonError(w, "prompt is required", http.StatusBadRequest)
		return
	}

	// Enriquecer prompt con contexto de agentes si se solicita análisis de red.
	fullPrompt := req.Prompt
	if req.Context != "" {
		fullPrompt = fmt.Sprintf("Contexto del sistema:\n%s\n\nConsulta: %s", req.Context, req.Prompt)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	var (
		resp string
		err  error
	)
	if req.Format == "json" {
		resp, err = s.deps.Ollama.GenerateJSON(ctx, fullPrompt)
	} else {
		resp, err = s.deps.Ollama.Generate(ctx, fullPrompt)
	}

	if err != nil {
		slog.Warn("ollama error", "error", err)
		rCtx := rules.Context{"message": req.Prompt, "requester": req.Requester}
		if result, matched := s.deps.Rules.Evaluate(rCtx); matched && result.Response != "" {
			jsonOK(w, map[string]string{"response": result.Response, "source": "rules"})
			return
		}
		jsonError(w, "AI unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	jsonOK(w, map[string]string{
		"response": resp,
		"source":   "ollama",
		"model":    s.deps.Ollama.Model(),
	})
}

// ─── Updates / Knowledge / Plugins / Queue (legacy) ───────────────────────

func (s *Server) handleUpdatesManifest(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, s.deps.Updates.CurrentManifest())
}

func (s *Server) handleKnowledgeSync(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	entries, err := s.deps.DB.ListKnowledge(category)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{
		"entries":   entries,
		"count":     len(entries),
		"synced_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleKnowledgeUpsert(w http.ResponseWriter, r *http.Request) {
	var entry db.KnowledgeEntry
	if !decodeJSON(w, r, &entry) {
		return
	}
	if entry.ID == "" || entry.Content == "" {
		jsonError(w, "id and content are required", http.StatusBadRequest)
		return
	}
	if err := s.deps.DB.UpsertKnowledge(entry); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handlePluginRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Plugin  string         `json:"plugin"`
		Payload map[string]any `json:"payload"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Plugin == "" {
		jsonError(w, "plugin name required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := s.deps.Plugins.Run(ctx, req.Plugin, req.Payload)
	if err != nil {
		jsonError(w, "plugin error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, result)
}

func (s *Server) handleQueueStats(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, s.deps.Queue.Stats())
}

// ─── helpers ──────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		jsonError(w, "read body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(dst); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func buildAgentURL(agent db.Agent, path string) string {
	return fmt.Sprintf("http://%s:%d%s", agent.IP, agent.Port, path)
}

// pathID extrae y parsea un segmento numérico del path (compatibilidad con Go 1.21).
func pathID(r *http.Request, name string) (int64, bool) {
	s := strings.TrimPrefix(r.URL.Path, "/")
	_ = s // placeholder — en Go 1.22 se usa r.PathValue(name)
	v := r.PathValue(name)
	id, err := strconv.ParseInt(v, 10, 64)
	return id, err == nil
}
