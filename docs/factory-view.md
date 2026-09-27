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

  The right column is the key menu of the screen shown, in columns of up to four keys:
  `<:> view  </> filter  <enter> explain  <l> log  <?> help  <q> quit  <ctrl-e> header ...`.
  Each view lists only the keys valid there, so the workers view has no `<enter> explain`.
  A frame too short to keep five table rows below the header drops the header on its own.
- **The title bar**: `── Units(all)[3] ──`, the view, its filter and its row count, then the sort
  when one is set (`── Units(/build)[2] ↑stage ──`); in a drill-down `── Explain T3 ──`,
  `── Log T3 ──`, `── Learning L-02 ──`, `── Checkpoint T3/2 ──` or `── Views ──`; `── Help ──`
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
| Enter | units, andon, tree | explain the unit under the cursor |
| Enter | learnings, checkpoints, search | the learning in full, the checkpoint's changed paths, the result's unit at the match |
| `l` | units, andon, tree | the unit's event log |
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
| `units` | `u` | every unit: stage, attempt, session, model, steps, age, state |
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

## Search

`:s <text>` (or `:search <text>`) searches, case-insensitively, the ledger's events, the run logs
(`.flywheel/runs/*.jsonl`), the reports (`.flywheel/runs/*.report.md`) and the briefs
(`.flywheel/briefs/*.txt`), and shows one row per matching line: SOURCE (`event`, `log`,
`report`, `brief`), TASK, ATTEMPT, LINE (an event's line in the ledger) and an excerpt, the match
with about 40 characters on each side. It stops at 500 results and the title says
`500+ (narrow the search)`. Enter on an event opens the unit's log at the match; on any other
result, the unit's explanation. The search runs when you enter it and on Ctrl-R, never on every
refresh.

## Coming next

- filtering by column, and sorting by the column under a cursor
- actions on the unit under the cursor
- marks, to act on several units at once
- a read-only mode that disables every action
- hotkeys for the views used most
