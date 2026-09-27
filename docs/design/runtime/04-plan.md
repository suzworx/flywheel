# Flywheel runtime: plan (Phase 0, part 4)

This plan sequences the design (docs/design/runtime/03-design.md) into eight flywheel units, one
per phase, each leaving the tree building and tested (spec §51). It fixes the CLI surface (§28-30,
40), the test plan (§42-43) and compatibility before code starts; the controller stays a boring
reconcile-execute loop with no RAM state (spec §44, §47).

## H. CLI changes

Commands register from their own init() and print like today's factory view (cmd/flywheel/*_cmd.go).
Exit codes follow AGENTS.md: 0 ok, 1 error, 2 usage, 5 gauges failed, 6 rule refusal (a stale
controller generation refusing to act, an illegal transition, a refused double dispatch).

```
flywheel goal <add|list|show> [flags]

$ flywheel goal add "Add deterministic factory runtime" --accept "all tasks landed"
goal-1 added: Add deterministic factory runtime

$ flywheel goal list
goal-1   active   Add deterministic factory runtime

$ flywheel goal show goal-1
goal-1  active
acceptance: all tasks landed
required:   (none)

flywheel status [flags]

$ flywheel status
Factory: fw-runtime              Health: DEGRADED
Goals:   active 1   met 0   failed 0
Tasks:   total 14  accepted 8  running 2  ready 2  blocked 1  failed 1
Attempts: running 2  lost 1  stale 0
Leases:  live 2  expired 1
Retries: scheduled 1  exhausted 0
Last event: 4s ago              Last meaningful progress: 2m 13s ago
Controller: healthy             Last reconciliation: 1.2s ago
Next actions: 3 (see flywheel next)

flywheel next [flags]

$ flywheel next
1. Task #8    DISPATCH   ready + needs met + capacity available
2. Task #11   WAIT       retry backoff expires in 32s
3. Task #15   BLOCK      depends on Task #13, not accepted
4. Task #7    REQUEST_INSPECTION   validated pass on r2 awaits inspection

flywheel explain <task> [flags]

$ flywheel explain 42
Task #42: retry-wait
Current attempt: r3
Reason: worker lease expired.
Evidence: last heartbeat 10:14:03; lease expiry 10:14:33; detected 10:14:35
Retry: next attempt 4; eligible 10:15:05; budget 1 remaining
Next action: dispatch when now >= eligible_at (10:15:05)

flywheel events [flags]

$ flywheel events
10:01:03  planned          task-7
10:01:05  dispatched       task-7 r1
10:03:41  lost             task-7 r1 (lease-expired)
10:03:41  retry_scheduled  task-7 eligible_at=10:04:11
10:04:11  dispatched       task-7 r2

flywheel controller [--once] [--interval D] [flags]

$ flywheel controller --once
tick 2026-09-14T10:00:00Z: 4 actions (2 dispatch, 1 retry_scheduled, 1 request_inspection)
```

## I. Test plan

Standard library only; t.TempDir(); an injected `now` clock; the sim adapter replays fixtures
(internal/flywheel/adapter.go:simAdapter) instead of a real model; no real sleeps where avoidable.

| Test | Level | How (fake clock, sim adapter, temp dir) | Asserts |
| --- | --- | --- | --- |
| replay determinism | unit (state) | temp dir, fixture log, injected now | Derive twice on one log gives byte-identical state.json |
| reconcile table tests | unit (reconcile) | table of (State, Observed, Policy, now) cases | each case's []Action exactly as expected |
| idempotency | unit (reconcile) | Reconcile, apply actions, repeat N ticks | no duplicate intents, leases or attempts |
| worker death | integration (run+reconcile) | sim adapter; clock past lease ttl | lost event, retry_scheduled, fresh dispatch |
| controller death and restart | integration (controller) | kill the loop mid-tick, restart, replay | state rebuilt; expired intents/leases reconciled; factory continues |
| total blackout and resume | integration (controller) | sim fixtures removed, then restored | health WAITING; no false success; resume dispatches automatically |
| late completion | unit (state) | fixture log with r1 finishing after r2 | late result moves to stale list; state unchanged |
| retry exhaustion | unit (reconcile) | attempts >= max_attempts on a retryable finish | terminal failed event with the reason |
| validation tree mismatch | integration (validate+inspect) | tree mutated between validated and inspected | inspect refuses (evidence mismatch, exit 6) |
| needs graph | unit (resolver) | planned events + brief needs on three tasks | C ready only when A and B are passed or landed; a terminal target blocks then fails C |
| stalled-but-alive | unit (reconcile) | lease live, last_progress_at past the stall threshold | no state change; health STALLED with rule and evidence |
| two controllers | integration (controller) | two loops share controller.lock, stale generation | stale generation refuses to act (exit 6); takeover only after lock expiry |

