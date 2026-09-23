# asdf-tui

A fast, flicker-free, column-style TUI (Go + [bubbletea](https://github.com/charmbracelet/bubbletea))
for discovering, installing, updating and removing tools managed by
[asdf](https://asdf-vm.com).

Type to search the catalog **fzf-style** (live match on name, project link,
description and project description, ranked — name matches surface first), then
cascade through **three columns**: tools → actions → versions.

## Installation

The easiest way — one command, no manual steps, the script detects your OS
and CPU, downloads the matching prebuilt binary from the latest GitHub
Release to your `/usr/local/bin`, and verifies it runs:

```bash
bash <(curl -sfL https://raw.githubusercontent.com/BrunIF/asdf-tui/main/install.sh)
```

That works on **Linux and macOS** (x86-64/amd64 and arm64). The tool goes to
`/usr/local/bin/asdf-tui`, so `sudo` will be prompted only when that directory
is not writable by your user.

If you prefer to see everything explicitly (or curl is unavailable), install
by hand:

```bash
# 1) find the latest version and the binary name for your machine
AR=amd64                        # use arm64 on Apple Silicon / ARM
BIN=asdf-tui-<VERSION>-$(uname -s | tr 'A-Z' 'a-z')-$AR
#    e.g. asdf-tui-1.2.4-linux-amd64

# 2) download it from the release into /usr/local/bin
curl -sfL -o /tmp/$BIN \
  https://github.com/BrunIF/asdf-tui/releases/download/v<VERSION>/$BIN
chmod +x /tmp/$BIN
sudo install -m 0755 /tmp/$BIN /usr/local/bin/asdf-tui
rm /tmp/$BIN

# 3) confirm it is alive
asdf-tui version
```

## Requirements

- **Go 1.24+** to build
- **asdf** in `PATH` at runtime
- a terminal emulator (uses the alternate screen; no fzf needed)

```bash
go build -o asdf-tui .
```

## Usage

```bash
./asdf-tui                 # interactive TUI
./asdf-tui list            # installed plugins + versions (CLI)
./asdf-tui update          # update every added plugin (CLI)
./asdf-tui plugins         # list added plugins (CLI)
./asdf-tui add NAME [REPO] # add a plugin (asdf plugin add) + append to catalog
./asdf-tui catalog-refresh # rebuild data/plugins.yaml from `asdf plugin list all`
./asdf-tui help            # help
```

`catalog-refresh` paces every forge API call — the plugin repo and its
upstream project description both share one rate limiter (300 ms between
calls by default; tune with `ASDF_TUI_REPO_DELAY_MS`) so GitHub's/GitLab's
rate limit does not blank the catalog. If the API still answers 403/429, the
plugin falls back to README-only data (description + project link, `archived`
left unknown) instead of being
marked unreachable.

**Tokens.** Anonymous GitHub/GitLab APIs are capped at ~60 requests/hour, so
a full walk mostly falls back to README data and the `🔒 archived` flag stays
unknown. Declare a token to raise the limits (GitHub 5000/hr): `GITHUB_TOKEN`
(or `GH_TOKEN`) for GitHub hosts and `GITLAB_TOKEN` (or
`GITLAB_PRIVATE_TOKEN`) for GitLab hosts. When a matching token is set it is
sent as the `Authorization` header automatically; without one the walk simply
stays paced and anonymous.

## TUI columns and keys

**Left column — tools catalog.** Just start typing; the list filters live.
`↑/↓`, `j/k` navigate. `r` remove (asks for confirmation). `q` quits (when the
search field is empty); `Esc` clears the search. `Enter` moves to the actions
column. Rows show just the name plus a state icon: `🔒` archived repo, `🗑`
dropped from the catalog, `🚫` unreachable.

**Filtering the catalog (`ctrl+f`).** The columns can be narrowed to a subset
of the catalog; the type-to-search filter keeps working on top of the subset.
**`ctrl+f`** opens a modal chooser over the screen center (`↑/↓` or `1`–`6` to
pick, `Enter` to apply, `Esc`/`q` to cancel — the row marked `●` is the active
filter):

| Subset          |
| --------------- |
| all plugins     |
| added to asdf   |
| active (not archived/removed/unreachable) |
| archived (`🔒`) |
| removed (`🗑`)  |
| unreachable (`🚫`) |

Switching subsets keeps the cursor on the currently selected plugin when it
survives (otherwise it jumps to the first visible row) and clears the
type-to-search filter so the new subset is shown in full. The modal is drawn
with cell-accurate widths, so box-drawing borders and emoji icons are never
split.

**Middle column — tool details + actions.** A fixed-height block on top always
shows, in order: the name, the repository status (active/archived/removed/
unreachable plus whether the plugin is added to asdf), the plugin's one-line
description, the plugin repo link, the upstream project's description (collected
from the project's forge API; indented behind a `—` so the two descriptions
never merge), and the upstream project link. Because the block is fixed-size
and every line has its own slot, the action list below never jumps. `↑/↓`,
`j/k`, `1`–`8` select; `Enter` runs; `Esc` returns to the tools column.

| # | Action |
| - | ------ |
| 1 | Install a specific version (opens the versions column with its own fuzzy filter; `L` installs the latest resolved version) |
| 2 | Install the latest stable version |
| 3 | Add plugin — `asdf plugin add NAME [REPO]` (needed before the version list can be fetched) |
| 4 | Set a default version — pick an **installed** version, then a scope |
| 5 | Update the plugin |
| 6 | Reshim |
| 7 | Refresh plugin info — re-fetch desc/archived/project for this plugin (same rate-limit → README fallback as `catalog-refresh`) and save to the YAML catalog |
| 8 | Remove the plugin (with confirmation) |

**Right column — versions.** `↑/↓`, `j/k` navigate, `PgUp/PgDn` turn pages
(●/· indicator under the list). Letters filter live; `/` restarts the filter,
`Esc` clears it (or walks back through the columns: versions → actions →
tools). `Enter` runs the chosen action.

Setting a default offers three scopes (`1/2/3`, then `Enter`):

- **user** — `~/.tool-versions` (`asdf set -u`, legacy `asdf global`)
- **folder** — `./.tool-versions` (`asdf set -p`, legacy `asdf local`;
  the file is created when missing)
- **system** — `/etc/asdf/tool-versions` (via `sudo` when not writable)

`asdf` ≥ 0.16 is auto-detected (`asdf set -u/-p`, `asdf list all`); older
versions fall back to `global`/`local`/`list-all`.

## Layout

```text
asdf-tui/
├── main.go          # CLI dispatch + TUI entry
├── ui.go            # bubbletea model: columns, keys, screens
├── asdf.go          # asdf operations + set scopes + version detection
├── catalog.go       # catalog loading (data/plugins.yaml)
└── data/
    └── plugins.yaml # catalog: name, desc, repo, flags, project
```

## Roadmap (not yet implemented)

- **Repository-backed catalog** — move `data/plugins.yaml` into its own repo so
  the description list is always current and searchable online.
- **Multi-version install** — mark several versions in the version list and
  install them in one go.
- **Multilingual UI** — descriptions are English by default; i18n of the UI and
  descriptions is planned.
- Categories/tags for the catalog, history of installed tools, diff of current
  versions vs latest.