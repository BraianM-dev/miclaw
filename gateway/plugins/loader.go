// Package plugins implements an external-executable plugin system.
// Each plugin is a standalone binary that communicates via JSON on stdin/stdout.
// Protocol:
//
//	stdin  → {"action":"run","payload":{...}}
//	stdout → {"ok":true,"result":{...}}   or   {"ok":false,"error":"..."}
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ─── Plugin manifest ───────────────────────────────────────────────────────

// PluginMeta is the self-description a plugin returns for action "describe".
type PluginMeta struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Actions     []string `json:"actions"`
}

// ─── Wire types ────────────────────────────────────────────────────────────

type pluginRequest struct {
	Action  string         `json:"action"`
	Payload map[string]any `json:"payload,omitempty"`
}

type pluginResponse struct {
	OK     bool           `json:"ok"`
	Result map[string]any `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// ─── Loader ────────────────────────────────────────────────────────────────

// Loader discovers and runs plugin executables from a directory.
type Loader struct {
	dir     string
	mu      sync.RWMutex
	plugins map[string]string // name → absolute path
}

// NewLoader creates a Loader for the given directory. The directory is scanned
// lazily on first use; call Reload() to force a re-scan.
func NewLoader(dir string) *Loader {
	l := &Loader{dir: dir, plugins: make(map[string]string)}
	_ = l.Reload()
	return l
}

// Reload re-scans the plugin directory for executables.
func (l *Loader) Reload() error {
	if err := os.MkdirAll(l.dir, 0755); err != nil {
		return err
	}

	found := make(map[string]string)
	err := filepath.WalkDir(l.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isExecutable(path) {
			return nil
		}
		name := pluginName(path)
		found[name] = path
		slog.Debug("plugin discovered", "name", name, "path", path)
		return nil
	})

	l.mu.Lock()
	l.plugins = found
	l.mu.Unlock()

	slog.Info("plugins loaded", "count", len(found), "dir", l.dir)
	return err
}

// List returns the names of all discovered plugins.
func (l *Loader) List() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	names := make([]string, 0, len(l.plugins))
	for n := range l.plugins {
		names = append(names, n)
	}
	return names
}

// Run executes the named plugin with a JSON payload and returns its result.
// The plugin receives {"action":"run","payload":{...}} on stdin and must
// write a single JSON line to stdout.
func (l *Loader) Run(ctx context.Context, name string, payload map[string]any) (map[string]any, error) {
	path, err := l.resolve(name)
	if err != nil {
		return nil, err
	}
	return l.invoke(ctx, path, "run", payload)
}

// Describe calls the plugin with action "describe" to fetch its metadata.
func (l *Loader) Describe(ctx context.Context, name string) (PluginMeta, error) {
	path, err := l.resolve(name)
	if err != nil {
		return PluginMeta{}, err
	}
	res, err := l.invoke(ctx, path, "describe", nil)
	if err != nil {
		return PluginMeta{}, err
	}

	// Unmarshal PluginMeta from result map
	raw, _ := json.Marshal(res)
	var meta PluginMeta
	_ = json.Unmarshal(raw, &meta)
	if meta.Name == "" {
		meta.Name = name
	}
	return meta, nil
}

// ─── internal ──────────────────────────────────────────────────────────────

func (l *Loader) resolve(name string) (string, error) {
	l.mu.RLock()
	path, ok := l.plugins[name]
	l.mu.RUnlock()
	if !ok {
		// Try reload once before failing
		_ = l.Reload()
		l.mu.RLock()
		path, ok = l.plugins[name]
		l.mu.RUnlock()
		if !ok {
			return "", fmt.Errorf("plugin %q not found in %s", name, l.dir)
		}
	}
	return path, nil
}

func (l *Loader) invoke(ctx context.Context, path, action string, payload map[string]any) (map[string]any, error) {
	req := pluginRequest{Action: action, Payload: payload}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal plugin request: %w", err)
	}

	cmd := exec.CommandContext(ctx, path)
	cmd.Stdin = bytes.NewReader(reqBytes)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("plugin exec error: %w (stderr: %s)", err, stderr.String())
	}

	var resp pluginResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		return nil, fmt.Errorf("plugin output parse: %w (output: %s)", err, stdout.String())
	}
	if !resp.OK {
		return nil, fmt.Errorf("plugin returned error: %s", resp.Error)
	}

	slog.Debug("plugin ran", "path", path, "action", action)
	return resp.Result, nil
}

// isExecutable returns true if the file looks like an executable we should load.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(path))
		return ext == ".exe" || ext == ".cmd" || ext == ".bat"
	}
	return info.Mode()&0111 != 0
}

// pluginName strips directory and extension from a path.
func pluginName(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}
