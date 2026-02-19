package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderBoard renders the kanban board in 2 or 3 column mode.
func RenderBoard(agents []CardData, selected int, columns int, width, height int) string {
	// Categorize agents
	var running, waiting, idle []CardData
	var runIdx, waitIdx, idleIdx []int

	for i, a := range agents {
		switch a.Status {
		case "RUNNING":
			running = append(running, a)
			runIdx = append(runIdx, i)
		case "WAITING":
			waiting = append(waiting, a)
			waitIdx = append(waitIdx, i)
		case "IDLE", "DONE":
			idle = append(idle, a)
			idleIdx = append(idleIdx, i)
		}
	}

	if columns == 2 {
		return render2Col(agents, running, waiting, idle, runIdx, waitIdx, idleIdx, selected, width, height)
	}
	return render3Col(agents, running, waiting, idle, runIdx, waitIdx, idleIdx, selected, width, height)
}

func render3Col(agents []CardData, running, waiting, idle []CardData, runIdx, waitIdx, idleIdx []int, selected, width, height int) string {
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

	// Cards per column
	col1 := renderColumnCards(idle, idleIdx, selected, colWidth)
	col2 := renderColumnCards(waiting, waitIdx, selected, colWidth)
	col3 := renderColumnCards(running, runIdx, selected, colWidth)

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

func render2Col(agents []CardData, running, waiting, idle []CardData, runIdx, waitIdx, idleIdx []int, selected, width, height int) string {
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

	col1 := renderColumnCards(idle, idleIdx, selected, colWidth)
	col2 := renderColumnCards(active, activeIdx, selected, colWidth)

	if len(idle) == 0 {
		col1 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No idle agents")
	}
	if len(active) == 0 {
		col2 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No active agents")
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, col1, " ", col2)

	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

func renderColumnCards(cards []CardData, indices []int, selected, width int) string {
	if len(cards) == 0 {
		return ""
	}
	var rendered []string
	for i, c := range cards {
		c.Selected = indices[i] == selected
		rendered = append(rendered, RenderCard(c, width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rendered...)
}

// RenderTitle renders the title bar.
func RenderTitle(width int, agentCount int, mode int) string {
	title := TitleBar.Render("TicketTok")
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
func RenderFooter(width int, mode int) string {
	var keys string
	switch mode {
	case 1:
		keys = "[←/→]Cycle  [N]ew  [Enter]Zoom  [K]ill  [S]end  [D]iscover  [C]lear  [1/2/3]Mode  [Q]uit"
	default:
		keys = "[↑/↓]Nav  [N]ew  [Enter]Zoom  [K]ill  [S]end  [D]iscover  [C]lear  [1/2/3]Mode  [Q]uit"
	}
	return FooterStyle.Width(width).Render(HelpStyle.Render(keys))
}
