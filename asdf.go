package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// asdfInstalled reports whether the asdf version manager is available on PATH.
// asdf-tui depends on it for every action it performs, so the TUI surfaces a
// warning at startup when it is missing.
func asdfInstalled() bool {
	_, err := exec.LookPath("asdf")
	return err == nil
}

func runCmd(args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command("asdf", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func runCmdDir(dir string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command("asdf", args...)
	cmd.Dir = dir
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// asdfVerRe matches the major.minor anywhere in `asdf version` output.
// The format changed over time: "version: 0.16.2" (pre-0.17) and plain
// "0.20.2 (revision unknown)" (0.17+, as shipped by Homebrew).
var asdfVerRe = regexp.MustCompile(`([0-9]+)\.([0-9]+)`)

func asdfVersionUsesSet(out string) bool {
	m := asdfVerRe.FindStringSubmatch(out)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return maj >= 1 || (maj == 0 && min >= 16)
}

func asdfUsesSet() bool {
	out, err := runCmd("version")
	if err != nil {
		return false
	}
	return asdfVersionUsesSet(out)
}

func asdfPluginList() []string {
	out, err := runCmd("plugin", "list")
	if err != nil {
		return nil
	}
	var res []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			res = append(res, t)
		}
	}
	return res
}

func toolInstalledVersions(name string) []string {
	out, err := runCmd("list", name)
	if err != nil {
		return nil
	}
	var res []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "*"))
		if line != "" && !strings.EqualFold(line, "No versions installed") {
			res = append(res, line)
		}
	}
	return res
}

func toolAllVersions(name string) ([]string, error) {
	args := []string{"list", "all", name}
	if !asdfUsesSet() {
		args = []string{"list-all", name}
	}
	out, err := runCmd(args...)
	if err != nil {
		return nil, err
	}
	var res []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		if t != "" && !strings.HasPrefix(t, "ref:") {
			res = append(res, t)
		}
	}
	return res, nil
}

func toolLatest(name string) (string, error) {
	out, err := runCmd("latest", name)
	if err != nil {
		return "", err
	}
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, name+" ") {
			line = strings.TrimPrefix(l, name+" ")
			break
		}
	}
	if line == "" {
		line = strings.TrimSpace(out)
	}
	return line, nil
}

func asdfAddPlugin(name, repo string) error {
	if asdfIsAdded(name) {
		return nil
	}
	args := []string{"plugin", "add", name}
	if repo != "" && repo != "-" {
		args = append(args, repo)
	}
	_, err := runCmd(args...)
	return err
}

func asdfIsAdded(name string) bool {
	for _, p := range asdfPluginList() {
		if p == name {
			return true
		}
	}
	return false
}

func asdfRemovePlugin(name string) error {
	_, err := runCmd("plugin", "remove", name)
	return err
}

func asdfUpdatePlugin(name string) error {
	_, err := runCmd("plugin", "update", name)
	return err
}

func asdfInstall(name, version, repo string) error {
	if err := asdfAddPlugin(name, repo); err != nil {
		return err
	}
	_, err := runCmd("install", name, version)
	return err
}

func asdfReshim(name string) error {
	_, err := runCmd("reshim", name)
	return err
}

func asdfSetVersion(name, version, scope, dir string) error {
	switch scope {
	case "user":
		if asdfUsesSet() {
			_, err := runCmd("set", "-u", name, version)
			return err
		}
		_, err := runCmd("global", name, version)
		return err
	case "folder":
		if asdfUsesSet() {
			ensureFolderVersionFile(dir)
			// plain `asdf set` (no -p) targets the working-directory file we
			// just ensured; --parent skips the cwd and walks up instead.
			_, err := runCmdDir(dir, "set", name, version)
			return err
		}
		_, err := runCmdDir(dir, "local", name, version)
		return err
	}
	return fmt.Errorf("unknown scope: %s", scope)
}

func ensureFolderVersionFile(dir string) {
	if _, err := os.Stat(filepath.Join(dir, ".tool-versions")); err == nil {
		return
	}
	f, err := os.Create(filepath.Join(dir, ".tool-versions"))
	if err == nil {
		f.Close()
	}
}

func asdfPluginListAll() ([]Plugin, error) {
	raw, err := runCmd("plugin", "list", "all")
	if err != nil {
		return nil, err
	}
	var out []Plugin
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "*")
		repo := ""
		if len(fields) > 1 {
			// `asdf plugin list all` prefixes some repos with `*`
			// (e.g. k9s → "*https://github.com/looztra/asdf-k9s.git").
			repo = strings.TrimPrefix(fields[1], "*")
		}
		out = append(out, Plugin{Name: name, Repo: repo})
	}
	return out, nil
}

