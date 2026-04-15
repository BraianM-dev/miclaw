// Package queue provides a SQLite-backed persistent job queue with automatic
// retries and exponential back-off. It is designed for offline-first scenarios
// where the gateway may temporarily lose connectivity to upstream services.
package queue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ─── Types ─────────────────────────────────────────────────────────────────

// Status values
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusDone       = "done"
	StatusFailed     = "failed"
)

// Job is a single unit of work in the queue.
type Job struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Payload     map[string]any `json:"payload"`
	Status      string         `json:"status"`
	Retries     int            `json:"retries"`
	MaxRetries  int            `json:"max_retries"`
	NextRetryAt time.Time      `json:"next_retry_at"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Error       string         `json:"error,omitempty"`
}

// Stats is a snapshot of queue counters.
type Stats struct {
	Pending    int `json:"pending"`
	Processing int `json:"processing"`
	Done       int `json:"done"`
	Failed     int `json:"failed"`
}

// Handler is called when a job is dequeued. Return nil to mark it done.
type Handler func(job Job) error

// ─── Queue ─────────────────────────────────────────────────────────────────

// Queue is the persistent FIFO queue with retry logic.
type Queue struct {
	db       *sql.DB
	mu       sync.Mutex
	handlers map[string]Handler
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// New opens (or creates) the queue database at path.
func New(path string) *Queue {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		panic(fmt.Sprintf("queue: open db: %v", err))
	}
	conn.SetMaxOpenConns(1)

	q := &Queue{
		db:       conn,
		handlers: make(map[string]Handler),
		stopCh:   make(chan struct{}),
	}
	q.migrate()
	return q
}

// Close shuts down the background worker and closes the database.
func (q *Queue) Close() {
	select {
	case <-q.stopCh:
	default:
		close(q.stopCh)
	}
	q.wg.Wait()
	q.db.Close()
}

// Register binds a handler to a job type. Call before StartWorker.
func (q *Queue) Register(jobType string, h Handler) {
	q.mu.Lock()
	q.handlers[jobType] = h
	q.mu.Unlock()
}

// StartWorker launches a background goroutine that polls and processes jobs.
// pollInterval controls how often idle-poll happens (e.g. 5 seconds).
func (q *Queue) StartWorker(pollInterval time.Duration) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				q.process()
			case <-q.stopCh:
				return
			}
		}
	}()
}

// Enqueue adds a new job to the queue.
func (q *Queue) Enqueue(id, jobType string, payload map[string]any, maxRetries int) error {
	raw, _ := json.Marshal(payload)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(
		`INSERT OR IGNORE INTO jobs
		 (id, type, payload, status, retries, max_retries, next_retry_at, created_at, updated_at)
		 VALUES (?,?,?,?,0,?,?,?,?)`,
		id, jobType, string(raw), StatusPending, maxRetries, now, now, now,
	)
	if err != nil {
		return fmt.Errorf("queue enqueue: %w", err)
	}
	slog.Debug("job enqueued", "id", id, "type", jobType)
	return nil
}

// Stats returns current queue counters.
func (q *Queue) Stats() Stats {
	var s Stats
	q.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status=?`, StatusPending).Scan(&s.Pending)
	q.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status=?`, StatusProcessing).Scan(&s.Processing)
	q.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status=?`, StatusDone).Scan(&s.Done)
	q.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status=?`, StatusFailed).Scan(&s.Failed)
	return s
}

// PendingJobs returns all jobs currently in pending/processing state.
func (q *Queue) PendingJobs() ([]Job, error) {
	rows, err := q.db.Query(
		`SELECT id, type, payload, status, retries, max_retries, next_retry_at, created_at, updated_at, COALESCE(error,'')
		 FROM jobs WHERE status IN (?,?) ORDER BY created_at ASC`,
		StatusPending, StatusProcessing,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// ─── internal ──────────────────────────────────────────────────────────────

func (q *Queue) process() {
	now := time.Now().UTC()
	rows, err := q.db.Query(
		`SELECT id, type, payload, status, retries, max_retries, next_retry_at, created_at, updated_at, COALESCE(error,'')
		 FROM jobs
		 WHERE status = ? AND next_retry_at <= ?
		 ORDER BY created_at ASC
		 LIMIT 10`,
		StatusPending, now.Format(time.RFC3339),
	)
	if err != nil {
		slog.Error("queue poll", "error", err)
		return
	}
	jobs, err := scanJobs(rows)
	rows.Close()
	if err != nil {
		slog.Error("queue scan", "error", err)
		return
	}

	for _, job := range jobs {
		q.runJob(job)
	}
}

func (q *Queue) runJob(job Job) {
	// Mark processing
	q.db.Exec(`UPDATE jobs SET status=?, updated_at=? WHERE id=?`,
		StatusProcessing, time.Now().UTC().Format(time.RFC3339), job.ID)

	q.mu.Lock()
	h, ok := q.handlers[job.Type]
	q.mu.Unlock()

	if !ok {
		slog.Warn("no handler for job type", "type", job.Type, "id", job.ID)
		q.markFailed(job.ID, "no handler registered for type: "+job.Type)
		return
	}

	err := h(job)
	if err == nil {
		q.db.Exec(`UPDATE jobs SET status=?, updated_at=? WHERE id=?`,
			StatusDone, time.Now().UTC().Format(time.RFC3339), job.ID)
		slog.Info("job done", "id", job.ID, "type", job.Type)
		return
	}

	slog.Warn("job failed", "id", job.ID, "type", job.Type, "retries", job.Retries, "error", err)

	job.Retries++
	if job.Retries >= job.MaxRetries {
		q.markFailed(job.ID, err.Error())
		return
	}

	// Exponential back-off: 2^retries * 10s, capped at 1h
	backoff := backoffDuration(job.Retries)
	nextRetry := time.Now().UTC().Add(backoff).Format(time.RFC3339)
	q.db.Exec(
		`UPDATE jobs SET status=?, retries=?, next_retry_at=?, error=?, updated_at=? WHERE id=?`,
		StatusPending, job.Retries, nextRetry, err.Error(),
		time.Now().UTC().Format(time.RFC3339), job.ID,
	)
	slog.Info("job scheduled for retry", "id", job.ID, "backoff", backoff, "retry", job.Retries)
}

func (q *Queue) markFailed(id, reason string) {
	q.db.Exec(
		`UPDATE jobs SET status=?, error=?, updated_at=? WHERE id=?`,
		StatusFailed, reason, time.Now().UTC().Format(time.RFC3339), id,
	)
	slog.Error("job permanently failed", "id", id, "reason", reason)
}

func (q *Queue) migrate() {
	q.db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id           TEXT PRIMARY KEY,
		type         TEXT NOT NULL,
		payload      TEXT NOT NULL DEFAULT '{}',
		status       TEXT NOT NULL DEFAULT 'pending',
		retries      INTEGER NOT NULL DEFAULT 0,
		max_retries  INTEGER NOT NULL DEFAULT 3,
		next_retry_at DATETIME NOT NULL,
		created_at   DATETIME NOT NULL,
		updated_at   DATETIME NOT NULL,
		error        TEXT
	)`)
	q.db.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_status_retry ON jobs(status, next_retry_at)`)
}

func scanJobs(rows *sql.Rows) ([]Job, error) {
	var out []Job
	for rows.Next() {
		var j Job
		var payload, nextRetry, createdAt, updatedAt string
		if err := rows.Scan(&j.ID, &j.Type, &payload, &j.Status,
			&j.Retries, &j.MaxRetries, &nextRetry, &createdAt, &updatedAt, &j.Error); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payload), &j.Payload)
		j.NextRetryAt, _ = time.Parse(time.RFC3339, nextRetry)
		j.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		j.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, j)
	}
	return out, rows.Err()
}

// backoffDuration returns exponential back-off capped at 1 hour.
func backoffDuration(attempt int) time.Duration {
	base := 10 * time.Second
	d := base
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > time.Hour {
			return time.Hour
		}
	}
	return d
}