## J. Compatibility

- Additive only: new event kinds (goal, dispatch_intent, retry_scheduled, lost, intent_expired,
  failed, inspected) and new fields (goal, intent, eligible_at) are appended; old events keep
  their shape, so a new binary replays any existing log unchanged.
- Old logs without goals, leases or intents behave as today, with stated defaults: no goals (goal
  counts are zero), no leases (nothing to expire), no intents (dispatch falls back to today's
  manual `flywheel run`), retry policy off (max_attempts 0 schedules no retries).
- Strict parsing cuts both ways: ParseEvents strict mode rejects unknown fields, so an old binary
  cannot read a new log — it exits 1 naming the first bad line. The log records its schema: the
  first event of a fresh log (init.go:InitSeeded) carries `"schema": 1`; ParseEvents refuses a log
  whose schema is newer than the binary understands, with the version in the error.
- state.json is derived, never a source of truth, so no migration: a new binary re-derives it
  from the same log on the first status/controller run; stale keys are regenerated.

## Phases

Eight units in dependency order (spec §51, reordered: status first so later phases extend it).
Each phase's unit header is a real brief: an `owns:` line with real file paths, a `needs:` line,
and `gate:` lines that are real commands run by `flywheel validate`; every phase ends with a
behaviour gate against the built binary (`go build -o .flywheel/bin/flywheel ./cmd/flywheel`,
a gitignored path). Phases leave the tree green; `auto_inspect` lands in P5.

### P1 Attempt identity and factory status

Goal: Derive compares each result-bearing event's attempt with the latest dispatched, moves
non-current results to the task's stale list, and `flywheel status` prints the §28 summary.
Owner sees: status with tasks/attempts/last event/last meaningful progress/health/next actions;
`flywheel verify` flags stale results.

```text
owns: internal/flywheel/state.go, internal/flywheel/status.go, cmd/flywheel/status_cmd.go
needs: design-b
gate: go build ./... && go vet ./... && go test ./internal/flywheel/ ./cmd/flywheel/ -count=1
gate: go test ./internal/flywheel/ -run 'Stale|ReplayDeterminism' -count=1
gate: go build -o .flywheel/bin/flywheel ./cmd/flywheel && .flywheel/bin/flywheel status; test $? -eq 0
```

### P2 Goals

Goal: goals are events — `flywheel goal add|list|show`, planned events may carry a goal — and
status shows goal progress.
Owner sees: `flywheel goal` works and status prints the goal block with met/failed transitions.

```text
owns: internal/flywheel/goal.go, internal/flywheel/events.go, cmd/flywheel/goal_cmd.go
needs: design-b
gate: go build ./... && go test ./internal/flywheel/ -run Goal -count=1
gate: go build -o .flywheel/bin/flywheel ./cmd/flywheel && .flywheel/bin/flywheel goal add "test goal" && .flywheel/bin/flywheel goal list | grep -q "test goal"
```

### P3 Leases and heartbeats

Goal: `flywheel run` writes, renews and removes `.flywheel/leases/<task>.<attempt>.json`; status
derives live/expired leases and reports lost attempts with evidence.
Owner sees: a sim worker's lease file appears and refreshes; after it "dies", status reports the
attempt lost with lease evidence.

