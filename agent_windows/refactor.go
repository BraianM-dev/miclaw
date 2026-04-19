//go:build windows
// +build windows

// refactor.go — Capas de infraestructura adicionales sobre Frank v2.1-beta
//
// CONTENIDO:
//   1. dynamicRulesMu — mutex que protege la slice dynamicRules de main.go
//      frente a escrituras concurrentes desde GatewaySync.
//
//   2. OfflineQueue — cola SQLite con reintentos exponenciales.
//      Garantiza la entrega de tickets y telemetría aunque el gateway
//      no esté disponible en el momento de generarlos.
//      Inicialización: llamar initOfflineQueue() en main().
//
//   3. GatewaySync — descarga rules.json/intents.json desde el gateway
//      en background y hace hot-reload sin reiniciar el agente.
//      Inicialización: newGatewaySyncStart(gatewayURL, ".", 15*time.Minute).
//
// COMPILACIÓN:
//   Siempre compilar con GOARCH=amd64 GOOS=windows.
//   La dependencia go-gl/gl (usada por Fyne/GLFW) no tiene fuentes para
//   GOARCH=386 en Windows — el compilador excluye todos los archivos
//   con ese build tag y devuelve "build constraints exclude all Go files".

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ════════════════════════════════════════════════════════════════════════════
//  MUTEX COMPARTIDO PARA dynamicRules (declarado en main.go)
//
//  matchDynamicRule() (main.go) lee dynamicRules.
//  loadDynamicRules() (main.go) escribe dynamicRules en startup (sin concurrencia).
//  reloadDynamicRules() (este archivo) escribe dynamicRules en hot-reload
//  lanzado desde GatewaySync en una goroutine separada → necesita lock.
// ════════════════════════════════════════════════════════════════════════════

// dynamicRulesMu protege la slice dynamicRules definida en main.go.
var dynamicRulesMu sync.RWMutex

// ════════════════════════════════════════════════════════════════════════════
//  OFFLINE QUEUE — SQLite persistente con reintentos y back-off exponencial
//
//  Uso típico:
//    initOfflineQueue()                         // en main()
//    agentQueue.Enqueue("id", "ticket", payload) // para encolar
//
//  El worker interno reintenta cada 10 s con back-off de 15s → 30s → 60s…
//  hasta 30 min, máximo 5 reintentos por job.
// ════════════════════════════════════════════════════════════════════════════

const (
	ojPending    = "pending"
	ojProcessing = "processing"
	ojDone       = "done"
	ojFailed     = "failed"
)

// OfflineJob representa una operación encolada que debe llegar al gateway.
type OfflineJob struct {
	ID          string
	Type        string
	Payload     map[string]any
	Retries     int
	MaxRetries  int
	NextRetryAt time.Time
	Error       string
}

// OfflineQueue es una cola persistente SQLite para el agente.
type OfflineQueue struct {
	db       *sql.DB
	mu       sync.Mutex
	handlers map[string]func(OfflineJob) error
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

var agentQueue *OfflineQueue

// initOfflineQueue abre (o crea) la base de datos de la cola del agente.
// Llamar una vez desde main().
func initOfflineQueue() {
	dbPath := getEnv("QUEUE_DB", "agent_queue.db")
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=3000")
	if err != nil {
		slog.Error("offline queue: open db", "error", err)
		return
	}
	conn.SetMaxOpenConns(1)

	q := &OfflineQueue{
		db:       conn,
		handlers: make(map[string]func(OfflineJob) error),
		stopCh:   make(chan struct{}),
	}
	q.migrate()
	agentQueue = q

	agentQueue.Register("ticket", sendTicketToGateway)
	agentQueue.Register("telemetry", sendTelemetryToGateway)

	agentQueue.StartWorker(10 * time.Second)
	slog.Info("offline queue ready", "db", dbPath)
}

func (q *OfflineQueue) migrate() {
	q.db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id            TEXT PRIMARY KEY,
		type          TEXT NOT NULL,
		payload       TEXT NOT NULL DEFAULT '{}',
		status        TEXT NOT NULL DEFAULT 'pending',
		retries       INTEGER NOT NULL DEFAULT 0,
		max_retries   INTEGER NOT NULL DEFAULT 5,
		next_retry_at DATETIME NOT NULL,
		error         TEXT,
		created_at    DATETIME NOT NULL
	)`)
	q.db.Exec(`CREATE INDEX IF NOT EXISTS idx_aq_status ON jobs(status, next_retry_at)`)
}

// Register enlaza un handler a un tipo de job.
func (q *OfflineQueue) Register(jobType string, h func(OfflineJob) error) {
	q.mu.Lock()
	q.handlers[jobType] = h
	q.mu.Unlock()
}

// Enqueue agrega un job. Idempotente en IDs duplicados.
func (q *OfflineQueue) Enqueue(id, jobType string, payload map[string]any) error {
	raw, _ := json.Marshal(payload)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(
		`INSERT OR IGNORE INTO jobs (id,type,payload,status,retries,max_retries,next_retry_at,created_at)
		 VALUES (?,?,?,?,0,5,?,?)`,
		id, jobType, string(raw), ojPending, now, now,
	)
	if err == nil {
		slog.Debug("job enqueued", "id", id, "type", jobType)
	}
	return err
}

// PendingCount devuelve cuántos jobs esperan entrega.
func (q *OfflineQueue) PendingCount() int {
	var n int
	q.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status=?`, ojPending).Scan(&n)
	return n
}

