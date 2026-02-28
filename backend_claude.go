package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeBackend implements Backend for Claude Code.
type ClaudeBackend struct{}

func init() {
	RegisterBackend(&ClaudeBackend{})
}

func (c *ClaudeBackend) Name() string { return "Claude Code" }
func (c *ClaudeBackend) ID() string   { return "claude" }

// SpawnCommand returns the shell command and env vars to strip for launching Claude.
func (c *ClaudeBackend) SpawnCommand(args []string) (string, []string) {
	cmd := "claude"
	if len(args) > 0 {
		cmd = "claude " + strings.Join(args, " ")
	}
	return cmd, []string{"CLAUDECODE"}
}

// ResumeArgs returns the CLI flags to resume the most recent conversation.
func (c *ClaudeBackend) ResumeArgs() []string {
	return []string{"--continue"}
}

// CheckDeps verifies that the claude CLI is installed.
func (c *ClaudeBackend) CheckDeps() error {
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("claude (npm install -g @anthropic-ai/claude-code)")
	}
	return nil
}

// DetectStatus determines agent status from tmux pane content.
func (c *ClaudeBackend) DetectStatus(content string) AgentStatus {
	lines := strings.Split(content, "\n")

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

	// RUNNING
	for _, line := range recent {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "esc to interrupt") {
			return StatusRunning
		}
		if strings.Contains(lower, "running…") || strings.Contains(lower, "running...") {
			return StatusRunning
		}
		hasEllipsis := strings.Contains(line, "…") || strings.Contains(line, "...")
		if hasEllipsis && hasDingbat(line) {
			return StatusRunning
		}
	}

	// WAITING
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

	// IDLE
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

	// DONE
	bottom := recent[0]
	bottomLower := strings.ToLower(bottom)
	for _, p := range []string{"exited", "goodbye", "session ended", "bye"} {
		if strings.Contains(bottomLower, p) {
			return StatusDone
		}
	}

	return StatusRunning
}

