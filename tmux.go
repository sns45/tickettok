package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
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

// DetectStatusFromContent determines agent status from tmux pane content.
func DetectStatusFromContent(content string) AgentStatus {
	lines := strings.Split(content, "\n")

	// Check from bottom up, skip blank lines
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(stripAnsiStr(lines[i]))
		if line == "" {
			continue
		}

		lower := strings.ToLower(line)

		// WAITING: permission prompts
		for _, p := range []string{"allow", "permission", "approve", "y/n", "yes/no", "accept", "deny", "confirm"} {
			if strings.Contains(lower, p) {
				return StatusWaiting
			}
		}

		// IDLE: at a prompt
		if line == ">" || line == "$" || strings.HasSuffix(line, "> ") || strings.HasSuffix(line, "$ ") || strings.Contains(line, "❯") {
			return StatusIdle
		}

		// RUNNING: actively processing
		if strings.Contains(lower, "esc to interrupt") {
			return StatusRunning
		}

		// DONE: session ended
		for _, p := range []string{"exited", "goodbye", "session ended", "bye"} {
			if strings.Contains(lower, p) {
				return StatusDone
			}
		}

		// Non-empty line that didn't match — assume running
		return StatusRunning
	}

	return StatusRunning
}

// PreviewFromContent extracts the last n meaningful lines from pane content.
func PreviewFromContent(content string, n int) []string {
	lines := strings.Split(content, "\n")
	var result []string
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		line := strings.TrimSpace(stripAnsiStr(lines[i]))
		if line != "" && len(line) > 2 {
			result = append([]string{line}, result...)
		}
	}
	return result
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
