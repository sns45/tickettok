package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Backend defines the contract for an AI coding agent backend.
type Backend interface {
	Name() string // Display name: "Claude Code"
	ID() string   // Stable key for state: "claude"

	// Spawning
	SpawnCommand(args []string) (command string, stripEnvVars []string)
	ResumeArgs() []string // args to pass to SpawnCommand to resume a prior session
	CheckDeps() error

	// Content analysis (called with ANSI-stripped pane content)
	DetectStatus(content string) AgentStatus
	DetectMode(content string) string
	StripChrome(lines []string, waiting bool) []string

	// Discovery
	LooksLikeMe(content string) bool
	Discover() []DiscoveredAgent

	// Hooks
	InstallHooks() error
	ReadHookStatus(agentID string) (AgentStatus, bool)
	CleanHookStatus(agentID string)
}

var (
	registryMu sync.RWMutex
	backends   = map[string]Backend{}
	defaultID  string
)

// RegisterBackend adds a backend to the registry.
// The first registered backend becomes the default.
func RegisterBackend(b Backend) {
	registryMu.Lock()
	defer registryMu.Unlock()
	backends[b.ID()] = b
	if defaultID == "" {
		defaultID = b.ID()
	}
}

// GetBackend returns the backend with the given ID, or nil.
func GetBackend(id string) Backend {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return backends[id]
}

// DefaultBackend returns the first-registered (default) backend.
func DefaultBackend() Backend {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return backends[defaultID]
}

// AllBackends returns all registered backends.
func AllBackends() []Backend {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Backend, 0, len(backends))
	for _, b := range backends {
		out = append(out, b)
	}
	return out
}

// AvailableBackends returns only backends whose CLI is installed.
func AvailableBackends() []Backend {
	var avail []Backend
	for _, b := range AllBackends() {
		if b.CheckDeps() == nil {
			avail = append(avail, b)
		}
	}
	return avail
}

// --- Shared hook status helpers ---

// hookStatusDir returns the shared status directory for all backends.
func hookStatusDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tickettok", "status")
}

// hookStatus represents the JSON written by hook scripts (all backends use the same format).
type hookStatus struct {
	State string `json:"state"`
	Ts    int64  `json:"ts"`
}

// readHookStatusFile reads and parses a hook-written status file for an agent.
// Returns the detected status and true if valid, or ("", false) if missing/expired.
func readHookStatusFile(agentID string) (AgentStatus, bool) {
	path := filepath.Join(hookStatusDir(), agentID+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}

	var hs hookStatus
	if err := json.Unmarshal(data, &hs); err != nil {
		return "", false
	}

	age := time.Now().Unix() - hs.Ts

	switch hs.State {
	case "RUNNING":
		if age > 30 {
			return "", false
		}
		return StatusRunning, true
	case "WAITING":
		if age > 300 {
			return "", false
		}
		return StatusWaiting, true
	case "IDLE":
		if age > 300 {
			return "", false
		}
		return StatusIdle, true
	case "DONE":
		if age > 300 {
			return "", false
		}
		return StatusDone, true
	default:
		return "", false
	}
}

// cleanHookStatusFile removes the status file for an agent.
func cleanHookStatusFile(agentID string) {
	path := filepath.Join(hookStatusDir(), agentID+".json")
	_ = os.Remove(path)
}
