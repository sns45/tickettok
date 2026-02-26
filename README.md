# TicketTok

![TicketTok](assets/marketing/poster.png)

Auto-updating status tracking dashboard for multitasking between multiple Claude Code instances via tmux.

Built with [Go](https://go.dev), [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Lipgloss](https://github.com/charmbracelet/lipgloss), and [creack/pty](https://github.com/creack/pty).

```
 TicketTok                                      3 agents  [3-col]

 ■ IDLE [1]          ■ WAITING [1]        ■ RUNNING [1]
 ╭──────────────╮    ╭──────────────╮    ╭──────────────╮
 │ myapp        │    │ backend      │    │ frontend     │
 │  IDLE        │    │  WAITING     │    │  IN-PROGRESS │
 │ ~/dev/myapp  │    │ ~/dev/api    │    │ ~/dev/web    │
 │ ──────────── │    │ ──────────── │    │ ──────────── │
 │ Done. Need   │    │ Allow once   │    │ Writing      │
 │ anything?    │    │ Allow always │    │ component... │
 ╰──────────────╯    ╰──────────────╯    ╰──────────────╯

 [↑/↓]Nav  [←/→]Column  [N]ew  [Enter]Zoom  [K]ill  [S]end  [Q]uit
```

## Prerequisites

- **tmux** — `brew install tmux`
- **Claude CLI** — `npm install -g @anthropic-ai/claude-code`

## Install

**Homebrew** (macOS/Linux):
```bash
brew install sns45/tap/tickettok
```

**Go install** (requires Go 1.21+):
```bash
go install github.com/sns45/tickettok@latest
```

**Shell script** (macOS/Linux):
```bash
curl -sSfL https://raw.githubusercontent.com/sns45/tickettok/main/install.sh | sh
```

**From source**:
```bash
git clone https://github.com/sns45/tickettok.git
cd tickettok
go build -o tickettok .
```

**Binary download**: grab a release from [GitHub Releases](https://github.com/sns45/tickettok/releases).

## Usage

```
tickettok              Launch the TUI dashboard
tickettok start        Launch the TUI dashboard
tickettok add <dir>    Spawn an agent headlessly (--name <name> optional)
tickettok list         List all agents
tickettok kill <name>  Kill an agent by name or ID
tickettok discover     Scan for running claude instances
tickettok clear        Remove completed agents
tickettok help         Show help
```

## TUI Keybindings

| Key | Action |
|-----|--------|
| `↑`/`↓` or `j`/`k` | Navigate agents |
| `←`/`→` or `h`/`l` | Move between columns (board mode) |
| `1` / `2` / `3` | Switch to carousel / 2-col / 3-col layout |
| `N` | Spawn new agent |
| `Enter` | Zoom into agent (full terminal view) |
| `Ctrl+Q` | Return from zoom |
| `S` | Send message to selected agent |
| `K` | Kill selected agent |
| `D` | Discover running claude instances |
| `C` | Clear completed agents |
| `Q` | Quit (agents keep running in tmux) |

In **zoom mode**, all keystrokes are forwarded to the agent's tmux session.

## Views

- **Board** (2 or 3 columns) — agents sorted into IDLE, WAITING, RUNNING columns
- **Carousel** (1 column) — vertical scrollable list of all agents
- **Zoom** — full-screen view of a single agent's tmux pane, with live capture

## How It Works

Each agent runs `claude` inside a detached **tmux session** (`tickettok_<id>`). TicketTok attaches a background PTY client so `capture-pane` always has content to grab.

**Status detection** uses two methods:
1. **Claude Code hooks** (fast) — a shell script installed into `~/.claude/settings.json` writes JSON status files to `~/.tickettok/status/` on lifecycle events (prompt submit, tool use, stop, permission prompts)
2. **capture-pane scraping** (fallback) — parses the last 15 lines of terminal output looking for spinners, permission prompts, idle indicators, etc.

**State** is persisted to `~/.tickettok/state.json` so agents survive TUI restarts.

## Project Structure

```
tickettok/
├── main.go        CLI entry, subcommands, TUI setup
├── model.go       Bubble Tea model, views, key handling
├── agent.go       AgentManager: spawn, kill, status, send keys
├── tmux.go        TmuxSession: PTY, capture-pane, resize, discovery
├── state.go       JSON state persistence
├── hooks.go       Claude Code hook installation & status reading
├── ui/
│   ├── styles.go      Colors, badge styles, card styles
│   ├── board.go       2/3-column kanban layout
│   ├── card.go        Agent card rendering
│   └── carousel.go    Single-column carousel view
├── go.mod
└── go.sum
```
