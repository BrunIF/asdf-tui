package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// TestRankPlugins proves text search is not limited to plugin names: a term
// that only occurs in the project link, description or project description
// still matches, while name matches rank first and non-matching plugins are
// dropped entirely.
func TestRankPlugins(t *testing.T) {
	plugins := []Plugin{
		{Name: "kubectl", Desc: "control plane tool", Project: "https://github.com/k8s/kubectl", ProjectDesc: ""},
		{Name: "kubeconform", Desc: "kubernetes manifest validator", Project: "https://github.com/yannh/kubeconform", ProjectDesc: "validates kubernetes config"},
		{Name: "helm", Desc: "kubernetes package manager", Project: "https://github.com/helm/helm", ProjectDesc: ""},
		{Name: "ripgrep", Desc: "grep on steroids", Project: "", ProjectDesc: ""},
	}

	// description-only match → helm is first (desc 'kubernetes ...'), rest dropped
	ranks := rankPlugins(plugins, "kubernetes")
	got := indexesOf(ranks)
	if len(got) != 2 || got[0] != 2 && got[1] != 2 {
		t.Errorf("kubernetes should match kubeconform+helm first: %v", got)
	}
	if len(got) == 2 && (got[0] == 1 && got[1] == 2 || got[0] == 2 && got[1] == 1) {
		// ok: both match via desc
	} else {
		t.Errorf("expected kubeconform & helm to match, got %v", got)
	}

	// substring of a project link matches (no name/desc hit)
	if ranks := indexesOf(rankPlugins(plugins, "yannh")); len(ranks) != 1 || ranks[0] != 1 {
		t.Errorf("project-link search should match kubeconform only: %v", ranks)
	}

	// project description search matches too
	if ranks := indexesOf(rankPlugins(plugins, "manifest validator")); len(ranks) != 1 || ranks[0] != 1 {
		t.Errorf("project-desc search should match kubeconform only: %v", ranks)
	}

	// non-matching term yields nothing
	if ranks := rankPlugins(plugins, "zzzznope"); len(ranks) != 0 {
		t.Errorf("no plugin should match zzzznope: %v", ranks)
	}

	// name match outranks a description-only match for the same term
	nr := indexesOf(rankPlugins(plugins, "kubernetes package manager")) // helm name 'helm' won't match; kubeconform desc does
	if len(nr) != 1 || nr[0] != 2 {
		t.Errorf("multiword desc query should match helm only: %v", nr)
	}
	// name substring outranks identical desc match: 'kube' hits kubectl(kube*d?), kubeconform, helm(desc)
	kr := indexesOf(rankPlugins(plugins, "kube"))
	if len(kr) < 2 {
		t.Fatalf("'kube' should match kubectl and kubeconform: %v", kr)
	}
	if kr[1] != 1 {
		t.Errorf("name-substring matches must rank above desc-only (helm has desc 'kubernetes'): %v got first=%d second=%d", kr, kr[0], kr[1])
	}

	// case-insensitivity
	if ranks := indexesOf(rankPlugins(plugins, "KUBectl")); len(ranks) != 1 || ranks[0] != 0 {
		t.Errorf("case-insensitive name search should match kubectl: %v", ranks)
	}
}

func indexesOf(ranks []list.Rank) []int {
	if ranks == nil {
		return nil
	}
	out := make([]int, len(ranks))
	for i, r := range ranks {
		out[i] = r.Index
	}
	return out
}

// TestVersionWindowTop proves PgUp/PgDn move the visible window by a full page
// (the regression where the list did not scroll when the selection left the
// visible rows).
func TestVersionWindowTop(t *testing.T) {
	cases := []struct {
		sel, total, n, want int
	}{
		{sel: 0, total: 47, n: 100, want: 0},
		{sel: 47, total: 47, n: 100, want: 47}, // first PgDn → next page
		{sel: 94, total: 47, n: 100, want: 53}, // end: clamp so last page is full
		{sel: 99, total: 47, n: 100, want: 53}, // clamp so last page is full
		{sel: 29, total: 47, n: 30, want: 0},   // short list, no scroll
		{sel: 46, total: 47, n: 47, want: 0},   // everything fits on one page
		{sel: 5, total: 1, n: 10, want: 5},     // tiny viewport follows sel
		{sel: 0, total: 47, n: 0, want: 0},     // empty list
	}
	for _, c := range cases {
		got := versionWindowTop(c.sel, c.total, c.n)
		if got != c.want {
			t.Errorf("versionWindowTop(sel=%d,total=%d,n=%d) = %d, want %d",
				c.sel, c.total, c.n, got, c.want)
		}
	}
}

