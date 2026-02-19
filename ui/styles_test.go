package ui

import (
	"strings"
	"testing"
)

func TestStatusBadge(t *testing.T) {
	tests := []struct {
		status string
		label  string
	}{
		{"RUNNING", "IN-PROGRESS"},
		{"WAITING", "WAITING"},
		{"IDLE", "IDLE"},
		{"DONE", "DONE"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := StatusBadge(tt.status)
			if got == "" {
				t.Errorf("StatusBadge(%q) returned empty string", tt.status)
			}
			// The rendered string includes ANSI styling; the label text should be present
			if !strings.Contains(got, tt.label) {
				t.Errorf("StatusBadge(%q) = %q, does not contain %q", tt.status, got, tt.label)
			}
		})
	}
}

func TestStatusDot(t *testing.T) {
	tests := []struct {
		status string
		symbol string
	}{
		{"RUNNING", "●"},
		{"WAITING", "▲"},
		{"IDLE", "○"},
		{"DONE", "✓"},
		{"UNKNOWN", "·"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := StatusDot(tt.status)
			if !strings.Contains(got, tt.symbol) {
				t.Errorf("StatusDot(%q) = %q, does not contain %q", tt.status, got, tt.symbol)
			}
		})
	}
}

func TestModeBadgeFor(t *testing.T) {
	tests := []struct {
		mode string
	}{
		{"PLAN"},
		{"EDITS"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got := ModeBadgeFor(tt.mode)
			if got == "" {
				t.Errorf("ModeBadgeFor(%q) returned empty string", tt.mode)
			}
			if !strings.Contains(got, tt.mode) {
				t.Errorf("ModeBadgeFor(%q) = %q, does not contain mode text", tt.mode, got)
			}
		})
	}
}
