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
  - `lead <session> · workers <busy>/<max> · landed today <n> · $<cost>`
  - `health <age> ago` for the latest health event, or `health none`
  - `flywheel <version>` (`dev` for a local build)

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
  in help. The search view's title names its text: `── Search "disk full"(all)[12] ──`.
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

Shift-N sorts by the first column, Shift-A by age, Shift-S by stage (or state where there is no
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

## Coming next

- filtering by column, and sorting by the column under a cursor
- actions on the unit under the cursor
- marks, to act on several units at once
- a read-only mode that disables every action
- hotkeys for the views used most