// repoRef identifies where a plugin's repository lives. host is a lowercase
// hostname ("github.com", "gitlab.com", "codeberg.org", …); slug is the
// repository path without .git — "owner/name" on GitHub, potentially
// "group/subgroup/project" on GitLab.
type repoRef struct {
	host  string
	owner string // first path segment
	name  string // last path segment
	slug  string
}

// parseRepo parses a repository URL — as emitted by `asdf plugin list all`,
// including the occasional leading `*` — into a repoRef. It understands
// https://, http://, git@host:path and ssh:// forms for any forge that
// serves a plain path URL (GitHub, GitLab, codeberg, …).
func parseRepo(repo string) (repoRef, bool) {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimPrefix(repo, "*")
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.TrimRight(repo, "/")
	if repo == "" {
		return repoRef{}, false
	}
	host, path := "", ""
	switch {
	case strings.HasPrefix(repo, "git@"), strings.HasPrefix(repo, "ssh://"):
		s := strings.TrimPrefix(repo, "ssh://")
		s = strings.TrimPrefix(s, "git@")
		s = strings.Replace(s, ":", "/", 1)
		i := strings.Index(s, "/")
		if i < 0 {
			return repoRef{}, false
		}
		host, path = s[:i], s[i+1:]
	default:
		rest := strings.TrimPrefix(repo, "https://")
		rest = strings.TrimPrefix(rest, "http://")
		i := strings.Index(rest, "/")
		if i < 0 {
			return repoRef{}, false
		}
		host, path = rest[:i], rest[i+1:]
	}
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return repoRef{}, false
	}
	seg := strings.Split(path, "/")
	return repoRef{
		host:  host,
		owner: seg[0],
		name:  seg[len(seg)-1],
		slug:  path,
	}, true
}

// ownerRepo is the legacy github-only view of parseRepo, kept for callers
// that only care about owner/name on GitHub.
func ownerRepo(repo string) (string, string) {
	ref, ok := parseRepo(repo)
	if !ok || ref.host != "github.com" {
		return "", ""
	}
	return ref.owner, ref.name
}

// rateLimiter paces forge REST API requests. Every API call (plugin repo and
// its upstream project description) must pass through the SAME limiter so the
// request rate is bounded by repoDelay — a burst of refreshes or the bulk
// catalog walk can not trip GitHub/GitLab rate limits. A zero delay disables
// pacing (rateLimiter.wait returns immediately).
type rateLimiter struct {
	d    time.Duration
	ch   chan struct{}
	done chan struct{}
}

func newRateLimiter(d time.Duration) *rateLimiter {
	rl := &rateLimiter{d: d}
	if d > 0 {
		rl.ch = make(chan struct{}, 1)
		rl.done = make(chan struct{})
		go rl.fill()
	}
	return rl
}

func (rl *rateLimiter) fill() {
	t := time.NewTicker(rl.d)
	defer t.Stop()
	defer close(rl.ch)
	for {
		select {
		case <-rl.done:
			return
		case <-t.C:
			select {
			case rl.ch <- struct{}{}:
			case <-rl.done:
				return
			}
		}
	}
}

// stop releases the limiter's background ticker; waiting goroutines unblock.
func (rl *rateLimiter) stop() {
	if rl.done != nil {
		close(rl.done)
	}
}

// wait blocks until the limiter hands out its next pace token.
func (rl *rateLimiter) wait() {
	if rl.ch == nil {
		return
	}
	select {
	case <-rl.ch:
	case <-rl.done:
	}
}

// projectRepoDescription asks the upstream project's forge API for its
// one-line description (e.g. https://github.com/ava-labs/avalanche-cli).
// It is strictly best effort: the project link must resolve to a GitHub or
// GitLab repository, the API must answer without a rate limit, and the reply
// is passed through cleanDescription. Anything else yields "" so the plugin
// is never marked unavailable because of a missing project description.
func projectRepoDescription(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return ""
	}
	ref, ok := parseRepo(project)
	if !ok {
		return ""
	}
	var apiURL string
	switch {
	case ref.host == "github.com":
		apiURL = "https://api.github.com/repos/" + url.PathEscape(ref.owner) + "/" + url.PathEscape(ref.name)
	case strings.Contains(ref.host, "gitlab"):
		apiURL = "https://" + ref.host + "/api/v4/projects/" + url.PathEscape(ref.slug)
	default:
		return "" // unknown forge — no API description
	}
	desc, _, err := forgeRepoFields(apiURL)
	if err != nil {
		return ""
	}
	return cleanDescription(desc)
}

