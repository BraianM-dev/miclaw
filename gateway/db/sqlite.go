package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite, sin CGO
)

// DB wraps sql.DB y centraliza todas las migraciones y queries.
type DB struct {
	conn *sql.DB
}

// New abre (o crea) la base de datos SQLite y corre las migraciones.
func New(path string) (*DB, error) {
	// modernc.org/sqlite registra el driver como "sqlite" (no "sqlite3").
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite es single-writer
	conn.SetMaxIdleConns(1)
	d := &DB{conn: conn}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("db migrate: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error { return d.conn.Close() }

// tryAddColumn agrega una columna si no existe (SQLite no soporta IF NOT EXISTS en ALTER).
func (d *DB) tryAddColumn(table, colDef string) {
	_, _ = d.conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, colDef))
}

func (d *DB) migrate() error {
	stmts := []string{
		// ── Agentes ──────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS agents (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			type        TEXT NOT NULL DEFAULT 'frank',
			ip          TEXT NOT NULL,
			port        INTEGER NOT NULL DEFAULT 8081,
			hostname    TEXT NOT NULL DEFAULT '',
			location    TEXT NOT NULL DEFAULT '',
			gateway     TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'unknown',
			version     TEXT NOT NULL DEFAULT '',
			agent_key   TEXT NOT NULL DEFAULT '',
			last_seen   DATETIME NOT NULL,
			enabled     INTEGER NOT NULL DEFAULT 1
		)`,

		// ── Heartbeats (historial de métricas por agente) ─────────────────
		`CREATE TABLE IF NOT EXISTS heartbeats (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			ip       TEXT NOT NULL,
			cpu_pct  REAL NOT NULL DEFAULT 0,
			mem_pct  REAL NOT NULL DEFAULT 0,
			disk_pct REAL NOT NULL DEFAULT 0,
			status   TEXT NOT NULL DEFAULT 'ok',
			ts       DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_heartbeats_agent ON heartbeats(agent_id, ts DESC)`,

		// ── Alertas (eventos sospechosos de agentes / Wazuh) ─────────────
		`CREATE TABLE IF NOT EXISTS alerts (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			level    TEXT NOT NULL DEFAULT 'info',
			source   TEXT NOT NULL DEFAULT 'agent',
			message  TEXT NOT NULL,
			details  TEXT NOT NULL DEFAULT '',
			status   TEXT NOT NULL DEFAULT 'open',
			ts       DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_ts ON alerts(ts DESC)`,

		// ── Comandos remotos (gateway → agente) ─────────────────────────
		`CREATE TABLE IF NOT EXISTS commands (
			id          TEXT PRIMARY KEY,
			agent_id    TEXT NOT NULL,
			command     TEXT NOT NULL,
			params      TEXT NOT NULL DEFAULT '{}',
			status      TEXT NOT NULL DEFAULT 'pending',
			result      TEXT NOT NULL DEFAULT '',
			requester   TEXT NOT NULL DEFAULT 'ui',
			created_at  DATETIME NOT NULL,
			executed_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_commands_agent ON commands(agent_id, created_at DESC)`,

		// ── Tickets ───────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS tickets (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			pc_name    TEXT NOT NULL,
			username   TEXT NOT NULL,
			message    TEXT NOT NULL,
			category   TEXT NOT NULL DEFAULT 'general',
			priority   TEXT NOT NULL DEFAULT 'normal',
			agent_id   TEXT NOT NULL DEFAULT '',
			telemetry  TEXT,
			status     TEXT NOT NULL DEFAULT 'open',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tickets_status ON tickets(status, created_at DESC)`,

		// ── Mensajes de ticket (comentarios / historial) ─────────────────
		`CREATE TABLE IF NOT EXISTS ticket_messages (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			ticket_id INTEGER NOT NULL,
			author    TEXT NOT NULL,
			content   TEXT NOT NULL,
			ts        DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ticket_msg ON ticket_messages(ticket_id, ts)`,

		// ── Tabla actions (legacy — preservada por compatibilidad) ────────
		`CREATE TABLE IF NOT EXISTS actions (
			id               TEXT PRIMARY KEY,
			agent_id         TEXT NOT NULL,
			command          TEXT NOT NULL,
			parameters       TEXT,
			rollback_command TEXT,
			status           TEXT NOT NULL DEFAULT 'pending',
			pre_telemetry    TEXT,
			post_telemetry   TEXT,
			result           TEXT,
			requester        TEXT,
			created_at       DATETIME NOT NULL,
			approved_at      DATETIME,
			executed_at      DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS permissions (
			command  TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			mode     TEXT NOT NULL,
			PRIMARY KEY (command, agent_id)
		)`,
		`CREATE TABLE IF NOT EXISTS ai_cache (
			key        TEXT PRIMARY KEY,
			response   TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge (
			id         TEXT PRIMARY KEY,
			category   TEXT NOT NULL,
			content    TEXT NOT NULL,
			embedding  TEXT,
			updated_at DATETIME NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := d.conn.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, s)
		}
	}

	// Columnas nuevas en tabla agents (backward compat — ignora si ya existen).
	d.tryAddColumn("agents", "hostname    TEXT NOT NULL DEFAULT ''")
	d.tryAddColumn("agents", "location    TEXT NOT NULL DEFAULT ''")
	d.tryAddColumn("agents", "gateway     TEXT NOT NULL DEFAULT ''")
	d.tryAddColumn("agents", "status      TEXT NOT NULL DEFAULT 'unknown'")
	d.tryAddColumn("agents", "version     TEXT NOT NULL DEFAULT ''")
	d.tryAddColumn("agents", "agent_key   TEXT NOT NULL DEFAULT ''")

	// Columnas nuevas en tabla tickets.
	d.tryAddColumn("tickets", "priority   TEXT NOT NULL DEFAULT 'normal'")
	d.tryAddColumn("tickets", "agent_id   TEXT NOT NULL DEFAULT ''")
	d.tryAddColumn("tickets", "updated_at DATETIME NOT NULL DEFAULT ''")

	return nil
}

// ─── Agents ────────────────────────────────────────────────────────────────

// Agent representa un agente Frank registrado en el gateway.
type Agent struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	IP       string    `json:"ip"`
	Port     int       `json:"port"`
	Hostname string    `json:"hostname"`
	Location string    `json:"location"`
	Gateway  string    `json:"gateway"`
	Status   string    `json:"status"`
	Version  string    `json:"version"`
	AgentKey string    `json:"agent_key,omitempty"` // no exponemos en la API pública
	LastSeen time.Time `json:"last_seen"`
	Enabled  bool      `json:"enabled"`
}

// AgentPublic es la vista pública del agente (sin agent_key).
type AgentPublic struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	IP       string    `json:"ip"`
	Port     int       `json:"port"`
	Hostname string    `json:"hostname"`
	Location string    `json:"location"`
	Gateway  string    `json:"gateway"`
	Status   string    `json:"status"`
	Version  string    `json:"version"`
	LastSeen time.Time `json:"last_seen"`
	Enabled  bool      `json:"enabled"`
}

func (a Agent) Public() AgentPublic {
	return AgentPublic{
		ID: a.ID, Name: a.Name, Type: a.Type,
		IP: a.IP, Port: a.Port, Hostname: a.Hostname,
		Location: a.Location, Gateway: a.Gateway,
		Status: a.Status, Version: a.Version,
		LastSeen: a.LastSeen, Enabled: a.Enabled,
	}
}

func (d *DB) UpsertAgent(a Agent) error {
	_, err := d.conn.Exec(
		`INSERT INTO agents
		 (id,name,type,ip,port,hostname,location,gateway,status,version,agent_key,last_seen,enabled)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, type=excluded.type, ip=excluded.ip, port=excluded.port,
		   hostname=excluded.hostname, location=excluded.location, gateway=excluded.gateway,
		   status=excluded.status, version=excluded.version,
		   agent_key=CASE WHEN excluded.agent_key='' THEN agent_key ELSE excluded.agent_key END,
		   last_seen=excluded.last_seen, enabled=excluded.enabled`,
		a.ID, a.Name, a.Type, a.IP, a.Port,
		a.Hostname, a.Location, a.Gateway, a.Status, a.Version, a.AgentKey,
		a.LastSeen.UTC().Format(time.RFC3339), btoi(a.Enabled),
	)
	return err
}

func (d *DB) GetAgent(id string) (Agent, bool) {
	row := d.conn.QueryRow(
		`SELECT id,name,type,ip,port,hostname,location,gateway,status,version,agent_key,last_seen,enabled
		 FROM agents WHERE id=?`, id)
	return scanAgent(row)
}

func (d *DB) ListAgents() ([]Agent, error) {
	rows, err := d.conn.Query(
		`SELECT id,name,type,ip,port,hostname,location,gateway,status,version,agent_key,last_seen,enabled
		 FROM agents ORDER BY name`)
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

func (d *DB) UpdateAgentStatus(id, status string) error {
	_, err := d.conn.Exec(
		`UPDATE agents SET status=? WHERE id=?`, status, id)
	return err
}

func (d *DB) UpdateAgentHeartbeat(id, status string, lastSeen time.Time) error {
	_, err := d.conn.Exec(
		`UPDATE agents SET status=?, last_seen=? WHERE id=?`,
		status, lastSeen.UTC().Format(time.RFC3339), id)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scanAgent(s scanner) (Agent, bool) {
	var a Agent
	var lastSeen string
	var enabled int
	if err := s.Scan(&a.ID, &a.Name, &a.Type, &a.IP, &a.Port,
		&a.Hostname, &a.Location, &a.Gateway, &a.Status, &a.Version,
		&a.AgentKey, &lastSeen, &enabled); err != nil {
		return a, false
	}
	a.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
	a.Enabled = enabled == 1
	return a, true
}

// ─── Heartbeats ────────────────────────────────────────────────────────────

// Heartbeat representa una muestra de métricas de un agente.
type Heartbeat struct {
	ID      int64     `json:"id"`
	AgentID string    `json:"agent_id"`
	IP      string    `json:"ip"`
	CPUPct  float64   `json:"cpu_pct"`
	MemPct  float64   `json:"mem_pct"`
	DiskPct float64   `json:"disk_pct"`
	Status  string    `json:"status"`
	TS      time.Time `json:"ts"`
}

func (d *DB) InsertHeartbeat(h Heartbeat) error {
	_, err := d.conn.Exec(
		`INSERT INTO heartbeats (agent_id,ip,cpu_pct,mem_pct,disk_pct,status,ts)
		 VALUES (?,?,?,?,?,?,?)`,
		h.AgentID, h.IP, h.CPUPct, h.MemPct, h.DiskPct, h.Status,
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// RecentHeartbeats devuelve las últimas N muestras de un agente.
func (d *DB) RecentHeartbeats(agentID string, n int) ([]Heartbeat, error) {
	rows, err := d.conn.Query(
		`SELECT id,agent_id,ip,cpu_pct,mem_pct,disk_pct,status,ts
		 FROM heartbeats WHERE agent_id=? ORDER BY ts DESC LIMIT ?`, agentID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Heartbeat
	for rows.Next() {
		var hb Heartbeat
		var ts string
		if err := rows.Scan(&hb.ID, &hb.AgentID, &hb.IP,
			&hb.CPUPct, &hb.MemPct, &hb.DiskPct, &hb.Status, &ts); err == nil {
			hb.TS, _ = time.Parse(time.RFC3339, ts)
			out = append(out, hb)
		}
	}
	return out, rows.Err()
}

// PruneHeartbeats elimina heartbeats con más de maxAge de antigüedad.
func (d *DB) PruneHeartbeats(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339)
	_, err := d.conn.Exec(`DELETE FROM heartbeats WHERE ts < ?`, cutoff)
	return err
}

// ─── Alerts ────────────────────────────────────────────────────────────────

// Alert es un evento de seguridad o sistema enviado por un agente.
type Alert struct {
	ID      int64     `json:"id"`
	AgentID string    `json:"agent_id"`
	Level   string    `json:"level"`   // "info" | "warning" | "critical"
	Source  string    `json:"source"`  // "wazuh" | "agent" | "system"
	Message string    `json:"message"`
	Details string    `json:"details"`
	Status  string    `json:"status"`  // "open" | "ack" | "resolved"
	TS      time.Time `json:"ts"`
}

func (d *DB) InsertAlert(a Alert) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO alerts (agent_id,level,source,message,details,status,ts)
		 VALUES (?,?,?,?,?,?,?)`,
		a.AgentID, a.Level, a.Source, a.Message, a.Details, a.Status,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListAlerts(limit int, levelFilter string) ([]Alert, error) {
	q := `SELECT id,agent_id,level,source,message,details,status,ts FROM alerts`
	var args []any
	if levelFilter != "" {
		q += ` WHERE level=?`
		args = append(args, levelFilter)
	}
	q += ` ORDER BY ts DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var ts string
		if err := rows.Scan(&a.ID, &a.AgentID, &a.Level, &a.Source,
			&a.Message, &a.Details, &a.Status, &ts); err == nil {
			a.TS, _ = time.Parse(time.RFC3339, ts)
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

func (d *DB) AckAlert(id int64, status string) error {
	_, err := d.conn.Exec(`UPDATE alerts SET status=? WHERE id=?`, status, id)
	return err
}

// ─── Remote Commands ───────────────────────────────────────────────────────

// Command representa un comando enviado desde el gateway a un agente.
type Command struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agent_id"`
	Command    string     `json:"command"`
	Params     string     `json:"params"`
	Status     string     `json:"status"`  // "pending" | "sent" | "done" | "failed" | "timeout"
	Result     string     `json:"result"`
	Requester  string     `json:"requester"`
	CreatedAt  time.Time  `json:"created_at"`
	ExecutedAt *time.Time `json:"executed_at,omitempty"`
}

func (d *DB) InsertCommand(c Command) error {
	_, err := d.conn.Exec(
		`INSERT INTO commands (id,agent_id,command,params,status,result,requester,created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		c.ID, c.AgentID, c.Command, c.Params, c.Status, c.Result, c.Requester,
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetCommand(id string) (Command, bool) {
	var c Command
	var createdAt string
	var executedAt sql.NullString
	err := d.conn.QueryRow(
		`SELECT id,agent_id,command,params,status,result,requester,created_at,executed_at
		 FROM commands WHERE id=?`, id).
		Scan(&c.ID, &c.AgentID, &c.Command, &c.Params, &c.Status,
			&c.Result, &c.Requester, &createdAt, &executedAt)
	if err != nil {
		return c, false
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if executedAt.Valid {
		t, _ := time.Parse(time.RFC3339, executedAt.String)
		c.ExecutedAt = &t
	}
	return c, true
}

func (d *DB) UpdateCommandResult(id, status, result string) error {
	_, err := d.conn.Exec(
		`UPDATE commands SET status=?, result=?, executed_at=? WHERE id=?`,
		status, result, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

func (d *DB) ListCommands(agentID string, limit int) ([]Command, error) {
	var rows *sql.Rows
	var err error
	if agentID != "" {
		rows, err = d.conn.Query(
			`SELECT id,agent_id,command,params,status,result,requester,created_at,executed_at
			 FROM commands WHERE agent_id=? ORDER BY created_at DESC LIMIT ?`, agentID, limit)
	} else {
		rows, err = d.conn.Query(
			`SELECT id,agent_id,command,params,status,result,requester,created_at,executed_at
			 FROM commands ORDER BY created_at DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		var c Command
		var createdAt string
		var executedAt sql.NullString
		if err := rows.Scan(&c.ID, &c.AgentID, &c.Command, &c.Params,
			&c.Status, &c.Result, &c.Requester, &createdAt, &executedAt); err == nil {
			c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
			if executedAt.Valid {
				t, _ := time.Parse(time.RFC3339, executedAt.String)
				c.ExecutedAt = &t
			}
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// ─── Tickets ───────────────────────────────────────────────────────────────

// Ticket representa un ticket de soporte IT.
type Ticket struct {
	ID        int64     `json:"id"`
	PCName    string    `json:"pc_name"`
	Username  string    `json:"username"`
	Message   string    `json:"message"`
	Category  string    `json:"category"`
	Priority  string    `json:"priority"`
	AgentID   string    `json:"agent_id"`
	Telemetry string    `json:"telemetry,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (d *DB) InsertTicket(t Ticket) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.conn.Exec(
		`INSERT INTO tickets
		 (pc_name,username,message,category,priority,agent_id,telemetry,status,created_at,updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		t.PCName, t.Username, t.Message, t.Category, t.Priority,
		t.AgentID, t.Telemetry, t.Status, now, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetTicket(id int64) (Ticket, bool) {
	row := d.conn.QueryRow(
		`SELECT id,pc_name,username,message,category,COALESCE(priority,'normal'),
		        COALESCE(agent_id,''),COALESCE(telemetry,''),status,created_at,COALESCE(updated_at,created_at)
		 FROM tickets WHERE id=?`, id)
	return scanTicket(row)
}

func (d *DB) ListTickets(limit int) ([]Ticket, error) {
	rows, err := d.conn.Query(
		`SELECT id,pc_name,username,message,category,COALESCE(priority,'normal'),
		        COALESCE(agent_id,''),COALESCE(telemetry,''),status,created_at,COALESCE(updated_at,created_at)
		 FROM tickets ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ticket
	for rows.Next() {
		t, ok := scanTicket(rows)
		if ok {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func (d *DB) UpdateTicketStatus(id int64, status string) error {
	_, err := d.conn.Exec(
		`UPDATE tickets SET status=?, updated_at=? WHERE id=?`,
		status, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func scanTicket(s scanner) (Ticket, bool) {
	var t Ticket
	var ca, ua string
	if err := s.Scan(&t.ID, &t.PCName, &t.Username, &t.Message,
		&t.Category, &t.Priority, &t.AgentID, &t.Telemetry,
		&t.Status, &ca, &ua); err != nil {
		return t, false
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, ca)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, ua)
	return t, true
}

// ─── Ticket Messages ───────────────────────────────────────────────────────

// TicketMessage es un comentario en un ticket.
type TicketMessage struct {
	ID       int64     `json:"id"`
	TicketID int64     `json:"ticket_id"`
	Author   string    `json:"author"`
	Content  string    `json:"content"`
	TS       time.Time `json:"ts"`
}

func (d *DB) InsertTicketMessage(m TicketMessage) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO ticket_messages (ticket_id,author,content,ts) VALUES (?,?,?,?)`,
		m.TicketID, m.Author, m.Content, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListTicketMessages(ticketID int64) ([]TicketMessage, error) {
	rows, err := d.conn.Query(
		`SELECT id,ticket_id,author,content,ts FROM ticket_messages
		 WHERE ticket_id=? ORDER BY ts`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TicketMessage
	for rows.Next() {
		var m TicketMessage
		var ts string
		if err := rows.Scan(&m.ID, &m.TicketID, &m.Author, &m.Content, &ts); err == nil {
			m.TS, _ = time.Parse(time.RFC3339, ts)
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

// ─── Actions (legacy — preservado) ────────────────────────────────────────

type Action struct {
	ID            string     `json:"id"`
	AgentID       string     `json:"agent_id"`
	Command       string     `json:"command"`
	Parameters    string     `json:"parameters"`
	RollbackCmd   string     `json:"rollback_command"`
	Status        string     `json:"status"`
	PreTelemetry  string     `json:"pre_telemetry"`
	PostTelemetry string     `json:"post_telemetry"`
	Result        string     `json:"result"`
	Requester     string     `json:"requester"`
	CreatedAt     time.Time  `json:"created_at"`
	ApprovedAt    *time.Time `json:"approved_at,omitempty"`
	ExecutedAt    *time.Time `json:"executed_at,omitempty"`
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
	err := d.conn.QueryRow(
		`SELECT response, created_at FROM ai_cache WHERE key=?`, key).
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
	var args []any
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
		`INSERT INTO knowledge (id,category,content,updated_at) VALUES (?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   category=excluded.category, content=excluded.content, updated_at=excluded.updated_at`,
		e.ID, e.Category, e.Content, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// ─── Stats ─────────────────────────────────────────────────────────────────

// DashboardStats agrega métricas para el dashboard del frontend.
type DashboardStats struct {
	TotalAgents   int `json:"total_agents"`
	OnlineAgents  int `json:"online_agents"`
	OfflineAgents int `json:"offline_agents"`
	OpenTickets   int `json:"open_tickets"`
	OpenAlerts    int `json:"open_alerts"`
	CriticalAlerts int `json:"critical_alerts"`
}

func (d *DB) GetDashboardStats() (DashboardStats, error) {
	var s DashboardStats
	_ = d.conn.QueryRow(`SELECT COUNT(*) FROM agents WHERE enabled=1`).Scan(&s.TotalAgents)
	_ = d.conn.QueryRow(`SELECT COUNT(*) FROM agents WHERE enabled=1 AND status='ok'`).Scan(&s.OnlineAgents)
	_ = d.conn.QueryRow(`SELECT COUNT(*) FROM agents WHERE enabled=1 AND status='offline'`).Scan(&s.OfflineAgents)
	_ = d.conn.QueryRow(`SELECT COUNT(*) FROM tickets WHERE status='open'`).Scan(&s.OpenTickets)
	_ = d.conn.QueryRow(`SELECT COUNT(*) FROM alerts WHERE status='open'`).Scan(&s.OpenAlerts)
	_ = d.conn.QueryRow(`SELECT COUNT(*) FROM alerts WHERE status='open' AND level='critical'`).Scan(&s.CriticalAlerts)
	return s, nil
}

// ─── helpers ───────────────────────────────────────────────────────────────

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SanitizeID construye un ID estable para un agente desde nombre+IP.
func SanitizeID(name, ip string) string {
	return strings.ReplaceAll(name, " ", "_") + "-" + ip
}
