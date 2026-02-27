package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sns45/tickettok/ui"
)

// sgrMouseRe matches SGR mouse escape sequences that arrive as literal runes
// when bubbletea fails to parse them (e.g. "[<65;132;34M").
// Captures the button number in group 1 for scroll handling.
var sgrMouseRe = regexp.MustCompile(`<(\d+);\d+;\d+[Mm]`)

// View modes
type viewMode int

const (
	viewBoard    viewMode = iota
	viewCarousel
	viewZoom
	viewSpawn
	viewSend
	viewConfirmQuit
	viewConfirmKill
)

// tickMsg is sent periodically to refresh status.
type tickMsg time.Time

// zoomTickMsg carries captured tmux pane content for zoom view.
type zoomTickMsg struct{ content string }

// discoverMsg carries newly discovered external Claude agents.
type discoverMsg struct{ found []DiscoveredAgent }

// reconcileMsg signals that stale discovered agents have been reconciled.
type reconcileMsg struct{}

// Model is the Bubble Tea application model.
type Model struct {
	store    *Store
	manager  *AgentManager
	agents   []*Agent // cached agent list
	selected int
	columns  int // 1, 2, or 3
	view     viewMode
	width    int
	height   int

	// Spawn dialog fields
	spawnDir        textinput.Model
	spawnSuggestions []string // filtered directory matches
	spawnSelIdx     int      // selected suggestion index (-1 = none)

	// Send dialog
	sendInput textinput.Model

	// Zoom mode
	zoomAgentID    string
	zoomSession    string   // tmux session name
	zoomContent    string   // captured pane content (full scrollback)
	zoomScrollOff  int      // scroll offset from bottom (0 = follow latest)
	zoomTotalLines int      // total lines in captured content
	zoomAltBracket bool     // true after receiving alt+[ (potential SGR mouse prefix)

	// Status message
	statusMsg     string
	statusExpires time.Time

	// Scroll offset for board/carousel views
	scrollOffset int

	// Tick counter for periodic re-discovery
	tickCount int
}

func initialModel(store *Store, manager *AgentManager) Model {
	dirInput := textinput.New()
	dirInput.Placeholder = "~/dev (default)"
	dirInput.CharLimit = 200
	dirInput.Width = 60

	sendInput := textinput.New()
	sendInput.Placeholder = "message to send to agent"
	sendInput.CharLimit = 500
	sendInput.Width = 60

	return Model{
		store:    store,
		manager:  manager,
		agents:   store.List(),
		columns:  3,
		view:     viewBoard,
		width:    120,
		height:   40,
		spawnDir: dirInput,
		sendInput: sendInput,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		tea.ClearScreen,
		discoverCmd(),
		reconcileCmd(m.store),
		tea.SetWindowTitle("TicketTok"),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.view == viewZoom && m.zoomSession != "" && m.selected < len(m.agents) {
			agent := m.agents[m.selected]
			if !agent.Discovered {
				if sess := m.manager.GetSession(agent); sess != nil {
					sess.SetSize(m.width, m.height-2)
				}
			}
		}
		return m, nil

	case tickMsg:
		m.refreshStatuses()
		m.agents = m.store.List()
		m.tickCount++
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd())
		// Re-discover every 5th tick (~10s)
		if m.tickCount%5 == 0 {
			cmds = append(cmds, discoverCmd())
		}
		return m, tea.Batch(cmds...)

	case discoverMsg:
		m.mergeDiscovered(msg.found)
		m.agents = m.store.List()
		return m, nil

	case reconcileMsg:
		m.agents = m.store.List()
		return m, nil

	case zoomTickMsg:
		if m.view == viewZoom {
			m.zoomContent = msg.content
			m.zoomTotalLines = strings.Count(msg.content, "\n") + 1
			return m, zoomCaptureCmd(m.zoomSession)
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Update text inputs if in dialog
	var cmd tea.Cmd
	switch m.view {
	case viewSpawn:
		cmd = m.updateSpawnInputs(msg)
	case viewSend:
		m.sendInput, cmd = m.sendInput.Update(msg)
	}
	return m, cmd
}

func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.view == viewZoom {
		return m.handleZoomMouse(msg)
	}
	return m, nil
}

