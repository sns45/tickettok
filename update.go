package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// updateCheckMsg reports the result of a background version check.
type updateCheckMsg struct {
	available bool
	latest    string // e.g. "0.6.0"
	assetURL  string // browser_download_url for matching tarball
}

// updateDoneMsg reports the result of a download+install.
type updateDoneMsg struct {
	err     error
	version string
}

// forceQuitMsg triggers TUI exit after a successful update.
type forceQuitMsg struct{}

const lastCheckFile = "last_update_check"
const checkInterval = 24 * time.Hour
const githubReleasesURL = "https://api.github.com/repos/sns45/tickettok/releases/latest"

// shouldCheckUpdate returns true if we haven't checked in the last 24h.
func shouldCheckUpdate() bool {
	path := filepath.Join(stateDir(), lastCheckFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return true
	}
	return time.Since(time.Unix(ts, 0)) > checkInterval
}

// touchCheckFile writes the current timestamp to the check file.
func touchCheckFile() {
	path := filepath.Join(stateDir(), lastCheckFile)
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d", time.Now().Unix())), 0644)
}

// parseVersion parses "v1.2.3" or "1.2.3" into (major, minor, patch).
// Strips -rc and similar suffixes.
func parseVersion(s string) (int, int, int, error) {
	s = strings.TrimPrefix(s, "v")
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid version: %s", s)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, err
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, err
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, 0, 0, err
	}
	return major, minor, patch, nil
}

// isNewer returns true if latest is a newer version than current.
func isNewer(latest, current string) bool {
	lMaj, lMin, lPat, err := parseVersion(latest)
	if err != nil {
		return false
	}
	cMaj, cMin, cPat, err := parseVersion(current)
	if err != nil {
		return false
	}
	if lMaj != cMaj {
		return lMaj > cMaj
	}
	if lMin != cMin {
		return lMin > cMin
	}
	return lPat > cPat
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// checkUpdateCmd returns a tea.Cmd that checks GitHub for a newer release.
func checkUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		if !shouldCheckUpdate() {
			return updateCheckMsg{available: false}
		}

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(githubReleasesURL)
		if err != nil {
			return updateCheckMsg{available: false}
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return updateCheckMsg{available: false}
		}

		var release ghRelease
		if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
			return updateCheckMsg{available: false}
		}

		touchCheckFile()

		if !isNewer(release.TagName, version) {
			return updateCheckMsg{available: false}
		}

		// Find matching asset: tickettok_<GOOS>_<GOARCH>.tar.gz
		wantName := fmt.Sprintf("tickettok_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
		var assetURL string
		for _, a := range release.Assets {
			if a.Name == wantName {
				assetURL = a.BrowserDownloadURL
				break
			}
		}
		if assetURL == "" {
			return updateCheckMsg{available: false}
		}

		latestVer := strings.TrimPrefix(release.TagName, "v")
		return updateCheckMsg{
			available: true,
			latest:    latestVer,
			assetURL:  assetURL,
		}
	}
}

// doUpdateCmd downloads the tarball, extracts the binary, and replaces the current one.
func doUpdateCmd(assetURL, latestVersion string) tea.Cmd {
	return func() tea.Msg {
		err := performUpdate(assetURL)
		if err != nil {
			return updateDoneMsg{err: err, version: latestVersion}
		}
		return updateDoneMsg{err: nil, version: latestVersion}
	}
}

func performUpdate(assetURL string) error {
	// Resolve current binary path (follows symlinks for Homebrew)
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	exeDir := filepath.Dir(exePath)

	// Verify write permission by creating a temp file in the same directory
	probe, err := os.CreateTemp(exeDir, ".tickettok-update-probe-*")
	if err != nil {
		return fmt.Errorf("no write permission to %s: %w", exeDir, err)
	}
	probePath := probe.Name()
	probe.Close()
	os.Remove(probePath)

	// Download tarball
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(assetURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	// Extract binary from tar.gz
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var binaryData []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		// The binary is named "tickettok" inside the tarball
		if filepath.Base(hdr.Name) == "tickettok" && hdr.Typeflag == tar.TypeReg {
			binaryData, err = io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("read binary from tar: %w", err)
			}
			break
		}
	}
	if binaryData == nil {
		return fmt.Errorf("binary not found in tarball")
	}

	// Write to temp file in same directory (same filesystem for atomic rename)
	tmpFile, err := os.CreateTemp(exeDir, ".tickettok-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(binaryData); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write binary: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	// Atomic rename (same filesystem)
	if err := os.Rename(tmpPath, exePath); err != nil {
		// Cross-filesystem fallback
		if copyErr := crossFSReplace(tmpPath, exePath); copyErr != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("replace binary: %w", copyErr)
		}
	}

	return nil
}

// crossFSReplace copies src to dst when they're on different filesystems.
func crossFSReplace(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0755); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}

// reExec replaces the current process with a fresh invocation of the binary.
func reExec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
