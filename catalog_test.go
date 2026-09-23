package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCatalogRoundTrip proves the YAML catalog layer preserves every field
// the TUI depends on (including the removed/archived/unavailable flags and
// the README-derived project link) without any network access. It is fully
// offline and deterministic.
func TestCatalogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.yaml")

	in := []Plugin{
		{Name: "xcbeautify", Desc: "format tool", Repo: "cpisciotta/asdf-xcbeautify", Project: "https://github.com/cpisciotta/xcbeautify", Archived: true},
		{Name: "kubectl", Desc: "kubectl plugin", Repo: "asdf-community/asdf-kubectl", Archived: false, Unavailable: true},
		{Name: "gone", Desc: "", Repo: "owner/gone", Removed: true, Unavailable: true},
		{Name: "adr-tools", Desc: "adr-tools plugin", Repo: "td7x/asdf/adr-tools", Project: "https://github.com/npryce/adr-tools", ProjectDesc: "Architecture Decision Records (ADR) tooling"},
	}

	if err := saveCatalogYAML(path, in); err != nil {
		t.Fatalf("saveCatalogYAML: %v", err)
	}
	out, err := loadCatalogYAML(path)
	if err != nil {
		t.Fatalf("loadCatalogYAML: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("round-trip lost plugins: got=%d want=4", len(out))
	}

	byName := map[string]Plugin{}
	for _, p := range out {
		byName[p.Name] = p
	}

	// archived flag survives
	x := byName["xcbeautify"]
	if !x.Archived {
		t.Errorf("Archived flag lost: %+v", x)
	}
	if x.Project != "https://github.com/cpisciotta/xcbeautify" {
		t.Errorf("Project link lost: %q", x.Project)
	}

	// project description survives
	a := byName["adr-tools"]
	if a.ProjectDesc != "Architecture Decision Records (ADR) tooling" {
		t.Errorf("ProjectDesc lost: %q", a.ProjectDesc)
	}

	// unavailable survives
	k := byName["kubectl"]
	if !k.Unavailable {
		t.Errorf("Unavailable flag lost: %+v", k)
	}

	// removed survives
	g := byName["gone"]
	if !g.Removed {
		t.Errorf("Removed flag lost: %+v", g)
	}
	if !g.Unavailable {
		t.Errorf("removed plugin should also be unavailable: %+v", g)
	}
}

// TestCatalogDiffMerge proves cmdCatalogRefresh is non-destructive: a plugin
// that disappears from `asdf plugin list all` is kept in the catalog with
// Removed=true instead of being deleted.
func TestCatalogDiffMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.yaml")
	before := []Plugin{{Name: "kubectl", Repo: "a/kubectl"}, {Name: "old", Repo: "o/old"}}
	fresh := []Plugin{{Name: "kubectl", Repo: "a/kubectl"}}

	if err := saveCatalogYAML(path, before); err != nil {
		t.Fatalf("saveCatalogYAML: %v", err)
	}
	out, err := mergeCatalogDiff(path, fresh, map[string]bool{"kubectl": true})
	if err != nil {
		t.Fatalf("mergeCatalogDiff: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("mergeCatalogDiff dropped removed plugin: got=%d want=2", len(out))
	}
	var old *Plugin
	for i := range out {
		if out[i].Name == "old" {
			old = &out[i]
		}
	}
	if old == nil || !old.Removed {
		t.Errorf("removed plugin should carry Removed=true: %+v", old)
	}
	if !old.Unavailable {
		t.Errorf("removed plugin should be marked unavailable: %+v", old)
	}
}

