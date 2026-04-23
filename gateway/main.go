package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"miclaw-gateway/ai"
	"miclaw-gateway/api"
	"miclaw-gateway/db"
	"miclaw-gateway/queue"
	"miclaw-gateway/rules"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// ── Database ──────────────────────────────────────────────────────────
	database, err := db.New(getenv("DB_PATH", "/app/data/miclaw.db"))
	if err != nil {
		slog.Error("database init failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// ── Rules engine ──────────────────────────────────────────────────────
	ruleEngine := rules.New()
	rulesPath := getenv("RULES_PATH", "/app/configs/rules.json")
	if err := ruleEngine.LoadFromFile(rulesPath); err != nil {
		slog.Warn("rules not loaded", "path", rulesPath, "error", err)
	}

	// ── AI client ─────────────────────────────────────────────────────────
	aiClient := ai.New(
		getenv("OLLAMA_URL", "http://ollama:11434"),
		getenv("OLLAMA_MODEL", "phi4-mini:3.8b"),
	)

	// ── Job queue ─────────────────────────────────────────────────────────
	q := queue.New(getenv("QUEUE_DB", "/app/data/queue.db"))
	defer q.Close()
	q.StartWorker(5 * time.Second)

	// ── SSE hub ───────────────────────────────────────────────────────────
	hub := api.NewHub()

	// ── HTTP server ───────────────────────────────────────────────────────
	server := api.NewServer(api.Deps{
		DB:     database,
		Hub:    hub,
		Rules:  ruleEngine,
		AI:     aiClient,
		Queue:  q,
		APIKey: getenv("MICLAW_AGENT_KEY", "changeme"),
	})

	port := getenv("PORT", "3000")
	httpSrv := &http.Server{
		Addr:         ":" + port,
		Handler:      server.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0 = sin límite (requerido para SSE)
		IdleTimeout:  120 * time.Second,
	}

	// ── Background jobs ───────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Detectar agentes offline (sin heartbeat > 2 min)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				agents, err := database.ListAgents()
				if err != nil {
					continue
				}
				for _, a := range agents {
					if a.Status != "offline" && time.Since(a.LastSeen) > 2*time.Minute {
						if err := database.SetAgentStatus(a.ID, "offline"); err == nil {
							hub.Broadcast(api.EvAgentUpdate, map[string]any{
								"id": a.ID, "status": "offline",
							})
							slog.Info("agent offline", "id", a.ID)
						}
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Limpiar heartbeats > 24h
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := database.PruneHeartbeats(24 * time.Hour); err != nil {
					slog.Warn("heartbeat prune error", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// ── Start ──────────────────────────────────────────────────────────────
	slog.Info("gateway started", "port", port)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down...")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	httpSrv.Shutdown(shutCtx)
	slog.Info("gateway stopped")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
