# For agents

This page is written for AI coding agents: an agent asked to use flywheel in a repository, or told
it is the lead or a worker there. People can read it too. It is dense on purpose; follow the links
for depth.

## What flywheel is

- A Go CLI plus agent skills. The factory is files in the repository; agents and models are rented.
- A **lead** plans, writes briefs (work orders) and judges. It never implements.
- Disposable **workers** build one brief each, inside the brief's `owns:` boundary.
- **Gauges** re-run every `gate:` line on the exact tree the worker built. A claim is not evidence.
- An append-only event log, `.flywheel/events.jsonl`, is the single source of truth.
  `.flywheel/state.json` and `flywheel.md` are projections of it.
- Poka-yoke rules (T1-T10) refuse unmeasured, self-inspected or out-of-bounds work with exit 6.
- Any session, human or model, reads the same files and continues the same work.

## Pick your role

One skill per role. Load the one that matches what you were asked to do.

| role | you are this when | your skill | commands you run |
| --- | --- | --- | --- |
| lead | you drive the loop: plan, brief, dispatch, validate, judge, land; you never implement | [flywheel](https://github.com/suzworx/flywheel/blob/main/skills/flywheel/SKILL.md) | `flywheel recover`, `flywheel lint`, `flywheel log`, `flywheel run`, `flywheel validate`, `flywheel inspect`, `flywheel ship` |
| planner | a spec or goal must become bounded tasks with `owns:`, `needs:` and gates, before any dispatch; you never dispatch | [flywheel-planner](https://github.com/suzworx/flywheel/blob/main/skills/flywheel-planner/SKILL.md) | `flywheel context`, `flywheel lint`, `flywheel log` |
| worker | you received a brief: implement exactly it, run its gates, report evidence | [flywheel-worker](https://github.com/suzworx/flywheel/blob/main/skills/flywheel-worker/SKILL.md) | the brief's own `gate:` lines; no git writes |
| inspector | a unit has run and reported done: judge it against its brief from gauge readings for the same tree; verdict pass, rework, scrap or escalate | [flywheel-inspector](https://github.com/suzworx/flywheel/blob/main/skills/flywheel-inspector/SKILL.md) | `flywheel explain`, `flywheel inspect`, `flywheel verify` |
| reviewer | `flywheel review <task> --agent` hands you a finished unit's diff: report findings with a failure scenario, never a pass | [flywheel-reviewer](https://github.com/suzworx/flywheel/blob/main/skills/flywheel-reviewer/SKILL.md) | `flywheel review`, `flywheel explain` |
| operator | you install flywheel into a repository, check it is healthy, read its state, or drive the loop as an operator | [flywheel-operator](https://github.com/suzworx/flywheel/blob/main/skills/flywheel-operator/SKILL.md) | `flywheel init`, `flywheel config validate`, `flywheel doctor`, `flywheel status` |
| foreman | briefs are ready: dispatch them, watch run states, apply the retry policy, pull the andon cord; you never plan or inspect | [flywheel-foreman](https://github.com/suzworx/flywheel/blob/main/skills/flywheel-foreman/SKILL.md) | `flywheel run`, `flywheel wait`, `flywheel supervise`, `flywheel watch` |
| steward | signals are untriaged, nonconformances are filed, or a session is ending: turn each into a learning with a corrective action | [flywheel-steward](https://github.com/suzworx/flywheel/blob/main/skills/flywheel-steward/SKILL.md) | `flywheel feedback`, `flywheel gate`, `flywheel handoff` |
| auditor | a first article is due, the line is sampled, or a record looks incomplete: re-measure in a clean environment and check the record | [flywheel-auditor](https://github.com/suzworx/flywheel/blob/main/skills/flywheel-auditor/SKILL.md) | `flywheel audit`, `flywheel verify`, `flywheel explain` |

Roles are sessions, not people: the session that built a unit can never inspect it (rule T4).

## Adopt flywheel in a repository

1. Install the binary. Download `flywheel-<version>-<os>-<arch>.zip` from the
   [latest release](https://github.com/suzworx/flywheel/releases/latest) (Windows binaries ship as
   `flywheel-<version>-windows-amd64.exe.zip`) plus `checksums.txt`, verify the SHA-256, and put
   the binary on PATH renamed to `flywheel`. Or install with Go:

   ```sh
   go install github.com/suzworx/flywheel/cmd/flywheel@latest
   flywheel version
   ```

   Per-platform install steps are in the [quickstart](quickstart.html). `flywheel upgrade --check`
   reports a newer release; `flywheel upgrade` installs it, checksum-verified.
2. Scaffold the factory inside the git repository:

   ```sh
   flywheel init
   ```

   The `factory:` summary it prints names each missing enforcement hook and the `flywheel init`
   flag that adds it (`--hooks`, `--git-hooks`, `--ci`).
3. Configure the worker in `.flywheel/config.json`: its adapter (`opencode`, `claude`, `codex`,
   `pi`, or `sim`, which replays a recorded run and writes no files) and its model. `pi` is the
   [pi coding agent](https://pi.dev) (`npm install -g @earendil-works/pi-coding-agent`); its models
   are `provider/id`, e.g. `anthropic/claude-sonnet-5`:

   ```json
   {
     "version": 1,
     "workers": [
       { "name": "default", "adapter": "claude", "model": "<model id>", "max_parallel": 4 }
     ]
   }
   ```

   Then check it and probe the model before any dispatch:

   ```sh
   flywheel config validate
   flywheel doctor
   ```

   `flywheel config set <key> <value>` edits it; an unknown key lists every settable key.
4. Install the skills into your agent's skill directory, one per skill folder:

   ```sh
   npx skills add suzworx/flywheel --skill flywheel
   npx skills add suzworx/flywheel --skill flywheel-worker
   # ... flywheel-planner, flywheel-foreman, flywheel-inspector, flywheel-reviewer,
   #     flywheel-auditor, flywheel-steward, flywheel-operator
   ```

5. Commit the factory. The ledger is committed; transcripts are not. `.flywheel/` holds:
   - `events.jsonl` — the event log, append-only, committed with `merge=union`.
   - `state.json` — a projection of the log; never edit it by hand.
   - `config.json` — workers, models and limits; committed.
   - `briefs/` — the work orders; committed.
   - `runs/` — worker transcripts; ignored.
   - `worktrees/<task>` — a unit's own checkout on branch `fw/<task>` (with `flywheel run --worktree`); ignored.

   `flywheel init` writes `.flywheel/.gitignore` and `.flywheel/.gitattributes` for this, and
   `flywheel doctor` warns while the log is untracked or lacks `merge=union`.

## The loop, command by command

Replace `<id>` with the task id and `<own>` with your own session id. `flywheel help <command>`
prints any command's flags; commands accept the task id before or after flags.

1. Write a brief at `.flywheel/briefs/<id>.txt` (format below).
2. Lint it and probe its gates once on the base tree:

   ```sh
   flywheel lint .flywheel/briefs/<id>.txt --probe --task <id>
   ```

   Exits 1 on any problem. `--task` records each probe, so a gate that already fails on the base
   tree is told apart from broken work.
3. Record it as planned:

   ```sh
   flywheel log --task <id> --kind planned --brief .flywheel/briefs/<id>.txt
   ```

   Appends a `planned` event. The brief is hashed at dispatch; a later edit is detectable (T1).
4. Dispatch a worker:

   ```sh
   flywheel run <id> --worktree
   ```

   Records `dispatched`, `started` and `finished`. Refuses (exit 6) an owns collision with an
   in-flight unit. Exit 3, 4 or 7 is the run's outcome, not a verdict. `--worktree` runs the worker
   in `.flywheel/worktrees/<id>` on branch `fw/<id>`.
5. Measure it yourself:

   ```sh
   flywheel validate <id>
   ```

   Re-runs every `gate:` line on the exact tree and checks every changed path is inside `owns:`.
   Records `validated` and `owns_checked` readings bound to the tree hash. Exit 5 on any failure.
6. Review the diff from the dispatch base against the brief. Optionally hand it to an independent
   reviewer with `flywheel review <id> --agent --session <own>`.
7. Give the verdict:

   ```sh
   flywheel inspect <id> --verdict pass --session <own>
   ```

   Refuses (exit 6) a pass with no passing readings for the same tree (T3), or from a session that
   was ever the unit's worker (T4). Other verdicts: `rework`, `scrap`, `escalate`.
8. Land it:

   ```sh
   flywheel ship <id>
   ```

   Commits leftovers, merges the integration branch into `fw/<id>`, re-runs the gates, pushes,
   opens the PR, waits for CI, squash merges, records `landed`. Without a remote, commit yourself
   and record the landing with `flywheel land <id> --commit <sha>`. Both refuse (exit 6) a unit
   with no inspected pass (T5) or with untriaged signals (T9).
9. Check the whole log:

   ```sh
   flywheel verify --all --log
   ```

   Checks every task against the poka-yoke rules and the log's hash chain. Exit 6 on a violation,
   8 when a check cannot be established.

Corrections: write a delta brief that repeats the full header (`owns:`, `needs:`, every `gate:`)
and states only what to change, then resume the worker's session with it:

```sh
flywheel run <id> --resume --delta .flywheel/briefs/<id>.delta.txt
```

The delta is dispatched as a correction attempt `c<N>`; validate and inspect again after it.

## Brief format

A brief is one text file: a header, a `# TASK:` goal, a body, and a `## Checks` report contract.

- `owns:` — the paths the unit may write. `(new)` marks a file it creates; a trailing `/` claims a
  directory; `*`, `?`, `[` make a glob; a leading `!` excepts a path.
- `needs:` — task ids that must land first; `none` for no dependencies.
- `kind:` — optional: `feature`, `fix`, `refactor`, `test`, `docs`, `chore` or `perf`.
- `gate:` — one shell command per line that must exit 0 on the built tree. List every gate.
- `line:` — optional: the product line from `.flywheel/config.json` the unit belongs to.

```text
owns: internal/greet/greet.go, internal/greet/greet_test.go (new)
needs: none
kind: feature
gate: go build ./...
gate: go vet ./...
gate: go test ./...
gate: git diff --check "$FLYWHEEL_BASE"

# TASK: greet — Hello(name) returns "Hello, <name>"

Add func Hello(name string) string to internal/greet and a table test for it.
At most one write per response; batch read-only calls.

## Checks

Report every command you ran and its real exit code.
```

Gates that actually measure:

- List every gate the gauges must run, including the repository's own build, lint and full test
  suite; `flywheel validate` runs only the `gate:` lines.
- A whitespace or diff gate is `git diff --check "$FLYWHEEL_BASE"`, never a bare `git diff --check`:
  each attempt is committed before validate, so a diff against HEAD sees nothing.
- `FLYWHEEL_BASE` is the unit's base commit; validate sets it for every gate and run sets it for
  the worker, so both measure the same diff.
- Use the repository's own test runner (`npm test -- <file>`, or the runner its test script calls);
  a worker gated on the wrong runner writes a shim to pass it, and `flywheel lint` warns.
- One command per gate, no disjunctions: a gate with an `||` fallback passes when the check fails.
- Probe before planning (`flywheel lint <brief> --probe --task <id>`) so a wrong path or missing
  tool is caught before a paid attempt.

## Exit codes

One convention across the CLI:

| code | meaning |
| --- | --- |
| 0 | ok |
| 1 | error: the command did not complete; read the message |
| 2 | usage: a flag or argument was wrong; run `flywheel help <command>` |
| 3 | silent: `flywheel run` saw no output within the start timeout |
| 4 | failed: `flywheel run` measured any other non-clean outcome |
| 5 | gauges failed: a gate failed or a change sits outside `owns:` |
| 6 | rule refusal: a poka-yoke rule refused the action; the message names the rule and the fix |
| 7 | stalled: `flywheel run` saw the run file stop growing for the stall timeout |
| 8 | inconclusive: a check could not be established, and no violation is established either |

Codes 3, 4 and 7 are `flywheel run`'s outcome codes for the dispatch it measured. Read an exit
code directly (`cmd; echo "exit=$?"`), never behind a pipe.

## Reading state

All state is derived from the event log. Read it with the CLI:

| command | what it prints | `--json` |
| --- | --- | --- |
| `flywheel context` | a compact pack of the factory's state for a joining agent; `--role` narrows it | yes |
| `flywheel recover` | where every unit is, whether the world matches the ledger, the next safe action | yes |
| `flywheel explain <id>` | one task's whole story from the ledger | yes |
| `flywheel status` | the factory's deterministic summary | yes |
| `flywheel next` | the reconciler's next actions | yes |
| `flywheel state` | the derived state | yes |

Start every session with `flywheel recover` (or `flywheel context`), then act on what it reports.
`flywheel factory` is a live dashboard for people; agents use the commands above with `--json`.

## Rules you must follow

- Stay inside your worktree and your brief's `owns:`; list every file you may create up front.
- Never write the shared git index; use a temporary `GIT_INDEX_FILE` when you need one.
- Workers never commit, add, stash, reset, checkout or push; the lead commits after inspection.
- Report every command you ran and its real exit code, never behind a pipe such as `| tail`.
- A claim is not evidence: the gauges re-measure it on the exact tree.
- Write state files atomically: a temp file, then rename.
- Never edit `.flywheel/events.jsonl` or `.flywheel/state.json` by hand; append through the CLI.
- Read only complete lines from files other processes append to.
- Never inspect work your own session built (T4).
- At most one write per response; batch read-only calls.

## Where to read more

- [Quickstart](quickstart.html) — one unit through the loop, with each refusal and its fix.
- [Concepts](concepts.html) — the vocabulary: work order, `owns:`, gates, the event log, the rules.
- [Protocol](PROTOCOL.html) — exactly what the code enforces: every event kind and rule.
- [Factory view](factory-view.html) — the live floor for people.
- [Fleet](fleet.html) — one view over many factory roots and ledgers.
- [Metrics](metrics.html) — the factory's own numbers.
- [Autonomous shipping](design/autonomous-shipping.html) — the factory model and rules T1-T10.
- [Worker briefs](https://github.com/suzworx/flywheel/blob/main/skills/flywheel/references/worker-brief.md) — briefs, dispatch and run states in depth.
- [AGENTS.md](https://github.com/suzworx/flywheel/blob/main/AGENTS.md) — the house rules flywheel builds itself by.
- [Skills](https://github.com/suzworx/flywheel/tree/main/skills) — one folder per role.
- [llms.txt](llms.txt) — the plain-text index of these pages.
