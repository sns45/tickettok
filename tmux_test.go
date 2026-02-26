package main

import (
	"strings"
	"testing"
)

func TestSessionName(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"1", "tickettok_1"},
		{"abc", "tickettok_abc"},
		{"", "tickettok_"},
	}
	for _, tt := range tests {
		if got := SessionName(tt.id); got != tt.want {
			t.Errorf("SessionName(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestStripAnsiStr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips color codes", "\x1b[32mhello\x1b[0m", "hello"},
		{"strips bold", "\x1b[1mbold\x1b[0m", "bold"},
		{"plain text unchanged", "hello world", "hello world"},
		{"empty string", "", ""},
		{"multiple codes", "\x1b[31;1mred bold\x1b[0m text", "red bold text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripAnsiStr(tt.input); got != tt.want {
				t.Errorf("stripAnsiStr(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

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

func TestIsSeparatorLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"box drawing dashes", strings.Repeat("─", 20), true},
		{"ascii dashes", strings.Repeat("-", 20), true},
		{"mixed box/ascii is valid", "──────-----", true},
		{"9 runes but enough bytes", strings.Repeat("─", 9), true},
		{"short byte length", "---", false},
		{"exactly 10", strings.Repeat("─", 10), true},
		{"empty", "", false},
		{"has other chars", "────hello────", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSeparatorLine(tt.input); got != tt.want {
				t.Errorf("isSeparatorLine(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDetectStatusFromContent(t *testing.T) {
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
			if got := DetectStatusFromContent(tt.content); got != tt.want {
				t.Errorf("DetectStatusFromContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectModeFromContent(t *testing.T) {
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
			if got := DetectModeFromContent(tt.content); got != tt.want {
				t.Errorf("DetectModeFromContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPreviewFromContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		n       int
		waiting bool
		minLen  int // minimum expected lines
	}{
		{
			"basic content",
			"line one\nline two\nline three\nline four\nline five",
			3, false, 3,
		},
		{
			"strips chrome with prompt",
			"actual output\nmore output\n" + strings.Repeat("─", 20) + "\n❯ type here\nstatus line",
			5, false, 2,
		},
		{
			"respects n limit",
			"line alpha\nline beta\nline gamma\nline delta\nline epsilon",
			2, false, 2,
		},
		{
			"waiting mode strips separators",
			"output\n" + strings.Repeat("─", 20) + "\nAllow once\nlast line",
			5, true, 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreviewFromContent(tt.content, tt.n, tt.waiting)
			if len(got) < tt.minLen {
				t.Errorf("PreviewFromContent() returned %d lines, want at least %d", len(got), tt.minLen)
			}
			if len(got) > tt.n {
				t.Errorf("PreviewFromContent() returned %d lines, want at most %d", len(got), tt.n)
			}
		})
	}
}

func TestStripChromeLines(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int // expected count of remaining lines
	}{
		{
			"removes separator and prompt",
			[]string{"output", strings.Repeat("─", 20), "❯ prompt", "status"},
			1, // only "output" remains
		},
		{
			"no prompt unchanged",
			[]string{"line 1", "line 2", "line 3"},
			3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripChromeLines(tt.lines)
			if len(got) != tt.want {
				t.Errorf("stripChromeLines() returned %d lines, want %d (got: %v)", len(got), tt.want, got)
			}
		})
	}
}

func TestStripWaitingChrome(t *testing.T) {
	tests := []struct {
		name string
		lines []string
		hasSep bool // should separators be removed
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
			got := stripWaitingChrome(tt.lines)
			// Verify separators are removed
			for _, l := range got {
				stripped := strings.TrimSpace(stripAnsiStr(l))
				if tt.hasSep && isSeparatorLine(stripped) {
					t.Error("stripWaitingChrome() did not remove separator lines")
				}
			}
		})
	}
}

func TestLooksLikeClaude(t *testing.T) {
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
			if got := looksLikeClaude(tt.content); got != tt.want {
				t.Errorf("looksLikeClaude() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "hello", "'hello'"},
		{"embedded quotes", "it's here", "'it'\\''s here'"},
		{"empty", "", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellQuote(tt.input); got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
