package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	pty "github.com/creack/pty/v2"
)

const sessionPrefix = "tickettok_"

// TmuxSession manages a tmux session running a claude process.
type TmuxSession struct {
	Name string   // e.g. "tickettok_1"
	ptmx *os.File // PTY master running "tmux attach-session"
}

// attachPty creates a persistent PTY connection acting as a virtual client.
func (t *TmuxSession) attachPty() error {
	// Use manual window-size so resize-window has full control (not constrained by client min).
	_ = exec.Command("tmux", "set-option", "-t", t.Name, "window-size", "manual").Run()
	// Detach any stale clients (e.g. leaked from a previous crash) via -d.
	cmd := exec.Command("tmux", "attach-session", "-d", "-t", t.Name)
	cmd.Env = append(filteredEnv(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		return fmt.Errorf("pty attach: %w", err)
	}
	t.ptmx = ptmx
	// Force window to known size (manual mode won't auto-adjust from client).
	_ = exec.Command("tmux", "resize-window", "-t", t.Name, "-x", "200", "-y", "50").Run()
	go io.Copy(io.Discard, ptmx) // drain stdout to prevent buffer blockage
	return nil
}

// closePty closes the PTY master fd if open.
func (t *TmuxSession) closePty() {
	if t.ptmx != nil {
		t.ptmx.Close()
		t.ptmx = nil
	}
}

// SessionName returns the tmux session name for an agent ID.
func SessionName(id string) string {
	return sessionPrefix + id
}

// CreateSession starts a new detached tmux session running claude.
func CreateSession(name, workDir string, args []string) (*TmuxSession, error) {
	// Wrap with env -u to strip CLAUDECODE so Claude doesn't refuse to start
	// ("cannot be launched inside another Claude Code session").
	program := "env -u CLAUDECODE claude"
	if len(args) > 0 {
		program = "env -u CLAUDECODE claude " + strings.Join(args, " ")
	}

	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-x", "200", "-y", "50", "-c", workDir, program)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tmux new-session: %s: %w", strings.TrimSpace(string(out)), err)
	}

	sess := &TmuxSession{Name: name}
	if err := sess.attachPty(); err != nil {
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
		return nil, fmt.Errorf("pty attach after create: %w", err)
	}
	return sess, nil
}

// IsAlive checks if the tmux session still exists.
func (t *TmuxSession) IsAlive() bool {
	return exec.Command("tmux", "has-session", "-t", t.Name).Run() == nil
}

// Kill destroys the tmux session.
func (t *TmuxSession) Kill() error {
	t.closePty()
	return exec.Command("tmux", "kill-session", "-t", t.Name).Run()
}

// SendKeys sends keystrokes to the tmux pane.
func (t *TmuxSession) SendKeys(keys string) error {
	return exec.Command("tmux", "send-keys", "-t", t.Name, keys, "Enter").Run()
}

