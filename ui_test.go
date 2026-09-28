package main

import (
	"fmt"
	"os"
	"path/filepath"
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
		{Name: "ripgrep", AppDesc: "fast file search tool", Desc: "grep on steroids", Project: "", ProjectDesc: ""},
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

	// the name-only app description is searchable too
	if rankPI := indexesOf(rankPlugins(plugins, "fast file")); len(rankPI) != 1 || rankPI[0] != 3 {
		t.Errorf("app-desc search should match ripgrep only: %v", rankPI)
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

func TestToolListTitleShowsSearch(t *testing.T) {
	m := newModelCheck([]Plugin{
		{Name: "kubernetes"},
		{Name: "helm"},
	}, "/tmp", true)

	if got := m.toolListTitle(); got != "List" {
		t.Fatalf("unfiltered list title = %q, want %q", got, "List")
	}

	m.tools.SetFilterText("kubernetes")
	if got := m.toolListTitle(); got != "List · filter: kubernetes" {
		t.Fatalf("filtered list title = %q, want %q", got, "List · filter: kubernetes")
	}

	view := stripANSI(m.renderTools(40, 10))
	if !strings.Contains(view, "List · filter: kubernetes") {
		t.Fatalf("filtered list should show its query next to List:\n%s", view)
	}

	m.tools.ResetFilter()
	if got := m.toolListTitle(); got != "List" {
		t.Fatalf("cleared list title = %q, want %q", got, "List")
	}
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
// plugin description and the project description on distinct lines so the two
// never merge, that the app description follows the name, and that the block
// always occupies a fixed height (10 lines) so the actions below do not jump.
func TestRenderToolInfoSeparatesDescs(t *testing.T) {
	m := newModelCheck([]Plugin{{
		Name:        "adr",
		AppDesc:     "Manage architecture decision records",
		Desc:        "adr-tools plugin for the asdf version manager",
		Repo:        "https://github.com/td7x/asdf-adr.git",
		Project:     "https://github.com/npryce/adr-tools",
		ProjectDesc: "Architecture Decision Records (ADR) tooling",
	}}, "/tmp", true)

	block := strings.TrimSuffix(stripANSI(m.renderToolInfo(60)), "\n")
	lines := strings.Split(block, "\n")
	if len(lines) != 10 {
		t.Fatalf("info block must be a fixed 10 lines, got %d:\n%q", len(lines), block)
	}
	if lines[1] != "Manage architecture decision records" {
		t.Errorf("line 2 should be the app description, got %q", lines[1])
	}
	if lines[3] != "" {
		t.Errorf("line 4 should be blank after the status, got %q", lines[3])
	}
	if lines[4] != "adr-tools plugin for the asdf version manager" {
		t.Errorf("line 5 should be the plugin description, got %q", lines[4])
	}
	if !strings.HasPrefix(lines[5], "🔌 ") {
		t.Errorf("line 6 should be the plugin repo link with an icon, got %q", lines[5])
	}
	if lines[6] != "" {
		t.Errorf("line 7 should be blank before the project description, got %q", lines[6])
	}
	if lines[7] != "Architecture Decision Records (ADR) tooling" {
		t.Errorf("line 8 should be the project description, got %q", lines[7])
	}
	if !strings.HasPrefix(lines[8], "↗ https://github.com/npryce/adr-tools") {
		t.Errorf("line 9 should be the project link with an icon, got %q", lines[8])
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

// itemsToPlugins unwraps the list items back into catalog rows.
func itemsToPlugins(items []list.Item) []Plugin {
	out := make([]Plugin, 0, len(items))
	for _, it := range items {
		if pi, ok := it.(pluginItem); ok {
			out = append(out, pi.p)
		}
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

// TestRemoveKeyDuringSearch proves the 'r' shortcut never fires while the
// user is typing a search term (otherwise "terraform" would open the remove
// confirmation on the highlighted plugin); instead the letter lands in the
// filter input. Without an active filter, 'r' still opens the confirmation.
func TestRemoveKeyDuringSearch(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "elasticsearch"}, {Name: "terraform"}}, "/tmp", true)

	// active search "te…" → 'r' must extend the filter, not ask for removal
	out, _ := m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = out.(model)
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = out.(model)
	if got := m.tools.FilterInput.Value(); got != "te" {
		t.Fatalf("filter should be 'te', got %q", got)
	}
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = out.(model)
	if m.cf != nil {
		t.Fatal("'r' during a search must not open the remove confirmation")
	}
	if got := m.tools.FilterInput.Value(); got != "ter" {
		t.Fatalf("'r' should land in the filter input, got %q", got)
	}

	// no active filter → 'r' still opens the confirmation
	m2 := newModelCheck([]Plugin{{Name: "elasticsearch"}}, "/tmp", true)
	out, _ = m2.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m2 = out.(model)
	if m2.cf == nil || m2.cf.kind != confirmRemovePlugin {
		t.Fatal("'r' with an empty filter must open the remove confirmation")
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

// TestAsdfVersionUsesSet proves the version probe recognizes both the legacy
// "version: 0.16.2" output and the plain "0.20.2 (revision unknown)" output
// that 0.17+ (Homebrew) prints — asdfUsesSet must stay true on the latter,
// otherwise the app falls back to the removed list-all/global commands.
func TestAsdfVersionUsesSet(t *testing.T) {
	cases := map[string]bool{
		"version: 0.16.2":           true,
		"v0.16.2":                   true,
		"0.20.2 (revision unknown)": true,
		"0.15.1":                    false,
		"nonsense":                  false,
	}
	for out, want := range cases {
		if got := asdfVersionUsesSet(out); got != want {
			t.Errorf("asdfVersionUsesSet(%q) = %v, want %v", out, got, want)
		}
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

// asModel normalizes the two shapes tea hands back after Update (value or
// pointer receiver) so tests can inspect fields directly.
func asModel(t *testing.T, tm tea.Model) model {
	t.Helper()
	switch m := tm.(type) {
	case model:
		return m
	case *model:
		return *m
	}
	t.Fatalf("unexpected model type %T", tm)
	return model{}
}

// TestUpdateAvailable proves the update gate: a newer published tag prompts,
// an equal/older one does not, and local "dev" builds never nag.
func TestUpdateAvailable(t *testing.T) {
	if !updateAvailable("0.1.1", "0.2.0") {
		t.Error("0.2.0 must update over 0.1.1")
	}
	if !updateAvailable("0.1.1", "0.1.2") {
		t.Error("patch bump must update")
	}
	if updateAvailable("0.1.1", "0.1.1") {
		t.Error("equal version must not update")
	}
	if updateAvailable("0.2.0", "0.1.0") {
		t.Error("older published tag must not update")
	}
	if updateAvailable("dev", "0.2.0") {
		t.Error("dev builds must skip the check")
	}
	if updateAvailable("", "0.2.0") {
		t.Error("empty current version must skip the check")
	}
	if updateAvailable("0.1.1", "") {
		t.Error("empty latest version must skip the check")
	}
}

// TestUpdateModalKeys proves the self-update offer goes through the shared
// confirmation modal: y (or moving the highlight to Yes and pressing Enter)
// quits with the upgrade flag set — the quit cmd actually yields tea.QuitMsg —
// while n/Esc dismiss it without quitting, and the render announces both the
// published and the running version.
func TestUpdateModalKeys(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "a"}}, "/tmp", true)
	m.openConfirm(confirmSelfUpdate, "0.2.0", "")

	out, cmd := m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got := asModel(t, out)
	if !got.doUpdate || cmd == nil {
		t.Fatal("y must set doUpdate and return tea.Quit")
	}
	switch cmd().(type) {
	case tea.QuitMsg:
	default:
		t.Fatalf("y must return tea.Quit, got %T", cmd())
	}

	// arrows then Enter: Enter alone must not install (the highlight starts
	// on No), ← moves it to Yes
	m = newModelCheck([]Plugin{{Name: "a"}}, "/tmp", true)
	m.openConfirm(confirmSelfUpdate, "0.2.0", "")
	out, cmd = m.keyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if got := asModel(t, out); got.cf != nil || got.doUpdate || cmd != nil {
		t.Fatal("Enter on the default No button must only close the dialog")
	}
	m = newModelCheck([]Plugin{{Name: "a"}}, "/tmp", true)
	m.openConfirm(confirmSelfUpdate, "0.2.0", "")
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyLeft})
	m = asModel(t, out)
	out, cmd = m.keyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	got = asModel(t, out)
	if !got.doUpdate || got.cf != nil || cmd == nil {
		t.Fatal("← then Enter must run the confirmed update")
	}

	m = newModelCheck([]Plugin{{Name: "a"}}, "/tmp", true)
	m.openConfirm(confirmSelfUpdate, "0.2.0", "")
	out, cmd = m.keyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	got = asModel(t, out)
	if got.cf != nil || got.doUpdate || cmd != nil {
		t.Fatal("Esc must dismiss the prompt without quitting")
	}

	m = newModelCheck([]Plugin{{Name: "a"}}, "/tmp", true)
	m.openConfirm(confirmSelfUpdate, "0.2.0", "")
	s := stripANSI(m.renderConfirmModal())
	if !strings.Contains(s, "0.2.0") || !strings.Contains(s, version) {
		t.Fatalf("update modal should announce latest and running version:\n%s", s)
	}
}

// TestHeaderShowsVersion proves the program header carries the asdf-tui
// version next to the app name (stamped by the release workflow, "dev" for
// local builds).
func TestHeaderShowsVersion(t *testing.T) {
	old := version
	version = "1.2.3"
	defer func() { version = old }()

	m := newModelCheck([]Plugin{{Name: "a"}}, "/tmp", true)
	m.width, m.height = 120, 30
	s := stripANSI(m.View())
	if !strings.Contains(s, "asdf-tui 1.2.3") {
		t.Fatalf("header should show the program version next to the name:\n%s", s)
	}
}

// TestCurrentVersionNextToName proves the middle column shows the tool's
// currently-in-use version (the `*` line of `asdf list`) right after the
// tool name once the state probe has answered.
func TestCurrentVersionNextToName(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	m.state["nodejs"] = toolSt{added: true, versions: []string{"20.0.0", "18.0.0"}, current: "20.0.0", loaded: true}

	block := stripANSI(m.renderToolInfo(60))
	if got := strings.SplitN(block, "\n", 2)[0]; !strings.Contains(got, "nodejs") || !strings.Contains(got, "v20.0.0") {
		t.Fatalf("name line should show the current version, got %q", got)
	}

	// not loaded yet → no version text
	m.state["nodejs"] = toolSt{loaded: false}
	if got := stripANSI(m.renderToolInfo(60)); strings.Contains(got, "v20.0.0") {
		t.Fatalf("unloaded state must not show a version:\n%s", got)
	}
}

// TestParseAsdfCurrent proves the `asdf current` decoder keeps the tool names
// and drops the table header — the "added to asdf" set, which also covers
// custom plugins that never joined the asdf-plugins registry.
func TestParseAsdfCurrent(t *testing.T) {
	out := "Name            Version         Source                    Installed\n" +
		"aws-sso-cli     ______          ______                    \n" +
		"k9s             0.51.0          /home/u/.tool-versions     true\n" +
		"prek            ______          ______                    \n"

	got := parseAsdfCurrent(out)
	if strings.Join(got, ",") != "aws-sso-cli,k9s,prek" {
		t.Fatalf("asdf current should decode to the tool names, got %v", got)
	}
	if names := parseAsdfCurrent(""); len(names) != 0 {
		t.Fatalf("empty output must decode to nothing, got %v", names)
	}
}

// TestHeaderCountsCatalog proves the tools-column header carries how many
// plugins the column holds and how many of them are custom (added straight
// from a repo URL, hence absent from the asdf-plugins registry).
func TestHeaderCountsCatalog(t *testing.T) {
	plugins := []Plugin{
		{Name: "k9s"},
		{Name: "kubectl"},
		{Name: "nodejs"},
		{Name: "prek", Repo: "https://github.com/a4z/asdf-prek.git", Custom: true},
	}
	m := newModelCheck(plugins, "/tmp", true)
	m.addedSet = map[string]bool{"k9s": true, "prek": true}

	if got := m.headerHints(); got != "tools — 4 plugins (1 custom) · type to search" {
		t.Fatalf("header should count the catalog and its custom rows, got %q", got)
	}

	// a subset reports how many rows it leaves of the total
	m.filter = filterAdded
	if got := m.headerHints(); got != "tools — added · 2 of 4 (1 custom) · type to search" {
		t.Fatalf("filtered header should count rows of the total, got %q", got)
	}

	// the custom plugin belongs to every subset it qualifies for
	if got := strings.Join(pluginNames(m.filteredPlugins()), ","); got != "k9s,prek" {
		t.Fatalf("added filter should list the custom plugin, got %v", got)
	}
	m.filter = filterActive
	if got := strings.Join(pluginNames(m.filteredPlugins()), ","); got != "k9s,kubectl,nodejs,prek" {
		t.Fatalf("active filter should list the custom plugin, got %v", got)
	}
}

// TestParseInstalledVersions proves the `asdf list` decoder separates
// installed versions from the current one (the `*`-marked line), tolerates
// "(set by …)" annotations and skips the "No versions installed" message.
func TestParseInstalledVersions(t *testing.T) {
	vs, cur := parseInstalledVersions("  18.0.0\n *20.0.0\n")
	if len(vs) != 2 || vs[0] != "18.0.0" || vs[1] != "20.0.0" || cur != "20.0.0" {
		t.Fatalf("basic: got %v current=%q", vs, cur)
	}

	vs, cur = parseInstalledVersions(" *20.0.0   (set by /home/u/.tool-versions)\n  18.0.0 (set by /p/.tool-versions)\n")
	if len(vs) != 2 || vs[0] != "20.0.0" || vs[1] != "18.0.0" || cur != "20.0.0" {
		t.Fatalf("annotated: got %v current=%q", vs, cur)
	}

	vs, cur = parseInstalledVersions("No versions installed\n")
	if len(vs) != 0 || cur != "" {
		t.Fatalf("empty: got %v current=%q", vs, cur)
	}

	vs, cur = parseInstalledVersions("")
	if len(vs) != 0 || cur != "" {
		t.Fatalf("blank: got %v current=%q", vs, cur)
	}
}

// TestUninstallFlow proves the fifth action ("Uninstall a version…") opens
// the right column on the installed versions, Enter on a row arms the shared
// confirmation modal over the columns, n/Esc cancel without leaving, and y
// schedules the `asdf uninstall` task.
func TestUninstallFlow(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	m.state["nodejs"] = toolSt{added: true, versions: []string{"20.0.0", "18.0.0"}, current: "20.0.0", loaded: true}

	out, _ := m.runAction(4)
	m = asModel(t, out)
	if m.mode != verUninstall || m.focus != focusVersions {
		t.Fatalf("uninstall action should open the version list, mode=%d focus=%d", m.mode, m.focus)
	}
	if len(m.verItems) != 2 {
		t.Fatalf("version list should carry the installed versions, got %v", m.verItems)
	}

	// Enter on the first (newest) version arms the confirmation
	out, _ = m.versionsKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(t, out)
	if m.cf == nil || m.cf.kind != confirmUninstallVersion || m.cf.arg != "20.0.0" {
		t.Fatalf("Enter should arm the confirmation on 20.0.0, got %+v", m.cf)
	}
	if m.cf.sel != btnNo {
		t.Fatal("the confirmation must start on No so Enter cannot destroy data")
	}
	if s := stripANSI(m.renderConfirmModal()); !strings.Contains(s, "Uninstall nodejs 20.0.0?") {
		t.Fatalf("confirm modal should ask about the version:\n%s", s)
	}

	// n cancels and stays in the list
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = asModel(t, out)
	if m.cf != nil || m.mode != verUninstall {
		t.Fatalf("n should cancel the confirmation, got %+v mode=%d", m.cf, m.mode)
	}

	// re-arm and y runs the task (asdf is absent in tests → the task errors)
	out, _ = m.versionsKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(t, out)
	conf, cmd := m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = asModel(t, conf)
	if m.cf != nil {
		t.Fatal("confirming must close the modal")
	}
	if !m.busy || cmd == nil {
		t.Fatalf("y should mark busy and schedule the uninstall, busy=%v", m.busy)
	}
	msg, ok := cmd().(taskDoneMsg)
	if !ok || !strings.HasPrefix(msg.label, "Uninstall nodejs 20.0.0") {
		t.Fatalf("task label should name the tool and version, got %+v", msg)
	}
}

// TestConfirmModalArrows proves the answer can be picked with ←/→: the
// highlight moves between the two buttons, and only Enter on Yes runs the
// action — everywhere in the dialog, not just on the letter keys.
func TestConfirmModalArrows(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	m.state["nodejs"] = toolSt{added: true, versions: []string{"20.0.0"}, loaded: true}
	m.openConfirm(confirmRemovePlugin, "nodejs", "")
	if m.cf.sel != btnNo {
		t.Fatal("the dialog must open on No")
	}

	out, cmd := m.keyMsg(tea.KeyMsg{Type: tea.KeyLeft})
	m = asModel(t, out)
	if m.cf.sel != btnYes || cmd != nil {
		t.Fatalf("← must select Yes without running, sel=%d cmd=%v", m.cf.sel, cmd)
	}
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyRight})
	m = asModel(t, out)
	if m.cf.sel != btnNo {
		t.Fatal("→ must move back to No")
	}
	// h/l mirror ←/→
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = asModel(t, out)
	if m.cf.sel != btnYes {
		t.Fatal("h must select Yes")
	}
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = asModel(t, out)
	if m.cf.sel != btnNo {
		t.Fatal("l must select No")
	}
	// tab cycles between the two buttons
	for _, k := range []tea.KeyMsg{{Type: tea.KeyTab}, {Type: tea.KeyShiftTab}} {
		out, _ = m.keyMsg(k)
		m = asModel(t, out)
		if m.cf == nil || m.cf.sel != btnYes {
			t.Fatalf("%v must select Yes", k)
		}
		out, _ = m.keyMsg(k)
		m = asModel(t, out)
		if m.cf == nil || m.cf.sel != btnNo {
			t.Fatalf("%v must select No", k)
		}
	}

	// Enter on No just closes the dialog, nothing is run
	out, cmd = m.keyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(t, out)
	if m.cf != nil || m.busy || cmd != nil {
		t.Fatal("Enter on No must only close the dialog")
	}

	// ← then Enter on Yes runs the removal (asdf is absent in tests → error)
	m.openConfirm(confirmRemovePlugin, "nodejs", "")
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyLeft})
	m = asModel(t, out)
	out, cmd = m.keyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(t, out)
	if !m.busy || cmd == nil {
		t.Fatalf("← then Enter must run the removal, busy=%v", m.busy)
	}
	if msg, ok := cmd().(taskDoneMsg); !ok || !strings.HasPrefix(msg.label, "Remove nodejs") {
		t.Fatalf("task label should name the plugin, got %+v", msg)
	}
}

