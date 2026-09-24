package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type focus int

const (
	focusTools focus = iota
	focusActions
	focusVersions
)

type verMode int

const (
	verNone verMode = iota
	verInstall
	verSet
	verScope
	verUninstall
)

// pluginFilter narrows the tools column to a subset of the catalog, switched
// through the ctrl+f modal; the type-to-search fuzzy filter still applies on
// top of whichever subset is active.
type pluginFilter int

const (
	filterAll         pluginFilter = iota // every plugin in the catalog
	filterAdded                           // already added to asdf
	filterActive                          // working: not archived/removed/unreachable
	filterArchived                        // 🔒
	filterRemoved                         // 🗑
	filterUnreachable                     // 🚫
)

// filterOptions is the modal's menu list; its order matches the pluginFilter
// enum values 0..5, so filterSel doubles as the mode index.
var filterOptions = []struct {
	mode  pluginFilter
	label string
}{
	{filterAll, "all plugins"},
	{filterAdded, "added to asdf"},
	{filterActive, "active (not archived/removed/unreachable)"},
	{filterArchived, "archived"},
	{filterRemoved, "removed"},
	{filterUnreachable, "unreachable"},
}

type toolSt struct {
	added    bool
	versions []string
	// current is the version of the tool currently in use, as marked by
	// `asdf list` (its `*` line); "" when none is set.
	current string
	loaded  bool
}

type pluginItem struct {
	p Plugin
}

func (i pluginItem) Title() string {
	return i.p.Name + pluginIcon(i.p)
}
func (i pluginItem) Description() string { return i.p.Desc }
func (i pluginItem) FilterValue() string {
	return i.p.Name + " " + i.p.Project + " " + i.p.Desc + " " + i.p.ProjectDesc
}

// toolDelegate renders the tools column rows directly instead of the bubbles
// DefaultDelegate's styles. It keeps the same geometry (1 line per row, one
// blank line between rows) but guarantees the selection highlight spans the
// full column width — so the trailing state icon sits on the highlighted
// background — and long titles are cut on rune boundaries, never splitting a
// 2-cell glyph in half.
type toolDelegate struct{}

func (d toolDelegate) Height() int { return 1 }
func (d toolDelegate) Spacing() int {
	return 1
}
func (d toolDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d toolDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	pi, ok := item.(pluginItem)
	if !ok {
		return
	}
	width := m.Width()
	if width < 2 {
		width = 2
	}
	title := truncateCells(pi.Title(), width-2)
	switch {
	case m.FilterState() == list.Filtering:
		// dimmed while the fuzzy filter is being typed
		fmt.Fprint(w, styleDim.Render(pad("  "+title, width)))
	case index == m.Index():
		// "› " mirrors the unselected "  " offset so the name never jumps
		fmt.Fprint(w, styleHighlight.Render(pad("› "+title, width)))
	default:
		fmt.Fprint(w, pad("  "+title, width))
	}
}

// truncateCells trims s so it fits within limit terminal cells, refusing to
// cut a 2-cell rune in half (a truncated emoji would render as a half-width
// fragment).
func truncateCells(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		c := 1
		if r > 0xff {
			c = 2
		}
		if n+c > limit {
			break
		}
		b.WriteRune(r)
		n += c
	}
	return b.String()
}

// pluginIcon draws the state icon shown after a plugin's name in the TUI:
// 🔒 archived (read-only), 🗑 removed from the catalog, 🚫 unreachable.
func pluginIcon(p Plugin) string {
	switch {
	case p.Archived:
		return " 🔒"
	case p.Removed:
		return " 🗑"
	case p.Unavailable:
		return " 🚫"
	}
	return ""
}

type statusMsg struct {
	name string
	st   toolSt
}

type listDoneMsg struct {
	mode     verMode
	versions []string
	err      error
}

type taskDoneMsg struct {
	label string
	out   string
	err   error
}

// pluginRefreshMsg reports the outcome of a single-plugin "Refresh" action.
type pluginRefreshMsg struct {
	p   Plugin // refreshed data (only valid when err == nil)
	err error
}

type refreshMsg struct{}

type model struct {
	plugins   []Plugin
	tools     list.Model
	state     map[string]toolSt
	addedSet  map[string]bool
	focus     focus
	selAct    int
	selScope  int
	mode      verMode
	verItems  []string
	verFilter string
	verSel    int
	verTop    int
	pickVer   string
	confirmRm bool
	// confirmUninstall holds the installed version awaiting a y/n before the
	// "Uninstall a version…" action removes it (version list, right column).
	confirmUninstall string
	busy             bool
	busyLabel        string
	spinner          spinner.Model
	statusMsg        string
	errMsg           string
	width            int
	height           int
	lastSel          string
	rootDir          string
	filter           pluginFilter
	filterOpen       bool
	filterSel        int
	// warnOpen shows the startup warning modal when the asdf version manager
	// itself is missing — the tool depends on it for every action.
	warnOpen bool
	// updateOpen shows the "new version available" modal at startup;
	// doUpdate records a confirmed upgrade so runTUI can hand the terminal
	// to the installer.
	updateOpen   bool
	updateLatest string
	doUpdate     bool
}

var (
	styleHighlight = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("86"))
	styleDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleErr       = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleOk        = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleBrand     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
)

func fuzzyMatch(hay, pat string) bool {
	if pat == "" {
		return true
	}
	hay = strings.ToLower(hay)
	pat = strings.ToLower(pat)
	j := 0
	for i := 0; i < len(hay) && j < len(pat); i++ {
		if hay[i] == pat[j] {
			j++
		}
	}
	return j == len(pat)
}

func newModel(plugins []Plugin, rootDir string) model {
	return newModelCheck(plugins, rootDir, asdfInstalled())
}

