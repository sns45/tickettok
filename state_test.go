package main

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return &Store{
		path:   filepath.Join(dir, "state.json"),
		agents: []*Agent{},
		nextID: 1,
	}
}

func TestStoreAddAndList(t *testing.T) {
	s := newTestStore(t)

	a := s.Add("myagent", "/tmp/project")
	if a.Name != "myagent" {
		t.Errorf("Add().Name = %q, want %q", a.Name, "myagent")
	}
	if a.Dir != "/tmp/project" {
		t.Errorf("Add().Dir = %q, want %q", a.Dir, "/tmp/project")
	}
	if a.Status != StatusRunning {
		t.Errorf("Add().Status = %q, want %q", a.Status, StatusRunning)
	}
	if a.ID != "1" {
		t.Errorf("Add().ID = %q, want %q", a.ID, "1")
	}

	agents := s.List()
	if len(agents) != 1 {
		t.Fatalf("List() returned %d agents, want 1", len(agents))
	}
	if agents[0].Name != "myagent" {
		t.Errorf("List()[0].Name = %q, want %q", agents[0].Name, "myagent")
	}
}

func TestStoreRemove(t *testing.T) {
	s := newTestStore(t)

	a := s.Add("agent1", "/tmp/a")
	if !s.Remove(a.ID) {
		t.Error("Remove(existing) returned false, want true")
	}
	if len(s.List()) != 0 {
		t.Errorf("List() after remove has %d agents, want 0", len(s.List()))
	}

	if s.Remove("nonexistent") {
		t.Error("Remove(unknown) returned true, want false")
	}
}

func TestStoreUpdate(t *testing.T) {
	s := newTestStore(t)

	a := s.Add("agent1", "/tmp/a")
	originalSince := a.StatusSince

	// Small delay to ensure timestamp differs
	time.Sleep(10 * time.Millisecond)

	// Status change should update StatusSince
	s.Update(a.ID, StatusWaiting)
	updated := s.Get(a.ID)
	if updated.Status != StatusWaiting {
		t.Errorf("Status = %q after Update, want %q", updated.Status, StatusWaiting)
	}
	if !updated.StatusSince.After(originalSince) {
		t.Error("StatusSince should be updated after status change")
	}

	// Same status should not update StatusSince
	sinceBefore := updated.StatusSince
	time.Sleep(10 * time.Millisecond)
	s.Update(a.ID, StatusWaiting)
	sameAgent := s.Get(a.ID)
	if sameAgent.StatusSince != sinceBefore {
		t.Error("StatusSince should not change when status is the same")
	}
}

func TestStoreGetByName(t *testing.T) {
	s := newTestStore(t)

	s.Add("alpha", "/tmp/a")
	s.Add("beta", "/tmp/b")

	if got := s.GetByName("alpha"); got == nil {
		t.Error("GetByName(alpha) returned nil")
	} else if got.Name != "alpha" {
		t.Errorf("GetByName(alpha).Name = %q", got.Name)
	}

	if got := s.GetByName("nonexistent"); got != nil {
		t.Error("GetByName(nonexistent) should return nil")
	}
}

func TestStoreClearDone(t *testing.T) {
	s := newTestStore(t)

	s.Add("running1", "/tmp/a")
	a2 := s.Add("done1", "/tmp/b")
	s.Add("idle1", "/tmp/c")
	a4 := s.Add("done2", "/tmp/d")

	s.Update(a2.ID, StatusDone)
	s.Update(a4.ID, StatusDone)

	removed := s.ClearDone()
	if removed != 2 {
		t.Errorf("ClearDone() = %d, want 2", removed)
	}

	remaining := s.List()
	if len(remaining) != 2 {
		t.Fatalf("List() after ClearDone has %d agents, want 2", len(remaining))
	}
	for _, a := range remaining {
		if a.Status == StatusDone {
			t.Errorf("agent %q still has DONE status after ClearDone", a.Name)
		}
	}
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s1 := &Store{
		path:   path,
		agents: []*Agent{},
		nextID: 1,
	}
	s1.Add("persist-me", "/tmp/project")

	// Create a new store from the same path
	s2 := &Store{
		path:   path,
		agents: []*Agent{},
		nextID: 1,
	}
	if err := s2.load(); err != nil {
		t.Fatalf("load() error: %v", err)
	}

	agents := s2.agents
	if len(agents) != 1 {
		t.Fatalf("Persisted store has %d agents, want 1", len(agents))
	}
	if agents[0].Name != "persist-me" {
		t.Errorf("Persisted agent name = %q, want %q", agents[0].Name, "persist-me")
	}
}
