package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
)

var version = "0.12.2"

func main() {
	checkDeps()
	installBackendHooks()

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
	case "send":
		cmdSend()
	case "status":
		cmdStatus()
	case "discover":
		cmdDiscover()
	case "clear":
		cmdClear()
	case "workspace", "ws":
		cmdWorkspace()
	case "version", "--version", "-v":
		fmt.Println("tickettok " + version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func checkDeps() {
	// tmux is always required
	if _, err := exec.LookPath("tmux"); err != nil {
		hint := "tmux (brew install tmux)"
		if runtime.GOOS == "linux" {
			hint = "tmux (apt install tmux)"
		}
		fmt.Fprintln(os.Stderr, "TicketTok requires:")
		fmt.Fprintf(os.Stderr, "  %s\n", hint)
		os.Exit(1)
	}

	// Check backends — warn about missing, fatal if none available
	var available int
	for _, b := range AllBackends() {
		if err := b.CheckDeps(); err != nil {
			fmt.Fprintf(os.Stderr, "  [optional] %s not found: %s\n", b.Name(), err)
		} else {
			available++
		}
	}
	if available == 0 {
		fmt.Fprintln(os.Stderr, "At least one agent CLI is required (claude, codex, or gemini)")
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

	finalModel, err := p.Run()
	if err != nil {
		manager.CloseAll()
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	manager.CloseAll()

	if fm, ok := finalModel.(Model); ok && fm.shouldReExec {
		if err := reExec(); err != nil {
			fmt.Fprintf(os.Stderr, "Restart failed: %v (please relaunch manually)\n", err)
			os.Exit(1)
		}
	}
}

// cmdAdd spawns an agent headlessly from CLI.
func cmdAdd() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: tickettok add <dir> [--name <name>] [--backend <claude|codex|gemini>] [--prompt <text>] [--auto-approve]")
		os.Exit(1)
	}

	dir := os.Args[2]
	name := ""
	backendID := ""
	prompt := ""
	autoApprove := false

	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--name":
			if i+1 < len(os.Args) {
				name = os.Args[i+1]
				i++
			}
		case "--backend":
			if i+1 < len(os.Args) {
				backendID = os.Args[i+1]
				i++
			}
		case "--prompt":
			if i+1 < len(os.Args) {
				prompt = os.Args[i+1]
				i++
			}
		case "--auto-approve":
			autoApprove = true
		}
	}

	if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}

	// Create directory if it doesn't exist
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Cannot create directory: %v\n", err)
			os.Exit(1)
		}
	}

	store, err := NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	manager := NewAgentManager()

	if name == "" {
		name = deriveNameFromDir(dir)
	}

	agent := store.Add(name, dir)

	// Apply backend selection
	if backendID != "" {
		if GetBackend(backendID) == nil {
			fmt.Fprintf(os.Stderr, "Unknown backend: %s\n", backendID)
			os.Exit(1)
		}
		agent.BackendID = backendID
	}

	// Apply auto-approve
	if autoApprove {
		agent.AutoApprove = true
	}

	// Build extra args from auto-approve
	var extraArgs []string
	if agent.AutoApprove {
		extraArgs = append(extraArgs, agent.Backend().AutoApproveArgs()...)
	}

	if err := manager.SpawnAgent(agent, extraArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to spawn agent: %v\n", err)
		os.Exit(1)
	}

	store.UpdateSessionName(agent.ID, agent.SessionName)
	// Persist backend and auto-approve to state
	store.Save()

	fmt.Printf("Spawned agent %q (ID: %s, session: %s) in %s\n", name, agent.ID, agent.SessionName, dir)

	// Send initial prompt after startup delay
	if prompt != "" {
		go SendPromptAfterDelay(agent.SessionName, prompt)
	}
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

func cmdSend() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: tickettok send <name-or-id> <message>")
		os.Exit(1)
	}

	target := os.Args[2]
	message := strings.Join(os.Args[3:], " ")

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

	if agent.SessionName == "" || !IsSessionAlive(agent.SessionName) {
		fmt.Fprintf(os.Stderr, "Agent %q is not running\n", agent.Name)
		os.Exit(1)
	}

	cmd := exec.Command("tmux", "send-keys", "-t", agent.SessionName, message, "Enter")
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to send message: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Sent to %q: %s\n", agent.Name, message)
}

func cmdStatus() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: tickettok status <name-or-id>")
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

	// Try hook-based status first
	backend := agent.Backend()
	if status, ok := backend.ReadHookStatus(agent.ID); ok {
		fmt.Printf("%s: %s\n", agent.Name, status)
		return
	}

	// Check if session is alive
	if agent.SessionName == "" || !IsSessionAlive(agent.SessionName) {
		fmt.Printf("%s: %s\n", agent.Name, StatusDone)
		return
	}

	// Fall back to capture-pane detection
	content, err := CapturePane(agent.SessionName)
	if err != nil {
		fmt.Printf("%s: %s\n", agent.Name, StatusRunning)
		return
	}

	result := backend.DetectStatus(content)
	fmt.Printf("%s: %s\n", agent.Name, result.Status)
}

