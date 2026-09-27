# Flywheel runtime: design (Phase 0, part 3)

Part three of the Phase 0 runtime design: A restates how the runtime works today (per
docs/design/runtime/01-architecture.md), C proposes the target architecture, D lists the exact
data model changes, following the owner's spec sections 3-19 (goals, attempt identity, leases,
retry, event-sourced recovery), 36-37 (concurrency, single controller) and 49-50 (no
overengineering; implementation plan). The lead's decisions are binding: the event log stays the
only source of truth and everything extends it additively — no database, no queue, no daemon
framework, standard library only; every cited code name was confirmed against the tree.

## A. Existing architecture

- **Persistence**: `.flywheel/events.jsonl`, an append-only JSONL source of truth; `AppendEvent` validates, stamps ts and appends with torn-byte repair, `ParseEvents` reads it back naming bad lines, `Validate` enforces task/attempt patterns, the kind set and verdict rules (internal/flywheel/events.go:AppendEvent, internal/flywheel/events.go:ParseEvents, internal/flywheel/events.go:Validate).
- **State**: `Derive` replays the log (sorted, canonical tiebreak) into state.json and the flywheel.md status block, written atomically (internal/flywheel/state.go:Derive, internal/flywheel/state.go:atomicWrite); it never checks attempt ids, so a late event from an older attempt wins.
- **Work orders**: brief headers (owns/needs/gate) parsed by `ParseBriefHeader`; gates are shell lines run by `runGate` under `ValidateTask` (internal/flywheel/brief.go:ParseBriefHeader, internal/flywheel/gauges.go:runGate, internal/flywheel/gauges.go:ValidateTask).
- **Config and init**: `LoadConfig` reads .flywheel/config.json with validation; `InitSeeded` scaffolds events.jsonl, state.json, config.json and flywheel.md (internal/flywheel/config.go:LoadConfig, internal/flywheel/init.go:InitSeeded).
- **Dispatch**: `Run` requires a planned event, appends dispatched, spawns opencode under an embedded deny policy, streams `.flywheel/runs/<task>.<attempt>.jsonl` and records started/worker_plan/report/finished; `attemptNum` numbers rN/cN attempts, `ExitCode` maps outcomes (internal/flywheel/run.go:Run, internal/flywheel/run.go:attemptNum, internal/flywheel/run.go:ExitCode).
- **Validation**: `ValidateTask` runs each gate, hashes the tree with a throwaway index (`treeHash`) and checks owns with evidence logs (internal/flywheel/gauges.go:treeHash, internal/flywheel/gauges.go:changedPaths).
- **Recovery**: replay-only — `Derive` rebuilds state after restarts; a torn final log byte is repaired on the next append; no leases, no liveness watch, no orphan reclamation.
- **Retry**: none in code — redispatch and resume are manual lead decisions; no retry policy, no eligible_at, no scheduler (max_parallel is declared, never enforced).
- **Status**: `classifyRun` infers silent/long-step/stalled from run-file age and growth and `buildAndon` lists them display-only; `InspectTask` gates verdicts and `VerifyTasks` replays rules T1/T3/T4/T5/T8 (internal/flywheel/factory.go:classifyRun, internal/flywheel/factory.go:buildAndon, internal/flywheel/inspect.go:InspectTask, internal/flywheel/verify.go:VerifyTasks).

## C. Proposed architecture

```text
CLI  flywheel next | controller | run | validate | inspect | factory (cmd/flywheel/*_cmd.go)
 │
 ▼
Factory runtime (internal/flywheel/)
 ├── Event log .............. .flywheel/events.jsonl ............ events.go (existing)
 ├── Goal store ............. goal events in the log ............ events.go (goals are events)
 ├── State reducer .......... state.json + flywheel.md ........... state.go (existing)
 ├── Reconcile .............. reconcile.go ...................... NEW: pure decision
 │    ├── Lease manager ...... .flywheel/leases/<task>.<attempt>.json ... leases.go (NEW)
 │    ├── Retry policy ....... config `retry` block .............. retry.go (NEW)
 │    ├── Dependency resolver  planned events + brief needs: .... resolver.go (NEW)
 │    └── Scheduler .......... worker max_parallel ............... controller.go (NEW)
 ├── Worker runtime .......... internal/flywheel/run.go:Run ...... existing
 ├── Validator ............... internal/flywheel/gauges.go:ValidateTask ... existing
 └── Status .................. internal/flywheel/factory.go:Refresh ..... existing (+ status.go NEW)
```