// newModelCheck builds the TUI model with an explicit asdf availability so
// tests are deterministic regardless of whether asdf happens to be installed
// on the machine running the suite.
func newModelCheck(plugins []Plugin, rootDir string, asdfOK bool) model {
	// toolDelegate: the selection highlight spans the whole row (so the icon
	// after the name is on the highlighted background) and long names are
	// truncated without splitting wide glyphs.
	delegate := toolDelegate{}

	items := make([]list.Item, len(plugins))
	for i, p := range plugins {
		items[i] = pluginItem{p: p}
	}

	s := spinner.New()
	s.Spinner = spinner.Dot

	m := model{
		plugins:  plugins,
		rootDir:  rootDir,
		state:    map[string]toolSt{},
		addedSet: map[string]bool{},
		spinner:  s,
		lastSel:  "",
		warnOpen: !asdfOK,
	}
	m.tools = list.New(items, delegate, 30, 20)
	m.tools.SetShowStatusBar(false)
	m.tools.SetShowHelp(false)
	m.tools.SetFilteringEnabled(true)
	m.tools.Styles.FilterPrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	return m
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, func() tea.Msg { return refreshMsg{} }}
	// Only stamped releases get the update check; "dev" builds have no semver
	// to compare against, so they never hit the network.
	if version != "dev" {
		cmds = append(cmds, checkUpdateCmd())
	}
	return tea.Batch(cmds...)
}

func stCmd(name string) tea.Cmd {
	return func() tea.Msg {
		st := toolSt{}
		if asdfIsAdded(name) {
			st.added = true
			st.versions, st.current = toolInstalledVersions(name)
		}
		st.loaded = true
		return statusMsg{name: name, st: st}
	}
}

func versionsCmd(name, repo string, mode verMode) tea.Cmd {
	return func() tea.Msg {
		// `asdf list all` only works for an added plugin, so add it first
		// when needed — otherwise the versions column can never open.
		if !asdfIsAdded(name) {
			if err := asdfAddPlugin(name, repo); err != nil {
				return listDoneMsg{mode: mode, err: err}
			}
		}
		vs, err := toolAllVersions(name)
		return listDoneMsg{mode: mode, versions: vs, err: err}
	}
}

func doTaskCmd(label string, fn func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		out, err := fn()
		return taskDoneMsg{label: label, out: out, err: err}
	}
}

// refreshPluginCmd re-fetches one plugin's repo info (desc/archived/project)
// in the background using the same rate-limit → README fallback as the
// catalog refresh, then persists it to the catalog YAML. It also clears the
// Removed flag (🗑) when `asdf plugin list all` still lists the plugin — that
// state may have gone stale if a catalog refresh ran while asdf's registry
// was incomplete.
func refreshPluginCmd(p Plugin) tea.Cmd {
	return func() tea.Msg {
		rl := newRateLimiter(repoDelay())
		defer rl.stop()
		np, _, err := refreshOnePlugin(p, rl)
		if err == nil {
			if all, err2 := asdfPluginListAll(); err2 == nil {
				for _, a := range all {
					if a.Name == np.Name && strings.TrimSpace(a.Repo) != "" {
						np.Removed = false
						break
					}
				}
			}
		}
		return pluginRefreshMsg{p: np, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		wa, _, _ := m.colWidths()
		m.tools.SetSize(wa-2, m.bodyHeight()-2)
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case refreshMsg:
		m.addedSet = map[string]bool{}
		for _, p := range asdfPluginList() {
			m.addedSet[p] = true
		}
		return m, nil

	case list.FilterMatchesMsg:
		var cmd tea.Cmd
		m.tools, cmd = m.tools.Update(msg)
		return m, m.trackSelection(cmd)

	case statusMsg:
		m.statusMsg = ""
		m.state[msg.name] = msg.st
		return m, nil

	case listDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.mode = verNone
			return m, nil
		}
		m.errMsg = ""
		m.verItems = sortVersionsDesc(msg.versions)
		m.verFilter = ""
		m.verSel = 0
		m.verTop = 0
		m.mode = msg.mode
		m.focus = focusVersions
		return m, nil

	case pluginRefreshMsg:
		m.busy = false
		name := msg.p.Name
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		found := false
		for i := range m.plugins {
			if m.plugins[i].Name == name {
				m.plugins[i] = msg.p
				found = true
				break
			}
		}
		if !found {
			m.errMsg = "plugin " + name + " not in catalog"
			return m, nil
		}
		m.syncToolItems()
		if err := saveCatalogYAML(catalogPath(), m.plugins); err != nil {
			m.errMsg = "refreshed, but catalog save failed: " + err.Error()
			return m, nil
		}
		m.errMsg = ""
		m.statusMsg = "✓ refreshed: " + name
		return m, nil

	case taskDoneMsg:
		m.busy = false
		m.statusMsg = msg.label
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("%s: %s", msg.label, strings.TrimSpace(msg.err.Error()))
		} else {
			m.errMsg = ""
			m.statusMsg = "✓ " + msg.label + " — done"
			if m.mode == verScope {
				m.mode = verNone
				m.focus = focusActions
			}
			if strings.HasPrefix(msg.label, "Remove") {
				m.confirmRm = false
				m.mode = verNone
				m.focus = focusActions
			}
			if strings.HasPrefix(msg.label, "Uninstall") {
				m.confirmUninstall = ""
				m.mode = verNone
				m.focus = focusActions
			}
		}
		// refresh both the plugin list and the currently selected tool's
		// installed/available versions so the middle column + set-default
		// list reflect the change immediately (no manual restart needed)
		name := m.selName()
		if name != "" {
			return m, tea.Batch(func() tea.Msg { return refreshMsg{} }, stCmd(name))
		}
		return m, func() tea.Msg { return refreshMsg{} }

	case updateCheckMsg:
		if msg.err == nil && updateAvailable(version, msg.latest) {
			m.updateLatest = msg.latest
			m.updateOpen = true
		}
		return m, nil

	case tea.KeyMsg:
		return m.keyMsg(msg)
	}
	return m, nil
}

func (m model) colWidths() (int, int, int) {
	body := m.width - 6
	if body < 60 {
		body = 60
	}
	toolsW := body * 35 / 100
	actW := body * 33 / 100
	rightW := body - toolsW - actW
	if rightW < 20 {
		rightW = 20
	}
	return toolsW, actW, rightW
}

func (m model) bodyHeight() int {
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	return h
}

