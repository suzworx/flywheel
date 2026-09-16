---
name: flywheel-operator
description: >-
  Operate the flywheel framework from any role — human or agent. Use when you want to install
  flywheel into a repo, validate it is healthy, understand its state, or drive the loop
  (plan/brief/dispatch/review/correct-or-land) as an operator rather than as a worker. The CLI
  implements init, config, log, state, run, validate, inspect, verify, factory, and staff:
  scaffold a repo, read and set the config, record events, derive state, dispatch workers, run
  the gauges, and register roles on the floor. Plan, retry, handoff, status, trace, and
  artifacts are planned, not built — until they land, drive those steps manually with brief
  files and the raw worker commands shown here; handoff is done by writing state files and
  passing emitted session IDs by hand.
license: MIT
metadata:
  version: 0.3.0 # x-release-please-version
---

# Flywheel Operator

Flywheel is a durable orchestrator-to-worker loop. **Any agent — or a human — can drive it.** The
CLI is the deterministic substrate; the skills are the judgment layer. You are the operator: you
decide what runs, who runs it, and whether it landed. The command table below implements
[protocol v1](../../docs/PROTOCOL.md).

## What the CLI implements today

Ten subcommands exist — init, version, config, log, state, run, validate, inspect, verify,
factory. The rest of this skill is the manual workflow that runs on the same state files until the
planned subcommands land — don't invoke commands that aren't built.

| Command | Status | What it does |
| --- | --- | --- |
| `flywheel init` | **implemented** | Scaffold `flywheel.md` + `.flywheel/state.json` + `.flywheel/briefs/`; refuses if either state file already exists unless `--force`. |
| `flywheel version` | **implemented** | Print the flywheel version. |
| `flywheel config` | **implemented** | Read and validate `.flywheel/config.json`. |
| `flywheel log --task <id> --kind planned --brief <path>` | **implemented** | Record a planned brief to the event log before dispatch. |
| `flywheel state` | **implemented** | Derive and print state from the event log. |
| `flywheel run <task>` | **implemented** | Canonical dispatch: attach the brief with `--file`, apply the deny policy, record every event. |
| `flywheel validate <task> [--workdir]` | **implemented** | Run the brief header's `gate:` lines on the exact tree and check `owns`; exit 0, or 5 on a failing gate or a file outside owns. |
| `flywheel lint <brief> [--dir DIR]` | **implemented** | Check a brief for problems: missing `owns:`, `gate:`, `# TASK` or `## Checks`, owns paths that don't exist; warnings for a missing write rule or `needs:` line (exit 0/1). |
| `flywheel inspect <task> --verdict pass\|rework\|scrap\|escalate --session <own session>` | **implemented** | Record an inspection; refused with exit 6 for a bad verdict, a worker's session, or no passing readings for the tree as it is now. |
| `flywheel verify [<task>...\|--all] [--json]` | **implemented** | Check the event log against rules T1, T3, T4, T5, T8; exit 0 or 6. |
| `flywheel staff --role lead --session <session> [--model M]` | **implemented** | Register a factory role on the floor; the lead line then reads `lead <session> (<model>)`. |
| `flywheel land <task> --commit <sha> [--note TEXT]` | **implemented** | Record a landing; refused with exit 6 unless the task passed inspection, and a different commit than a previous landing is refused. |
| `flywheel factory [--once\|--json]` | **implemented** | Render the floor — workers, units with run states, andon, output; bare `flywheel` opens it, one shot when stdout is not a terminal. |
| `flywheel status [--dir DIR] [--now RFC3339] [--json]` | **implemented** | Summarize the factory deterministically: task counts per status, live and stale attempts, last event and last meaningful progress, andon count. |
| `flywheel cost [--dir DIR] [--json]` | **implemented** | Sum finished events' tokens and cost per task and per model; a finished task without a dispatch is listed under `unknown`. |
| `flywheel stats [--dir DIR] [--json]` | **implemented** | The factory's own numbers from the event log: first-pass rate, corrections per task, finish reasons and unclean rate, mean attempt seconds, cost per landed task. |
| `flywheel next [--dir DIR] [--now RFC3339] [--json]` | **implemented** | Print the reconciler's next actions read-only: lost attempts, inspection requests, blocks, waits and dispatches; nothing executes them yet. |
| `flywheel goal add "<title>" --id <id> [--accept CMD]... [--require TASK]...` | **implemented** | Record a factory goal; later add, list, show and set its status with `flywheel goal <add\|list\|show\|set>`. |
| `flywheel controller [--once] [--interval D] [--dir DIR] [--now RFC3339]` | **implemented** | The controller loop: acquire `.flywheel/controller.lock`, tick (mark lost attempts, block tasks whose needs were scrapped), renew the lock each tick; `--once` runs one tick and releases the lock, a live lock held elsewhere exits 6. |
| `flywheel plan`, `retry`, `handoff` | **planned** | Control plane: create tasks, resume, transfer between agents. |
| `flywheel handoff [--dir DIR] [--stdout]` | **implemented** | Print the handoff summary for a new head — in-flight tasks (with session and model), blockers, next ready tasks and the default worker model; with `--stdout` to stdout, otherwise into `flywheel.md` between the handoff markers. |
| `flywheel plan`, `retry` | **planned** | Control plane: create tasks, resume, transfer between agents. |
| `flywheel trace <session> [--dir DIR]` | **implemented** | Everything one session did, across tasks: one line per event whose session matches, in log order; read-only, never derives state. |
| `flywheel artifacts` | **planned** | Data plane: worker outputs. |