func (m *Model) handleZoomMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.zoomSession == "" {
		return m, nil
	}
	scrollLines := 3
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.zoomScrollOff += scrollLines
		// Clamp to max scrollable range
		maxScroll := m.zoomTotalLines - (m.height - 2)
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.zoomScrollOff > maxScroll {
			m.zoomScrollOff = maxScroll
		}
	case tea.MouseButtonWheelDown:
		m.zoomScrollOff -= scrollLines
		if m.zoomScrollOff < 0 {
			m.zoomScrollOff = 0
		}
	default:
		return m, nil
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch {
	case m.view == viewZoom:
		return m.handleZoomKey(msg)
	case m.view == viewConfirmQuit:
		return m.handleConfirmQuit(key)
	case m.view == viewConfirmKill:
		return m.handleConfirmKill(key)
	case m.view == viewSpawn:
		return m.handleSpawnKey(msg)
	case m.view == viewSend:
		return m.handleSendKey(msg)
	}

	// Board/carousel keys
	switch key {
	case "q":
		m.view = viewConfirmQuit
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "n":
		m.openSpawnDialog()
		return m, nil
	case "1":
		m.columns = 1
		m.view = viewCarousel
		if len(m.agents) > 0 && m.selected >= len(m.agents) {
			m.selected = 0
		}
		return m, nil
	case "2":
		m.columns = 2
		m.view = viewBoard
		return m, nil
	case "3":
		m.columns = 3
		m.view = viewBoard
		return m, nil
	case "d":
		m.discoverAgents()
		return m, nil
	case "c":
		n := m.store.ClearDone()
		m.agents = m.store.List()
		m.setStatus(fmt.Sprintf("Cleared %d completed agents", n))
		if m.selected >= len(m.agents) && len(m.agents) > 0 {
			m.selected = len(m.agents) - 1
		}
		return m, nil
	}

	if m.view == viewCarousel || m.columns == 1 {
		return m.handleCarouselNav(key)
	}
	return m.handleBoardNav(key)
}

func (m *Model) handleBoardNav(key string) (tea.Model, tea.Cmd) {
	n := len(m.agents)
	if n == 0 {
		return m, nil
	}

	switch key {
	case "j", "down":
		m.selected = m.nextInSameColumn(+1)
	case "k", "up":
		m.selected = m.nextInSameColumn(-1)
	case "l", "right":
		m.selected = m.nextInColumn(1)
	case "h", "left":
		m.selected = m.nextInColumn(-1)
	case "enter":
		return m.enterZoom()
	case "x", "K":
		m.view = viewConfirmKill
	case "s", "S":
		m.openSendDialog()
	}
	m.ensureSelectedVisible()
	return m, nil
}

// nextInColumn returns the flat index of the nearest agent in an adjacent column.
// delta is -1 (left) or +1 (right).
func (m *Model) nextInColumn(delta int) int {
	n := len(m.agents)
	if n == 0 || m.selected >= n {
		return m.selected
	}

	// Build column assignment for each agent
	cols := make([]int, n)
	for i, a := range m.agents {
		cols[i] = m.columnForStatus(a.Status)
	}

	curCol := cols[m.selected]

	// Compute current row within current column
	curRow := 0
	for i := 0; i < m.selected; i++ {
		if cols[i] == curCol {
			curRow++
		}
	}

	// Target column, clamped
	maxCol := m.columns - 1
	targetCol := curCol + delta
	if targetCol < 0 {
		targetCol = 0
	}
	if targetCol > maxCol {
		targetCol = maxCol
	}
	if targetCol == curCol {
		return m.selected
	}

	// Find agents in target column, pick closest row
	bestIdx := m.selected
	bestDist := n + 1
	row := 0
	for i := 0; i < n; i++ {
		if cols[i] == targetCol {
			dist := row - curRow
			if dist < 0 {
				dist = -dist
			}
			if dist < bestDist {
				bestDist = dist
				bestIdx = i
			}
			row++
		}
	}

	return bestIdx
}

// nextInSameColumn returns the flat index of the next (delta=+1) or previous (delta=-1)
// agent within the same column as the currently selected agent.
func (m *Model) nextInSameColumn(delta int) int {
	n := len(m.agents)
	if n == 0 || m.selected >= n {
		return m.selected
	}

	curCol := m.columnForStatus(m.agents[m.selected].Status)

	// Collect flat indices of agents in the same column
	var sameCol []int
	for i, a := range m.agents {
		if m.columnForStatus(a.Status) == curCol {
			sameCol = append(sameCol, i)
		}
	}

	// Find current position within column
	pos := 0
	for i, idx := range sameCol {
		if idx == m.selected {
			pos = i
			break
		}
	}

	// Move within column, wrapping around
	k := len(sameCol)
	newPos := (pos + delta%k + k) % k
	return sameCol[newPos]
}

