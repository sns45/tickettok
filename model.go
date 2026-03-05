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

// estimatedCardHeight is the approximate rendered height of a board card in lines.
// Cards have: border(2) + header(1) + dir(1) + uptime(1) + sep(1) + preview(1+) + optional title(1).
// Using 10 as a conservative estimate avoids underscrolling when cards have preview content.
const estimatedCardHeight = 10

// View modes
type viewMode int

const (
	viewBoard    viewMode = iota
	viewCarousel
	viewZoom
	viewSpawn
	viewSend
	viewConfirmKill
	viewConfirmAutoApprove
	viewWorkspace
	viewBatch
)

// spawnFocus tracks which section of the spawn dialog has focus.
type spawnFocus int

const (
	focusBackend spawnFocus = iota // arrow keys change backend selection
	focusDir                       // typing goes to textinput, arrows navigate suggestions
	focusApprove                   // auto-approve toggle
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
	spawnDir         textinput.Model
	spawnSuggestions []string  // filtered directory matches
	spawnSelIdx      int       // selected suggestion index (-1 = none)
	spawnBackends    []Backend // available backends (populated on dialog open)
	spawnBackendIdx  int       // currently selected backend index
	spawnFocus       spawnFocus // focusBackend, focusDir, or focusApprove
	spawnAutoApprove bool       // toggle: bypass permission checks

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

	// Cached card data (refreshed on tick, not every render)
	cachedCards []ui.CardData

	// Batch dialog
	batchOptions []batchOption // computed when opening dialog

	// Tick counter for periodic re-discovery
	tickCount int

	// Update state
	updateAvailable bool
	latestVersion   string
	updateAssetURL  string
	updating        bool
	shouldReExec    bool

	// Workspace dialog
	wsNames         []string        // cached workspace names
	wsSelected      int             // selected index in list
	wsSaveMode      bool            // true = typing name to save
	wsNameInput     textinput.Model // text input for save-as name
	activeWorkspace string          // name of last loaded/saved workspace
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

	wsInput := textinput.New()
	wsInput.Placeholder = "workspace name"
	wsInput.CharLimit = 50
	wsInput.Width = 40

	return Model{
		store:       store,
		manager:     manager,
		agents:      store.List(),
		columns:     3,
		view:        viewBoard,
		width:       120,
		height:      40,
		spawnDir:    dirInput,
		sendInput:   sendInput,
		wsNameInput: wsInput,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		tea.ClearScreen,
		discoverCmd(),
		reconcileCmd(m.store),
		tea.SetWindowTitle("TicketTok"),
		checkUpdateCmd(),
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
		m.cachedCards = m.buildCardData()
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

	case updateCheckMsg:
		if msg.available {
			m.updateAvailable = true
			m.latestVersion = msg.latest
			m.updateAssetURL = msg.assetURL
		}
		return m, nil

	case updateDoneMsg:
		m.updating = false
		if msg.err != nil {
			m.setStatus(fmt.Sprintf("Update failed: %v", msg.err))
			return m, nil
		}
		m.shouldReExec = true
		m.setStatus(fmt.Sprintf("Updated to v%s! Restarting...", msg.version))
		return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
			return forceQuitMsg{}
		})

	case forceQuitMsg:
		return m, tea.Quit

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

	default:
		// Update text inputs if in dialog
		var cmd tea.Cmd
		switch m.view {
		case viewSpawn:
			cmd = m.updateSpawnInputs(msg)
		case viewSend:
			m.sendInput, cmd = m.sendInput.Update(msg)
		case viewWorkspace:
			if m.wsSaveMode {
				m.wsNameInput, cmd = m.wsNameInput.Update(msg)
			}
		}
		return m, cmd
	}
}

