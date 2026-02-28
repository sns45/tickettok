package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type AgentStatus string

const (
	StatusRunning AgentStatus = "RUNNING"
	StatusIdle    AgentStatus = "IDLE"
	StatusWaiting AgentStatus = "WAITING"
	StatusDone    AgentStatus = "DONE"
)

type Agent struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Dir         string      `json:"dir"`
	Status      AgentStatus `json:"status"`
	CreatedAt   time.Time   `json:"created_at"`
	StatusSince time.Time   `json:"status_since"`
	SessionName string      `json:"session_name,omitempty"`
	Discovered  bool        `json:"discovered,omitempty"`
	BackendID   string      `json:"backend,omitempty"`
}

type StateFile struct {
	Agents []*Agent `json:"agents"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	agents   []*Agent
	nextID   int
}

func stateDir() string {
	return filepath.Join(os.Getenv("HOME"), ".tickettok")
}

func statePath() string {
	return filepath.Join(stateDir(), "state.json")
}

func NewStore() (*Store, error) {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	s := &Store{
		path:   statePath(),
		nextID: 1,
	}

	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load state: %w", err)
	}

	// Set nextID based on existing agents
	for _, a := range s.agents {
		var id int
		if _, err := fmt.Sscanf(a.ID, "%d", &id); err == nil && id >= s.nextID {
			s.nextID = id + 1
		}
	}

	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var sf StateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}
	s.agents = sf.Agents
	if s.agents == nil {
		s.agents = []*Agent{}
	}
	// Migrate: default empty BackendID to "claude"
	for _, a := range s.agents {
		if a.BackendID == "" {
			a.BackendID = "claude"
		}
	}
	return nil
}

func (s *Store) save() error {
	sf := StateFile{Agents: s.agents}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return os.WriteFile(s.path, data, 0644)
}

func (s *Store) Add(name, dir string) *Agent {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	a := &Agent{
		ID:          fmt.Sprintf("%d", s.nextID),
		Name:        name,
		Dir:         dir,
		Status:      StatusRunning,
		CreatedAt:   now,
		StatusSince: now,
		BackendID:   DefaultBackend().ID(),
	}
	s.nextID++
	s.agents = append(s.agents, a)
	_ = s.save()
	return a
}

func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, a := range s.agents {
		if a.ID == id {
			s.agents = append(s.agents[:i], s.agents[i+1:]...)
			_ = s.save()
			return true
		}
	}
	return false
}

func (s *Store) Update(id string, status AgentStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, a := range s.agents {
		if a.ID == id {
			if a.Status != status {
				a.Status = status
				a.StatusSince = time.Now()
			}
			break
		}
	}
	_ = s.save()
}

func (s *Store) UpdateSessionName(id string, sessName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, a := range s.agents {
		if a.ID == id {
			a.SessionName = sessName
			break
		}
	}
	_ = s.save()
}

func (s *Store) Get(id string) *Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, a := range s.agents {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (s *Store) GetByName(name string) *Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, a := range s.agents {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func (s *Store) List() []*Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Agent, len(s.agents))
	copy(out, s.agents)
	return out
}

func (s *Store) UpdateDiscovered(id string, discovered bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, a := range s.agents {
		if a.ID == id {
			a.Discovered = discovered
			break
		}
	}
	_ = s.save()
}

// Backend returns the Backend for this agent, falling back to the default.
func (a *Agent) Backend() Backend {
	if b := GetBackend(a.BackendID); b != nil {
		return b
	}
	return DefaultBackend()
}

func (s *Store) ClearDone() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var kept []*Agent
	removed := 0
	for _, a := range s.agents {
		if a.Status == StatusDone {
			removed++
		} else {
			kept = append(kept, a)
		}
	}
	if removed > 0 {
		s.agents = kept
		if s.agents == nil {
			s.agents = []*Agent{}
		}
		_ = s.save()
	}
	return removed
}
