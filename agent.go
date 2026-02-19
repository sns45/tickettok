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

// SpawnAgent creates a tmux session running claude for the given agent.
func (m *AgentManager) SpawnAgent(agent *Agent) error {
	sessName := SessionName(agent.ID)

	// Always start Claude in interactive mode (not -p one-shot mode)
	// so the full Ink UI renders and capture-pane can see it.
	sess, err := CreateSession(sessName, agent.Dir, nil)
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
func (m *AgentManager) DetectStatus(agent *Agent) AgentStatus {
	// Try hook-based status first (fast, no subprocess)
	if status, ok := readHookStatus(agent.ID); ok {
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

	return DetectStatusFromContent(content)
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

	return PreviewFromContent(content, n)
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
