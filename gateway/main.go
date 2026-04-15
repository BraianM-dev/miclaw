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

	database, err := db.New(getEnv("DB_PATH", "./miclaw.db"))
	if err != nil {
		slog.Error("db init failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	ollamaClient := ollama.NewClient(ollama.Config{
		BaseURL:      getEnv("OLLAMA_URL", "http://localhost:11434"),
		Model:        getEnv("OLLAMA_MODEL", "phi4-mini:3.8b"),
		Timeout:      60 * time.Second,
		RPM:          20,
		CacheSize:    256,
		CacheTTL:     10 * time.Minute,
	})

	rulesEngine := rules.NewEngine()
	_ = rulesEngine.LoadFromFile(getEnv("RULES_PATH", "./configs/rules.json"))

	pluginLoader := plugins.NewLoader(getEnv("PLUGINS_DIR", "./plugins"))

	offlineQueue := queue.New(getEnv("QUEUE_DB", "./queue.db"))
	defer offlineQueue.Close()

	updateMgr := updates.NewManager(updates.Config{
		ManifestPath: getEnv("MANIFEST_PATH", "./configs/manifest.json"),
		DataDir:      getEnv("DATA_DIR", "./configs"),
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

	srv := api.NewServer(api.Deps{
		DB:      database,
		Ollama:  ollamaClient,
		Rules:   rulesEngine,
		Plugins: pluginLoader,
		Queue:   offlineQueue,
		Updates: updateMgr,
		APIKey:  getEnv("MICLAW_AGENT_KEY", "changeme"),
	})

	addr := ":" + getEnv("PORT", "3000")
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("gateway started", "addr", addr)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

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