// columnForStatus returns the column index for a given agent status.
func (m *Model) columnForStatus(status AgentStatus) int {
	if m.columns == 2 {
		// 2-col: IDLE/DONE=0, ACTIVE(RUNNING+WAITING)=1
		switch status {
		case StatusRunning, StatusWaiting:
			return 1
		default:
			return 0
		}
	}
	// 3-col: IDLE/DONE=0, WAITING=1, RUNNING=2
	switch status {
	case StatusWaiting:
		return 1
	case StatusRunning:
		return 2
	default:
		return 0
	}
}

// ensureSelectedVisible adjusts scrollOffset so the selected agent's card is on screen.
// Each card is ~7 lines tall. We keep a margin of 5 lines for title+footer.
func (m *Model) ensureSelectedVisible() {
	cardHeight := 7
	viewportLines := m.height - 5 // title bar + footer + padding
	if viewportLines < cardHeight {
		viewportLines = cardHeight
	}
	maxVisible := viewportLines / cardHeight
	if maxVisible < 1 {
		maxVisible = 1
	}

	if m.selected < m.scrollOffset {
		m.scrollOffset = m.selected
	} else if m.selected >= m.scrollOffset+maxVisible {
		m.scrollOffset = m.selected - maxVisible + 1
	}
}

func (m *Model) handleCarouselNav(key string) (tea.Model, tea.Cmd) {
	n := len(m.agents)
	if n == 0 {
		return m, nil
	}

	switch key {
	case "j", "down":
		m.selected = (m.selected + 1) % n
	case "k", "up":
		m.selected = (m.selected - 1 + n) % n
	case "enter":
		return m.enterZoom()
	case "x", "K":
		m.view = viewConfirmKill
	case "s", "S":
		m.openSendDialog()
	}
	m.ensureSelectedVisible()
	return m, nil
}

func (m *Model) handleZoomKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Ctrl+Q exits zoom
	if key == "ctrl+q" {
		m.view = viewBoard
		if m.columns == 1 {
			m.view = viewCarousel
			if len(m.agents) > 1 {
				m.selected = (m.selected + 1) % len(m.agents)
			}
		}
		m.zoomAgentID = ""
		m.zoomSession = ""
		m.zoomContent = ""
		m.zoomScrollOff = 0
		return m, tea.SetWindowTitle("TicketTok")
	}

	// PgUp/PgDown scroll the zoom view by half a page
	if msg.Type == tea.KeyPgUp || msg.Type == tea.KeyPgDown {
		halfPage := (m.height - 2) / 2
		if halfPage < 1 {
			halfPage = 1
		}
		if msg.Type == tea.KeyPgUp {
			m.zoomScrollOff += halfPage
			maxScroll := m.zoomTotalLines - (m.height - 2)
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.zoomScrollOff > maxScroll {
				m.zoomScrollOff = maxScroll
			}
		} else {
			m.zoomScrollOff -= halfPage
			if m.zoomScrollOff < 0 {
				m.zoomScrollOff = 0
			}
		}
		return m, nil
	}

	// SGR mouse sequence filter (two-phase).
	// When bubbletea fails to parse SGR mouse events, they arrive as two KeyMsgs:
	//   1) alt+[  (ESC [ interpreted as Alt+[)
	//   2) <btn;x;yM  (runes)
	// Phase 1: buffer alt+[ instead of forwarding it.
	if key == "alt+[" && m.zoomSession != "" {
		m.zoomAltBracket = true
		return m, nil
	}
	// Phase 2: if previous key was alt+[, check if this completes an SGR mouse sequence.
	if m.zoomAltBracket {
		m.zoomAltBracket = false
		if msg.Type == tea.KeyRunes && m.zoomSession != "" {
			s := string(msg.Runes)
			if sgrMouseRe.MatchString(s) {
				// Adjust scroll offset (same as handleZoomMouse)
				for _, match := range sgrMouseRe.FindAllStringSubmatch(s, -1) {
					if btn, err := strconv.Atoi(match[1]); err == nil {
						if btn == 64 {
							m.zoomScrollOff += 3
						} else if btn == 65 {
							m.zoomScrollOff -= 3
							if m.zoomScrollOff < 0 {
								m.zoomScrollOff = 0
							}
						}
					}
				}
				return m, nil
			}
		}
		// Not a mouse sequence — flush the buffered alt+[ then fall through
		exec.Command("tmux", "send-keys", "-t", m.zoomSession, "Escape").Run()
		exec.Command("tmux", "send-keys", "-t", m.zoomSession, "-l", "[").Run()
	}

	// Any keypress resets scroll to follow latest output
	if m.zoomScrollOff > 0 {
		m.zoomScrollOff = 0
	}

	// Forward keystroke to tmux session
	m.forwardKeyToTmux(msg)
	return m, nil
}

