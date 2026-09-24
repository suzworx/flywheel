# The factory is files: owned setup, run intelligence

Status: proposal · 2026-09-13 · builds on autonomous-shipping.md and flywheel-at-scale.md
## Principle

The factory is something you own; intelligence is something you run. Owning means
the setup is committed, readable and reviewable like any other code: which product
lines exist, how many workers of which kind staff each line, what the process is,
which agents are used and what is inside each agent. Running means a disposable
session that reads those files, does work, and reports back through the event log.
At any moment anyone — a human, the lead, an auditor, CI — answers "what is the
factory and what is it doing" from committed files alone: no runtime, no asking.

File-based means the factory's configuration is files, not sessions or state in a
vendor cloud: `.flywheel/lines/<line>.md` (one per product line: what it produces,
the process, staffing, limits), `.flywheel/agents/<name>.md` (one per agent: role,
runtime, model, variant, the tools the runtime gives it in the repo, lines
staffed, max_parallel), and `.flywheel/config.json`, the machine view the CLI
already loads (see `## Files`).

Two sources of truth, cleanly split. The event log (`.flywheel/events.jsonl`) stays
the source of truth for what happened: append-only and replayable today;
hash-chaining is planned (#57). The
factory files are the source of truth for how the factory is set up: committed,
diffable, reviewed in the same PRs as the code they describe. `flywheel.md` is the
derived view — the generated factory report, rewritten on every event — so "what
is the factory doing" is one committed file that changes only when it did.

The economy follows the ownership: frontier models are expensive, so they do
only critical work: ambiguous requirements, design choices, changes to safety or
permissions, verdicts where a wrong pass is costly, exceptions raised by the
andon, and sampled sign-off. Disposable cheap workers (OpenCode, e.g. DeepSeek
on variant max) do the volume: building, tests, fixes, drafting work orders and
briefs from the lead's short outline, check scripts, docs, PR and issue text,
and first-pass inspection with the gauges' evidence. The goal is a 100x
outcome. Every worker agent is file-based, and every capability its runtime
gives it in the repo is used, not a subset: the agent file declares the full
tool surface so a brief can point at it and the adapter can honor it (issue
#49).

## Roles

One agent file per role instance; the role is what the file says.

| Role | Does | Runtime | Example instance |
| --- | --- | --- | --- |
| Owner | installs flywheel, assigns roles, sets merge and publish policy | human | the person running this repo |
| Lead | sets goals, outlines and approves work orders, makes the critical calls, judges escalations and samples; the one budgeted frontier seat; never implements | frontier model (Claude or Codex) | `lead-claude` |
| Planner | drafts work orders from a spec: `owns:`/`needs:`/gates; the lead approves each work order before dispatch | cheap model via OpenCode | `planner-1` |
| Foreman | runs a line: dispatches, watches run states, retries by policy (stall: redispatch, capped: split), pulls the andon cord | cheap model via OpenCode, or the CLI alone | `foreman-ds-1` |
| Builder | executes one brief at its own station and reports evidence | cheap model via OpenCode | `builder-ds-1` .. `builder-ds-4` |
| Gauge | runs the gates on the exact tree, checks `owns:`, records readings bound to the tree hash | deterministic program: `flywheel validate`, later `flywheel supervise` (#55) | the CLI, no model |
| Inspector | inspects finished units against their work order; pass / rework / scrap / escalate, using the gauges' readings | cheap model via OpenCode | `inspector-ds-1` |
| Auditor | audits first articles, samples and records; files nonconformances | cheap or frontier model, different vendor where possible | `auditor-1` |
| Steward | triages signals and nonconformances into learnings | cheap model via OpenCode | `steward-ds-1` |

The split is by criticality and the cost of a mistake, not by activity; the
lead approves every draft.

Rule T4: the inspector's session never equals the worker's session or the session
that planned the task — `flywheel verify` and `flywheel inspect` refuse. The
auditor is never the same session as the lead, planner or inspector, and should be
a different model or vendor; its report is only worth something if it cannot be
talked out of it. A worker never records gauge readings, inspections or audits.

What may be a cheap model: builder, foreman, planner (it only drafts; the lead
approves each work order before dispatch — the approval is the frontier decision
point, the drafting is not), inspector (with the gauges' evidence), steward.
What may not: owner (human), lead (the one budgeted frontier seat, which plans,
judges and approves work orders), gauge (no model at all), and auditor
(independent by construction).

## Replaceable roles

In a factory, people are replaceable, and so are agents, in every role. A role
is a seat defined by files; the agent in the seat can be swapped at any moment,
for an agent from any vendor, and the work continues. Example: a seat starts
with a Codex agent and continues with a Claude agent.

The mechanism:

- all role state lives in files: the event log (who did what, when), the briefs
  (work orders, with `amended` events explaining every change), the line and
  agent files (setup), flywheel.md (the picture); a joining agent reads a
  per-role context pack (`flywheel context --role <role>`, issue #58) and
  continues;
- sessions are disposable: nothing a role needs lives only in one session's
  memory;
- every swap is recorded: `flywheel staff --role <role> --session <new> --model
  <m>` appends a `staffed` event, and the report shows who holds each seat and
  since when;
- rules key on sessions and seats, never vendors: T4 compares sessions, so a
  swapped inspector is simply a new session;
- runtime adapters (#49) let any vendor fill any seat; swapping the runtime
  means pointing the seat at a different agent file;
- one holder per seat at a time: a lease on the seat (#47) that the new holder
  takes and the old one loses, so two agents can never act as the same role at
  once.

Evidence from 2026-09-13 (generic wording, no project names or local paths):
the lead seat changed hands twice, from one frontier model to another from a
handoff file when a session budget ran out, and back. The second lead read the
files, registered with `flywheel staff`, inspected and committed a unit,
dispatched the next, and amended that unit's brief twice with the reasons in
the log; the first lead resumed from the event log and those amendments and
adopted the second lead's queued follow-up unit instead of writing a duplicate.
What broke: nothing stopped both leads acting on one worktree for a few minutes
(the guards stopped the collision before anything was committed), which is why
seats need leases.

## Files

### `.flywheel/lines/<line>.md` — a product line

Header block (`key: value`, like the brief parser reads) plus a body describing
the process. One file per product/repo path.

```markdown
name: flywheel-cli
product: .   # this repo
process: plan, build, gauge, inspect, audit, land
staffing: lead: 1, planner: 1, foreman: 1, builder: 4, inspector: 1, steward: 1
default_gates:
  - go build ./... && go vet ./...
  - go test ./...
  - GOOS=linux go vet ./...
wip_limit: 4
eta_target: 30m
```

The body holds the process description (which stations a unit passes, in order)
and line-specific rules. Agents are assigned by name through the agent files, so
"how many workers of which kind staff each line" reads from these two file kinds.

### `.flywheel/agents/<name>.md` — one agent

```markdown
name: builder-ds-1
role: builder
runtime: opencode
model: openrouter/deepseek/deepseek-v4-flash-0731
variant: max
tools: bash, read, edit, write, glob, grep, todowrite, task
lines: flywheel-cli
max_parallel: 4
```

`tools:` is the full tool surface this runtime gives the agent in the repo — not a
subset (see `## Using every capability`). `max_parallel: 4` matches the config
today; `builder-ds-1..4` are one agent file or four — the file says which.

### `.flywheel/config.json` — keep it, generated

Keep config.json, but make it a derived file. The CLI already loads and strictly
validates it (`LoadConfig`, unknown fields refused), and the factory view reads it
today; a markdown header parser would be a second, looser code path. So the line
and agent files are the authored source of truth; `flywheel config sync` rewrites
config.json from them (workers, models, variants, max_parallel, fallbacks,
limits). One source of truth to edit, one machine format to run.

### `flywheel.md` — the generated factory report

Today flywheel.md is placeholder text and untracked (issue #82). It becomes a
generated report, committed and rewritten on every event: `WriteState`, which
every command that appends an event already calls, regenerates the report (the
same renderer as `flywheel factory`, written to a file instead of the terminal):
the per-line and per-unit state of `## State and ETA`, the staffing, the andon
signals and the output tally. Committed means the factory's history is reviewable
in git; rewritten means the file is a snapshot, never a log — readers who want
the record read the event log, readers who want the picture read flywheel.md.

## State and ETA

The report (`flywheel.md`) and the live `flywheel factory` view show the same
derived state, per line and per unit. Per line:

- queue length — planned units not yet dispatched on the line
- WIP vs limit — in-flight units against the line's `wip_limit` and the agent's
  `max_parallel`
- throughput — landed units per hour over the last 24 h and 7 d
- lead time — median wall time from `planned` to `landed` for finished units
- cost per unit — worker cost from `finished` events, and frontier cost per landed
  unit against the baseline of issue #59
- headcount by role — from the line and agent files, and how many agents are live
- utilisation — busy slots over staffed slots for each agent

Per unit: stage (planned, building, finished, inspecting, audited, landed,
blocked), attempt number, steps (from the run file), elapsed since the unit's
last event, run state (running, exploring, long-step, silent, stalled, capped,
provider-error, blocked, no-writes, done — the classifier in internal/flywheel/factory.go), and ETA.

**ETA.** The median wall time of finished units on the same line and agent, minus
elapsed, with the sample size; "no history" when there is none. Example: a unit
on `flywheel-cli` with agent `builder-ds-1` has 9 finished siblings whose median
wall time is 22 min; the unit has run 7 min, so its ETA is 15 min (n=9). A line
with no finished units shows "no history" instead of a number.

Rendered example (flywheel.md, as committed):

```markdown
## Status
status: running

| Task | Stage | Attempt | Steps | Elapsed | ETA | Run state |
| --- | --- | --- | --- | --- | --- | --- |
| factory-design | building | 1 | 41 | 7m | 15m (n=9) | running |
| stats-pack | planned | 0 | - | - | no history | waiting |

## Lines
| Line | Queue | WIP / limit | Throughput | Lead time | Cost / unit |
| --- | --- | --- | --- | --- | --- |
| flywheel-cli | 3 | 4 / 4 | 2.1/h | 24m | $0.05 |

## Staffing
lead: lead-claude · planner: planner-1 · foreman: foreman-ds-1
builder: builder-ds-1..4 (3 busy / 4) · inspector: inspector-ds-1
steward: steward-ds-1

## Andon
| Task | State | Age |
| --- | --- | --- |
| (none) |
```

Every number comes from the event log, the run files and the factory files; no
model is asked anything to draw it (the factory view already builds from these
sources alone, internal/flywheel/factory.go).

## Panes

The factory view and flywheel.md show four panes, not only state. Each pane
renders from committed files alone — the event log, the run files, the line
and agent files — and no model is asked anything to draw it.

- **Data** — what exists and what happened: units, events, briefs, run files,
  gauge readings, and the traveler per unit: its planned path through the
  stations and where it actually went (#58).
- **Control** — what the factory is set to do and who may act: the queue, WIP
  limits, seats and leases (#47), the policies (worker permission policy,
  gates, merge policy), and approvals waiting on the lead.
- **Observability** — why things look as they do: run states, the andon with
  causes (stalled, capped with the reasoning spent, host-blocked), a per-unit
  timeline, the last tool calls, and traces (#62).
- **Telemetry** — numbers over time: throughput, lead time p50/p90, ETA
  accuracy (predicted vs actual), cost per unit (worker vs frontier),
  frontier tokens per landed unit, first-pass rate, rework rate, gauge pass
  rate, host-block rate, reasoning tokens per step.

A floor sketch of the four panes:

```text
DATA                CONTROL             OBSERVABILITY        TELEMETRY
7 units landed      queue: 3, WIP 4/4   stats-pack: running  throughput 2.1/h
1,402 events        seats: all leased   factory-design:      lead time 24m
39 briefs           gates: strict       stalled (host-block) ETA error ±3m
12 run files        approvals: 1        last tool calls:     first-pass 80%
0 gauge failures    leases: #47         traces: 9 sessions   frontier $/unit 0.03
```

## Using every capability

An agent file's `tools:` line declares the runtime's full tool surface in this
repo — the tools, and the permission policy that governs them. The adapters
(issue #49) translate that declaration into the runtime's real flags, so a brief
can say "use everything `builder-ds-1` has" and the adapter honors it rather
than a subset.

- OpenCode: the tools the CLI exposes in the repo (bash, read, edit, write,
  glob, grep, todowrite, task) plus the permission policy `flywheel run` already
  applies (read-only git, no write commands, `--pure`, `--auto`, closed stdin).
- Claude Code: subagents, MCP servers, web/browser access — declared as the
  tools a `claude`-runtime agent file may name, wired by the adapter through
  flags and hooks.
- Codex CLI: sandbox mode and patch application — declared as `sandbox: on` and
  `patches: on` in the header; the adapter maps them onto `codex exec` flags.
- Generic CLI: any headless runtime the adapter seam can drive; `tools:` is the
  contract between the brief and the adapter.

Briefs point at the agent file instead of restating the toolset, so the
capability list lives in one place and a runtime change updates one file, not
every brief.

## Value

- The 100x is landed output per frontier dollar, measured: frontier tokens and
  cost per landed unit, `flywheel stats` against the frontier-only baseline
  (issue #59), with the frontier spend trending down as the cheap inspector's
  first-pass rate rises. Frontier tokens only for critical work — approvals,
  design and safety calls, costly verdicts, escalations and sampled sign-off;
  the claim is measured, not promised, so
  a wave proves the fraction.
- Deterministic gauges instead of model review for checkable facts. A gate is a
  command with an exit code bound to a tree hash; a model's "looks fine" is not
  evidence. Gauges cost no tokens and never flatter.
- A cheap inspector with the gauges' evidence. The inspector applies judgment
  within the readings' envelope; the lead inspects only escalations and samples,
  and the sample shrinks as first-pass rate rises.
- Andon-driven foreman actions, so the lead sees exceptions only: stall →
  redispatch; capped → split the brief; provider error → breaker. The foreman's
  policy lives in the line file; the lead is paged by escalation, not routine.
- Portability, because the factory is files. A new head — Claude Code, Codex,
  OpenCode, or a human — reads the same lines, agents, config and report and
  continues the same factory. There is no vendor session to migrate, because
  there never was one.

## Benchmark

The claim is landed output per frontier dollar, so it is measured the way the
Holistic Agent Leaderboard measures cost-controlled accuracy: the same task
set and the same verifier, two arms, and only cost and resolve rate on the
axes.

- Arm A: a frontier model alone as the coding agent — no workers, no gauges,
  no lead role, the model's own tools.
- Arm B: flywheel — one frontier lead planning and judging, cheap workers
  building, deterministic gauges verifying.

Report per arm: resolve rate, dollars per resolved task, wall time per task,
frontier tokens per resolved task, and a cost-versus-accuracy Pareto plot
(cost per resolved task on x, resolve rate on y, one series per task set).

Task sets, each pinned to a fixed subset:

- SWE-bench Verified, e.g. 50 tasks; the verifier is the benchmark's own
  tests.
- Terminal-Bench 2.1, 89 command-line tasks with their verifiers.
- flywheel's own issue backlog; the verifier is the unit's gates and
  inspection.

Reproducibility: pin the model, the variant and the brief templates before
the run; the event log and the run files are the published evidence — anyone
can replay the wave from the files, not just read the summary.

Reference points:

- SWE-bench Verified: per-task cost ranges from $0.08 to $32 across agents, a
  400x spread driven by model and scaffold; one leading agent reported 79.2%
  resolved at $1.26 and 10.5 minutes per issue
  ([swebench.com](https://www.swebench.com),
  [Sonar's report](https://www.sonarsource.com/company/press-releases/sonar-claims-top-spot-on-swe-bench-leaderboard/)).
- Holistic Agent Leaderboard (ICLR 2026): 21,730 rollouts across 9 models and
  9 benchmarks; the costliest models are rarely on the cost-accuracy Pareto
  frontier; 1% accuracy can cost 100x
  ([arXiv:2510.11977](https://arxiv.org/abs/2510.11977)).
- Terminal-Bench:
  [github.com/harbor-framework/terminal-bench](https://github.com/harbor-framework/terminal-bench)
  ([paper](https://arxiv.org/abs/2601.11868)).

## Techniques

The prompt-structure techniques flywheel uses, each with why and the measured
effect on this project (2026-09-13):

- **Structured brief header** (owns / needs / gate lines): the gauges can
  verify every claim in the brief, and the brief's hash is checked against
  the event log (rule T1); a gate is a command with an exit code, not a
  promise.
- **One write per response (at most 120 lines), read-only calls batched**:
  output caps come from large writes, so writes are the scarce resource;
  unbatched reads made every read a round trip — 23 s per step, 44% of model
  time — so reads are batched instead.
- **Increments as fresh sessions**: a resumed session grew from 104k to 279k
  tokens and from 18 to 63-79 s per step as context accumulated; fresh
  correction sessions ran 11-36 steps in 96-431 s. Large increments start
  fresh; corrections are cheap enough to restart.
- **Large files in named parts, and the needed APIs listed in the brief**:
  workers edit one named part per write instead of scanning the whole file,
  and `go doc` targets come from the brief instead of a search.
- **Reasoning variant as a config choice**: the default variant capped out
  on 3 of 3 large increments (17-30k reasoning tokens in one step, nothing
  written); `low` finished one in 493 s with at most ~1k reasoning tokens
  per step; `max` never capped (15-29 min, $0.03-0.08 per unit). The variant
  is a per-worker knob, chosen per increment size.
- **Behaviour gates in the brief** (commands that exercise the built
  product, not just compile it): they caught regressions the workers' unit
  tests did not — a claimed fix that did not fix, and flags silently dropped
  after the task id.

The pattern: make the brief machine-checkable, bound the writes, and start
fresh when context grows; every technique here was adopted because a gauge
or a timing measurement proved it, not because it felt right.

## Building flywheel with flywheel

Flywheel staffs its own lines from these files. Two lines today:

- `flywheel-cli` — product: this repo (cmd/, internal/); process: plan, build,
  gauge, inspect, land; builders on `default` (opencode, variant max,
  max_parallel 4); the lead holds the one frontier seat.
- `flywheel-skills` — product: skills/ and docs/; process: plan, build, gauge,
  inspect, land; builders on the same worker pool.

The unit loop, with the real commands:

1. `flywheel log --task <id> --kind planned --brief <path>` — record the work order.
2. `flywheel run <id>` — dispatch an OpenCode worker; the CLI records dispatched,
   worker_plan, finished and report.
3. `flywheel validate <id>` — machine gauges: gates on the exact tree, owns check.
4. `flywheel inspect <id> --verdict ... --session <own>` — QC verdict, refused
   unless the readings cover the tree (T3/T4).
5. `git commit` — the lead commits the inspected tree.
6. `flywheel verify` — the event chain against the rules it implements today
   (T1, T3, T4, T5 and T8; the rest arrive with their commands).
7. `flywheel factory` — the floor: state and ETA from committed files.

Evidence from the 2026-09-13 run (described generically): 7 units were taken
through the full loop by the CLI. On the `max` variant each unit took 15-29
minutes and $0.03-0.08 of worker cost. Documentation units cost $0.005-0.011
each; this design doc was drafted, corrected twice and extended for about
$0.03 of worker cost. What the run surfaced — caught and
diagnosed by the lead's checks and the gauges, not by the workers' unit tests:

- a worker report claimed a regression was fixed when it was not: the lead's diff
  review caught it before inspection;
- flags placed after the task id were silently dropped: the lead's built-binary
  guard caught it before anything was committed;
- every flag of four commands was frozen at its default: the lead's behaviour
  gates failed in `flywheel validate`, so inspection could not pass (rule T3), and
  the next worker diagnosed the cause;
- a test binary was persistently refused by the host's application control: a host
  policy; the test gate was amended to compile the tests and run them;
- a resume picked the inspector's session instead of the worker's (#97): OpenCode
  failed with "Session not found";
- verify's brief-hash rule compared a correction's hash with the planned brief
  instead of the correction file itself (#98), so every resumed task failed
  verify;
- the first-pass metric ignored inspection verdicts (#100), so the factory showed
  "first-pass n/a" for inspected units.

The loop's point: worker unit tests are the workers' own evidence and were not
relied on; the gauges re-measured every claim.

## Plan

Issue-sized units, in dependency order. "Cheap" = a cheap OpenCode worker can
build it from a brief; "lead" = needs the lead's judgment to specify or judge.

| Unit | Advances | Owns (files) | Gates | Who |
| --- | --- | --- | --- | --- |
| P1 parse line files | #69 | internal/flywheel/lines.go | parse a fixture, report errors | cheap |
| P2 parse agent files | #69 | internal/flywheel/agents.go | parse a fixture, unknown-key error | cheap |
| P3 config sync | #69 | internal/flywheel/config.go, cmd/flywheel/config_cmd.go | sync writes config.json; validate round-trips | cheap |
| P4 staffing in the floor | #69 | internal/flywheel/factory.go | headcount by role from files | cheap |
| P5 flywheel.md generation | #82 | internal/flywheel/report.go, internal/flywheel/state.go | report rewritten on every event; committed | cheap |
| P6 ETA in state and report | #41 | internal/flywheel/report.go, factory.go | median wall time, sample size, "no history" | lead |
| P7 tool surface + adapters | #49 | internal/flywheel/adapters.go, skills/ | every declared tool honored, none dropped | lead |
| P8 cost per unit + frontier baseline | #59 | internal/flywheel/stats.go, cmd/flywheel/stats_cmd.go | cost per landed unit vs baseline | lead |
| P9 context pack from files | #58 | cmd/flywheel/context_cmd.go | factory state sized for a joining agent | cheap |
| P10 capture every session | #62 | internal/flywheel/trace.go | every persona session traced | cheap |
| P11 worktree per task | #45 | internal/flywheel/worktree.go, cmd/flywheel/land_cmd.go | one worktree per task, landing queue | lead |
| P12 supervise | #55 | cmd/flywheel/supervise_cmd.go | gauges measure finished units | cheap |
| P13 audit | #61 | cmd/flywheel/audit_cmd.go | first articles, samples, nonconformances | lead |
| P14 vendor-neutral lead skills | #43 | skills/flywheel/ | lead reads the same files in any vendor | lead |
| P15 seat leases and per-role handoff pack | #47, #58 | internal/flywheel/lease.go, cmd/flywheel/staff_cmd.go, cmd/flywheel/context_cmd.go | a second holder is refused while the lease is live; a released seat can be taken; the pack lists the role's open work | lead |
| P16 telemetry and control panes | #41, #62 | internal/flywheel/factory.go, render.go | each pane renders from files; ETA accuracy recorded | cheap |
| P17 benchmark harness | #59, #48 | bench/ (new) | both arms on a pinned task subset; Pareto report | lead |

P1-P5 and P9, P10, P12 are mechanical: parse, render, record. P6-P8 need the
lead to fix the formulas (ETA window, baseline, cost attribution). P7, P11, P13,
P14 change safety properties (tool permissions, landing, independence, policy
text) and get the lead's inspection even when a worker builds them.