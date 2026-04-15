package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

// Deps holds all service dependencies injected into the HTTP layer.
type Deps struct {
	DB      *db.DB
	Ollama  *ollama.Client
	Rules   *rules.Engine
	Plugins *plugins.Loader
	Queue   *queue.Queue
	Updates *updates.Manager
	APIKey  string
}

// Server owns the mux and all handlers.
type Server struct {
	deps    Deps
	limiter *ipLimiter
}

// NewServer creates a Server with all deps wired in.
func NewServer(d Deps) *Server {
	return &Server{deps: d, limiter: newIPLimiter(120)}
}

// Routes returns the fully configured http.Handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /health", s.handleHealth)

	// Auth-protected
	protected := http.NewServeMux()
	protected.HandleFunc("POST /agents/register", s.handleAgentsRegister)
	protected.HandleFunc("GET /agents", s.handleAgentsList)
	protected.HandleFunc("POST /tickets", s.handleTicketsCreate)
	protected.HandleFunc("GET /tickets", s.handleTicketsList)
	protected.HandleFunc("POST /ai/query", s.handleAIQuery)
	protected.HandleFunc("GET /updates/manifest", s.handleUpdatesManifest)
	protected.HandleFunc("GET /knowledge/sync", s.handleKnowledgeSync)
	protected.HandleFunc("POST /knowledge", s.handleKnowledgeUpsert)
	protected.HandleFunc("POST /plugins/run", s.handlePluginRun)
	protected.HandleFunc("GET /queue/stats", s.handleQueueStats)

	mux.Handle("/", s.authMiddleware(protected))

	// Stack: CORS → recovery → logging → rate-limit → mux
	var h http.Handler = mux
	h = rateLimitMiddleware(s.limiter)(h)
	h = loggingMiddleware(h)
	h = recoveryMiddleware(h)
	h = corsMiddleware(h)
	return h
}

// ─── Health ────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, map[string]string{"status": "ok", "time": time.Now().UTC().Format(time.RFC3339)})
}

// ─── Agents ────────────────────────────────────────────────────────────────

func (s *Server) handleAgentsRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.IP == "" {
		jsonError(w, "name and ip are required", http.StatusBadRequest)
		return
	}

	agent := db.Agent{
		ID:       req.Name + "-" + req.IP,
		Name:     req.Name,
		Type:     req.Type,
		IP:       req.IP,
		Port:     req.Port,
		LastSeen: time.Now(),
		Enabled:  true,
	}
	if err := s.deps.DB.UpsertAgent(agent); err != nil {
		jsonError(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("agent registered", "id", agent.ID, "ip", agent.IP)
	jsonOK(w, agent)
}

func (s *Server) handleAgentsList(w http.ResponseWriter, _ *http.Request) {
	agents, err := s.deps.DB.ListAgents()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, agents)
}

// ─── Tickets ───────────────────────────────────────────────────────────────

func (s *Server) handleTicketsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PCName    string `json:"pc_name"`
		Username  string `json:"username"`
		Message   string `json:"message"`
		Category  string `json:"category"`
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

	t := db.Ticket{
		PCName:    req.PCName,
		Username:  req.Username,
		Message:   req.Message,
		Category:  req.Category,
		Telemetry: req.Telemetry,
		Status:    "open",
	}

	// Apply rules
	ctx := rules.Context{
		"pc_name":  req.PCName,
		"username": req.Username,
		"message":  req.Message,
		"category": req.Category,
	}
	if result, matched := s.deps.Rules.Evaluate(ctx); matched {
		slog.Info("rule matched", "action", result.Action, "category", req.Category)
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
	jsonOK(w, map[string]any{"id": id, "status": "open", "category": t.Category})
}

func (s *Server) handleTicketsList(w http.ResponseWriter, r *http.Request) {
	tickets, err := s.deps.DB.ListTickets(50)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, tickets)
}

// ─── AI Query ─────────────────────────────────────────────────────────────

func (s *Server) handleAIQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt    string `json:"prompt"`
		AgentID   string `json:"agent_id"`
		Requester string `json:"requester"`
		Format    string `json:"format"` // "json" or ""
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Prompt == "" {
		jsonError(w, "prompt is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	var (
		resp string
		err  error
	)
	if req.Format == "json" {
		resp, err = s.deps.Ollama.GenerateJSON(ctx, req.Prompt)
	} else {
		resp, err = s.deps.Ollama.Generate(ctx, req.Prompt)
	}

	if err != nil {
		slog.Warn("ollama error", "error", err)
		// Fallback: rule-based response
		rCtx := rules.Context{"message": req.Prompt, "requester": req.Requester}
		if result, matched := s.deps.Rules.Evaluate(rCtx); matched && result.Response != "" {
			jsonOK(w, map[string]string{"response": result.Response, "source": "rules"})
			return
		}
		jsonError(w, "AI unavailable and no matching rule: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	jsonOK(w, map[string]string{"response": resp, "source": "ollama", "model": s.deps.Ollama.Model()})
}

// ─── Updates Manifest ──────────────────────────────────────────────────────

func (s *Server) handleUpdatesManifest(w http.ResponseWriter, _ *http.Request) {
	m := s.deps.Updates.CurrentManifest()
	jsonOK(w, m)
}

// ─── Knowledge Sync ───────────────────────────────────────────────────────

func (s *Server) handleKnowledgeSync(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	entries, err := s.deps.DB.ListKnowledge(category)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{
		"entries":      entries,
		"count":        len(entries),
		"synced_at":    time.Now().UTC().Format(time.RFC3339),
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

// ─── Plugin Run ───────────────────────────────────────────────────────────

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

// ─── Queue Stats ──────────────────────────────────────────────────────────

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
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		// still try to decode; some clients omit content-type
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB max
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

// buildAgentURL constructs an HTTP URL for a remote agent endpoint.
// Used by the planner when forwarding commands to registered agents.
func buildAgentURL(agent db.Agent, path string) string {
	return fmt.Sprintf("http://%s:%d%s", agent.IP, agent.Port, path)
}