// TestConfirmModalCoversScreen proves every confirmation is a centered modal
// over the whole TUI (not an inline line in a column) and that the columns
// keep their own content underneath it.
func TestConfirmModalCoversScreen(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	m.width, m.height = 120, 30
	m.openConfirm(confirmRemovePlugin, "nodejs", "")

	screen := stripANSI(m.View())
	if !strings.Contains(screen, "Remove nodejs?") {
		t.Fatalf("the dialog should be drawn over the screen:\n%s", screen)
	}
	// the columns keep their own content underneath the dialog
	if !strings.Contains(screen, "nodejs") || !strings.Contains(screen, "Remove plugin") {
		t.Fatalf("columns should stay visible under the modal:\n%s", screen)
	}
	// …and the prompt is not rendered inline in the middle column anymore
	if strings.Contains(m.renderActions(30, 20), "yes") {
		t.Fatalf("the action column must not hold an inline y/n prompt:\n%s", m.renderActions(30, 20))
	}
}

// TestOverlayLineKeepsColouredRowsAligned guards the dialog splice against
// counting colour escapes as cells. When it did, the dialog was placed off
// centre and the row ended up wider than the terminal, which wrapped it and
// shifted every line below — the visible symptom of a broken layout.
func TestOverlayLineKeepsColouredRowsAligned(t *testing.T) {
	const width = 80
	// 1 + 30 + 20 + 2 (the lock) + 27 = 80 cells, with a colour escape in front.
	base := "\x1b[38;5;140m│" + strings.Repeat("a", 30) + "\x1b[0m" +
		strings.Repeat("b", 20) + "🔒" + strings.Repeat("c", 27)
	box := "\x1b[38;5;86m│ help  │\x1b[0m"
	const left = 30

	got := stripANSI(overlayLine(base, box, left, width))
	want := "│" + strings.Repeat("a", left-1) + "│ help  │" +
		strings.Repeat("b", 12) + "🔒" + strings.Repeat("c", 27)
	if got != want {
		t.Fatalf("dialog spliced at the wrong cell:\n got %q\nwant %q", got, want)
	}
	if w := lipgloss.Width(overlayLine(base, box, left, width)); w != width {
		t.Fatalf("row is %d cells wide, terminal is %d", w, width)
	}
	// a dialog wider than the room left is clipped, never wrapped
	narrow := overlayLine(base, box, left, width-10)
	if w := lipgloss.Width(narrow); w != width-10 {
		t.Fatalf("clipped row is %d cells wide, terminal is %d", w, width-10)
	}
}

