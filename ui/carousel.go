package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// RenderCarousel renders the 1-column carousel view as a vertical stack of all cards.
func RenderCarousel(agents []CardData, pos int, width, height int) string {
	if len(agents) == 0 {
		return DimText.Render("No agents. Press N to spawn one.")
	}
	var rendered []string
	for i, a := range agents {
		a.Selected = i == pos
		rendered = append(rendered, RenderCard(a, width-2))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rendered...)
}
