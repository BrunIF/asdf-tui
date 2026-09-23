# asdf-tui — план роботи (Go)

Каталог: `/home/brun/Development/asdf-tui`
Збірка зараз: `go build` = 0, `go vet` = 0, `gofmt -l` = порожньо,
`go test ./...` = ok.

## Якорі (підтверджені детерміновано)
- `data/plugins.yaml` — єдиний формат каталогу (TSV прибрано з `catalog.go`/`main.go`).
- `catalog.go`: `Plugin{Name, Desc, Repo, Unavailable, Archived, Removed, Project}`;
  `saveCatalogYAML`/`loadCatalogYAML` (emit: `unavailable/archived/removed/project/desc/repo`).
- `asdf.go`: `asdfPluginListAll`, `parseRepo(URL) → repoRef{host,owner,name,slug}` (github/gitlab/будь-який форж, `*`-префікс, ssh-форми),
  `pluginRepoInfo(ref) (desc, archived, project, err)` (GitHub API `/repos`, GitLab API `/projects`, інші — README-only),
  `forgeRepoFields`, `rawReadme`/`rawReadmeURLs`/`readmeHeading`, `projectLinkFromREADME(body, self)`.
- `ui.go` (bubbletea): `verMode{verNone,verInstall,verSet,verScope}`, `toolSt{added,versions,loaded}`,
  `versionsCmd`→`toolAllVersions`, `doTaskCmd`, `fuzzyMatch`, `verFilter`, `verSel`, `verTop`.

Підтверджений факт: у третій стовпець (`verItems`) потрапляє **повний** список версій
(`m.verItems = sortVersionsDesc(msg.versions)` — без обрізання `[:N]`).

## Проблема
Користувач не може дістатися версій за межами екрана (напр. 9.0.1, коли на екрані 20..10):
третій стовпець не має ні сторінкового паджера (крапки, як у першому), ні пошуку, ні переходу за
видимий край.

## План (стан: виконано)
1. **Пагінація третього стовпця (крапки, як у першому).** ✅
   - `renderDots` малює `●/·` під списком версій; поточна сторінка з `verSel`.
   - `PgUp/PgDn` гортають на одну сторінку через `verPageSize()` (рівно висоті списку).
2. **Пошук у третьому стовпці.** ✅
   - Букви фільтрують живцем (`verFilter` через `fuzzyMatch`); `/` перезапускає
     фільтр; `Esc` спершу очищає фільтр, за порожнього — виходить із стовпця.
3. **Підтвердження не виправляє мережу — офлайн-режим.** ✅
   - `pluginRepoInfo` має 8s таймаут; якщо мережа недоступна → `Unavailable=true`
     (плагін сіріє), а не падає. Це очікувана поведінка без мережі.
4. **Каталог: temp-diff-merge (недеструктивний refresh).** ✅
   - `mergeCatalogDiff`: плагін, що зник з `asdf`, лишається в YAML з
     `removed: true` + `unavailable: true` (+ іконка 🗑 після назви).
   - Архівовані репо → `archived: true` (+ 🔒), недоступні → `unavailable: true` (+ 🚫).
5. **Проєкт-лінк з README.** ✅ `project:` у YAML через `projectLinkFromREADME`.
6. **TLI іконки станів.** ✅ `pluginIcon(Plugin)`: 🔒/🗑/🚫 після назви плагіна
   (лівий стовпець + права панель).
7. **Перевірка кожного кроку лише exit-кодами:** ✅
   `go build`, `go vet`, `gofmt -l .` та `go test ./...` — зелено.

### Додатково полагоджено
- **Ламанний YAML-лекер** (`catalog.go`): після `TrimSpace` префікси перевірялися
  з подвійним пробілом і ніколи не збігалися — desc/repo/флаги губилися на load.
- **Поломанний тест** (`catalog_test.go`): `mergeCatalog` → `mergeCatalogDiff`
  (тест тепер компілюється і зеленіє).
- **GitHub rate limit** (refresh): pacing між перевірками репо через
  `repoDelay()` (`ASDF_TUI_REPO_DELAY_MS`, за замовч. 300 ms, concurrency 4);
  403/429 → fallback на README (`rawReadme` + `readmeHeading`,
  raw.githubusercontent не рейтлімітиться) замість `unavailable`.