// TestModalScreensFitTheWidth proves no dialog — at any size, with any number
// of plugins — pushes a row past the terminal width, which is what made the
// columns jump sideways when a dialog was open.
func TestModalScreensFitTheWidth(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}, {209, 37}} {
		w, h := size[0], size[1]
		m := newModelCheck([]Plugin{
			{Name: "nodejs"},
			{Name: "prek", Repo: "https://github.com/a4z/asdf-prek.git", Custom: true},
			{Name: "terraform"},
		}, "/tmp", true)
		m.width, m.height = w, h
		m.addedSet["nodejs"] = true
		open := map[string]func(model) model{
			"help":   func(m model) model { m.helpOpen = true; return m },
			"add":    func(m model) model { m.addOpen, m.add = true, newAddForm(); return m },
			"filter": func(m model) model { m.filterOpen = true; return m },
			"warn":   func(m model) model { m.warnOpen = true; return m },
			"ask":    func(m model) model { m.openConfirm(confirmRemovePlugin, "nodejs", ""); return m },
		}
		for name, show := range open {
			mm := show(m)
			for i, line := range strings.Split(mm.View(), "\n") {
				if got := lipgloss.Width(line); got > w {
					t.Errorf("%s dialog at %dx%d: row %d is %d cells wide", name, w, h, i, got)
				}
			}
		}
	}
}

