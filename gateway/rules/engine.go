// Package rules provides a hot-reloadable JSON rules engine for ticket triage.
package rules

import (
	"encoding/json"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ── Types ──────────────────────────────────────────────────────────────────

type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

type Action struct {
	Type     string `json:"type"`
	Category string `json:"category,omitempty"`
	Priority string `json:"priority,omitempty"`
	Response string `json:"response,omitempty"`
	Command  string `json:"action,omitempty"`
}

type Rule struct {
	ID         string      `json:"id"`
	Priority   int         `json:"priority"`
	Conditions []Condition `json:"conditions"`
	Action     Action      `json:"action"`
}

// Result is returned after evaluating fields against all rules.
type Result struct {
	Matched  bool
	RuleID   string
	Category string
	Priority string
	Response string
	Command  string
}

// ── Engine ─────────────────────────────────────────────────────────────────

type Engine struct {
	mu    sync.RWMutex
	rules []Rule
	cache map[string]*regexp.Regexp
}

func New() *Engine {
	return &Engine{cache: make(map[string]*regexp.Regexp)}
}

// LoadFromFile loads and compiles rules from a JSON file.
func (e *Engine) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var loaded []Rule
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	cache := make(map[string]*regexp.Regexp)
	for _, r := range loaded {
		for _, c := range r.Conditions {
			if c.Operator == "regex" {
				if _, ok := cache[c.Value]; !ok {
					compiled, err := regexp.Compile("(?i)" + c.Value)
					if err != nil {
						slog.Warn("invalid regex in rule", "rule", r.ID, "pattern", c.Value)
						continue
					}
					cache[c.Value] = compiled
				}
			}
		}
	}
	sort.Slice(loaded, func(i, j int) bool {
		return loaded[i].Priority > loaded[j].Priority
	})
	e.mu.Lock()
	e.rules = loaded
	e.cache = cache
	e.mu.Unlock()
	slog.Info("rules loaded", "count", len(loaded), "file", path)
	return nil
}

// Evaluate runs all rules against the given fields. First match wins.
func (e *Engine) Evaluate(fields map[string]string) Result {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, rule := range e.rules {
		if e.matchAll(rule.Conditions, fields) {
			return Result{
				Matched:  true,
				RuleID:   rule.ID,
				Category: rule.Action.Category,
				Priority: rule.Action.Priority,
				Response: rule.Action.Response,
				Command:  rule.Action.Command,
			}
		}
	}
	return Result{}
}

func (e *Engine) matchAll(conditions []Condition, fields map[string]string) bool {
	for _, c := range conditions {
		val := strings.ToLower(fields[c.Field])
		target := strings.ToLower(c.Value)
		switch c.Operator {
		case "eq", "equals":
			if val != target {
				return false
			}
		case "not_eq", "neq":
			if val == target {
				return false
			}
		case "contains":
			if !strings.Contains(val, target) {
				return false
			}
		case "not_contains":
			if strings.Contains(val, target) {
				return false
			}
		case "starts_with":
			if !strings.HasPrefix(val, target) {
				return false
			}
		case "ends_with":
			if !strings.HasSuffix(val, target) {
				return false
			}
		case "regex":
			re := e.cache[c.Value]
			if re == nil {
				return false
			}
			if !re.MatchString(fields[c.Field]) {
				return false
			}
		case "exists":
			if fields[c.Field] == "" {
				return false
			}
		case "not_exists":
			if fields[c.Field] != "" {
				return false
			}
		}
	}
	return true
}

// Rules returns a copy of the current rule list.
func (e *Engine) Rules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Rule, len(e.rules))
	copy(out, e.rules)
	return out
}