// forwardKeyToTmux sends a keystroke to the tmux session via send-keys.
func (m *Model) forwardKeyToTmux(msg tea.KeyMsg) {
	if m.zoomSession == "" {
		return
	}

	// Map Bubble Tea key names to tmux key names
	var tmuxKey string
	switch msg.Type {
	case tea.KeyRunes:
		// Regular character input
		exec.Command("tmux", "send-keys", "-t", m.zoomSession, "-l", string(msg.Runes)).Run()
		return
	case tea.KeySpace:
		tmuxKey = "Space"
	case tea.KeyEnter:
		tmuxKey = "Enter"
	case tea.KeyBackspace:
		tmuxKey = "BSpace"
	case tea.KeyTab:
		tmuxKey = "Tab"
	case tea.KeyShiftTab:
		tmuxKey = "BTab"
	case tea.KeyUp:
		tmuxKey = "Up"
	case tea.KeyDown:
		tmuxKey = "Down"
	case tea.KeyLeft:
		tmuxKey = "Left"
	case tea.KeyRight:
		tmuxKey = "Right"
	case tea.KeyDelete:
		tmuxKey = "DC"
	case tea.KeyHome:
		tmuxKey = "Home"
	case tea.KeyEnd:
		tmuxKey = "End"
	case tea.KeyPgUp:
		tmuxKey = "PPage"
	case tea.KeyPgDown:
		tmuxKey = "NPage"
	case tea.KeyCtrlC:
		tmuxKey = "C-c"
	case tea.KeyCtrlD:
		tmuxKey = "C-d"
	case tea.KeyCtrlZ:
		tmuxKey = "C-z"
	case tea.KeyCtrlL:
		tmuxKey = "C-l"
	case tea.KeyCtrlA:
		tmuxKey = "C-a"
	case tea.KeyCtrlE:
		tmuxKey = "C-e"
	case tea.KeyCtrlU:
		tmuxKey = "C-u"
	case tea.KeyCtrlK:
		tmuxKey = "C-k"
	case tea.KeyCtrlW:
		tmuxKey = "C-w"
	case tea.KeyEscape:
		tmuxKey = "Escape"
	default:
		// Try the string representation
		s := msg.String()
		if len(s) == 1 {
			exec.Command("tmux", "send-keys", "-t", m.zoomSession, "-l", s).Run()
			return
		}
		return
	}

	exec.Command("tmux", "send-keys", "-t", m.zoomSession, tmuxKey).Run()
}

func (m *Model) handleSpawnKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		m.view = viewBoard
		if m.columns == 1 {
			m.view = viewCarousel
		}
		return m, nil
	case "tab", "down":
		// Move selection down in suggestions
		if len(m.spawnSuggestions) > 0 {
			m.spawnSelIdx++
			if m.spawnSelIdx >= len(m.spawnSuggestions) {
				m.spawnSelIdx = 0
			}
		}
		return m, nil
	case "shift+tab", "up":
		// Move selection up in suggestions
		if len(m.spawnSuggestions) > 0 {
			m.spawnSelIdx--
			if m.spawnSelIdx < 0 {
				m.spawnSelIdx = len(m.spawnSuggestions) - 1
			}
		}
		return m, nil
	case "enter":
		// If a suggestion is selected, apply it and drill deeper
		if m.spawnSelIdx >= 0 && m.spawnSelIdx < len(m.spawnSuggestions) {
			sel := m.spawnSuggestions[m.spawnSelIdx]
			m.spawnDir.SetValue(sel + "/")
			m.spawnDir.CursorEnd()
			m.spawnSelIdx = -1
			m.refreshSpawnSuggestions()
			return m, nil
		}
		// No selection — spawn
		return m.doSpawn()
	}
	// Forward other keys to textinput, then refresh suggestions
	var cmd tea.Cmd
	m.spawnDir, cmd = m.spawnDir.Update(msg)
	m.refreshSpawnSuggestions()
	return m, cmd
}

func (m *Model) handleSendKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.view = viewBoard
		if m.columns == 1 {
			m.view = viewCarousel
		}
		return m, nil
	case "enter":
		return m.doSend()
	}
	var cmd tea.Cmd
	m.sendInput, cmd = m.sendInput.Update(msg)
	return m, cmd
}

func (m *Model) handleConfirmQuit(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y", "enter":
		return m, tea.Quit
	default:
		m.view = viewBoard
		if m.columns == 1 {
			m.view = viewCarousel
		}
		return m, nil
	}
}