func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.view == viewZoom {
		return m.handleZoomMouse(msg)
	}

	// Mouse wheel scrolls the viewport without changing selection
	n := len(m.agents)
	if n == 0 {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.scrollOffset > 0 {
			m.scrollOffset--
		}
	case tea.MouseButtonWheelDown:
		maxScroll := m.maxScrollRows() - 1
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.scrollOffset < maxScroll {
			m.scrollOffset++
		}
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
	case m.view == viewConfirmKill:
		return m.handleConfirmKill(key)
	case m.view == viewConfirmAutoApprove:
		return m.handleConfirmAutoApprove(key)
	case m.view == viewBatch:
		return m.handleBatchKey(key)
	case m.view == viewSpawn:
		return m.handleSpawnKey(msg)
	case m.view == viewWorkspace:
		return m.handleWorkspaceKey(msg)
	case m.view == viewSend:
		return m.handleSendKey(msg)
	}

	// Board/carousel keys
	switch key {
	case "q", "ctrl+q", "ctrl+c":
		return m, tea.Quit
	case "n":
		m.openSpawnDialog()
		return m, nil
	case "w":
		m.openWorkspaceDialog()
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
	case "b":
		m.openBatchDialog()
		return m, nil
	case "u":
		if m.updateAvailable && !m.updating {
			m.updating = true
			m.setStatus(fmt.Sprintf("Downloading v%s...", m.latestVersion))
			return m, doUpdateCmd(m.updateAssetURL, m.latestVersion)
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
	case "a":
		m.toggleAutoApprove()
	case "r", "R":
		return m.restartStuckAgent()
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

	// Target column, skipping empty columns in the delta direction
	maxCol := m.columns - 1
	targetCol := curCol + delta
	for targetCol >= 0 && targetCol <= maxCol {
		// Check if any agent lives in this column
		hasAgent := false
		for i := 0; i < n; i++ {
			if cols[i] == targetCol {
				hasAgent = true
				break
			}
		}
		if hasAgent {
			break
		}
		targetCol += delta
	}
	if targetCol < 0 || targetCol > maxCol || targetCol == curCol {
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
		// 2-col: IDLE/DONE=0, ACTIVE(RUNNING+WAITING+STUCK)=1
		switch status {
		case StatusRunning, StatusWaiting, StatusError:
			return 1
		default:
			return 0
		}
	}
	// 3-col: IDLE/DONE=0, WAITING/STUCK=1, RUNNING=2
	switch status {
	case StatusWaiting, StatusError:
		return 1
	case StatusRunning:
		return 2
	default:
		return 0
	}
}

// ensureSelectedVisible adjusts scrollOffset so the selected agent's card is on screen.
func (m *Model) ensureSelectedVisible() {
	maxVisible := m.maxVisibleCards()

	// Use visual row (position within column) instead of flat index
	// so multi-column layouts scroll correctly.
	row := m.visualRow(m.selected)

	if row < m.scrollOffset {
		m.scrollOffset = row
	} else if row >= m.scrollOffset+maxVisible {
		m.scrollOffset = row - maxVisible + 1
	}
}

// maxVisibleCards returns how many card rows fit in the viewport.
// Conservative estimate ensures we scroll before cards get cut off.
func (m *Model) maxVisibleCards() int {
	// 7 lines of chrome: title bar, blank line, column headers, footer, status, gaps
	viewportLines := m.height - 7
	if viewportLines < estimatedCardHeight {
		viewportLines = estimatedCardHeight
	}
	n := viewportLines / estimatedCardHeight
	if n < 1 {
		n = 1
	}
	return n
}

// visualRow returns the visual row of agent at flat index idx.
// In carousel mode (1 col), this is the flat index.
// In board mode (2/3 col), this is the agent's position within its column.
func (m *Model) visualRow(idx int) int {
	if m.columns == 1 || idx >= len(m.agents) {
		return idx
	}
	col := m.columnForStatus(m.agents[idx].Status)
	row := 0
	for i, a := range m.agents {
		if i == idx {
			return row
		}
		if m.columnForStatus(a.Status) == col {
			row++
		}
	}
	return row
}

// maxScrollRows returns the number of card rows in the tallest column.
// In carousel mode, this is the total agent count.
func (m *Model) maxScrollRows() int {
	if len(m.agents) == 0 {
		return 0
	}
	if m.columns == 1 {
		return len(m.agents)
	}
	colCounts := make(map[int]int)
	for _, a := range m.agents {
		colCounts[m.columnForStatus(a.Status)]++
	}
	maxCol := 0
	for _, c := range colCounts {
		if c > maxCol {
			maxCol = c
		}
	}
	return maxCol
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
	case "a":
		m.toggleAutoApprove()
	case "r", "R":
		return m.restartStuckAgent()
	}
	m.ensureSelectedVisible()
	return m, nil
}

func (m *Model) handleZoomKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Ctrl+Q exits zoom
	if key == "ctrl+q" {
		zoomedID := m.zoomAgentID

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

		// Immediate status refresh for the agent we just exited
		if agent := m.store.Get(zoomedID); agent != nil {
			newStatus := m.manager.DetectStatus(agent)
			if newStatus != agent.Status {
				m.store.Update(agent.ID, newStatus)
			}
		}
		m.agents = m.store.List()
		m.cachedCards = m.buildCardData()

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
	case tea.KeyCtrlJ:
		// Shift+Enter arrives as LF (0x0a) in Warp/iTerm. Forward as tmux
		// key name "C-j" so tmux sends 0x0a to the pane (not CR/Enter).
		exec.Command("tmux", "send-keys", "-t", m.zoomSession, "C-j").Run()
		return
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
	case tea.KeyCtrlT:
		tmuxKey = "C-t"
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

	// Esc always exits
	if key == "esc" {
		m.view = viewBoard
		if m.columns == 1 {
			m.view = viewCarousel
		}
		return m, nil
	}

	if m.spawnFocus == focusBackend {
		return m.handleSpawnBackendKey(msg)
	}
	if m.spawnFocus == focusApprove {
		return m.handleSpawnApproveKey(msg)
	}
	return m.handleSpawnDirKey(msg)
}

