package main

import (
	"strings"
	"testing"
)

// --- Registry tests ---

func TestRegistryDefaultBackend(t *testing.T) {
	b := DefaultBackend()
	if b == nil {
		t.Fatal("DefaultBackend() returned nil")
	}
	if b.ID() != "claude" {
		t.Errorf("DefaultBackend().ID() = %q, want %q", b.ID(), "claude")
	}
	if b.Name() != "Claude Code" {
		t.Errorf("DefaultBackend().Name() = %q, want %q", b.Name(), "Claude Code")
	}
}

func TestRegistryGetBackend(t *testing.T) {
	b := GetBackend("claude")
	if b == nil {
		t.Fatal("GetBackend(\"claude\") returned nil")
	}
	if b.ID() != "claude" {
		t.Errorf("GetBackend(\"claude\").ID() = %q, want %q", b.ID(), "claude")
	}
}

func TestRegistryGetBackendNotFound(t *testing.T) {
	b := GetBackend("nonexistent")
	if b != nil {
		t.Errorf("GetBackend(\"nonexistent\") should return nil, got %v", b)
	}
}

func TestRegistryAllBackends(t *testing.T) {
	all := AllBackends()
	if len(all) == 0 {
		t.Fatal("AllBackends() returned empty slice")
	}
	found := false
	for _, b := range all {
		if b.ID() == "claude" {
			found = true
		}
	}
	if !found {
		t.Error("AllBackends() does not contain claude backend")
	}
}

// --- Claude backend: DetectStatus ---

func TestClaudeDetectStatus(t *testing.T) {
	cb := &ClaudeBackend{}
	tests := []struct {
		name    string
		content string
		want    AgentStatus
	}{
		{
			"running - esc to interrupt",
			"Processing files...\nesc to interrupt",
			StatusRunning,
		},
		{
			"running - spinner with ellipsis",
			"some output\n\u2722 Thinking...",
			StatusRunning,
		},
		{
			"running - Running...",
			"Tool output\nRunning...\n",
			StatusRunning,
		},
		{
			"waiting - allow once",
			"Some output\nAllow once\nAllow always",
			StatusWaiting,
		},
		{
			"waiting - yes/no",
			"Do something?\nyes/no",
			StatusWaiting,
		},
		{
			"idle - prompt",
			"Done with task\n❯ ",
			StatusIdle,
		},
		{
			"idle - ? for shortcuts",
			"output\n? for shortcuts",
			StatusIdle,
		},
		{
			"done - goodbye",
			"All done\nGoodbye!",
			StatusDone,
		},
		{
			"done - session ended",
			"work complete\nsession ended",
			StatusDone,
		},
		{
			"empty content defaults to running",
			"",
			StatusRunning,
		},
		{
			"whitespace only defaults to running",
			"   \n   \n   ",
			StatusRunning,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cb.DetectStatus(tt.content); got != tt.want {
				t.Errorf("DetectStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Claude backend: DetectMode ---

func TestClaudeDetectMode(t *testing.T) {
	cb := &ClaudeBackend{}
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"edits mode", "some output\naccept edits\nprompt", "EDITS"},
		{"plan mode", "output\nPlan Mode active\nprompt", "PLAN"},
		{"exited plan mode skipped", "output\nExited Plan Mode\nprompt", ""},
		{"normal mode", "just regular output\nno mode indicator", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cb.DetectMode(tt.content); got != tt.want {
				t.Errorf("DetectMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Claude backend: LooksLikeMe ---

func TestClaudeLooksLikeMe(t *testing.T) {
	cb := &ClaudeBackend{}
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"prompt symbol", "some output\n❯ type here", true},
		{"shortcuts hint", "? for shortcuts\nprompt", true},
		{"esc to interrupt", "Processing\nesc to interrupt", true},
		{"claude code text", "Welcome to Claude Code\n>", true},
		{"anthropic mention", "Powered by Anthropic\n>", true},
		{"allow once", "Allow once\nAllow always", true},
		{"ansi-wrapped prompt", "output\n\x1b[38;5;208m❯\x1b[0m type here", true},
		{"ansi-wrapped shortcuts", "\x1b[2m? for shortcuts\x1b[0m", true},
		{"ansi-wrapped esc hint", "\x1b[1mProcessing\x1b[0m\n\x1b[90mesc to interrupt\x1b[0m", true},
		{"unrelated content", "hello world\nfoo bar", false},
		{"empty", "", false},
		{"bash session", "user@host:~$ ls\nfile1 file2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cb.LooksLikeMe(tt.content); got != tt.want {
				t.Errorf("LooksLikeMe() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Claude backend: StripChrome ---

func TestClaudeStripChromeLines(t *testing.T) {
	cb := &ClaudeBackend{}
	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{
			"removes separator and prompt",
			[]string{"output", strings.Repeat("─", 20), "❯ prompt", "status"},
			1,
		},
		{
			"no prompt unchanged",
			[]string{"line 1", "line 2", "line 3"},
			3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cb.StripChrome(tt.lines, false)
			if len(got) != tt.want {
				t.Errorf("StripChrome(waiting=false) returned %d lines, want %d (got: %v)", len(got), tt.want, got)
			}
		})
	}
}

func TestClaudeStripWaitingChrome(t *testing.T) {
	cb := &ClaudeBackend{}
	tests := []struct {
		name   string
		lines  []string
		hasSep bool
	}{
		{
			"removes separators and last non-blank",
			[]string{"output", strings.Repeat("─", 20), "Allow once", "status"},
			true,
		},
		{
			"no separators just drops last",
			[]string{"line 1", "line 2", "last line"},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cb.StripChrome(tt.lines, true)
			for _, l := range got {
				stripped := strings.TrimSpace(stripAnsiStr(l))
				if tt.hasSep && isSeparatorLine(stripped) {
					t.Error("StripChrome(waiting=true) did not remove separator lines")
				}
			}
		})
	}
}

// --- Claude backend: SpawnCommand ---

func TestClaudeSpawnCommand(t *testing.T) {
	cb := &ClaudeBackend{}

	cmd, strip := cb.SpawnCommand(nil)
	if cmd != "claude" {
		t.Errorf("SpawnCommand(nil) cmd = %q, want %q", cmd, "claude")
	}
	if len(strip) != 1 || strip[0] != "CLAUDECODE" {
		t.Errorf("SpawnCommand(nil) strip = %v, want [CLAUDECODE]", strip)
	}

	cmd, strip = cb.SpawnCommand([]string{"--verbose"})
	if cmd != "claude --verbose" {
		t.Errorf("SpawnCommand([--verbose]) cmd = %q, want %q", cmd, "claude --verbose")
	}
	if len(strip) != 1 || strip[0] != "CLAUDECODE" {
		t.Errorf("SpawnCommand([--verbose]) strip = %v, want [CLAUDECODE]", strip)
	}
}

// --- Claude backend: hasDingbat (shared helper) ---

func TestHasDingbat(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"dingbat present", "processing \u2722 loading...", true},
		{"another dingbat", "\u2736 working", true},
		{"no dingbat", "just plain text", false},
		{"empty string", "", false},
		{"other unicode", "hello 日本語", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDingbat(tt.input); got != tt.want {
				t.Errorf("hasDingbat(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
