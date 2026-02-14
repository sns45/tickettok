package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderCarousel renders the 1-column carousel view.
func RenderCarousel(agents []CardData, pos int, width, height int) string {
	if len(agents) == 0 {
		empty := lipgloss.NewStyle().
			Width(width - 4).
			Align(lipgloss.Center).
			Foreground(ColorDim).
			Render("\n\n  No agents yet. Press [N] to spawn one.\n\n")
		return empty
	}

	// Current agent
	agent := agents[pos]
	agent.Selected = true

	// Position indicator
	posStr := DimText.Render(fmt.Sprintf("[%d/%d]", pos+1, len(agents)))

	// Status badge + position on same line
	badge := StatusBadge(agent.Status)
	header := lipgloss.JoinHorizontal(lipgloss.Top,
		"  ", badge, strings.Repeat(" ", max(1, width-lipgloss.Width(badge)-lipgloss.Width(posStr)-8)), posStr, "  ",
	)

	// Main card with extended preview
	previewLines := height - 14
	if previewLines < 3 {
		previewLines = 3
	}
	if previewLines > 8 {
		previewLines = 8
	}
	card := RenderCarouselCard(agent, width-4, previewLines)

	// Dot indicators showing all agents' statuses
	var dots []string
	for _, a := range agents {
		dots = append(dots, StatusDot(a.Status))
	}
	dotLine := lipgloss.NewStyle().Align(lipgloss.Center).Width(width - 4).Render(strings.Join(dots, " "))

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		card,
		dotLine,
	)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