// TestUninstallSuccessExitsList proves a successful uninstall task closes the
// version list back to the actions column and clears the confirmation.
func TestUninstallSuccessExitsList(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	m.state["nodejs"] = toolSt{added: true, versions: []string{"20.0.0", "18.0.0"}, loaded: true}
	m.mode, m.focus = verUninstall, focusVersions
	m.openConfirm(confirmUninstallVersion, "nodejs 18.0.0", "18.0.0")

	out, _ := m.Update(taskDoneMsg{label: "Uninstall nodejs 18.0.0"})
	m = asModel(t, out)
	if m.mode != verNone || m.focus != focusActions || m.cf != nil {
		t.Fatalf("successful uninstall should exit the list, mode=%d focus=%d cf=%+v", m.mode, m.focus, m.cf)
	}
	if m.errMsg != "" || !strings.Contains(m.statusMsg, "done") {
		t.Fatalf("success should report done, err=%q status=%q", m.errMsg, m.statusMsg)
	}
}

// typeRunes feeds printable characters into the model the way the terminal
// delivers them.
func typeRunes(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		out, _ := m.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = asModel(t, out)
	}
	return m
}

// TestHelpModal proves the "?" dialog opens from every column, lists the
// catalog counters and the key map, swallows keys while it is up, and never
// covers another modal.
func TestHelpModal(t *testing.T) {
	m := newModelCheck([]Plugin{
		{Name: "nodejs"},
		{Name: "prek", Repo: "https://github.com/a4z/asdf-prek.git", Custom: true},
	}, "/tmp", true)
	m.width, m.height = 120, 30
	m.addedSet["nodejs"] = true

	box := stripANSI(m.renderHelpModal())
	for _, want := range []string{
		"asdf-tui",               // about
		"2 plugins",              // statistics
		"1 installed in asdf",    // statistics
		"1 available",            // statistics
		"1 custom",               // statistics
		"ctrl+p", "add a plugin", // key map
		"ctrl+f", "filter the catalog", // key map
		"remove the plugin",    // key map
		"←/→", "pick Yes / No", // key map
	} {
		if !strings.Contains(box, want) {
			t.Errorf("help dialog should mention %q:\n%s", want, box)
		}
	}

	// opens from every column
	for _, f := range []focus{focusTools, focusActions, focusVersions} {
		m2 := m
		m2.focus = f
		if f == focusVersions {
			m2.mode = verInstall
		}
		out, _ := m2.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
		m2 = asModel(t, out)
		if !m2.helpOpen {
			t.Fatalf("? must open the help dialog from focus %d", f)
		}
		if got := m2.activeModal(); !strings.Contains(stripANSI(got), "column TUI") {
			t.Fatalf("the help dialog should be the drawn modal, got %q", stripANSI(got))
		}
		// keys it does not use are swallowed, they must not reach the columns
		out, cmd := m2.keyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m2 = asModel(t, out)
		if !m2.helpOpen || cmd != nil || m2.selAct != 0 {
			t.Fatalf("an open help dialog must swallow keys, helpOpen=%v selAct=%d", m2.helpOpen, m2.selAct)
		}
		// …and Esc/?/q close it without quitting
		for _, k := range []tea.KeyMsg{
			{Type: tea.KeyRunes, Runes: []rune{'?'}},
			{Type: tea.KeyRunes, Runes: []rune{'q'}},
			{Type: tea.KeyEsc},
		} {
			m2.helpOpen = true
			out, cmd := m2.keyMsg(k)
			m2 = asModel(t, out)
			if m2.helpOpen || cmd != nil {
				t.Fatalf("%v must close the help dialog and nothing else", k)
			}
		}
	}
}