func (m *Model) handleConfirmKill(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y", "enter":
		m.killSelected()
		m.view = viewBoard
		if m.columns == 1 {
			m.view = viewCarousel
		}
		return m, nil
	default:
		m.view = viewBoard
		if m.columns == 1 {
			m.view = viewCarousel
		}
		return m, nil
	}
}

func (m *Model) openSpawnDialog() {
	m.view = viewSpawn
	m.spawnDir.SetValue("~/dev/")
	m.spawnDir.CursorEnd()
	m.spawnDir.Focus()
	m.refreshSpawnSuggestions()
}

func (m *Model) openSendDialog() {
	if len(m.agents) == 0 || m.selected >= len(m.agents) {
		return
	}
	m.view = viewSend
	m.sendInput.SetValue("")
	m.sendInput.Focus()
}

func (m *Model) doSpawn() (tea.Model, tea.Cmd) {
	dir := strings.TrimSpace(m.spawnDir.Value())

	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "dev")
	}
	if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}

	name := deriveNameFromDir(dir)

	agent := m.store.Add(name, dir)
	if err := m.manager.SpawnAgent(agent); err != nil {
		m.setStatus(fmt.Sprintf("Spawn error: %v", err))
	} else {
		m.store.UpdateSessionName(agent.ID, agent.SessionName)
		m.setStatus(fmt.Sprintf("Spawned: %s", name))
	}

	m.agents = m.store.List()
	m.view = viewBoard
	if m.columns == 1 {
		m.view = viewCarousel
		m.selected = len(m.agents) - 1
	}
	return m, nil
}

func (m *Model) doSend() (tea.Model, tea.Cmd) {
	if m.selected >= len(m.agents) {
		return m, nil
	}
	agent := m.agents[m.selected]
	msg := m.sendInput.Value()
	if msg == "" {
		return m, nil
	}

	if err := m.manager.SendKeys(agent, msg); err != nil {
		m.setStatus(fmt.Sprintf("Send error: %v", err))
	} else {
		m.setStatus(fmt.Sprintf("Sent to %s", agent.Name))
	}

	m.view = viewBoard
	if m.columns == 1 {
		m.view = viewCarousel
		if len(m.agents) > 1 {
			m.selected = (m.selected + 1) % len(m.agents)
		}
	}
	return m, nil
}

func (m *Model) enterZoom() (tea.Model, tea.Cmd) {
	if len(m.agents) == 0 || m.selected >= len(m.agents) {
		return m, nil
	}
	agent := m.agents[m.selected]

	if agent.Discovered {
		// PTY-free path: no GetSession/SetSize, just capture directly
		if !IsSessionAlive(agent.SessionName) {
			m.setStatus("External session no longer alive")
			return m, nil
		}
		m.zoomAgentID = agent.ID
		m.zoomSession = agent.SessionName
		m.zoomContent = ""
		m.view = viewZoom
		return m, tea.Batch(
			zoomCaptureCmd(agent.SessionName),
			tea.SetWindowTitle(fmt.Sprintf("TicketTok — %s", agent.Name)),
		)
	}

	sess := m.manager.GetSession(agent)
	if sess == nil || !sess.IsAlive() {
		m.setStatus("No active tmux session — spawn a new agent first")
		return m, nil
	}

	m.zoomAgentID = agent.ID
	m.zoomSession = sess.Name
	m.zoomContent = ""
	m.view = viewZoom

	// Resize tmux pane to match our terminal (delay slightly so Ink can redraw)
	sess.SetSize(m.width, m.height-2)

	return m, tea.Batch(
		zoomCaptureCmd(sess.Name),
		tea.SetWindowTitle(fmt.Sprintf("TicketTok — %s", agent.Name)),
	)
}

// zoomCaptureCmd returns a command that captures the tmux pane content
// including full scrollback history (up to 10000 lines above visible area).
func zoomCaptureCmd(sessionName string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(80 * time.Millisecond)
		out, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-J", "-S", "-10000", "-t", sessionName).Output()
		if err != nil {
			return zoomTickMsg{content: fmt.Sprintf("capture error: %v", err)}
		}
		return zoomTickMsg{content: string(out)}
	}
}

func (m *Model) killSelected() {
	if len(m.agents) == 0 || m.selected >= len(m.agents) {
		return
	}
	agent := m.agents[m.selected]

	// Try manager first (has session in memory)
	sess := m.manager.GetSession(agent)
	if sess != nil {
		_ = m.manager.Kill(agent.ID)
	} else if agent.SessionName != "" {
		// Fallback: kill tmux session by name from state
		_ = KillBySession(agent.SessionName)
	}

	// Clean up hook status file
	cleanHookStatus(agent.ID)

	// Remove from store entirely (not just mark DONE)
	m.store.Remove(agent.ID)
	m.agents = m.store.List()
	m.setStatus(fmt.Sprintf("Killed: %s", agent.Name))
	if m.selected >= len(m.agents) && len(m.agents) > 0 {
		m.selected = len(m.agents) - 1
	}
	if len(m.agents) == 0 {
		m.selected = 0
	}
}