func (m model) keyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if os.Getenv("ASDF_TUI_DEBUG") != "" {
		f, _ := os.OpenFile("/tmp/opencode/keylog", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if f != nil {
			fmt.Fprintf(f, "focus=%d key=%q type=%d\n", m.focus, key, msg.Type)
			f.Close()
		}
	}

	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}

	if m.updateOpen {
		return m.updateModalKey(key)
	}

	if m.warnOpen {
		switch key {
		case "enter", "esc", "q", "ctrl+c":
			m.warnOpen = false
		}
		return m, nil
	}

	if m.filterOpen {
		return m.filterModalKey(key)
	}

	if m.confirmRm {
		switch key {
		case "y", "Y":
			return m.runRemove()
		case "n", "N", "esc":
			m.confirmRm = false
		}
		return m, nil
	}

	if m.confirmUninstall != "" {
		switch key {
		case "y", "Y":
			return m.runUninstall(m.confirmUninstall)
		case "n", "N", "esc", "q":
			m.confirmUninstall = ""
		}
		return m, nil
	}

	switch m.focus {
	case focusVersions:
		return m.versionsKey(msg)
	case focusActions:
		return m.actionsKey(key)
	}

	// focusTools
	if key == "ctrl+f" {
		m.filterOpen = true
		m.filterSel = int(m.filter)
		m.statusMsg = ""
		m.errMsg = ""
		return m, nil
	}
	if key == "q" && m.tools.FilterInput.Value() == "" {
		return m, tea.Quit
	}
	if key == "enter" || key == "tab" {
		var cmd tea.Cmd
		m.tools, cmd = m.tools.Update(msg)
		if len(m.tools.Items()) > 0 {
			m.focus = focusActions
			m.selAct = 0
		}
		return m, m.trackSelection(cmd)
	}
	// 'r' removes — but only when not actively typing into the search
	// filter, otherwise the letter leaks into the remove confirmation while
	// the user is looking for a plugin.
	if key == "r" && m.tools.FilterInput.Value() == "" {
		if m.selected() != nil {
			m.confirmRm = true
		}
		return m, nil
	}
	var cmd tea.Cmd
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		runes := msg.Runes
		ok := true
		for _, ru := range runes {
			if ru < ' ' || ru > '~' {
				ok = false
				break
			}
		}
		if ok {
			if m.tools.FilterState() != list.Filtering {
				prime := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
				m.tools, _ = m.tools.Update(prime)
			}
			for i, ru := range runes {
				kmsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ru}}
				var c tea.Cmd
				m.tools, c = m.tools.Update(kmsg)
				if i == len(runes)-1 {
					cmd = c
				}
			}
			return m, m.trackSelection(cmd)
		}
	}
	m.tools, cmd = m.tools.Update(msg)
	return m, m.trackSelection(cmd)
}

func (m model) selName() string {
	if p := m.selected(); p != nil {
		return p.Name
	}
	return ""
}

func (m *model) trackSelection(cmd tea.Cmd) tea.Cmd {
	if n := m.selName(); n != m.lastSel {
		m.lastSel = n
		m.statusMsg = ""
		m.errMsg = ""
		if n != "" {
			if cmd == nil {
				return stCmd(n)
			}
			return tea.Batch(cmd, stCmd(n))
		}
	}
	return cmd
}

func (m model) selected() *Plugin {
	it := m.tools.SelectedItem()
	if it == nil {
		return nil
	}
	if pi, ok := it.(pluginItem); ok {
		return &pi.p
	}
	return nil
}

// syncToolItems rebuilds the left-column list items after m.plugins changed
// (e.g. a single-plugin refresh updated desc/archived/project) OR the active
// filter changed, preserving the current selection index. The items are built
// from the filtered subset, so the active filter stays applied after a refresh.
// It also wires the ranks-based search function so text search is not limited
// to plugin names: descriptions, project links and project descriptions count
// too, with name matches always ranked above description-only matches.
func (m *model) syncToolItems() {
	plugins := m.filteredPlugins()
	items := make([]list.Item, 0, len(plugins))
	for _, p := range plugins {
		items = append(items, pluginItem{p: p})
	}
	m.tools.SetItems(items)
	// items and plugins share the same order, so the closure can index into
	// the plugin slice for relevance while bubbles restricts the visible set
	m.tools.Filter = func(term string, _ []string) []list.Rank {
		return rankPlugins(plugins, term)
	}
}

// rankPlugins searches a plugin subset by relevance. Every space-separated
// token must match the name, project link, app description, project or plugin
// description of a plugin; the plugins score points per token from the most
// specific field match (exact name > name prefix > name substring > project >
// project/app description > plugin description), plus a bonus when all tokens
// land in the name. Results come back best-first so the most relevant plugin
// sits at the cursor.
func rankPlugins(plugins []Plugin, term string) []list.Rank {
	if len(plugins) == 0 {
		return nil
	}
	tokens := strings.Fields(strings.ToLower(term))
	if len(tokens) == 0 {
		out := make([]list.Rank, len(plugins))
		for i := range out {
			out[i] = list.Rank{Index: i}
		}
		return out
	}
	type scored struct {
		index int
		score int
	}
	matched := make([]scored, 0, len(plugins))
	for i, p := range plugins {
		if s := pluginSearchScore(p, tokens); s > 0 {
			matched = append(matched, scored{index: i, score: s})
		}
	}
	sort.SliceStable(matched, func(a, b int) bool { return matched[a].score > matched[b].score })
	out := make([]list.Rank, len(matched))
	for k, e := range matched {
		out[k] = list.Rank{Index: e.index}
	}
	return out
}