// TestHelpKeyIsTypedIntoSearch proves "?" is a search character while a filter
// is being typed — the help dialog never eats a keystroke of a search.
func TestHelpKeyIsTypedIntoSearch(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "helm"}}, "/tmp", true)
	m = typeRunes(t, m, "a?")
	if m.helpOpen {
		t.Fatal("? must not open the help dialog while searching")
	}
	if got := m.tools.FilterInput.Value(); got != "a?" {
		t.Fatalf("? should land in the filter input, got %q", got)
	}

	// the versions-column filter behaves the same
	m2 := newModelCheck([]Plugin{{Name: "helm"}}, "/tmp", true)
	m2.focus, m2.mode = focusVersions, verInstall
	m2 = typeRunes(t, m2, "1?")
	if m2.helpOpen {
		t.Fatal("? must not open the help dialog while filtering versions")
	}
	if m2.verFilter != "1?" {
		t.Fatalf("? should land in the version filter, got %q", m2.verFilter)
	}
}

// TestHelpModalNeverCoversAnotherModal proves the precedence: warning → filter
// chooser → confirmation → add form → help.
func TestHelpModalNeverCoversAnotherModal(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	m.width, m.height = 120, 30
	m.helpOpen = true

	m.filterOpen = true
	if got := stripANSI(m.activeModal()); !strings.Contains(got, "pick") {
		t.Fatalf("the filter chooser outranks help:\n%s", got)
	}
	m.filterOpen = false

	m.openConfirm(confirmRemovePlugin, "nodejs", "")
	if got := stripANSI(m.activeModal()); !strings.Contains(got, "Remove nodejs?") {
		t.Fatalf("a confirmation outranks help:\n%s", got)
	}
	m.cf = nil

	m.addOpen, m.add = true, newAddForm()
	if got := stripANSI(m.activeModal()); !strings.Contains(got, "add plugin") {
		t.Fatalf("the add form outranks help:\n%s", got)
	}
	m.addOpen = false

	if got := stripANSI(m.activeModal()); !strings.Contains(got, "column TUI") {
		t.Fatalf("help is drawn when nothing else is open:\n%s", got)
	}
}

