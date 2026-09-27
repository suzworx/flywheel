# Flywheel runtime: gap analysis (Phase 0, part 2)

Every requirement of the owner's spec (sections 1-27) classified against the code at commit
2e6f6fd (2026-09-13). Classes: ALREADY EXISTS (implemented), PARTIALLY EXISTS (partly
implemented, what is missing in Why), MISSING (absent), NOT NEEDED (out of scope for this
local-first CLI), SHOULD NOT BE IMPLEMENTED (actively wrong). Analysis only; no code changed.

## Sections 1-27

| § | Requirement | Class | Evidence | Why |
| --- | --- | --- | --- | --- |
| 1 | Factory as control plane owning lifecycle | MISSING | none | CLI dispatches and watches single runs; no controller owns worker lifecycle. |
| 2 | Deterministic core: persistence, derivation, gates | PARTIALLY EXISTS | internal/flywheel/state.go:Derive | Log, derivation and gates are deterministic; leases, heartbeats, scheduling, retry, health are missing. |
| 3 | First-class Goal concept with acceptance criteria | MISSING | none | Tasks are the top unit; no goal entity, status or acceptance criteria. |
| 4 | Distinct task, attempt, worker, evidence concepts | PARTIALLY EXISTS | internal/flywheel/run.go:attemptNum | Tasks and rN/cN attempts are distinct; no goal, worker identity or evidence objects. |
| 5 | Factory-to-goal-to-task-to-attempt hierarchy | MISSING | none | Flat task list only; no goals, subgoals or hierarchy. |
| 6 | Desired versus observed state distinction | PARTIALLY EXISTS | internal/flywheel/factory.go:classifyRun | Observed run states derived from file signals; no desired-state model. |
| 7 | Deterministic controller with reconcile loop | MISSING | none | No loop, no reconcile, no policy; dispatch is one-shot CLI. |
| 8 | Event-driven and periodic reconciliation | MISSING | none | Events are logged but nothing reacts; no periodic liveness loop. |
| 9 | Pure reconcile(desired, observed, policy, now) | MISSING | none | No reconcile function exists to test without an LLM. |
| 10 | Formalized, validated task state machine | PARTIALLY EXISTS | internal/flywheel/state.go:Derive | Statuses derive deterministically from the latest event; no legal-transition table, illegal transitions not rejected. |
| 11 | Expiring worker leases | MISSING | none | Workers assumed alive until the run exits; no lease concept. |
| 12 | Periodic worker heartbeats | MISSING | none | Only a start watchdog and file-activity heuristics; no heartbeat events. |
| 13 | Unique attempt identity per execution | PARTIALLY EXISTS | internal/flywheel/run.go:attemptNum | Attempts numbered rN/cN from dispatched events; no worker_id, lease_id or generation. |
| 14 | Stale-result rejection by attempt token | MISSING | none | Late event from an older attempt overwrites newer status: Derive never checks attempt against latest dispatch. |
| 15 | Deterministic scheduler of ready tasks | MISSING | none | No scheduler; needs and max_parallel declared but never enforced. |
| 16 | Config-driven retry policy with budget | MISSING | none | Retries are manual lead decisions; config budget declared but unused. |
| 17 | Durable retry eligibility and timing | MISSING | none | No retry_eligible_at; nothing moves tasks back to ready automatically. |
| 18 | Idempotent repeated reconciliation | PARTIALLY EXISTS | internal/flywheel/state.go:Derive | Derivation and atomic writes are idempotent; nothing stops double dispatch or duplicate workers. |
| 19 | Append-only log with deterministic reducer | ALREADY EXISTS | internal/flywheel/events.go:AppendEvent, internal/flywheel/state.go:Derive | Log is append-only with torn-write repair; Derive rebuilds state deterministically. |
| 20 | Crash recovery from durable log | PARTIALLY EXISTS | internal/flywheel/state.go:Derive | State re-derives after restart; no controller to recover, orphaned children not reclaimed. |
| 21 | Factory inspectable during agent blackout | PARTIALLY EXISTS | internal/flywheel/factory.go:buildAndon | Floor and andon derive from files with no LLM; no explicit blackout state. |
| 22 | Automatic redispatch when capacity returns | MISSING | none | Resume is a manual --resume decision; nothing watches capacity. |
| 23 | Activity tracked separately from progress | PARTIALLY EXISTS | internal/flywheel/factory.go:classifyRun | Ages distinguish silent/long-step/stalled; no last_event_at versus last_progress_at. |
| 24 | Goal progress derived from task states | MISSING | none | No goal entity; per-status counts exist but no goal progress accounting. |
| 25 | Persisted acceptance criteria, evaluated deterministically | MISSING | none | Gates validate per task, not goal criteria; no criteria storage. |
| 26 | Stuck-factory detection from deterministic state | PARTIALLY EXISTS | internal/flywheel/factory.go:buildAndon | Silent/stalled/capped raise an andon; no retry-loop, no-progress or budget-exhausted conditions; display only. |
| 27 | Deterministic factory health model | MISSING | none | Andon flags individual units; no HEALTHY/DEGRADED/STALLED health state. |