// pluginRepoInfo fetches the one-line description, the archived state and
// the first `[label](url)` link found in the README (the upstream project
// the plugin wraps) for any supported forge: GitHub and GitLab go through
// their REST APIs, other forges are README-only. A 403/429 (rate limit)
// returns errRateLimit so the caller can fall back to README-only data
// instead of marking the plugin unreachable; anything else returns an error
// so the caller can mark it unavailable.
func pluginRepoInfo(ref repoRef) (desc string, archived bool, project string, err error) {
	switch {
	case ref.host == "github.com":
		apiURL := "https://api.github.com/repos/" + url.PathEscape(ref.owner) + "/" + url.PathEscape(ref.name)
		desc, archived, err = forgeRepoFields(apiURL)
	case strings.Contains(ref.host, "gitlab"):
		apiURL := "https://" + ref.host + "/api/v4/projects/" + url.PathEscape(ref.slug)
		desc, archived, err = forgeRepoFields(apiURL)
	default:
		// unknown forge: no API — README-only, still a valid plugin
		body, rerr := rawReadme(ref)
		if rerr != nil {
			return "", false, "", rerr
		}
		return readmeHeading(body), false, projectLinkFromREADME(string(body), ref), nil
	}
	if err != nil {
		return "", false, "", err
	}
	// forge descriptions may contain markdown (GitLab certainly does) —
	// show them as plain text
	desc = cleanDescription(desc)
	if body, rerr := rawReadme(ref); rerr == nil {
		project = projectLinkFromREADME(string(body), ref)
		if desc == "" {
			desc = readmeHeading(body)
		}
	}
	return desc, archived, project, nil
}

// githubToken returns an authenticated GitHub API token from the environment
// (GITHUB_TOKEN, then GH_TOKEN) or "" when none is declared — the walk then
// falls back to anonymous pacing and the README fallback.
func githubToken() string {
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}

// gitlabToken returns a GitLab API token from the environment (GITLAB_TOKEN,
// then GITLAB_PRIVATE_TOKEN) or "" when none is declared.
func gitlabToken() string {
	if t := strings.TrimSpace(os.Getenv("GITLAB_TOKEN")); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv("GITLAB_PRIVATE_TOKEN"))
}

// authHeaderForAPI returns the Authorization header value for a forge API URL
// when a matching token is declared in the environment, or "" — the walk then
// falls back to anonymous pacing.
func authHeaderForAPI(apiURL string) string {
	switch {
	case strings.Contains(apiURL, "api.github.com"):
		if t := githubToken(); t != "" {
			return "token " + t
		}
	case strings.Contains(apiURL, "gitlab"):
		if t := gitlabToken(); t != "" {
			return "Bearer " + t
		}
	}
	return ""
}

// forgeRepoFields asks a forge REST API (GitHub /repos/{owner}/{name},
// GitLab /projects/{id}) for the repository description and archived flag.
// 403/429 become errRateLimit so callers can fall back to README-only data.
func forgeRepoFields(apiURL string) (string, bool, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("User-Agent", "asdf-tui catalog-refresh")
	if h := authHeaderForAPI(apiURL); h != "" {
		req.Header.Set("Authorization", h)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return "", false, errRateLimit
	default:
		return "", false, fmt.Errorf("repo %s: status %d", apiURL, resp.StatusCode)
	}
	var repo struct {
		Description string `json:"description"`
		Archived    bool   `json:"archived"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(repo.Description), repo.Archived, nil
}

// rawReadmeURLs lists candidate raw-README URLs for a forge, most likely
// first: HEAD is the default-branch alias on GitHub and GitLab, other
// forges fall back to the usual branch names. Only the common README file
// names are probed (several forges default to README.adoc instead of .md).
func rawReadmeURLs(ref repoRef) []string {
	var base string
	var branches, files []string
	switch {
	case ref.host == "github.com":
		base = "https://raw.githubusercontent.com/" + ref.slug + "/"
		branches = []string{"HEAD"}
		files = []string{"README.md", "readme.md", "README.markdown", "README.adoc", "readme.adoc", "README.rst"}
	case strings.Contains(ref.host, "gitlab"):
		base = "https://" + ref.host + "/" + ref.slug + "/-/raw/"
		branches = []string{"HEAD", "main", "master"}
		files = []string{"README.md", "readme.md", "README.adoc", "readme.adoc"}
	default:
		// bitbucket.org/raw/HEAD, codeberg/gitea raw/branch/<name>, …
		base = "https://" + ref.host + "/" + ref.slug + "/raw/"
		branches = []string{"HEAD", "branch/main", "branch/master"}
		files = []string{"README.md", "readme.md", "README.adoc", "readme.adoc"}
	}
	var out []string
	for _, b := range branches {
		for _, f := range files {
			out = append(out, base+b+"/"+f)
		}
	}
	return out
}

// rawReadme fetches the repository README over the host's raw-content URLs,
// trying the candidates in order and returning the first that answers 200.
func rawReadme(ref repoRef) ([]byte, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	var lastErr error
	for _, u := range rawReadmeURLs(ref) {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "asdf-tui catalog-refresh")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if resp.StatusCode == http.StatusOK && len(body) > 0 {
			return body, nil
		}
		lastErr = fmt.Errorf("readme %s: status %d", u, resp.StatusCode)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no README candidates for %s/%s", ref.host, ref.slug)
	}
	return nil, lastErr
}

// readmeHeading returns the README's first heading as a plain description
// (markdown `#` or AsciiDoc `=` headings, decorations stripped); "" when
// there is none.
func readmeHeading(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "=") {
			continue
		}
		if d := cleanDescription(t); d != "" {
			return d
		}
	}
	return ""
}