```text
owns: internal/flywheel/leases.go, internal/flywheel/run.go, internal/flywheel/factory.go
needs: design-b
gate: go build ./... && go test ./internal/flywheel/ -run Lease -count=1
gate: go build -o .flywheel/bin/flywheel ./cmd/flywheel && .flywheel/bin/flywheel status; test $? -eq 0
```

### P4 Pure Reconcile and flywheel next

Goal: Reconcile(State, Observed, Policy, now) is pure — no I/O — and `flywheel next` prints its
actions read-only.
Owner sees: `flywheel next` lists expire/mark-lost/retry/dispatch/request-inspection actions with
reasons; running the same tick twice prints nothing new.

```text
owns: internal/flywheel/reconcile.go, cmd/flywheel/next_cmd.go
needs: design-b
gate: go build ./... && go test ./internal/flywheel/ -run Reconcile -count=1
gate: go build -o .flywheel/bin/flywheel ./cmd/flywheel && .flywheel/bin/flywheel next; test $? -eq 0
```

### P5 Controller loop

Goal: `flywheel controller` ticks Reconcile and executes actions crash-safely — intent appended
before spawn, echo on dispatched — under controller.lock, resolving needs and capping dispatch at
max_parallel; policy `auto_inspect` on dispatches the inspector persona on RequestInspection.
Owner sees: two controllers refuse to double-act (stale generation, exit 6); a killed controller
restarts and continues from the log; live attempts never exceed max_parallel.

```text
owns: internal/flywheel/controller.go, internal/flywheel/resolver.go, internal/flywheel/config.go, cmd/flywheel/controller_cmd.go
needs: design-b
gate: go build ./... && go test ./internal/flywheel/ -run Controller -count=1
gate: go build -o .flywheel/bin/flywheel ./cmd/flywheel && .flywheel/bin/flywheel controller --once; test $? -eq 0
```

### P6 Retry policy and durable retry state

Goal: finish reasons classify onto retry/wait/rework/terminal; retry_scheduled carries
eligible_at; attempts >= max_attempts fail the task; retry-wait tasks re-dispatch after
eligible_at.
Owner sees: a lost sim attempt shows retry-wait with eligible_at in status; exhausting the budget
fails the task with its reason; a controller restart keeps the retry timeline.

```text
owns: internal/flywheel/retry.go, internal/flywheel/reconcile.go, internal/flywheel/config.go
needs: design-b
gate: go build ./... && go test ./internal/flywheel/ -run Retry -count=1
gate: go build -o .flywheel/bin/flywheel ./cmd/flywheel && .flywheel/bin/flywheel status | grep -q retry
```

### P7 Health, stall, blackout and automatic resume

Goal: health derives one of the seven states with rule and evidence; a live-but-silent factory is
STALLED; a provider blackout makes it WAITING; restored capacity resumes dispatch automatically.
Owner sees: status shows the health state and why; killing every worker then restoring one resumes
the factory without manual redispatch.

```text
owns: internal/flywheel/status.go, internal/flywheel/reconcile.go, internal/flywheel/factory.go
needs: design-b
gate: go build ./... && go test ./internal/flywheel/ -run Health -count=1
gate: go build -o .flywheel/bin/flywheel ./cmd/flywheel && .flywheel/bin/flywheel status | grep -q 'Health:'
```

### P8 Explain, timeline, metrics, chaos tests and docs

Goal: `flywheel explain <task>` answers WHY with evidence, `flywheel events` prints the timeline,
`flywheel status --json` emits metrics, the §I chaos suite runs, and the runtime docs land.
Owner sees: explain/events work on a real fixture log; the twelve chaos tests pass under the fake
clock; docs/design/runtime/05-runtime.md describes the shipped runtime.

```text
owns: cmd/flywheel/explain_cmd.go, cmd/flywheel/events_cmd.go, cmd/flywheel/status_cmd.go, internal/flywheel/chaos_test.go, docs/design/runtime/05-runtime.md
needs: design-b
gate: go build ./... && go vet ./... && go test ./... -count=1
gate: go build -o .flywheel/bin/flywheel ./cmd/flywheel && .flywheel/bin/flywheel events | grep -q retry_scheduled
gate: .flywheel/bin/flywheel status --json | grep -q '"health"'
```