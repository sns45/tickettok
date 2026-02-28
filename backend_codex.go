package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CodexBackend implements Backend for OpenAI Codex CLI.
type CodexBackend struct{}

func init() {
	RegisterBackend(&CodexBackend{})
}

func (c *CodexBackend) Name() string { return "Codex" }
func (c *CodexBackend) ID() string   { return "codex" }

// SpawnCommand returns the shell command for launching Codex.
func (c *CodexBackend) SpawnCommand(args []string) (string, []string) {
	cmd := "codex"
	if len(args) > 0 {
		cmd = "codex " + strings.Join(args, " ")
	}
	return cmd, nil
}

// ResumeArgs returns empty — Codex has no --continue equivalent.
func (c *CodexBackend) ResumeArgs() []string {
	return nil
}

// CheckDeps verifies that the codex CLI is installed.
func (c *CodexBackend) CheckDeps() error {
	if _, err := exec.LookPath("codex"); err != nil {
		return fmt.Errorf("codex (npm i -g @openai/codex)")
	}
	return nil
}

// DetectStatus determines agent status from tmux pane content.
// Codex's status bar ("tokens used") is always visible, even while running.
// So we must check for RUNNING-specific indicators (like "esc to interrupt") before IDLE.
func (c *CodexBackend) DetectStatus(content string) AgentStatus {
	lines := strings.Split(content, "\n")

	var recent []string
	for i := len(lines) - 1; i >= 0 && len(recent) < 20; i-- {
		line := strings.TrimSpace(stripAnsiStr(lines[i]))
		if line != "" {
			recent = append(recent, line)
		}
	}

	if len(recent) == 0 {
		return StatusRunning
	}

	// DONE — check bottommost line first
	bottomLower := strings.ToLower(recent[0])
	for _, p := range []string{"exited", "goodbye", "session ended", "bye"} {
		if strings.Contains(bottomLower, p) {
			return StatusDone
		}
	}

	// RUNNING — Codex shows "esc to interrupt" during processing.
	// Must check before IDLE because "tokens used" status bar is always visible.
	for _, line := range recent {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "esc to interrupt") {
			return StatusRunning
		}
	}

	// WAITING — approval prompts (Codex uses y/n inline)
	for _, line := range recent {
		lower := strings.ToLower(line)
		for _, p := range []string{
			"approve", "deny", "allow",
			"yes/no", "y/n", "(y)es", "(n)o",
			"do you want to proceed",
			"permission", "/permissions",
		} {
			if strings.Contains(lower, p) {
				return StatusWaiting
			}
		}
	}

	// IDLE — Codex status bar shows "tokens used"; input placeholder shows "find and fix"
	for _, line := range recent {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "tokens used") ||
			strings.Contains(lower, "what would you like") ||
			strings.Contains(lower, "how can i help") ||
			line == ">" || line == "$" ||
			strings.HasSuffix(line, "> ") ||
			strings.HasSuffix(line, "$ ") {
			return StatusIdle
		}
	}

	// Default: RUNNING (agent is processing)
	return StatusRunning
}

// DetectMode returns empty — Codex doesn't have EDITS/PLAN modes.
func (c *CodexBackend) DetectMode(content string) string {
	return ""
}

// StripChrome returns lines as-is — Codex has minimal chrome to strip.
func (c *CodexBackend) StripChrome(lines []string, waiting bool) []string {
	return lines
}

