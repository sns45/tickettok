package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// CardData holds the display data for an agent card.
type CardData struct {
	Name     string
	Dir      string
	Status   string
	Uptime   time.Duration
	Since    time.Duration
	Preview  []string
	Selected bool
}

// RenderCard renders a single agent card at the given width.
func RenderCard(d CardData, width int) string {
	style := CardNormal
	if d.Selected {
		style = CardSelected
	}
	style = style.Width(width - 2) // account for border

	badge := StatusBadge(d.Status)
	name := AgentName.Render(d.Name)
	header := lipgloss.JoinHorizontal(lipgloss.Top, name, "  ", badge)

	// Project dir (shortened)
	dir := shortenDir(d.Dir)
	dirLine := DimText.Render("DIR: " + dir)

	// Uptime
	uptimeLine := statusTimeLine(d.Status, d.Uptime, d.Since)

	// Separator
	inner := width - 6 // border + padding
	if inner < 10 {
		inner = 10
	}
	sep := Separator.Render(strings.Repeat("─", inner))

	// Preview
	var previewStr string
	if len(d.Preview) > 0 {
		lines := d.Preview
		maxLines := 1
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		for i, l := range lines {
			if len(l) > inner {
				lines[i] = l[:inner-1] + "…"
			}
		}
		previewStr = PreviewText.Render(strings.Join(lines, "\n"))
	} else {
		previewStr = DimText.Render("(no output yet)")
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		header,
		dirLine,
		uptimeLine,
		sep,
		previewStr,
	)

	return style.Render(content)
}

// RenderCarouselCard renders an expanded card for carousel mode.
func RenderCarouselCard(d CardData, width int, previewLines int) string {
	style := CarouselCard.Width(width - 4)

	badge := StatusBadge(d.Status)
	name := AgentName.Render(d.Name)
	header := lipgloss.JoinHorizontal(lipgloss.Top, name, "  ", badge)

	dir := shortenDir(d.Dir)
	dirLine := DimText.Render("PROJECT: " + dir)

	uptimeLine := statusTimeLine(d.Status, d.Uptime, d.Since)

	inner := width - 8
	if inner < 10 {
		inner = 10
	}
	sep := Separator.Render(strings.Repeat("─", inner))

	// Extended preview
	var previewStr string
	if len(d.Preview) > 0 {
		lines := d.Preview
		if len(lines) > previewLines {
			lines = lines[len(lines)-previewLines:]
		}
		for i, l := range lines {
			if len(l) > inner {
				lines[i] = l[:inner-1] + "…"
			}
		}
		previewStr = PreviewText.Render(strings.Join(lines, "\n"))
	} else {
		previewStr = DimText.Render("(no output yet)")
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		header,
		dirLine,
		uptimeLine,
		sep,
		previewStr,
	)

	return style.Render(content)
}

func shortenDir(dir string) string {
	home := fmt.Sprintf("%s/", homeDir())
	if strings.HasPrefix(dir, home) {
		return "~/" + strings.TrimPrefix(dir, home)
	}
	return dir
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func statusTimeLine(status string, uptime, since time.Duration) string {
	dur := formatDuration(since)
	switch status {
	case "RUNNING":
		return lipgloss.NewStyle().Foreground(ColorRunning).Render("IN-PROGRESS: " + dur)
	case "WAITING":
		return lipgloss.NewStyle().Foreground(ColorWaiting).Bold(true).Render("WAITING: " + dur)
	case "IDLE":
		return lipgloss.NewStyle().Foreground(ColorIdle).Render("IDLE: " + dur)
	case "DONE":
		return DimText.Render("DONE: " + dur + " ago")
	default:
		return DimText.Render("UPTIME: " + formatDuration(uptime))
	}
}
