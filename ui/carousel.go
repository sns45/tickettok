package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// RenderCarousel renders the 1-column carousel view showing only visible cards.
func RenderCarousel(agents []CardData, pos int, width, height, scrollOffset, maxVisible int) string {
	if len(agents) == 0 {
		return DimText.Render("No agents. Press N to spawn one.")
	}
	start := scrollOffset
	if start > len(agents) {
		start = len(agents)
	}
	end := start + maxVisible
	if end > len(agents) {
		end = len(agents)
	}
	if start >= end {
		return ""
	}
	var rendered []string
	for i := start; i < end; i++ {
		agents[i].Selected = i == pos
		rendered = append(rendered, RenderCard(agents[i], width-2))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rendered...)
}