func (m *Model) refreshStatuses() {
	for _, agent := range m.agents {
		newStatus := m.manager.DetectStatus(agent)
		if newStatus != agent.Status {
			m.store.Update(agent.ID, newStatus)
		}
	}

	// Auto-remove discovered agents that have been DONE for >30s
	for _, agent := range m.agents {
		if agent.Discovered && agent.Status == StatusDone &&
			time.Since(agent.StatusSince) > 30*time.Second {
			m.store.Remove(agent.ID)
		}
	}
}

func (m *Model) discoverAgents() {
	found := discoverTmuxClaude()
	before := len(m.agents)
	m.mergeDiscovered(found)
	m.agents = m.store.List()
	added := len(m.agents) - before

	// Count total external agents for a more informative message
	totalExt := 0
	for _, a := range m.agents {
		if a.Discovered && a.Status != StatusDone {
			totalExt++
		}
	}
	if added > 0 {
		m.setStatus(fmt.Sprintf("Discovered %d new agent(s)", added))
	} else if totalExt > 0 {
		m.setStatus(fmt.Sprintf("No new agents (%d external tracked)", totalExt))
	} else {
		m.setStatus("No external Claude sessions found")
	}
}

func (m *Model) setStatus(msg string) {
	m.statusMsg = msg
	m.statusExpires = time.Now().Add(5 * time.Second)
}

func (m *Model) updateSpawnInputs(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.spawnDir, cmd = m.spawnDir.Update(msg)
	return cmd
}

// expandTilde replaces a leading ~/ with the user's home directory.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// collapseTilde replaces the home directory prefix with ~/ for display.
func collapseTilde(path string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(path, home+"/") {
		return "~/" + path[len(home)+1:]
	}
	if path == home {
		return "~"
	}
	return path
}

// listSubdirs returns sorted subdirectory paths under dir (with ~/ prefix for display).
func listSubdirs(dir string) []string {
	expanded := expandTilde(dir)
	entries, err := os.ReadDir(expanded)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(expanded, e.Name())
		dirs = append(dirs, collapseTilde(full))
	}
	sort.Strings(dirs)
	if len(dirs) > 20 {
		dirs = dirs[:20]
	}
	return dirs
}

// refreshSpawnSuggestions updates the suggestion list based on current input.
func (m *Model) refreshSpawnSuggestions() {
	val := m.spawnDir.Value()
	if val == "" {
		m.spawnSuggestions = nil
		m.spawnSelIdx = -1
		return
	}

	// Determine base directory and partial name
	var baseDir, partial string
	if strings.HasSuffix(val, "/") {
		// Input ends with / — list contents of that directory
		baseDir = val
		partial = ""
	} else {
		// Input has a partial name — split into dir + prefix
		baseDir = filepath.Dir(val) + "/"
		partial = filepath.Base(val)
	}

	all := listSubdirs(baseDir)
	if partial == "" {
		m.spawnSuggestions = all
	} else {
		lowerPartial := strings.ToLower(partial)
		var filtered []string
		for _, s := range all {
			name := filepath.Base(s)
			if strings.HasPrefix(strings.ToLower(name), lowerPartial) {
				filtered = append(filtered, s)
			}
		}
		m.spawnSuggestions = filtered
	}
	m.spawnSelIdx = -1
}

// View renders the full UI.
func (m Model) View() string {
	switch m.view {
	case viewZoom:
		return m.viewZoom()
	case viewSpawn:
		return m.viewSpawn()
	case viewSend:
		return m.viewSend()
	case viewConfirmQuit:
		return m.viewConfirmQuit()
	case viewConfirmKill:
		return m.viewConfirmKill()
	case viewCarousel:
		return m.viewCarousel()
	default:
		return m.viewBoard()
	}
}