// TestAddPluginForm proves the ctrl+p form: two inputs, Tab switches fields,
// Enter validates the name and then runs `asdf plugin add`, Esc cancels.
func TestAddPluginForm(t *testing.T) {
	m := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	m.width, m.height = 120, 30

	out, cmd := m.keyMsg(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = asModel(t, out)
	if !m.addOpen || cmd == nil {
		t.Fatal("ctrl+p must open the add form and start the cursor blink")
	}
	if !m.add.name.Focused() || m.add.repo.Focused() {
		t.Fatal("the name field must take the focus first")
	}
	if got := stripANSI(m.View()); strings.Count(got, "add plugin") == 0 {
		t.Fatalf("the form must be drawn as a modal:\n%s", got)
	}

	// typing lands in the name field
	m = typeRunes(t, m, "prek")
	if got := m.add.name.Value(); got != "prek" {
		t.Fatalf("name field should hold %q, got %q", "prek", got)
	}

	// Tab moves to the repo field, then back
	out, cmd = m.keyMsg(tea.KeyMsg{Type: tea.KeyTab})
	m = asModel(t, out)
	if m.add.field != 1 || cmd == nil || !m.add.repo.Focused() {
		t.Fatalf("Tab must focus the repo field, field=%d", m.add.field)
	}
	m = typeRunes(t, m, "https://github.com/a4z/asdf-prek.git")
	if got := m.add.repo.Value(); got != "https://github.com/a4z/asdf-prek.git" {
		t.Fatalf("repo field should hold the URL, got %q", got)
	}
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = asModel(t, out)
	if m.add.field != 0 {
		t.Fatalf("shift+Tab must go back to the name field, field=%d", m.add.field)
	}

	// Enter runs the add (the command itself is not executed: it would shell
	// out to asdf and really add a plugin)
	out, cmd = m.keyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(t, out)
	if m.addOpen || !m.busy || cmd == nil {
		t.Fatalf("Enter must close the form and run the add, busy=%v cmd=%v", m.busy, cmd != nil)
	}
	if !strings.Contains(m.busyLabel, "prek") {
		t.Fatalf("the busy label should name the plugin, got %q", m.busyLabel)
	}

	// Esc cancels…
	out, _ = m.keyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	if asModel(t, out).addOpen {
		t.Fatal("Esc must close the form")
	}
	// …and an empty name is refused in place
	m2 := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	out, _ = m2.keyMsg(tea.KeyMsg{Type: tea.KeyCtrlP})
	m2 = asModel(t, out)
	out, cmd = m2.keyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m2 = asModel(t, out)
	if !m2.addOpen || m2.busy || cmd != nil {
		t.Fatalf("an empty name must be refused, addOpen=%v busy=%v", m2.addOpen, m2.busy)
	}
	if m2.add.err == "" {
		t.Fatal("an empty name must be reported in the form")
	}
}

// TestAddPluginReachesCatalog proves a successful add inserts the row into the
// catalog slice, the list items and the YAML, keeping the name sorted.
func TestAddPluginReachesCatalog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.yaml")

	m := newModelCheck([]Plugin{{Name: "nodejs"}, {Name: "terraform"}}, "/tmp", true)
	m.catalogFile = path
	if err := m.mergePlugin(Plugin{Name: "prek", Repo: "https://github.com/a4z/asdf-prek.git", Custom: true}); err != nil {
		t.Fatalf("mergePlugin: %v", err)
	}
	if got := strings.Join(pluginNames(m.plugins), ","); got != "nodejs,prek,terraform" {
		t.Fatalf("the catalog slice should be sorted with the new row, got %v", got)
	}
	items := pluginNames(itemsToPlugins(m.tools.Items()))
	if !strings.Contains(strings.Join(items, ","), "prek") {
		t.Fatalf("the list should show the new plugin, got %v", items)
	}
	if p := m.selected(); p == nil || p.Name != "prek" {
		t.Fatalf("the new plugin should be selected, got %+v", p)
	}
	saved, err := loadCatalog(path)
	if err != nil {
		t.Fatalf("loadCatalog: %v", err)
	}
	if got := strings.Join(pluginNames(saved), ","); got != "nodejs,prek,terraform" {
		t.Fatalf("the new plugin should be persisted, got %v", got)
	}
	if saved[1].Repo != "https://github.com/a4z/asdf-prek.git" || !saved[1].Custom {
		t.Fatalf("the saved row should keep its repo and custom flag, got %+v", saved[1])
	}
}

