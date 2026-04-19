package ollama

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Config holds all tuning knobs for the Ollama client.
type Config struct {
	BaseURL   string
	Model     string
	Timeout   time.Duration
	RPM       int           // max requests per minute (rate limit)
	CacheSize int           // max cached entries (LRU)
	CacheTTL  time.Duration // how long a cached response is valid
}

// Client is a thread-safe Ollama API client with caching and rate limiting.
type Client struct {
	cfg     Config
	http    *http.Client
	limiter *rateLimiter
	cache   *lruCache
}

// NewClient creates a ready-to-use Client.
func NewClient(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.RPM <= 0 {
		cfg.RPM = 20
	}
	if cfg.CacheSize <= 0 {
		cfg.CacheSize = 128
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 10 * time.Minute
	}
	return &Client{
		cfg:     cfg,
		http:    &http.Client{Timeout: cfg.Timeout},
		limiter: newRateLimiter(cfg.RPM),
		cache:   newLRU(cfg.CacheSize, cfg.CacheTTL),
	}
}

// ─── Public API ────────────────────────────────────────────────────────────

// ChatRequest is the payload for a chat completion.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Format   string    `json:"format,omitempty"`
}

// Message is a single chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse is the response from /api/chat.
type ChatResponse struct {
	Model     string  `json:"model"`
	Message   Message `json:"message"`
	Done      bool    `json:"done"`
	CreatedAt string  `json:"created_at"`
}

// Chat sends a multi-turn conversation. Returns the assistant reply.
func (c *Client) Chat(ctx context.Context, msgs []Message) (string, error) {
	return c.chat(ctx, msgs, "")
}

// ChatJSON sends a conversation expecting a JSON-formatted reply.
func (c *Client) ChatJSON(ctx context.Context, msgs []Message) (string, error) {
	return c.chat(ctx, msgs, "json")
}

// Generate is a convenience wrapper for single-prompt queries with caching.
func (c *Client) Generate(ctx context.Context, prompt string) (string, error) {
	key := cacheKey(c.cfg.Model, prompt)
	if cached, ok := c.cache.Get(key); ok {
		slog.Debug("ollama cache hit", "key", key[:8])
		return cached, nil
	}

	if !c.limiter.Allow() {
		return "", fmt.Errorf("ollama rate limit exceeded (%d rpm)", c.cfg.RPM)
	}

	resp, err := c.chat(ctx, []Message{{Role: "user", Content: prompt}}, "")
	if err != nil {
		return "", err
	}

	c.cache.Set(key, resp)
	return resp, nil
}

// GenerateJSON is like Generate but requests a JSON response and caches it.
func (c *Client) GenerateJSON(ctx context.Context, prompt string) (string, error) {
	key := cacheKey(c.cfg.Model+"_json", prompt)
	if cached, ok := c.cache.Get(key); ok {
		return cached, nil
	}

	if !c.limiter.Allow() {
		return "", fmt.Errorf("ollama rate limit exceeded")
	}

	resp, err := c.chat(ctx, []Message{{Role: "user", Content: prompt}}, "json")
	if err != nil {
		return "", err
	}

	c.cache.Set(key, resp)
	return resp, nil
}

// Model returns the configured model name.
func (c *Client) Model() string { return c.cfg.Model }

// ─── internal ──────────────────────────────────────────────────────────────

func (c *Client) chat(ctx context.Context, msgs []Message, format string) (string, error) {
	req := ChatRequest{
		Model:    c.cfg.Model,
		Messages: msgs,
		Stream:   false,
		Format:   format,
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama request build: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("ollama read body: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama status %d: %s", res.StatusCode, string(raw))
	}

	var cr ChatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("ollama parse: %w", err)
	}
	return cr.Message.Content, nil
}

// ─── Rate Limiter (token bucket, 1-second granularity) ─────────────────────

type rateLimiter struct {
	mu       sync.Mutex
	tokens   int
	max      int
	lastFill time.Time
}

func newRateLimiter(rpm int) *rateLimiter {
	return &rateLimiter{tokens: rpm, max: rpm, lastFill: time.Now()}
}

func (r *rateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(r.lastFill)
	if elapsed >= time.Minute {
		r.tokens = r.max
		r.lastFill = now
	} else {
		// Refill proportionally
		added := int(float64(r.max) * elapsed.Seconds() / 60.0)
		r.tokens += added
		if r.tokens > r.max {
			r.tokens = r.max
		}
		if added > 0 {
			r.lastFill = now
		}
	}
	if r.tokens <= 0 {
		return false
	}
	r.tokens--
	return true
}

// ─── LRU Cache ─────────────────────────────────────────────────────────────

type cacheEntry struct {
	value     string
	expiresAt time.Time
	prev, next *cacheEntry
	key       string
}

type lruCache struct {
	mu       sync.Mutex
	cap      int
	ttl      time.Duration
	items    map[string]*cacheEntry
	head     *cacheEntry // most-recent
	tail     *cacheEntry // least-recent
}

func newLRU(cap int, ttl time.Duration) *lruCache {
	return &lruCache{cap: cap, ttl: ttl, items: make(map[string]*cacheEntry, cap)}
}

func (c *lruCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiresAt) {
		c.remove(e)
		return "", false
	}
	c.moveToFront(e)
	return e.value, true
}

func (c *lruCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.value = value
		e.expiresAt = time.Now().Add(c.ttl)
		c.moveToFront(e)
		return
	}
	e := &cacheEntry{key: key, value: value, expiresAt: time.Now().Add(c.ttl)}
	c.items[key] = e
	c.pushFront(e)
	if len(c.items) > c.cap {
		c.evict()
	}
}

func (c *lruCache) pushFront(e *cacheEntry) {
	e.next = c.head
	e.prev = nil
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *lruCache) moveToFront(e *cacheEntry) {
	if c.head == e {
		return
	}
	c.remove(e)
	c.pushFront(e)
}

func (c *lruCache) remove(e *cacheEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	delete(c.items, e.key)
}

func (c *lruCache) evict() {
	if c.tail != nil {
		c.remove(c.tail)
	}
}

func cacheKey(model, prompt string) string {
	h := sha256.Sum256([]byte(model + "\x00" + prompt))
	return fmt.Sprintf("%x", h)
}
