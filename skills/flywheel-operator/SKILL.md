---
name: flywheel-operator
description: >-
  Operate the flywheel framework from any role — human or agent. Use when you want to install
  flywheel into a repo, validate it is healthy, understand its state, or drive the loop
  (plan/brief/dispatch/review/correct-or-land) as an operator rather than as a worker. The CLI
  implements the whole loop except creating and resuming tasks: init, config, log, state, run
  (through the opencode, claude or sim adapter), validate, inspect, verify, factory, staff, cost,
  stats, next, status, handoff, trace, claim/release/claims, controller, goal, land and lint. Only
  `flywheel plan`, `retry` and `artifacts` are still planned — until they land, create and resume
  tasks with brief files and the raw worker commands shown here.
license: MIT
metadata:
  version: 0.17.0 # x-release-please-version
---

# Flywheel Operator

Flywheel is a durable orchestrator-to-worker loop. **Any agent — or a human — can drive it.** The
CLI is the deterministic substrate; the skills are the judgment layer. You are the operator: you
decide what runs, who runs it, and whether it landed. The command table below implements
[protocol v1](../../docs/PROTOCOL.md).

## What the CLI implements today

The table below lists every command `flywheel help` prints, kept in sync with the registry (a doc
test fails the build if a command is missing here). Only `flywheel plan`, `retry` and `artifacts`
are not built yet; the rest of this skill's manual workflow explains what to do until those three
land.