// TestMergeKeepsArchived proves a README-only (rate-limited) walk does not
// wipe a known archived flag and never flips 🔒 into 🗑: the plugin is back in
// asdf (fresh, reachable) but its archive state was not re-confirmed by the
// forge API.
func TestMergeKeepsArchived(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.yaml")
	before := []Plugin{{Name: "acc", Repo: "a/acc", Desc: "old desc", Archived: true}}
	fresh := []Plugin{{Name: "acc", Repo: "a/acc", Desc: "fresh desc"}} // API rate-limited → fallback

	if err := saveCatalogYAML(path, before); err != nil {
		t.Fatalf("saveCatalogYAML: %v", err)
	}
	out, err := mergeCatalogDiff(path, fresh, map[string]bool{}) // nothing verified
	if err != nil {
		t.Fatalf("mergeCatalogDiff: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("mergeCatalogDiff: got %d rows, want 1", len(out))
	}
	got := out[0]
	if !got.Archived {
		t.Errorf("unverified fresh walk lost Archived: %+v", got)
	}
	if got.Removed {
		t.Errorf("reachable plugin must not be marked Removed: %+v", got)
	}
	if got.Desc != "fresh desc" {
		t.Errorf("verified-ish fresh desc should win (fallback wrote it): %q", got.Desc)
	}

	// An API-confirmed walk overrides the old archived flag either way.
	unarch := []Plugin{{Name: "acc", Repo: "a/acc", Desc: "live again"}}
	out2, err := mergeCatalogDiff(path, unarch, map[string]bool{"acc": true})
	if err != nil {
		t.Fatalf("mergeCatalogDiff: %v", err)
	}
	if len(out2) != 1 || out2[0].Archived {
		t.Fatalf("API-confirmed fresh row should clear Archived: %+v", out2)
	}
	if out2[0].Removed {
		t.Errorf("reachable verified plugin must not be removed: %+v", out2[0])
	}
}

// TestMergeKeepsKnownDataOnFailure proves a failed probe does not erase the
// catalog's known data: a hard-failed plugin keeps its description, project
// link and project description and is marked unreachable, and a rate-limited
// walk keeps the last collected project description.
func TestMergeKeepsKnownDataOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.yaml")
	before := []Plugin{{
		Name:        "adb",
		Repo:        "a/adb",
		Desc:        "old desc",
		Project:     "https://github.com/x/adb",
		ProjectDesc: "old project desc",
		Archived:    true,
	}}
	if err := saveCatalogYAML(path, before); err != nil {
		t.Fatalf("saveCatalogYAML: %v", err)
	}

	// hard failure: fresh row carries Unavailable and empty known fields
	hard := []Plugin{{Name: "adb", Repo: "a/adb", Unavailable: true}}
	out, err := mergeCatalogDiff(path, hard, map[string]bool{}) // nothing verified
	if err != nil {
		t.Fatalf("mergeCatalogDiff: %v", err)
	}
	got := out[0]
	if !got.Unavailable {
		t.Errorf("hard-failed plugin must be marked unavailable: %+v", got)
	}
	if !got.Archived {
		t.Errorf("hard failure must keep the known Archived flag: %+v", got)
	}
	if got.Desc != "old desc" || got.Project != "https://github.com/x/adb" || got.ProjectDesc != "old project desc" {
		t.Errorf("hard failure must keep desc/project/project_desc: %+v", got)
	}

	// rate-limited fallback re-derives desc/project from the README; the
	// project description survives while the project link still matches
	rl := []Plugin{{Name: "adb", Repo: "a/adb", Desc: "readme desc", Project: "https://github.com/x/adb"}}
	out2, err := mergeCatalogDiff(path, rl, map[string]bool{})
	if err != nil {
		t.Fatalf("mergeCatalogDiff: %v", err)
	}
	g2 := out2[0]
	if g2.Desc != "readme desc" {
		t.Errorf("README fallback desc should win: %+v", g2)
	}
	if g2.ProjectDesc != "old project desc" {
		t.Errorf("rate-limited walk must keep the last project desc: %+v", g2)
	}

	// if the fallback project link changed, a stale project desc is dropped
	rl2 := []Plugin{{Name: "adb", Repo: "a/adb", Desc: "readme desc", Project: "https://github.com/other/adb"}}
	out3, err := mergeCatalogDiff(path, rl2, map[string]bool{})
	if err != nil {
		t.Fatalf("mergeCatalogDiff: %v", err)
	}
	if g3 := out3[0]; g3.ProjectDesc != "" {
		t.Errorf("stale project desc must be dropped when the project changed: %+v", g3)
	}
}

// TestProgressReporter proves catalog-refresh tallies aggregate correctly.
func TestProgressReporter(t *testing.T) {
	p := newProgressReporter(4)
	p.add(repoResult{unavailable: true}, false)
	p.add(repoResult{archived: true}, false)
	p.add(repoResult{rateLimited: true, fallback: true}, false)
	p.add(repoResult{}, false)
	if p.done != 4 || p.unavailable != 1 || p.archived != 1 || p.rate != 1 || p.fallback != 1 {
		t.Fatalf("tallies wrong: %+v", p)
	}
}

// TestRateLimiterPaces proves the shared forge-API limiter: a zero-delay
// limiter never blocks, while a paced limiter hands out tokens repoDelay
// apart — so bulk walks and repeated single refreshes can not fire REST calls
// back-to-back and trip GitHub/GitLab rate limits.
func TestRateLimiterPaces(t *testing.T) {
	rl := newRateLimiter(0)
	done := make(chan struct{})
	go func() {
		rl.wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("disabled limiter must not block")
	}

	start := time.Now()
	rl2 := newRateLimiter(30 * time.Millisecond)
	defer rl2.stop()
	rl2.wait()
	rl2.wait()
	if d := time.Since(start); d < 20*time.Millisecond {
		t.Fatalf("two paced waits must be repoDelay apart, got %v", d)
	}
}

