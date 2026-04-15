package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DB wraps sql.DB and owns all schema migrations.
type DB struct {
	conn *sql.DB
}

// New opens (or creates) the SQLite database at path and runs migrations.
func New(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite is single-writer
	conn.SetMaxIdleConns(1)
	d := &DB{conn: conn}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("db migrate: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error { return d.conn.Close() }

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			ip TEXT NOT NULL,
			port INTEGER NOT NULL,
			last_seen DATETIME NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS tickets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pc_name TEXT NOT NULL,
			username TEXT NOT NULL,
			message TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'general',
			telemetry TEXT,
			status TEXT NOT NULL DEFAULT 'open',
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS actions (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			command TEXT NOT NULL,
			parameters TEXT,
			rollback_command TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			pre_telemetry TEXT,
			post_telemetry TEXT,
			result TEXT,
			requester TEXT,
			created_at DATETIME NOT NULL,
			approved_at DATETIME,
			executed_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS permissions (
			command TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			mode TEXT NOT NULL,
			PRIMARY KEY (command, agent_id)
		)`,
		`CREATE TABLE IF NOT EXISTS ai_cache (
			key TEXT PRIMARY KEY,
			response TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge (
			id TEXT PRIMARY KEY,
			category TEXT NOT NULL,
			content TEXT NOT NULL,
			embedding TEXT,
			updated_at DATETIME NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := d.conn.Exec(s); err != nil {
			return fmt.Errorf("migrate stmt: %w\nSQL: %s", err, s)
		}
	}
	return nil
}

// ─── Agents ────────────────────────────────────────────────────────────────

type Agent struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	IP       string    `json:"ip"`
	Port     int       `json:"port"`
	LastSeen time.Time `json:"last_seen"`
	Enabled  bool      `json:"enabled"`
}

func (d *DB) UpsertAgent(a Agent) error {
	_, err := d.conn.Exec(
		`INSERT INTO agents (id,name,type,ip,port,last_seen,enabled)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, type=excluded.type, ip=excluded.ip,
		   port=excluded.port, last_seen=excluded.last_seen, enabled=excluded.enabled`,
		a.ID, a.Name, a.Type, a.IP, a.Port, a.LastSeen.UTC().Format(time.RFC3339), btoi(a.Enabled),
	)
	return err
}

func (d *DB) GetAgent(id string) (Agent, bool) {
	row := d.conn.QueryRow(
		`SELECT id,name,type,ip,port,last_seen,enabled FROM agents WHERE id=?`, id)
	return scanAgent(row)
}

func (d *DB) ListAgents() ([]Agent, error) {
	rows, err := d.conn.Query(`SELECT id,name,type,ip,port,last_seen,enabled FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, ok := scanAgent(rows)
		if ok {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAgent(s scanner) (Agent, bool) {
	var a Agent
	var lastSeen string
	var enabled int
	if err := s.Scan(&a.ID, &a.Name, &a.Type, &a.IP, &a.Port, &lastSeen, &enabled); err != nil {
		return a, false
	}
	a.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
	a.Enabled = enabled == 1
	return a, true
}

// ─── Tickets ───────────────────────────────────────────────────────────────

type Ticket struct {
	ID        int64     `json:"id"`
	PCName    string    `json:"pc_name"`
	Username  string    `json:"username"`
	Message   string    `json:"message"`
	Category  string    `json:"category"`
	Telemetry string    `json:"telemetry,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *DB) InsertTicket(t Ticket) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO tickets (pc_name,username,message,category,telemetry,status,created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		t.PCName, t.Username, t.Message, t.Category, t.Telemetry, t.Status,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListTickets(limit int) ([]Ticket, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.conn.Query(
		`SELECT id,pc_name,username,message,category,COALESCE(telemetry,''),status,created_at
		 FROM tickets ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ticket
	for rows.Next() {
		var t Ticket
		var ts string
		if err := rows.Scan(&t.ID, &t.PCName, &t.Username, &t.Message,
			&t.Category, &t.Telemetry, &t.Status, &ts); err == nil {
			t.CreatedAt, _ = time.Parse(time.RFC3339, ts)
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// ─── Actions ───────────────────────────────────────────────────────────────

type Action struct {
	ID             string     `json:"id"`
	AgentID        string     `json:"agent_id"`
	Command        string     `json:"command"`
	Parameters     string     `json:"parameters"`
	RollbackCmd    string     `json:"rollback_command"`
	Status         string     `json:"status"`
	PreTelemetry   string     `json:"pre_telemetry"`
	PostTelemetry  string     `json:"post_telemetry"`
	Result         string     `json:"result"`
	Requester      string     `json:"requester"`
	CreatedAt      time.Time  `json:"created_at"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	ExecutedAt     *time.Time `json:"executed_at,omitempty"`
}

func (d *DB) InsertAction(a Action) error {
	_, err := d.conn.Exec(
		`INSERT INTO actions (id,agent_id,command,parameters,rollback_command,status,
		  pre_telemetry,post_telemetry,result,requester,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.AgentID, a.Command, a.Parameters, a.RollbackCmd, a.Status,
		a.PreTelemetry, a.PostTelemetry, a.Result, a.Requester,
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) UpdateActionStatus(id, status, result string) error {
	_, err := d.conn.Exec(
		`UPDATE actions SET status=?, result=?, executed_at=? WHERE id=?`,
		status, result, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

// ─── AI Cache ──────────────────────────────────────────────────────────────

func (d *DB) GetCachedAI(key string, ttl time.Duration) (string, bool) {
	var resp, createdAt string
	err := d.conn.QueryRow(`SELECT response, created_at FROM ai_cache WHERE key=?`, key).
		Scan(&resp, &createdAt)
	if err != nil {
		return "", false
	}
	t, _ := time.Parse(time.RFC3339, createdAt)
	if time.Since(t) > ttl {
		_, _ = d.conn.Exec(`DELETE FROM ai_cache WHERE key=?`, key)
		return "", false
	}
	return resp, true
}

func (d *DB) SetCachedAI(key, response string) error {
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO ai_cache (key,response,created_at) VALUES (?,?,?)`,
		key, response, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// ─── Knowledge ─────────────────────────────────────────────────────────────

type KnowledgeEntry struct {
	ID        string    `json:"id"`
	Category  string    `json:"category"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (d *DB) ListKnowledge(category string) ([]KnowledgeEntry, error) {
	q := `SELECT id,category,content,updated_at FROM knowledge`
	args := []any{}
	if category != "" {
		q += ` WHERE category=?`
		args = append(args, category)
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := d.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeEntry
	for rows.Next() {
		var e KnowledgeEntry
		var ts string
		if err := rows.Scan(&e.ID, &e.Category, &e.Content, &ts); err == nil {
			e.UpdatedAt, _ = time.Parse(time.RFC3339, ts)
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

func (d *DB) UpsertKnowledge(e KnowledgeEntry) error {
	_, err := d.conn.Exec(
		`INSERT INTO knowledge (id,category,content,updated_at)
		 VALUES (?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   category=excluded.category, content=excluded.content, updated_at=excluded.updated_at`,
		e.ID, e.Category, e.Content, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// ─── helpers ───────────────────────────────────────────────────────────────

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