// LooksLikeMe checks pane content for Codex UI signatures.
func (c *CodexBackend) LooksLikeMe(content string) bool {
	lower := strings.ToLower(stripAnsiStr(content))
	for _, sig := range []string{"codex", "openai"} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// Discover finds tmux sessions and processes running Codex.
func (c *CodexBackend) Discover() []DiscoveredAgent {
	found := c.discoverTmux()
	found = append(found, c.discoverProcesses()...)
	return found
}

func (c *CodexBackend) discoverTmux() []DiscoveredAgent {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}

	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{session_name}|#{pane_current_path}|#{pane_current_command}").Output()
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
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
		paneCmd := parts[2]

		if strings.HasPrefix(sessName, sessionPrefix) || seen[sessName] {
			continue
		}

		if strings.Contains(strings.ToLower(paneCmd), "codex") {
			seen[sessName] = true
			found = append(found, DiscoveredAgent{
				Name:        deriveNameFromDir(dir),
				Dir:         dir,
				SessionName: sessName,
			})
			continue
		}

		content, err := CapturePanePlain(sessName)
		if err != nil {
			continue
		}
		if c.LooksLikeMe(content) {
			seen[sessName] = true
			found = append(found, DiscoveredAgent{
				Name:        deriveNameFromDir(dir),
				Dir:         dir,
				SessionName: sessName,
			})
		}
	}

	return found
}

func (c *CodexBackend) discoverProcesses() []DiscoveredAgent {
	out, err := exec.Command("pgrep", "-af", "codex").Output()
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

		if !strings.Contains(parts[1], "codex") {
			continue
		}

		dir := getCwd(pid)
		if dir == "" {
			dir = "unknown"
		}

		found = append(found, DiscoveredAgent{
			Name: fmt.Sprintf("codex-%d", pid),
			Dir:  dir,
			PID:  pid,
		})
	}
	return found
}

// --- Hook support (Codex notify) ---

func codexNotifyScriptPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tickettok", "tickettok-codex-notify.sh")
}

const codexInlineNotifyScript = `#!/bin/bash
set -euo pipefail
# Codex notify passes JSON as argument
EVENT_TYPE=$(echo "$1" | jq -r '.type // empty')
SESS=$(tmux display-message -p '#{session_name}' 2>/dev/null || true)
[[ "$SESS" == tickettok_* ]] || exit 0
AGENT_ID="${SESS#tickettok_}"
STATUS_DIR="$HOME/.tickettok/status"
mkdir -p "$STATUS_DIR"
STATE=""
case "$EVENT_TYPE" in
  agent-turn-complete) STATE="IDLE" ;;
esac
[ -z "$STATE" ] && exit 0
TMP=$(mktemp "$STATUS_DIR/.tmp.XXXXXX")
echo "{\"state\":\"$STATE\",\"ts\":$(date +%s)}" > "$TMP"
mv "$TMP" "$STATUS_DIR/${AGENT_ID}.json"
`

// InstallHooks installs the notify script and registers it in Codex's config.toml.
func (c *CodexBackend) InstallHooks() error {
	if err := c.installNotifyScript(); err != nil {
		return fmt.Errorf("install notify script: %w", err)
	}
	if err := c.registerCodexNotify(); err != nil {
		return fmt.Errorf("register notify: %w", err)
	}
	return nil
}

func (c *CodexBackend) installNotifyScript() error {
	dest := codexNotifyScriptPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(codexInlineNotifyScript), 0755)
}

func (c *CodexBackend) registerCodexNotify() error {
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".codex", "config.toml")

	scriptPath := codexNotifyScriptPath()

	// Read existing config if present
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	content := string(data)

	// Check if already registered
	if strings.Contains(content, scriptPath) {
		return nil
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return err
	}

	// Check if a notify line already exists
	if strings.Contains(content, "notify") {
		// There's already a notify config — don't overwrite it, user may have custom settings.
		// Only add if it's a simple array we can append to.
		// For safety, skip modification if notify already exists but doesn't contain our script.
		return nil
	}

	// Append notify line
	notifyLine := fmt.Sprintf("\nnotify = [\"%s\"]\n", scriptPath)
	content += notifyLine

	return os.WriteFile(configPath, []byte(content), 0644)
}

// ReadHookStatus reads the hook-written status file for an agent.
func (c *CodexBackend) ReadHookStatus(agentID string) (AgentStatus, bool) {
	return readHookStatusFile(agentID)
}

// CleanHookStatus removes the status file for an agent.
func (c *CodexBackend) CleanHookStatus(agentID string) {
	cleanHookStatusFile(agentID)
}