// TestRenderVersionsPages renders the versions column directly and asserts a
// PgDn actually changes which rows are visible.
func TestRenderVersionsPages(t *testing.T) {
	items := make([]string, 100)
	for i := range items {
		items[i] = fmt.Sprintf("v%02d", i)
	}

	m := model{verItems: items, verFilter: "", verSel: 0, verTop: 0, mode: verInstall}
	first := m.renderVersions(24, 51) // total = 47 rows per page
	if !strings.Contains(first, "v00") || strings.Contains(first, "v47") {
		t.Fatalf("page 1 should show v00..v46, got window around v00=%v v47=%v", strings.Contains(first, "v00"), strings.Contains(first, "v47"))
	}

	m.verSel = 47 // one PgDn on a 47-row viewport
	second := m.renderVersions(24, 51)
	if !strings.Contains(second, "v47") || strings.Contains(second, "v00") {
		t.Fatalf("page 2 should show v47..v93 without v00, got window v00=%v v47=%v", strings.Contains(second, "v00"), strings.Contains(second, "v47"))
	}
}

// TestProjectLinkFromREADME proves the README project-link parser skips badge
// images/destinations and the plugin's own repo, picking the upstream tool URL
// (avalanche regression: it previously grabbed the GitHub Actions badge).
func TestProjectLinkFromREADME(t *testing.T) {
	avalanche := "# asdf-avalanche [![Build](https://github.com/embtools/asdf-avalanche/actions/workflows/build.yml/badge.svg)](https://github.com/embtools/asdf-avalanche/actions/workflows/build.yml) [![Lint](https://github.com/embtools/asdf-avalanche/actions/workflows/lint.yml/badge.svg)](https://github.com/embtools/asdf-avalanche/actions/workflows/lint.yml)\n\n[avalanche](https://github.com/ava-labs/avalanche-cli) plugin for the [asdf version manager](https://asdf-vm.com).\n"
	want := "https://github.com/ava-labs/avalanche-cli"
	if got := projectLinkFromREADME(avalanche, mustParse(t, "https://github.com/embtools/asdf-avalanche.git")); got != want {
		t.Errorf("avalanche: got %q, want %q", got, want)
	}

	apko := "[apko](https://codeberg.org/omissis/asdf-apko) plugin for asdf.\n"
	if got := projectLinkFromREADME(apko, mustParse(t, "https://github.com/omissis/asdf-apko.git")); got != "https://codeberg.org/omissis/asdf-apko" {
		t.Errorf("apko: got %q, want codeberg project link", got)
	}

	// own-repo link first must be skipped in favour of the real project
	selfFirst := "Install via [asdf-avalanche](https://github.com/embtools/asdf-avalanche.git).\nSee [avalanche-cli](https://github.com/ava-labs/avalanche-cli).\n"
	if got := projectLinkFromREADME(selfFirst, mustParse(t, "https://github.com/embtools/asdf-avalanche.git")); got != want {
		t.Errorf("self-repo first: got %q, want %q", got, want)
	}

	// nested-group self-link on the same forge must be skipped too
	gitlabSelf := "# asdf-adr-tools\n[adr-tools](https://gitlab.com/td7x/asdf/adr-tools) plugin for [asdf](https://github.com/asdf-vm/asdf).\n[adr-tools](https://github.com/npryce/adr-tools).\n"
	if got := projectLinkFromREADME(gitlabSelf, mustParse(t, "https://gitlab.com/td7x/asdf/adr-tools.git")); got != "https://github.com/npryce/adr-tools" {
		t.Errorf("gitlab self-link first: got %q, want npryce/adr-tools", got)
	}

	// only badges/images → no project link
	onlyBadges := "![build](https://img.shields.io/.../badge.svg)\n"
	if got := projectLinkFromREADME(onlyBadges, mustParse(t, "https://github.com/embtools/asdf-avalanche.git")); got != "" {
		t.Errorf("badges only: got %q, want empty", got)
	}

	if o, r := githubSlug("https://github.com/embtools/asdf-avalanche/actions/workflows/build.yml"); o != "embtools" || r != "asdf-avalanche" {
		t.Errorf("githubSlug: got (%q,%q), want (embtools,asdf-avalanche)", o, r)
	}
}

