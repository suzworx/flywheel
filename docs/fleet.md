# Fleet: one view across factory roots

Every flywheel ledger stands alone: `.flywheel/events.jsonl` (or its shard directory) in one
checkout. A lead often runs several at once: the main checkout, sibling checkouts, unit
worktrees under `.flywheel/worktrees` and `.claude/worktrees`, and other repositories. A
**fleet** is the list of those roots, registered once per user, plus one merged status table
across every ledger found under them (issue #585).

## The registry

The fleet lives in one JSON file:

```json
{
  "version": 1,
  "roots": [
    {"name": "flywheel", "path": "D:\\work\\flywheel"},
    {"name": "olexa", "path": "D:\\work\\olexa"}
  ]
}
```

Its location is `fleet.json` in the `flywheel` folder of the user config directory (Go's
`os.UserConfigDir`: `%AppData%` on Windows, `~/Library/Application Support` on macOS,
`$XDG_CONFIG_HOME` or `~/.config` on Linux). Set `FLYWHEEL_FLEET` to a file path to use another
file. A missing file is an empty fleet. Every write is atomic (temp file plus rename).

## Discovery

For each root, in registration order, discovery looks at:

| Kind | Where |
| --- | --- |
| `root` | the root itself |
| `git-worktree` | every entry of `git worktree list --porcelain` (read-only; skipped when git fails) |
| `flywheel-worktree` | each directory under `<root>/.flywheel/worktrees/` |
| `claude-worktree` | each directory under `<root>/.claude/worktrees/` |

Only a directory holding `.flywheel/events.jsonl` or a `.flywheel/events/` shard directory is a
ledger; a worktree without one is skipped. A path reached twice (a unit worktree is both a git
worktree and a `.flywheel/worktrees` entry) is listed once, under the kind that reached it
first; paths compare absolute, symlinks resolved, and case-folded on Windows. Each root's own
ledger comes first, then the rest ordered by path. A root that no longer exists is reported as
`root missing`; it never fails the others.

Discovery is `flywheel.FleetLedgers(fleet)`, a plain function other commands reuse.

## Commands

| Command | What it does |
| --- | --- |
| `flywheel fleet add <path> [--name N]` | Register a root. The path is made absolute; the name defaults to its base, made unique with `-2`, `-3`. The same path twice is refused, naming the existing entry; a path with no `.flywheel/` that is not a git repository is refused. |
| `flywheel fleet remove <name>` | Unregister the root named `name`. |
| `flywheel fleet list [--json]` | Each root and the ledgers discovered under it. |
| `flywheel fleet status [--json] [--all] [--idle-after D]` | One row per ledger; idle worktree ledgers fold into one row per root. |
| `flywheel fleet learnings [--pending\|--all] [--json] [--sync=false] [--since D] [--done <key\|title prefix>\|--done-all\|--import-seen FILE]` | Sync every ledger's learnings into the pending queue, then list it (see [Learnings queue](#learnings-queue)). |
| `flywheel fleet watch [--once] [--interval D] [--notify CMD] [--report-all] [--registry FILE]` | Print what is new since the last look: learnings, andons, state changes, stale health; the first run records a baseline (see [Watch](#watch)). |

`fleet status` prints an aligned table:

```
NAME                                     KIND               EVENTS  RUNNING  PASSED  FINISHED  ANDON  STATE        HEALTH  LAST
flywheel                                 root               412     2        1       0         1      running      4m      30s
flywheel/f1                              flywheel-worktree  +9      0        0       0         0      running      -       2h
+37 idle worktree ledgers (oldest 12d)   idle
olexa                                    root               230     0        0       1         1      paused: m1   -       12m
olexa-old                                root               57      0        0       0         0      SUSPENDED    -       3d
```

Each row is a summary of one read of that ledger's events (issue #605): no floor is built, no git
runs, no run file is read, so a fleet of hundreds of ledgers answers in seconds. The root ledgers
are read first, then the rest in parallel, `GOMAXPROCS` at a time; rows print in discovery order.

The ledger is committed, so a git worktree carries its root's events as of its branch point, and
any it later merges in (issue #608). An event of a worktree ledger that is also in its root's
ledger (the same event, byte for byte) is inherited: a worktree row shows only its own events
after the fork, never a copy of the root's history. A worktree whose root fails to read is
summarised in full.

- EVENTS is the number of events summarised: the plain count for a root (or a worktree summarised
  in full), `+N` for a worktree row, N being its own events after the fork. In `--json`, `events`
  is that count and `inherited` the events shared with the root.
- For a worktree row, RUNNING, PASSED, FINISHED and the per-unit andons count only units with an
  event of its own; STATE, HEALTH, LAST and the other andons read its own events only, so an
  inherited suspension or pause never shows.
- RUNNING counts the dispatched and running units; PASSED and FINISHED count units in those
  stages, derived from the events as `flywheel status` derives them.
- ANDON counts the andon kinds the events alone decide: the suspension, each model a rate limit
  pauses, each group with open blocking integration findings, a stale health event (older than
  the `stale_after` it records), and each unit with an attempt that is blocked or finished for a
  reason other than a clean stop (capped, failed, rate-limited, ...). The floor's run-file states
  (silent, stalled, no-writes), open review findings and staffing mismatches are left out, so
  ANDON can be lower than `flywheel factory` shows; open that ledger's floor for the full list.
- STATE is `SUSPENDED` (see `flywheel suspend`), `paused: <models>` when a rate limit pauses
  models, or `running`.
- HEALTH is the age of the latest `health` event, LAST the age of the latest event; `-` when
  there is none.
- A ledger that fails to read shows `error: <why>` as its STATE; the other rows still print.

In the factory view, `:ctx` lists the same ledgers and Enter switches the view to one;
`flywheel factory --ctx <name>` starts there ([factory view](factory-view.md#contexts)).

### Idle worktree ledgers

A ledger that is not a root (`git-worktree`, `flywheel-worktree`, `claude-worktree`) whose latest
event is older than `--idle-after` (default `72h`, any Go duration) is not listed. Instead each
root that has any gets one row, kind `idle`, after its other rows: `+N idle worktree ledgers
(oldest <age>)`, the age of the oldest one's latest event. A worktree that is an exact copy of its
root (no event after the fork, `+0`) folds the same way however recent its events are (issue
#608); it has no own event, so it adds to the count but not to the age. Root ledgers are always
listed, however old; so are ledgers that fail to read and ledgers with no event yet. `--all`
lists every ledger, an exact copy included as `+0`.
`--json` applies the same rule: a fold row carries `"kind": "idle"`, `idle` (the count) and
`last_age` (the oldest age, in seconds).

## Learnings queue

`flywheel fleet learnings` gathers every `learning` event across the fleet's ledgers, so a lead
that runs several factories no longer needs a hand-written watcher script with its own seen-set
and pending list.

- **The key.** A learning's key is the hex sha256 of its title and observed text. The same
  learning in several ledgers, a root and the worktrees that carry its committed ledger, is one
  learning; the earliest `ts` wins as its first-seen ledger and time. A worktree's learning that
  is also in its root's ledger is inherited (the rule `fleet status` uses) and never first-seen
  in the worktree; a learning the worktree records after the fork is its own.
- **Dismissed.** A learning is dismissed when a `dismissed` event in any ledger targets it (by
  that ledger's `L-NN` id). A dismissed learning is marked seen and never becomes pending.
- **The queue.** `fleet-learnings.json` beside the registry (the directory of `fleet.json`, or of
  `FLYWHEEL_FLEET`) holds `{"version": 1, "seen": [keys], "pending": {key: learning}}` and is
  written atomically (temp file plus rename), so a crash mid-write leaves the previous queue. A
  queue that does not parse is an error, never silently reset.
- **Sync.** Every run (unless `--sync=false`) adds each key not yet seen to `seen` and, unless
  dismissed, to `pending`, and notes on stderr how many it added. Repeated syncs add nothing new.
- **The first sync.** On an empty queue the first sync marks every learning seen but makes pending
  only those newer than `--since` (default `168h`, 7 days; any Go duration such as `24h`), so
  history does not flood the queue; it says so on stderr. `--since` matters only on that first
  sync.
- **Importing what was already triaged.** `--import-seen FILE` reads a JSON array of titles, or of
  `{"title": ...}` objects (the shape a hand-written watcher keeps; other fields are ignored). It
  syncs first (so an empty queue still gets the `--since` window), then marks every current
  learning whose title is in the file seen and removes it from pending, and prints how many
  learnings matched. A file of another shape is an error.
- **Draining.** `--done <ref>` removes from pending every learning whose key starts with `ref`
  (at least 6 characters; the table shows 12) or, when none does, whose title starts with `ref`
  (case folded). `--done-all` empties pending. Neither syncs, and neither removes from `seen`,
  so a handled learning never comes back. An unmatched `--done` exits 1.

The default lists the pending queue, oldest first:

```
KEY           SEVERITY  AGE  ROOT      TASK  TITLE
3f9a0c41be27  P1        2h   olexa     t12   gate runs twice on a resumed session
```

`--all` lists every learning in the fleet instead (a dismissed one marked `(dismissed)`); `--json`
prints the list as JSON (`[]` when empty) for scripts.

## Watch

`flywheel fleet watch [--once] [--interval D] [--notify CMD] [--report-all] [--registry FILE]` tells a person
what is new across the fleet since the last look. It compares the fleet now with the state it
saved last time, `fleet-watch.json` beside the registry (written atomically; a file that does not
parse is an error, never a reset that would report everything again), and prints one line per
item:

```
2026-09-27T10:05:00Z olexa learning: [P1] gate runs twice on a resumed session (3f9a0c41be27)
2026-09-27T10:05:00Z olexa state: running -> SUSPENDED
2026-09-27T10:05:00Z flywheel/f1 andon: f1 finished: length
2026-09-27T10:05:00Z flywheel stale: health older than 10m0s
```

| Kind | Reported when |
| --- | --- |
| `learning` | the learnings queue sync made a learning pending (the same sync as `fleet learnings`, so the first watch on an empty queue follows the first-sync rule) |
| `state` | a ledger's STATE changed: `running`, `SUSPENDED` or `paused: <models>`, both ways, so a thaw or an unpause is reported too; a ledger seen for the first time counts as `running` before |
| `andon` | a ledger has an andon it did not have last time: a group with open blocking findings, or a unit with an attempt that is blocked or finished for a reason other than a clean stop. The suspension and the pauses are `state` items and stale health is a `stale` item, so none is reported twice |
| `stale` | a ledger's latest health event became older than 10 minutes |
| `error` | the learnings sync failed (for example, a queue that does not parse) |

Learnings come first (oldest first), then each ledger in discovery order: its state change, its
new andons (sorted), its stale health. A worktree ledger counts only its own events after the
fork, as in `fleet status`. A ledger that fails to read keeps its previous state and reports
nothing. The next look with the saved state reports nothing until something changes again.

**The first run records a baseline.** With no `fleet-watch.json` yet (the first run, or the file
removed), the watch records the current fleet as the baseline and prints one line instead of every
historical andon:

```
baseline recorded: 4 ledgers, 37 andons, 12 pending learnings; changes from now on are reported
```

It runs no `--notify` and exits 0; only later runs report differences. The learnings sync on that
run follows the queue's own first-sync rule, and its pending learnings stay in
`flywheel fleet learnings`.

- `--report-all` prints the whole current set once: every pending learning (oldest first), then per
  ledger its state when not `running`, every open andon and stale health. It records the state
  like any run and runs no `--notify`.
- `--once` looks once and exits; without it the watch loops every `--interval` (default `5m`).
- `--notify CMD` runs `CMD` once per item through the same shell chooser as gates (bash where
  there is one), with `FLYWHEEL_FLEET_ITEM` set to the item's line. The state is saved before
  notifying; a notify failure only warns on stderr.
- `--registry FILE` watches another registry file than the default (the scheduled task passes
  it, so a task never depends on `FLYWHEEL_FLEET` being set).

### Running the watch from the OS scheduler

`flywheel schedule install --fleet [--every D] [--notify CMD]` registers a separate OS task,
`flywheel-fleet-<user>`, next to any repository's controller task, with the same schedulers
(Task Scheduler on Windows, the user crontab on Linux, launchd on macOS). It runs
`flywheel fleet watch --once --registry <registry>` (plus `--notify CMD` when given) every
`--every`, default `5m`, and appends its output to `fleet-watch.log` beside the registry.
`flywheel schedule status --fleet` and `flywheel schedule remove --fleet` act on that same task.

Exit codes: 0 ok, 1 error (unreadable registry, queue or watch state, refused add, unknown name,
unmatched `--done`, unreadable `--import-seen` file), 2 usage.
