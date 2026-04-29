// Package ai provides an Ollama client with response caching.
package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Client wraps the Ollama HTTP API with an in-memory LRU cache.
type Client struct {
	baseURL string
	model   string
	timeout time.Duration
	http    *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	response string
	exp      time.Time
}

// New creates a new AI client.
func New(ollamaURL, model string) *Client {
	return &Client{
		baseURL: ollamaURL,
		model:   model,
		timeout: 60 * time.Second,
		http:    &http.Client{Timeout: 60 * time.Second},
		cache:   make(map[string]cacheEntry),
	}
}

// Query sends a prompt to Ollama and returns the response.
// Results are cached for 10 minutes by (model+prompt) key.
func (c *Client) Query(prompt, systemPrompt string) (string, error) {
	return c.query(prompt, systemPrompt, "", 0.7, 512)
}

// QueryJSON sends a prompt requesting a JSON-formatted response.
// Ollama's "format":"json" constraint forces valid JSON output.
// Uses lower temperature (0.3) for consistent structured output.
func (c *Client) QueryJSON(prompt, systemPrompt string) (string, error) {
	return c.query(prompt, systemPrompt, "json", 0.3, 768)
}

// query is the shared implementation used by Query and QueryJSON.
func (c *Client) query(prompt, systemPrompt, format string, temperature float64, numPredict int) (string, error) {
	cKey := cacheKey(c.model + systemPrompt + format + prompt)

	c.mu.Lock()
	if entry, ok := c.cache[cKey]; ok && time.Now().Before(entry.exp) {
		resp := entry.response
		c.mu.Unlock()
		return resp, nil
	}
	c.mu.Unlock()

	body := map[string]any{
		"model":  c.model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"temperature": temperature,
			"num_predict": numPredict,
		},
	}
	if systemPrompt != "" {
		body["system"] = systemPrompt
	}
	if format != "" {
		body["format"] = format
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", c.baseURL+"/api/generate", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama unavailable: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result struct {
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("ollama response parse error: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("ollama error: %s", result.Error)
	}

	c.mu.Lock()
	c.cache[cKey] = cacheEntry{response: result.Response, exp: time.Now().Add(10 * time.Minute)}
	if len(c.cache) > 256 {
		for k, v := range c.cache {
			if time.Now().After(v.exp) {
				delete(c.cache, k)
			}
		}
	}
	c.mu.Unlock()

	return result.Response, nil
}

// Models returns the list of models available in Ollama.
func (c *Client) Models() ([]string, error) {
	resp, err := c.http.Get(c.baseURL + "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	names := make([]string, len(result.Models))
	for i, m := range result.Models {
		names[i] = m.Name
	}
	return names, nil
}

// Healthy checks if Ollama is reachable.
func (c *Client) Healthy() bool {
	resp, err := c.http.Get(c.baseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func cacheKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