// DetectMode scans pane content for Claude Code mode indicators.
func (c *ClaudeBackend) DetectMode(content string) string {
	lines := strings.Split(content, "\n")

	var recent []string
	for i := len(lines) - 1; i >= 0 && len(recent) < 5; i-- {
		line := strings.TrimSpace(stripAnsiStr(lines[i]))
		if line != "" {
			recent = append(recent, line)
		}
	}

	for _, line := range recent {
		lower := strings.ToLower(line)
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

// StripChrome removes Claude Code's bottom chrome from captured pane lines.
func (c *ClaudeBackend) StripChrome(lines []string, waiting bool) []string {
	if waiting {
		return claudeStripWaitingChrome(lines)
	}
	return claudeStripChromeLines(lines)
}

// claudeStripChromeLines removes Claude Code's bottom chrome (separator, prompt, state line).
func claudeStripChromeLines(lines []string) []string {
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
	for i := promptIdx - 1; i >= 0; i-- {
		stripped := strings.TrimSpace(stripAnsiStr(lines[i]))
		if isSeparatorLine(stripped) {
			return lines[:i]
		}
	}
	return lines[:promptIdx]
}

// claudeStripWaitingChrome removes separator lines and last non-blank line,
// keeping the selection UI visible.
func claudeStripWaitingChrome(lines []string) []string {
	var filtered []string
	for _, l := range lines {
		stripped := strings.TrimSpace(stripAnsiStr(l))
		if !isSeparatorLine(stripped) {
			filtered = append(filtered, l)
		}
	}

	for i := len(filtered) - 1; i >= 0; i-- {
		stripped := strings.TrimSpace(stripAnsiStr(filtered[i]))
		if stripped != "" {
			filtered = append(filtered[:i], filtered[i+1:]...)
			break
		}
	}

	return filtered
}

// LooksLikeMe checks pane content for Claude Code UI signatures.
func (c *ClaudeBackend) LooksLikeMe(content string) bool {
	lower := strings.ToLower(stripAnsiStr(content))
	signatures := []string{
		"❯",
		"? for shortcuts",
		"esc to interrupt",
		"claude code",
		"anthropic",
		"allow once",
		"allow always",
	}
	for _, sig := range signatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// Discover finds tmux sessions and processes running Claude Code.
func (c *ClaudeBackend) Discover() []DiscoveredAgent {
	found := c.discoverTmux()
	found = append(found, c.discoverProcesses()...)
	return found
}

func (c *ClaudeBackend) discoverTmux() []DiscoveredAgent {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}

	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{session_name}|#{pane_current_path}|#{pane_current_command}").Output()
	if err != nil {
		return c.discoverTmuxFallback()
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

		if strings.HasPrefix(sessName, sessionPrefix) {
			continue
		}
		if seen[sessName] {
			continue
		}

		if strings.Contains(strings.ToLower(paneCmd), "claude") {
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

func (c *ClaudeBackend) discoverTmuxFallback() []DiscoveredAgent {
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

		if strings.HasPrefix(sessName, sessionPrefix) {
			continue
		}

		paneCmd := parts[2]
		if strings.Contains(strings.ToLower(paneCmd), "claude") {
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
			found = append(found, DiscoveredAgent{
				Name:        deriveNameFromDir(dir),
				Dir:         dir,
				SessionName: sessName,
			})
		}
	}

	return found
}

func (c *ClaudeBackend) discoverProcesses() []DiscoveredAgent {
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

// --- Hook support ---

// claudeHookStatus represents the JSON written by the hook script.
type claudeHookStatus struct {
	State string `json:"state"`
	Ts    int64  `json:"ts"`
}

func claudeHookScriptPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tickettok", "tickettok-hook.sh")
}

func claudeStatusDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tickettok", "status")
}

// InstallHooks installs the hook script and registers hooks in Claude's settings.json.
func (c *ClaudeBackend) InstallHooks() error {
	if err := c.installHookScript(); err != nil {
		return fmt.Errorf("install hook script: %w", err)
	}
	if err := c.registerClaudeHooks(); err != nil {
		return fmt.Errorf("register hooks: %w", err)
	}
	return nil
}

func (c *ClaudeBackend) installHookScript() error {
	dest := claudeHookScriptPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	scriptSrc := claudeFindHookScriptSource()
	data, err := os.ReadFile(scriptSrc)
	if err != nil {
		data = []byte(claudeInlineHookScript)
	}

	return os.WriteFile(dest, data, 0755)
}

func claudeFindHookScriptSource() string {
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "scripts", "tickettok-hook.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "scripts", "tickettok-hook.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

const claudeInlineHookScript = `#!/bin/bash
set -euo pipefail
INPUT=$(cat)
EVENT=$(echo "$INPUT" | jq -r '.hook_event_name // empty')
NTYPE=$(echo "$INPUT" | jq -r '.notification_type // empty')
SESS=$(tmux display-message -p '#{session_name}' 2>/dev/null || true)
[[ "$SESS" == tickettok_* ]] || exit 0
AGENT_ID="${SESS#tickettok_}"
STATUS_DIR="$HOME/.tickettok/status"
mkdir -p "$STATUS_DIR"
STATE=""
case "$EVENT" in
  UserPromptSubmit|PreToolUse) STATE="RUNNING" ;;
  Stop) STATE="IDLE" ;;
  SessionEnd) STATE="DONE" ;;
  Notification)
    case "$NTYPE" in
      permission_prompt) STATE="WAITING" ;;
      idle_prompt) STATE="IDLE" ;;
    esac ;;
esac
[ -z "$STATE" ] && exit 0
TMP=$(mktemp "$STATUS_DIR/.tmp.XXXXXX")
echo "{\"state\":\"$STATE\",\"ts\":$(date +%s)}" > "$TMP"
mv "$TMP" "$STATUS_DIR/${AGENT_ID}.json"
`

func (c *ClaudeBackend) registerClaudeHooks() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	var settings map[string]interface{}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			settings = make(map[string]interface{})
		} else {
			return err
		}
	} else {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse settings: %w", err)
		}
	}

	if c.alreadyInstalled(settings) {
		return nil
	}

	cmd := claudeHookScriptPath()

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	tickettokHook := map[string]interface{}{
		"type":    "command",
		"command": cmd,
		"async":   true,
	}

	events := []string{"UserPromptSubmit", "PreToolUse", "Stop", "SessionEnd", "Notification"}
	for _, event := range events {
		entry := map[string]interface{}{
			"hooks": []interface{}{tickettokHook},
		}
		existing, _ := hooks[event].([]interface{})
		existing = append(existing, entry)
		hooks[event] = existing
	}

	settings["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0644)
}

func (c *ClaudeBackend) alreadyInstalled(settings map[string]interface{}) bool {
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}

	scriptPath := claudeHookScriptPath()
	for _, entries := range hooks {
		arr, ok := entries.([]interface{})
		if !ok {
			continue
		}
		for _, entry := range arr {
			em, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			hookList, ok := em["hooks"].([]interface{})
			if !ok {
				continue
			}
			for _, h := range hookList {
				hm, ok := h.(map[string]interface{})
				if !ok {
					continue
				}
				if cmd, ok := hm["command"].(string); ok && cmd == scriptPath {
					return true
				}
			}
		}
	}
	return false
}

// ReadHookStatus reads the hook-written status file for an agent.
func (c *ClaudeBackend) ReadHookStatus(agentID string) (AgentStatus, bool) {
	path := filepath.Join(claudeStatusDir(), agentID+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}

	var hs claudeHookStatus
	if err := json.Unmarshal(data, &hs); err != nil {
		return "", false
	}

	age := time.Now().Unix() - hs.Ts

	switch hs.State {
	case "RUNNING":
		if age > 30 {
			return "", false
		}
		return StatusRunning, true
	case "WAITING":
		if age > 300 {
			return "", false
		}
		return StatusWaiting, true
	case "IDLE":
		if age > 300 {
			return "", false
		}
		return StatusIdle, true
	case "DONE":
		if age > 300 {
			return "", false
		}
		return StatusDone, true
	default:
		return "", false
	}
}

// CleanHookStatus removes the status file for an agent.
func (c *ClaudeBackend) CleanHookStatus(agentID string) {
	path := filepath.Join(claudeStatusDir(), agentID+".json")
	_ = os.Remove(path)
}