func cmdDiscover() {
	var found []DiscoveredAgent
	for _, b := range AllBackends() {
		found = append(found, b.Discover()...)
	}

	if len(found) == 0 {
		fmt.Println("No running agent instances found.")
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
	fmt.Println(`TicketTok - Terminal Kanban for AI Coding Agents

Usage:
  tickettok              Launch the TUI dashboard
  tickettok start        Launch the TUI dashboard
  tickettok add <dir> [flags]
                         Spawn an agent headlessly
    --name <name>        Agent display name (default: dir basename)
    --backend <id>       Backend to use: claude, codex, gemini
    --prompt <text>      Initial prompt sent after agent starts
    --auto-approve       Enable auto-approve mode for the backend
  tickettok send <name-or-id> <message>
                         Send a message to a running agent
  tickettok status <name-or-id>
                         Check an agent's current status
  tickettok list         List all agents
  tickettok kill <name>  Kill an agent by name or ID
  tickettok discover     Scan for running agent instances
  tickettok clear        Remove completed agents
  tickettok workspace save <name>          Save current agents as workspace
  tickettok workspace load <name>          Clear current + spawn workspace agents
  tickettok workspace add <name>           Spawn workspace agents alongside current
  tickettok workspace list                 List saved workspaces
  tickettok workspace create <name>        Create empty workspace
  tickettok workspace delete <name>        Delete saved workspace
  tickettok workspace agent <ws> <dir> [flags]
                                           Add agent template to workspace
  tickettok help         Show this help

TUI Keybindings:
  ↑/↓ or j/k    Navigate agents (board mode)
  ←/→ or h/l    Cycle agents (carousel mode)
  1/2/3          Switch column mode
  N              Spawn new agent
  W              Workspace manager
  Enter          Zoom into agent (Ctrl+Q to return)
  S              Send message to agent
  K              Kill selected agent
  D              Discover running instances
  C              Clear completed agents
  Q              Quit

Requires: tmux + at least one agent CLI (claude, codex, or gemini)`)
}

func cmdWorkspace() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: tickettok workspace <save|load|add|list|create|delete|agent> ...")
		os.Exit(1)
	}

	sub := os.Args[2]

	switch sub {
	case "save":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: tickettok workspace save <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		store, err := NewStore()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		agents := store.List()
		if err := SaveWorkspace(name, agents); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving workspace: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Saved workspace %q with %d agent(s).\n", name, len(agents))

	case "load":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: tickettok workspace load <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		wf, err := LoadWorkspace(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		store, err := NewStore()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		// Kill all current agents
		for _, a := range store.List() {
			if a.SessionName != "" {
				_ = KillBySession(a.SessionName)
			}
			a.Backend().CleanHookStatus(a.ID)
			store.Remove(a.ID)
		}
		manager := NewAgentManager()
		count := spawnWorkspaceAgents(wf, store, manager)
		fmt.Printf("Loaded workspace %q: spawned %d agent(s).\n", name, count)

	case "add":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: tickettok workspace add <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		wf, err := LoadWorkspace(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		store, err := NewStore()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		manager := NewAgentManager()
		count := spawnWorkspaceAgents(wf, store, manager)
		fmt.Printf("Added workspace %q: spawned %d agent(s).\n", name, count)

	case "list":
		names, err := ListWorkspaces()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(names) == 0 {
			fmt.Println("No saved workspaces.")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tAGENTS\tCREATED")
		for _, n := range names {
			wf, err := LoadWorkspace(n)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "%s\t%d\t%s\n", n, len(wf.Agents), wf.CreatedAt.Format("2006-01-02 15:04"))
		}
		w.Flush()

	case "create":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: tickettok workspace create <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		if WorkspaceExists(name) {
			fmt.Fprintf(os.Stderr, "Workspace %q already exists.\n", name)
			os.Exit(1)
		}
		if err := SaveWorkspace(name, nil); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created empty workspace %q.\n", name)

	case "delete":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: tickettok workspace delete <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		if err := DeleteWorkspace(name); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Deleted workspace %q.\n", name)

	case "agent":
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, "Usage: tickettok workspace agent <workspace> <dir> [--name <name>] [--backend <id>] [--auto-approve]")
			os.Exit(1)
		}
		wsName := os.Args[3]
		dir := os.Args[4]

		agentName := ""
		backendID := ""
		autoApprove := false

		for i := 5; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--name":
				if i+1 < len(os.Args) {
					agentName = os.Args[i+1]
					i++
				}
			case "--backend":
				if i+1 < len(os.Args) {
					backendID = os.Args[i+1]
					i++
				}
			case "--auto-approve":
				autoApprove = true
			}
		}

		if strings.HasPrefix(dir, "~/") {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[2:])
		}

		if agentName == "" {
			agentName = deriveNameFromDir(dir)
		}

		if !WorkspaceExists(wsName) {
			fmt.Fprintf(os.Stderr, "Workspace %q does not exist. Create it first with: tickettok workspace create %s\n", wsName, wsName)
			os.Exit(1)
		}

		wa := WorkspaceAgent{
			Name:        agentName,
			Dir:         dir,
			BackendID:   backendID,
			AutoApprove: autoApprove,
		}
		if err := AddAgentToWorkspace(wsName, wa); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Added agent %q to workspace %q.\n", agentName, wsName)

	default:
		fmt.Fprintf(os.Stderr, "Unknown workspace command: %s\n", sub)
		fmt.Fprintln(os.Stderr, "Usage: tickettok workspace <save|load|add|list|create|delete|agent> ...")
		os.Exit(1)
	}
}

func installBackendHooks() {
	for _, b := range AllBackends() {
		if err := b.InstallHooks(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not install %s hooks: %v\n", b.Name(), err)
		}
	}
}

func shortenPath(p string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