// pluginSearchScore scores one plugin against all search tokens; 0 means the
// plugin does not match (a token was found nowhere).
func pluginSearchScore(p Plugin, tokens []string) int {
	name := strings.ToLower(p.Name)
	project := strings.ToLower(p.Project)
	projectDesc := strings.ToLower(p.ProjectDesc)
	appDesc := strings.ToLower(p.AppDesc)
	desc := strings.ToLower(p.Desc)
	total := 0
	allInName := true
	for _, t := range tokens {
		tier := 0
		switch {
		case name == t:
			tier = 6
		case strings.HasPrefix(name, t):
			tier = 5
		case strings.Contains(name, t):
			tier = 4
		case strings.Contains(project, t):
			tier = 3
		case projectDesc != "" && strings.Contains(projectDesc, t):
			tier = 2
		case appDesc != "" && strings.Contains(appDesc, t):
			tier = 2
		case desc != "" && strings.Contains(desc, t):
			tier = 1
		}
		if tier == 0 {
			return 0
		}
		if !strings.Contains(name, t) {
			allInName = false
		}
		total += tier
	}
	if len(tokens) > 1 && allInName {
		total += 2 // a multi-word query matches the name → top results
	}
	return total
}

// filteredPlugins returns the catalog rows currently visible: the full list
// for filterAll, otherwise only the rows matching the active filter.
func (m model) filteredPlugins() []Plugin {
	if m.filter == filterAll {
		return m.plugins
	}
	out := make([]Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		if m.matchesFilter(p) {
			out = append(out, p)
		}
	}
	return out
}

func (m model) matchesFilter(p Plugin) bool {
	switch m.filter {
	case filterAdded:
		return m.addedSet[p.Name]
	case filterActive:
		return !p.Archived && !p.Removed && !p.Unavailable
	case filterArchived:
		return p.Archived
	case filterRemoved:
		return p.Removed
	case filterUnreachable:
		return p.Unavailable
	}
	return true
}

func (m model) filterLabel() string {
	switch m.filter {
	case filterAdded:
		return "added"
	case filterActive:
		return "active"
	case filterArchived:
		return "archived"
	case filterRemoved:
		return "removed"
	case filterUnreachable:
		return "unreachable"
	}
	return "all"
}

// renderFilterModal draws the boxed chooser shown over the screen while the
// catalog filter is being picked (ctrl+f). The row marked with ● is the
// currently active filter; the highlighted row is the one Enter would apply.
func (m model) renderFilterModal() string {
	innerW := 0
	for _, o := range filterOptions {
		if w := lipgloss.Width("    " + o.label); w > innerW {
			innerW = w
		}
	}
	if innerW < 24 {
		innerW = 24
	}
	var b strings.Builder
	for i, o := range filterOptions {
		arrow := "  "
		if i == m.filterSel {
			arrow = "› "
		}
		line := arrow + fmt.Sprintf("%d · %s", i+1, o.label)
		row := pad(line, innerW)
		if o.mode == m.filter {
			row += " ●"
		}
		if i == m.filterSel {
			b.WriteString(styleHighlight.Render(row) + "\n")
		} else {
			b.WriteString(row + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(styleDim.Render("↑/↓ · 1-6 pick · Enter apply · Esc cancel"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("86")).
		Padding(0, 1).
		Render(b.String())
}

// overlayBox centers box over the screen rectangle and draws it on top of the
// existing frame, cell-accurate (wide glyphs and box-drawing borders are
// measured with lipgloss/x-ansi widths, never split).
func overlayBox(under, box string, screenW int) string {
	ul := strings.Split(under, "\n")
	bl := strings.Split(box, "\n")
	bw := 0
	for _, l := range bl {
		if w := lipgloss.Width(l); w > bw {
			bw = w
		}
	}
	bh := len(bl)
	left := (screenW - bw) / 2
	if left < 0 {
		left = 0
	}
	top := (len(ul) - bh) / 2
	if top < 0 {
		top = 0
	}
	for i, b := range bl {
		row := top + i
		if row < 0 || row >= len(ul) {
			continue
		}
		ul[row] = overlayLine(ul[row], b, left, screenW)
	}
	return strings.Join(ul, "\n")
}

// overlayLine overwrites the cell window [left, left+len(text)) of line with
// text and pads the result to the screen width, keeping the covered region
// opaque (the modal box has a solid background). Wide glyphs on either side
// are walked by cell width so the splice never lands mid-glyph.
func overlayLine(line, text string, left, width int) string {
	text = ansi.Truncate(text, width-left, "")
	tr := []rune(text)
	rr := []rune(line)
	cellAt := func(cell int) int {
		n := 0
		for i, r := range rr {
			if n >= cell {
				return i
			}
			n += lipgloss.Width(string(r))
		}
		return len(rr)
	}
	start := cellAt(left)
	after := left
	for _, r := range tr {
		after += lipgloss.Width(string(r))
	}
	rest := cellAt(after)
	out := append([]rune{}, rr[:start]...)
	out = append(out, tr...)
	out = append(out, rr[rest:]...)
	return pad(string(out), width)
}

// setFilter switches the tools-column subset, keeping the cursor on the
// previously selected plugin when it survives the filter (otherwise it jumps
// to the first visible row) and clearing any type-to-search filter so the new
// subset is shown in full.
func (m model) setFilter(f pluginFilter) (tea.Model, tea.Cmd) {
	if f != m.filter {
		m.filter = f
		prev := m.selName()
		m.tools.ResetFilter()
		m.syncToolItems()
		items := m.tools.Items()
		idx := -1
		for i := range items {
			if pi, ok := items[i].(pluginItem); ok && pi.p.Name == prev {
				idx = i
				break
			}
		}
		if idx >= 0 {
			m.tools.Select(idx)
		} else if len(items) > 0 {
			m.tools.Select(0)
		}
	}
	m.errMsg = ""
	m.statusMsg = "filter: " + m.filterLabel()
	return m, nil
}

// updateModalKey handles the startup update prompt: y confirms and quits the
// TUI so runTUI can hand the terminal to the installer; n/Esc dismisses and
// the session keeps running.
func (m model) updateModalKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y", "enter":
		m.doUpdate = true
		return m, tea.Quit
	case "n", "N", "esc", "q":
		m.updateOpen = false
	}
	return m, nil
}

// filterModalKey handles keys while the ctrl+f filter chooser is open.
func (m model) filterModalKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		m.filterOpen = false
	case "up", "k":
		if m.filterSel > 0 {
			m.filterSel--
		}
	case "down", "j":
		if m.filterSel < len(filterOptions)-1 {
			m.filterSel++
		}
	case "enter", " ":
		m.filterOpen = false
		return m.setFilter(pluginFilter(m.filterSel))
	case "1", "2", "3", "4", "5", "6":
		i := int(key[0] - '1')
		m.filterOpen = false
		return m.setFilter(filterOptions[i].mode)
	}
	return m, nil
}

