// Package rules provides a dynamic, hot-reloadable rule engine.
// Rules are loaded from rules.json and evaluated against a string key→value Context.
// Each rule can match on conditions (equality, contains, regex) and trigger an action.
package rules

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
)

// Context is the set of key-value pairs that rules are evaluated against.
type Context map[string]string

// ─── Data Model ────────────────────────────────────────────────────────────

// Rule is a single rule loaded from JSON.
type Rule struct {
	ID         string      `json:"id"`
	Priority   int         `json:"priority"`   // higher = evaluated first
	Conditions []Condition `json:"conditions"` // all must match (AND)
	Action     Action      `json:"action"`
}

// Condition tests a single field.
type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"` // eq, contains, starts_with, ends_with, regex, not_eq
	Value    string `json:"value"`
}

// Action is what happens when a rule matches.
type Action struct {
	Type     string `json:"type"`     // categorize, respond, tag, log
	Response string `json:"response"` // text to return (if type == respond)
	Category string `json:"category"` // ticket category override
	Tag      string `json:"tag"`      // arbitrary tag
	Action   string `json:"action"`   // free-form action name
}

// RuleSet is the file structure.
type RuleSet struct {
	Version string `json:"version"`
	Rules   []Rule `json:"rules"`
}

// ─── Engine ────────────────────────────────────────────────────────────────

// Engine holds the compiled rule set and evaluates contexts.
type Engine struct {
	mu      sync.RWMutex
	version string
	rules   []Rule           // sorted by descending priority
	regexes map[string]*regexp.Regexp
}

// NewEngine returns an empty engine ready to load rules.
func NewEngine() *Engine {
	return &Engine{regexes: make(map[string]*regexp.Regexp)}
}

// LoadFromFile reads and compiles rules from a JSON file.
// It is safe to call concurrently and replaces the existing rule set atomically.
func (e *Engine) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("rules: read %s: %w", path, err)
	}
	var rs RuleSet
	if err := json.Unmarshal(data, &rs); err != nil {
		return fmt.Errorf("rules: parse %s: %w", path, err)
	}
	return e.load(rs)
}

// LoadFromBytes parses and compiles rules from raw JSON bytes.
func (e *Engine) LoadFromBytes(data []byte) error {
	var rs RuleSet
	if err := json.Unmarshal(data, &rs); err != nil {
		return fmt.Errorf("rules: parse bytes: %w", err)
	}
	return e.load(rs)
}

// Version returns the currently loaded rule set version string.
func (e *Engine) Version() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.version
}

// Evaluate runs the context through all rules and returns the first match.
func (e *Engine) Evaluate(ctx Context) (Action, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, rule := range e.rules {
		if e.matches(rule, ctx) {
			slog.Debug("rule matched", "id", rule.ID, "action", rule.Action.Type)
			return rule.Action, true
		}
	}
	return Action{}, false
}

// ─── internal ──────────────────────────────────────────────────────────────

func (e *Engine) load(rs RuleSet) error {
	// Pre-compile regexes
	regexes := make(map[string]*regexp.Regexp)
	for _, rule := range rs.Rules {
		for _, cond := range rule.Conditions {
			if cond.Operator == "regex" {
				key := rule.ID + ":" + cond.Field + ":" + cond.Value
				re, err := regexp.Compile("(?i)" + cond.Value)
				if err != nil {
					return fmt.Errorf("rules: invalid regex in rule %q: %w", rule.ID, err)
				}
				regexes[key] = re
			}
		}
	}

	// Sort by descending priority (stable)
	sorted := make([]Rule, len(rs.Rules))
	copy(sorted, rs.Rules)
	stableSort(sorted)

	e.mu.Lock()
	e.version = rs.Version
	e.rules = sorted
	e.regexes = regexes
	e.mu.Unlock()

	slog.Info("rules loaded", "version", rs.Version, "count", len(sorted))
	return nil
}

func (e *Engine) matches(rule Rule, ctx Context) bool {
	if len(rule.Conditions) == 0 {
		return true // wildcard rule
	}
	for _, cond := range rule.Conditions {
		if !e.conditionMatches(rule.ID, cond, ctx) {
			return false
		}
	}
	return true
}

func (e *Engine) conditionMatches(ruleID string, cond Condition, ctx Context) bool {
	val, ok := ctx[cond.Field]
	if !ok {
		// Field not present → treat as empty string
		val = ""
	}
	v := strings.ToLower(val)
	cv := strings.ToLower(cond.Value)

	switch cond.Operator {
	case "eq", "equals":
		return v == cv
	case "not_eq", "neq":
		return v != cv
	case "contains":
		return strings.Contains(v, cv)
	case "not_contains":
		return !strings.Contains(v, cv)
	case "starts_with":
		return strings.HasPrefix(v, cv)
	case "ends_with":
		return strings.HasSuffix(v, cv)
	case "regex":
		key := ruleID + ":" + cond.Field + ":" + cond.Value
		if re, ok := e.regexes[key]; ok {
			return re.MatchString(val)
		}
		return false
	case "exists":
		return ok
	case "not_exists":
		return !ok
	default:
		slog.Warn("unknown rule operator", "operator", cond.Operator)
		return false
	}
}

// insertion sort (rules are typically < 100, no need for quicksort)
func stableSort(rules []Rule) {
	for i := 1; i < len(rules); i++ {
		for j := i; j > 0 && rules[j].Priority > rules[j-1].Priority; j-- {
			rules[j], rules[j-1] = rules[j-1], rules[j]
		}
	}
}
