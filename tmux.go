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

// TmuxSession manages a tmux session running an agent process.
type TmuxSession struct {
	Name     string   // e.g. "tickettok_1"
	ptmx     *os.File // PTY master running "tmux attach-session"
	stripEnv []string // env var prefixes to strip when attaching
}

// attachPty creates a persistent PTY connection acting as a virtual client.
func (t *TmuxSession) attachPty() error {
	// Use manual window-size so resize-window has full control (not constrained by client min).
	_ = exec.Command("tmux", "set-option", "-t", t.Name, "window-size", "manual").Run()
	// Detach any stale clients (e.g. leaked from a previous crash) via -d.
	cmd := exec.Command("tmux", "attach-session", "-d", "-t", t.Name)
	cmd.Env = append(filteredEnv(t.stripEnv), "TERM=xterm-256color")
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

// CreateSession starts a new detached tmux session running the given command.
// stripEnv lists environment variable prefixes to strip via `env -u`.
func CreateSession(name, workDir, command string, stripEnv []string) (*TmuxSession, error) {
	program := command
	for _, v := range stripEnv {
		program = "env -u " + v + " " + program
	}

	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-x", "200", "-y", "50", "-c", workDir, program)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tmux new-session: %s: %w", strings.TrimSpace(string(out)), err)
	}

	sess := &TmuxSession{Name: name, stripEnv: stripEnv}
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

// CapturePane captures tmux pane content by session name without PTY attachment.
// Includes ANSI color codes (-e) for rendering in zoom/preview.
func CapturePane(sessionName string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-J", "-t", sessionName).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// CapturePanePlain captures tmux pane content as plain text (no ANSI codes).
// Used for discovery content checks where color codes interfere with matching.
func CapturePanePlain(sessionName string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-J", "-t", sessionName).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// IsSessionAlive checks if a tmux session exists by name (standalone, no PTY needed).
func IsSessionAlive(sessionName string) bool {
	return exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil
}

// --- Discovery ---

// DiscoveredAgent represents an agent instance found via tmux or process scan.
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

// PreviewFromContent extracts the last n meaningful lines from pane content.
// stripFn removes backend-specific chrome from the raw lines before extraction.
func PreviewFromContent(content string, n int, stripFn func([]string) []string) []string {
	lines := strings.Split(content, "\n")
	if stripFn != nil {
		lines = stripFn(lines)
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

// filteredEnv returns os.Environ() with the given variable prefixes stripped.
func filteredEnv(stripPrefixes []string) []string {
	var env []string
	for _, e := range os.Environ() {
		skip := false
		for _, prefix := range stripPrefixes {
			if strings.HasPrefix(e, prefix+"=") {
				skip = true
				break
			}
		}
		if !skip {
			env = append(env, e)
		}
	}
	return env
}

// GetPaneTitle reads the tmux pane title (set by OSC 2 escape sequences).
// Claude Code emits these to describe what it's working on.
func GetPaneTitle(sessionName string) string {
	out, err := exec.Command("tmux", "display-message", "-p",
		"-t", sessionName, "#{pane_title}").Output()
	if err != nil {
		return ""
	}
	title := strings.TrimSpace(string(out))
	// Strip leading dingbat characters (Claude Code spinner: ✢, ✶, ✻, ✳, etc.)
	title = strings.TrimLeftFunc(title, func(r rune) bool {
		return r >= '\u2700' && r <= '\u27BF'
	})
	title = strings.TrimSpace(title)
	if isDefaultPaneTitle(title) {
		return ""
	}
	return title
}

// isDefaultPaneTitle returns true for shell defaults and hostname-like values
// that aren't meaningful Claude-set titles.
func isDefaultPaneTitle(s string) bool {
	if s == "" {
		return true
	}
	lower := strings.ToLower(s)
	for _, d := range []string{"bash", "zsh", "fish", "sh", "login"} {
		if lower == d {
			return true
		}
	}
	// Claude-set titles always contain spaces; single-word short strings are defaults
	if !strings.Contains(s, " ") && len(s) < 30 {
		return true
	}
	return false
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
