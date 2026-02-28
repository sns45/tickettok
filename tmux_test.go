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

func TestPreviewFromContent(t *testing.T) {
	cb := &ClaudeBackend{}
	normalStrip := func(lines []string) []string { return cb.StripChrome(lines, false) }
	waitingStrip := func(lines []string) []string { return cb.StripChrome(lines, true) }

	tests := []struct {
		name    string
		content string
		n       int
		stripFn func([]string) []string
		minLen  int
	}{
		{
			"basic content",
			"line one\nline two\nline three\nline four\nline five",
			3, normalStrip, 3,
		},
		{
			"strips chrome with prompt",
			"actual output\nmore output\n" + strings.Repeat("─", 20) + "\n❯ type here\nstatus line",
			5, normalStrip, 2,
		},
		{
			"respects n limit",
			"line alpha\nline beta\nline gamma\nline delta\nline epsilon",
			2, normalStrip, 2,
		},
		{
			"waiting mode strips separators",
			"output\n" + strings.Repeat("─", 20) + "\nAllow once\nlast line",
			5, waitingStrip, 1,
		},
		{
			"nil stripFn passes through",
			"line one\nline two\nline three",
			3, nil, 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreviewFromContent(tt.content, tt.n, tt.stripFn)
			if len(got) < tt.minLen {
				t.Errorf("PreviewFromContent() returned %d lines, want at least %d", len(got), tt.minLen)
			}
			if len(got) > tt.n {
				t.Errorf("PreviewFromContent() returned %d lines, want at most %d", len(got), tt.n)
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
