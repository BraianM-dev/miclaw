// Package updates implements a manifest-based hot-reload update system.
// The manager watches manifest.json, downloads changed components, validates
// their SHA-256 hashes, and triggers callbacks for hot-reload without restart.
package updates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ─── Manifest types ────────────────────────────────────────────────────────

// Manifest is the top-level structure of manifest.json.
type Manifest struct {
	Version   string            `json:"version"`
	UpdatedAt string            `json:"updated_at"`
	BaseURL   string            `json:"base_url,omitempty"` // optional remote base for downloads
	Files     []FileEntry       `json:"files"`
}

// FileEntry describes one downloadable component.
type FileEntry struct {
	Name     string `json:"name"`     // e.g. "rules.json"
	Path     string `json:"path"`     // local relative path under DataDir
	URL      string `json:"url"`      // full URL (overrides BaseURL+Path)
	SHA256   string `json:"sha256"`   // expected hex digest
	Version  string `json:"version"`
	HotLoad  bool   `json:"hot_load"` // trigger OnReload after update
}

// ─── Config ────────────────────────────────────────────────────────────────

// Config drives the Manager.
type Config struct {
	ManifestPath  string                  // local path to manifest.json
	DataDir       string                  // root for downloaded files
	CheckInterval time.Duration           // how often to re-read manifest (default 5m)
	OnReload      func(component string)  // called after a hot-reload-eligible file changes
}

// ─── Manager ───────────────────────────────────────────────────────────────

// Manager watches the manifest and orchestrates incremental downloads.
type Manager struct {
	cfg      Config
	mu       sync.RWMutex
	manifest Manifest
	stopCh   chan struct{}
	http     *http.Client
}

// NewManager creates a Manager. Call Start() to begin background checks.
func NewManager(cfg Config) *Manager {
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 5 * time.Minute
	}
	return &Manager{
		cfg:    cfg,
		stopCh: make(chan struct{}),
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// Start launches the background check loop. Non-blocking.
func (m *Manager) Start() {
	// Do an immediate check on start
	if err := m.check(); err != nil {
		slog.Warn("update check on start", "error", err)
	}
	go m.loop()
}

// Stop signals the background loop to exit.
func (m *Manager) Stop() {
	close(m.stopCh)
}

// CurrentManifest returns a snapshot of the loaded manifest.
func (m *Manager) CurrentManifest() Manifest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.manifest
}

// ForceCheck triggers an immediate out-of-schedule check.
func (m *Manager) ForceCheck() error {
	return m.check()
}

// ─── internal ──────────────────────────────────────────────────────────────

func (m *Manager) loop() {
	ticker := time.NewTicker(m.cfg.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := m.check(); err != nil {
				slog.Warn("update check failed", "error", err)
			}
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) check() error {
	data, err := os.ReadFile(m.cfg.ManifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	var newManifest Manifest
	if err := json.Unmarshal(data, &newManifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	m.mu.RLock()
	currentVersion := m.manifest.Version
	m.mu.RUnlock()

	if newManifest.Version == currentVersion {
		slog.Debug("manifest unchanged", "version", currentVersion)
		return nil
	}

	slog.Info("manifest changed", "old", currentVersion, "new", newManifest.Version)

	var updated []string
	for _, entry := range newManifest.Files {
		changed, err := m.syncFile(newManifest.BaseURL, entry)
		if err != nil {
			slog.Error("sync file failed", "name", entry.Name, "error", err)
			continue
		}
		if changed && entry.HotLoad && m.cfg.OnReload != nil {
			updated = append(updated, entry.Name)
		}
	}

	m.mu.Lock()
	m.manifest = newManifest
	m.mu.Unlock()

	// Fire hot-reload callbacks after manifest is updated
	for _, name := range updated {
		slog.Info("hot-reload", "component", name)
		m.cfg.OnReload(name)
	}

	return nil
}

// syncFile downloads (or verifies) a single file entry.
// Returns true if the local file was changed.
func (m *Manager) syncFile(baseURL string, entry FileEntry) (bool, error) {
	localPath := filepath.Join(m.cfg.DataDir, entry.Path)
	if entry.Path == "" {
		localPath = filepath.Join(m.cfg.DataDir, entry.Name)
	}

	// Check if local file already matches expected hash
	if entry.SHA256 != "" {
		if ok, _ := fileMatchesHash(localPath, entry.SHA256); ok {
			slog.Debug("file up to date", "name", entry.Name)
			return false, nil
		}
	}

	// Determine download URL
	downloadURL := entry.URL
	if downloadURL == "" && baseURL != "" {
		p := entry.Path
		if p == "" {
			p = entry.Name
		}
		downloadURL = baseURL + "/" + p
	}

	// If no URL, just validate local existence
	if downloadURL == "" {
		if _, err := os.Stat(localPath); err != nil {
			return false, fmt.Errorf("file %q not found locally and no URL configured", entry.Name)
		}
		return false, nil
	}

	slog.Info("downloading", "name", entry.Name, "url", downloadURL)
	if err := m.download(downloadURL, localPath, entry.SHA256); err != nil {
		return false, fmt.Errorf("download %q: %w", entry.Name, err)
	}

	slog.Info("file updated", "name", entry.Name, "version", entry.Version)
	return true, nil
}

func (m *Manager) download(url, localPath, expectedHash string) error {
	resp, err := m.http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	// Write to temp file first
	tmp := localPath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()

	// Validate hash
	if expectedHash != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != expectedHash {
			os.Remove(tmp)
			return fmt.Errorf("hash mismatch: expected %s got %s", expectedHash, got)
		}
	}

	return os.Rename(tmp, localPath)
}

// fileMatchesHash returns true if the file at path has the given SHA-256 hex digest.
func fileMatchesHash(path, expectedHex string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == expectedHex, nil
}
