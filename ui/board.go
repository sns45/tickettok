package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderBoard renders the kanban board in 2 or 3 column mode.
func RenderBoard(agents []CardData, selected int, columns int, width, height int) string {
	// Categorize agents
	var running, idle, done []CardData
	var runIdx, idleIdx, doneIdx []int

	for i, a := range agents {
		switch a.Status {
		case "RUNNING", "WAITING":
			running = append(running, a)
			runIdx = append(runIdx, i)
		case "IDLE":
			idle = append(idle, a)
			idleIdx = append(idleIdx, i)
		case "DONE":
			done = append(done, a)
			doneIdx = append(doneIdx, i)
		}
	}

	if columns == 2 {
		return render2Col(agents, running, idle, done, runIdx, idleIdx, doneIdx, selected, width, height)
	}
	return render3Col(agents, running, idle, done, runIdx, idleIdx, doneIdx, selected, width, height)
}

func render3Col(agents []CardData, running, idle, done []CardData, runIdx, idleIdx, doneIdx []int, selected, width, height int) string {
	colWidth := (width - 6) / 3
	if colWidth < 20 {
		colWidth = 20
	}

	// Headers
	rc := len(running)
	ic := len(idle)
	dc := len(done)

	hdrRun := ColumnHeader.Foreground(ColorRunning).Render(fmt.Sprintf("■ RUNNING [%d]", rc))
	hdrIdle := ColumnHeader.Foreground(ColorIdle).Render(fmt.Sprintf("■ IDLE [%d]", ic))
	hdrDone := ColumnHeader.Foreground(ColorDone).Render(fmt.Sprintf("■ COMPLETED [%d]", dc))

	hdrRun = lipgloss.NewStyle().Width(colWidth).Render(hdrRun)
	hdrIdle = lipgloss.NewStyle().Width(colWidth).Render(hdrIdle)
	hdrDone = lipgloss.NewStyle().Width(colWidth).Render(hdrDone)

	header := lipgloss.JoinHorizontal(lipgloss.Top, hdrRun, " ", hdrIdle, " ", hdrDone)

	// Cards per column
	col1 := renderColumnCards(running, runIdx, selected, colWidth)
	col2 := renderColumnCards(idle, idleIdx, selected, colWidth)
	col3 := renderColumnCards(done, doneIdx, selected, colWidth)

	if len(running) == 0 {
		col1 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No running agents")
	}
	if len(idle) == 0 {
		col2 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No idle agents")
	}
	if len(done) == 0 {
		col3 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No completed agents")
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, col1, " ", col2, " ", col3)

	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

func render2Col(agents []CardData, running, idle, done []CardData, runIdx, idleIdx, doneIdx []int, selected, width, height int) string {
	colWidth := (width - 4) / 2
	if colWidth < 25 {
		colWidth = 25
	}

	// Active = running + idle
	var active []CardData
	var activeIdx []int
	active = append(active, running...)
	activeIdx = append(activeIdx, runIdx...)
	active = append(active, idle...)
	activeIdx = append(activeIdx, idleIdx...)

	ac := len(active)
	dc := len(done)

	hdrActive := ColumnHeader.Foreground(ColorAccent).Render(fmt.Sprintf("■ ACTIVE [%d]", ac))
	hdrDone := ColumnHeader.Foreground(ColorDone).Render(fmt.Sprintf("■ COMPLETED [%d]", dc))

	hdrActive = lipgloss.NewStyle().Width(colWidth).Render(hdrActive)
	hdrDone = lipgloss.NewStyle().Width(colWidth).Render(hdrDone)

	header := lipgloss.JoinHorizontal(lipgloss.Top, hdrActive, " ", hdrDone)

	col1 := renderColumnCards(active, activeIdx, selected, colWidth)
	col2 := renderColumnCards(done, doneIdx, selected, colWidth)

	if len(active) == 0 {
		col1 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No active agents")
	}
	if len(done) == 0 {
		col2 = lipgloss.NewStyle().Width(colWidth).Foreground(ColorDim).Render("\n  No completed agents")
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
