package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GeminiBackend implements Backend for Google Gemini CLI.
type GeminiBackend struct{}

func init() {
	RegisterBackend(&GeminiBackend{})
}

func (g *GeminiBackend) Name() string { return "Gemini" }
func (g *GeminiBackend) ID() string   { return "gemini" }

// SpawnCommand returns the shell command for launching Gemini.
func (g *GeminiBackend) SpawnCommand(args []string) (string, []string) {
	cmd := "gemini"
	if len(args) > 0 {
		cmd = "gemini " + strings.Join(args, " ")
	}
	return cmd, nil
}

// ResumeArgs returns empty — Gemini has no resume flag.
func (g *GeminiBackend) ResumeArgs() []string {
	return nil
}

// CheckDeps verifies that the gemini CLI is installed.
func (g *GeminiBackend) CheckDeps() error {
	if _, err := exec.LookPath("gemini"); err != nil {
		return fmt.Errorf("gemini (npm i -g @google/gemini-cli)")
	}
	return nil
}

// DetectStatus determines agent status from tmux pane content.
// Gemini's input box ("Type your message") is always visible, even while running.
// So we must check for RUNNING-specific indicators (like "esc to cancel") before IDLE.
func (g *GeminiBackend) DetectStatus(content string) AgentStatus {
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

	// RUNNING — Gemini shows "esc to cancel" during processing (spinner line).
	// Must check before IDLE because "Type your message" input box is always visible.
	for _, line := range recent {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "esc to cancel") {
			return StatusRunning
		}
	}

	// WAITING — Gemini uses Y/n confirmation
	for _, line := range recent {
		lower := strings.ToLower(line)
		for _, p := range []string{
			"approve", "deny", "allow",
			"yes/no", "y/n", "(y)es", "(n)o",
			"do you want to proceed",
			"shall i proceed", "should i proceed",
		} {
			if strings.Contains(lower, p) {
				return StatusWaiting
			}
		}
	}

	// IDLE — Gemini's input box contains "Type your message"
	for _, line := range recent {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "type your message") ||
			strings.Contains(lower, "what would you like") ||
			strings.Contains(lower, "how can i help") ||
			strings.Contains(lower, "let me know what") {
			return StatusIdle
		}
	}

	// Default: RUNNING (agent is processing)
	return StatusRunning
}

// DetectMode returns empty — Gemini doesn't have EDITS/PLAN modes.
func (g *GeminiBackend) DetectMode(content string) string {
	return ""
}

// StripChrome returns lines as-is — Gemini has minimal chrome to strip.
func (g *GeminiBackend) StripChrome(lines []string, waiting bool) []string {
	return lines
}

// LooksLikeMe checks pane content for Gemini UI signatures.
func (g *GeminiBackend) LooksLikeMe(content string) bool {
	lower := strings.ToLower(stripAnsiStr(content))
	for _, sig := range []string{"gemini", "google"} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// Discover finds tmux sessions and processes running Gemini.
func (g *GeminiBackend) Discover() []DiscoveredAgent {
	found := g.discoverTmux()
	found = append(found, g.discoverProcesses()...)
	return found
}

func (g *GeminiBackend) discoverTmux() []DiscoveredAgent {
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

		if strings.Contains(strings.ToLower(paneCmd), "gemini") {
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
		if g.LooksLikeMe(content) {
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

func (g *GeminiBackend) discoverProcesses() []DiscoveredAgent {
	out, err := exec.Command("pgrep", "-af", "gemini").Output()
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

		if !strings.Contains(parts[1], "gemini") {
			continue
		}

		dir := getCwd(pid)
		if dir == "" {
			dir = "unknown"
		}

		found = append(found, DiscoveredAgent{
			Name: fmt.Sprintf("gemini-%d", pid),
			Dir:  dir,
			PID:  pid,
		})
	}
	return found
}

// --- Hook support ---

func geminiHookScriptPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tickettok", "tickettok-gemini-hook.sh")
}

const geminiInlineHookScript = `#!/bin/bash
set -euo pipefail
INPUT=$(cat)
EVENT=$(echo "$INPUT" | jq -r '.hook_event_name // empty')
SESS=$(tmux display-message -p '#{session_name}' 2>/dev/null || true)
[[ "$SESS" == tickettok_* ]] || exit 0
AGENT_ID="${SESS#tickettok_}"
STATUS_DIR="$HOME/.tickettok/status"
mkdir -p "$STATUS_DIR"
STATE=""
case "$EVENT" in
  BeforeAgent|BeforeTool) STATE="RUNNING" ;;
  AfterAgent) STATE="IDLE" ;;
  Notification) STATE="WAITING" ;;
  SessionEnd) STATE="DONE" ;;
esac
[ -z "$STATE" ] && exit 0
TMP=$(mktemp "$STATUS_DIR/.tmp.XXXXXX")
echo "{\"state\":\"$STATE\",\"ts\":$(date +%s)}" > "$TMP"
mv "$TMP" "$STATUS_DIR/${AGENT_ID}.json"
`

// InstallHooks installs the hook script and registers hooks in Gemini's settings.json.
func (g *GeminiBackend) InstallHooks() error {
	if err := g.installHookScript(); err != nil {
		return fmt.Errorf("install hook script: %w", err)
	}
	if err := g.registerGeminiHooks(); err != nil {
		return fmt.Errorf("register hooks: %w", err)
	}
	return nil
}

func (g *GeminiBackend) installHookScript() error {
	dest := geminiHookScriptPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(geminiInlineHookScript), 0755)
}

func (g *GeminiBackend) registerGeminiHooks() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".gemini", "settings.json")

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

	if g.alreadyInstalled(settings) {
		return nil
	}

	cmd := geminiHookScriptPath()

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	tickettokHook := map[string]interface{}{
		"name":    "tickettok",
		"type":    "command",
		"command": cmd,
	}

	events := []string{"BeforeAgent", "BeforeTool", "AfterAgent", "Notification", "SessionEnd"}
	for _, event := range events {
		entry := map[string]interface{}{
			"matcher": "*",
			"hooks":   []interface{}{tickettokHook},
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

func (g *GeminiBackend) alreadyInstalled(settings map[string]interface{}) bool {
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}

	scriptPath := geminiHookScriptPath()
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
func (g *GeminiBackend) ReadHookStatus(agentID string) (AgentStatus, bool) {
	return readHookStatusFile(agentID)
}

// CleanHookStatus removes the status file for an agent.
func (g *GeminiBackend) CleanHookStatus(agentID string) {
	cleanHookStatusFile(agentID)
}
