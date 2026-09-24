package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// updateURL points at the GitHub API's latest (non-prerelease) asdf-tui
// release; tag_name comes back as "v1.2.3".
const updateURL = "https://api.github.com/repos/BrunIF/asdf-tui/releases/latest"

// installCmd is the one-line installer from the README, wrapped in bash so
// the curl process substitution closes over the download.
const installCmd = "bash <(curl -sfL https://raw.githubusercontent.com/BrunIF/asdf-tui/main/install.sh)"

type updateCheckMsg struct {
	latest string
	err    error
}

// checkUpdateCmd asks GitHub for the newest published asdf-tui build in the
// background. Availability is never fatal: a flaky network, rate limit or
// missing release simply yields no prompt.
func checkUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		latest, err := latestRelease()
		return updateCheckMsg{latest: latest, err: err}
	}
}

func latestRelease() (string, error) {
	req, err := http.NewRequest("GET", updateURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "asdf-tui")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update check: status %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	if latest == "" {
		return "", fmt.Errorf("update check: empty version")
	}
	return latest, nil
}

// updateAvailable reports whether the published latest tag is newer than the
// stamped build version. Local/`go run` builds carry "dev" and are skipped —
// there is no stamped semver to compare against.
func updateAvailable(current, latest string) bool {
	if current == "" || current == "dev" || latest == "" {
		return false
	}
	return verLessNewer(latest, current)
}

// wantsUpdate digs the upgrade flag out of the final model: tea hands back
// either the model value or its pointer, depending on the last Update
// receiver, so both shapes are accepted.
func wantsUpdate(tm tea.Model) bool {
	switch m := tm.(type) {
	case model:
		return m.doUpdate
	case *model:
		return m.doUpdate
	}
	return false
}

// runInstaller fires the official install script with the terminal handed
// over, then the process exits with the script's result.
func runInstaller() {
	cmd := exec.Command("bash", "-c", installCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "update failed:", err)
		os.Exit(1)
	}
}
