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
- **The title bar**: `── Units(all)[3] ──`, the view, its filter and its row count; in a
  drill-down `── Explain T3 ──` or `── Log T3 ──`; `── Help ──` in help.
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
| `:` | table | the view prompt: `units` (`u`), `workers` (`w`), `andon` (`a`), `events` (`e`), `lines` (`l`), `quit` (`q`) |
| `/` | table | the filter prompt; the table filters as you type, Enter keeps it |
| Enter | units, andon | explain the unit under the cursor |
| `l` | units, andon | the unit's event log |
| Esc | everywhere | leave the drill-down, help or prompt; in a table, clear the filter |
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

## Coming next

- history: go back and forward through the views visited
- sorting by any column
- filter modes: inverse and fuzzy filters, filtering by column
- tree and health views
- actions on the unit under the cursor
- marks, to act on several units at once
- a read-only mode that disables every action
- hotkeys for the views used most