func (m *Model) handleSpawnBackendKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "up":
		if m.spawnBackendIdx > 0 {
			m.spawnBackendIdx--
		}
		return m, nil
	case "down":
		if m.spawnBackendIdx < len(m.spawnBackends)-1 {
			m.spawnBackendIdx++
		} else {
			// Past last backend → switch to directory
			m.spawnFocus = focusDir
			m.spawnDir.Focus()
		}
		return m, nil
	case "enter":
		m.spawnFocus = focusDir
		m.spawnDir.Focus()
		return m, nil
	}
	// Any rune key → switch to dir and forward to textinput
	if msg.Type == tea.KeyRunes {
		m.spawnFocus = focusDir
		m.spawnDir.Focus()
		var cmd tea.Cmd
		m.spawnDir, cmd = m.spawnDir.Update(msg)
		m.refreshSpawnSuggestions()
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleSpawnDirKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.spawnSelIdx == -1 {
		// Text input focused (no suggestion highlighted)
		switch key {
		case "up":
			if len(m.spawnBackends) > 1 {
				m.spawnFocus = focusBackend
				m.spawnDir.Blur()
				m.spawnBackendIdx = len(m.spawnBackends) - 1
			}
			return m, nil
		case "down", "tab":
			if len(m.spawnSuggestions) > 0 {
				m.spawnSelIdx = 0
			} else if m.spawnSelectedBackendSupportsAutoApprove() {
				m.spawnFocus = focusApprove
				m.spawnDir.Blur()
			}
			return m, nil
		case "enter":
			return m.doSpawn()
		}
	} else {
		// Suggestion highlighted
		switch key {
		case "up":
			m.spawnSelIdx--
			// If goes to -1, back to text input (stays in focusDir)
			return m, nil
		case "down":
			if m.spawnSelIdx < len(m.spawnSuggestions)-1 {
				m.spawnSelIdx++
			} else if m.spawnSelectedBackendSupportsAutoApprove() {
				// Past last suggestion → move to approve toggle
				m.spawnFocus = focusApprove
				m.spawnDir.Blur()
				m.spawnSelIdx = -1
			}
			return m, nil
		case "enter":
			if m.spawnSelIdx >= 0 && m.spawnSelIdx < len(m.spawnSuggestions) {
				sel := m.spawnSuggestions[m.spawnSelIdx]
				m.spawnDir.SetValue(sel + "/")
				m.spawnDir.CursorEnd()
				m.spawnSelIdx = -1
				m.refreshSpawnSuggestions()
			}
			return m, nil
		}
		// Any rune key → reset selection, forward to textinput
		if msg.Type == tea.KeyRunes {
			m.spawnSelIdx = -1
			var cmd tea.Cmd
			m.spawnDir, cmd = m.spawnDir.Update(msg)
			m.refreshSpawnSuggestions()
			return m, cmd
		}
		return m, nil
	}

	// Forward other keys to textinput, then refresh suggestions
	var cmd tea.Cmd
	m.spawnDir, cmd = m.spawnDir.Update(msg)
	m.refreshSpawnSuggestions()
	return m, cmd
}