## Install

```bash
# build and validate the CLI
go build ./... && go vet ./... && go test ./...

# install the binary
go install ./cmd/flywheel

# scaffold state into a repo
flywheel init --dir <target>     # creates flywheel.md + .flywheel/state.json + .flywheel/briefs/
```

Requires: Go toolchain (to build), the `opencode` CLI (to dispatch workers), git.

**Windows.** An existing factory whose `.flywheel/.gitattributes` predates init (init writes
`* text eol=lf`) should add that line and re-checkout the briefs, so dispatch hashes and brief
hashes agree despite CRLF.

**Upgrading.** Swap the binary — renaming the old executable is safe while a unit runs. Keep
symlinked skill installs rather than copies (the skills installer can replace symlinks). Commit
`skills-lock.json`, or any tracked file the skills installer touched, before the next
`flywheel validate`, because files the lead changes after a unit's dispatch count against that
unit's owns check. Run `flywheel status` afterwards.

## State model

Everything is files — no database.

| File | Purpose |
| --- | --- |
| `flywheel.md` | Human-readable state: Status, Main session, Workers, Roles, Task log. |
| `.flywheel/state.json` | Machine-precise state: `version`, `status`, `tasks[]`. |
| `.flywheel/briefs/` | One file per task brief (`<id>.txt`) and per correction (`<id>.delta.txt`). |
| `.flywheel/runs/` | Raw dispatch output (JSONL) per attempt — `<id>.r1.jsonl` fresh run, `<id>.c<n>.jsonl` corrections. |
| `.flywheel/learnings.md` | Dogfood log — friction becomes spec (create it by hand). |

**Repo is the session.** State lives in files, not in any vendor CLI session. That is what makes
handoff free: a new head reads the same files and continues. Helper scripts and notes must live in
the repo, never in a per-session scratch directory — a session restart loses them. Because
`handoff` is not built yet, transfer is manual — write the current state into `flywheel.md` and
the brief files, and pass the emitted session ID by hand to the next head.

