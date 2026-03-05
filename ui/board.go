package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderBoard renders the kanban board in 2 or 3 column mode.
// scrollOffset and maxVisible control the visible window of cards per column.
func RenderBoard(agents []CardData, selected int, columns int, width, height, scrollOffset, maxVisible int) string {
	// Categorize agents
	var running, waiting, idle []CardData
	var runIdx, waitIdx, idleIdx []int

	for i, a := range agents {
		switch a.Status {
		case "RUNNING":
			running = append(running, a)
			runIdx = append(runIdx, i)
		case "WAITING", "STUCK":
			waiting = append(waiting, a)
			waitIdx = append(waitIdx, i)
		case "IDLE", "DONE":
			idle = append(idle, a)
			idleIdx = append(idleIdx, i)
		}
	}

	if columns == 2 {
		return render2Col(agents, running, waiting, idle, runIdx, waitIdx, idleIdx, selected, width, height, scrollOffset, maxVisible)
	}
	return render3Col(agents, running, waiting, idle, runIdx, waitIdx, idleIdx, selected, width, height, scrollOffset, maxVisible)
}

func render3Col(agents []CardData, running, waiting, idle []CardData, runIdx, waitIdx, idleIdx []int, selected, width, height, scrollOffset, maxVisible int) string {
	colWidth := (width - 6) / 3
	if colWidth < 20 {
		colWidth = 20
	}

	// Headers
	hdrRun := ColumnHeader.Foreground(ColorRunning).Render(fmt.Sprintf("■ RUNNING [%d]", len(running)))
	hdrWait := ColumnHeader.Foreground(ColorWaiting).Render(fmt.Sprintf("■ WAITING [%d]", len(waiting)))
	hdrIdle := ColumnHeader.Foreground(ColorIdle).Render(fmt.Sprintf("■ IDLE [%d]", len(idle)))

	hdrRun = lipgloss.NewStyle().Width(colWidth).Render(hdrRun)
	hdrWait = lipgloss.NewStyle().Width(colWidth).Render(hdrWait)
	hdrIdle = lipgloss.NewStyle().Width(colWidth).Render(hdrIdle)

	header := lipgloss.JoinHorizontal(lipgloss.Top, hdrIdle, " ", hdrWait, " ", hdrRun)

	// Cards per column (only the visible slice)
	col1 := renderColumnCards(idle, idleIdx, selected, colWidth, scrollOffset, maxVisible)
	col2 := renderColumnCards(waiting, waitIdx, selected, colWidth, scrollOffset, maxVisible)
	col3 := renderColumnCards(running, runIdx, selected, colWidth, scrollOffset, maxVisible)

	if len(idle) == 0 {
		col1 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No idle agents")
	}
	if len(waiting) == 0 {
		col2 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No waiting agents")
	}
	if len(running) == 0 {
		col3 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No running agents")
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, col1, " ", col2, " ", col3)

	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

func render2Col(agents []CardData, running, waiting, idle []CardData, runIdx, waitIdx, idleIdx []int, selected, width, height, scrollOffset, maxVisible int) string {
	colWidth := (width - 4) / 2
	if colWidth < 25 {
		colWidth = 25
	}

	// Active = running + waiting
	var active []CardData
	var activeIdx []int
	active = append(active, running...)
	activeIdx = append(activeIdx, runIdx...)
	active = append(active, waiting...)
	activeIdx = append(activeIdx, waitIdx...)

	hdrActive := ColumnHeader.Foreground(ColorAccent).Render(fmt.Sprintf("■ ACTIVE [%d]", len(active)))
	hdrIdle := ColumnHeader.Foreground(ColorIdle).Render(fmt.Sprintf("■ IDLE [%d]", len(idle)))

	hdrActive = lipgloss.NewStyle().Width(colWidth).Render(hdrActive)
	hdrIdle = lipgloss.NewStyle().Width(colWidth).Render(hdrIdle)

	header := lipgloss.JoinHorizontal(lipgloss.Top, hdrIdle, " ", hdrActive)

	col1 := renderColumnCards(idle, idleIdx, selected, colWidth, scrollOffset, maxVisible)
	col2 := renderColumnCards(active, activeIdx, selected, colWidth, scrollOffset, maxVisible)

	if len(idle) == 0 {
		col1 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No idle agents")
	}
	if len(active) == 0 {
		col2 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No active agents")
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, col1, " ", col2)

	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

func renderColumnCards(cards []CardData, indices []int, selected, width, scrollOffset, maxVisible int) string {
	if len(cards) == 0 {
		return ""
	}
	start := scrollOffset
	if start > len(cards) {
		start = len(cards)
	}
	end := start + maxVisible
	if end > len(cards) {
		end = len(cards)
	}
	if start >= end {
		return ""
	}
	var rendered []string
	for i := start; i < end; i++ {
		cards[i].Selected = indices[i] == selected
		rendered = append(rendered, RenderCard(cards[i], width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rendered...)
}

// RenderTitle renders the title bar.
// activeWorkspace is shown in parentheses next to the title when non-empty.
// updateVersion is shown as a bordered badge next to the title when non-empty (e.g. "0.6.0").
func RenderTitle(width int, agentCount int, mode int, updateVersion string, activeWorkspace string) string {
	titleText := "TicketTok"
	if activeWorkspace != "" {
		titleText += fmt.Sprintf(" (%s)", activeWorkspace)
	}
	title := TitleBar.Render(titleText)

	if updateVersion != "" {
		badge := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d97706")).
			Bold(true).
			Render(fmt.Sprintf("(%s available — [U] to update)", updateVersion))
		title += " " + badge
	}

	modeStr := fmt.Sprintf("[%d-col]", mode)
	count := DimText.Render(fmt.Sprintf("%d agents", agentCount))
	right := lipgloss.JoinHorizontal(lipgloss.Top, count, "  ", DimText.Render(modeStr))

	gap := width - lipgloss.Width(title) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}

	return lipgloss.JoinHorizontal(lipgloss.Top,
		title,
		strings.Repeat(" ", gap),
		right,
	)
}

// RenderFooter renders the keybindings help footer.
// When updateAvailable is true, an [U]pdate hint is appended.
func RenderFooter(width int, mode int, updateAvailable bool) string {
	var keys string
	switch mode {
	case 1:
		keys = "[↑/↓]Nav  [N]ew  [Enter]Zoom  [X]Kill  [S]end  [A]uto-approve  [B]atch  [D]iscover  [C]lear  [W]orkspace  [1/2/3]Mode  [Q]uit"
	default:
		keys = "[↑/↓]Nav  [←/→]Column  [N]ew  [Enter]Zoom  [X]Kill  [S]end  [A]uto-approve  [B]atch  [D]iscover  [C]lear  [W]orkspace  [1/2/3]Mode  [Q]uit"
	}
	if updateAvailable {
		keys += "  [U]pdate"
	}
	return FooterStyle.Width(width).Render(HelpStyle.Render(keys))
}