func (m *Model) handleSpawnApproveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "up":
		m.spawnFocus = focusDir
		m.spawnDir.Focus()
		// If there are suggestions, select the last one
		if len(m.spawnSuggestions) > 0 {
			m.spawnSelIdx = len(m.spawnSuggestions) - 1
			if m.spawnSelIdx > 7 {
				m.spawnSelIdx = 7 // max visible
			}
		}
		return m, nil
	case " ":
		m.spawnAutoApprove = !m.spawnAutoApprove
		return m, nil
	case "enter":
		return m.doSpawn()
	}
	// Any rune key → switch to dir input and forward
	if msg.Type == tea.KeyRunes {
		m.spawnFocus = focusDir
		m.spawnDir.Focus()
		var cmd tea.Cmd
		m.spawnDir, cmd = m.spawnDir.Update(msg)
		m.refreshSpawnSuggestions()
		return m, cmd
	}
	return m, nil
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
	m.spawnBackends = AvailableBackends()
	m.spawnBackendIdx = 0
	m.spawnFocus = focusDir
	m.spawnSelIdx = -1
	m.spawnAutoApprove = false
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

	// Create directory if it doesn't exist
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			m.setStatus(fmt.Sprintf("Cannot create dir: %v", err))
			m.view = viewBoard
			return m, nil
		}
	}

	name := deriveNameFromDir(dir)

	agent := m.store.Add(name, dir)
	// Set backend from spawn dialog selection
	if len(m.spawnBackends) > 0 && m.spawnBackendIdx < len(m.spawnBackends) {
		agent.BackendID = m.spawnBackends[m.spawnBackendIdx].ID()
	}
	agent.AutoApprove = m.spawnAutoApprove
	var spawnArgs []string
	if agent.AutoApprove {
		spawnArgs = agent.Backend().AutoApproveArgs()
	}
	if err := m.manager.SpawnAgent(agent, spawnArgs); err != nil {
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
		// Dead session — respawn with --continue to resume the conversation
		if err := m.manager.RespawnAgent(agent); err != nil {
			m.setStatus(fmt.Sprintf("Resume error: %v", err))
			return m, nil
		}
		m.store.UpdateSessionName(agent.ID, agent.SessionName)
		m.store.Update(agent.ID, StatusRunning)
		m.agents = m.store.List()
		m.setStatus(fmt.Sprintf("Resumed: %s", agent.Name))
		sess = m.manager.GetSession(agent)
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
	agent.Backend().CleanHookStatus(agent.ID)

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

func (m *Model) toggleAutoApprove() {
	if len(m.agents) == 0 || m.selected >= len(m.agents) {
		return
	}
	agent := m.agents[m.selected]

	// Only supported for backends that have auto-approve args
	if len(agent.Backend().AutoApproveArgs()) == 0 {
		m.setStatus(fmt.Sprintf("%s does not support auto-approve", agent.Backend().Name()))
		return
	}

	// For DONE agents, just flip the flag without confirmation (no respawn needed)
	if agent.Status == StatusDone {
		agent.AutoApprove = !agent.AutoApprove
		m.store.Save()
		label := "ON"
		if !agent.AutoApprove {
			label = "OFF"
		}
		m.setStatus(fmt.Sprintf("Auto-approve %s for %s", label, agent.Name))
		return
	}

	// Alive agent: show confirmation since it requires kill+respawn
	m.view = viewConfirmAutoApprove
}

func (m *Model) handleConfirmAutoApprove(key string) (tea.Model, tea.Cmd) {
	returnView := viewBoard
	if m.columns == 1 {
		returnView = viewCarousel
	}

	switch key {
	case "y", "Y", "enter":
		m.doToggleAutoApprove()
	default:
		m.setStatus("Auto-approve toggle cancelled")
	}

	m.view = returnView
	return m, nil
}

func (m *Model) doToggleAutoApprove() {
	if len(m.agents) == 0 || m.selected >= len(m.agents) {
		return
	}
	agent := m.agents[m.selected]

	agent.AutoApprove = !agent.AutoApprove
	m.store.Save()

	label := "ON"
	if !agent.AutoApprove {
		label = "OFF"
	}

	// Kill and respawn with new setting
	_ = m.manager.Kill(agent.ID)
	if agent.SessionName != "" {
		_ = KillBySession(agent.SessionName)
	}
	agent.Backend().CleanHookStatus(agent.ID)

	if err := m.manager.RespawnAgent(agent); err != nil {
		m.setStatus(fmt.Sprintf("Respawn failed: %v", err))
		return
	}
	m.store.UpdateSessionName(agent.ID, agent.SessionName)
	agent.Status = StatusRunning
	agent.StatusSince = time.Now()
	m.store.Save()

	m.setStatus(fmt.Sprintf("Auto-approve %s for %s", label, agent.Name))
}

func (m *Model) refreshStatuses() {
	// Track transitions for notifications
	var transitions []statusTransition

	for _, agent := range m.agents {
		oldStatus := agent.Status
		newStatus := m.manager.DetectStatus(agent)
		if newStatus != oldStatus {
			m.store.Update(agent.ID, newStatus)
			transitions = append(transitions, statusTransition{agent.Name, oldStatus, newStatus})
		}
	}

	// Stuck detection: RUNNING >10min with no recent hook activity
	for _, agent := range m.agents {
		if agent.Status == StatusRunning && !agent.Discovered &&
			time.Since(agent.StatusSince) > 10*time.Minute {
			// Check if hook file is stale or missing
			hookPath := filepath.Join(hookStatusDir(), agent.ID+".json")
			info, err := os.Stat(hookPath)
			if err != nil || time.Since(info.ModTime()) > 5*time.Minute {
				m.store.Update(agent.ID, StatusError)
				transitions = append(transitions, statusTransition{agent.Name, StatusRunning, StatusError})
			}
		}
	}

	// Notify on transitions
	if len(transitions) > 0 {
		m.notifyTransitions(transitions)
	}

	// Auto-remove discovered agents that have been DONE for >30s
	for _, agent := range m.agents {
		if agent.Discovered && agent.Status == StatusDone &&
			time.Since(agent.StatusSince) > 30*time.Second {
			m.store.Remove(agent.ID)
		}
	}
}

// statusTransition records a single agent status change.
type statusTransition struct {
	name  string
	oldSt AgentStatus
	newSt AgentStatus
}

// notifyTransitions shows a status bar message and rings the bell for WAITING transitions.
func (m *Model) notifyTransitions(transitions []statusTransition) {
	// Priority: WAITING > STUCK > DONE > IDLE > RUNNING
	priority := func(s AgentStatus) int {
		switch s {
		case StatusWaiting:
			return 5
		case StatusError:
			return 4
		case StatusDone:
			return 3
		case StatusIdle:
			return 2
		default:
			return 1
		}
	}

	// Find highest priority transition
	best := 0
	for i, t := range transitions {
		if priority(t.newSt) > priority(transitions[best].newSt) {
			best = i
		}
	}

	t := transitions[best]
	msg := fmt.Sprintf("%s: %s \u2192 %s", t.name, t.oldSt, t.newSt)
	if len(transitions) > 1 {
		msg += fmt.Sprintf(" (+%d more)", len(transitions)-1)
	}
	m.setStatus(msg)

	// Ring terminal bell for transitions that need attention
	if t.newSt == StatusWaiting || t.newSt == StatusError {
		fmt.Print("\a")
	}
}

func (m *Model) discoverAgents() {
	var found []DiscoveredAgent
	for _, b := range AllBackends() {
		found = append(found, b.Discover()...)
	}
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
		m.setStatus("No external agent sessions found")
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
		// No partial typed — show first batch only (display caps at 8 visible)
		m.spawnSuggestions = all
		m.spawnSelIdx = -1
		return
	}
	{
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

// spawnSelectedBackendSupportsAutoApprove returns true if the currently selected
// backend has auto-approve CLI args.
func (m Model) spawnSelectedBackendSupportsAutoApprove() bool {
	if len(m.spawnBackends) > 0 && m.spawnBackendIdx < len(m.spawnBackends) {
		return len(m.spawnBackends[m.spawnBackendIdx].AutoApproveArgs()) > 0
	}
	return false
}

// View renders the full UI.
func (m Model) View() string {
	switch m.view {
	case viewZoom:
		return m.viewZoom()
	case viewSpawn:
		return m.viewSpawn()
	case viewWorkspace:
		return m.viewWorkspace()
	case viewSend:
		return m.viewSend()
	case viewConfirmKill:
		return m.viewConfirmKill()
	case viewConfirmAutoApprove:
		return m.viewConfirmAutoApprove()
	case viewBatch:
		return m.viewBatchDialog()
	case viewCarousel:
		return m.viewCarousel()
	default:
		return m.viewBoard()
	}
}

func (m Model) viewZoom() string {
	// Resolve agent info
	name := m.zoomAgentID
	var dir string
	if m.selected < len(m.agents) {
		agent := m.agents[m.selected]
		name = agent.Name
		dir = agent.Dir
		if title := GetPaneTitle(agent.SessionName); title != "" {
			name = title
		}
	}

	// Title bar
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(ui.ColorAccent).
		Render(fmt.Sprintf(" ZOOM: %s ", name))
	if dir != "" {
		header += lipgloss.NewStyle().Foreground(ui.ColorDim).Render("  " + dir)
	}
	if m.zoomScrollOff > 0 {
		header += ui.HelpStyle.Render(fmt.Sprintf("  [scrolled +%d lines]", m.zoomScrollOff))
	}

	// Horizontal rules
	rule := lipgloss.NewStyle().Foreground(ui.ColorBorder).Render(strings.Repeat("─", m.width))

	// Footer (pinned to bottom, matching dashboard style)
	footerKeys := ui.HelpStyle.Render("[Ctrl+Q] dashboard  [Ctrl+J] newline  [PgUp/PgDn] scroll")
	footer := rule + "\n" + " " + footerKeys

	// Calculate content area: total height minus header(1) + top rule(1) + bottom rule(1) + footer text(1)
	headerHeight := 2 // header line + rule
	footerHeight := 2 // rule + footer text
	maxLines := m.height - headerHeight - footerHeight
	if maxLines < 1 {
		maxLines = 1
	}

	// Pane content — show a window into the full scrollback.
	content := m.zoomContent
	lines := strings.Split(content, "\n")

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

	// Pad body to push footer to bottom
	for len(visible) < maxLines {
		visible = append(visible, "")
	}

	body := strings.Join(visible, "\n")

	return header + "\n" + rule + "\n" + body + "\n" + footer
}

func (m Model) viewBoard() string {
	updateVer := ""
	if m.updateAvailable && !m.updating {
		updateVer = m.latestVersion
	}
	title := ui.RenderTitle(m.width, len(m.agents), m.columns, updateVer, m.activeWorkspace)
	footer := ui.RenderFooter(m.width, m.columns, m.updateAvailable && !m.updating)

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

	cards := m.getCards()
	maxVisible := m.maxVisibleCards()
	board := ui.RenderBoard(cards, m.selected, m.columns, m.width, boardHeight, m.scrollOffset, maxVisible)

	// Safety clip: trim any overflow without scroll math
	board = clipHeight(board, boardHeight)

	content := lipgloss.JoinVertical(lipgloss.Left, title, "", board)

	contentHeight := lipgloss.Height(content)
	gap := m.height - contentHeight - footerHeight - 1
	if gap > 0 {
		content += strings.Repeat("\n", gap)
	}

	return lipgloss.JoinVertical(lipgloss.Left, content, status, footer)
}

func (m Model) viewCarousel() string {
	updateVer := ""
	if m.updateAvailable && !m.updating {
		updateVer = m.latestVersion
	}
	title := ui.RenderTitle(m.width, len(m.agents), 1, updateVer, m.activeWorkspace)
	footer := ui.RenderFooter(m.width, 1, m.updateAvailable && !m.updating)

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

	cards := m.getCards()
	maxVisible := m.maxVisibleCards()
	carousel := ui.RenderCarousel(cards, m.selected, m.width, m.height, m.scrollOffset, maxVisible)

	// Safety clip: trim any overflow without scroll math
	carousel = clipHeight(carousel, carouselHeight)

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

	// Render backend selector (vertical radio-style list)
	var backendLines []string
	if len(m.spawnBackends) > 1 {
		backendLines = append(backendLines, "Backend:")
		for i, b := range m.spawnBackends {
			indicator := "○"
			style := lipgloss.NewStyle().Foreground(ui.ColorDim)
			if i == m.spawnBackendIdx {
				indicator = "●"
				style = lipgloss.NewStyle().Foreground(ui.ColorAccent).Bold(true)
			}
			prefix := "  "
			if m.spawnFocus == focusBackend && i == m.spawnBackendIdx {
				prefix = "> "
			}
			backendLines = append(backendLines, style.Render(prefix+indicator+" "+b.Name()))
		}
	} else if len(m.spawnBackends) == 1 {
		backendLines = append(backendLines, "Backend:  "+m.spawnBackends[0].Name())
	}

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

	help := ui.HelpStyle.Render("[Enter] select/spawn  [↑/↓] navigate  [Esc] cancel")

	var parts []string
	parts = append(parts, title, "")
	if len(backendLines) > 0 {
		parts = append(parts, backendLines...)
		parts = append(parts, "")
	}
	parts = append(parts, fields)
	if suggestions != "" {
		parts = append(parts, suggestions)
	}

	// Auto-approve toggle (only shown if backend supports it)
	if m.spawnSelectedBackendSupportsAutoApprove() {
		checkmark := "\u2610" // ☐
		if m.spawnAutoApprove {
			checkmark = "\u2611" // ☑
		}
		approveStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
		approvePrefix := "  "
		if m.spawnFocus == focusApprove {
			approveStyle = lipgloss.NewStyle().Foreground(ui.ColorAccent).Bold(true)
			approvePrefix = "> "
		}
		approveLine := approveStyle.Render(approvePrefix + checkmark + " Auto-approve (skip permissions)")
		parts = append(parts, "", approveLine)
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
		warning = "This is an external session. Killing it will terminate the agent instance."
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

func (m Model) viewConfirmAutoApprove() string {
	name := "(none)"
	newState := "ON"
	if m.selected < len(m.agents) {
		name = m.agents[m.selected].Name
		if m.agents[m.selected].AutoApprove {
			newState = "OFF"
		}
	}

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FBBF24")).
		Padding(1, 2).
		Width(55)

	content := lipgloss.JoinVertical(lipgloss.Left,
		ui.AgentName.Render(fmt.Sprintf("Toggle auto-approve %s: %s?", newState, name)),
		"",
		"Agent will be killed and respawned to apply the change.",
		"The conversation will be resumed automatically.",
		"",
		ui.HelpStyle.Render("[Y] confirm  [N/Esc] cancel"),
	)

	rendered := dialog.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, rendered)
}

// --- Batch operations dialog ---

type batchOption struct {
	key   string // "1", "2", "3"
	label string
	count int
	action func(m *Model)
}

func (m *Model) openBatchDialog() {
	var opts []batchOption
	keyNum := 1

	// Count agents by status
	var doneCount, waitingCount, totalCount int
	for _, a := range m.agents {
		totalCount++
		switch a.Status {
		case StatusDone:
			doneCount++
		case StatusWaiting:
			waitingCount++
		}
	}

	if doneCount > 0 {
		opts = append(opts, batchOption{
			key:   fmt.Sprintf("%d", keyNum),
			label: fmt.Sprintf("Kill all DONE agents (%d)", doneCount),
			count: doneCount,
			action: func(m *Model) {
				n := m.store.ClearDone()
				m.agents = m.store.List()
				m.setStatus(fmt.Sprintf("Killed %d DONE agents", n))
				if m.selected >= len(m.agents) && len(m.agents) > 0 {
					m.selected = len(m.agents) - 1
				}
			},
		})
		keyNum++
	}

	if totalCount > 0 {
		opts = append(opts, batchOption{
			key:   fmt.Sprintf("%d", keyNum),
			label: fmt.Sprintf("Kill all agents (%d)", totalCount),
			count: totalCount,
			action: func(m *Model) {
				for _, a := range m.store.List() {
					sess := m.manager.GetSession(a)
					if sess != nil {
						_ = m.manager.Kill(a.ID)
					} else if a.SessionName != "" {
						_ = KillBySession(a.SessionName)
					}
					a.Backend().CleanHookStatus(a.ID)
					m.store.Remove(a.ID)
				}
				m.agents = m.store.List()
				m.selected = 0
				m.setStatus(fmt.Sprintf("Killed all %d agents", totalCount))
			},
		})
		keyNum++
	}

	if waitingCount > 0 {
		opts = append(opts, batchOption{
			key:   fmt.Sprintf("%d", keyNum),
			label: fmt.Sprintf("Send \"y\" to all WAITING agents (%d)", waitingCount),
			count: waitingCount,
			action: func(m *Model) {
				sent := 0
				for _, a := range m.agents {
					if a.Status == StatusWaiting {
						_ = m.manager.SendKeys(a, "y")
						sent++
					}
				}
				m.setStatus(fmt.Sprintf("Sent \"y\" to %d WAITING agents", sent))
			},
		})
	}

	if len(opts) == 0 {
		m.setStatus("No batch operations available")
		return
	}
	m.batchOptions = opts
	m.view = viewBatch
}

func (m *Model) handleBatchKey(key string) (tea.Model, tea.Cmd) {
	returnView := viewBoard
	if m.columns == 1 {
		returnView = viewCarousel
	}

	if key == "esc" {
		m.view = returnView
		return m, nil
	}

	for _, opt := range m.batchOptions {
		if key == opt.key {
			opt.action(m)
			m.view = returnView
			return m, nil
		}
	}

	m.view = returnView
	return m, nil
}

func (m Model) viewBatchDialog() string {
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorAccent).
		Padding(1, 2).
		Width(50)

	lines := []string{
		ui.AgentName.Render("Batch Operations"),
		"",
	}
	for _, opt := range m.batchOptions {
		lines = append(lines, fmt.Sprintf("  [%s] %s", opt.key, opt.label))
	}
	lines = append(lines, "", ui.HelpStyle.Render("[Esc] Cancel"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	rendered := dialog.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, rendered)
}

// restartStuckAgent restarts a STUCK agent by killing and respawning it.
func (m *Model) restartStuckAgent() (tea.Model, tea.Cmd) {
	if len(m.agents) == 0 || m.selected >= len(m.agents) {
		return m, nil
	}
	agent := m.agents[m.selected]
	if agent.Status != StatusError {
		m.setStatus("Only STUCK agents can be restarted (use R)")
		return m, nil
	}

	// Kill and respawn
	_ = m.manager.Kill(agent.ID)
	if agent.SessionName != "" {
		_ = KillBySession(agent.SessionName)
	}
	agent.Backend().CleanHookStatus(agent.ID)

	if err := m.manager.RespawnAgent(agent); err != nil {
		m.setStatus(fmt.Sprintf("Restart failed: %v", err))
		return m, nil
	}
	m.store.UpdateSessionName(agent.ID, agent.SessionName)
	m.store.Update(agent.ID, StatusRunning)
	m.agents = m.store.List()
	m.setStatus(fmt.Sprintf("Restarted: %s", agent.Name))
	return m, nil
}

// --- Workspace dialog ---

func (m *Model) openWorkspaceDialog() {
	names, _ := ListWorkspaces()
	m.wsNames = names
	m.wsSelected = 0
	m.wsSaveMode = false
	m.view = viewWorkspace
}

func (m *Model) handleWorkspaceKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "esc" {
		if m.wsSaveMode {
			m.wsSaveMode = false
			m.wsNameInput.Blur()
			return m, nil
		}
		m.view = viewBoard
		if m.columns == 1 {
			m.view = viewCarousel
		}
		return m, nil
	}

	if m.wsSaveMode {
		return m.handleWorkspaceSaveKey(msg)
	}
	return m.handleWorkspaceListKey(msg)
}

func (m *Model) handleWorkspaceListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "j", "down":
		if len(m.wsNames) > 0 && m.wsSelected < len(m.wsNames)-1 {
			m.wsSelected++
		}
	case "k", "up":
		if m.wsSelected > 0 {
			m.wsSelected--
		}
	case "s":
		m.wsSaveMode = true
		m.wsNameInput.SetValue("")
		m.wsNameInput.Focus()
		return m, nil
	case "enter":
		if len(m.wsNames) > 0 && m.wsSelected < len(m.wsNames) {
			return m.doWorkspaceLoad(m.wsNames[m.wsSelected])
		}
	case "a":
		if len(m.wsNames) > 0 && m.wsSelected < len(m.wsNames) {
			return m.doWorkspaceAdd(m.wsNames[m.wsSelected])
		}
	case "d":
		if len(m.wsNames) > 0 && m.wsSelected < len(m.wsNames) {
			m.doWorkspaceDelete(m.wsNames[m.wsSelected])
		}
	}
	return m, nil
}