// TestRefreshOnePluginParseError proves the offline branch of the shared
// single-plugin refresh helper: an unparseable repo is reported unavailable
// with an error (no network involved).
func TestRefreshOnePluginParseError(t *testing.T) {
	p, rep, err := refreshOnePlugin(Plugin{Name: "x", Repo: "not-a-url"}, newRateLimiter(0))
	if err == nil {
		t.Fatal("expected error for unparseable repo")
	}
	if !rep.unavailable || rep.rateLimited {
		t.Fatalf("expected unavailable (not rate-limited): %+v", rep)
	}
	if p.Name != "x" {
		t.Fatalf("plugin mutated on failure: %+v", p)
	}
}

// TestRefreshActionWiring proves the "Refresh plugin info" action sits at
// index 6 (key 7, before Remove plugin at index 7) and that running it
// schedules a background command instead of blocking.
func TestRefreshActionWiring(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "abc", Repo: "https://github.com/x/y.git"}}, "/tmp", true)
	out := m.renderActions(40, 20)
	if !strings.Contains(out, "Refresh plugin info") || !strings.Contains(out, "Remove plugin") {
		t.Fatalf("actions missing Refresh/Remove:\n%s", out)
	}
	if strings.Index(out, "Refresh plugin info") > strings.Index(out, "Remove plugin") {
		t.Fatalf("Refresh should come before Remove:\n%s", out)
	}

	sm, cmd := m.runAction(6)
	if cmd == nil {
		t.Fatal("refresh action should schedule a command")
	}
	if r, ok := sm.(*model); !ok || !r.busy {
		t.Fatalf("refresh action should mark the TUI busy")
	}

	boom, _ := m.runAction(7)
	if del, ok := boom.(*model); !ok || !del.confirmRm {
		t.Fatal("action 7 should trigger the remove confirmation")
	}
}

// TestRepoCatalogPath proves the checkout layout is detected only when a
// data/plugins.yaml actually exists next to the program or in the working
// directory; a plain working directory (e.g. a user running a bare binary)
// is not a checkout.
func TestRepoCatalogPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if p := repoCatalogPath(); p != "" {
		t.Fatalf("no data/ yet: want empty, got %q", p)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "data", "plugins.yaml")
	if err := os.WriteFile(want, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoCatalogPath(); got != want {
		t.Fatalf("checkout data should win: got %q want %q", got, want)
	}
}

// TestConfigCatalogPath proves the default catalog lives under $XDG_CONFIG_HOME
// (falling back to ~/.config) in the asdf-tui/data/ directory.
func TestConfigCatalogPath(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	want := filepath.Join(cfg, "asdf-tui", "data", "plugins.yaml")
	if got := configCatalogPath(); got != want {
		t.Fatalf("XDG config path: got %q want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := configCatalogPath(); got != filepath.Join(home, ".config", "asdf-tui", "data", "plugins.yaml") {
		t.Fatalf("HOME fallback path: got %q", got)
	}
}

// TestCatalogPathOutsideCheckout proves that when the program runs outside a
// repository checkout the config copy is the default for both loading and
// catalog-refresh. The file is pre-created, so no network is touched.
func TestCatalogPathOutsideCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // no data/ here → non-checkout mode
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := configCatalogPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := catalogPath(); got != p {
		t.Fatalf("catalogPath outside checkout: got %q want %q", got, p)
	}
	if got := catalogRefreshPath(); got != p {
		t.Fatalf("catalogRefreshPath outside checkout: got %q want %q", got, p)
	}
}

// TestCatalogPathPrefersCheckout proves the working-dir data/plugins.yaml wins
// over the XDG config copy whenever both exist.
func TestCatalogPathPrefersCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "data", "plugins.yaml")
	if err := os.WriteFile(repo, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := configCatalogPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := catalogPath(); got != repo {
		t.Fatalf("checkout copy must win: got %q want %q", got, repo)
	}
}

// TestWriteCatalogFile proves a downloaded catalog lands at the target path,
// creating parent directories on the way.
func TestWriteCatalogFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "b", "plugins.yaml")
	if err := writeCatalogFile(p, []byte("name: kubectl\n")); err != nil {
		t.Fatalf("writeCatalogFile: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(b) != "name: kubectl\n" {
		t.Fatalf("content mismatch: %q", b)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