// TestAddPluginKeepsCatalogRow proves adding a plugin that is already listed
// only refreshes its repo URL and custom flag — the catalog description and
// flags stay.
func TestAddPluginKeepsCatalogRow(t *testing.T) {
	m := newModelCheck([]Plugin{
		{Name: "prek", Desc: "pre-commit hooks", Project: "prek", Archived: true},
	}, "/tmp", true)
	m.catalogFile = filepath.Join(t.TempDir(), "plugins.yaml")

	if err := m.mergePlugin(Plugin{Name: "prek", Repo: "https://github.com/a4z/asdf-prek.git", Custom: true}); err != nil {
		t.Fatalf("mergePlugin: %v", err)
	}
	if len(m.plugins) != 1 {
		t.Fatalf("no duplicate row should be added, got %v", pluginNames(m.plugins))
	}
	p := m.plugins[0]
	if p.Desc != "pre-commit hooks" || !p.Archived || !p.Custom || p.Repo == "" {
		t.Fatalf("the catalog row should be kept and enriched, got %+v", p)
	}
}

// TestFilterLabelsAreShort proves the filter chooser labels stay one word
// each — no "not archived/removed/unreachable" parenthesis.
func TestFilterLabelsAreShort(t *testing.T) {
	for _, o := range filterOptions {
		if strings.Contains(o.label, "(") {
			t.Errorf("filter label %q should not carry an explanation", o.label)
		}
	}
	m := newModelCheck([]Plugin{{Name: "nodejs"}}, "/tmp", true)
	m.filter = filterActive
	if got := m.filterLabel(); strings.Contains(got, "(") {
		t.Errorf("the header filter label should stay short, got %q", got)
	}
}