- **GitLab/інші форжі** (adr-tools): `parseRepo` розуміє gitlab.com (+ ssh, `*`-префікс,
  вкладені групи; self-hosted gitlab через наявність "gitlab" у хості);
  GitLab-опис через API `/api/v4/projects/{id}` + `cleanDescription` (markdown-лінки в ньому),
  проект-лінк і заголовок README через `/-/raw/HEAD/…`; codeberg/bitbucket/gitea — README-only
  без позначки unreachable, якщо README досяжний. Опис форж-API проходить `cleanDescription`.
- **README.adoc / AsciiDoc** (age-plugin-yubikey): `rawReadmeURLs` пробує також
  `README.adoc`/`readme.adoc` (+ `README.markdown`/`.rst` на GitHub); `projectLinkFromREADME`
  розуміє AsciiDoc-форми `URL[label]` (інверсія markdown) і вирізає `image:URL[alt,…]` макроси;
  `readmeHeading` читає `= заголовок`; бейджеві макроси `img.shields.io` відхиляються.
- **Селектор `›` у лівому стовпці**: Дефолтний bubbles-делегат різав заголовок
  по клітинках умовно (міг розрізати 2-клітинний емодзі навпіл) і підсвічував
  лише середню частину рядка. Замінено на власний `toolDelegate`:
  - назва не стрибає (виділений `› ` = звичайний `  `, початок на колонці 2);
  - підсвітка завжди на всю ширину колонки — іконка стану повністю на виділеному фоні;
  - обрізка довгих назв `truncateCells` — ніколи не розрізає широкий гліф навпіл.
- **Refresh окремого плагіна (TUI)**: новий екшн `7 · Refresh plugin info`
  (Remove переїхав на `8`). Спільний `refreshOnePlugin(p)` у `main.go` (форж-API →
  403/429 → README fallback) використовують і `fillPluginRepoInfo`, і
  `refreshPluginCmd`; результат оновлює рядок у пам'яті, лівий стовпець
  (`syncToolItems`) і зберігається в `data/plugins.yaml` через `saveCatalogYAML`.
  Якщо `asdf plugin list all` досі містить плагін — `Removed` (🗑) знімається.
- **Архівованість не губиться між повними refresh**: тимчасовий хак
  `old.Archived → f.Removed = true` перетворював 🔒 на 🗑 у YAML при
  рейт-ліміті (README-fallback не знає про archive). Тепер `reported verified`
  (API відповів) позначає рядки, для яких `Archived` авторитетний; для
  fallback/failure зберігається останнє відоме `archived: true`, а провал
  проби не стирає `desc`/`project`. (argocd-image-updater було позначено
  🗑 помилково — сам репо в `asdf plugin list all`; тепер `archived: true`, 🔒.)
- **Опис проєкту (`project_desc:`)**: нове поле YAML + середній стовпець.
  `projectRepoDescription(project)` — best-effort запит до форж-API upstream-
  проєкту (GitHub/GitLab тільки; рейт-ліміт/чужий форж/сайт → пусто, плагін
  не падає). Заповнюється і в `catalog-refresh`, і в TUI-екшні
  `Refresh plugin info`. Порядок блоку: назва → статус → опис плагіна →
  `plugin: <repo>` → `  — <опис проєкту>` → `project: <url>`; фіксована
  висота (7 рядків) — екшни не стрибають, два описи ніколи не зливаються
  (опис проєкту з відступом за `—`).
- **Спільний rate-limiter для форж-API**: `rateLimiter` (asdf.go) видає токен
  раз на `repoDelay()`; ЧЕРЕЗ ОДИН limiter проходять і запит репо плагіна, і
  запит опису проєкту (`refreshOnePlugin(p, rl)` — `rl.wait()` перед кожним).
  Bulk-walk тримає один загальний limiter, TUI-`Refresh plugin info` створює
  приватний з тією ж затримкою — ні сплеск refresh, ні повна прогулянка не
  можуть бити GitHub/GitLab швидше за 1 REST-виклик на `repoDelay`
  (за замовч. 300 ms). При rate-limit опис проєкту не запитується (він би
  однаково 403); `ASDF_TUI_REPO_DELAY_MS` продовжує регулювати інтервал.
