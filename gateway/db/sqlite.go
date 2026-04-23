// Package db provides all database operations for the gateway.
package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// ── Structs ────────────────────────────────────────────────────────────────

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
	AgentKey string    `json:"-"`
	LastSeen time.Time `json:"last_seen"`
	Enabled  bool      `json:"enabled"`
}

type Heartbeat struct {
	ID      int64     `json:"id"`
	AgentID string    `json:"agent_id"`
	IP      string    `json:"ip"`
	CPU     float64   `json:"cpu_pct"`
	Mem     float64   `json:"mem_pct"`
	Disk    float64   `json:"disk_pct"`
	Status  string    `json:"status"`
	TS      time.Time `json:"ts"`
}

type Alert struct {
	ID      int64     `json:"id"`
	AgentID string    `json:"agent_id"`
	Level   string    `json:"level"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
	Details string    `json:"details"`
	Status  string    `json:"status"`
	TS      time.Time `json:"ts"`
}

type Command struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agent_id"`
	Command    string     `json:"command"`
	Params     string     `json:"params"`
	Status     string     `json:"status"`
	Result     string     `json:"result"`
	Requester  string     `json:"requester"`
	CreatedAt  time.Time  `json:"created_at"`
	ExecutedAt *time.Time `json:"executed_at,omitempty"`
}

