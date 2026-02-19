package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// hookStatus represents the JSON written by the hook script.
type hookStatus struct {
	State string `json:"state"`
	Ts    int64  `json:"ts"`
}

// hookEntry represents a single hook in Claude's settings.json.
type hookEntry struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []hook `json:"hooks"`
}

type hook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Async   bool   `json:"async,omitempty"`
}

func hookScriptPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tickettok", "tickettok-hook.sh")
}

func statusDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tickettok", "status")
}

// installHookScript copies the hook script to ~/.tickettok/tickettok-hook.sh and makes it executable.
func installHookScript() error {
	dest := hookScriptPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	// Read from the embedded script next to the binary, or use the known source path
	scriptSrc := findHookScriptSource()
	data, err := os.ReadFile(scriptSrc)
	if err != nil {
		// Fallback: write a minimal inline version
		data = []byte(inlineHookScript)
	}

	if err := os.WriteFile(dest, data, 0755); err != nil {
		return err
	}
	return nil
}

// findHookScriptSource locates the hook script relative to the running binary.
func findHookScriptSource() string {
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "scripts", "tickettok-hook.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Try current working directory
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "scripts", "tickettok-hook.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "" // will trigger fallback
}

const inlineHookScript = `#!/bin/bash
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

// ensureHooksInstalled installs the hook script and registers hooks in Claude's settings.json.
func ensureHooksInstalled() {
	if err := installHookScript(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not install hook script: %v\n", err)
		return
	}

	if err := registerClaudeHooks(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not register hooks: %v\n", err)
	}
}

// registerClaudeHooks adds TicketTok hook entries to ~/.claude/settings.json.
func registerClaudeHooks() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Read existing settings
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

	// Check if hooks already contain our entries
	if alreadyInstalled(settings) {
		return nil
	}

	// Build our hook command
	cmd := hookScriptPath()

	// The hooks structure in settings.json is:
	// "hooks": { "<event>": [ { "matcher": "...", "hooks": [{"type":"command","command":"..."}] } ] }
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

	// Write back
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0644)
}

// alreadyInstalled checks if TicketTok hooks are already in settings.
func alreadyInstalled(settings map[string]interface{}) bool {
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}

	// Check if any event has our hook command
	scriptPath := hookScriptPath()
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

// readHookStatus reads the hook-written status file for an agent.
// All states expire after a TTL — if a hook fails to fire, we fall back
// to capture-pane rather than showing a stale state forever.
// RUNNING: 30s (Stop hook should fire quickly after processing ends)
// WAITING: 5min (user may sit on a permission prompt for a while)
// IDLE/DONE: 5min (stable but not permanent — hooks refresh on next event)
func readHookStatus(agentID string) (AgentStatus, bool) {
	path := filepath.Join(statusDir(), agentID+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}

	var hs hookStatus
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

// cleanHookStatus removes the status file for an agent.
func cleanHookStatus(agentID string) {
	path := filepath.Join(statusDir(), agentID+".json")
	_ = os.Remove(path)
}