func (m *Model) handleWorkspaceSaveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "enter":
		name := strings.TrimSpace(m.wsNameInput.Value())
		if name != "" {
			return m.doWorkspaceSave(name)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.wsNameInput, cmd = m.wsNameInput.Update(msg)
	return m, cmd
}

func (m *Model) doWorkspaceSave(name string) (tea.Model, tea.Cmd) {
	agents := m.store.List()
	if err := SaveWorkspace(name, agents); err != nil {
		m.setStatus(fmt.Sprintf("Save error: %v", err))
	} else {
		m.setStatus(fmt.Sprintf("Saved workspace %q (%d agents)", name, len(agents)))
		m.activeWorkspace = name
	}
	m.view = viewBoard
	if m.columns == 1 {
		m.view = viewCarousel
	}
	return m, nil
}

func (m *Model) doWorkspaceLoad(name string) (tea.Model, tea.Cmd) {
	wf, err := LoadWorkspace(name)
	if err != nil {
		m.setStatus(fmt.Sprintf("Load error: %v", err))
		return m, nil
	}

	// Kill all current agents
	for _, a := range m.store.List() {
		sess := m.manager.GetSession(a)
		if sess != nil {
			_ = m.manager.Kill(a.ID)
		} else if a.SessionName != "" {
			_ = KillBySession(a.SessionName)
		}
		a.Backend().CleanHookStatus(a.ID)
		m.store.Remove(a.ID)
	}

	count := spawnWorkspaceAgents(wf, m.store, m.manager)
	m.agents = m.store.List()
	m.selected = 0
	m.activeWorkspace = name
	m.setStatus(fmt.Sprintf("Loaded workspace %q: %d agent(s)", name, count))
	m.view = viewBoard
	if m.columns == 1 {
		m.view = viewCarousel
	}
	return m, nil
}