var mdLinkRe = regexp.MustCompile(`!?\[[^\]]*\]\((https?://[^\s)]+)\)`)

// markdown cleanup regexes used to turn a README heading into a plain
// description (see cleanDescription).
var (
	mdImageRe     = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdCleanLinkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdEmphasisRe  = regexp.MustCompile(`\*+([^*]+)\*+`)
	// AsciiDoc README forms (README.adoc): image macros `image:URL[alt,…]`
	// and external links `URL[label]` (inverse of markdown).
	adocImageRe = regexp.MustCompile(`image:https?://[^\s\[]+\[[^\]]*\]`)
	adocLinkRe  = regexp.MustCompile(`(https?://[^\s\[\]]+)\[[^\]]*\]`)
)

// errRateLimit marks a GitHub API 403/429 (rate limit) so the catalog refresh
// can fall back to README-only data instead of flagging the repo unreachable.
var errRateLimit = errors.New("github API rate limit exceeded")

// projectLinkFromREADME picks the README link that points at the upstream
// project the plugin wraps. It understands both markdown `[label](url)` and
// AsciiDoc `url[label]` links, skipping markdown/AsciiDoc images, badge
// hosts, the plugin's own repository and the asdf docs, then returns the
// first remaining link — e.g. for asdf-avalanche that is
// github.com/ava-labs/avalanche-cli.
func projectLinkFromREADME(body string, self repoRef) string {
	body = adocImageRe.ReplaceAllString(body, " ")
	for _, m := range mdLinkRe.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 {
			continue
		}
		if strings.HasPrefix(m[0], "!") {
			continue // ![alt](image) — badge/logo, not the project
		}
		u := strings.TrimSpace(m[1])
		if projectLinkCandidate(u, self) {
			return u
		}
	}
	for _, m := range adocLinkRe.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 {
			continue
		}
		u := strings.TrimSpace(m[1])
		if projectLinkCandidate(u, self) {
			return u
		}
	}
	return ""
}

// projectLinkCandidate rejects badge/decorative and self-referencing links,
// keeping genuine project/homepage URLs (github, codeberg, gitlab, …).
func projectLinkCandidate(u string, self repoRef) bool {
	low := strings.ToLower(u)
	for _, bad := range []string{
		"img.shields.io", "actions/workflows", "coveralls",
		"codecov", "travis-ci", "appveyor", "circleci",
		"asdf-vm.com", "example.com",
	} {
		if strings.Contains(low, bad) {
			return false
		}
	}
	for _, suffix := range []string{".svg", ".png", ".jpg", ".jpeg", ".gif", ".ico"} {
		if strings.HasSuffix(low, suffix) {
			return false
		}
	}
	o, r := githubSlug(u)
	if o == "" && strings.Contains(low, "github.com/") {
		return false // github repo we cannot attribute — be safe
	}
	if o == self.owner && r == self.name {
		return false // the plugin's own repository
	}
	if o == "asdf-vm" && r == "asdf" {
		return false // the asdf version manager itself
	}
	// same forge + same full path (also catches nested GitLab groups and
	// self-links on forges githubSlug cannot parse)
	if c, ok := parseRepo(u); ok && strings.EqualFold(c.host, self.host) &&
		strings.EqualFold(c.slug, self.slug) {
		return false
	}
	return true
}

// githubSlug extracts owner/repo (first two path segments) from a GitHub URL,
// tolerating extra path segments, .git suffixes and ssh:// or git@ forms.
func githubSlug(u string) (string, string) {
	u = strings.TrimSpace(u)
	for _, pre := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		if strings.HasPrefix(u, pre) {
			u = strings.TrimPrefix(u, pre)
			break
		}
	}
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimRight(u, "/")
	parts := strings.SplitN(u, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// cleanDescription strips markdown decorations (badge images, links — keeping
// their text — emphasis, code markers and leading #) from a README heading so
// the catalog description is a plain one-liner instead of e.g. the whole
// header line with `![Build](…badge.svg) ![Lint](…badge.svg)` appended.
func cleanDescription(s string) string {
	s = mdImageRe.ReplaceAllString(s, " ")
	s = mdCleanLinkRe.ReplaceAllString(s, "$1")
	s = mdEmphasisRe.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, "#=")
	return strings.TrimSpace(s)
}
