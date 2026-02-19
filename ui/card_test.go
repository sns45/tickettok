package ui

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 45 * time.Second, "45s"},
		{"minutes", 5 * time.Minute, "5m"},
		{"hours and minutes", 2*time.Hour + 30*time.Minute, "2h30m"},
		{"zero", 0, "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDuration(tt.d); got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestShortenDir(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{"home prefix replaced", home + "/projects/foo", "~/projects/foo"},
		{"non-home unchanged", "/tmp/something", "/tmp/something"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortenDir(tt.dir)
			if tt.name == "home prefix replaced" {
				if !strings.HasPrefix(got, "~/") {
					t.Errorf("shortenDir(%q) = %q, want prefix ~/", tt.dir, got)
				}
			} else {
				if got != tt.want {
					t.Errorf("shortenDir(%q) = %q, want %q", tt.dir, got, tt.want)
				}
			}
		})
	}
}