## Sections 28-53

| § | Requirement | Class | Evidence | Why |
| --- | --- | --- | --- | --- |
| 28 | Deterministic `factory status` (health, goals, workers, attempts, retries, next actions), no LLM | PARTIALLY EXISTS | internal/flywheel/factory.go:buildAndon | Floor, staffing and andon render from files without a model, but no aggregate status exists: no health, goal, worker-health, retry or next-action sections. |
| 29 | Deterministic task explainability (`task status` answering why with evidence) | MISSING | none | No task command; state has no reasons attached and no leases exist to cite as evidence. |
| 30 | Next-action explainability (what happens next, with reasons) | MISSING | none | No scheduled or eligible-action model; nothing renders upcoming dispatch, wait or block decisions. |
| 31 | Validation and evidence: a worker claim never suffices; bind to immutable evidence | ALREADY EXISTS | internal/flywheel/gauges.go:ValidateTask | A finished event is only a claim; acceptance needs validated gate readings on the tree hash plus an inspected pass (T3), so claims alone never accept. |
| 32 | Validation must match exact work: evidence for tree X cannot accept tree Y | ALREADY EXISTS | internal/flywheel/inspect.go:hasPassingValidated | Validated events carry the tree hash; inspect accepts pass verdicts only for readings on the current tree, refusing when the tree changed. |
| 33 | Deterministic dependency resolution | MISSING | none | needs is recorded in planned/amended events and brief headers but never resolved; no blocked-by logic or ready gating. |
| 34 | Scheduler loop deciding dispatch from state | MISSING | none | No scheduler: needs and max_parallel are declared but never enforced; dispatch is a manual CLI decision. |
| 35 | Time as an explicit input for testable decisions | PARTIALLY EXISTS | internal/flywheel/factory.go:Refresh | Floor rendering and its tests take an injected now; run, dispatch and watchdog paths read the real clock and use real sleeps, so lease/backoff-style tests are impossible. |
| 36 | Concurrency: state must not corrupt under multiple workers/controllers | PARTIALLY EXISTS | internal/flywheel/events.go:AppendEvent | Append-only log with torn-write repair and atomic state writes prevent corruption; no leases, ownership tokens or CAS, so two dispatchers can double-dispatch. |
| 37 | Single controller with explicit ownership | MISSING | none | No controller exists; dispatch is uncoordinated CLI runs with no controller id, generation or lease to prevent duplicate work. |
| 38 | Explicit factory liveness invariants, tested | PARTIALLY EXISTS | internal/flywheel/verify.go:ruleT3 | Some listed invariants hold and are tested (evidence matches the tree, state reconstructable from the log); lease, retry-budget, dependency and stale-attempt invariants are absent. |
| 39 | Deterministic factory observability metrics | PARTIALLY EXISTS | internal/flywheel/factory.go:buildUnits | Per-status task counts, staffing and output derive deterministically; no reconcile counters, healthy/lost worker counts, attempt/retry metrics or metrics endpoint. |
| 40 | `factory events` deterministic timeline | MISSING | none | The event log holds the history, but no timeline command exists and the event kinds lack lease, heartbeat and retry events to show. |
| 41 | Transcripts are diagnostic data, not factory state | ALREADY EXISTS | internal/flywheel/state.go:Derive | State derives only from structured events; transcripts are parsed into observations (steps, tokens) but never into status, and worker claims in prose carry no authority. |
| 42 | Chaos: replay test, same event stream yields equivalent final state | ALREADY EXISTS | internal/flywheel/state.go:Derive | Derivation is order-independent and deterministically sorted; covered by the state derivation tests. |
| 42 | Chaos: reconciliation test, same inputs yield equivalent actions | MISSING | none | No reconcile function exists, so there are no actions to reproduce. |
| 42 | Chaos: idempotency test, repeated reconcile must not duplicate attempts | MISSING | none | Nothing deduplicates dispatch; repeated runs would keep dispatching new attempts. |
| 42 | Chaos: worker death test, lease expires, worker lost, retry, redispatch | MISSING | none | No leases or heartbeats; a killed worker leaves the task dispatched/running forever. |
| 42 | Chaos: controller death test, restart reconstructs and reconciles | PARTIALLY EXISTS | internal/flywheel/state.go:Derive | State reconstructs from the log on restart, but no controller exists to reconcile or detect expired workers. |
| 42 | Chaos: total blackout test, factory inspectable and no false success | PARTIALLY EXISTS | internal/flywheel/factory.go:buildAndon | The floor stays inspectable with no LLM and no work is falsely marked successful; ready work does not resume automatically when capacity returns. |
| 42 | Chaos: late completion test, stale attempt result rejected | MISSING | none | Derive never checks an attempt against the latest dispatch, so a late result from an older attempt overwrites the newer one. |
| 42 | Chaos: retry exhaustion test, repeated failures reach terminal state | MISSING | none | No retry budget or count exists; a task can be retried forever. |
| 42 | Chaos: validation mismatch test, tree X evidence cannot accept tree Y | ALREADY EXISTS | internal/flywheel/inspect.go:hasPassingValidated | Covered: inspect refuses a pass when the tree changed after validation. |
| 42 | Chaos: dependency test, C cannot become READY before A and B satisfy policy | MISSING | none | needs is never resolved; dependency policy is unenforced. |
| 42 | Chaos: stalled-but-alive test, heartbeating worker with no progress | PARTIALLY EXISTS | internal/flywheel/factory.go:classifyRun | Stalled is detected from file-age heuristics and reaches the andon; no heartbeat-versus-progress distinction exists. |
| 43 | Deterministic test clock for time-based decisions | PARTIALLY EXISTS | internal/flywheel/factory_test.go:fixtureNow | Factory tests inject a fixture clock and --now makes renders reproducible; run and watchdog tests depend on real sleeps and wall clocks. |
| 44 | Keep the controller boring: deterministic infrastructure, no reasoning | ALREADY EXISTS | internal/flywheel/factory.go:classifyRun | Every existing control-plane decision (run states, andon, statuses, dispatch) is deterministic code; no agent-like controller exists to drift. |
| 45 | Agents can propose new work via structured, validated events | MISSING | none | No proposal event or command exists; only the lead records planned events through the CLI and nothing validates worker-proposed work. |
| 46 | Command/event boundary: requested action distinct from persisted fact | PARTIALLY EXISTS | internal/flywheel/events.go:AppendEvent | CLI commands append only facts to the log and dispatch intent is recorded before the worker spawns; there is no durable command/intent record with acceptance. |
| 47 | Crash-safe action execution: durable intent + idempotent execution + reconcile | PARTIALLY EXISTS | internal/flywheel/run.go:Run | Dispatched is appended before spawn and every in-process failure records finished, but a hard crash leaves an orphaned dispatched event; no reconcile loop recovers either crash window. |
| 48 | Factory should converge: reconcile moves observed toward desired | MISSING | none | No desired-state model or reconcile loop; nothing continuously restores order around execution uncertainty. |
| 49 | Do not overengineer: no Kubernetes/Kafka/Temporal/Redis/PostgreSQL/queues/consensus/leader election | NOT NEEDED | none | Standard-library-only, local-first CLI; none of the listed infrastructure exists, is used or is justified, and the event log plus reducer already covers persistence. |
| 50 | Implementation plan (sections A-J) required before coding | MISSING | none | No plan following the A-J structure exists; docs/design holds architecture and this gap analysis, but no data model, state machines, reconcile pseudocode, failure matrix, CLI changes, test plan or compatibility section. |
| 51 | Implement in incremental phases, usable and green after each | MISSING | none | No phased plan or per-phase milestones exist; the runtime work is not sequenced. |
| 52 | DoD: factory status without an LLM during blackout (health, why stopped, blocked work, next action) | PARTIALLY EXISTS | internal/flywheel/factory.go:buildAndon | Floor and andon render with no model, but no health, reason-execution-stopped, blocked-work or next-action sections exist. |
| 52 | DoD: automatic resume without human intervention when capacity returns | MISSING | none | Restoring capacity never triggers dispatch; resume stays a manual --resume decision. |
| 53 | Final principle: deterministic control plane owns truth, memory, lifecycle, scheduling, ownership, validation, recovery, liveness | PARTIALLY EXISTS | internal/flywheel/state.go:Derive | Flywheel provides truth and memory (event log + derivation) and validation (gates); scheduling, ownership, recovery and liveness are absent. |

## Totals

Rows per class, both parts (sections 1-53):

| Class | Rows |
| --- | --- |
| ALREADY EXISTS | 7 |
| PARTIALLY EXISTS | 23 |
| MISSING | 33 |
| NOT NEEDED | 1 |
| SHOULD NOT BE IMPLEMENTED | 0 |

Five biggest gaps:

- No deterministic controller or reconcile loop (§7-9, 34, 37, 48): nothing continuously restores order, and two dispatches can double-dispatch.
- No worker leases or heartbeats (§11-12, 42): a killed worker is never marked lost, retried or resumed.
- No goal entity, desired-state model or goal progress (§3, 5, 24): the factory cannot express what should exist.
- Stale attempt results are accepted (§14, 42): Derive overwrites a newer attempt's state with a late report.
- No retry policy, budget or eligibility (§16-17, 42): failed work never resumes without a manual lead decision.