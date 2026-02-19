#!/bin/bash
# Claude Code hook for TicketTok reactive state detection.
# Reads JSON on stdin, checks if running inside a tickettok tmux session,
# writes status to ~/.tickettok/status/<agent_id>.json

set -euo pipefail

INPUT=$(cat)
EVENT=$(echo "$INPUT" | jq -r '.hook_event_name // empty')
NTYPE=$(echo "$INPUT" | jq -r '.notification_type // empty')

# Only act inside tickettok-managed tmux sessions
SESS=$(tmux display-message -p '#{session_name}' 2>/dev/null || true)
[[ "$SESS" == tickettok_* ]] || exit 0

AGENT_ID="${SESS#tickettok_}"
STATUS_DIR="$HOME/.tickettok/status"
mkdir -p "$STATUS_DIR"
STATUS_FILE="$STATUS_DIR/${AGENT_ID}.json"

STATE=""
case "$EVENT" in
  UserPromptSubmit|PreToolUse)
    STATE="RUNNING" ;;
  Stop)
    STATE="IDLE" ;;
  SessionEnd)
    STATE="DONE" ;;
  Notification)
    case "$NTYPE" in
      permission_prompt) STATE="WAITING" ;;
      idle_prompt)       STATE="IDLE" ;;
    esac ;;
esac

[ -z "$STATE" ] && exit 0

# Atomic write
TMP=$(mktemp "$STATUS_DIR/.tmp.XXXXXX")
echo "{\"state\":\"$STATE\",\"ts\":$(date +%s)}" > "$TMP"
mv "$TMP" "$STATUS_FILE"