// mustParse parses a repository URL in tests, failing the test on garbage.
func mustParse(t *testing.T, repo string) repoRef {
	t.Helper()
	ref, ok := parseRepo(repo)
	if !ok {
		t.Fatalf("parseRepo(%q) failed", repo)
	}
	return ref
}

// TestParseRepo proves repo URLs from `asdf plugin list all` parse for GitHub,
// GitLab (incl. nested groups, `*` prefixes and ssh forms) and other forges,
// and reject junk (adr-tools regression: GitLab repos were unavailable).
func TestParseRepo(t *testing.T) {
	cases := []struct {
		in    string
		ok    bool
		host  string
		owner string
		name  string
		slug  string
	}{
		{"https://github.com/looztra/asdf-k9s.git", true, "github.com", "looztra", "asdf-k9s", "looztra/asdf-k9s"},
		{"*https://github.com/looztra/asdf-k9s.git", true, "github.com", "looztra", "asdf-k9s", "looztra/asdf-k9s"},
		{"git@github.com:looztra/asdf-k9s.git", true, "github.com", "looztra", "asdf-k9s", "looztra/asdf-k9s"},
		{"ssh://git@github.com/looztra/asdf-k9s.git", true, "github.com", "looztra", "asdf-k9s", "looztra/asdf-k9s"},
		{"https://gitlab.com/td7x/asdf/adr-tools.git", true, "gitlab.com", "td7x", "adr-tools", "td7x/asdf/adr-tools"},
		{"git@gitlab.com:td7x/asdf/adr-tools.git", true, "gitlab.com", "td7x", "adr-tools", "td7x/asdf/adr-tools"},
		{"https://codeberg.org/omissis/asdf-apko.git", true, "codeberg.org", "omissis", "asdf-apko", "omissis/asdf-apko"},
		{"", false, "", "", "", ""},
		{"no-scheme-here", false, "", "", "", ""},
	}
	for _, c := range cases {
		ref, ok := parseRepo(c.in)
		if ok != c.ok {
			t.Errorf("parseRepo(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if ref.host != c.host || ref.owner != c.owner || ref.name != c.name || ref.slug != c.slug {
			t.Errorf("parseRepo(%q) = {%q %q %q %q}, want {%q %q %q %q}",
				c.in, ref.host, ref.owner, ref.name, ref.slug, c.host, c.owner, c.name, c.slug)
		}
	}
}

// TestAsciiDocREADME proves plugin projects whose README is AsciiDoc
// (age-plugin-yubikey: README.adoc) still resolve their heading and project
// link — AsciiDoc uses `= title` and `URL[label]` instead of markdown forms.
func TestAsciiDocREADME(t *testing.T) {
	adoc := "= asdf-age-plugin-yubikey\n\n" +
		"image:https://github.com/joke/asdf-age-plugin-yubikey/actions/workflows/build.yml/badge.svg[link=https://github.com/joke/asdf-age-plugin-yubikey/actions/workflows/build.yml]\n" +
		"image:https://img.shields.io/badge/pre--commit-enabled-brightgreen?logo=pre-commit[pre-commit, link=https://github.com/pre-commit/pre-commit]\n\n" +
		"https://github.com/str4d/age-plugin-yubikey[age-plugin-yubikey]\n" +
		"plugin for the https://github.com/asdf-vm/asdf[asdf] version manager.\n"
	self := mustParse(t, "https://github.com/joke/asdf-age-plugin-yubikey.git")

	if h := readmeHeading([]byte(adoc)); h != "asdf-age-plugin-yubikey" {
		t.Errorf("readmeHeading(adoc): got %q, want asdf-age-plugin-yubikey", h)
	}
	if got := projectLinkFromREADME(adoc, self); got != "https://github.com/str4d/age-plugin-yubikey" {
		t.Errorf("projectLinkFromREADME(adoc): got %q, want str4d/age-plugin-yubikey", got)
	}
}

// TestRawReadmeURLs proves a GitLab repository resolves to the GitLab raw
// content path (HEAD branch first) — adr-tools regression.
func TestRawReadmeURLs(t *testing.T) {
	gl := rawReadmeURLs(mustParse(t, "https://gitlab.com/td7x/asdf/adr-tools.git"))
	if len(gl) == 0 || gl[0] != "https://gitlab.com/td7x/asdf/adr-tools/-/raw/HEAD/README.md" {
		t.Errorf("gitlab first candidate = %q", gl)
	}
	gh := rawReadmeURLs(mustParse(t, "https://github.com/looztra/asdf-k9s.git"))
	if len(gh) == 0 || gh[0] != "https://raw.githubusercontent.com/looztra/asdf-k9s/HEAD/README.md" {
		t.Errorf("github first candidate = %q", gh)
	}
	found := false
	for _, u := range gh {
		if strings.HasSuffix(u, "/README.adoc") {
			found = true
		}
	}
	if !found {
		t.Errorf("github candidates should include README.adoc: %v", gh)
	}
}

// TestCleanDescription proves the README-heading fallback strips badge images
// and keeps link text, so the catalog description is a plain one-liner
// (1password-cli regression: the heading came through with ![Build]/![Lint]).
func TestCleanDescription(t *testing.T) {
	heading := "asdf-1password-cli ![Build](https://github.com/NeoHsu/asdf-1password-cli/workflows/Build/badge.svg) ![Lint](https://github.com/NeoHsu/asdf-1password-cli/workflows/Lint/badge.svg)"
	got := cleanDescription(heading)
	if got != "asdf-1password-cli" {
		t.Errorf("cleanDescription: got %q, want %q", got, "asdf-1password-cli")
	}

	if s := cleanDescription("# [k9s](https://github.com/derailed/k9s) the **kubernetes** CLI"); s != "k9s the kubernetes CLI" {
		t.Errorf("cleanDescription links/bold: got %q", s)
	}
	if s := cleanDescription("![logo](https://x/logo.svg)"); s != "" {
		t.Errorf("cleanDescription image-only: got %q, want empty", s)
	}
}

// TestOwnerRepoStar proves a `*`-prefixed repository URL still parses to the
// right owner/name (k9s regression: `asdf plugin list all` emits
// "*https://github.com/looztra/asdf-k9s.git").
func TestOwnerRepoStar(t *testing.T) {
	o, n := ownerRepo("*https://github.com/looztra/asdf-k9s.git")
	if o != "looztra" || n != "asdf-k9s" {
		t.Errorf("ownerRepo(*…): got (%q,%q), want (looztra,asdf-k9s)", o, n)
	}
}

// TestVersionFilter proves live letter-search narrows the versions column and
// that Esc clears an active filter instead of leaving the column.
func TestVersionFilter(t *testing.T) {
	items := []string{"0.51.0", "0.50.18", "0.32.7", "9.0.1", "1.2.3"}
	m := model{verItems: items, verFilter: "", verSel: 0, verTop: 0, mode: verInstall, focus: focusVersions}

	f := m.filteredVersions()
	if len(f) != 5 {
		t.Fatalf("no filter: got %d versions, want 5", len(f))
	}

	m.verFilter = "0.5"
	f = m.filteredVersions()
	if len(f) != 2 || f[0] != "0.51.0" || f[1] != "0.50.18" {
		t.Fatalf("filter 0.5 should keep 0.51.0+0.50.18, got %v", f)
	}

	m.verFilter = "0.5"
	m.versionsKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.verFilter != "" {
		t.Fatalf("Esc should clear the filter, got %q", m.verFilter)
	}
	if m.focus != focusVersions {
		t.Fatalf("Esc with an active filter must stay in the column, focus=%d", m.focus)
	}
}

// TestTruncateCells proves titles are cut on rune boundaries so a 2-cell wide
// glyph (an emoji state icon) is never split in half by a cell-count limit.
func TestTruncateCells(t *testing.T) {
	if got := truncateCells("abc😀def", 4); got != "abc" {
		t.Errorf("limit 4 must drop the wide glyph entirely, got %q", got)
	}
	if got := truncateCells("abc😀def", 5); got != "abc😀" {
		t.Errorf("limit 5 must keep the full wide glyph, got %q", got)
	}
	if got := truncateCells("abc😀def", 6); got != "abc😀d" {
		t.Errorf("limit 6 continues after the wide glyph, got %q", got)
	}
	if got := truncateCells("abc", 10); got != "abc" {
		t.Errorf("no truncation needed: %q", got)
	}
	if got := truncateCells("abc", 0); got != "" {
		t.Errorf("zero limit: got %q", got)
	}
}

// TestToolDelegateRender proves the tools column keeps the selection offset
// identical ("› " vs "  " for normal rows) so names never jump, and that the
// selected row is padded to the full column width — the trailing icon stays on
// the highlighted background.
func TestToolDelegateRender(t *testing.T) {
	plugins := []Plugin{
		{Name: "create-secret", Repo: "https://github.com/x/create-secret.git", Archived: true},
		{Name: "plain", Repo: "https://github.com/x/plain.git"},
	}
	m := newModelCheck(plugins, "/tmp", true)
	m.tools.SetSize(30, 10)
	d := toolDelegate{}

	var sel, off strings.Builder
	d.Render(&sel, m.tools, 0, m.tools.Items()[0])
	d.Render(&off, m.tools, 1, m.tools.Items()[1])

	stripped := stripANSI(sel.String())
	if !strings.HasPrefix(stripped, "› create-secret 🔒") {
		t.Fatalf("selected row head wrong: %q", stripped)
	}
	if got := lipgloss.Width(stripped); got != 30 {
		t.Errorf("selected row must span the full width, got %d cells", got)
	}

	plain := stripANSI(off.String())
	if !strings.HasPrefix(plain, "  plain") {
		t.Fatalf("normal row head wrong: %q", plain)
	}
	if got := lipgloss.Width(plain); got != 30 {
		t.Errorf("normal row must span the full width, got %d", got)
	}
}

// TestProjectRepoDescriptionOffline proves the project-description collector
// is strictly best effort and never touches the network for URLs it cannot
// attribute to a supported forge.
func TestProjectRepoDescriptionOffline(t *testing.T) {
	if got := projectRepoDescription(""); got != "" {
		t.Errorf("empty project: got %q, want empty", got)
	}
	if got := projectRepoDescription("https://argocd-image-updater.readthedocs.io"); got != "" {
		t.Errorf("website-only project should have no forge API desc: got %q", got)
	}
	if got := projectRepoDescription("https://example.com/some/path"); got != "" {
		t.Errorf("unknown forge should yield no desc: got %q", got)
	}
}

// TestRenderToolInfoSeparatesDescs proves the middle-column block keeps the
// plugin description and the project description on distinct lines (the
// project one indented behind a "—" dash) so the two never merge, and that
// the block always occupies a fixed height (7 lines) so the actions below do
// not jump.
func TestRenderToolInfoSeparatesDescs(t *testing.T) {
	m := newModelCheck([]Plugin{{
		Name:        "adr",
		Desc:        "adr-tools plugin for the asdf version manager",
		Repo:        "https://github.com/td7x/asdf-adr.git",
		Project:     "https://github.com/npryce/adr-tools",
		ProjectDesc: "Architecture Decision Records (ADR) tooling",
	}}, "/tmp", true)

	block := strings.TrimSuffix(stripANSI(m.renderToolInfo(60)), "\n")
	lines := strings.Split(block, "\n")
	if len(lines) != 7 {
		t.Fatalf("info block must be a fixed 7 lines, got %d:\n%q", len(lines), block)
	}
	if lines[2] != "adr-tools plugin for the asdf version manager" {
		t.Errorf("line 3 should be the plugin description, got %q", lines[2])
	}
	if !strings.HasPrefix(lines[3], "plugin: ") {
		t.Errorf("line 4 should be the plugin repo link, got %q", lines[3])
	}
	if !strings.HasPrefix(lines[4], "  — Architecture Decision Records") {
		t.Errorf("line 5 should be the indented project description, got %q", lines[4])
	}
	if !strings.HasPrefix(lines[5], "project: https://github.com/npryce/adr-tools") {
		t.Errorf("line 6 should be the project link, got %q", lines[5])
	}
}

// TestPluginFilterModes proves each filter mode shows only the matching
// plugins, switching modes clears the type-to-search filter, and the cursor
// stays on the previously selected plugin when it survives the filter
// (otherwise it jumps to the first visible row). Mode switching goes through
// the same setFilter the modal uses.
func TestPluginFilterModes(t *testing.T) {
	plugins := []Plugin{
		{Name: "arch", Archived: true},
		{Name: "good"},
		{Name: "gone", Removed: true},
		{Name: "down", Unavailable: true},
	}
	m := newModelCheck(plugins, "/tmp", true)
	m.addedSet = map[string]bool{"arch": true, "good": true}

	names := func(mm model) []string {
		return pluginNames(mm.filteredPlugins())
	}

	out, _ := m.setFilter(filterAll)
	if m := out.(model); m.filter != filterAll || len(m.filteredPlugins()) != 4 {
		t.Fatalf("all must show 4 plugins, got filter=%d names=%v", m.filter, names(m))
	}

	out, _ = m.setFilter(filterAdded)
	if m := out.(model); m.filter != filterAdded || strings.Join(names(m), ",") != "arch,good" {
		t.Fatalf("added must show [arch good], got %v", names(m))
	}

	out, _ = m.setFilter(filterActive)
	if m := out.(model); m.filter != filterActive || strings.Join(names(m), ",") != "good" {
		t.Fatalf("active must show [good], got %v", names(m))
	}

	out, _ = m.setFilter(filterArchived)
	if m := out.(model); m.filter != filterArchived || strings.Join(names(m), ",") != "arch" {
		t.Fatalf("archived must show [arch], got %v", names(m))
	}

	out, _ = m.setFilter(filterRemoved)
	if m := out.(model); m.filter != filterRemoved || strings.Join(names(m), ",") != "gone" {
		t.Fatalf("removed must show [gone], got %v", names(m))
	}

	out, _ = m.setFilter(filterUnreachable)
	if m := out.(model); m.filter != filterUnreachable || strings.Join(names(m), ",") != "down" {
		t.Fatalf("unreachable must show [down], got %v", names(m))
	}

	// cursor jumps to the first visible row when the old selection is gone:
	// unreachable selects "down", switching to added drops it → first row.
	out, _ = m.setFilter(filterAdded)
	m2 := out.(model)
	if got := m2.tools.SelectedItem().(pluginItem).p.Name; got != "arch" {
		t.Fatalf("after filtering too far, first visible row should be selected, got %q", got)
	}

	// cursor is preserved when the selected plugin survives: move to "good",
	// then to archived drops it, back to added keeps "arch" selected.
	out, _ = m2.keyMsg(tea.KeyMsg{Type: tea.KeyDown})
	m3 := out.(model)
	if got := m3.tools.SelectedItem().(pluginItem).p.Name; got != "good" {
		t.Fatalf("expected cursor on good, got %q", got)
	}
	out, _ = m3.setFilter(filterArchived)
	m4 := out.(model)
	out, _ = m4.setFilter(filterAdded)
	m5 := out.(model)
	if got := m5.tools.SelectedItem().(pluginItem).p.Name; got != "arch" {
		t.Fatalf("cursor should stay on arch across archived→added, got %q", got)
	}
}

func pluginNames(ps []Plugin) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// TestFilterModal proves ctrl+f opens the chooser, ↑/↓ + Enter apply the
// highlighted mode, digits apply directly, and Esc/q cancel without changing
// the current filter.
func TestFilterModal(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "a"}}, "/tmp", true)

	out, _ := m.keyMsg(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = out.(model)
	if !m.filterOpen {
		t.Fatal("ctrl+f must open the filter modal")
	}
	if m.filterSel != int(m.filter) {
		t.Fatalf("cursor should start on the active filter (%d), got %d", m.filter, m.filterSel)
	}

	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(model)
	if m.filterSel != 1 {
		t.Fatalf("down should move to option 1 (added), got %d", m.filterSel)
	}
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(model)
	if m.filterOpen || m.filter != filterAdded {
		t.Fatalf("Enter should apply added; open=%v filter=%d", m.filterOpen, m.filter)
	}

	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = out.(model)
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = out.(model)
	if m.filterOpen || m.filter != filterActive {
		t.Fatalf("digit 3 should apply active; open=%v filter=%d", m.filterOpen, m.filter)
	}

	// esc cancels, keeping the filter
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = out.(model)
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(model)
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(model)
	if m.filterOpen || m.filter != filterActive {
		t.Fatalf("esc should cancel; open=%v filter=%d", m.filterOpen, m.filter)
	}

	// q cancels too (never quits from the modal)
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = out.(model)
	var quit tea.Cmd
	out, quit = m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = out.(model)
	if quit != nil {
		t.Fatal("q while modal is open must cancel, not quit")
	}
	if m.filterOpen {
		t.Fatal("q should close the modal")
	}
}