// CapturePaneContent returns the current visible content of the tmux pane
// with ANSI colors preserved.
func (t *TmuxSession) CapturePaneContent() (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-J", "-t", t.Name).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SetSize resizes the tmux pane to match the given dimensions.
func (t *TmuxSession) SetSize(cols, rows int) error {
	if t.ptmx != nil {
		_ = pty.Setsize(t.ptmx, &pty.Winsize{
			Rows: uint16(rows), Cols: uint16(cols),
		})
	}
	return exec.Command("tmux", "resize-window", "-t", t.Name, "-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows)).Run()
}

// deriveNameFromDir returns a short agent name based on the git repo or directory basename.
func deriveNameFromDir(dir string) string {
	// Try git repo root name
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		if name := filepath.Base(strings.TrimSpace(string(out))); name != "" && name != "." {
			return name
		}
	}
	// Fall back to directory basename
	if name := filepath.Base(dir); name != "" && name != "." && name != "/" {
		return name
	}
	return "agent"
}

// --- Discovery ---

// DiscoveredAgent represents a claude instance found via tmux or process scan.
type DiscoveredAgent struct {
	Name        string
	Dir         string
	SessionName string
	PaneID      string
	PID         int
}

// ANSI strip regex for status detection
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripAnsiStr(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// hasDingbat returns true if the string contains a Unicode Dingbat character (U+2700-U+27BF).
// Claude Code uses rotating dingbats (✢, ✶, ✻, etc.) for its spinner animation.
func hasDingbat(s string) bool {
	for _, r := range s {
		if r >= '\u2700' && r <= '\u27BF' {
			return true
		}
	}
	return false
}

// DetectStatusFromContent determines agent status from tmux pane content.
// Scans the last 15 non-blank lines for reliable detection.
// Priority: RUNNING > WAITING > IDLE > DONE > default RUNNING.
func DetectStatusFromContent(content string) AgentStatus {
	lines := strings.Split(content, "\n")

	// Collect last 15 non-blank lines (bottom-up)
	var recent []string
	for i := len(lines) - 1; i >= 0 && len(recent) < 15; i-- {
		line := strings.TrimSpace(stripAnsiStr(lines[i]))
		if line != "" {
			recent = append(recent, line)
		}
	}

	if len(recent) == 0 {
		return StatusRunning
	}

	// RUNNING = actively processing (check first — most specific signals)
	// "esc to interrupt" appears at the bottom while Claude is executing.
	// "Running…" appears next to tool output while a tool is in progress.
	// Spinner uses rotating Unicode dingbats (U+2700–U+27BF): ✢, ✶, ✻, etc.
	for _, line := range recent {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "esc to interrupt") {
			return StatusRunning
		}
		if strings.Contains(lower, "running…") || strings.Contains(lower, "running...") {
			return StatusRunning
		}
		// Active spinner: any Unicode dingbat (U+2700-U+27BF) + ellipsis
		hasEllipsis := strings.Contains(line, "…") || strings.Contains(line, "...")
		if hasEllipsis && hasDingbat(line) {
			return StatusRunning
		}
	}

	// WAITING = permission prompts and interactive TUI selectors
	for _, line := range recent {
		lower := strings.ToLower(line)
		for _, p := range []string{
			"allow once", "allow always",
			"enter to select", "space to select",
			"yes/no/always allow",
			"do you want to proceed",
			"shall i proceed", "should i proceed",
			"approve", "deny", "reject",
			"(y)es", "(n)o", "y/n", "yes/no",
			"ctrl+g to edit",
		} {
			if strings.Contains(lower, p) {
				return StatusWaiting
			}
		}
	}

	// IDLE = at the prompt, waiting for next input
	for _, line := range recent {
		lower := strings.ToLower(line)
		if line == ">" || line == "$" ||
			strings.HasSuffix(line, "> ") ||
			strings.HasSuffix(line, "$ ") ||
			strings.Contains(line, "❯") ||
			strings.Contains(lower, "? for shortcuts") ||
			strings.Contains(lower, "has completed") ||
			strings.Contains(lower, "anything else") ||
			strings.Contains(lower, "can i help") {
			return StatusIdle
		}
	}

	// DONE = session ended
	bottom := recent[0]
	bottomLower := strings.ToLower(bottom)
	for _, p := range []string{"exited", "goodbye", "session ended", "bye"} {
		if strings.Contains(bottomLower, p) {
			return StatusDone
		}
	}

	// Default: assume running (active output)
	return StatusRunning
}

// PreviewFromContent extracts the last n meaningful lines from pane content,
// stripping bottom chrome (separator, prompt, state line) first.
// When waiting is true, the ❯ selection UI is kept — only separator lines
// and the last line are removed.
func PreviewFromContent(content string, n int, waiting bool) []string {
	lines := strings.Split(content, "\n")
	if waiting {
		lines = stripWaitingChrome(lines)
	} else {
		lines = stripChromeLines(lines)
	}
	var result []string
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		line := strings.TrimSpace(stripAnsiStr(lines[i]))
		if line != "" && len(line) > 2 {
			result = append([]string{line}, result...)
		}
	}
	return result
}

// stripWaitingChrome removes only separator lines and the last non-blank line,
// keeping the ❯ selection UI visible for WAITING agents.
func stripWaitingChrome(lines []string) []string {
	// Filter out separator lines
	var filtered []string
	for _, l := range lines {
		stripped := strings.TrimSpace(stripAnsiStr(l))
		if !isSeparatorLine(stripped) {
			filtered = append(filtered, l)
		}
	}

	// Drop the last non-blank line (status/chrome line)
	for i := len(filtered) - 1; i >= 0; i-- {
		stripped := strings.TrimSpace(stripAnsiStr(filtered[i]))
		if stripped != "" {
			filtered = append(filtered[:i], filtered[i+1:]...)
			break
		}
	}

	return filtered
}