func (m Model) viewZoom() string {
	// Header bar
	name := m.zoomAgentID
	if m.selected < len(m.agents) {
		name = m.agents[m.selected].Name
	}
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(ui.ColorAccent).
		Render(fmt.Sprintf(" ZOOM: %s ", name))
	help := ui.HelpStyle.Render("[Ctrl+Q] return to dashboard")
	gap := m.width - lipgloss.Width(header) - lipgloss.Width(help) - 1
	if gap < 1 {
		gap = 1
	}
	topBar := header + strings.Repeat(" ", gap) + help

	// Pane content — show a window into the full scrollback.
	// zoomScrollOff=0 means "follow bottom" (show latest output).
	content := m.zoomContent
	lines := strings.Split(content, "\n")
	maxLines := m.height - 2 // header + bottom margin
	if maxLines < 1 {
		maxLines = 1
	}

	// Calculate the visible window
	end := len(lines) - m.zoomScrollOff
	if end < maxLines {
		end = maxLines
	}
	if end > len(lines) {
		end = len(lines)
	}
	start := end - maxLines
	if start < 0 {
		start = 0
	}
	visible := lines[start:end]
	body := strings.Join(visible, "\n")

	// Show scroll indicator when not at bottom
	if m.zoomScrollOff > 0 {
		indicator := ui.HelpStyle.Render(fmt.Sprintf(" [scrolled +%d lines] ", m.zoomScrollOff))
		topBar = topBar + "\n" + indicator
	}

	return topBar + "\n" + body
}

func (m Model) viewBoard() string {
	title := ui.RenderTitle(m.width, len(m.agents), m.columns)
	footer := ui.RenderFooter(m.width, m.columns)

	var status string
	if m.statusMsg != "" && time.Now().Before(m.statusExpires) {
		status = ui.DimText.Render("  " + m.statusMsg)
	}

	titleHeight := lipgloss.Height(title) + 1 // +1 for blank line
	footerHeight := lipgloss.Height(footer)
	if status != "" {
		footerHeight += lipgloss.Height(status)
	}
	boardHeight := m.height - titleHeight - footerHeight - 1
	if boardHeight < 5 {
		boardHeight = 5
	}

	cards := m.buildCardData()
	board := ui.RenderBoard(cards, m.selected, m.columns, m.width, boardHeight)

	// Crop board to available height
	board = cropToHeight(board, boardHeight, m.scrollOffset)

	content := lipgloss.JoinVertical(lipgloss.Left, title, "", board)

	contentHeight := lipgloss.Height(content)
	gap := m.height - contentHeight - footerHeight - 1
	if gap > 0 {
		content += strings.Repeat("\n", gap)
	}

	return lipgloss.JoinVertical(lipgloss.Left, content, status, footer)
}

func (m Model) viewCarousel() string {
	title := ui.RenderTitle(m.width, len(m.agents), 1)
	footer := ui.RenderFooter(m.width, 1)

	var status string
	if m.statusMsg != "" && time.Now().Before(m.statusExpires) {
		status = ui.DimText.Render("  " + m.statusMsg)
	}

	titleHeight := lipgloss.Height(title) + 1
	footerHeight := lipgloss.Height(footer)
	if status != "" {
		footerHeight += lipgloss.Height(status)
	}
	carouselHeight := m.height - titleHeight - footerHeight - 1
	if carouselHeight < 5 {
		carouselHeight = 5
	}

	cards := m.buildCardData()
	carousel := ui.RenderCarousel(cards, m.selected, m.width, m.height)

	// Crop to available height with scroll support
	carousel = cropToHeight(carousel, carouselHeight, m.scrollOffset)

	content := lipgloss.JoinVertical(lipgloss.Left, title, "", carousel)

	contentHeight := lipgloss.Height(content)
	gap := m.height - contentHeight - footerHeight - 1
	if gap > 0 {
		content += strings.Repeat("\n", gap)
	}

	return lipgloss.JoinVertical(lipgloss.Left, content, status, footer)
}

func (m Model) viewSpawn() string {
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorAccent).
		Padding(1, 2).
		Width(70)

	title := ui.AgentName.Render("Spawn New Agent")

	fields := lipgloss.JoinVertical(lipgloss.Left,
		"Directory:", m.spawnDir.View(),
	)

	// Render suggestion list (max 8 visible)
	maxShow := 8
	if len(m.spawnSuggestions) < maxShow {
		maxShow = len(m.spawnSuggestions)
	}
	var suggLines []string
	for i := 0; i < maxShow; i++ {
		name := filepath.Base(m.spawnSuggestions[i])
		if i == m.spawnSelIdx {
			suggLines = append(suggLines, lipgloss.NewStyle().
				Foreground(ui.ColorAccent).Bold(true).
				Render("  > "+name))
		} else {
			suggLines = append(suggLines, lipgloss.NewStyle().
				Foreground(ui.ColorDim).
				Render("    "+name))
		}
	}
	suggestions := strings.Join(suggLines, "\n")

	var help string
	if len(m.spawnSuggestions) > 0 {
		help = ui.HelpStyle.Render("[Enter] select  [Tab/↓] next  [Esc] cancel")
	} else {
		help = ui.HelpStyle.Render("[Enter] spawn  [Esc] cancel")
	}

	var parts []string
	parts = append(parts, title, "", fields)
	if suggestions != "" {
		parts = append(parts, suggestions)
	}
	parts = append(parts, "", help)

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	rendered := dialog.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, rendered)
}

