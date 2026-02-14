package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	checkDeps()

	if len(os.Args) < 2 {
		runTUI()
		return
	}

	switch os.Args[1] {
	case "start":
		runTUI()
	case "add":
		cmdAdd()
	case "list":
		cmdList()
	case "kill":
		cmdKill()
	case "discover":
		cmdDiscover()
	case "clear":
		cmdClear()
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func checkDeps() {
	var missing []string
	if _, err := exec.LookPath("tmux"); err != nil {
		missing = append(missing, "tmux (brew install tmux)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		missing = append(missing, "claude (npm install -g @anthropic-ai/claude-code)")
	}
	if len(missing) > 0 {
		fmt.Fprintln(os.Stderr, "TicketTok requires:")
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		os.Exit(1)
	}
}

func runTUI() {
	store, err := NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing state: %v\n", err)
		os.Exit(1)
	}

	manager := NewAgentManager()

	m := initialModel(store, manager)
	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		manager.CloseAll()
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	manager.CloseAll()
}

// cmdAdd spawns an agent headlessly from CLI.
func cmdAdd() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: tickettok add <dir> \"<prompt>\" [--name <name>]")
		os.Exit(1)
	}

	dir := os.Args[2]
	prompt := os.Args[3]
	name := ""

	for i := 4; i < len(os.Args)-1; i++ {
		if os.Args[i] == "--name" {
			name = os.Args[i+1]
		}
	}

	if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}

	store, err := NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	manager := NewAgentManager()

	if name == "" {
		agents := store.List()
		name = fmt.Sprintf("agent-%d", len(agents)+1)
	}

	agent := store.Add(name, dir, prompt)
	if err := manager.SpawnAgent(agent); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to spawn agent: %v\n", err)
		os.Exit(1)
	}

	store.UpdateSessionName(agent.ID, agent.SessionName)
	fmt.Printf("Spawned agent %q (ID: %s, session: %s) in %s\n", name, agent.ID, agent.SessionName, dir)
}

func cmdList() {
	store, err := NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	agents := store.List()
	if len(agents) == 0 {
		fmt.Println("No agents.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATUS\tDIR\tSESSION")
	for _, a := range agents {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.ID, a.Name, a.Status, shortenPath(a.Dir), a.SessionName)
	}
	w.Flush()
}

func cmdKill() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: tickettok kill <name-or-id>")
		os.Exit(1)
	}

	target := os.Args[2]

	store, err := NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	agent := store.Get(target)
	if agent == nil {
		agent = store.GetByName(target)
	}
	if agent == nil {
		fmt.Fprintf(os.Stderr, "Agent not found: %s\n", target)
		os.Exit(1)
	}

	if agent.SessionName != "" {
		_ = KillBySession(agent.SessionName)
	}

	store.Update(agent.ID, StatusDone)
	fmt.Printf("Killed agent %q (ID: %s)\n", agent.Name, agent.ID)
}

func cmdDiscover() {
	found := discoverTmuxClaude()
	procFound := discoverProcesses()
	found = append(found, procFound...)

	if len(found) == 0 {
		fmt.Println("No running claude instances found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tNAME\tDIR\tSESSION/PID")
	for _, d := range found {
		source := "tmux"
		id := d.SessionName
		if d.PID > 0 {
			source = "process"
			id = fmt.Sprintf("%d", d.PID)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", source, d.Name, d.Dir, id)
	}
	w.Flush()
}

func cmdClear() {
	store, err := NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	n := store.ClearDone()
	fmt.Printf("Cleared %d completed agents.\n", n)
}

func printUsage() {
	fmt.Println(`TicketTok - Terminal Kanban for Claude Code Agents

Usage:
  tickettok              Launch the TUI dashboard
  tickettok start        Launch the TUI dashboard
  tickettok add <dir> "<prompt>" [--name <name>]
                         Spawn an agent headlessly
  tickettok list         List all agents
  tickettok kill <name>  Kill an agent by name or ID
  tickettok discover     Scan for running claude instances
  tickettok clear        Remove completed agents
  tickettok help         Show this help

TUI Keybindings:
  ↑/↓ or j/k    Navigate agents (board mode)
  ←/→ or h/l    Cycle agents (carousel mode)
  1/2/3          Switch column mode
  N              Spawn new agent
  Enter          Zoom into agent (Ctrl+Q to return)
  S              Send message to agent
  K              Kill selected agent
  D              Discover running instances
  C              Clear completed agents
  Q              Quit

Requires: tmux, claude CLI`)
}

func shortenPath(p string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
