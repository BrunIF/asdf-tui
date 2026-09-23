package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Plugin struct {
	Name string
	Desc string
	Repo string
	// Unavailable marks a plugin whose repository could not be reached
	// during catalog refresh (network error, removed plugin, rate-limit).
	// The TUI greys these rows out.
	Unavailable bool
	// Archived marks a repo the GitHub API reports as archived (read-only,
	// no longer accepting PRs).
	Archived bool
	// Removed marks a plugin that is present in the catalog but no longer
	// listed by `asdf plugin list all`. It is kept (not deleted) so the
	// TUI can draw a deletion icon after its name.
	Removed bool
	// Project is the upstream project this plugin wraps, discovered by
	// scanning the plugin's README for the first `[label](http…)` link
	// (e.g. asdf-xcbeautify → https://github.com/cpisciotta/xcbeautify).
	Project string
	// ProjectDesc is the upstream project's one-line description, fetched
	// from the project repo's forge API when the project link resolves to a
	// GitHub/GitLab repository (best effort — empty when unknown).
	ProjectDesc string
}

func loadCatalog(path string) ([]Plugin, error) {
	return loadCatalogYAML(path)
}

func loadCatalogYAML(path string) ([]Plugin, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Plugin
	sc := bufio.NewScanner(f)
	var cur *Plugin
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "- name:") {
			p := Plugin{Name: yamlUnquote(strings.TrimSpace(strings.TrimPrefix(t, "- name:")))}
			out = append(out, p)
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(t, "unavailable:") {
			cur.Unavailable = strings.TrimSpace(strings.TrimPrefix(t, "unavailable:")) == "true"
		} else if strings.HasPrefix(t, "archived:") {
			cur.Archived = strings.TrimSpace(strings.TrimPrefix(t, "archived:")) == "true"
		} else if strings.HasPrefix(t, "removed:") {
			cur.Removed = strings.TrimSpace(strings.TrimPrefix(t, "removed:")) == "true"
		} else if strings.HasPrefix(t, "desc:") {
			cur.Desc = yamlUnquote(strings.TrimSpace(strings.TrimPrefix(t, "desc:")))
		} else if strings.HasPrefix(t, "repo:") {
			cur.Repo = yamlUnquote(strings.TrimSpace(strings.TrimPrefix(t, "repo:")))
		} else if strings.HasPrefix(t, "project_desc:") {
			cur.ProjectDesc = yamlUnquote(strings.TrimSpace(strings.TrimPrefix(t, "project_desc:")))
		} else if strings.HasPrefix(t, "project:") {
			cur.Project = yamlUnquote(strings.TrimSpace(strings.TrimPrefix(t, "project:")))
		}
	}
	return out, sc.Err()
}

func yamlUnquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return s[1 : len(s)-1]
	}
	return s
}

func saveCatalogYAML(path string, plugins []Plugin) error {
	var b strings.Builder
	b.WriteString("# asdf-tui plugin catalog\n")
	b.WriteString("# regenerated from `asdf plugin list all`\n")
	b.WriteString("plugins:\n")
	for _, p := range plugins {
		b.WriteString("- name: " + yamlScalar(p.Name) + "\n")
		if p.Unavailable {
			b.WriteString("  unavailable: true\n")
		}
		if p.Archived {
			b.WriteString("  archived: true\n")
		}
		if p.Removed {
			b.WriteString("  removed: true\n")
		}
		b.WriteString("  desc: " + yamlScalar(p.Desc) + "\n")
		b.WriteString("  repo: " + yamlScalar(p.Repo) + "\n")
		if p.ProjectDesc != "" {
			b.WriteString("  project_desc: " + yamlScalar(p.ProjectDesc) + "\n")
		}
		if p.Project != "" {
			b.WriteString("  project: " + yamlScalar(p.Project) + "\n")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func yamlScalar(s string) string {
	if s == "" {
		return "\"\""
	}
	if strings.ContainsAny(s, ":#\n\"'\t\n") {
		return strconv.Quote(s)
	}
	return s
}

// resolvedCatalogTemplate is the branch/tag placeholder used to probe both
// upstream branches when seeding the user catalog on first run outside a
// checkout.
const remoteCatalogTemplate = "https://raw.githubusercontent.com/BrunIF/asdf-tui/%s/data/plugins.yaml"

// repoCatalogPath returns an existing data/plugins.yaml in the checkout layout:
// next to the running executable first, then in the working directory. An empty
// string means the program is not running from a repository checkout.
func repoCatalogPath() string {
	checks := make([]string, 0, 2)
	if exe, err := os.Executable(); err == nil {
		checks = append(checks, filepath.Join(filepath.Dir(exe), "data", "plugins.yaml"))
	}
	if p, err := filepath.Abs("data/plugins.yaml"); err == nil {
		checks = append(checks, p)
	}
	for _, c := range checks {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// configCatalogPath returns the user-writable default catalog location:
// ~/.config/asdf-tui/data/plugins.yaml (XDG_CONFIG_HOME respected).
func configCatalogPath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = ".config"
	}
	return filepath.Join(base, "asdf-tui", "data", "plugins.yaml")
}

// catalogPath resolves the catalog file used for loading. Inside a repository
// checkout the committed data/plugins.yaml wins; otherwise the location is
// ~/.config/asdf-tui/data/plugins.yaml and the catalog is seeded from the
// project repository on first use, so a plain binary (no checkout on disk)
// still ships with a full plugin list.
func catalogPath() string {
	if p := repoCatalogPath(); p != "" {
		return p
	}
	p := configCatalogPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		if err := fetchRemoteCatalog(p); err != nil {
			fmt.Fprintln(os.Stderr, "warning: cannot download default catalog:", err)
		}
	}
	return p
}

// catalogRefreshPath resolves the file catalog-refresh rewrites: the checkout
// copy when present, otherwise the user config location. It never seeds from
// the network — refresh rebuilds the whole catalog from `asdf plugin list all`
// anyway.
func catalogRefreshPath() string {
	if p := repoCatalogPath(); p != "" {
		return p
	}
	return configCatalogPath()
}

// fetchRemoteCatalog downloads data/plugins.yaml from the project repository
// (trying the common default branches) and stores it at path.
func fetchRemoteCatalog(path string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	for _, branch := range []string{"main", "master"} {
		url := fmt.Sprintf(remoteCatalogTemplate, branch)
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil || resp.StatusCode != http.StatusOK || len(strings.TrimSpace(string(body))) == 0 {
			continue
		}
		return writeCatalogFile(path, body)
	}
	return fmt.Errorf("no catalog found upstream (tried %s)", fmt.Sprintf(remoteCatalogTemplate, "main"))
}

// writeCatalogFile atomically stores a downloaded catalog: parents are
// created, bytes go to a temp file that is renamed over the target.
func writeCatalogFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".plugins-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
