package main

import (
	"strings"
	"sync"
)

// AgentManager tracks tmux sessions for all agents.
type AgentManager struct {
	mu       sync.RWMutex
	sessions map[string]*TmuxSession
}

func NewAgentManager() *AgentManager {
	return &AgentManager{
		sessions: make(map[string]*TmuxSession),
	}
}

// SpawnAgent creates a tmux session running the agent's backend.
func (m *AgentManager) SpawnAgent(agent *Agent) error {
	sessName := SessionName(agent.ID)

	backend := agent.Backend()
	command, stripEnv := backend.SpawnCommand(nil)

	sess, err := CreateSession(sessName, agent.Dir, command, stripEnv)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.sessions[agent.ID] = sess
	m.mu.Unlock()

	// Store session name in agent state
	agent.SessionName = sessName

	return nil
}

// RespawnAgent re-creates the tmux session for a dead agent, resuming its
// previous conversation via the backend's ResumeArgs.
func (m *AgentManager) RespawnAgent(agent *Agent) error {
	sessName := SessionName(agent.ID)

	backend := agent.Backend()
	command, stripEnv := backend.SpawnCommand(backend.ResumeArgs())

	sess, err := CreateSession(sessName, agent.Dir, command, stripEnv)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.sessions[agent.ID] = sess
	m.mu.Unlock()

	agent.SessionName = sessName
	return nil
}

// Kill destroys the tmux session for the given agent.
func (m *AgentManager) Kill(id string) error {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	if ok {
		return sess.Kill()
	}
	return nil
}

// KillBySession kills a tmux session by name (for agents not spawned this session).
func KillBySession(sessionName string) error {
	sess := &TmuxSession{Name: sessionName}
	return sess.Kill()
}

// GetSession returns the tmux session for an agent. If not in memory,
// reconstructs it from the agent's session name.
func (m *AgentManager) GetSession(agent *Agent) *TmuxSession {
	m.mu.RLock()
	sess, ok := m.sessions[agent.ID]
	m.mu.RUnlock()

	if ok {
		return sess
	}

	// Reconstruct from state — the tmux session may still be alive from a previous run
	if agent.SessionName != "" {
		sess = &TmuxSession{Name: agent.SessionName}
		if sess.IsAlive() {
			_ = sess.attachPty() // re-attach PTY so capture-pane works
			m.mu.Lock()
			m.sessions[agent.ID] = sess
			m.mu.Unlock()
			return sess
		}
	}

	return nil
}

// DetectStatus checks hook-based status first, then falls back to capture-pane scraping.
// For discovered (external) agents, uses PTY-free capture to avoid detaching the user's terminal.
func (m *AgentManager) DetectStatus(agent *Agent) AgentStatus {
	backend := agent.Backend()

	if agent.Discovered {
		// PTY-free path for external sessions
		if !IsSessionAlive(agent.SessionName) {
			return StatusDone
		}
		content, err := CapturePane(agent.SessionName)
		if err != nil {
			return StatusDone
		}
		return backend.DetectStatus(content)
	}

	// Try hook-based status first (fast, no subprocess)
	if status, ok := backend.ReadHookStatus(agent.ID); ok {
		return status
	}

	// Fall back to capture-pane scraping
	sess := m.GetSession(agent)
	if sess == nil || !sess.IsAlive() {
		return StatusDone
	}

	content, err := sess.CapturePaneContent()
	if err != nil {
		return StatusDone
	}

	return backend.DetectStatus(content)
}

// GetPreview returns the last n meaningful output lines from the agent's tmux pane.
func (m *AgentManager) GetPreview(agent *Agent, n int) []string {
	sess := m.GetSession(agent)
	if sess == nil {
		return nil
	}

	content, err := sess.CapturePaneContent()
	if err != nil {
		return nil
	}

	backend := agent.Backend()
	stripFn := func(lines []string) []string {
		return backend.StripChrome(lines, false)
	}
	return PreviewFromContent(content, n, stripFn)
}

// PaneInfo holds both preview lines and detected mode from a single pane capture.
type PaneInfo struct {
	Preview []string
	Mode    string
	Title   string
}

// GetPaneInfo captures the pane once and returns both preview and mode.
// status is passed so preview stripping can adapt (e.g. WAITING keeps ❯ lines).
// For discovered (external) agents, uses PTY-free capture.
func (m *AgentManager) GetPaneInfo(agent *Agent, n int) PaneInfo {
	var content string
	var err error

	if agent.Discovered {
		// PTY-free path for external sessions
		if !IsSessionAlive(agent.SessionName) {
			return PaneInfo{}
		}
		content, err = CapturePane(agent.SessionName)
	} else {
		sess := m.GetSession(agent)
		if sess == nil {
			return PaneInfo{}
		}
		content, err = sess.CapturePaneContent()
	}

	if err != nil {
		return PaneInfo{}
	}

	// Read pane title (OSC 2 set by Claude Code)
	sessName := agent.SessionName
	if sessName == "" {
		sessName = SessionName(agent.ID)
	}
	title := GetPaneTitle(sessName)

	backend := agent.Backend()
	waiting := agent.Status == StatusWaiting
	stripFn := func(lines []string) []string {
		return backend.StripChrome(lines, waiting)
	}
	return PaneInfo{
		Preview: PreviewFromContent(content, n, stripFn),
		Mode:    backend.DetectMode(content),
		Title:   title,
	}
}

// SendKeys sends text input to the agent's tmux pane.
func (m *AgentManager) SendKeys(agent *Agent, text string) error {
	sess := m.GetSession(agent)
	if sess == nil {
		return nil
	}
	return sess.SendKeys(text)
}

// CloseAll closes all PTY connections (call on exit to prevent leaked processes).
func (m *AgentManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sess := range m.sessions {
		sess.closePty()
	}
}

// shellQuote wraps a string in single quotes for shell safety.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