- Event log: the only source of truth; every change is a new event kind or field, appended additively, so old factories replay unchanged (events.go).
- Goal store: no new store — goals are `goal` events carrying id, title, acceptance and status; a `planned` event may carry a goal (events.go).
- State reducer: `Derive` re-derives State and is changed to compare each result-bearing event's attempt with the latest dispatched one, moving non-current results to the task's stale list (state.go:Derive).
- Reconcile: pure `Reconcile(State, Observed, Policy, now) -> []Action`, no LLM, no I/O; expires a dispatch_intent with no dispatched after a timeout and marks a dispatched attempt with no live lease lost (reconcile.go, new).
- Lease manager: writes and atomically renews `.flywheel/leases/<task>.<attempt>.json` every renew interval while the worker lives and removes it after appending finished; renewal is the heartbeat, i.e. liveness (leases.go, new).
- Retry policy: classifies finish reasons (lost/process crash -> retry, provider unavailable -> wait, dependency blocked -> no budget, budget exhausted -> terminal) and emits `retry_scheduled` with eligible_at from the config retry block (retry.go, new).
- Dependency resolver: a task is ready only when every brief `needs:` task has landed; a plan is the task graph of planned events plus needs (resolver.go, new).
- Scheduler: picks ready tasks within the worker's max_parallel, one live attempt per task, at most one dispatch per task (controller.go, new).
- Worker runtime: `Run` spawns opencode, streams the run file, echoes the intent id on dispatched and removes the lease after finished (run.go:Run).
- Validator: `ValidateTask` runs gates and owns on the exact tree; a validated pass is the first meaningful-progress signal (gauges.go:ValidateTask).
- Status: health is derived, never stored — HEALTHY, DEGRADED, WAITING, STALLED, BLOCKED, FAILED, COMPLETED, each with the rule and evidence that produced it (status.go, new).
- Liveness vs progress: lease renewal is liveness; new completed steps in the run file are activity; meaningful progress is a validated pass, an inspected pass, landed or a met goal criterion. Every time decision takes now explicitly.

## D. Data model changes

