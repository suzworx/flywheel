# The factory view

`flywheel factory` on a terminal opens the interactive factory: a live view of the floor laid out
like k9s (issue #583). `flywheel factory --plain` redraws the plain floor instead, and
`flywheel factory --once` (or `--json`) prints it once and exits; neither changes with this layout.

## The screen

From top to bottom:

- **The header** (Ctrl-E hides it). The left column is the factory's context:
  - `repo <dir> · <integration branch>`
  - `factory running`, or `factory FROZEN since HH:MM until HH:MM by <session>: <reason>` while
    `flywheel suspend` holds it (no `until` when it lasts until `flywheel resume`)
  - one `paused <model> until HH:MM` line per model a rate limit pauses
  - `lead <session> · workers <busy>/<max> · landed today <n> · $<cost>`, the cost (like the
    middle column's spend) compact rather than cut: `$7.25` under $100, then `$711`, `$1.2k`, `$3.4M`
  - `health <age> ago` for the latest health event, or `health none`
  - `flywheel <version>` (`dev` for a local build)

  The middle column is the last 24 hours' numbers (see [Metrics](#metrics)):
  `throughput <n>/24h` with a sparkline of the landings per hour, `wip <n>`, `first-pass <n>%`,
  `andons <n>`, `spend 24h $<n>` and `workers <busy>/<max> busy`. The live view computes them at
  most every 30 seconds, so a key press never waits for them. The column shows only while the
  whole key menu still fits beside it; a narrow terminal drops it first.

  The right column is the key menu of the screen shown:
  `<:> view  </> filter  <enter> why  <l> log  <?> help  <q> quit  <ctrl-e> header ...`.
  It takes four rows, or five when four do not fit, and as many columns as fit beside the context
  column (which keeps at most three fifths of the width, its long lines cut with `~`). A hint is
  never cut: when five rows still do not fit, whole hints are dropped from the end and `<?> help`
  stays, since help lists every key. Each view lists only the keys valid there, so the workers view
  has no `<enter> why`.
  A frame too short to keep five table rows below the header drops the header on its own.
- **The title bar**: `── Units(all)[3] ──`, the view, its filter and its row count, then the sort
  when one is set (`── Units(/build)[2] ↑stage ──`); in a unit's detail the tab and the unit,
  `── Why T3 ──`, `── Explain T3 ──`, `── Brief T3 ──`, `── Log T3 ──`, `── Checkpoints T3 ──`,
  `── Findings T3 ──` or `── Events T3 ──`; in another drill-down `── Learning L-02 ──`,
  `── Checkpoint T3/2 ──` or `── Views ──`; `── Help ──`
  in help. The search view's title names its text: `── Search "disk full"(all)[12] ──`. The
  metrics views name their window: `── Pulse 24h ──`, `── Metrics 7d(all)[24] ──`,
  `── Metric lead time 24h ──`.
- **The table**, the drill-down's lines or the help text. The cursor row starts with `>`.
- **The flash line**: the latest message (an unknown view, a toggle, a reload). The next key
  clears it, or it clears itself after five seconds.
- **The crumbs** (Ctrl-G hides them): the navigation path as tags, e.g. `<units> <T3> <log>`.
  While a prompt is open the last line is the prompt, `:` or `/` followed by what you typed.

Hiding the header or the crumbs gives their lines to the table.

## Keys

| Key | Where | What it does |
| --- | --- | --- |
| `j` / Down, `k` / Up | table, drill-down, help | move the cursor, or scroll |
| `g` / Home, `G` / End | table | first row, last row |
| PgDn, PgUp | table, drill-down, help | a page down or up |
| `:` | table | the view prompt: a view by name or alias (see [Views](#views)), `s <text>` to search, `q` to quit |
| `/` | table | the filter prompt; the table filters as you type, Enter keeps it (see [Filters](#filters)) |
| Enter | units, andon, tree | the unit's detail, at its why tab (see [Unit detail](#unit-detail)) |
| Enter | learnings, checkpoints, search | the learning in full, the checkpoint's changed paths, the result's unit at the match |
| `l` | units, andon, tree | the unit's detail at its log tab |
| `w` `d` `y` `l` `c` `F` `e` | unit detail | switch tab: why, explain, brief, log, checkpoints, findings, events |
| `J` (Shift) | unit detail | open the detail of the unit's first need that has not landed |
| `f` | drill-down | fullscreen: hide the header and the crumbs and give their lines to the drill-down; again to leave |
| `G` (Shift) | drill-down | the last line; in the log tab, follow the unit again |
| `w` `t` | log tab | wrap long lines (instead of cutting them); show or hide the timestamps. In the log tab `w` wraps, so the why tab is Esc then Enter away |
| `1` `2` `3` | pulse, metrics | the window: 24h, 7d, 30d |
| arrows, `h` `j` `k` `l` | pulse | move between the panels |
| Enter | pulse, metrics | the metric's drill-down: the panel's first metric, the row's metric (see [Metric drill-down](#metric-drill-down)) |
| `h` `u` | metric drill-down | the chart, the units behind the number |
| Enter | metric drill-down, units | the unit's detail at its why; Esc comes back to the units |
| Esc | everywhere | leave the drill-down, help or prompt; in a table, clear the filter, else go back to the previous view |
| `-` | table | swap to the previous view, and back |
| `[` / `]` | table | step back / forward through the `:` commands entered |
| `N` `A` `S` `C` (Shift) | table | sort by the first column, age, stage (or state), cost; again flips the direction |
| Ctrl-A | everywhere but a prompt | list every view and alias in the drill-down pane |
| Backspace | prompt | delete the last character |
| `?` | table, drill-down, help | show or leave help |
| `q` | table, drill-down, help | quit (in a prompt it is typed) |
| Ctrl-C | everywhere | quit |
| Ctrl-E | everywhere | show or hide the header |
| Ctrl-G | everywhere | show or hide the crumbs |
| Ctrl-W | everywhere | wide: show every cell whole; normally a cell before a row's last is cut to 40 characters with `~` |
| Ctrl-R | everywhere | reload now: read the whole ledger afresh instead of waiting for the next refresh |

The terminal delivers Ctrl-A to Ctrl-Z as control bytes; Ctrl-C, Ctrl-H (Backspace), Ctrl-I (Tab)
and Ctrl-J / Ctrl-M (Enter) keep their usual meaning. Ctrl-Space and `Ctrl-\` are decoded too,
for the keys to come.

## Views

| View | Alias | Rows |
| --- | --- | --- |
| `units` | `u` | every unit: stage, attempt, session, model, steps, age, state, and last its why (see [Unit detail](#unit-detail)) |
| `workers` | `w` | the worker lines and the staffed roles |
| `andon` | `a` | the units that stopped the line |
| `events` | `e` | the recent events, newest first |
| `lines` | `l` | the product lines |
| `tree` | `t` | the `needs` tree of every unit not landed, like k9s xray: each unit no other open unit needs is a root, its needs are its children (`├─`, `└─`), each node `task stage`; a landed need is a leaf, a cycle is cut and marked `cycle` |
| `health` | `h` | the recent health events: time, age, running, stalled, rate-limited, andon, paused models |
| `learnings` | `lr` | the learnings, newest first: id, title, severity, task, dismissed |
| `checkpoints` | `c` | `refs/flywheel/checkpoints/<task>/<attempt>`: task, attempt, sha, files, age (read from git as the view opens and on Ctrl-R) |
| `pulse` | `p` | the metrics dashboard: six panels (see [Pulse](#pulse)) |
| `metrics` | `m` | every metric: family, metric, value, trend, change, definition (see [Metrics table](#metrics-table)) |
| `search` | `s <text>` | the search results (see [Search](#search)) |

An unknown name flashes `unknown view :x (Ctrl-A lists them)`.

## History

Every view you open with `:` goes on a stack, and the crumbs show it: `<units> <workers> <andon>`.
Esc goes back one level (after clearing the view's filter, if it has one), `-` swaps the current
view with the previous one, and `[` / `]` re-run the previous / next `:` command you entered. A view
keeps its cursor, filter and sort: coming back to it restores them.

## Filters

- `/text`: a case-insensitive regular expression over the row's cells. An invalid expression
  matches literally and the flash line says `literal match`.
- `/!text`: the inverse, the rows the expression does not match.
- `/-f text`: fuzzy, the rows holding every character of `text` in order.

## Sort

Shift-N sorts by the first column (METRIC in the metrics table), Shift-A by age, Shift-S by stage (or state where there is no
stage), Shift-C by cost where the view has a cost column (none has yet; the flash says so). The
same key again flips the direction; the title bar shows it (`↑stage`, `↓stage`). The sort is
stable, so rows with equal keys keep their order. Ages sort as durations and numbers as numbers.
Sorting by the column under a column cursor (k9s Shift-O with Shift-Left/Right) is not there: the
terminal decoder does not report Shift-Left/Right. The tree keeps its order.

## Unit detail

Enter on a unit opens its detail. The first line is its why (below), then the tab's lines. A single
key switches the tab, and the crumbs name it (`<units> <T3> <brief>`):

| Key | Tab | Lines |
| --- | --- | --- |
| `w` | why | the why and the timeline (the tab Enter opens) |
| `d` | explain | `flywheel explain` of the unit |
| `y` | brief | the brief's text, the file its latest planned, amended or dispatched event names |
| `l` | log | the unit's events as readable lines |
| `c` | checkpoints | its `refs/flywheel/checkpoints/<task>/<attempt>` and their changed paths (read from git as the tab opens and on Ctrl-R) |
| `F` | findings | its review findings, `OPEN` or `closed`, each followed by the responses to it |
| `e` | events | its events as the ledger records them |

Shift-J opens the detail of the unit's first need that has not landed; Esc returns to the list,
the cursor where it was.

### Why

One or two plain sentences, from the ledger's facts only, the same on every machine (times in
UTC). They name the state, the reason and the next step as `flywheel recover` would:

- planned: `blocked: needs k1, which has not landed.`, or `queued: planned and its needs are met;
  next: flywheel run k2.` (or that the factory is frozen)
- dispatched: `queued: attempt 2 dispatched 3m ago, waiting for the worker to start.`
- running: `building: attempt 2, 14 steps, running for 23m.`; `stalled` or `silent` when the floor
  says so or nothing happened for longer than the worker's stall timeout, with the timeout
- finished: `rate-limited` (the model and its reset), `capped` (the output cap and the peak
  reasoning), `suspended` (frozen by suspend at a time; resumes at the thaw or on flywheel resume),
  `failed` (the finish reason), `validation failed` (the failing gates and the first failing
  command), `needs correction` (the open blocking findings), `finished, awaiting validation`,
  `validated, awaiting inspection` or `validated, awaiting the review panel`
- `passed, awaiting landing`, `landed as <commit> (PR #n)`, `withdrawn: <note>`, `lost`

The units table shows it as its last column, WHY, cut to the frame; the detail has it whole.

### Timeline

Every event of the unit in order, and a `·· <length> <what>` row for each gap of more than five
minutes between two of them:

- `attempt N running`: an attempt was dispatched or started and had not finished
- `waiting for the rate-limit reset at HH:MM UTC`: the gap follows a rate-limited finish
- `frozen by suspend`: a factory suspension overlapped the gap
- `waiting for validation`: the gap follows a finish
- `waiting for inspection`: the gap follows a passing reading (owns checked or a gate passed)
- `idle`: anything else

The last line sums it up: `total` from the first event to the last, `touch` the attempts' run time
(each dispatch or start to its finish or loss) and the `flow efficiency`, touch over total.

## Search

`:s <text>` (or `:search <text>`) searches, case-insensitively, the ledger's events, the run logs
(`.flywheel/runs/*.jsonl`), the reports (`.flywheel/runs/*.report.md`) and the briefs
(`.flywheel/briefs/*.txt`), and shows one row per matching line: SOURCE (`event`, `log`,
`report`, `brief`), TASK, ATTEMPT, LINE (an event's line in the ledger) and an excerpt, the match
with about 40 characters on each side. It stops at 500 results and the title says
`500+ (narrow the search)`. Enter on an event opens the unit's log at the match; on any other
result, the unit's detail at its explain tab. The search runs when you enter it and on Ctrl-R, never on every
refresh.

## Metrics

The metrics views read the factory's lean metrics ([metrics.md](metrics.md)) over a window: `1`
the last 24 hours (1-hour buckets, the default), `2` the last 7 days, `3` the last 30 days (1-day
buckets). A trend arrow compares a value with the same metric over the previous window of equal
length: `↑` higher, `↓` lower, `→` equal. Every number opens its chart, and the chart the exact
units behind it.

### Pulse

`:pulse` (`:p`) is six panels in a grid: three columns at 110 cells or wider, two at 72 or wider,
one below.

| Panel | Lines |
| --- | --- |
| FLOW | landed, wip, lead p50/p90 |
| QUALITY | first-pass yield, rework, gate fail rate, findings per reviewed unit |
| RELIABILITY | andons, MTTR, frozen, paused |
| COST | spend, cost per landed unit, tokens per step |
| CAPACITY | utilization, idle |
| BY MODEL | a bar per model: its cost per accepted unit (`–` when none of its units was accepted) and its spend; a narrow panel drops the spend, never cutting an amount |

Each line is the value, the sparkline of its per-bucket series (landed, wip, andons and spend
have one) and its trend arrow. The panel under the cursor is marked `▶`; the arrows or `h` `j`
`k` `l` move it, and Enter opens the drill-down of the panel's metric: lead time for FLOW, the
first line's metric for QUALITY, RELIABILITY and CAPACITY, cost per landed unit for COST, the
by-model scoreboard for BY MODEL.

### Metrics table

`:metrics` (`:m`) lists every metric: FAMILY, METRIC, VALUE, TREND (the sparkline, where the
metric has a series, and the arrow), Δ PREV (the change from the previous window, `=` when none)
and DEFINITION (its one line from [metrics.md](metrics.md)). Shift-N sorts by METRIC; `/` filters.
Enter opens the metric's drill-down.

### Metric drill-down

The value, its trend and change, and its definition, then one of two parts; the crumbs name it,
`<units> <pulse> <lead time> <units>`:

- `h`, the chart (the part it opens at): a histogram with the p50 and p90 marked for lead, queue
  and touch time and MTTR; a control chart (mean and ±2σ, units in landing order) for cycle time;
  the WIP over time as a flow; bars for a breakdown (gate fail rate per gate, andons by kind,
  pause per model, cost per unit by model, utilization per worker) and for a split of the units
  (first pass or corrected, severity, landed or not); else the series as one wide sparkline.
- `u`, the units behind the number (its [evidence](metrics.md#evidence)): TASK, VALUE (the
  unit's part, e.g. `lead 9h12m`, `corrected x2`, `stalled 09-02 13:58, open`, `$3.40`) and GROUP,
  worst first. `j` `k` move the cursor; Enter opens that unit's detail at its why and timeline,
  and Esc from it comes back to the same row.

## Instant keys

A key never waits for the ledger. The view redraws at once from the data it last read; reading
runs in the background, every refresh interval, and on Ctrl-R, and the frame redraws when it ends.
A key that needs other data (another view, a unit's tab, a search, a metrics window) starts a
read too, and until it arrives the drill-down says `loading…`. Only one read runs at a time: a
refresh or Ctrl-R asked for while one runs starts right after it. Only the first frame waits for
its data.

## Colours

With colour on, each row of the units, andon and tree tables is drawn whole in the colour of its
state (a landed unit by its stage, else by its STATE, else by its STAGE):

| Colour (dark skin) | States |
| --- | --- |
| cyan | running, exploring, long-step |
| green | passed, done, validated |
| yellow | waiting, planned, dispatched, finished, blocked, needs-correction, rate-limited, stacked |
| red | failed, stalled, silent, capped, provider-error, mismatch, no-writes, lost |
| magenta | suspended, frozen |
| dim | landed, withdrawn |

The header's `factory running` takes the running colour and `factory FROZEN` the frozen one; the
key menu's `<key>`s and the crumbs have their own colours. The cursor row is in reverse video.

### Changed rows

Like k9s's MODIFIED and NEW markers, a row of the units, andon, tree, workers or lines table whose
cells changed since the previous read, or that was not there before, is drawn bold for two
refreshes. Its age and its WHY move with the clock, so they alone do not count as a change. The
first read marks nothing.

### Skins

`factory.skin` in `.flywheel/config.json` picks the colours: `dark` (the default), `light` (darker
colours for a light terminal), or `none` for no colour at all (the cursor's reverse video
included). Any other value is refused when the config is read.

```json
{ "factory": { "skin": "light" } }
```

`flywheel factory --plain`, `--once` and `--json` do not use the skin.

## The log tab

A running unit's log follows it: the last line stays in sight as lines arrive. Scrolling up (`k`,
Up, PgUp) pauses that and the title says `paused; G to follow`; `G` follows again. A unit that
does not run opens its log at the first line. `w` wraps long lines instead of cutting them, `t`
hides or shows the timestamps, and `f` (as in every drill-down) goes fullscreen.

## Coming next

- filtering by column, and sorting by the column under a cursor
- actions on the unit under the cursor
- marks, to act on several units at once
- a read-only mode that disables every action
- hotkeys for the views used most
