package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WorkspaceAgent is a saved agent template (no live state).
type WorkspaceAgent struct {
	Name        string `json:"name"`
	Dir         string `json:"dir"`
	BackendID   string `json:"backend,omitempty"`
	AutoApprove bool   `json:"auto_approve,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

// WorkspaceFile represents a saved workspace containing agent templates.
type WorkspaceFile struct {
	Name      string           `json:"name"`
	Agents    []WorkspaceAgent `json:"agents"`
	CreatedAt time.Time        `json:"created_at"`
}

// lookupClaudeSessionID finds the most recently modified Claude session ID
// for the given directory by reading Claude's sessions-index.json.
func lookupClaudeSessionID(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	encoded := strings.ReplaceAll(dir, "/", "-")
	indexPath := filepath.Join(home, ".claude", "projects", encoded, "sessions-index.json")

	data, err := os.ReadFile(indexPath)
	if err != nil {
		return ""
	}

	var entries []struct {
		SessionID string `json:"sessionId"`
		Modified  string `json:"modified"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return ""
	}

	if len(entries) == 0 {
		return ""
	}

	best := 0
	bestTime, _ := time.Parse(time.RFC3339Nano, entries[0].Modified)
	for i := 1; i < len(entries); i++ {
		t, err := time.Parse(time.RFC3339Nano, entries[i].Modified)
		if err != nil {
			continue
		}
		if t.After(bestTime) {
			bestTime = t
			best = i
		}
	}
	return entries[best].SessionID
}

func workspaceDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tickettok", "workspaces")
}

func workspacePath(name string) string {
	return filepath.Join(workspaceDir(), name+".json")
}

// SaveWorkspace extracts templates from live agents and writes a workspace file.
func SaveWorkspace(name string, agents []*Agent) error {
	if err := os.MkdirAll(workspaceDir(), 0755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}

	var templates []WorkspaceAgent
	for _, a := range agents {
		wa := WorkspaceAgent{
			Name:        a.Name,
			Dir:         a.Dir,
			BackendID:   a.BackendID,
			AutoApprove: a.AutoApprove,
		}
		if a.BackendID == "claude" || a.BackendID == "" {
			wa.SessionID = lookupClaudeSessionID(a.Dir)
		}
		templates = append(templates, wa)
	}
	if templates == nil {
		templates = []WorkspaceAgent{}
	}

	wf := WorkspaceFile{
		Name:      name,
		Agents:    templates,
		CreatedAt: time.Now(),
	}

	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace: %w", err)
	}
	return os.WriteFile(workspacePath(name), data, 0644)
}

// LoadWorkspace reads and parses a workspace file.
func LoadWorkspace(name string) (*WorkspaceFile, error) {
	data, err := os.ReadFile(workspacePath(name))
	if err != nil {
		return nil, fmt.Errorf("read workspace %q: %w", name, err)
	}

	var wf WorkspaceFile
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse workspace %q: %w", name, err)
	}
	return &wf, nil
}

// ListWorkspaces returns sorted names of all saved workspaces.
func ListWorkspaces() ([]string, error) {
	entries, err := os.ReadDir(workspaceDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names, nil
}

// DeleteWorkspace removes a saved workspace file.
func DeleteWorkspace(name string) error {
	return os.Remove(workspacePath(name))
}

// WorkspaceExists checks whether a workspace file exists.
func WorkspaceExists(name string) bool {
	_, err := os.Stat(workspacePath(name))
	return err == nil
}

// AddAgentToWorkspace appends an agent template to an existing workspace.
func AddAgentToWorkspace(name string, agent WorkspaceAgent) error {
	wf, err := LoadWorkspace(name)
	if err != nil {
		return err
	}
	wf.Agents = append(wf.Agents, agent)
	return SaveWorkspace(name, agentsFromTemplates(wf))
}

// agentsFromTemplates converts workspace templates back to Agent pointers
// so SaveWorkspace can re-serialize them.
func agentsFromTemplates(wf *WorkspaceFile) []*Agent {
	agents := make([]*Agent, len(wf.Agents))
	for i, t := range wf.Agents {
		agents[i] = &Agent{
			Name:        t.Name,
			Dir:         t.Dir,
			BackendID:   t.BackendID,
			AutoApprove: t.AutoApprove,
		}
	}
	return agents
}

// spawnWorkspaceAgents spawns agents from workspace templates, returning the
// count of successfully started agents.
func spawnWorkspaceAgents(wf *WorkspaceFile, store *Store, manager *AgentManager) int {
	count := 0
	for _, t := range wf.Agents {
		dir := t.Dir
		if strings.HasPrefix(dir, "~/") {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[2:])
		}

		name := t.Name
		if name == "" {
			name = deriveNameFromDir(dir)
		}

		agent := store.Add(name, dir)

		if t.BackendID != "" {
			agent.BackendID = t.BackendID
		}
		agent.AutoApprove = t.AutoApprove

		// Use exact session ID when available, otherwise fall back to --continue.
		var extraArgs []string
		if t.SessionID != "" {
			extraArgs = []string{"--resume", t.SessionID}
		} else {
			extraArgs = agent.Backend().ResumeArgs()
		}
		if agent.AutoApprove {
			extraArgs = append(extraArgs, agent.Backend().AutoApproveArgs()...)
		}

		if err := manager.SpawnAgent(agent, extraArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to spawn %q: %v\n", name, err)
			continue
		}

		store.UpdateSessionName(agent.ID, agent.SessionName)
		store.Save()
		count++
	}
	return count
}
