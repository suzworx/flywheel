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
| `flywheel fleet status [--json]` | One row per ledger. |

`fleet status` prints an aligned table:

```
NAME          KIND               RUNNING  PASSED  FINISHED  ANDON  STATE        HEALTH  LAST
flywheel      root               2        1       0         1      running      4m      30s
flywheel/f1   flywheel-worktree  0        0       0         0      running      -       2h
olexa         root               0        0       1         1      paused: m1   -       12m
olexa-old     root               0        0       0         0      SUSPENDED    -       3d
```

- RUNNING counts the dispatched and running units; PASSED and FINISHED count units in those
  stages. The counts come from `flywheel status` for that ledger, never a second derivation.
- ANDON is the floor's andon count.
- STATE is `SUSPENDED` (see `flywheel suspend`), `paused: <models>` when a rate limit pauses
  models, or `running`.
- HEALTH is the age of the latest `health` event, LAST the age of the latest event; `-` when
  there is none.
- A ledger that fails to read shows `error: <why>` as its STATE; the other rows still print.

Exit codes: 0 ok, 1 error (unreadable registry, refused add, unknown name), 2 usage.

## Coming next

- A durable learnings queue fed from every ledger in the fleet.
- `flywheel fleet watch`: a watcher over the fleet.
- `:ctx` in the factory view to switch between fleet ledgers.