func (m Model) viewSend() string {
	if m.selected >= len(m.agents) {
		return ""
	}
	agent := m.agents[m.selected]

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorAccent).
		Padding(1, 2).
		Width(70)

	title := ui.AgentName.Render(fmt.Sprintf("Send to: %s", agent.Name))

	content := lipgloss.JoinVertical(lipgloss.Left,
		title, "",
		"Message:", m.sendInput.View(), "",
		ui.HelpStyle.Render("[Enter] send  [Esc] cancel"),
	)

	rendered := dialog.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, rendered)
}

func (m Model) viewConfirmQuit() string {
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorWaiting).
		Padding(1, 2).
		Width(50)

	content := lipgloss.JoinVertical(lipgloss.Left,
		ui.AgentName.Render("Quit TicketTok?"),
		"",
		"Running agents will continue in tmux.",
		"",
		ui.HelpStyle.Render("[Y] quit  [N/Esc] cancel"),
	)

	rendered := dialog.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, rendered)
}

func (m Model) viewConfirmKill() string {
	name := "(none)"
	isDiscovered := false
	if m.selected < len(m.agents) {
		name = m.agents[m.selected].Name
		isDiscovered = m.agents[m.selected].Discovered
	}

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorWaiting).
		Padding(1, 2).
		Width(50)

	warning := "This will destroy the tmux session."
	if isDiscovered {
		warning = "This is an external session. Killing it will terminate the Claude instance."
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		ui.AgentName.Render(fmt.Sprintf("Kill agent: %s?", name)),
		"",
		warning,
		"",
		ui.HelpStyle.Render("[Y] kill  [N/Esc] cancel"),
	)

	rendered := dialog.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, rendered)
}

// cropToHeight takes rendered content and crops it to maxLines,
// skipping lines proportional to scrollOffset (each card ~7 lines).
func cropToHeight(content string, maxLines int, scrollOffset int) string {
	lines := strings.Split(content, "\n")
	skipLines := scrollOffset * 7 // approximate lines per card
	if skipLines >= len(lines) {
		skipLines = 0
	}
	lines = lines[skipLines:]
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func (m Model) buildCardData() []ui.CardData {
	now := time.Now()
	cards := make([]ui.CardData, len(m.agents))
	for i, a := range m.agents {
		info := m.manager.GetPaneInfo(a, 13)
		cards[i] = ui.CardData{
			Name:       a.Name,
			Dir:        a.Dir,
			Status:     string(a.Status),
			Mode:       info.Mode,
			Uptime:     now.Sub(a.CreatedAt),
			Since:      now.Sub(a.StatusSince),
			Preview:    info.Preview,
			Selected:   i == m.selected,
			Discovered: a.Discovered,
		}
	}
	return cards
}

// discoverCmd runs discovery asynchronously and returns a discoverMsg.
func discoverCmd() tea.Cmd {
	return func() tea.Msg {
		found := discoverTmuxClaude()
		return discoverMsg{found: found}
	}
}

// reconcileCmd checks discovered agents in state and marks stale ones DONE.
func reconcileCmd(store *Store) tea.Cmd {
	return func() tea.Msg {
		for _, a := range store.List() {
			if a.Discovered && a.Status != StatusDone {
				if !IsSessionAlive(a.SessionName) {
					store.Update(a.ID, StatusDone)
				}
			}
		}
		return reconcileMsg{}
	}
}

// mergeDiscovered adds newly found external agents that aren't already tracked.
func (m *Model) mergeDiscovered(found []DiscoveredAgent) {
	for _, d := range found {
		// Check if already tracked by session name
		var match *Agent
		for _, a := range m.agents {
			if a.SessionName == d.SessionName {
				match = a
				break
			}
		}
		if match != nil {
			// Revive dead agents whose session came back (reused tmux session name)
			if match.Status == StatusDone {
				m.store.Update(match.ID, StatusRunning)
				m.store.UpdateDiscovered(match.ID, true)
			}
			continue
		}
		agent := m.store.Add(d.Name, d.Dir)
		agent.SessionName = d.SessionName
		agent.Discovered = true
		m.store.UpdateSessionName(agent.ID, d.SessionName)
		m.store.UpdateDiscovered(agent.ID, true)
	}
}