// StartWorker lanza el loop de reintentos en background.
func (q *OfflineQueue) StartWorker(interval time.Duration) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				q.process()
			case <-q.stopCh:
				return
			}
		}
	}()
}

// Stop detiene el worker de forma ordenada.
func (q *OfflineQueue) Stop() {
	select {
	case <-q.stopCh:
	default:
		close(q.stopCh)
	}
	q.wg.Wait()
	q.db.Close()
}

func (q *OfflineQueue) process() {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := q.db.Query(
		`SELECT id,type,payload,retries,max_retries,COALESCE(error,'')
		 FROM jobs WHERE status=? AND next_retry_at<=? ORDER BY created_at ASC LIMIT 5`,
		ojPending, now,
	)
	if err != nil {
		return
	}
	var jobs []OfflineJob
	for rows.Next() {
		var j OfflineJob
		var raw string
		if rows.Scan(&j.ID, &j.Type, &raw, &j.Retries, &j.MaxRetries, &j.Error) == nil {
			_ = json.Unmarshal([]byte(raw), &j.Payload)
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	for _, job := range jobs {
		q.runJob(job)
	}
}

func (q *OfflineQueue) runJob(job OfflineJob) {
	q.db.Exec(`UPDATE jobs SET status=? WHERE id=?`, ojProcessing, job.ID)

	q.mu.Lock()
	h, ok := q.handlers[job.Type]
	q.mu.Unlock()
	if !ok {
		q.markFailed(job.ID, "no handler for: "+job.Type)
		return
	}

	err := h(job)
	if err == nil {
		q.db.Exec(`UPDATE jobs SET status=? WHERE id=?`, ojDone, job.ID)
		slog.Info("offline job done", "id", job.ID, "type", job.Type)
		return
	}

	job.Retries++
	if job.Retries >= job.MaxRetries {
		q.markFailed(job.ID, err.Error())
		return
	}

	backoff := agentBackoff(job.Retries)
	next := time.Now().UTC().Add(backoff).Format(time.RFC3339)
	q.db.Exec(
		`UPDATE jobs SET status=?,retries=?,next_retry_at=?,error=? WHERE id=?`,
		ojPending, job.Retries, next, err.Error(), job.ID,
	)
	slog.Warn("job retry scheduled", "id", job.ID, "backoff", backoff, "attempt", job.Retries)
}

func (q *OfflineQueue) markFailed(id, reason string) {
	q.db.Exec(`UPDATE jobs SET status=?,error=? WHERE id=?`, ojFailed, reason, id)
	slog.Error("job permanently failed", "id", id, "reason", reason)
}

// agentBackoff calcula el tiempo de espera con back-off exponencial.
// Empieza en 15 s y se duplica hasta un máximo de 30 min.
func agentBackoff(attempt int) time.Duration {
	d := 15 * time.Second
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > 30*time.Minute {
			return 30 * time.Minute
		}
	}
	return d
}

// ─── Handlers por defecto ─────────────────────────────────────────────────

func sendTicketToGateway(job OfflineJob) error {
	return postToGateway("/tickets", job.Payload)
}

func sendTelemetryToGateway(job OfflineJob) error {
	return postToGateway("/knowledge", job.Payload)
}

func postToGateway(path string, payload map[string]any) error {
	endpoint := gatewayURL + path
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", agentAPIKey)

	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 500 {
		return fmt.Errorf("gateway error: HTTP %d", res.StatusCode)
	}
	return nil
}

// EnqueueTicket encola un ticket de soporte para entrega diferida.
// Se llama cuando el gateway no está disponible en el momento del evento.
func EnqueueTicket(pcNameVal, username, message, category string) {
	if agentQueue == nil {
		return
	}
	id := fmt.Sprintf("ticket-%d", time.Now().UnixNano())
	_ = agentQueue.Enqueue(id, "ticket", map[string]any{
		"pc_name":  pcNameVal,
		"username": username,
		"message":  message,
		"category": category,
	})
}

// ════════════════════════════════════════════════════════════════════════════
//  GATEWAY SYNC — descarga rules.json/intents.json y hace hot-reload
//
//  Uso:
//    gs := newGatewaySyncStart(gatewayURL, ".", 15*time.Minute)
//    defer gs.Stop()
//
//  Si el gateway no está disponible, los errores se logean en DEBUG
//  y el agente continúa funcionando con los archivos locales.
// ════════════════════════════════════════════════════════════════════════════

// GatewaySync descarga archivos de configuración desde el gateway periódicamente.
type GatewaySync struct {
	gatewayURL string
	dataDir    string
	interval   time.Duration
	stopCh     chan struct{}
}

// newGatewaySyncStart crea e inicia inmediatamente un GatewaySync.
func newGatewaySyncStart(gURL, dataDir string, interval time.Duration) *GatewaySync {
	gs := &GatewaySync{
		gatewayURL: gURL,
		dataDir:    dataDir,
		interval:   interval,
		stopCh:     make(chan struct{}),
	}
	gs.Start()
	return gs
}

// Start lanza el loop de sincronización en background.
func (g *GatewaySync) Start() {
	go g.loop()
}

// Stop detiene la sincronización.
func (g *GatewaySync) Stop() {
	close(g.stopCh)
}

func (g *GatewaySync) loop() {
	g.sync()
	t := time.NewTicker(g.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			g.sync()
		case <-g.stopCh:
			return
		}
	}
}