- **Підтримка токенів форжів**: `authHeaderForAPI(apiURL)` +
  `githubToken()`/`gitlabToken()` — якщо оголошено `GITHUB_TOKEN`/`GH_TOKEN`
  (GitHub) чи `GITLAB_TOKEN`/`GITLAB_PRIVATE_TOKEN` (GitLab), запити до
  відповідного форжу йдуть з `Authorization`-заголовком (`token …` /
  `Bearer …`), що підіймає ліміт з ~60/год (анонім) до 5000/год — archived
  реально збирається. Якщо токен не оголошений — як і раніше: pacing +
  README-fallback (без заголовка, чужий форж на кшталт codeberg — завжди
  анонім). У note-повідомленні `catalog-refresh` згадка про токени
  показується лише коли токена немає.
- **Баг: провалений probe стирав дані**: `fillPluginRepoInfo` при помилці не
  ставив `pl.Unavailable` — гілка `if f.Unavailable` у `mergeCatalogDiff`
  ніколи не спрацьовувала, тому недоступний плагін перезаписувався з
  ПОРОЖНІМ desc, 🚫 не виставлявся, а таллі «✗N» розходились з YAML
  («0 unavailable»). Виправлено: `pl.Unavailable = true` у else-гілці;
  додатково `!verified`-шлях тепер зберігає `ProjectDesc` (поки `project`
  не змінився) і при провалі — `Desc`/`Project`. Тест
  `TestMergeKeepsKnownDataOnFailure`.
- **README project-link парсер**: пропускає `![…]` картинки/бейджі, їх цілі
  (`actions/workflows`), shield-хости, власний репо плагіна (avalanche → ava-labs/avalanche-cli).
- **Фільтри каталогу (модалка ctrl+f)**: лівий стовпець звужується до
  підмножини каталогу — усі, додані в asdf (`addedSet`), активні (не
  archived/removed/unavailable), 🔒 archived, 🗑 removed, 🚫 unreachable.
  Реалізовано через `filteredPlugins()`/`matchesFilter()`: `syncToolItems()` і
  `setFilter()` перебудовують `list.Model` з фільтрованої підмножини, курсор
  зберігається за назвою плагіна, якщо він залишився, інакше стрибає на
  перший видимий рядок; текстовий пошук працює поверх підмножини.
  (bubbles v1.0.0 `list.Model` не має `Count()`, тому перевірки — через
  `len(m.tools.Items())`; `ResetFilter()` перед перебудовою очищає пошук.)
- **Рангований текстовий пошук**: кастомний `m.tools.Filter` (`rankPlugins`/
  `pluginSearchScore`) замість fuzzy-deфолту — шукає по назві, `project`,
  `desc` і `project_desc` (кожен токен має співпасти хоча б в одному полі),
  ранжує: точна назва > префікс назви > підрядок назви > проєкт > опис
  проєкту > опис, + бонус за мультисловний запит повністю в назві. Назви
  завжди вище за desc-only. `FilterValue()` розширено до
  `name project desc project_desc`. Тест `TestRankPlugins`.
- **Модальний вибір фільтра (ctrl+f)**: `filterOpen`/`filterSel` +
  `filterModalKey`: `ctrl+f` відкриває модалку поверх екрана (курсор
  стартує на активному фільтрі, рядок з `●` = активний), `↑/↓` або цифри
  `1–6` обирають, `Enter`/пробіл застосовують, `Esc`/`q` скасовують. Це ЄДИНИЙ
  шлях перемикання фільтра (альтернативні F1–F6 прибрано на прохання
  користувача). Оверлей — `overlayBox`/`overlayLine` по центру екрана; ширини
  рахуються через `github.com/charmbracelet/x/ansi` (`StringWidth`/`Truncate`),
  тому рамки `RoundedBorder` (U+2500) і широкі гліфи ніколи не розрізаються
  (своя евристика `rune>0xff → 2 клітинки` для рамки — невірна; для
  бокс-дроу потрібен вірний wcwidth).

## Порядок виконання
1. Побачити поточний рендер `View` третього стовпця. ✅
2. Додати паджери-крапки + клавіші сторінок у третій стовпець. ✅
3. Додати `/`-пошук по версіях. ✅
4. `go build && go vet && gofmt -l .` → зелені. ✅
5. Оновити цей файл: відмітити виконане. ✅

## Заборонено
- Писати навмання без assert-гейта (середовище псує багаторядкові відбитки).
- Перезаписувати каталог цілком (видалені плагіни лишаються з `removed: true`).
- Покладатися на текстовий вивід bash замість exit-кодів.