func (m *Model) doWorkspaceAdd(name string) (tea.Model, tea.Cmd) {
	wf, err := LoadWorkspace(name)
	if err != nil {
		m.setStatus(fmt.Sprintf("Load error: %v", err))
		return m, nil
	}

	count := spawnWorkspaceAgents(wf, m.store, m.manager)
	m.agents = m.store.List()
	m.activeWorkspace = name
	m.setStatus(fmt.Sprintf("Added workspace %q: %d agent(s)", name, count))
	m.view = viewBoard
	if m.columns == 1 {
		m.view = viewCarousel
	}
	return m, nil
}

func (m *Model) doWorkspaceDelete(name string) {
	if err := DeleteWorkspace(name); err != nil {
		m.setStatus(fmt.Sprintf("Delete error: %v", err))
		return
	}
	m.setStatus(fmt.Sprintf("Deleted workspace %q", name))
	// Refresh list
	m.wsNames, _ = ListWorkspaces()
	if m.wsSelected >= len(m.wsNames) && len(m.wsNames) > 0 {
		m.wsSelected = len(m.wsNames) - 1
	}
	if len(m.wsNames) == 0 {
		m.wsSelected = 0
	}
}

func (m Model) viewWorkspace() string {
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorAccent).
		Padding(1, 2).
		Width(60)

	title := ui.AgentName.Render("Workspaces")

	var content string
	if m.wsSaveMode {
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, "",
			"Save current agents as:", "",
			m.wsNameInput.View(), "",
			ui.HelpStyle.Render("[Enter] save  [Esc] cancel"),
		)
	} else {
		var listLines []string
		if len(m.wsNames) == 0 {
			listLines = append(listLines, lipgloss.NewStyle().
				Foreground(ui.ColorDim).Render("  No saved workspaces"))
		} else {
			for i, name := range m.wsNames {
				agentCount := 0
				if wf, err := LoadWorkspace(name); err == nil {
					agentCount = len(wf.Agents)
				}
				label := fmt.Sprintf("%s (%d agents)", name, agentCount)
				if i == m.wsSelected {
					listLines = append(listLines, lipgloss.NewStyle().
						Foreground(ui.ColorAccent).Bold(true).
						Render("> "+label))
				} else {
					listLines = append(listLines, lipgloss.NewStyle().
						Foreground(ui.ColorDim).
						Render("  "+label))
				}
			}
		}
		list := strings.Join(listLines, "\n")

		content = lipgloss.JoinVertical(lipgloss.Left,
			title, "",
			list, "",
			ui.HelpStyle.Render("[s] save current  [Enter] load  [a] add  [d] delete  [Esc] close"),
		)
	}

	rendered := dialog.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, rendered)
}