type Ticket struct {
	ID        int64     `json:"id"`
	PCName    string    `json:"pc_name"`
	Username  string    `json:"username"`
	Message   string    `json:"message"`
	Category  string    `json:"category"`
	Priority  string    `json:"priority"`
	AgentID   string    `json:"agent_id"`
	Telemetry string    `json:"telemetry"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TicketMessage struct {
	ID       int64     `json:"id"`
	TicketID int64     `json:"ticket_id"`
	Author   string    `json:"author"`
	Content  string    `json:"content"`
	TS       time.Time `json:"ts"`
}

type DashboardStats struct {
	TotalAgents    int `json:"total_agents"`
	OnlineAgents   int `json:"online_agents"`
	OfflineAgents  int `json:"offline_agents"`
	OpenTickets    int `json:"open_tickets"`
	OpenAlerts     int `json:"open_alerts"`
	CriticalAlerts int `json:"critical_alerts"`
}

type GatewaySettings struct {
	GatewayName        string `json:"gateway_name"`
	Organization       string `json:"organization"`
	AlertCPUThreshold  int    `json:"alert_cpu_threshold"`
	AlertMemThreshold  int    `json:"alert_mem_threshold"`
	AlertDiskThreshold int    `json:"alert_disk_threshold"`
	AutoCloseDays      int    `json:"auto_close_days"`
	WebhookURL         string `json:"webhook_url"`
	OllamaEnabled      bool   `json:"ollama_enabled"`
	OllamaModel        string `json:"ollama_model"`
}

type Knowledge struct {
	ID        string    `json:"id"`
	Category  string    `json:"category"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ── DB ─────────────────────────────────────────────────────────────────────

type DB struct {
	conn *sql.DB
}

func New(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	conn.SetMaxOpenConns(1)
	d := &DB{conn: conn}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func (d *DB) Close() { d.conn.Close() }

// ── Agents ─────────────────────────────────────────────────────────────────

func (d *DB) UpsertAgent(a Agent) error {
	_, err := d.conn.Exec(`
		INSERT INTO agents (id,name,type,ip,port,hostname,location,gateway,status,version,agent_key,last_seen,enabled)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1)
		ON CONFLICT(id) DO UPDATE SET
		  name=excluded.name, type=excluded.type, ip=excluded.ip, port=excluded.port,
		  hostname=excluded.hostname, location=excluded.location, gateway=excluded.gateway,
		  status=excluded.status, version=excluded.version, agent_key=excluded.agent_key,
		  last_seen=excluded.last_seen`,
		a.ID, a.Name, a.Type, a.IP, a.Port, a.Hostname, a.Location, a.Gateway,
		a.Status, a.Version, a.AgentKey, a.LastSeen.UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetAgent(id string) (Agent, error) {
	row := d.conn.QueryRow(`
		SELECT id,name,type,ip,port,hostname,location,gateway,status,version,agent_key,last_seen,enabled
		FROM agents WHERE id=?`, id)
	return scanAgent(row)
}

func (d *DB) ListAgents() ([]Agent, error) {
	rows, err := d.conn.Query(`
		SELECT id,name,type,ip,port,hostname,location,gateway,status,version,agent_key,last_seen,enabled
		FROM agents ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (d *DB) SetAgentStatus(id, status string) error {
	_, err := d.conn.Exec(
		`UPDATE agents SET status=?, last_seen=? WHERE id=?`,
		status, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

func (d *DB) DeleteAgent(id string) error {
	_, err := d.conn.Exec(`DELETE FROM agents WHERE id=?`, id)
	return err
}

func scanAgent(s interface {
	Scan(...any) error
}) (Agent, error) {
	var a Agent
	var lastSeen string
	err := s.Scan(
		&a.ID, &a.Name, &a.Type, &a.IP, &a.Port, &a.Hostname,
		&a.Location, &a.Gateway, &a.Status, &a.Version, &a.AgentKey,
		&lastSeen, &a.Enabled,
	)
	if err != nil {
		return a, err
	}
	a.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
	return a, nil
}

// ── Heartbeats ─────────────────────────────────────────────────────────────

func (d *DB) InsertHeartbeat(h Heartbeat) error {
	_, err := d.conn.Exec(`
		INSERT INTO heartbeats (agent_id,ip,cpu_pct,mem_pct,disk_pct,status,ts)
		VALUES (?,?,?,?,?,?,?)`,
		h.AgentID, h.IP, h.CPU, h.Mem, h.Disk, h.Status,
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetHeartbeats(agentID string, limit int) ([]Heartbeat, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.conn.Query(`
		SELECT id,agent_id,ip,cpu_pct,mem_pct,disk_pct,status,ts
		FROM heartbeats WHERE agent_id=? ORDER BY ts DESC LIMIT ?`,
		agentID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Heartbeat
	for rows.Next() {
		var h Heartbeat
		var ts string
		if err := rows.Scan(&h.ID, &h.AgentID, &h.IP, &h.CPU, &h.Mem, &h.Disk, &h.Status, &ts); err != nil {
			return nil, err
		}
		h.TS, _ = time.Parse(time.RFC3339, ts)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (d *DB) PruneHeartbeats(maxAge time.Duration) error {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339)
	_, err := d.conn.Exec(`DELETE FROM heartbeats WHERE ts < ?`, cutoff)
	return err
}

// ── Alerts ─────────────────────────────────────────────────────────────────

func (d *DB) CreateAlert(a Alert) (int64, error) {
	res, err := d.conn.Exec(`
		INSERT INTO alerts (agent_id,level,source,message,details,status,ts)
		VALUES (?,?,?,?,?,?,?)`,
		a.AgentID, a.Level, a.Source, a.Message, a.Details,
		coalesce(a.Status, "open"), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListAlerts(level string, limit int) ([]Alert, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id,agent_id,level,source,message,details,status,ts FROM alerts`
	var args []any
	if level != "" {
		q += ` WHERE level=?`
		args = append(args, level)
	}
	q += ` ORDER BY ts DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlerts(rows)
}

func (d *DB) UpdateAlertStatus(id int64, status string) error {
	_, err := d.conn.Exec(`UPDATE alerts SET status=? WHERE id=?`, status, id)
	return err
}

func scanAlerts(rows *sql.Rows) ([]Alert, error) {
	var out []Alert
	for rows.Next() {
		var a Alert
		var ts string
		if err := rows.Scan(&a.ID, &a.AgentID, &a.Level, &a.Source, &a.Message, &a.Details, &a.Status, &ts); err != nil {
			return nil, err
		}
		a.TS, _ = time.Parse(time.RFC3339, ts)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── Commands ───────────────────────────────────────────────────────────────

func (d *DB) CreateCommand(c Command) error {
	params, _ := json.Marshal(c.Params)
	_, err := d.conn.Exec(`
		INSERT INTO commands (id,agent_id,command,params,status,result,requester,created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		c.ID, c.AgentID, c.Command, string(params), coalesce(c.Status, "pending"),
		"", c.Requester, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) GetCommand(id string) (Command, error) {
	row := d.conn.QueryRow(`
		SELECT id,agent_id,command,params,status,result,requester,created_at,executed_at
		FROM commands WHERE id=?`, id)
	return scanCommand(row)
}

func (d *DB) ListCommands(agentID string, limit int) ([]Command, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id,agent_id,command,params,status,result,requester,created_at,executed_at FROM commands`
	var args []any
	if agentID != "" {
		q += ` WHERE agent_id=?`
		args = append(args, agentID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) UpdateCommand(id, status, result string) error {
	_, err := d.conn.Exec(`
		UPDATE commands SET status=?, result=?, executed_at=? WHERE id=?`,
		status, result, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

func scanCommand(s interface {
	Scan(...any) error
}) (Command, error) {
	var c Command
	var createdAt string
	var executedAt sql.NullString
	err := s.Scan(
		&c.ID, &c.AgentID, &c.Command, &c.Params, &c.Status,
		&c.Result, &c.Requester, &createdAt, &executedAt,
	)
	if err != nil {
		return c, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if executedAt.Valid {
		t, _ := time.Parse(time.RFC3339, executedAt.String)
		c.ExecutedAt = &t
	}
	return c, nil
}

// ── Tickets ────────────────────────────────────────────────────────────────

func (d *DB) CreateTicket(t Ticket) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.conn.Exec(`
		INSERT INTO tickets (pc_name,username,message,category,priority,agent_id,telemetry,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		t.PCName, t.Username, t.Message,
		coalesce(t.Category, "general"), coalesce(t.Priority, "normal"),
		t.AgentID, t.Telemetry, coalesce(t.Status, "open"), now, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetTicket(id int64) (Ticket, error) {
	row := d.conn.QueryRow(`
		SELECT id,pc_name,username,message,category,priority,agent_id,telemetry,status,created_at,updated_at
		FROM tickets WHERE id=?`, id)
	return scanTicket(row)
}

func (d *DB) ListTickets(status string, limit int) ([]Ticket, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id,pc_name,username,message,category,priority,agent_id,telemetry,status,created_at,updated_at FROM tickets`
	var args []any
	if status != "" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) UpdateTicket(id int64, status string) error {
	_, err := d.conn.Exec(`
		UPDATE tickets SET status=?, updated_at=? WHERE id=?`,
		status, time.Now().UTC().Format(time.RFC3339), id,
	)
	return err
}

func scanTicket(s interface {
	Scan(...any) error
}) (Ticket, error) {
	var t Ticket
	var createdAt, updatedAt string
	err := s.Scan(
		&t.ID, &t.PCName, &t.Username, &t.Message, &t.Category,
		&t.Priority, &t.AgentID, &t.Telemetry, &t.Status, &createdAt, &updatedAt,
	)
	if err != nil {
		return t, err
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return t, nil
}

// ── Ticket Messages ────────────────────────────────────────────────────────

func (d *DB) AddMessage(m TicketMessage) (int64, error) {
	res, err := d.conn.Exec(`
		INSERT INTO ticket_messages (ticket_id,author,content,ts) VALUES (?,?,?,?)`,
		m.TicketID, m.Author, m.Content, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetMessages(ticketID int64) ([]TicketMessage, error) {
	rows, err := d.conn.Query(`
		SELECT id,ticket_id,author,content,ts FROM ticket_messages
		WHERE ticket_id=? ORDER BY ts ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TicketMessage
	for rows.Next() {
		var m TicketMessage
		var ts string
		if err := rows.Scan(&m.ID, &m.TicketID, &m.Author, &m.Content, &ts); err != nil {
			return nil, err
		}
		m.TS, _ = time.Parse(time.RFC3339, ts)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ── Knowledge ──────────────────────────────────────────────────────────────

func (d *DB) UpsertKnowledge(k Knowledge) error {
	_, err := d.conn.Exec(`
		INSERT INTO knowledge (id,category,content,updated_at) VALUES (?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET category=excluded.category,
		  content=excluded.content, updated_at=excluded.updated_at`,
		k.ID, k.Category, k.Content, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (d *DB) ListKnowledge(category string) ([]Knowledge, error) {
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
	var out []Knowledge
	for rows.Next() {
		var k Knowledge
		var updatedAt string
		if err := rows.Scan(&k.ID, &k.Category, &k.Content, &updatedAt); err != nil {
			return nil, err
		}
		k.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, k)
	}
	return out, rows.Err()
}

// ── Dashboard Stats ────────────────────────────────────────────────────────

func (d *DB) GetStats() (DashboardStats, error) {
	var s DashboardStats
	d.conn.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&s.TotalAgents)
	d.conn.QueryRow(`SELECT COUNT(*) FROM agents WHERE status='ok' OR status='warning'`).Scan(&s.OnlineAgents)
	d.conn.QueryRow(`SELECT COUNT(*) FROM agents WHERE status='offline'`).Scan(&s.OfflineAgents)
	d.conn.QueryRow(`SELECT COUNT(*) FROM tickets WHERE status NOT IN ('resolved','closed')`).Scan(&s.OpenTickets)
	d.conn.QueryRow(`SELECT COUNT(*) FROM alerts WHERE status='open'`).Scan(&s.OpenAlerts)
	d.conn.QueryRow(`SELECT COUNT(*) FROM alerts WHERE status='open' AND level='critical'`).Scan(&s.CriticalAlerts)
	return s, nil
}

// ── Settings ───────────────────────────────────────────────────────────────

func (d *DB) GetSettings() (GatewaySettings, error) {
	rows, err := d.conn.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return GatewaySettings{}, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		m[k] = v
	}
	return GatewaySettings{
		GatewayName:        getStr(m, "gateway_name", "MicLaw Gateway"),
		Organization:       getStr(m, "organization", "MicLaw IT"),
		AlertCPUThreshold:  getInt(m, "alert_cpu_threshold", 90),
		AlertMemThreshold:  getInt(m, "alert_mem_threshold", 90),
		AlertDiskThreshold: getInt(m, "alert_disk_threshold", 90),
		AutoCloseDays:      getInt(m, "auto_close_days", 7),
		WebhookURL:         getStr(m, "webhook_url", ""),
		OllamaEnabled:      getBool(m, "ollama_enabled", true),
		OllamaModel:        getStr(m, "ollama_model", "phi4-mini:3.8b"),
	}, nil
}

func (d *DB) SaveSettings(s GatewaySettings) error {
	vals := map[string]string{
		"gateway_name":         s.GatewayName,
		"organization":         s.Organization,
		"alert_cpu_threshold":  fmt.Sprintf("%d", s.AlertCPUThreshold),
		"alert_mem_threshold":  fmt.Sprintf("%d", s.AlertMemThreshold),
		"alert_disk_threshold": fmt.Sprintf("%d", s.AlertDiskThreshold),
		"auto_close_days":      fmt.Sprintf("%d", s.AutoCloseDays),
		"webhook_url":          s.WebhookURL,
		"ollama_enabled":       fmt.Sprintf("%t", s.OllamaEnabled),
		"ollama_model":         s.OllamaModel,
	}
	for k, v := range vals {
		if _, err := d.conn.Exec(
			`INSERT INTO settings (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			k, v,
		); err != nil {
			return err
		}
	}
	return nil
}

// ── Migration ──────────────────────────────────────────────────────────────

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			id        TEXT PRIMARY KEY,
			name      TEXT NOT NULL,
			type      TEXT NOT NULL DEFAULT 'frank',
			ip        TEXT NOT NULL,
			port      INTEGER NOT NULL DEFAULT 8081,
			hostname  TEXT NOT NULL DEFAULT '',
			location  TEXT NOT NULL DEFAULT '',
			gateway   TEXT NOT NULL DEFAULT '',
			status    TEXT NOT NULL DEFAULT 'unknown',
			version   TEXT NOT NULL DEFAULT '',
			agent_key TEXT NOT NULL DEFAULT '',
			last_seen DATETIME NOT NULL,
			enabled   INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS heartbeats (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			ip       TEXT NOT NULL DEFAULT '',
			cpu_pct  REAL NOT NULL DEFAULT 0,
			mem_pct  REAL NOT NULL DEFAULT 0,
			disk_pct REAL NOT NULL DEFAULT 0,
			status   TEXT NOT NULL DEFAULT 'ok',
			ts       DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_hb_agent_ts ON heartbeats(agent_id, ts DESC)`,
		`CREATE TABLE IF NOT EXISTS alerts (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL DEFAULT '',
			level    TEXT NOT NULL DEFAULT 'info',
			source   TEXT NOT NULL DEFAULT 'system',
			message  TEXT NOT NULL,
			details  TEXT NOT NULL DEFAULT '',
			status   TEXT NOT NULL DEFAULT 'open',
			ts       DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS commands (
			id          TEXT PRIMARY KEY,
			agent_id    TEXT NOT NULL,
			command     TEXT NOT NULL,
			params      TEXT NOT NULL DEFAULT '{}',
			status      TEXT NOT NULL DEFAULT 'pending',
			result      TEXT NOT NULL DEFAULT '',
			requester   TEXT NOT NULL DEFAULT '',
			created_at  DATETIME NOT NULL,
			executed_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS tickets (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			pc_name    TEXT NOT NULL DEFAULT '',
			username   TEXT NOT NULL DEFAULT '',
			message    TEXT NOT NULL,
			category   TEXT NOT NULL DEFAULT 'general',
			priority   TEXT NOT NULL DEFAULT 'normal',
			agent_id   TEXT NOT NULL DEFAULT '',
			telemetry  TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL DEFAULT 'open',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS ticket_messages (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			ticket_id INTEGER NOT NULL,
			author    TEXT NOT NULL,
			content   TEXT NOT NULL,
			ts        DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS knowledge (
			id         TEXT PRIMARY KEY,
			category   TEXT NOT NULL DEFAULT 'general',
			content    TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, stmt := range stmts {
		if _, err := d.conn.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Default settings
	defaults := [][]string{
		{"gateway_name", "MicLaw Gateway"},
		{"organization", "MicLaw IT"},
		{"alert_cpu_threshold", "90"},
		{"alert_mem_threshold", "90"},
		{"alert_disk_threshold", "90"},
		{"auto_close_days", "7"},
		{"webhook_url", ""},
		{"ollama_enabled", "true"},
		{"ollama_model", "phi4-mini:3.8b"},
	}
	for _, kv := range defaults {
		d.conn.Exec(`INSERT OR IGNORE INTO settings (key,value) VALUES (?,?)`, kv[0], kv[1])
	}
	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func coalesce(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func getStr(m map[string]string, k, def string) string {
	if v, ok := m[k]; ok && v != "" {
		return v
	}
	return def
}

func getInt(m map[string]string, k string, def int) int {
	if v, ok := m[k]; ok {
		var n int
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			return n
		}
	}
	return def
}

func getBool(m map[string]string, k string, def bool) bool {
	if v, ok := m[k]; ok {
		return v == "true" || v == "1"
	}
	return def
}
