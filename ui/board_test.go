package ui

import (
	"strings"
	"testing"
)

func TestRenderTitle(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		agentCount int
		mode       int
	}{
		{"3-col with agents", 120, 5, 3},
		{"2-col no agents", 80, 0, 2},
		{"1-col carousel", 100, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderTitle(tt.width, tt.agentCount, tt.mode)
			if !strings.Contains(got, "TicketTok") {
				t.Error("RenderTitle does not contain 'TicketTok'")
			}
			if !strings.Contains(got, "agents") {
				t.Error("RenderTitle does not contain agent count")
			}
			if !strings.Contains(got, "-col") {
				t.Error("RenderTitle does not contain column mode")
			}
		})
	}
}

func TestRenderFooter(t *testing.T) {
	t.Run("carousel mode omits Column nav", func(t *testing.T) {
		got := RenderFooter(120, 1)
		if strings.Contains(got, "Column") {
			t.Error("RenderFooter(mode=1) should not contain 'Column' nav")
		}
		if !strings.Contains(got, "Nav") {
			t.Error("RenderFooter(mode=1) should contain 'Nav'")
		}
	})

	t.Run("board mode includes Column nav", func(t *testing.T) {
		got := RenderFooter(120, 3)
		if !strings.Contains(got, "Column") {
			t.Error("RenderFooter(mode=3) should contain 'Column' nav")
		}
	})
}
