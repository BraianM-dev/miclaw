// Package queue provides a SQLite-backed persistent job queue with automatic
// retries and exponential back-off.
package queue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusDone       = "done"
	StatusFailed     = "failed"
)

// Job is a single unit of work.
type Job struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Payload     map[string]any `json:"payload"`
	Status      string         `json:"status"`
	Retries     int            `json:"retries"`
	MaxRetries  int            `json:"max_retries"`
	NextRetryAt time.Time      `json:"next_retry_at"`
	CreatedAt   time.Time      `json:"created_at"`
	Error       string         `json:"error,omitempty"`
}

// Stats is a snapshot of queue counters.
type Stats struct {
	Pending    int `json:"pending"`
	Processing int `json:"processing"`
	Done       int `json:"done"`
	Failed     int `json:"failed"`
}

// Handler processes a job. Return nil to mark done.
type Handler func(job Job) error

// Queue is the persistent FIFO queue.
type Queue struct {
	db       *sql.DB
	mu       sync.Mutex
	handlers map[string]Handler
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// New opens (or creates) the queue database at path.
func New(path string) *Queue {
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
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

// Register binds a handler to a job type.
func (q *Queue) Register(jobType string, h Handler) {
	q.mu.Lock()
	q.handlers[jobType] = h
	q.mu.Unlock()
}

// StartWorker launches the background processing goroutine.
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

// Enqueue adds a new job.
func (q *Queue) Enqueue(id, jobType string, payload map[string]any, maxRetries int) error {
	raw, _ := json.Marshal(payload)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(
		`INSERT OR IGNORE INTO jobs
		 (id, type, payload, status, retries, max_retries, next_retry_at, created_at)
		 VALUES (?,?,?,?,0,?,?,?)`,
		id, jobType, string(raw), StatusPending, maxRetries, now, now,
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

func (q *Queue) process() {
	now := time.Now().UTC()
	rows, err := q.db.Query(
		`SELECT id, type, payload, status, retries, max_retries, next_retry_at, created_at, COALESCE(error,'')
		 FROM jobs WHERE status=? AND next_retry_at<=?
		 ORDER BY created_at ASC LIMIT 10`,
		StatusPending, now.Format(time.RFC3339),
	)
	if err != nil {
		slog.Error("queue poll", "error", err)
		return
	}
	var jobs []Job
	for rows.Next() {
		var j Job
		var payload, nextRetry, createdAt string
		if err := rows.Scan(&j.ID, &j.Type, &payload, &j.Status,
			&j.Retries, &j.MaxRetries, &nextRetry, &createdAt, &j.Error); err == nil {
			json.Unmarshal([]byte(payload), &j.Payload)
			j.NextRetryAt, _ = time.Parse(time.RFC3339, nextRetry)
			j.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	for _, job := range jobs {
		q.runJob(job)
	}
}

func (q *Queue) runJob(job Job) {
	q.db.Exec(`UPDATE jobs SET status=? WHERE id=?`, StatusProcessing, job.ID)
	q.mu.Lock()
	h, ok := q.handlers[job.Type]
	q.mu.Unlock()
	if !ok {
		q.markFailed(job.ID, "no handler for type: "+job.Type)
		return
	}
	err := h(job)
	if err == nil {
		q.db.Exec(`UPDATE jobs SET status=? WHERE id=?`, StatusDone, job.ID)
		return
	}
	job.Retries++
	if job.Retries >= job.MaxRetries {
		q.markFailed(job.ID, err.Error())
		return
	}
	backoff := backoffDuration(job.Retries)
	nextRetry := time.Now().UTC().Add(backoff).Format(time.RFC3339)
	q.db.Exec(
		`UPDATE jobs SET status=?, retries=?, next_retry_at=?, error=? WHERE id=?`,
		StatusPending, job.Retries, nextRetry, err.Error(), job.ID,
	)
}

func (q *Queue) markFailed(id, reason string) {
	q.db.Exec(`UPDATE jobs SET status=?, error=? WHERE id=?`, StatusFailed, reason, id)
	slog.Error("job failed permanently", "id", id, "reason", reason)
}

func (q *Queue) migrate() {
	q.db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id            TEXT PRIMARY KEY,
		type          TEXT NOT NULL,
		payload       TEXT NOT NULL DEFAULT '{}',
		status        TEXT NOT NULL DEFAULT 'pending',
		retries       INTEGER NOT NULL DEFAULT 0,
		max_retries   INTEGER NOT NULL DEFAULT 3,
		next_retry_at DATETIME NOT NULL,
		created_at    DATETIME NOT NULL,
		error         TEXT
	)`)
	q.db.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status, next_retry_at)`)
}

func backoffDuration(attempt int) time.Duration {
	d := 10 * time.Second
	for i := 0; i < attempt; i++ {
		d *= 2
		if d > time.Hour {
			return time.Hour
		}
	}
	return d
}
