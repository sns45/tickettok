package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func newTestWebServer(t *testing.T) (*WebServer, *Store) {
	t.Helper()
	store := newTestStore(t)
	manager := NewAgentManager()
	ws := NewWebServer(store, manager, 0)
	return ws, store
}

func TestWebServerStartStop(t *testing.T) {
	ws, _ := newTestWebServer(t)

	if err := ws.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ws.Stop()

	if ws.Addr() == "" {
		t.Error("Addr() is empty after Start()")
	}
	if ws.Token() == "" {
		t.Error("Token() is empty")
	}
	if len(ws.Token()) != 32 {
		t.Errorf("Token() length = %d, want 32 hex chars", len(ws.Token()))
	}

	ws.Stop()
}

func TestWebServerIndexPage(t *testing.T) {
	ws, _ := newTestWebServer(t)

	srv := httptest.NewServer(ws.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?token=" + ws.Token())
	if err != nil {
		t.Fatalf("GET / error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("GET / status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestWebServerTokenRequired(t *testing.T) {
	ws, _ := newTestWebServer(t)

	srv := httptest.NewServer(ws.handler())
	defer srv.Close()

	// No token
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("GET / without token status = %d, want 401", resp.StatusCode)
	}

	// Wrong token
	resp, err = http.Get(srv.URL + "/?token=wrong")
	if err != nil {
		t.Fatalf("GET / error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("GET / with wrong token status = %d, want 401", resp.StatusCode)
	}

	// API endpoint also requires token
	resp, err = http.Get(srv.URL + "/api/agents")
	if err != nil {
		t.Fatalf("GET /api/agents error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("GET /api/agents without token status = %d, want 401", resp.StatusCode)
	}
}

func TestWebServerAPIAgents(t *testing.T) {
	ws, store := newTestWebServer(t)

	store.Add("test-agent", "/tmp/project")

	srv := httptest.NewServer(ws.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/agents?token=" + ws.Token())
	if err != nil {
		t.Fatalf("GET /api/agents error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var agents []*Agent
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	if agents[0].Name != "test-agent" {
		t.Errorf("agent name = %q, want %q", agents[0].Name, "test-agent")
	}
}

func TestWebSocketStateSync(t *testing.T) {
	ws, store := newTestWebServer(t)
	store.Add("ws-agent", "/tmp/ws")

	srv := httptest.NewServer(ws.handler())
	defer srv.Close()

	// Convert http URL to ws URL
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?token=" + ws.Token()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial error: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Read initial state message
	var msg wsMessage
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		t.Fatalf("ws read error: %v", err)
	}

	if msg.Type != "state" {
		t.Errorf("first message type = %q, want %q", msg.Type, "state")
	}
	if len(msg.Agents) != 1 {
		t.Fatalf("state has %d agents, want 1", len(msg.Agents))
	}
	if msg.Agents[0].Name != "ws-agent" {
		t.Errorf("agent name = %q, want %q", msg.Agents[0].Name, "ws-agent")
	}
}

func TestWebActionKill(t *testing.T) {
	ws, store := newTestWebServer(t)

	agent := store.Add("kill-me", "/tmp/kill")

	// Verify agent exists
	if got := store.Get(agent.ID); got == nil {
		t.Fatal("agent should exist before kill")
	}

	// Call handleKill directly
	ws.handleKill(&wsMessage{AgentID: agent.ID})

	// Verify agent was removed
	if got := store.Get(agent.ID); got != nil {
		t.Error("agent should be nil after kill")
	}
}

func TestWebActionSend(t *testing.T) {
	ws, store := newTestWebServer(t)

	store.Add("send-target", "/tmp/send")

	// handleSend should not panic even with no tmux session
	ws.handleSend(&wsMessage{
		AgentID: "1",
		Message: "hello world",
	})
}