func (m *model) actionsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q", "tab":
		m.focus = focusTools
	case "up", "k":
		if m.selAct > 0 {
			m.selAct--
		}
	case "down", "j":
		if m.selAct < 8 {
			m.selAct++
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.selAct = int(key[0] - '1')
	case "enter", " ":
		return m.runAction(m.selAct)
	case "r":
		if m.selected() != nil {
			m.confirmRm = true
		}
	}
	return m, nil
}

func (m *model) versionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		if m.verFilter != "" {
			m.verFilter = ""
			m.verSel, m.verTop = 0, 0
			return m, nil
		}
		m.mode = verNone
		m.pickVer = ""
		m.focus = focusActions
	case "q":
		m.mode = verNone
		m.pickVer = ""
		m.focus = focusActions
	case "up", "k":
		if m.mode == verScope {
			if m.selScope > 0 {
				m.selScope--
			}
		} else if m.verSel > 0 {
			m.verSel--
		}
	case "down", "j":
		if m.mode == verScope {
			if m.selScope < 1 {
				m.selScope++
			}
		} else if m.verSel < len(m.filteredVersions())-1 {
			m.verSel++
		}
	case "pgup":
		if len(m.filteredVersions()) == 0 {
			return m, nil
		}
		m.verSel -= m.verPageSize()
		if m.verSel < 0 {
			m.verSel = 0
		}
	case "pgdown":
		max := len(m.filteredVersions()) - 1
		if max < 0 {
			return m, nil
		}
		m.verSel += m.verPageSize()
		if m.verSel > max {
			m.verSel = max
		}
	case "backspace":
		if len(m.verFilter) > 0 {
			m.verFilter = m.verFilter[:len(m.verFilter)-1]
			m.verSel, m.verTop = 0, 0
		}
	case "/":
		if m.verFilter != "" {
			m.verFilter = ""
			m.verSel, m.verTop = 0, 0
		}
	case "enter":
		f := m.filteredVersions()
		if m.mode == verScope {
			return m.runSetScope()
		}
		if m.verSel < len(f) {
			switch m.mode {
			case verInstall:
				return m.runInstall(f[m.verSel])
			case verSet:
				m.pickVer = f[m.verSel]
				m.mode = verScope
				m.selScope = 0
			case verUninstall:
				m.confirmUninstall = f[m.verSel]
			}
		}
	case "l", "L":
		if m.mode == verInstall {
			return m.runInstallLatest()
		}
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			ok := true
			for _, ru := range msg.Runes {
				if ru < ' ' || ru > '~' {
					ok = false
					break
				}
			}
			if ok {
				m.verFilter += msg.String()
				m.verSel, m.verTop = 0, 0
			}
		}
	}
	return m, nil
}

func sortVersionsDesc(vs []string) []string {
	out := make([]string, len(vs))
	copy(out, vs)
	sort.SliceStable(out, func(i, j int) bool {
		return verLessNewer(out[i], out[j])
	})
	return out
}

func verCmpNumeric(a, b string) int {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		var av, bv string
		for ai < len(a) && a[ai] >= '0' && a[ai] <= '9' {
			av += string(a[ai])
			ai++
		}
		for bi < len(b) && b[bi] >= '0' && b[bi] <= '9' {
			bv += string(b[bi])
			bi++
		}
		if av != "" && bv != "" {
			an, bn := len(av), len(bv)
			_ = an
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
			if av != bv {
				if av < bv {
					return -1
				}
				return 1
			}
		}
		if ai >= len(a) || bi >= len(b) {
			break
		}
		if a[ai] != b[bi] {
			if a[ai] < b[bi] {
				return -1
			}
			return 1
		}
		ai++
		bi++
	}
	// remaining digits compare
	ra, rb := a[ai:], b[bi:]
	if ra == "" && rb == "" {
		return 0
	}
	if ra == "" {
		return -1
	}
	if rb == "" {
		return 1
	}
	return strings.Compare(ra, rb)
}

func verLessNewer(a, b string) bool {
	if a == "" || b == "" {
		return a > b
	}
	return verCmpNumeric(a, b) > 0 // newer (higher) sorts first
}

func (m model) filteredVersions() []string {
	if m.verFilter == "" {
		return m.verItems
	}
	out := make([]string, 0, len(m.verItems))
	for _, v := range m.verItems {
		if fuzzyMatch(v, m.verFilter) {
			out = append(out, v)
		}
	}
	return out
}

// verPageSize returns how many version rows fit on one screen: it mirrors the
// viewport math in renderVersions so PgUp/PgDn step by exactly one page.
func (m model) verPageSize() int {
	pg := m.bodyHeight() - 6
	if pg < 1 {
		pg = 1
	}
	return pg
}

// versionWindowTop computes the first visible row index for a list of n items
// on a total-row viewport, keeping the selection on screen. It snaps the
// window to page boundaries when the selection moves past the visible rows,
// so PgUp/PgDn visibly turn pages (and the ●/· dots stay in sync).
func versionWindowTop(sel, total, n int) int {
	if total < 1 {
		total = 1
	}
	if n <= 0 {
		return 0
	}
	if sel > n-1 {
		sel = n - 1
	}
	top := 0
	if sel >= top+total {
		top = (sel / total) * total
	}
	if top > sel {
		top = sel
	}
	if top+total > n {
		top = n - total
		if top < 0 {
			top = 0
		}
	}
	return top
}

func (m *model) runInstall(ver string) (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		return m, nil
	}
	m.busy = true
	m.busyLabel = "Installing " + p.Name + " " + ver
	m.statusMsg = ""
	m.errMsg = ""
	name := p.Name
	return m, doTaskCmd("Install "+name+" "+ver, func() (string, error) {
		return "", asdfInstall(name, ver, p.Repo)
	})
}