func (g *GatewaySync) sync() {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, g.gatewayURL+"/updates/manifest", nil)
	if err != nil {
		return
	}
	req.Header.Set("X-API-Key", agentAPIKey)

	res, err := client.Do(req)
	if err != nil {
		slog.Debug("gateway sync failed", "error", err)
		return
	}
	defer res.Body.Close()

	var manifest struct {
		Files []struct {
			Name   string `json:"name"`
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.NewDecoder(res.Body).Decode(&manifest); err != nil {
		return
	}

	os.MkdirAll(g.dataDir, 0755)
	for _, f := range manifest.Files {
		localPath := g.dataDir + "/" + f.Name
		if f.SHA256 != "" {
			if ok, _ := fileMatchesHashAgent(localPath, f.SHA256); ok {
				continue
			}
		}
		if err := downloadFileAgent(client, f.URL, localPath, agentAPIKey); err != nil {
			slog.Warn("sync download failed", "file", f.Name, "error", err)
			continue
		}
		slog.Info("synced file", "name", f.Name)
		if f.Name == "rules.json" {
			reloadDynamicRules(localPath)
		}
	}
}

func fileMatchesHashAgent(path, expected string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	got := fmt.Sprintf("%x", h.Sum(nil))
	return got == expected, nil
}

func downloadFileAgent(client *http.Client, fileURL, dest, apiKey string) error {
	req, err := http.NewRequest(http.MethodGet, fileURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", apiKey)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, dest)
}

// reloadDynamicRules reemplaza dynamicRules desde un archivo JSON.
// Usa dynamicRulesMu para serializar con matchDynamicRule.
func reloadDynamicRules(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rs struct {
		Rules []DynamicRule `json:"rules"`
	}
	if err := json.Unmarshal(data, &rs); err != nil {
		return
	}
	dynamicRulesMu.Lock()
	dynamicRules = rs.Rules
	dynamicRulesMu.Unlock()
	slog.Info("dynamic rules hot-reloaded", "count", len(rs.Rules))
}