| Kind | Name | Where | Change |
| --- | --- | --- | --- |
| event | goal | .flywheel/events.jsonl | new kind: create/update a Goal carrying id, title, acceptance (gate commands plus required task ids) and status |
| event | dispatch_intent | .flywheel/events.jsonl | new kind: controller appends the intent id before spawning; the run echoes it on dispatched |
| event | retry_scheduled | .flywheel/events.jsonl | new kind: every retry decision carries eligible_at, so a restart never resets retry history |
| field | goal | Event | optional Goal object on goal events and on planned events |
| field | intent | Event | dispatched events carry the intent id they echo (crash-safe dispatch) |
| field | eligible_at | Event | retry_scheduled timestamp; the task is ready again once now >= eligible_at |
| field | stale | TaskState | list of stale attempts whose late results Derive ignores (attempt identity) |
| type | Goal | goal.go (new) | id, title, acceptance, status; owned by the factory, not a session |
| type | Lease | leases.go (new) | pid, host, started_at, renewed_at, expires_at, run file |
| type | Observed | reconcile.go (new) | leases + run-file ages + controller lock |
| type | Policy | reconcile.go (new) | retry budget, backoff, timeouts, taken from config |
| type | Action | reconcile.go (new) | MarkLost, ScheduleRetry, MakeReady, Dispatch, Block, Fail, MarkGoalMet, SetHealth, RequestInspection, each with a reason string |
| type | Health | status.go (new) | HEALTHY, DEGRADED, WAITING, STALLED, BLOCKED, FAILED, COMPLETED, with the rule and evidence |
| file | .flywheel/leases/<task>.<attempt>.json | lease manager | written atomically each renew interval; removed after appending finished |
| file | .flywheel/controller.lock | controller | pid, generation, expires_at; renewed each tick; an expired lock is taken over with generation+1 |
| config | retry | .flywheel/config.json | new block: max_attempts, initial_delay, backoff, max_delay |
| func | Reconcile | reconcile.go (new) | pure (State, Observed, Policy, now) -> []Action; every time decision takes now |
| func | NextActions | next_cmd.go (new) | prints Reconcile's actions without executing them; read-only, built first |
| func | Derive | state.go | change: ignore finished/report/validated/owns_checked whose attempt is not the latest dispatched; count them as stale |
| event | lost | .flywheel/events.jsonl | an attempt whose lease expired (attempt, reason lease-expired, evidence: lease expires_at and the tick time) |
| event | intent_expired | .flywheel/events.jsonl | a dispatch intent with no dispatched echo within the intent timeout |
| event | failed | .flywheel/events.jsonl | terminal task failure: retry budget exhausted, a terminal finish reason, or a failed needs target, with the reason |
| config | lease | .flywheel/config.json | renew_interval, ttl |
| config | controller | .flywheel/config.json | interval, intent_timeout |
| field | last_event_at | State | activity: the latest event's ts, any kind |
| field | last_progress_at | State | meaningful progress: passed, inspected pass, landed, goal met |

Dependency rules everywhere: a needs: target is satisfied when its status is passed or landed (the lead
may commit after an inspected pass without logging landed); a needs target that failed terminally blocks
its dependents and then fails them.

## E. State machines

Transitions are explicit, persisted, replayable, idempotent and validated (spec §10). Task statuses
reuse today's (internal/flywheel/state.go:Derive, kindRank) — planned, dispatched, running, finished,
passed, needs-correction, rejected, blocked, landed — adding only retry-wait, lost and failed. The
machines are enforced where events are appended: the command derives the source task's status from the
log and refuses an illegal transition as an invariant violation; Derive records it and the command
exits 6. Derive itself stays a pure replay; a pre-existing illegal sequence is surfaced by verify.
### Task
| Machine | from | to | trigger event or action | guard |
| --- | --- | --- | --- | --- |
| Task | planned | dispatched | dispatched event echoes the intent id | needs all passed or landed; no live lease; no unexpired intent; capacity |
| Task | planned | blocked | blocked event | a needs target is rejected or failed |
| Task | dispatched | running | started event | lease live |
| Task | dispatched | lost | lost event | lease expired before started |
| Task | running | finished | finished event | run file complete, rc recorded |
| Task | running | lost | lost event | lease expired before finished |
| Task | finished | passed | inspected pass | inspected after the latest finished on the validated tree |
| Task | finished | needs-correction | inspected rework or a failed gauge | gate or owns failure after the latest finished |
| Task | finished | rejected | inspected scrap | lead reject with reason |
| Task | finished | retry-wait | retry_scheduled event | reason retryable and attempts < max_attempts |
| Task | finished | failed | failed event | reason terminal or attempts >= max_attempts |
| Task | lost | retry-wait | retry_scheduled event | attempts < max_attempts |
| Task | lost | failed | failed event | attempts >= max_attempts |
| Task | retry-wait | dispatched | dispatched event | now >= eligible_at; needs met; capacity |
| Task | passed | landed | landed event | owns_checked on the exact validated tree |
| Task | needs-correction | dispatched | dispatched event (correction cN) | rework allowed by the retry policy |
| Task | blocked | planned | resolver re-ready | needs target now passed or landed |
### Attempt
| Machine | from | to | trigger event or action | guard |
| --- | --- | --- | --- | --- |
| Attempt | dispatched | running | started event | lease live |
| Attempt | dispatched | lost | lost event | lease expired before started |
| Attempt | running | finished | finished event | run file complete |
| Attempt | running | lost | lost event | lease expired while running |
| Attempt | finished | stale | Derive | a newer dispatched exists; the late result is ignored (spec §14) |
### Worker lease (live, expired, released)
| Machine | from | to | trigger event or action | guard |
| --- | --- | --- | --- | --- |
| Lease | live | expired | reconciler tick | now > expires_at |
| Lease | live | released | lease file removed | after finished appended |
| Lease | expired | released | lease file removed | after lost recorded |
### Goal
| Machine | from | to | trigger event or action | guard |
| --- | --- | --- | --- | --- |
| Goal | active | met | goal event met | acceptance criteria satisfied with evidence |
| Goal | active | failed | goal event failed | a required task failed terminally |
| Goal | active | abandoned | goal event abandoned | lead decision |
### Factory health (seven states)
| Machine | from | to | trigger event or action | guard |
| --- | --- | --- | --- | --- |
| Health | any | HEALTHY | recompute | work exists; capacity healthy; progress recent |
| Health | any | DEGRADED | recompute | some attempts lost; progress still possible |
| Health | any | WAITING | recompute | ready work, no eligible capacity (provider outage) |
| Health | any | STALLED | recompute | no meaningful progress past the threshold |
| Health | any | BLOCKED | recompute | remaining work blocked on unresolved needs |
| Health | any | FAILED | recompute | terminal invariant failure or retry budget exhausted |
| Health | any | COMPLETED | recompute | all goals met |