func (m *model) runAddPlugin() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		return m, nil
	}
	name, repo := p.Name, p.Repo
	m.busy = true
	m.busyLabel = "Adding plugin " + name
	m.statusMsg = ""
	m.errMsg = ""
	return m, doTaskCmd("Add plugin "+name, func() (string, error) {
		return "", asdfAddPlugin(name, repo)
	})
}

func (m *model) runInstallLatest() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		return m, nil
	}
	name := p.Name
	m.busy = true
	m.busyLabel = "Resolving latest version of " + name
	m.statusMsg = ""
	m.errMsg = ""
	return m, doTaskCmd("Install latest "+name, func() (string, error) {
		latest, err := toolLatest(name)
		if err != nil {
			return "", err
		}
		return latest, asdfInstall(name, latest, p.Repo)
	})
}

func (m *model) runSetScope() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil || m.pickVer == "" {
		return m, nil
	}
	scopes := []string{"user", "folder"}
	sc := scopes[m.selScope]
	pick, name, dir := m.pickVer, p.Name, m.rootDir
	return m.runSetDefaultTask(name, pick, sc, dir)
}

func (m *model) runSetDefaultTask(name, version, sc, dir string) (tea.Model, tea.Cmd) {
	m.busy = true
	m.busyLabel = "Setting default (" + sc + "): " + name + " = " + version
	m.statusMsg = ""
	m.errMsg = ""
	return m, doTaskCmd("Set default ("+sc+") "+name, func() (string, error) {
		return "", asdfSetVersion(name, version, sc, dir)
	})
}

func (m *model) runRemove() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		m.confirmRm = false
		return m, nil
	}
	name := p.Name
	m.confirmRm = false
	m.busy = true
	m.busyLabel = "Removing plugin " + name
	m.statusMsg = ""
	m.errMsg = ""
	return m, doTaskCmd("Remove "+name, func() (string, error) {
		return "", asdfRemovePlugin(name)
	})
}

// runUninstall removes one installed version of the selected tool after the
// user confirmed it in the versions column (`asdf uninstall <name> <version>`).
func (m *model) runUninstall(ver string) (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		m.confirmUninstall = ""
		return m, nil
	}
	name := p.Name
	m.confirmUninstall = ""
	m.busy = true
	m.busyLabel = "Uninstalling " + name + " " + ver
	m.statusMsg = ""
	m.errMsg = ""
	return m, doTaskCmd("Uninstall "+name+" "+ver, func() (string, error) {
		return "", asdfUninstall(name, ver)
	})
}

func (m *model) runAction(i int) (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		return m, nil
	}
	switch i {
	case 0:
		m.busy = true
		m.busyLabel = "Fetching versions of " + p.Name
		return m, versionsCmd(p.Name, p.Repo, verInstall)
	case 1:
		return m.runInstallLatest()
	case 2:
		return m.runAddPlugin()
	case 3:
		st, ok := m.state[p.Name]
		if !ok || !st.loaded || len(st.versions) == 0 {
			m.errMsg = "no installed versions yet — install one first, then set default"
			return m, nil
		}
		m.verItems = st.versions
		m.verFilter = ""
		m.verSel, m.verTop = 0, 0
		m.mode = verSet
		m.errMsg = ""
		m.focus = focusVersions
	case 4:
		st, ok := m.state[p.Name]
		if !ok || !st.loaded || len(st.versions) == 0 {
			m.errMsg = "no installed versions yet — install one first, then uninstall"
			return m, nil
		}
		m.verItems = st.versions
		m.verFilter = ""
		m.verSel, m.verTop = 0, 0
		m.mode = verUninstall
		m.errMsg = ""
		m.focus = focusVersions
	case 5:
		name := p.Name
		m.busy = true
		m.busyLabel = "Updating plugin " + name
		return m, doTaskCmd("Update "+name, func() (string, error) {
			return "", asdfUpdatePlugin(name)
		})
	case 6:
		name := p.Name
		m.busy = true
		m.busyLabel = "Reshim " + name
		return m, doTaskCmd("Reshim "+name, func() (string, error) {
			return "", asdfReshim(name)
		})
	case 7:
		m.busy = true
		m.busyLabel = "Refreshing " + p.Name
		m.statusMsg = ""
		m.errMsg = ""
		return m, refreshPluginCmd(*p)
	case 8:
		m.confirmRm = true
	}
	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "resizing…"
	}
	wa, aa, ra := m.colWidths()
	bh := m.bodyHeight()

	render := func(f focus, w, h int, content string) string {
		s := styleDim
		if m.focus == f {
			s = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("86")).Padding(0, 1)
		} else {
			s = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
		}
		return s.Width(w).Height(h).Render(content)
	}

	left := render(focusTools, wa, bh, m.renderTools(wa-2, bh-2))
	middle := render(focusActions, aa, bh, m.renderActions(aa-2, bh-2))
	rightFocus := focusVersions
	if m.mode == verNone {
		rightFocus = focusActions
	}
	right := render(rightFocus, ra, bh, m.renderRight(ra-2, bh-2))

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, middle, right)

	header := lipgloss.JoinHorizontal(lipgloss.Left,
		styleBrand.Render(" asdf-tui ")+styleDim.Render(version+" "),
		styleDim.Render(m.headerHints()),
	)

	footer := m.statusLine()
	screen := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	switch {
	case m.updateOpen:
		screen = overlayBox(screen, m.renderUpdateModal(), m.width)
	case m.warnOpen:
		screen = overlayBox(screen, m.renderWarnModal(), m.width)
	case m.filterOpen:
		screen = overlayBox(screen, m.renderFilterModal(), m.width)
	}
	return screen
}