**In a consumer repo.** Commit the state: `flywheel.md`, `.flywheel/state.json`,
`.flywheel/briefs/`, `.flywheel/plans/`, `.flywheel/learnings.md`, `.flywheel/scripts/` (and
`.flywheel/events.jsonl` once the event log lands, issue #11); ignore `.flywheel/runs/` and any
local cache. This framework repo ignores its own `.flywheel/` only because its dogfood state is
scratch — consumer repos commit theirs.

## Config

`.flywheel/config.json` is the project configuration, created by `flywheel init`:

| Field | Meaning |
| --- | --- |
| `version` | Config schema version (1). |
| `workers[]` | One entry per worker: `name`, `adapter` (`opencode` or `sim`), `model`, `variant`, `max_parallel` (0 means 1), `fallbacks[{model, approved}]` (fallback models, each with a standing `approved` OK to switch without asking). |
| `limits` | Shared caps: `per_host` (parallel workers per host) and `budget{wave_cost_usd}` (spending cap per wave). |
| `feedback` | `upstream` (owner/repo) and `submit` (`ask` or `never`). |

Read and set it with the CLI:

```bash
flywheel config get model                    # bare keys use the default worker
flywheel config set variant low              # or model, adapter, max_parallel
flywheel config set workers.<name>.<key> <v> # any worker by name
flywheel config set feedback.upstream <owner/repo>
flywheel config set feedback.submit ask|never
flywheel config set limits.per_host <n>
flywheel config show                        # effective config as JSON
flywheel config validate                    # check the config, list every problem
```

Settable keys: `model`, `variant`, `adapter`, `max_parallel` (bare = the default worker, or
`workers.<name>.<key>`), `feedback.upstream`, `feedback.submit`, `limits.per_host`.
`fallbacks` is not settable — edit `.flywheel/config.json` for it. `flywheel init --model <m>
--variant <v>` seeds a fresh config at setup.

## Operating the loop

The CLI has no plan/retry/handoff commands yet, so those steps are done with files and raw
commands. `flywheel init` only scaffolds; the loop below is the manual fallback and runs on the
same state files the planned subcommands will automate. `$MODEL` comes from
`.flywheel/config.json` — `flywheel config get model` (see Config below).

1. **Plan** — decompose into bounded single-purpose tasks; each gets a brief file:
   `cat > .flywheel/briefs/<id>.txt` with goal, exact change, don't-touch list, required gates,
   report contract.
2. **Brief** — the brief file from step 1 is the brief: goal, exact change, don't-touch list,
   required gates, report contract.
3. **Dispatch** — first choice is `flywheel log --task <id> --kind planned --brief <path>` then
   `flywheel run <task>`. Manual fallback (e.g. one increment of a brief) — fresh run with
   `--variant low`, capture rc and sessionID:
   ```bash
   mkdir -p .flywheel/runs
   OPENCODE_CONFIG=skills/flywheel/references/worker-permissions.json \
     opencode run --pure -m "$MODEL" --auto --format json --title "<id>-r1" --variant low \
     "Follow the attached brief exactly." --file .flywheel/briefs/<id>.txt < /dev/null > .flywheel/runs/<id>.r1.jsonl; rc=$?
   ```
   Every dispatch sets `OPENCODE_CONFIG` to the worker permission policy, which denies
   tree-rewriting git commands (ordering and `--auto` behaviour:
   [../flywheel/references/worker-brief.md#2-dispatch-verify-then-use-the-safe-quoted-file-brief](../flywheel/references/worker-brief.md#2-dispatch-verify-then-use-the-safe-quoted-file-brief)).
   Session id (every JSONL event carries it):
   ```bash
   grep -o '"sessionID":"[^"]*"' .flywheel/runs/<id>.r1.jsonl | head -1
   ```
   Record the exit code and the emitted session ID in `flywheel.md` — `flywheel run` will do this
   when it lands.
4. **Review** — judge the actual exit status and `git diff`, never self-report. Run `flywheel
   validate <task>` then `flywheel inspect <task> --verdict ... --session <your own session>` from
   your own session — a worker's report is never evidence. Re-run gates independently on sensitive
   changes.
5. **Correct or land (manual fallback)** — resume the emitted session ID with a delta brief for
   corrections. Pass the session ID by hand; there is no automatic handoff:
   ```bash
   OPENCODE_CONFIG=skills/flywheel/references/worker-permissions.json \
     opencode run --pure -m "$MODEL" --auto --format json --title "<id>-c<n>" --variant low --session "<emitted-sessionID>" \
     "Apply the attached correction to the same task." --file .flywheel/briefs/<id>.delta.txt < /dev/null > .flywheel/runs/<id>.c<n>.jsonl; rc=$?
   ```

### Validating while other units run

When parallel units run on one checkout, repo-wide gates fail with each other's half-written code, triggering the T3 refusal (exit 6, "no passing supervisor validated reading on tree"). To avoid this, use separate worktrees: for each unit, reset a verify worktree to main's HEAD, clean it, and copy only that unit's owned files. Then:

1. Run `flywheel validate <task> --workdir <tree>` to measure the gates on the stable worktree.
2. Run `flywheel inspect <task> --verdict pass --workdir <tree>` using the same worktree (T3 will find the passing supervisor reading on that tree hash).
3. Commit only the unit's owned files.

A unit's gates may depend on machine state outside the repo — a database, a local stack, or git-ignored env files. A fresh worktree holds only the unit's files, so that state must be carried in before the gates run, or the gate result is meaningless: a red gate that looks like a defect in the unit.

## Control plane vs data plane

| | Commands (all planned) | Purpose | Until they land |
| --- | --- | --- | --- |
| **Control plane** | `flywheel plan`, `run`, `retry`, `handoff` | Move work forward: create tasks, dispatch, resume, transfer between agents. | Write brief files and run the raw `opencode` commands by hand (above). |
| **Data plane** | `flywheel status`, `trace`, `artifacts` | Understand state: what's in flight, where each task sits, what each worker produced. | Read `flywheel.md`, `.flywheel/state.json`, and `.flywheel/briefs/` directly. |

Both are reachable by anyone (agent or human) — the judgment layer differs, the substrate
doesn't. The CLI commands are planned; do not invoke them yet.

## Role economy

- **Planner/validator** (frontier model: Claude Code / Codex) — decomposes, briefs, judges.
- **Worker** (cheap disposable: OpenCode + DeepSeek) — explores, implements, tests, reports.
- **Operator** (you, or any agent) — decides who plays which role for a given run.

Role ≠ adapter: the role comes first; the cheapest head that can fill it is selected. Run out of
tokens on the planner mid-session? The repo is the session — a new head reads the same files and
continues. The loop never waits for a vendor.

## Assigning personas

Each persona is a skill; any agent (or a human) can load one. To give an agent a role:

1. **Load that skill** into the agent — copy or install the persona's `SKILL.md` (and the factory
   model it links to, `skills/flywheel/references/factory.md`).
2. **Tell it its persona and session** — which role it plays, which repo or worktree is its
   session, and who else is on the line (the lead, the foreman, the auditor) so it can find its
   work and know what it must not do.

The independence rules hold for every assignment: the **auditor** is never the same session as the
lead, planner or inspector, and should be a different model or vendor; the **inspector** never
inspects work from its own session; a **worker** never records gauge readings, inspections or
audits. A session that is two personas at once may do either role's work, but never both on the
same unit.

Minimal staffing:
- **One frontier lead** holding the planner, foreman, inspector and steward roles (the lead plans,
  runs the line, inspects and triages learnings — the default at small scale).
- **OpenCode workers** (the approved model) executing the work orders.
- **A different model as auditor** — a separate session, ideally a different vendor, that audits
  first articles and samples.

## Health

```bash
go test ./...            # one-command validation
git status               # what's dirty
flywheel factory --once  # status at a glance (use --json for machine use)
cat .flywheel/state.json # machine state
cat .flywheel/learnings.md # what the loop has taught itself (create by hand until planned)
grep '<sessionID>' ~/.local/share/opencode/log/opencode.log | tail -20   # provider errors (key limits) show up only here
```

If a dispatch stalls or fails, classify the run first
([../flywheel/references/worker-brief.md#3-run-states-and-failures](../flywheel/references/worker-brief.md#3-run-states-and-failures)),
then report the blocker and halt — never take over the worker's job. Never kill opencode processes
by name; on Windows that can kill OpenCode Desktop.

## Files to read

- `skills/flywheel/SKILL.md` — the orchestrator skill (full loop rules)
- `skills/flywheel/references/worker-brief.md` — brief template + dispatch safety
- `skills/flywheel-worker/SKILL.md` — the worker's contract
- `examples/` — worked briefs