Illegal transitions: terminal states (landed, rejected, failed) accept no outgoing transition; backwards
moves (running -> planned) and skipped steps (planned -> running without dispatched) are illegal. Health
is derived every tick, never appended; the other machines are enforced at append time as above.

## F. Reconciliation

func Reconcile(s State, obs Observed, p Policy, now time.Time) []Action

One tick, in this order, each step reading only State + Observed and emitting Actions:

1.  Expire controller-owned intents: a dispatch_intent with no dispatched echo within intent_timeout
    becomes intent_expired, freeing the task for a fresh intent (spec §47).
2.  Expire leases: an attempt whose lease has now > expires_at is marked lost; liveness is lease
    renewal, never process existence (spec §11).
3.  Classify finished attempts: map the finished reasons (stop, error, silent, capped, provider-error,
    start-failed) onto retry classes — retry, wait, rework, terminal (spec §16).
4.  Process validation outcomes: a validated pass on the latest finished makes the task await
    inspection; Reconcile emits RequestInspection, which dispatches the inspector persona when the
    policy `auto_inspect` is on and otherwise waits for the lead; a failed gauge makes the task
    needs-correction. Reconcile never writes an inspected event; acceptance is only an inspected
    pass (T3, T4).
5.  Resolve needs: a task is ready only when every needs target is passed or landed; a task whose
    target failed terminally is blocked, then failed.
6.  Evaluate retry eligibility: retryable finishes below max_attempts emit retry_scheduled with
    eligible_at; attempts >= max_attempts fail the task (budget exhausted is terminal).
7.  Select ready tasks: ready = planned or retry-wait status, needs met, no live lease, no unexpired
    intent; blocked tasks keep their reason visible.
8.  Compute capacity: p.max_parallel minus live leases.
9.  Dispatch up to capacity in stable order — planned time, then task id; each Dispatch action carries
    a fresh intent id appended before spawn (at-most-once).
10. Recompute goal status: met when the acceptance criteria hold with evidence; failed when a required
    task failed terminally.
11. Recompute health: derive one of the seven states with its rule and evidence; never stored.