// stripChromeLines removes Claude Code's bottom chrome (separator, ❯ prompt,
// state line) from captured pane lines.
func stripChromeLines(lines []string) []string {
	// Find last ❯ line (bottom-up)
	promptIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		stripped := strings.TrimSpace(stripAnsiStr(lines[i]))
		if strings.HasPrefix(stripped, "❯") {
			promptIdx = i
			break
		}
	}
	if promptIdx < 0 {
		return lines
	}
	// Find separator above ❯
	for i := promptIdx - 1; i >= 0; i-- {
		stripped := strings.TrimSpace(stripAnsiStr(lines[i]))
		if isSeparatorLine(stripped) {
			return lines[:i]
		}
	}
	return lines[:promptIdx]
}

// isSeparatorLine returns true if the string is a horizontal rule made of ─ or - chars.
func isSeparatorLine(s string) bool {
	if len(s) < 10 {
		return false
	}
	for _, r := range s {
		if r != '─' && r != '-' {
			return false
		}
	}
	return true
}

// DetectModeFromContent scans the last few non-blank lines of pane content
// for Claude Code mode indicators and returns a short tag string.
func DetectModeFromContent(content string) string {
	lines := strings.Split(content, "\n")

	// Collect last 5 non-blank lines (bottom-up)
	var recent []string
	for i := len(lines) - 1; i >= 0 && len(recent) < 5; i-- {
		line := strings.TrimSpace(stripAnsiStr(lines[i]))
		if line != "" {
			recent = append(recent, line)
		}
	}

	for _, line := range recent {
		lower := strings.ToLower(line)
		// Skip lines that describe leaving a mode (e.g. "Exited Plan Mode")
		if strings.Contains(lower, "exit") {
			continue
		}
		if strings.Contains(lower, "accept edits") || strings.Contains(line, "⏵⏵") {
			return "EDITS"
		}
		if strings.Contains(lower, "plan mode") || (strings.Contains(line, "⏸") && strings.Contains(lower, "plan")) {
			return "PLAN"
		}
	}
	return ""
}

// discoverTmuxClaude finds tmux sessions running claude (excluding our own sessions).
func discoverTmuxClaude() []DiscoveredAgent {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}

	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}|#{session_path}|#{pane_current_command}").Output()
	if err != nil {
		return nil
	}

	var found []DiscoveredAgent
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		sessName := parts[0]
		dir := parts[1]

		// Skip our own managed sessions
		if strings.HasPrefix(sessName, sessionPrefix) {
			continue
		}

		// Check if this session has claude
		content, err := exec.Command("tmux", "capture-pane", "-p", "-t", sessName).Output()
		if err != nil {
			continue
		}
		lower := strings.ToLower(string(content))
		if !strings.Contains(lower, "claude") && !strings.Contains(lower, "anthropic") {
			// Also check the pane command
			paneCmd := parts[2]
			if !strings.Contains(strings.ToLower(paneCmd), "claude") {
				continue
			}
		}

		found = append(found, DiscoveredAgent{
			Name:        "tmux-" + sessName,
			Dir:         dir,
			SessionName: sessName,
		})
	}

	return found
}

// discoverProcesses finds claude processes not managed by us.
func discoverProcesses() []DiscoveredAgent {
	out, err := exec.Command("pgrep", "-af", "claude").Output()
	if err != nil {
		return nil
	}

	var found []DiscoveredAgent
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue
		}

		var pid int
		fmt.Sscanf(parts[0], "%d", &pid)

		if !strings.Contains(parts[1], "claude") {
			continue
		}

		dir := getCwd(pid)
		if dir == "" {
			dir = "unknown"
		}

		found = append(found, DiscoveredAgent{
			Name: fmt.Sprintf("proc-%d", pid),
			Dir:  dir,
			PID:  pid,
		})
	}
	return found
}

// filteredEnv returns os.Environ() with CLAUDECODE stripped so nested
// Claude sessions don't refuse to start.
func filteredEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			env = append(env, e)
		}
	}
	return env
}

func getCwd(pid int) string {
	out, err := exec.Command("lsof", "-p", fmt.Sprintf("%d", pid), "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") && strings.Contains(line, "/") {
			path := line[1:]
			if strings.HasPrefix(path, "/") && !strings.Contains(path, ".") {
				return path
			}
		}
	}
	return ""
}