// renderUpdateModal draws the startup prompt shown when GitHub lists a newer
// asdf-tui than the stamped build: opting in closes the TUI and lets the
// installer take over the terminal.
func (m model) renderUpdateModal() string {
	var b strings.Builder
	b.WriteString(styleBrand.Render(" ⬆ asdf-tui update available ") + "\n\n")
	b.WriteString("New version " + m.updateLatest + " is out,\n")
	b.WriteString("you are running " + version + ".\n\n")
	b.WriteString("Install it now? The TUI exits and the\n")
	b.WriteString("installer takes over this terminal.\n\n")
	b.WriteString(styleDim.Render("y / Enter — install · n / Esc — stay"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("86")).
		Padding(0, 1).
		Render(b.String())
}

// renderWarnModal draws the startup warning shown when asdf itself is missing:
// the tool depends on the version manager for every action, so this is
// surfaced loudly instead of failing silently on the first asdf call.
func (m model) renderWarnModal() string {
	var b strings.Builder
	b.WriteString(styleErr.Render(" ⚠ asdf not found ") + "\n\n")
	b.WriteString("asdf-tui depends on the asdf version manager.\n")
	b.WriteString("Install it and make sure `asdf` is on your PATH:\n")
	b.WriteString(styleDim.Render("  https://asdf-vm.com") + "\n\n")
	b.WriteString("The catalog stays browsable, but every action on\n")
	b.WriteString("your tools silently does nothing until asdf is\n")
	b.WriteString("installed.\n\n")
	b.WriteString(styleDim.Render("Enter / Esc to dismiss"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("214")).
		Padding(0, 1).
		Render(b.String())
}

func (m model) headerHints() string {
	switch {
	case m.busy:
		return "working…"
	case m.focus == focusActions:
		return "middle column — actions"
	case m.focus == focusVersions:
		return "right column — " + verModeName(m.mode)
	}
	return "tools — " + m.filterLabel() + " · type to search"
}

func verModeName(v verMode) string {
	switch v {
	case verInstall:
		return "install a version"
	case verSet:
		return "choose installed version"
	case verScope:
		return "choose scope"
	case verUninstall:
		return "remove an installed version"
	}
	return ""
}

func (m model) renderTools(w, h int) string {
	if len(m.plugins) == 0 {
		return styleDim.Render("empty catalog")
	}
	if len(m.filteredPlugins()) == 0 {
		return styleDim.Render("no plugins match " + m.filterLabel() + " (ctrl+f)")
	}
	m.tools.SetSize(w, h)
	return m.tools.View()
}

func (m model) renderActions(w, h int) string {
	if m.confirmRm {
		p := m.selected()
		name := "plugin"
		if p != nil {
			name = p.Name
		}
		return styleErr.Render("Remove plugin "+name+"\n(erases all its versions)?") +
			"\n\n  " + styleOk.Render("y") + " yes    " + styleDim.Render("n") + " no"
	}
	var b strings.Builder
	// fixed-height info block: name, app description, repo status, plugin +
	// project descriptions with their links. It always occupies the same
	// number of lines so the actions below never jump when the description or
	// links change.
	b.WriteString(m.renderToolInfo(w))
	labels := []string{
		"Install a specific version…",
		"Install latest version",
		"Add plugin",
		"Set default version…",
		"Uninstall a version…",
		"Update plugin",
		"Reshim",
		"Refresh plugin info",
		"Remove plugin",
	}
	for i, l := range labels {
		line := fmt.Sprintf(" %d · %s", i+1, l)
		if i == m.selAct {
			b.WriteString(styleHighlight.Render(pad("› "+l, len(line))))
		} else {
			b.WriteString("  " + l)
		}
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderToolInfo prints the selected tool's header block on a fixed number of
// lines (name, name-only app description, repo status, plugin description,
// plugin repo link, project description, project link, blank separator) so the
// action list below keeps a stable offset. The two descriptions are
// deliberately on distinct lines so they never merge.
func (m model) renderToolInfo(w int) string {
	var b strings.Builder
	p := m.selected()
	if p == nil {
		b.WriteString(styleDim.Render("choose a tool in the left column"))
		b.WriteString("\n\n\n\n\n\n\n\n\n\n")
		return b.String()
	}
	b.WriteString(styleBrand.Render(p.Name+pluginIcon(*p)) + m.currentVersionLine(p.Name) + "\n")
	if ad := firstLine(p.AppDesc); ad != "" {
		b.WriteString(styleDim.Render(ad) + "\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString(m.repoStatusLine(*p) + "\n")
	b.WriteString("\n")
	desc := firstLine(p.Desc)
	if desc == "" {
		b.WriteString(styleDim.Render("—") + "\n")
	} else {
		b.WriteString(styleDim.Render(desc) + "\n")
	}
	if p.Repo != "" {
		b.WriteString(styleDim.Render("🔌 "+p.Repo) + "\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if pd := firstLine(p.ProjectDesc); pd != "" {
		b.WriteString(styleDim.Render(pd) + "\n")
	} else {
		b.WriteString("\n")
	}
	if p.Project != "" {
		b.WriteString(styleDim.Render("↗ "+p.Project) + "\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// currentVersionLine renders the version of the tool currently in use (from
// the `*` line of `asdf list`), dimmed right after the tool name in the
// middle column; "" when the state probe has not answered yet or no version
// is set.
func (m model) currentVersionLine(name string) string {
	st, ok := m.state[name]
	if !ok || !st.loaded || st.current == "" {
		return ""
	}
	return "  " + styleDim.Render("v"+st.current)
}

// repoStatusLine combines the repository state (archived/removed/unreachable/
// active) with whether the plugin is added to asdf.
func (m model) repoStatusLine(p Plugin) string {
	var status string
	switch {
	case p.Archived:
		status = styleDim.Render("🔒 repo archived")
	case p.Removed:
		status = styleErr.Render("🗑 removed from catalog")
	case p.Unavailable:
		status = styleErr.Render("🚫 repo unreachable")
	default:
		status = styleOk.Render("● repo active")
	}
	added := styleDim.Render("not added")
	if st, ok := m.state[p.Name]; ok && st.loaded {
		if st.added {
			added = styleOk.Render("✓ added")
		} else {
			added = styleDim.Render("not added")
		}
	}
	return status + "  ·  " + added
}

// firstLine returns the first non-empty line of s (or s itself), keeping the
// fixed info block one line tall even if a description contains newlines.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (m model) renderRight(w, h int) string {
	if m.busy {
		return m.spinner.View() + "  " + m.busyLabel
	}
	if m.mode == verInstall || m.mode == verSet || m.mode == verUninstall {
		return m.renderVersions(w, h)
	}
	if m.mode == verScope {
		return m.renderScope(w, h)
	}
	p := m.selected()
	if p == nil {
		return styleDim.Render("choose a tool in the left column")
	}
	st := m.state[p.Name]
	var b strings.Builder
	b.WriteString(styleBrand.Render(p.Name+pluginIcon(*p)) + "\n\n")
	b.WriteString(p.Desc + "\n\n")
	b.WriteString(styleDim.Render(p.Repo) + "\n")
	if st.loaded {
		switch {
		case !st.added:
			b.WriteString("\n" + styleDim.Render("plugin not added — install it via actions"))
		case len(st.versions) == 0:
			b.WriteString("\n" + styleOk.Render("added, no versions installed yet"))
		default:
			b.WriteString("\n" + styleOk.Render("installed versions:") + "\n  " + strings.Join(st.versions, "\n  "))
		}
	}
	return b.String()
}

func (m model) renderVersions(w, h int) string {
	if m.confirmUninstall != "" {
		name := "..."
		if p := m.selected(); p != nil {
			name = p.Name
		}
		var b strings.Builder
		b.WriteString(styleErr.Render("Uninstall "+name+" "+m.confirmUninstall+"?") + "\n")
		b.WriteString(styleDim.Render("removes the installed version") + "\n\n")
		b.WriteString("  " + styleOk.Render("y") + " yes    " + styleDim.Render("n") + " no")
		return b.String()
	}
	var b strings.Builder
	title := "install a version"
	switch m.mode {
	case verSet:
		title = "installed versions → pick, then scope"
	case verUninstall:
		title = "installed versions → pick, then remove"
	}
	b.WriteString(styleDim.Render(title) + "\n")
	b.WriteString(styleDim.Render("> "+m.verFilter+" ") + "\n\n")
	f := m.filteredVersions()
	if len(f) == 0 {
		b.WriteString(styleDim.Render("(no versions)"))
		return b.String()
	}
	total := h - 4
	if total < 1 {
		total = 1
	}
	if m.verSel > len(f)-1 {
		m.verSel = len(f) - 1
	}
	m.verTop = versionWindowTop(m.verSel, total, len(f))
	for i := m.verTop; i < m.verTop+total; i++ {
		if i >= len(f) {
			break
		}
		v := f[i]
		if i == m.verSel {
			b.WriteString(styleHighlight.Render(pad("› "+v, w)) + "\n")
		} else {
			b.WriteString("  " + v + "\n")
		}
	}
	b.WriteString(m.renderDots(f, m.verSel, total, w))
	return strings.TrimSuffix(b.String(), "\n")
}

// renderDots draws the ●/· page indicator under the version list, mirroring
// the pagination dots of the tools column: a filled dot marks the page the
// selection currently sits on.
func (m model) renderDots(f []string, sel, pageSize, w int) string {
	if pageSize < 1 {
		pageSize = 1
	}
	pages := (len(f) + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	cur := sel / pageSize
	if cur >= pages {
		cur = pages - 1
	}
	var b strings.Builder
	for i := 0; i < pages; i++ {
		if i == cur {
			b.WriteString(styleBrand.Render("●"))
		} else {
			b.WriteString(styleDim.Render("·"))
		}
	}
	return pad(b.String(), w)
}

func (m model) renderScope(w, h int) string {
	name := "..."
	if p := m.selected(); p != nil {
		name = p.Name
	}
	items := []string{
		"user   — ~/.tool-versions (asdf set -u)",
		"folder — ./.tool-versions (asdf set)",
	}
	var b strings.Builder
	b.WriteString(styleDim.Render("set default") + "  " + styleBrand.Render(name+" "+m.pickVer) + "\n\n")
	for i, it := range items {
		if i == m.selScope {
			b.WriteString(styleHighlight.Render(pad("› "+it, w)) + "\n\n")
		} else {
			b.WriteString("  " + it + "\n\n")
		}
	}
	b.WriteString(styleDim.Render("↑ / ↓ — pick, then Enter"))
	return b.String()
}

func (m model) statusLine() string {
	var b strings.Builder
	switch {
	case m.busy:
		b.WriteString(styleBrand.Render(" running: ") + m.busyLabel + "\n")
	case m.confirmUninstall != "":
		b.WriteString(styleDim.Render(" y — confirm uninstall · n — cancel") + "\n")
	case m.focus == focusActions && !m.confirmRm:
		b.WriteString(styleDim.Render(" ↑/↓ · j/k · 1-9 pick · Enter run · Esc back to tools") + "\n")
	case m.focus == focusVersions:
		if m.mode == verScope {
			b.WriteString(styleDim.Render(" ↑/↓ pick · Enter run · Esc back") + "\n")
		} else if m.mode == verUninstall {
			b.WriteString(styleDim.Render(" Enter uninstall · / letters filter · PgUp/PgDn pages · Esc clear/back") + "\n")
		} else {
			b.WriteString(styleDim.Render(" Enter install · L latest · / letters filter · PgUp/PgDn pages · Esc clear/back") + "\n")
		}
	case m.confirmRm:
		b.WriteString(styleDim.Render(" y — confirm removal · n — cancel") + "\n")
	default:
		b.WriteString(styleDim.Render(" type to search names, apps, urls · ↑/↓ move · Enter pick · Esc clear filter") + "\n")
	}
	if m.errMsg != "" {
		b.WriteString(styleErr.Render(" ✗ "+m.errMsg) + "\n")
	} else if m.statusMsg != "" {
		b.WriteString(styleOk.Render(" ✓ "+m.statusMsg) + "\n")
	} else {
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(m.width - 2).Render(strings.TrimRight(b.String(), "\n"))
}

func pad(s string, w int) string {
	if w <= 0 {
		return s
	}
	n := lipgloss.Width(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

func runTUI(plugins []Plugin, rootDir string) {
	m := newModel(plugins, rootDir)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if wantsUpdate(final) {
		// The TUI has fully restored the terminal here, so the installer can
		// print and prompt normally.
		runInstaller()
		os.Exit(0)
	}
}
