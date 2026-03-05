package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

//go:embed web/index.html
var webFS embed.FS

// wsMessage is the JSON envelope for all WebSocket communication.
type wsMessage struct {
	Type    string   `json:"type"`              // "state", "zoom"
	Agents  []*Agent `json:"agents,omitempty"`
	AgentID string   `json:"agentId,omitempty"`
	Content string   `json:"content,omitempty"`

	// Client action fields
	Action      string `json:"action,omitempty"`  // "zoom", "unzoom", "kill", "send", "sendkeys", "spawn"
	Backend     string `json:"backend,omitempty"`
	Dir         string `json:"dir,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	AutoApprove bool   `json:"autoApprove,omitempty"`
	Message     string `json:"message,omitempty"`
	Keys        string `json:"keys,omitempty"`
}

// wsClient represents a single connected WebSocket client.
type wsClient struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	zoomID string // non-empty when zoomed into an agent
}

// WebServer serves the remote web UI and WebSocket API.
type WebServer struct {
	store   *Store
	manager *AgentManager
	token   string
	port    int

	mu      sync.Mutex
	clients []*wsClient

	listener net.Listener
	server   *http.Server
	done     chan struct{}
	stopOnce sync.Once
}

// NewWebServer creates a WebServer with a random auth token.
func NewWebServer(store *Store, manager *AgentManager, port int) *WebServer {
	tokenBytes := make([]byte, 16)
	_, _ = rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	return &WebServer{
		store:   store,
		manager: manager,
		token:   token,
		port:    port,
		done:    make(chan struct{}),
	}
}

// Token returns the auth token required for all endpoints.
func (ws *WebServer) Token() string {
	return ws.token
}

// Addr returns the listener address (useful when port is 0).
func (ws *WebServer) Addr() string {
	if ws.listener != nil {
		return ws.listener.Addr().String()
	}
	return ""
}

// handler builds the HTTP mux. Exported as a method so tests can use httptest.
func (ws *WebServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleIndex)
	mux.HandleFunc("/api/agents", ws.handleAPIAgents)
	mux.HandleFunc("/ws", ws.handleWS)
	return mux
}

// Start begins listening on the configured port.
func (ws *WebServer) Start() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", ws.port))
	if err != nil {
		return fmt.Errorf("web listen: %w", err)
	}
	ws.listener = ln

	ws.server = &http.Server{
		Handler: ws.handler(),
	}

	// Start the zoom capture loop
	go ws.zoomLoop()

	go func() {
		if err := ws.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("web server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the web server. Safe to call multiple times.
func (ws *WebServer) Stop() {
	ws.stopOnce.Do(func() {
		close(ws.done)

		if ws.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = ws.server.Shutdown(ctx)
		}

		// Close all WebSocket connections
		ws.mu.Lock()
		for _, c := range ws.clients {
			c.conn.Close(websocket.StatusGoingAway, "server shutting down")
		}
		ws.clients = nil
		ws.mu.Unlock()
	})
}

// checkToken validates the token query parameter. Returns false and writes 401 if invalid.
func (ws *WebServer) checkToken(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("token") != ws.token {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// handleIndex serves the embedded index.html.
func (ws *WebServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !ws.checkToken(w, r) {
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// handleAPIAgents returns the agent list as JSON.
func (ws *WebServer) handleAPIAgents(w http.ResponseWriter, r *http.Request) {
	if !ws.checkToken(w, r) {
		return
	}
	agents := ws.store.List()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agents)
}

// handleWS upgrades to WebSocket and manages the client lifecycle.
func (ws *WebServer) handleWS(w http.ResponseWriter, r *http.Request) {
	if !ws.checkToken(w, r) {
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("ws accept: %v", err)
		return
	}

	client := &wsClient{conn: conn}
	ws.addClient(client)
	defer ws.removeClient(client)

	// Send initial state
	ws.sendState(client)

	// Read loop for client actions
	ctx := r.Context()
	for {
		var msg wsMessage
		err := wsjson.Read(ctx, conn, &msg)
		if err != nil {
			break
		}
		ws.handleAction(client, &msg)
	}
}

// addClient registers a new WebSocket client.
func (ws *WebServer) addClient(c *wsClient) {
	ws.mu.Lock()
	ws.clients = append(ws.clients, c)
	ws.mu.Unlock()
}

// removeClient deregisters a WebSocket client.
func (ws *WebServer) removeClient(c *wsClient) {
	ws.mu.Lock()
	for i, cl := range ws.clients {
		if cl == c {
			ws.clients = append(ws.clients[:i], ws.clients[i+1:]...)
			break
		}
	}
	ws.mu.Unlock()
	c.conn.Close(websocket.StatusNormalClosure, "")
}

// sendState sends the current agent list to a single client.
func (ws *WebServer) sendState(c *wsClient) {
	msg := wsMessage{
		Type:   "state",
		Agents: ws.store.List(),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = wsjson.Write(ctx, c.conn, msg)
}

// BroadcastState sends the current agent list to all connected clients.
// Intended to be called from the TUI tick loop.
func (ws *WebServer) BroadcastState() {
	msg := wsMessage{
		Type:   "state",
		Agents: ws.store.List(),
	}
	ws.mu.Lock()
	snapshot := make([]*wsClient, len(ws.clients))
	copy(snapshot, ws.clients)
	ws.mu.Unlock()

	for _, c := range snapshot {
		c.mu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = wsjson.Write(ctx, c.conn, msg)
		cancel()
		c.mu.Unlock()
	}
}

// BroadcastZoom sends zoom content to all clients currently zoomed into the given agent.
// Intended to be called from the TUI zoom tick loop.
func (ws *WebServer) BroadcastZoom(agentID, content string) {
	msg := wsMessage{
		Type:    "zoom",
		AgentID: agentID,
		Content: content,
	}
	ws.mu.Lock()
	snapshot := make([]*wsClient, len(ws.clients))
	copy(snapshot, ws.clients)
	ws.mu.Unlock()

	for _, c := range snapshot {
		c.mu.Lock()
		if c.zoomID == agentID {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = wsjson.Write(ctx, c.conn, msg)
			cancel()
		}
		c.mu.Unlock()
	}
}

// zoomLoop periodically captures pane content for remote clients that are zoomed in.
func (ws *WebServer) zoomLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ws.done:
			return
		case <-ticker.C:
			ws.mu.Lock()
			// Collect unique zoom IDs
			zoomed := make(map[string]bool)
			for _, c := range ws.clients {
				c.mu.Lock()
				if c.zoomID != "" {
					zoomed[c.zoomID] = true
				}
				c.mu.Unlock()
			}
			ws.mu.Unlock()

			// Capture and broadcast for each zoomed agent
			for agentID := range zoomed {
				agent := ws.store.Get(agentID)
				if agent == nil {
					continue
				}
				sessName := agent.SessionName
				if sessName == "" {
					sessName = SessionName(agentID)
				}
				content, err := CapturePane(sessName)
				if err != nil {
					continue
				}
				ws.BroadcastZoom(agentID, content)
			}
		}
	}
}

// handleAction dispatches a client action message.
func (ws *WebServer) handleAction(client *wsClient, msg *wsMessage) {
	switch msg.Action {
	case "zoom":
		ws.handleZoom(client, msg)
	case "unzoom":
		ws.handleUnzoom(client)
	case "kill":
		ws.handleKill(msg)
	case "send":
		ws.handleSend(msg)
	case "sendkeys":
		ws.handleSendKeys(msg)
	case "spawn":
		ws.handleSpawn(msg)
	}
}

// handleZoom sets the client's zoom target and sends immediate content.
func (ws *WebServer) handleZoom(client *wsClient, msg *wsMessage) {
	client.mu.Lock()
	client.zoomID = msg.AgentID
	client.mu.Unlock()

	// Send immediate zoom content
	agent := ws.store.Get(msg.AgentID)
	if agent == nil {
		return
	}
	sessName := agent.SessionName
	if sessName == "" {
		sessName = SessionName(msg.AgentID)
	}
	content, err := CapturePane(sessName)
	if err != nil {
		return
	}

	zoomMsg := wsMessage{
		Type:    "zoom",
		AgentID: msg.AgentID,
		Content: content,
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = wsjson.Write(ctx, client.conn, zoomMsg)
}

// handleUnzoom clears the client's zoom target and sends state.
func (ws *WebServer) handleUnzoom(client *wsClient) {
	client.mu.Lock()
	client.zoomID = ""
	client.mu.Unlock()
	ws.sendState(client)
}

// handleKill terminates an agent.
func (ws *WebServer) handleKill(msg *wsMessage) {
	agent := ws.store.Get(msg.AgentID)
	if agent == nil {
		return
	}
	_ = ws.manager.Kill(agent.ID)
	if agent.SessionName != "" {
		_ = KillBySession(agent.SessionName)
	}
	ws.store.Remove(agent.ID)
}

// handleSend sends a message (with Enter) to an agent.
func (ws *WebServer) handleSend(msg *wsMessage) {
	agent := ws.store.Get(msg.AgentID)
	if agent == nil {
		return
	}
	sessName := agent.SessionName
	if sessName == "" {
		sessName = SessionName(agent.ID)
	}
	// Send text literally (no key name interpretation), then press Enter
	if err := exec.Command("tmux", "send-keys", "-l", "-t", sessName, msg.Message).Run(); err != nil {
		return
	}
	exec.Command("tmux", "send-keys", "-t", sessName, "Enter").Run()
}

// handleSendKeys sends raw keystrokes to an agent.
func (ws *WebServer) handleSendKeys(msg *wsMessage) {
	agent := ws.store.Get(msg.AgentID)
	if agent == nil {
		return
	}
	sessName := agent.SessionName
	if sessName == "" {
		sessName = SessionName(agent.ID)
	}
	// Use -l for literal text, handle \n as Enter
	parts := strings.Split(msg.Keys, "\n")
	for i, part := range parts {
		if part != "" {
			exec.Command("tmux", "send-keys", "-l", "-t", sessName, part).Run()
		}
		if i < len(parts)-1 {
			exec.Command("tmux", "send-keys", "-t", sessName, "Enter").Run()
		}
	}
}

// handleSpawn creates and starts a new agent.
func (ws *WebServer) handleSpawn(msg *wsMessage) {
	dir := msg.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}

	name := deriveNameFromDir(dir)
	agent := ws.store.Add(name, dir)

	if msg.Backend != "" {
		if GetBackend(msg.Backend) != nil {
			agent.BackendID = msg.Backend
		}
	}
	agent.AutoApprove = msg.AutoApprove

	var extraArgs []string
	if agent.AutoApprove {
		extraArgs = append(extraArgs, agent.Backend().AutoApproveArgs()...)
	}

	if err := ws.manager.SpawnAgent(agent, extraArgs); err != nil {
		// Spawn failed; remove from store
		ws.store.Remove(agent.ID)
		return
	}

	ws.store.UpdateSessionName(agent.ID, agent.SessionName)
	ws.store.Save()

	if msg.Prompt != "" {
		go SendPromptAfterDelay(agent.SessionName, msg.Prompt)
	}
}
