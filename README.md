<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-lockup-dark.svg">
    <img src="assets/logo-lockup.svg" alt="flywheel — own the factory" width="380">
  </picture>
</p>

<p align="center"><strong>Own the factory. Rent the agents and the intelligence.</strong></p>

A dark factory for AI coding work. The factory (work orders, gauges, the event log, its
memory) is files you own. Agents and models are rented from any vendor and swapped at will:
cheap disposable agents build, deterministic gauges check every claim, and one frontier
lead makes only the critical calls.

[![CI](https://github.com/suzworx/flywheel/actions/workflows/ci.yml/badge.svg)](https://github.com/suzworx/flywheel/actions)
[![Latest release](https://img.shields.io/github/v/release/suzworx/flywheel)](https://github.com/suzworx/flywheel/releases)
[![License](https://img.shields.io/github/license/suzworx/flywheel)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-suzworx.github.io%2Fflywheel-1f6feb)](https://suzworx.github.io/flywheel/)

**Docs and quickstart → [suzworx.github.io/flywheel](https://suzworx.github.io/flywheel/)**

## Built by flywheel

Flywheel is built by flywheel. Sujeet ([@suzworx](https://github.com/suzworx)) runs its
development as a dark factory.

- From v0.3.0 on, flywheel's own features (`flywheel run`, the gauges `validate`, `inspect` and
  `verify`, the `flywheel factory` view), the CLI help and flag fixes, and the factory design
  docs were built as work orders by disposable OpenCode workers on DeepSeek v4 flash (variant
  max).
- Each work order is a brief with `owns:`, `needs:` and `gate:` lines. flywheel's own gauges
  re-run the gates on the exact tree hash and check `owns:`; inspection and `flywheel verify`
  (rules T1, T3, T4, T5 and T8) gate every commit. Workers never commit.
- One frontier lead seat outlines work orders, reviews diffs, runs checks against the built
  binary and signs off. The lead seat changed hands between vendors mid-project and the work
  continued from the files.
- Measured worker cost: the CLI help and flag work took 7 units, 1.7M tokens and $0.33; a
  design doc was drafted, corrected and extended by 5 units for about $0.03; a code unit costs
  $0.03-0.08 and takes 15-29 minutes.

The house rules the workers follow are in [AGENTS.md](AGENTS.md).

## The factory

Flywheel is a factory for AI coding work: a small Go CLI plus agent skills that run a durable
**orchestrator-to-worker loop**. A frontier lead agent plans, briefs and judges; cheap disposable
worker agents (OpenCode) do the reading, writing and testing. The **control plane** dispatches
work and enforces policy; the **data plane** keeps traceability, telemetry and accountability for
every session.

### Roles

| Factory role | Persona | Does | Never |
| --- | --- | --- | --- |
| Plant manager | lead | sets goals and policy, handles escalations, signs off | implements |
| Production planner | planner | turns a spec into work orders (`owns:`/`needs:`/gates) | dispatches or inspects |
| Line supervisor | foreman | runs a line of workers, retries by policy, pulls the cord | plans or implements |
| Line worker | worker (OpenCode) | builds one work order at its own station | plans, inspects, commits |
| Machine gauges | supervisor (CLI, no model) | measures every unit: runs gates, checks `owns:` | judges intent |
| QC inspector | inspector | inspects a unit against its work order | runs gauges or fixes units |
| External auditor | auditor | audits first articles and samples, files nonconformances | works the line |
| Continuous improvement | steward | turns signals and nonconformances into learnings | changes units |
| Owner | operator | installs, assigns roles, sets merge and publish policy | — |

### A unit's path

```mermaid
flowchart LR
    WO["Work order"] --> W["Worker (OpenCode)"]
    W --> G["Machine gauges"]
    G --> I["QC inspector"]
    I --> A["Auditor (first articles, samples)"]
    A --> L["Landing"]
    I -. "rework" .-> W
```

### Set up, run, watch

- **Set up** — `flywheel init` scaffolds `flywheel.md` plus the `.flywheel/` state files
  (available in v0.2.0). Building the full factory — lines, staffing, and the policy that keeps it
  safe — is [epic #69](https://github.com/suzworx/flywheel/issues/69).
- **Run** — the lead records each work order as an event with `flywheel log --kind planned`;
  [flywheel run #20](https://github.com/suzworx/flywheel/issues/20) dispatches OpenCode workers
  and records their runs automatically (v0.3.0).
- **Watch** — `flywheel state` derives the floor from the event log (available in v0.2.0); the live
  floor is the [flywheel factory #63](https://github.com/suzworx/flywheel/issues/63) view.

Design priorities, in order: **efficiency and consistency**, then **speed, reliability and
recoverability** — the lead spends tokens only where judgment is needed, gauges and telemetry cost
no tokens, and everything replays from the log after any crash.

## Why flywheel

Frontier-quality results at a fraction of frontier cost — measured, not promised: the field run
behind the skills used **99 worker runs across 44 tasks, up to 7 workers in parallel, and about
90 % of all worker input across the field run served from the model cache**. `max_parallel` caps
concurrent *workers*, not concurrent *gates*: units whose `gate:` lines name the same expensive
command serialise on it, and the contention manufactures false findings (phantom timeouts) that
read as code defects — `flywheel run` prints a shared-gate warning at dispatch whenever an
in-flight unit declares the same gate line.

The goal is to ship with no human in the loop. That is only safe if every step is **recorded** (an
append-only event log), **measured** by the machine (gauges run by the CLI, never self-reported by
an agent), and **audited** by independent checkers. Today the repo has the event log with
`flywheel log` / `flywheel state`, the project config, the worker deny policy, and the persona
skills; the gauges ([#55](https://github.com/suzworx/flywheel/issues/55)) — `validate`, `inspect`
and `verify`, with `inspect` refusing a worker inspecting its own unit
([#24](https://github.com/suzworx/flywheel/issues/24)) — and the run dispatcher
([#20](https://github.com/suzworx/flywheel/issues/20)) shipped in v0.3.0; external audit
([#61](https://github.com/suzworx/flywheel/issues/61)) is still planned.

Any agent can lead. The loop lives in repository files and shell commands, not inside any one
vendor's session, so a new head — Claude Code, Codex, OpenCode, or a human — reads the same state
and continues the same work.

## How it works

The loop is five steps: **Plan → Brief → Dispatch → Review → Correct-or-land**. Steps 1, 4 and 5
are lead judgment; 2 and 3 are mechanical.

**Repo is the session.** All state lives in the repository as files — the event log, work orders,
worker plans, reports — not inside any vendor session. That is what makes agents swappable and the
loop crash-safe: run out of tokens, hit a host timeout, lose a watcher; the next head reads the
same files and continues, and nothing is lost.

Two design docs define the factory:

- [Autonomous shipping: the flywheel factory](docs/design/autonomous-shipping.md) — the factory
  model, the required events, the poka-yoke transition rules, and the enforcement layers.
- [Flywheel at scale](docs/design/flywheel-at-scale.md) — personas, many parallel workers,
  per-task worktrees and landing, native feedback, and offline use.

## See it

Real transcripts from this branch, rendered from actual CLI output.

*`flywheel init` scaffolds `flywheel.md` plus the `.flywheel/` state files.*

![flywheel init scaffolds a project](docs/assets/demo-init.svg)

*A task's life is recorded in the append-only event log; state is derived from the log, not stored
alongside it.*

![a task recorded in the event log, state derived from it](docs/assets/demo-log-state.svg)

*Real output: the worker's tree-rewriting git commands are refused even under `--auto`; read-only
git still works.*

![a worker cannot rewrite the shared tree](docs/assets/demo-deny-policy.svg)

*The gauges measure the tree; inspect refuses to pass a unit that has no supervisor readings on record.*

![the gauges measure; the inspector cannot pass without readings](docs/assets/demo-gauges.svg)

*Real output: the factory floor at a fixed instant, showing landed, running, finished-awaiting-inspection, capped and planned units.*

![flywheel factory: the floor at a glance](docs/assets/demo-factory.svg)

### Measure, don't trust

`flywheel validate` runs a task's gates and checks its `owns` boundary, recording supervisor readings; `flywheel inspect` only passes a unit whose readings are on record (and refuses a worker session's verdict); `flywheel verify` checks every poka-yoke rule. `validate` exits 5 when a gate fails or a change sits outside `owns`, and an `inspect` refusal exits 6.

### Worker adapters

`flywheel run` dispatches through one of three adapters: `opencode`, `claude`, or the offline
`sim` adapter used by tests and this repo's own demo. Each worker in `.flywheel/config.json` names
its adapter; `flywheel run --worker <name>` picks between several configured workers, so one
factory can be all-OpenCode, all-Claude, or a mix. The `opencode` adapter is the original,
most-used path. The `claude` adapter (issue [#49](https://github.com/suzworx/flywheel/issues/49))
has been exercised against the live CLI: `flywheel run` dispatches it, parses the
`--output-format stream-json` stream, captures the session id and the assistant text, and
records the finish. That run did not get past authentication in the environment where it
was tried, so a productive run — tool calls, file edits, token and cost accounting — is
still unverified. Try one real run in your own environment before depending on it.

On the `claude` adapter, a worker's only way to run its own gate lines is its Bash tool, and
`--permission-mode acceptEdits` alone grants file edits — not commands. Each worker in
`.flywheel/config.json` can therefore set `allowed_tools` and `disallowed_tools` (Claude Code
tool patterns) to shape the dispatch's `--allowedTools`/`--disallowedTools`. Unset, a worker
defaults to `allowed_tools: ["Bash"]` and a `disallowed_tools` covering the git-write family
(`Bash(git commit:*)`, `Bash(git push:*)`, `Bash(git stash:*)`, `Bash(git reset:*)`,
`Bash(git checkout:*)`, `Bash(git rebase:*)`, `Bash(git merge:*)`): the worker can run its own
gates but still cannot commit, stash, reset, checkout, rebase or merge — the worker permission
policy is enforced by the permission layer, not by asking nicely. An explicitly configured list
replaces its default; it is not merged with it, so an operator can widen or narrow deliberately.

`flywheel run` exits 0 on a clean finish, exit 3 on a silent start (no output before the start
timeout), 4 when the worker exited nonzero, capped, or hit a provider error, and exit 7 on a
mid-stream stall (no run-file line for the stall timeout while the process is still alive).

## Quickstart

New here? Start with the two pages that close the gap between "I have a binary" and "I have
landed one unit through the loop": [**Quickstart**](docs/quickstart.md) walks you through it end
to end, and [**Concepts**](docs/concepts.md) defines the vocabulary every command rests on —
work orders, `owns:`, gates, the event log, and the poka-yoke rules.

1. **Get the CLI.** Download **flywheel-\<version\>-\<os\>-\<arch\>.zip** from the
   [latest release](https://github.com/suzworx/flywheel/releases/latest) — Windows binaries ship
   as `flywheel-<version>-windows-amd64.exe.zip` — plus `checksums.txt`, and verify the SHA-256
   before putting it on PATH (the archive holds a single binary named after the release, which
   you rename to `flywheel` on PATH). Once installed, `flywheel upgrade --check` tells you whether a
   newer release exists and `flywheel upgrade` installs it, checksum-verified. Or build from
   source:

   ```bash
   git clone https://github.com/suzworx/flywheel.git && cd flywheel
   mkdir -p bin && go build -o bin/flywheel ./cmd/flywheel
   ```

   Or install with `go install github.com/suzworx/flywheel/cmd/flywheel@latest`: go.mod declares
   the module as `github.com/suzworx/flywheel`, so the path resolves through the Go proxy and the
   resulting `flywheel` binary lands in `$(go env GOPATH)/bin`.

2. **Install the skills** into your agent — one per skill folder below:

   ```bash
   npx skills add suzworx/flywheel --skill flywheel
   npx skills add suzworx/flywheel --skill flywheel-worker
   # ... flywheel-planner, flywheel-foreman, flywheel-inspector,
   #     flywheel-auditor, flywheel-steward, flywheel-operator
   ```

3. **Scaffold a project:**

   ```bash
   flywheel init --dir demo
   ```

4. **Run.** Ask your lead agent to load the `flywheel` skill and drive the loop: plan → brief →
   dispatch → review → correct-or-land. The lead writes precise bounded briefs, dispatches OpenCode
   workers, and judges the evidence. `flywheel run` records each dispatch automatically; the
   lead records the other events (reviews, landings) with `flywheel log`.

### Upgrading

- Swap the binary; renaming the old executable is safe while a unit runs.
- Keep symlinked skill installs rather than copies — the skills installer can replace symlinks
  with copies.
- Commit `skills-lock.json`, or any tracked file the skills installer touched, before the next
  `flywheel validate`, because files the lead changes after a unit's dispatch count against that
  unit's owns check. A lead-side edit made after dispatch can be declared afterwards with
  `flywheel claim-edit --paths <p1,p2> --session <session>`, so the owns check attributes it
  instead of refusing the unit; the claim is bound to the content it declared, so a later change
  to the same path is outside again (issue #258).
- Run `flywheel status` afterwards.

## CLI

`flywheel help <command>` (or `<command> -h`) prints any command's flags.

| Command | Status | What it does |
| --- | --- | --- |
| `flywheel version` | available (v0.2.0) | Print the flywheel version. |
| `flywheel init` | available (v0.2.0) | Scaffold `flywheel.md` + `.flywheel/state.json` + `.flywheel/events.jsonl` + `.flywheel/briefs/`. |
| `flywheel log` | available (v0.2.0) | Append an event to `.flywheel/events.jsonl` and re-derive state. |
| `flywheel state` | available (v0.2.0) | Derive and print state from the event log. |
| `flywheel config` | available | Read, validate and `set` `.flywheel/config.json` (config package merged). |
| `flywheel doctor` | available | Probe every configured model and classify its availability (exit 0/1). |
| `flywheel run` | available | Dispatch a worker (adapter and model from `.flywheel/config.json`, or `--worker <name>`) and capture the run. `[--increment N]` sends only increment N of a brief with an `## Increments` list, as a fresh session, and records it on the dispatched event. |
| `flywheel status` | available ([#21](https://github.com/suzworx/flywheel/issues/21)) | Summarize the factory: task counts, live/stale attempts, last event and progress, andon. |
| `flywheel handoff` | available | Print the handoff summary for a new head: in-flight tasks (with session and model), blockers, next ready tasks, the untriaged signals it carries forward, and the default worker model; `--stdout` prints it, otherwise it goes into `flywheel.md`. |
| `flywheel cost` | available ([#29](https://github.com/suzworx/flywheel/issues/29)) | Sum finished events' tokens and cost per task and per model. |
| `flywheel stats` | available ([#41](https://github.com/suzworx/flywheel/issues/41)) | The factory's own numbers: first-pass rate, corrections per task, finish reasons, mean attempt time, cost per landed task, token totals, spend, and spend against a frontier-only baseline priced from `baseline` in config. |
| `flywheel next` | available | Print the reconciler's next actions read-only: lost attempts, inspection requests, blocks, waits and dispatches. |
| `flywheel watch` | planned ([#22](https://github.com/suzworx/flywheel/issues/22)) | Watch the line, one readable line per transition. |
| `flywheel validate` | available | Machine gauges: run a task's gate: lines on the exact tree and check owns (exit 0/5). |
| `flywheel lint` | available | Check a brief for problems: owns, gate, goal, report contract, owns paths (exit 0/1). |
| `flywheel supervise [--once] [--interval D] [--json] [--dir DIR]` | available ([#55](https://github.com/suzworx/flywheel/issues/55)) | Machine gauges without anyone asking: every unit whose worker finished and whose current attempt has not been measured since is validated (gates and owns, recorded as supervisor readings). `--once` runs one pass (exit 5 if any unit's gauges fail); `--interval D` repeats. Never inspects or lands. |
| `flywheel verify` | available | Check the event log against the transition rules T1, T3, T4, T5, T8 (exit 0/6). `--log` also checks the event log's hash chain. |
| `flywheel inspect` | available | Inspection verdict, refused unless the gauges' readings cover the tree as it is now (T3/T4/T8; exit 6). |
| `flywheel review <task> --verdict pass\|correct\|reject --session <session> [--model M]` | available ([#24](https://github.com/suzworx/flywheel/issues/24)) | Re-run a task's gates and owns check on an isolated copy of the tree, so another in-flight worker's half-written files can't skew the reading; refused (exit 6) for a bad verdict, a worker's session, a changed path outside owns, or a failing gate. The reviewer's session and model are recorded on the reviewed event. |
| `flywheel audit` | planned ([#61](https://github.com/suzworx/flywheel/issues/61)) | External audit of first articles and samples. |
| `flywheel land <task> --commit <sha> [--exception TEXT --session S] [--allow-untriaged REASON]` | available | Record a landing for a passed task; refused without a passing inspection (exit 6, rule T5). Refused (exit 6, rule T9) while the task has untriaged signals unless `--allow-untriaged <reason>` records why. Use `--exception "<what you ran and saw>" --session <your session>` to land a hand-verified unit on a recorded exception that `verify` reports. |
| `flywheel factory` | available | Live terminal dashboard of the floor; bare `flywheel` opens it ([#63](https://github.com/suzworx/flywheel/issues/63)). |
| `flywheel controller` | available | The controller loop: one tick at a time (single-process lock), marking lost attempts and blocking tasks whose needs were scrapped. |
| `flywheel staff` | available | Register the lead (or another role) on the floor. |
| `flywheel goal` | available | Manage the factory's goals: add, list, show and set (add, list, show, set). |
| `flywheel claim <task>` | available ([#165](https://github.com/suzworx/flywheel/issues/165)) | Claim a task for a session so another lead sharing the tree knows it is driven; refused (exit 6) for a live claim held elsewhere unless `--force`. |
| `flywheel release <task>` | available ([#165](https://github.com/suzworx/flywheel/issues/165)) | Release a claimed task; refused (exit 6) for a live claim held elsewhere unless `--force`. |
| `flywheel claims` | available ([#165](https://github.com/suzworx/flywheel/issues/165)) | List every claim: task, session, note, age, live or expired. |
| `flywheel claim-edit` | available ([#228](https://github.com/suzworx/flywheel/issues/228)) | Declare a lead's own edit made after a unit's dispatch so the owns check attributes it instead of stranding the unit; the claim records each path's content hash, so it covers only the edit declared, and a non-literal path is refused (exit 2) ([#258](https://github.com/suzworx/flywheel/issues/258)). |
| `flywheel trace <session> [--dir DIR]` | available ([#62](https://github.com/suzworx/flywheel/issues/62)) | Everything one session did, across tasks. |
| `flywheel explain <task> [--json] [--dir DIR]` | available ([#58](https://github.com/suzworx/flywheel/issues/58)) | One task's whole story from the ledger — brief, planner, attempts with steps and cost, gate readings, verdicts, signals and landing — as Markdown or JSON. Read-only. |
| `flywheel gate [--json] [--dir DIR]` | available ([#56](https://github.com/suzworx/flywheel/issues/56)) | Exit 6 while work is left unjudged — finished units not yet inspected, untriaged signals — listing each; exit 0 when clear. For agent Stop hooks. `flywheel init --hooks` installs it as a Claude Code Stop hook. Read-only. |
| `flywheel context [--json] [--learnings N] [--dir DIR]` | available ([#58](https://github.com/suzworx/flywheel/issues/58)) | A compact pack of the factory's state for a joining agent: active goals, in-flight, blocked and ready tasks, what still needs a verdict or triage, and recent learnings. Read-only. |
| `flywheel feedback` | available (add/list/dismiss/regen/export/submit) | Turn signals into learnings: lists the untriaged signals (a signal is triaged once a later learning on its task names it with `--signals`; a recurrence after that learning is untriaged again); `add`, list, `dismiss`, and a generated `learnings.md`; `regen` rebuilds `learnings.md` from the event log without appending; `export [--out PATH]` writes a sanitised Markdown report of undismissed learnings; `submit [--yes]` sends it upstream as a gh issue — consent-first, with an offline outbox when gh fails. |
| `flywheel upgrade` | available ([#201](https://github.com/suzworx/flywheel/issues/201)) | Self-update to a release with checksum verification: `--check` prints current and latest and whether an upgrade is available; otherwise download, verify the SHA-256 and install atomically. |

## Skills

Each role ships as a skill folder any agent can load:

- [`flywheel`](skills/flywheel/SKILL.md) — the lead: drive the loop, judge evidence, never implement.
- [`flywheel-planner`](skills/flywheel-planner/SKILL.md) — write work orders; never dispatch.
- [`flywheel-foreman`](skills/flywheel-foreman/SKILL.md) — run a line of OpenCode workers; retry by policy.
- [`flywheel-worker`](skills/flywheel-worker/SKILL.md) — execute one brief, run its gates, report evidence.
- [`flywheel-inspector`](skills/flywheel-inspector/SKILL.md) — QC verdicts: pass, rework, scrap, escalate.
- [`flywheel-auditor`](skills/flywheel-auditor/SKILL.md) — independent audit of first articles and samples.
- [`flywheel-steward`](skills/flywheel-steward/SKILL.md) — turn signals and nonconformances into learnings.
- [`flywheel-operator`](skills/flywheel-operator/SKILL.md) — install, configure, assign personas.

## Roadmap

Three epics drive the factory:

- [#10](https://github.com/suzworx/flywheel/issues/10) — the factory core: the append-only event
  log, project config, and built-in feedback.
- [#35](https://github.com/suzworx/flywheel/issues/35) — flywheel at scale: native feedback,
  personas, many workers, offline.
- [#51](https://github.com/suzworx/flywheel/issues/51) — autonomous shipping: a unit's full path
  from work order to landing, with no human in the loop.

## CI/CD

- **`ci`** runs on every PR and push to main: build, vet and tests on Linux, Windows and macOS,
  gofmt, a cross-compile of all release targets, a JSON parse check, and a PR-title check.
- **`release`** keeps one release PR open; merging it tags `vX.Y.Z`, publishes the GitHub release,
  and attaches binaries for five platforms plus `checksums.txt`.
- Bump rules, highest wins: `type!` or `BREAKING CHANGE:` → major (minor while major is 0);
  `feat` → minor; `fix`/`perf`/`docs`/`refactor`/`revert` → patch; anything else → no release.
  PRs are squash-merged, so the PR title decides the bump. The release PR is opened by GitHub
  Actions, so CI checks do not run on it (a `GITHUB_TOKEN` limitation).
- **`watch`** monitors feedback channels persistently without checking out code: when
  devin-ai-integration[bot] submits a review or comment on a PR, it adds the `review-findings`
  label, and an hourly reconcile (also run on every push to a PR from this repository) adds or
  removes that label so it matches whether the bot still has unresolved review threads. When a
  `ci` run for a push to `main` in this repository fails, it opens an issue titled "CI failing on
  main" with the run URL and commit details, or comments on the open one; when a later push run
  succeeds and is still the latest completed run on `main`, it closes that issue with the recovery
  run URL, so the issue records the whole episode. Incident jobs are serialised, so a failure and
  a recovery never race. PR runs and branches named `main` in forks never open an incident.
  (Watching a consumer project's local learnings file, outside this repository, still requires a
  person or a persistent session.)

## Learnings

Consumer repos keep their learnings in `.flywheel/learnings.md` — the log of what hurt, so friction
becomes spec (flywheel will generate it from signals,
[#38](https://github.com/suzworx/flywheel/issues/38)). This repo gitignores that file and tracks
its own learnings as issues under [epic #10](https://github.com/suzworx/flywheel/issues/10).

## License

MIT — see [LICENSE](LICENSE).