// TestOverlayBox proves the modal overlay replaces the exact centered cell
// window and pads the covered row to the full width (opaque background).
func TestOverlayBox(t *testing.T) {
	under := "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc"
	box := "XXXX"
	out := overlayBox(under, box, 10)
	lines := strings.Split(out, "\n")
	if lines[1] != "bbbXXXXbbb" {
		t.Errorf("middle row should have XXXX centered and opaque, got %q", lines[1])
	}
	if lines[0] != "aaaaaaaaaa" || lines[2] != "cccccccccc" {
		t.Errorf("rows outside the modal window must be untouched: %v", lines)
	}
}

// TestAuthHeaderForAPI proves that declared forge tokens are sent as the
// Authorization header (raising the API rate limit) and that non-declared or
// foreign hosts fall back to anonymous requests (no header).
func TestAuthHeaderForAPI(t *testing.T) {
	if h := authHeaderForAPI("https://api.github.com/repos/x/y"); h != "" {
		t.Fatalf("without a token the header must be empty, got %q", h)
	}
	if h := authHeaderForAPI("https://gitlab.com/api/v4/projects/x"); h != "" {
		t.Fatalf("without a token the header must be empty, got %q", h)
	}

	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	if h := authHeaderForAPI("https://api.github.com/repos/x/y"); h != "token ghp_secret" {
		t.Fatalf("github token header: got %q", h)
	}
	if h := authHeaderForAPI("https://gitlab.com/api/v4/projects/x"); h != "" {
		t.Fatalf("gitlab must not receive the github token, got %q", h)
	}
	// GITHUB_TOKEN wins over GH_TOKEN
	t.Setenv("GH_TOKEN", "ghp_other")
	if h := authHeaderForAPI("https://api.github.com/repos/x/y"); h != "token ghp_secret" {
		t.Fatalf("GITHUB_TOKEN must take precedence: got %q", h)
	}

	t.Setenv("GITLAB_TOKEN", "glpat_x")
	if h := authHeaderForAPI("https://gitlab.com/api/v4/projects/x"); h != "Bearer glpat_x" {
		t.Fatalf("gitlab token header: got %q", h)
	}
	if h := authHeaderForAPI("https://api.github.com/repos/x/y"); h != "token ghp_secret" {
		t.Fatalf("github must not receive the gitlab token, got %q", h)
	}
	// other forges stay anonymous even with tokens set
	if h := authHeaderForAPI("https://codeberg.org/api/v1/repos/x/y"); h != "" {
		t.Fatalf("codeberg must stay anonymous, got %q", h)
	}
}

