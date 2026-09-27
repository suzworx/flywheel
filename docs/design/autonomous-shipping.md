# Autonomous shipping: the flywheel factory

Status: protocol v1, draft · 2026-09-12 · phase 1 workers are OpenCode only

Goal: ship features with flywheel and cheap OpenCode worker agents at a fraction of frontier cost,
with no human in the loop. That is only safe if every step is recorded, validation is done by the
machine, independent auditors check the checkers, and the rules are enforced where an agent
cannot skip them.

Flywheel plays the part of a factory's execution system: the **control plane** dispatches work
and enforces policy; the **data plane** keeps traceability, telemetry and accountability for
every agent session. Leads use it to communicate, understand the situation and trace any result
back to the sessions that produced it.

## 0. Design priorities

In order: **efficiency and consistency**, then **speed, reliability and recoverability**. Every
feature in this document is held to them:

- Efficiency: the lead spends tokens only where judgment is needed; gauges, verification and the
  dashboard cost no tokens; readers use file offsets instead of rereading whole logs.
- Consistency: the same input gives the same state, verdict and output, whichever machine or agent
  runs the command; the rules are code, not advice.
- Speed: units build in parallel in their own worktrees; landing does not wait on unrelated work.
- Reliability: writes are atomic, commands are idempotent, and a silent run is classified within
  a minute.
- Recoverability: all state is in the append-only log, so a crashed supervisor, watcher, dashboard
  or lead restarts from the log and loses nothing.

## 1. The factory model

| Factory role | Flywheel persona | Does | Never | Skill |
| --- | --- | --- | --- | --- |
| Plant manager | lead (frontier) | sets goals, capacity and policy; handles escalations; signs off on sensitive work | implements | `flywheel` |
| Production planner | planner | writes work orders (briefs): `owns:`/`needs:`/`exclusive:`, gates, acceptance criteria | dispatches or inspects | `flywheel-planner` |
| Line supervisor | foreman | runs a line: dispatches, watches, retries by policy, pulls the cord on trouble | plans or implements | `flywheel-foreman` |
| Line worker | worker (OpenCode) | builds one work order at its own station (worktree) and reports | plans, inspects, commits | `flywheel-worker` |
| Machine gauges | supervisor (the CLI, no model) | measures every unit: runs the gates in the unit's worktree, checks `owns:` | judges intent | built into `flywheel supervise` |
| QC inspector (internal) | inspector | inspects each unit against its work order, using the gauge readings; pass, rework, scrap or escalate | runs gauges or fixes units | `flywheel-inspector` |
| External auditor | auditor (independent agent + CI) | audits samples and the process: re-measures, re-inspects, checks the records and whether QC catches defects; files nonconformances | works on the line or talks to it before reporting | `flywheel-auditor` |
| Continuous improvement | steward | turns stopped lines and nonconformances into corrective actions (learnings) | changes units | `flywheel-steward` |
| Owner | operator (human or agent) | installs, assigns roles to agents, sets merge and publish policy | — | `flywheel-operator` |

One agent may hold several internal roles at small scale, but **independence rules are hard**:
the auditor is never the same session as the lead, planner or inspector, and should be a
different model or vendor; the inspector never inspects work from its own session; a worker never
records gauge readings, inspections or audits.

Factory mechanics, as enforced features:

- **Work order and traveler.** Each task's brief is its work order; its event chain is the
  traveler that goes with the unit from plan to landing.
- **Lot traceability.** Every gauge reading, inspection and landing is bound to the unit's git
  tree hash, so a reading can never be reused for a different unit.
- **Poka-yoke (mistake-proofing).** Commands refuse illegal steps instead of trusting agents to
  follow a checklist (§3).
- **Andon cord.** Any stopped-line condition (stall, output cap, read loop, provider error, failed
  gauge, rejected inspection) raises a signal. The line cannot checkpoint, land or hand off until
  each signal is triaged.
- **First article inspection.** The first unit of every wave, and of every new kind of task, gets
  full inspection plus an audit before the rest of the lot runs.
- **Sampling.** After first articles pass, the auditor samples units at a rate that rises with
  nonconformances and falls with a clean record.
- **Nonconformance and corrective action.** An audit finding is a nonconformance report; the
  steward turns it into a learning with a corrective action, and the line's pass rate is tracked.
- **Shift handoff.** `flywheel handoff` and `flywheel context` give the next lead the whole state.

## 2. Required entries

Every event records who wrote it: `by: {persona, agent, model, session}`.

| Stage | Event | Required fields | Written by |
| --- | --- | --- | --- |
| Plan | `planned` | brief path, brief SHA-256, `owns`, `needs`, gates, acceptance criteria | planner or lead (`flywheel plan add`) |
| Dispatch | `dispatched` | adapter, model, session, attempt, run file | CLI (`flywheel run`) |
| Work | `worker_plan` | the worker's plan message (required before step 20) | CLI, from the run |
| Work | `finished` | rc, finish reason, tokens, cost, run-file SHA-256 | CLI |
| Work | `report` | final report, "Findings outside `owns:`" | CLI, from the run |
| Gauges | `validated` | gate, command, **git tree hash**, exit code, duration, output SHA-256, evidence path, environment | supervisor only |
| Gauges | `owns_checked` | changed paths, paths outside `owns:`, grants applied | supervisor only |
| Inspection | `inspected` | verdict (`pass`, `rework`, `scrap`, `escalate`), tree hash, checklist items, note | inspector or lead |
| Audit | `audited` | scope (unit, sample, wave), tree hashes, re-measurements, re-inspection result, record check, verdict (`conforms`, `nonconformance`) | auditor only |
| Landing | `landed` | commit, tree hash, the gauge and inspection events it relies on | landing queue (`flywheel land`) |
| Andon | `signal`, `dismissed` | kind, task, evidence | CLI; steward or lead triage |
| Improvement | `learning` | severity, observed, evidence, ask, linked signals or nonconformances | steward |

