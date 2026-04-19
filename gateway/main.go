package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"miclaw-gateway/api"
	"miclaw-gateway/db"
	"miclaw-gateway/ollama"
	"miclaw-gateway/plugins"
	"miclaw-gateway/queue"
	"miclaw-gateway/rules"
	"miclaw-gateway/updates"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// ── Base de datos ─────────────────────────────────────────────────────
	database, err := db.New(getEnv("DB_PATH", "./miclaw.db"))
	if err != nil {
		slog.Error("db init failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// ── Ollama (LLM local) ────────────────────────────────────────────────
	ollamaClient := ollama.NewClient(ollama.Config{
		BaseURL:   getEnv("OLLAMA_URL", "http://localhost:11434"),
		Model:     getEnv("OLLAMA_MODEL", "phi4-mini:3.8b"),
		Timeout:   60 * time.Second,
		RPM:       20,
		CacheSize: 256,
		CacheTTL:  10 * time.Minute,
	})

	// ── Rules engine ──────────────────────────────────────────────────────
	rulesEngine := rules.NewEngine()
	_ = rulesEngine.LoadFromFile(getEnv("RULES_PATH", "./configs/rules.json"))

	// ── Plugins ───────────────────────────────────────────────────────────
	pluginLoader := plugins.NewLoader(getEnv("PLUGINS_DIR", "./plugins"))

	// ── Offline queue ─────────────────────────────────────────────────────
	offlineQueue := queue.New(getEnv("QUEUE_DB", "./queue.db"))
	defer offlineQueue.Close()

	// ── Update manager (hot-reload) ───────────────────────────────────────
	updateMgr := updates.NewManager(updates.Config{
		ManifestPath:  getEnv("MANIFEST_PATH", "./configs/manifest.json"),
		DataDir:       getEnv("DATA_DIR", "./configs"),
		CheckInterval: 5 * time.Minute,
		OnReload: func(component string) {
			slog.Info("hot-reload triggered", "component", component)
			if component == "rules.json" {
				if err := rulesEngine.LoadFromFile("./configs/rules.json"); err != nil {
					slog.Error("rules reload failed", "error", err)
				}
			}
		},
	})
	updateMgr.Start()
	defer updateMgr.Stop()

	// ── SSE Hub ───────────────────────────────────────────────────────────
	hub := api.NewHub()

	// ── HTTP Server ───────────────────────────────────────────────────────
	srv := api.NewServer(api.Deps{
		DB:      database,
		Ollama:  ollamaClient,
		Rules:   rulesEngine,
		Plugins: pluginLoader,
		Queue:   offlineQueue,
		Updates: updateMgr,
		Hub:     hub,
		APIKey:  getEnv("MICLAW_AGENT_KEY", "changeme"),
	})

	addr := ":" + getEnv("PORT", "3000")
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0 = sin límite para SSE streams
		IdleTimeout:  120 * time.Second,
	}

	// ── Cleanup goroutines ────────────────────────────────────────────────

	// Marcar agentes offline si no hay heartbeat en 2 min.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			agents, err := database.ListAgents()
			if err != nil {
				continue
			}
			for _, a := range agents {
				if time.Since(a.LastSeen) > 2*time.Minute && a.Status != "offline" {
					if err := database.UpdateAgentStatus(a.ID, "offline"); err == nil {
						hub.Broadcast(api.EvAgentUpdate, map[string]any{
							"id": a.ID, "status": "offline",
						})
						slog.Info("agent marked offline", "id", a.ID, "last_seen", a.LastSeen)
					}
				}
			}
		}
	}()

	// Limpiar heartbeats con más de 24h de antigüedad (evitar que la DB crezca).
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := database.PruneHeartbeats(24 * time.Hour); err != nil {
				slog.Warn("heartbeat prune failed", "error", err)
			}
		}
	}()

	// ── Iniciar servidor ──────────────────────────────────────────────────
	slog.Info("gateway started", "addr", addr)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	slog.Info("gateway stopped gracefully")
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