// stripANSI removes SGR escape sequences so rendered row text can be asserted.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// TestAsdfInstalled proves the availability probe is honest: with no PATH the
// version manager is (obviously) not installed.
func TestAsdfInstalled(t *testing.T) {
	t.Setenv("PATH", "")
	if asdfInstalled() {
		t.Fatal("asdfInstalled must be false with an empty PATH")
	}
}

// TestWarnModal proves the startup warning opens when asdf is missing, other
// keys are swallowed while it is up, and Enter dismisses it (never quits).
func TestWarnModal(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "a"}}, "/tmp", false)
	if !m.warnOpen {
		t.Fatal("missing asdf must open the warning modal at startup")
	}
	if s := m.renderWarnModal(); !strings.Contains(s, "asdf") || !strings.Contains(s, "dismiss") {
		t.Fatalf("warning modal should explain the asdf dependency:\n%s", s)
	}

	// down must not move rows or close the modal while it is up
	out, _ := m.keyMsg(tea.KeyMsg{Type: tea.KeyDown})
	m2 := out.(model)
	if !m2.warnOpen {
		t.Fatalf("keys must be swallowed while the warning is up")
	}
	if m2.tools.Index() != m.tools.Index() {
		t.Fatalf("warning modal must not move the list cursor")
	}

	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if out.(model).warnOpen {
		t.Fatal("Enter must dismiss the warning modal")
	}

	// Esc dismisses too
	m3 := newModelCheck([]Plugin{{Name: "a"}}, "/tmp", false)
	out, _ = m3.keyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	if out.(model).warnOpen {
		t.Fatal("Esc must dismiss the warning modal")
	}
}

// TestNoWarnWhenAsdfPresent proves an asdf-backed machine never shows the
// startup warning.
func TestNoWarnWhenAsdfPresent(t *testing.T) {
	if m := newModelCheck([]Plugin{{Name: "a"}}, "/tmp", true); m.warnOpen {
		t.Fatal("present asdf must not open the warning modal")
	}
}