Why this order: intents and leases must expire before dispatch eligibility and capacity are computed;
classification must precede retry (the policy needs the finish reason); validation must precede needs
resolution (a target's acceptance decides readiness); retry eligibility must precede selection; capacity
is computed after selection so dispatch is capped; goals and health come last because they summarize
the tick.

Idempotency: Reconcile is pure — the same (State, Observed, Policy, now) produce the same []Action.
An action is emitted only when State does not already reflect it: MarkLost only for an attempt with no
lost event, ScheduleRetry only when no retry_scheduled covers the latest finished, Dispatch only when
no unexpired intent exists for the task. Executing an action changes State, so the next tick with the
same observations emits nothing new (spec §18: at-least-once reconciliation with idempotent effects).
Dispatch is at-most-once per intent id: the controller appends the intent before spawn, the run echoes
it on dispatched, and a second spawn for the same id is refused.

Crash semantics (spec §47): case 1 — intent appended, worker spawned, controller crashes before the
dispatch is recorded: the dispatched echo is written by the run, not the controller, so a restart
replays intent + echo and continues; if the echo never lands, the intent expires (intent_expired), the
lease expires (lost), and a fresh intent dispatches a new attempt — at-most-once per intent prevents a
double spawn. Case 2 — intent appended, controller crashes, worker never starts: the unacknowledged
intent expires (intent_expired), nothing was spawned to kill, the task returns to ready, and the next
tick appends a new intent and spawns. Both cases recover by replay + observe + reconcile; the
controller keeps no RAM state.

## G. Failure matrix

| Failure | Detection | Deterministic response | Event(s) |
| --- | --- | --- | --- |
| worker crash | lease not renewed; now > expires_at | mark the attempt lost; retry if budget remains | lost, retry_scheduled |
| worker hang (alive, no progress) | lease live but last_progress_at past the stall threshold | no state change; health recomputes STALLED; surfaced on the andon (classifyRun) | none |
| flywheel run killed | lease expires; run file stops growing | attempt lost; run file kept as diagnostic; retry if budget remains | lost, retry_scheduled |
| controller crash | on restart the log is replayed and leases observed | continue reconciling; expired intents and leases reconciled away; no RAM dependency | intent_expired, lost |
| two controllers | controller.lock generation checked every tick | the stale generation refuses to act; takeover only after lock expiry (generation+1); two live locks are an invariant violation | none (refused, exit 6) |
| provider outage (every attempt provider-error or start-failed) | every finished reason is provider-error or start-failed | classify as wait, backoff retry; health WAITING; resumes when capacity returns | retry_scheduled, health |
| capped output | finished reason capped (run.go:ExitCode) | treated as a validation-classified outcome; rework via the retry policy | inspected rework or a failed gauge, retry_scheduled |
| validation failure | gate rc != 0 on the validated tree | task needs-correction; rework consumes retry budget | inspected, retry_scheduled |
| retry budget exhausted | attempts >= max_attempts on a retryable finish | terminal: task failed; dependent goals may fail; health FAILED; manual intervention | failed |
| duplicate dispatch | two unexpired intents for one task / second live attempt | scheduler keeps one live attempt per task; a second intent for the same task is refused (at-most-once per intent id) | none (refused, exit 6) |
| late result from an older attempt | result-bearing event's attempt != latest dispatched | Derive moves the result to the task's stale list; state unchanged; verify flags it | stale (field), verify flag |
| needs target failed | a needs target reaches rejected or failed | dependent task blocked with the target named; if the target is terminal the task fails | blocked, failed |
| validated tree changed before accept | tree hash at validated differs from the tree at inspected/landed | inspect refuses (evidence mismatch, T3); task needs-correction | inspected rework or a failed gauge, refusal |
| torn event-log write | ParseEvents names the bad line; AppendEvent repairs the torn byte | the torn line is not replayed; the next append repairs; verify flags an invariant violation if the torn line was a transition | none (repair) |
| machine restart | replay after boot; leases observed cold | Derive rebuilds; expired leases -> lost, expired intents -> intent_expired, retries rescheduled from eligible_at | lost, intent_expired, retry_scheduled |