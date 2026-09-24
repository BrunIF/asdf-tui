package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func catalogValue(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func cmdAdd(name string, repo string) {
	if strings.TrimSpace(name) == "" {
		fmt.Fprintln(os.Stderr, "usage: asdf-tui add NAME [REPO]")
		os.Exit(1)
	}
	if err := asdfAddPlugin(name, repo); err != nil {
		fmt.Fprintln(os.Stderr, "add failed:", err)
		os.Exit(1)
	}
	path := catalogPath()
	plugins, err := loadCatalog(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "cannot read catalog:", err)
		os.Exit(1)
	}
	dup := false
	for _, p := range plugins {
		if p.Name == name {
			dup = true
			break
		}
	}
	if !dup {
		plugins = append(plugins, Plugin{Name: name, Repo: repo})
	}
	err = saveCatalogYAML(path, plugins)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot update catalog:", err)
		os.Exit(1)
	}
	fmt.Println("added", name, "("+repo+")")
}

func cmdCatalogRefresh() {
	rows, err := asdfPluginListAll()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot fetch plugin list:", err)
		os.Exit(1)
	}
	// Walk every repository in parallel and fill desc / archived / project
	// (unavailable kept grey / unavailable). Offline-safe: each repo gets
	// an 8s timeout and failures mark the row unavailable instead of
	// aborting the refresh. Requests are paced (repoDelay) so GitHub's rate
	// limit does not block the whole run; a paced 403/429 falls back to
	// README-only data (raw.githubusercontent is not API-rate-limited).
	nRate, nFallback, verified := fillPluginRepoInfo(rows)
	path := catalogRefreshPath()
	merged, err := mergeCatalogDiff(path, rows, verified)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot diff catalog:", err)
		os.Exit(1)
	}
	if err := saveCatalogYAML(path, merged); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write catalog:", err)
		os.Exit(1)
	}
	nUnavail, nArch := 0, 0
	for _, p := range merged {
		if p.Unavailable {
			nUnavail++
		}
		if p.Archived {
			nArch++
		}
	}
	fmt.Printf("catalog refreshed: %d plugins → %s (%d unavailable, %d archived, %d API-rate-limited → README fallback)\n",
		len(merged), path, nUnavail, nArch, nRate)
	if nFallback > 0 {
		if githubToken() != "" || gitlabToken() != "" {
			fmt.Printf("  note: %d plugins got desc/project from README only (archived unknown)\n", nFallback)
		} else {
			fmt.Printf("  note: %d plugins got desc/project from README only (archived unknown); set ASDF_TUI_REPO_DELAY_MS higher or declare GITHUB_TOKEN/GITLAB_TOKEN to raise the API rate limit\n", nFallback)
		}
	}
}