| Command | Status | What it does |
| --- | --- | --- |
| `flywheel init [--track\|--ignore] [--agents-md] [--hooks] [--force]` | **implemented** | Scaffold `flywheel.md` + `.flywheel/state.json` + `.flywheel/events.jsonl` + `.flywheel/briefs/`; `--track` (default) keeps `flywheel.md` a committed file, `--ignore` adds it to the target's root `.gitignore` instead; `--agents-md` writes/refreshes an AGENTS.md block naming the installed skills; `--hooks` writes the Claude/OpenCode session-logging hooks and a Claude Code Stop hook that runs `flywheel gate` (blocks ending the session while work is left unjudged; an existing .claude/settings.json is never overwritten, so add the Stop entry by hand there); refuses an existing state file unless `--force`. Ends with a factory summary: workers, limits, audit policy, enforcement layers installed or missing (with the command to add each), and how to view the floor. |
| `flywheel version` | **implemented** | Print the flywheel version. |
| `flywheel config` | **implemented** | Read and validate `.flywheel/config.json`. |
| `flywheel doctor [--worker NAME] [--record] [--dir DIR]` | **implemented** | Probe the configured worker's model, then its fallbacks, through the worker's own adapter and print one `<model>: <class>` line per probe; exit 0 when every probe is ok, 1 when any is not. A `flywheel-local/<model>` is checked against its server first (`local endpoint down`, `model not pulled`). `--record` appends a `probed` event per model; an `ok` probe closes that model's breaker. |
| `flywheel log --task <id> --kind planned --brief <path>` | **implemented** | Record a planned brief to the event log before dispatch. `--shard` switches the event log to per-task shards under `.flywheel/events/` (one-way; the legacy file is sealed and kept) ([#47](https://github.com/suzworx/flywheel/issues/47)). |
| `flywheel state` | **implemented** | Derive and print state from the event log. |
| `flywheel run <task> [--worker NAME] [--stall-timeout D] [--increment N] [--worktree]` | **implemented** | Canonical dispatch: pick a worker from `.flywheel/config.json`, or `--worker NAME` to choose among several configured workers; attach the brief, apply the deny policy, record every event; `--stall-timeout` bounds a mid-stream gap (0 = the worker's configured `stall_timeout`, itself 600s). `--increment N` sends only increment N of a brief with an `## Increments` list, as a fresh session, and records it on the dispatched event. `--worktree` runs the worker in `.flywheel/worktrees/<task>` on branch `fw/<task>`; validate and inspect then default to that tree. |
| `flywheel validate <task> [--workdir]` | **implemented** | Run the brief header's `gate:` lines on the exact tree and check `owns`; exit 0, or 5 on a failing gate or a file outside owns. |
| `flywheel lint <brief> [--dir DIR]` | **implemented** | Check a brief for problems: missing `owns:`, `gate:`, `# TASK` or `## Checks`, owns paths that don't exist; warnings for a missing write rule or `needs:` line (exit 0/1). |
| `flywheel inspect <task> --verdict pass\|rework\|scrap\|escalate --session <own session>` | **implemented** | Record an inspection; refused with exit 6 for a bad verdict, a worker's session, or no passing readings for the tree as it is now. |
| `flywheel review <task> --verdict pass\|correct\|reject --session <own session> [--note NOTE] [--dir DIR] [--workdir PATH]` | **implemented** | Re-run a task's gates and `owns` check on an isolated copy of the tree (HEAD plus uncommitted changes) and record the reviewed verdict; refused with exit 6 for a bad verdict, a worker's session, a changed path outside owns, or a failing gate. |
| `flywheel verify [<task>...\|--all] [--json] [--log] [--workdir PATH]` | **implemented** | Check the event log against rules T1, T3, T4, T5, T8; exit 0, 6 on an established violation, or 8 when a failing check is inconclusive (a pass's tree no repository this verifier can see resolves; `--workdir` names the external clone the readings were measured in). `--log` checks the event log's hash chain (an edited or removed record fails, exit 6). |
| `flywheel audit (<task> \| --sample RATE \| --first-article \| --wave) --session <own session> [--seed N] [--list] [--note TEXT] [--workdir PATH] [--json]` | **implemented** | An independent audit of one unit or a selection: re-runs its gates in a clean copy of its tree, checks its record with the verify rules, and records `audited` `conforms`/`nonconformance` with the findings; refused with exit 6 for a session that planned, built or inspected the unit (exit 0 / 5). `--first-article` audits the first unit each worker adapter/model built; `--sample RATE` a seeded random sample of passed, unaudited units whose rate doubles-plus after nonconformances and halves after 10 clean ones; `--wave` audits every passed, unaudited unit in the ledger; `--list` prints the selection only. |
| `flywheel staff --role lead --session <session> [--model M]` | **implemented** | Register a factory role on the floor; the lead line then reads `lead <session> (<model>)`. |
| `flywheel land <task> [--merge [--onto BRANCH]] [--commit <sha> [--note TEXT]]` | **implemented** | Record a landing; refused with exit 6 unless the task passed inspection, and a different commit than a previous landing is refused. `--merge [--onto BRANCH]` lands a passed unit from its worktree through the local queue (rebase, re-gate, fast-forward, remove the worktree); on a conflict the integration branch is merged into the unit instead, the markers stay in its worktree, `.flywheel/briefs/<task>.land-delta.txt` is written for a correction, and it exits 6 (commit the resolved merge, validate, inspect, land again). |
| `flywheel factory [--once\|--json\|--plain]` | **implemented** | Render the floor — workers, units with run states, andon, output; bare `flywheel` opens it. In a terminal it is interactive (k9s-style views `:units` `:workers` `:andon` `:events` `:lines`, `/` filter, enter explain, `l` log, `?` help); `--plain` keeps the redraw loop. |
| `flywheel status [--dir DIR] [--now RFC3339] [--json]` | **implemented** | Summarize the factory deterministically: task counts per status, live and stale attempts, last event and last meaningful progress, andon count. |
| `flywheel cost [--dir DIR] [--json]` | **implemented** | Sum finished events' tokens and cost per task and per model; a finished task without a dispatch is listed under `unknown`. |
| `flywheel stats [--dir DIR] [--json]` | **implemented** | The factory's own numbers from the event log: first-pass rate, corrections per task, finish reasons and unclean rate, mean attempt seconds, cost per landed task, token totals, spend, and spend against a frontier-only baseline priced from `baseline` in config. |
| `flywheel next [--dir DIR] [--now RFC3339] [--json]` | **implemented** | Print the reconciler's next actions read-only: lost attempts, inspection requests, blocks, waits and dispatches; nothing executes them yet (a task whose owns overlap, or whose exclusive resource matches, one in flight or one already chosen waits instead). |
| `flywheel goal add "<title>" --id <id> [--accept CMD]... [--require TASK]...` | **implemented** | Record a factory goal; later add, list, show and set its status with `flywheel goal <add\|list\|show\|set>`. |
| `flywheel controller [--once] [--interval D] [--dir DIR] [--now RFC3339]` | **implemented** | The controller loop: acquire `.flywheel/controller.lock`, tick (mark lost attempts, block tasks whose needs were scrapped), renew the lock each tick; `--once` runs one tick and releases the lock, a live lock held elsewhere exits 6. |
| `flywheel claim <task> [--session S] [--ttl D] [--note TEXT] [--force] [--dir DIR]` | **implemented** | Claim a task so a second lead sharing the tree knows it is driven; refuses a live claim held by another session with exit 6 unless `--force` takes it over. Advisory only — nothing yet refuses to run because of one. |
| `flywheel release <task> [--session S] [--force] [--dir DIR]` | **implemented** | Release a claimed task; refuses to drop a live claim held by another session with exit 6 unless `--force`. Releasing an unclaimed task is a no-op (exit 0). |
| `flywheel claims [--json] [--dir DIR]` | **implemented** | List every claim sorted by task: session, note, age, and live or expired; a malformed claim file is skipped, never fatal. |
| `flywheel claim-edit --paths P1,P2 --session S [--note TEXT] [--dir DIR]` | **implemented** | Declare a lead's own edit made after a unit's dispatch, so `flywheel validate` attributes the changed paths to the declaring session instead of refusing the unit; each path is bound to the content hash recorded at claim time, so the claim covers only that edit — a later change to the path is outside again — and a non-literal path (`*`, `?`, `[`, or a trailing `/`) is refused (exit 2). Usage error (exit 2) without `--paths` or `--session`. |
| `flywheel handoff [--dir DIR] [--stdout]` | **implemented** | Print the handoff summary for a new head — in-flight tasks (with session and model), blockers, next ready tasks, the untriaged signals it carries forward, and the default worker model; with `--stdout` to stdout, otherwise into `flywheel.md` between the handoff markers. |
| `flywheel plan`, `retry` | **planned** | Control plane: create tasks, resume, transfer between agents. |
| `flywheel trace <session> [--dir DIR]` | **implemented** | Everything one session did, across tasks: one line per event whose session matches, in log order; read-only, never derives state. |
| `flywheel explain <task> [--json] [--dir DIR]` | **implemented** | One task's whole story folded from the event log: a summary (brief, owns, needs, gates, planner, goal, attempts, steps, cost, landing) and a timeline with one line per event; `--json` for machines. Read-only; exit 1 for a task with no events, 2 for a missing task id. |
| `flywheel gate [--json] [--dir DIR]` | **implemented** | Lists what blocks ending the session — tasks finished but not inspected, and untriaged signals — and exits 6 while any exist (0 when clear); `--json` for hooks. Read-only. |
| `flywheel init --git-hooks` | **implemented** | Git-layer enforcement: a `commit-msg` hook requiring a `Flywheel-Task: <id>` trailer and a `pre-push` hook running `flywheel verify` on each unit the pushed commits name, plus `flywheel verify --log`. Existing hooks are left alone. |
| `flywheel init --ci` | **implemented** | The CI layer: writes a `flywheel-audit` GitHub Actions job (`flywheel verify --all --log` on every PR; exit 8 inconclusive warns, violations fail). Make it a required check so it cannot be bypassed; needs the event log committed. |
| `flywheel init --local <model> [--local-url URL]` | **implemented** | Offline workers: adds an OpenCode provider `flywheel-local` (OpenAI-compatible, Ollama's `http://localhost:11434/v1` by default) to `.flywheel/opencode-worker.json` and a worker `local` (`flywheel run <task> --worker local`). A rerun replaces the model or URL. |
| `flywheel context [--json] [--learnings N] [--role R] [--dir DIR]` | **implemented** | The factory's state in one small read for an agent joining it: active goals, in-flight/blocked/ready tasks, units needing a verdict and untriaged signals, and the most recent undismissed learnings (default 5). `--role` (lead, planner, foreman, inspector, steward, auditor) keeps only that role's open work. Read-only. |
| `flywheel watch [--once] [--last N] [--interval D] [--dir DIR]` | **implemented** | Streams the factory's events as readable lines (time, task, the same summary `flywheel explain` prints) — the last N, then each new one as it is appended; `--once` prints and exits. Read-only. |
| `flywheel supervise [--once] [--interval D] [--json] [--dir DIR]` | **implemented** | Validates every finished unit not yet measured since it finished (supervisor readings, as `flywheel validate` records them), and re-measures a failed or passed unit whose owned files changed since its reading; `--once` one pass (exit 5 when a unit's gauges fail), `--interval D` repeats. Never inspects or lands. |
| `flywheel artifacts` | **planned** | Data plane: worker outputs. |
| `flywheel feedback [--dir DIR]` | **implemented** | List learnings, one line per learning in log order, then the untriaged signals computed from the log (each one `<task> <attempt> <signal>`; a later learning on the same task naming it with `--signals` triages it; a recurrence after that learning is untriaged again); `add --task ID --severity P0\|P1\|P2 --title T --observed O --evidence E --ask A [--signals a,b]` records a learning and rewrites `.flywheel/learnings.md`; `dismiss L-NN --reason WHY` dismisses one by id without renumbering; `regen` rebuilds `.flywheel/learnings.md` from the event log without appending anything (exit 0 when the file already matches); `export [--out PATH]` writes a sanitised Markdown report of every undismissed learning (absolute paths and tokens redacted) to stdout or a file; `submit [--yes]` sends the report upstream as a gh issue — without `--yes` it shows the exact text and refuses, and when gh is missing or fails the report is parked in `.flywheel/feedback/outbox/` and the command still exits 0. |
| `flywheel upgrade [--check] [--to VERSION] [--repo REPO]` | **implemented** | Self-update to a release with checksum verification: `--check` prints `current:`/`latest:` then `upgrade available` or `up to date` (exit 0 either way); otherwise download the host's zip, verify its SHA-256 against `checksums.txt` and install it atomically over the running binary. |

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
| `.flywheel/learnings.md` | Dogfood log — friction becomes spec; generated (regenerated from the event log on every `add`, `dismiss`, and `log --json` import carrying a learning or dismissed event, so do not hand-edit it). |

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
| `lines` | Product lines: each `{name, worker, owns}` — a part of the product, the worker that builds it, the paths it covers. A unit belongs to the line its brief names (`line:``) or the first whose `owns` cover all of its `owns:`; `flywheel run` uses that line's worker unless `--worker` is given, and refuses a brief naming an unknown line (exit 6, rule `line`). |
| `limits` | Shared caps, enforced by `flywheel run` (refused with exit 6, rule `limits`/`budget`/`rate`/`breaker`; `flywheel next` respects all: fewer DISPATCHes under `per_host` and within the model's free `rate_per_minute` slots, and HOLD with the reason instead of DISPATCH while a cost or token budget is spent or the default model's breaker is open): `per_host` (attempts in flight at once in this ledger; 0 = no cap), `budget{wave_cost_usd}` (once the ledger's recorded spend reaches it, new dispatches are refused; the ledger is the wave), `budget{wave_tokens}` (the same for recorded input+output+reasoning tokens), `rate_per_minute` (at most this many dispatches of one model in any 60 s; rule `rate`), and `breaker{errors, cooldown}` (after `errors` consecutive provider errors on a model, `flywheel run` refuses it — exit 6, rule `breaker` — until `cooldown` after the last one; then one probe is let through; unless `--model` was given, an approved fallback takes over — the first whose own breaker is closed — and otherwise the refusal names the approved fallbacks). |
| `feedback` | `upstream` (owner/repo) and `submit` (`ask` or `never`). |
| `baseline` | Frontier prices for `flywheel stats`'s cost comparison: `model`, `input_per_mtok`, `output_per_mtok`, `cache_read_per_mtok`, `cache_write_per_mtok` (USD per million tokens; reasoning is priced as output). |
| `audit` | `first_article` (bool): opt into rule T7 — `flywheel land` refuses a unit (exit 6, rule `T7`) until its worker line (adapter/model of the passing attempt) has a conforming audit, and while the line's latest audit is a nonconformance; an `--exception` landing is not gated. Set it in .flywheel/config.json as `"audit": {"first_article": true}` (`audit.first_article`). |
| `log` | `shards` (bool): opt into per-task shards under `.flywheel/events/` instead of a single `.flywheel/events.jsonl` file (issue [#47](https://github.com/suzworx/flywheel/issues/47)). Written by `flywheel log --shard`; the layout is one-way. Read-only: use `flywheel log --shard` to switch. |

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

| | Commands | Purpose | Not yet built |
| --- | --- | --- | --- |
| **Control plane** | `flywheel run`, `handoff`, `claim`, `release`, `land`, `controller` (`plan`, `retry` still planned) | Move work forward: dispatch, resume, transfer, land. | `flywheel plan`/`retry`: write brief files and run the raw `opencode` commands by hand (above). |
| **Data plane** | `flywheel status`, `trace`, `cost`, `stats`, `next` (`artifacts` still planned) | Understand state: what's in flight, where each task sits, what each worker produced. | `flywheel artifacts`: read `.flywheel/runs/` and `.flywheel/briefs/` directly. |

Both are reachable by anyone (agent or human) — the judgment layer differs, the substrate
doesn't. `flywheel plan`, `retry` and `artifacts` are still planned; everything else in this table
is built and safe to invoke.

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
cat .flywheel/learnings.md # what the loop has taught itself (generated by flywheel feedback; not hand-edited)
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