## 3. Poka-yoke: transition rules

`flywheel verify` fails, and the enforcing commands refuse, when any of these is broken:

- **T1.** `dispatched` needs a `planned` event whose brief hash matches the brief at dispatch time;
  a changed brief needs an `amended` event.
- **T2.** Every attempt that ran past step 20 has a `worker_plan`, or a signal.
- **T3.** `inspected pass` needs, for the same tree hash, a passing `validated` event from the
  supervisor for every declared gate, recorded after the attempt finished, and an `owns_checked`
  with nothing outside `owns:` except explicit grants.
- **T4.** The inspector's session differs from the worker's and from the session that planned the
  task.
- **T5.** `landed` needs `inspected pass` for the landed tree; a rebase at landing is re-measured
  before it lands.
- **T6.** Sensitive domains (auth, row-level security, tokens, crypto, payments) need the lead's
  sign-off and an audit before landing.
- **T7.** A wave's first article needs `audited conforms` before the rest of the lot lands; an open
  nonconformance stops landing for that kind of task until the steward records a corrective action.
- **T8.** Personas write only their own event kinds (table §2).
- **T9.** Checkpoint, land and handoff refuse while signals are untriaged, unless an
  `allow_untriaged` event with a reason is recorded.
- **T10.** The log is append-only with a hash chain per shard; any break fails verification.

## 4. Enforcement layers

| Layer | Mechanism | Stops | Bypass |
| --- | --- | --- | --- |
| CLI | commands refuse illegal transitions | a persona recording a result it has not earned | editing the log by hand, which T10 detects |
| Supervisor | `flywheel supervise` measures every finished unit itself | "tests pass" claims; skipped measurement | not running it, which T3 then fails at inspection |
| Git hooks | `commit-msg` requires `Flywheel-Task:`; `pre-push` runs `flywheel verify` | untracked or unverified commits leaving the machine | `--no-verify`, which CI catches |
| Agent hooks | a Claude Code `Stop` hook and an OpenCode plugin run `flywheel gate` before a session ends | a lead or foreman ending with uninspected units, missing readings or untriaged signals | agents without hooks, which CI catches |
| External audit | the auditor persona plus the CI `flywheel-audit` check (required status) | internal QC passing defects; a broken chain reaching `main` | a repository admin override, visible on the PR |

## 5. Telemetry: every session is captured

- **Workers**: each OpenCode attempt is a run file plus `dispatched`, `worker_plan`, `finished` and
  `report` events carrying the session id.
- **Leads and other personas**: agent hooks record session start, the flywheel commands the
  session runs, and session end (`flywheel log --persona lead --session <id> ...`), so every
  inspection, sign-off and escalation names the session that made it.
- `flywheel trace <session>`: everything one session did, across tasks.
- `flywheel explain <task>`: the traveler, from work order to landing, with each step's session.
- `flywheel watch --human`: the live stream, one readable line per transition.
- `flywheel context`: the state of the factory in a pack sized for a joining agent's context.
- `flywheel factory`: a live terminal dashboard of the floor, built from the log alone: work
  orders, units in progress and their stage, who is building, managing, inspecting and auditing
  each one, open signals, and output and cost. No AI is asked anything to draw it.

## 6. Evidence

- Committed: the event log (with digests), work orders, worker plans and reports.
- Gauge output: `.flywheel/evidence/<task>/<attempt>/<gate>.log`, committed under a size limit,
  otherwise kept locally with the digest committed; CI re-measures regardless.
- Output is scrubbed of known secret patterns before storing; the scrub is recorded.
- A flaky gate's retry is a new `validated` event, never an overwrite.

## 7. Cost

The lead spends tokens on planning, escalations and sign-offs; workers do the reading and
writing; gauges cost no tokens; auditors spend on samples, not on every unit. `flywheel stats`
reports cost per landed unit by persona against a frontier-only baseline (the same tokens priced
at the lead's model), so the "fraction of frontier cost" claim is measured per wave.

## 8. A wave, end to end

1. The planner records work orders (`planned`).
2. The foreman runs the line: `flywheel run` dispatches OpenCode workers (`dispatched`), captures
   their plans (`worker_plan`), or pulls the cord (`signal`).
3. A unit finishes (`finished`, `report`); the supervisor measures it (`validated`,
   `owns_checked`). A failed gauge gets a templated rework delta up to a retry limit, then
   escalates.
4. The inspector inspects (`inspected`). First articles and sensitive units go to the auditor
   (`audited`) and, for sensitive domains, the lead.
5. The landing queue re-measures if rebased and lands (`landed`); the PR's `flywheel-audit` check
   verifies the chain; release-please prepares the release.
6. The steward closes the wave: every signal and nonconformance has a learning or a dismissal.

## 9. Open questions

- Sampling rates for the auditor, and how fast they adapt.
- Evidence size limits and retention.
- Secret-scrubbing patterns, and proving a scrub removed nothing that mattered.
- Whether the OpenCode plugin API exposes a reliable session-end event for the agent hook.