// repoDelay returns the pause between per-repo checks. Override via
// ASDF_TUI_REPO_DELAY_MS (ms); 0 disables pacing. Slower = far fewer 403s.
func repoDelay() time.Duration {
	ms := 300
	if v := os.Getenv("ASDF_TUI_REPO_DELAY_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// fillPluginRepoInfo walks rows in parallel, but no more than 4 at once and
// with every forge API call (plugin repo + its upstream project description)
// passing through a single rateLimiter that hands out one token per
// repoDelay, to stay under GitHub's/GitLab's rate limits. When the API
// replies 403/429 it falls back to the README (desc via first heading,
// project via first real link) instead of marking the plugin unavailable.
// While it runs it reports live progress (spinner, count, error/fallback/
// archived tallies) to stderr. It returns how many were rate-limited, how
// many of those the README fallback recovered, and the set of plugins whose
// archive state the forge API confirmed (success, not fallback) —
// mergeCatalogDiff needs this to avoid erasing a known archived flag on a
// README-only walk.
func fillPluginRepoInfo(rows []Plugin) (nRate, nFallback int, verified map[string]bool) {
	rl := newRateLimiter(repoDelay())
	defer rl.stop()
	results := make(chan repoResult, 64)
	go func() {
		defer close(results)
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for i := range rows {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				pl := &rows[i]
				np, rep, err := refreshOnePlugin(*pl, rl)
				rep.name = np.Name
				rep.verified = err == nil && !rep.fallback
				if err == nil {
					*pl = np
				} else {
					// hard failure: keep the fresh row but flag it so
					// mergeCatalogDiff preserves its known data and marks
					// the plugin unreachable (🚫) instead of blanking it.
					pl.Unavailable = true
					rep.unavailable = true
				}
				results <- rep
			}(i)
		}
		wg.Wait()
	}()

	tty := isTerminal(os.Stderr)
	prog := newProgressReporter(len(rows))
	verified = make(map[string]bool, len(rows))
	for r := range results {
		prog.add(r, tty)
		if r.verified {
			verified[r.name] = true
		}
	}
	if tty {
		fmt.Fprintln(os.Stderr)
	}
	return prog.rate, prog.fallback, verified
}

// repoResult reports what happened to one plugin during the catalog walk.
type repoResult struct {
	name        string // plugin this row describes
	verified    bool   // the forge API answered (archived is authoritative)
	unavailable bool   // marked unreachable (no data at all)
	rateLimited bool   // API said 403/429
	fallback    bool   // recovered from the README after the rate limit
	archived    bool   // repo is archived (read-only)
}

const spinnerFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// progressReporter aggregates catalog-refresh tallies and draws a single live
// status line on a terminal (carriage-return updates) or a periodic plain
// line otherwise.
type progressReporter struct {
	total       int
	done        int
	unavailable int
	rate        int
	fallback    int
	archived    int
}

func newProgressReporter(total int) *progressReporter { return &progressReporter{total: total} }

func (p *progressReporter) add(r repoResult, tty bool) {
	p.done++
	if r.unavailable {
		p.unavailable++
	}
	if r.rateLimited {
		p.rate++
	}
	if r.fallback {
		p.fallback++
	}
	if r.archived {
		p.archived++
	}
	switch {
	case tty:
		spin := string(spinnerFrames[p.done%len(spinnerFrames)])
		pct := 0
		if p.total > 0 {
			pct = p.done * 100 / p.total
		}
		fmt.Fprintf(os.Stderr, "\r\x1b[K%s %d/%d (%d%%) · ✗%d · ≫%d ⇐%d · 🔒%d",
			spin, p.done, p.total, pct, p.unavailable, p.rate, p.fallback, p.archived)
	case p.done%25 == 0 || p.done == p.total:
		fmt.Fprintf(os.Stderr, "checked %d/%d (%d unavailable, %d rate-limited, %d via README, %d archived)\n",
			p.done, p.total, p.unavailable, p.rate, p.fallback, p.archived)
	}
}

// isTerminal reports whether f is a character device (a TTY), so progress
// output can decide between \r updates and plain periodic lines.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// refreshOnePlugin re-fetches a single plugin's repository data (desc,
// archived, project, unavailable) using the exact same logic as the catalog
// walk: forge API first, 403/429 → README-only fallback. Every forge API
// request it makes waits for a token on rl, so bursts (bulk walk or repeated
// single refreshes) can not exceed one REST call per repoDelay. The returned
// repoResult explains what happened (unavailable / rateLimited / fallback /
// archived) so callers can report tallies; a non-nil error means the plugin
// could not produce usable data.
func refreshOnePlugin(p Plugin, rl *rateLimiter) (Plugin, repoResult, error) {
	ref, ok := parseRepo(p.Repo)
	if !ok {
		return p, repoResult{unavailable: true}, fmt.Errorf("cannot parse repo %q", p.Repo)
	}
	rl.wait()
	desc, archived, project, err := pluginRepoInfo(ref)
	if err == nil {
		p.Desc, p.Archived, p.Project, p.Unavailable = desc, archived, project, false
		// the API just answered, so a second paced call for the project's
		// description is affordable; skip it when we were rate-limited
		rl.wait()
		p.ProjectDesc = projectRepoDescription(project)
		return p, repoResult{archived: archived}, nil
	}
	if !errors.Is(err, errRateLimit) {
		return p, repoResult{unavailable: true}, err
	}
	// rate-limited: README-only data instead of "unreachable". A README can
	// not tell whether the repo is archived, so keep the last known Archived
	// flag instead of flipping 🔒 off.
	body, rerr := rawReadme(ref)
	if fb := readmeHeading(body); rerr != nil || fb == "" {
		return p, repoResult{unavailable: true, rateLimited: true},
			fmt.Errorf("repo rate-limited and no README fallback available")
	}
	p.Desc, p.Project, p.Unavailable = readmeHeading(body), projectLinkFromREADME(string(body), ref), false
	return p, repoResult{rateLimited: true, fallback: true}, nil
}

// mergeCatalogDiff keeps rows that already live in the YAML catalog (so a
// plugin that dropped out of `asdf plugin list all` is NOT deleted but stays
// with Removed=true and the 🗑 icon), and only rewrites rows that actually
// changed. Rows fetched fresh keep their network/summary fields.
//
// verified holds the plugins whose forge API answered during the walk; for
// every other row (README fallback or hard failure) the walk could not
// re-derive the archive state, so the last known Archived flag (🔒) is kept
// instead of being silently wiped. A hard failure (Unavailable) also keeps the
// last known desc/project/project_desc — a failed probe must not erase data
// the catalog already had.
func mergeCatalogDiff(path string, fresh []Plugin, verified map[string]bool) ([]Plugin, error) {
	existing, err := loadCatalog(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	byName := make(map[string]*Plugin, len(existing))
	for i := range existing {
		byName[existing[i].Name] = &existing[i]
	}
	out := make([]Plugin, 0, len(fresh))
	for _, f := range fresh {
		if old, ok := byName[f.Name]; ok {
			if !verified[f.Name] {
				// fallback / failure: keep flags that only the API knows
				if old.Archived {
					f.Archived = true
				}
				if f.Unavailable {
					// a probe failure must not erase a known description
					if old.Desc != "" {
						f.Desc = old.Desc
					}
					if old.Project != "" {
						f.Project = old.Project
					}
				}
				// a rate-limited walk does not re-collect the project
				// description; keep the last known one while the upstream
				// project link still matches
				if f.ProjectDesc == "" && old.Project != "" && old.Project == f.Project {
					f.ProjectDesc = old.ProjectDesc
				}
			}
			// a plugin that is back in `asdf plugin list all` and reachable
			// is no longer "removed"; unreachable rows keep their flag
			f.Removed = old.Removed && f.Unavailable
			// app_desc is curated data (a name-only utility description) and
			// is never re-derived from the forge, so carry the known value
			// over on every refresh.
			f.AppDesc = old.AppDesc
		}
		out = append(out, f)
	}
	// anything in the old catalog no longer in `fresh` → Removed (greyed,
	// with 🗑 after the name) instead of deleted from disk.
	for _, o := range existing {
		found := false
		for _, f := range fresh {
			if f.Name == o.Name {
				found = true
				break
			}
		}
		if !found {
			o.Removed = true
			o.Unavailable = true
			out = append(out, o)
		}
	}
	// stable sort by name so the diff/rewrite is idempotent
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func usage() {
	fmt.Println(styleBrand.Render(" asdf-tui ") + " fuzzy column TUI for asdf-managed tools\n")
	fmt.Println("  asdf-tui            interactive TUI (default)")
	fmt.Println("  asdf-tui list       installed plugins and versions")
	fmt.Println("  asdf-tui update     update every added plugin")
	fmt.Println("  asdf-tui plugins    list added plugins")
	fmt.Println("  asdf-tui add NAME [REPO]")
	fmt.Println("                       add a plugin (asdf plugin add) and append to catalog")
	fmt.Println("  asdf-tui catalog-refresh")
	fmt.Println("                       rebuild the catalog from `asdf plugin list all`")
	fmt.Println("\nCatalog: data/plugins.yaml in the checkout, or seeded from the")
	fmt.Println("project repository into ~/.config/asdf-tui/data/plugins.yaml.")
	fmt.Println("\nRequires: asdf.")
}

func cmdList() {
	plugins := asdfPluginList()
	if len(plugins) == 0 {
		fmt.Println("(no plugins added)")
		return
	}
	for _, p := range plugins {
		fmt.Printf("  • %s\n", p)
		versions, _ := toolInstalledVersions(p)
		for _, v := range versions {
			fmt.Printf("      %s\n", v)
		}
	}
}

func cmdUpdate() {
	plugins := asdfPluginList()
	if len(plugins) == 0 {
		fmt.Println("(no plugins added)")
		return
	}
	fmt.Println("updating all plugins…")
	if _, err := runCmd("plugin", "update", "--all"); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("done")
}

func cmdPlugins() {
	for _, p := range asdfPluginList() {
		fmt.Println(p)
	}
}

// version is stamped at build time by the release workflow
// (`go build -ldflags "-X main.version=..."`) and shown by `asdf-tui --version`.
// Local/`go run` builds keep "dev" so a hand-built binary is never mistaken
// for a published one.
var version = "dev"

func cmdVersion() {
	fmt.Printf("asdf-tui %s\n", version)
}

func main() {
	cmd := "search"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	cwd, _ := os.Getwd()
	switch cmd {
	case "search":
		plugins, err := loadCatalog(catalogPath())
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot load catalog:", err)
			os.Exit(1)
		}
		if len(plugins) == 0 {
			fmt.Fprintln(os.Stderr, "catalog is empty:", catalogPath())
			os.Exit(1)
		}
		runTUI(plugins, cwd)
	case "list":
		cmdList()
	case "update":
		cmdUpdate()
	case "plugins":
		cmdPlugins()
	case "add":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: asdf-tui add NAME [REPO]")
			os.Exit(1)
		}
		cmdAdd(os.Args[2], catalogValue(os.Args, 3))
	case "catalog-refresh":
		cmdCatalogRefresh()
	case "version", "--version", "-v":
		cmdVersion()
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", strings.TrimSpace(cmd))
		usage()
		os.Exit(1)
	}
}
