package ui

import "github.com/charmbracelet/lipgloss"

var (
	// Status colors
	ColorRunning = lipgloss.Color("#22c55e") // green
	ColorWaiting = lipgloss.Color("#ef4444") // red
	ColorIdle    = lipgloss.Color("#f97316") // orange
	ColorDone    = lipgloss.Color("#6b7280") // gray
	ColorAccent  = lipgloss.Color("#06b6d4") // cyan
	ColorDim     = lipgloss.Color("#4b5563") // dim gray
	ColorWhite   = lipgloss.Color("#f9fafb")
	ColorBg      = lipgloss.Color("#1a1a2e")
	ColorCardBg  = lipgloss.Color("#16213e")
	ColorBorder  = lipgloss.Color("#374151")

	// Badge styles
	BadgeRunning = lipgloss.NewStyle().
			Background(ColorRunning).
			Foreground(lipgloss.Color("#000000")).
			Bold(true).
			Padding(0, 1)

	BadgeWaiting = lipgloss.NewStyle().
			Background(ColorWaiting).
			Foreground(ColorWhite).
			Bold(true).
			Padding(0, 1)

	BadgeIdle = lipgloss.NewStyle().
			Background(ColorIdle).
			Foreground(lipgloss.Color("#000000")).
			Bold(true).
			Padding(0, 1)

	BadgeDone = lipgloss.NewStyle().
			Background(ColorDone).
			Foreground(ColorWhite).
			Padding(0, 1)

	// Card styles
	CardSelected = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent).
			Padding(0, 1)

	CardNormal = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1)

	// Column header styles
	ColumnHeader = lipgloss.NewStyle().
			Bold(true).
			Padding(0, 1)

	// Title bar
	TitleBar = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorAccent).
			Padding(0, 1)

	// Footer / help
	HelpStyle = lipgloss.NewStyle().
			Foreground(ColorDim)

	FooterStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(ColorBorder).
			Padding(0, 1)

	// Dim text
	DimText = lipgloss.NewStyle().
		Foreground(ColorDim)

	// Agent name
	AgentName = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorWhite)

	// Preview text
	PreviewText = lipgloss.NewStyle().
			Foreground(ColorDim)

	// Carousel-specific
	CarouselCard = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(ColorAccent).
			Padding(1, 2)

	// Separator line
	Separator = lipgloss.NewStyle().
			Foreground(ColorBorder)
)

func StatusBadge(status string) string {
	switch status {
	case "RUNNING":
		return BadgeRunning.Render("IN-PROGRESS")
	case "WAITING":
		return BadgeWaiting.Render("WAITING")
	case "IDLE":
		return BadgeIdle.Render("IDLE")
	case "DONE":
		return BadgeDone.Render("DONE")
	default:
		return BadgeDone.Render(status)
	}
}

func StatusDot(status string) string {
	switch status {
	case "RUNNING":
		return lipgloss.NewStyle().Foreground(ColorRunning).Render("●")
	case "WAITING":
		return lipgloss.NewStyle().Foreground(ColorWaiting).Render("▲")
	case "IDLE":
		return lipgloss.NewStyle().Foreground(ColorIdle).Render("○")
	case "DONE":
		return lipgloss.NewStyle().Foreground(ColorDone).Render("✓")
	default:
		return "·"
	}
}