// TestAddPluginResolvesRow drives the whole "add a plugin" background command
// against a stub `asdf` on PATH: the row it reports back must carry the repo URL
// asdf recorded and the custom flag when the name is outside asdf's registry.
func TestAddPluginResolvesRow(t *testing.T) {
	stub := writeStubAsdf(t, `
if [ "$1" = "plugin" ] && [ "$2" = "list" ]; then
  case "$3" in
  --urls)
    echo "my-tool  https://github.com/acme/asdf-demo.git"
    echo "nodejs  https://github.com/nodejs/asdf-nodejs.git"
    ;;
  *) echo "nodejs" ;;
  esac
  exit 0
fi
if [ "$1" = "plugin" ] && [ "$2" = "add" ]; then
  case "$3" in
  nodejs|my-tool) echo added; exit 0 ;;
  *) echo "unknown plugin: $3" >&2; exit 1 ;;
  esac
fi
`)
	defer stub()

	// a name asdf's registry knows → not custom, repo taken from `plugin list --urls`
	msg := addPluginCmd("nodejs", "")().(addPluginMsg)
	if msg.err != nil {
		t.Fatalf("add: %v", msg.err)
	}
	if msg.p.Custom {
		t.Errorf("a registry plugin must not be flagged custom: %+v", msg.p)
	}
	if msg.p.Repo != "https://github.com/nodejs/asdf-nodejs.git" {
		t.Errorf("repo should come from asdf, got %q", msg.p.Repo)
	}

	// a name only asdf can resolve from the URL → custom
	msg = addPluginCmd("my-tool", "https://github.com/acme/asdf-demo.git")().(addPluginMsg)
	if msg.err != nil {
		t.Fatalf("add: %v", msg.err)
	}
	if !msg.p.Custom || msg.p.Repo != "https://github.com/acme/asdf-demo.git" {
		t.Errorf("a repo-added plugin should be custom with its URL, got %+v", msg.p)
	}

	// a failing add is reported, not swallowed
	msg = addPluginCmd("nope", "")().(addPluginMsg)
	if msg.err == nil {
		t.Errorf("a rejected add must report the error, got %+v", msg.p)
	}
}

// writeStubAsdf puts a fake `asdf` first on PATH for the duration of the test
// and returns the cleanup func.
func writeStubAsdf(t *testing.T, body string) func() {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "asdf"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	old := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+old)
	return func() { os.Setenv("PATH", old) }
}
