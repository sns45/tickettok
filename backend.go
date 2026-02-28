package main

import "sync"

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