// clipHeight trims rendered content to maxLines without any scroll offset math.
// Used as a safety net after card-level slicing in the renderers.
func clipHeight(content string, maxLines int) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	return strings.Join(lines[:maxLines], "\n")
}

// buildCardData fetches pane info for all agents (expensive — calls tmux per agent).
// Results are cached in m.cachedCards; call only on tick or state changes.
func (m Model) buildCardData() []ui.CardData {
	now := time.Now()
	cards := make([]ui.CardData, len(m.agents))
	for i, a := range m.agents {
		info := m.manager.GetPaneInfo(a, 13)
		cards[i] = ui.CardData{
			Name:        a.Name,
			Dir:         a.Dir,
			Title:       info.Title,
			Status:      string(a.Status),
			Mode:        info.Mode,
			Uptime:      now.Sub(a.CreatedAt),
			Since:       now.Sub(a.StatusSince),
			Preview:     info.Preview,
			Selected:    i == m.selected,
			Discovered:  a.Discovered,
			AutoApprove: a.AutoApprove,
		}
	}
	return cards
}

// getCards returns cached card data with the Selected field updated for the
// current selection. This avoids expensive tmux calls on every render.
func (m Model) getCards() []ui.CardData {
	cards := m.cachedCards
	if len(cards) != len(m.agents) {
		// Cache stale or empty — rebuild
		cards = m.buildCardData()
	} else {
		// Update dynamic fields without tmux calls
		now := time.Now()
		for i, a := range m.agents {
			cards[i].Selected = i == m.selected
			cards[i].Uptime = now.Sub(a.CreatedAt)
			cards[i].Since = now.Sub(a.StatusSince)
		}
	}
	return cards
}

// discoverCmd runs discovery asynchronously and returns a discoverMsg.
func discoverCmd() tea.Cmd {
	return func() tea.Msg {
		var found []DiscoveredAgent
		for _, b := range AllBackends() {
			found = append(found, b.Discover()...)
		}
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
