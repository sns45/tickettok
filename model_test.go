package main

import (
	"strings"
	"testing"
)

func TestColumnForStatus(t *testing.T) {
	tests := []struct {
		name    string
		columns int
		status  AgentStatus
		want    int
	}{
		// 2-column mode
		{"2col RUNNING", 2, StatusRunning, 1},
		{"2col WAITING", 2, StatusWaiting, 1},
		{"2col IDLE", 2, StatusIdle, 0},
		{"2col DONE", 2, StatusDone, 0},
		// 3-column mode
		{"3col RUNNING", 3, StatusRunning, 2},
		{"3col WAITING", 3, StatusWaiting, 1},
		{"3col IDLE", 3, StatusIdle, 0},
		{"3col DONE", 3, StatusDone, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Model{columns: tt.columns}
			if got := m.columnForStatus(tt.status); got != tt.want {
				t.Errorf("columnForStatus(%q) = %d, want %d", tt.status, got, tt.want)
			}
		})
	}
}

func TestNextInSameColumn(t *testing.T) {
	agents := []*Agent{
		{ID: "1", Status: StatusIdle},
		{ID: "2", Status: StatusRunning},
		{ID: "3", Status: StatusIdle},
		{ID: "4", Status: StatusRunning},
	}

	t.Run("wraps within column", func(t *testing.T) {
		m := &Model{agents: agents, selected: 0, columns: 3}
		// Agent 0 is IDLE (col 0), Agent 2 is also IDLE (col 0)
		next := m.nextInSameColumn(+1)
		if next != 2 {
			t.Errorf("nextInSameColumn(+1) from 0 = %d, want 2", next)
		}
		// Wrap back
		m.selected = 2
		next = m.nextInSameColumn(+1)
		if next != 0 {
			t.Errorf("nextInSameColumn(+1) from 2 should wrap to 0, got %d", next)
		}
	})

	t.Run("single agent stays", func(t *testing.T) {
		single := []*Agent{{ID: "1", Status: StatusRunning}}
		m := &Model{agents: single, selected: 0, columns: 3}
		if got := m.nextInSameColumn(+1); got != 0 {
			t.Errorf("nextInSameColumn(+1) with single agent = %d, want 0", got)
		}
	})

	t.Run("empty agents unchanged", func(t *testing.T) {
		m := &Model{agents: nil, selected: 0, columns: 3}
		if got := m.nextInSameColumn(+1); got != 0 {
			t.Errorf("nextInSameColumn(+1) with empty agents = %d, want 0", got)
		}
	})
}

func TestNextInColumn(t *testing.T) {
	agents := []*Agent{
		{ID: "1", Status: StatusIdle},    // col 0
		{ID: "2", Status: StatusWaiting}, // col 1
		{ID: "3", Status: StatusRunning}, // col 2
		{ID: "4", Status: StatusIdle},    // col 0
	}

	t.Run("moves to adjacent column", func(t *testing.T) {
		m := &Model{agents: agents, selected: 0, columns: 3}
		// From col 0 → col 1
		next := m.nextInColumn(+1)
		if next != 1 {
			t.Errorf("nextInColumn(+1) from col0 = %d, want 1", next)
		}
	})

	t.Run("clamps at left edge", func(t *testing.T) {
		m := &Model{agents: agents, selected: 0, columns: 3}
		next := m.nextInColumn(-1)
		if next != 0 {
			t.Errorf("nextInColumn(-1) from col0 = %d, want 0 (clamped)", next)
		}
	})

	t.Run("clamps at right edge", func(t *testing.T) {
		m := &Model{agents: agents, selected: 2, columns: 3}
		next := m.nextInColumn(+1)
		if next != 2 {
			t.Errorf("nextInColumn(+1) from col2 = %d, want 2 (clamped)", next)
		}
	})

	t.Run("empty agents", func(t *testing.T) {
		m := &Model{agents: nil, selected: 0, columns: 3}
		if got := m.nextInColumn(+1); got != 0 {
			t.Errorf("nextInColumn(+1) with empty agents = %d, want 0", got)
		}
	})

	t.Run("skips empty middle column", func(t *testing.T) {
		// No WAITING agents — column 1 is empty
		noWaiting := []*Agent{
			{ID: "1", Status: StatusIdle},    // col 0
			{ID: "2", Status: StatusRunning}, // col 2
			{ID: "3", Status: StatusIdle},    // col 0
		}
		m := &Model{agents: noWaiting, selected: 0, columns: 3}
		// Right from col 0 should skip empty col 1 and land in col 2
		next := m.nextInColumn(+1)
		if next != 1 { // agent index 1 is RUNNING in col 2
			t.Errorf("nextInColumn(+1) skipping empty col1 = %d, want 1", next)
		}
		// Left from col 2 should skip empty col 1 and land in col 0
		m.selected = 1
		next = m.nextInColumn(-1)
		if next != 0 {
			t.Errorf("nextInColumn(-1) skipping empty col1 = %d, want 0", next)
		}
	})
}

func TestCropToHeight(t *testing.T) {
	content := strings.Join([]string{
		"line 0", "line 1", "line 2", "line 3", "line 4",
		"line 5", "line 6", "line 7", "line 8", "line 9",
	}, "\n")

	t.Run("basic crop", func(t *testing.T) {
		got := cropToHeight(content, 3, 0)
		lines := strings.Split(got, "\n")
		if len(lines) != 3 {
			t.Errorf("cropToHeight(maxLines=3) returned %d lines, want 3", len(lines))
		}
		if lines[0] != "line 0" {
			t.Errorf("first line = %q, want %q", lines[0], "line 0")
		}
	})

	t.Run("scrollOffset 0", func(t *testing.T) {
		got := cropToHeight(content, 5, 0)
		lines := strings.Split(got, "\n")
		if lines[0] != "line 0" {
			t.Errorf("first line with offset=0 = %q, want %q", lines[0], "line 0")
		}
	})

	t.Run("large offset clamped", func(t *testing.T) {
		got := cropToHeight(content, 5, 100)
		// Large offset should reset to 0
		lines := strings.Split(got, "\n")
		if len(lines) == 0 {
			t.Error("cropToHeight with large offset returned empty")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		got := cropToHeight("", 5, 0)
		if got != "" {
			t.Errorf("cropToHeight(\"\") = %q, want empty", got)
		}
	})
}
