# Flywheel runtime: architecture report (Phase 0)

Flywheel's runtime today is a single Go CLI (module `flywheel`, standard library only) that
drives a factory loop of AI coding workers. A lead records work as events in an append-only
JSONL event log; materialized views (state.json, flywheel.md) are derived from it; work orders
carry owns/needs/gate; `flywheel run` dispatches an OpenCode worker process and watches it;
`flywheel factory` renders run states and raises an andon. Analysis only, as of commit 2e6f6fd
(2026-09-13); no code was changed.

## Subsystems

### Event log

The event log is `.flywheel/events.jsonl`, one JSON object per line, and is the source of truth:
state.json and flywheel.md are derived from it. `AppendEvent`
validates the event, stamps TS when empty, and appends one JSON line with a single O_APPEND
write, prefixing a newline when the file ends in a torn (crash) byte
(internal/flywheel/events.go:AppendEvent, internal/flywheel/events.go:needsNewlinePrefix).
`ReadEvents` parses the file; `ParseEvents` skips blank lines, errors on unresolved git conflict
markers and malformed lines (naming the line), and supports strict mode rejecting unknown fields
(internal/flywheel/events.go:ReadEvents, internal/flywheel/events.go:ParseEvents). `Validate`
enforces the task pattern `^[A-Za-z0-9._-]+$`, attempt pattern `^[rc][0-9]+$`, the kind set, and
kind-specific verdict rules (internal/flywheel/events.go:Validate). Event kinds: planned,
dispatched, started, worker_plan, finished, report, reviewed, blocked, landed, amended,
validated, owns_checked, inspected, staffed.

- State owned: `.flywheel/events.jsonl`; Event fields ts, task, kind, session, model, attempt,
  rc, reason, verdict, brief, needs, owns, commit, note, adapter, path, sha256, tokens, cost,
  steps, tree, gate, command, duration_ms, outside, persona (internal/flywheel/events.go:Validate).
- Guarantees: append-only log with one write per event; torn-write repair on append; every
  event validated before append; parse failures name the offending line; conflict markers never
  silently merged.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, appends and parses are deterministic; only the TS stamp reads the clock. |
| Q7 | Needs an agent alive? | No, log and parser run in the CLI itself. |
| Q8 | A worker dies? | Yes, events already appended survive; the CLI records the outcome. |
| Q9 | The orchestrating process dies? | Yes, events persist; a torn final byte is repaired on next append. |
| Q10 | All workers die? | Yes, log integrity is independent of worker liveness. |
| Q11 | All LLM providers unavailable? | Yes, logging requires no LLM. |
| Q12 | State reconstructable deterministically? | Partial, the log is the source of truth but state derivation is elsewhere. |
| Q13 | Stalled work detected automatically? | No, detection is not in this subsystem. |
| Q14 | Retried automatically? | No, retries are a higher-level decision. |
| Q15 | Retries survive a restart? | Partial, events survive; no retry policy exists; redispatch is a manual lead decision. |
| Q16 | Duplicate work possible? | Yes, nothing deduplicates events at append time. |
| Q17 | Stale worker results mutate newer state? | No, the log is append-only; mutation happens in derived state. |
| Q18 | Progress measurable without conversations? | Yes, kinds and fields (rc, tokens, duration_ms) carry progress. |

- Gaps: no sequence number or id on events; reads re-parse the whole file every time; no
  compaction or rotation; no schema versioning on the log.
### Materialized state

`State` is the derived snapshot written to `.flywheel/state.json` (version 2, updated_at, tasks,
counts). `Derive` recomputes it from the event log: events are sorted by parsed TS, then Task,
then a per-kind rank (planned < amended < dispatched < started < worker_plan < finished < report
< validated < owns_checked < inspected < reviewed < blocked < landed), then canonical JSON, so
the result is independent of the order events were concatenated in
(internal/flywheel/state.go:Derive). The latest status-bearing
event per task decides status (planned, dispatched, running, finished, passed, needs-correction,
rejected, blocked, landed); amended only updates brief/needs/owns
(internal/flywheel/state.go:Derive). `WriteState` reads the log, derives, writes state.json via
temp file plus rename (`atomicWrite`), and updates the marked status table in flywheel.md between
`<!-- flywheel:status:start -->` markers; same input gives byte-identical output
(internal/flywheel/state.go:WriteState, internal/flywheel/state.go:atomicWrite).

- State owned: `.flywheel/state.json`, the marked status block in flywheel.md; TaskState fields
  id, status, session, model, attempt, rc, verdict, reason, brief, needs, owns, attempts,
  updated_at; State counts per status (internal/flywheel/state.go:updateStatusBlock).
- Guarantees: derived deterministically from the log (sorted replay, canonical tiebreak); atomic
  writes so a crash never leaves a half-written state.json; no mutation of the log itself.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, Derive sorts deterministically; same log gives byte-identical output. |
| Q7 | Needs an agent alive? | No, derivation runs in the CLI. |
| Q8 | A worker dies? | Yes, state reflects whatever events were appended. |
| Q9 | The orchestrating process dies? | Yes, state.json is atomically written; stale files are overwritten. |
| Q10 | All workers die? | Yes, derivation needs no workers. |
| Q11 | All LLM providers unavailable? | Yes, state derivation is pure computation. |
| Q12 | State reconstructable deterministically? | Yes, Derive fully reconstructs State from the log. |
| Q13 | Stalled work detected automatically? | No, statuses are event-driven; nothing watches wall-clock time. |
| Q14 | Retried automatically? | No, derivation only reflects retries someone else scheduled. |
| Q15 | Retries survive a restart? | Yes, retries are events in the log and replay identically. |
| Q16 | Duplicate work possible? | Partial, attempts counter increments per dispatched; no dedup check. |
| Q17 | Stale worker results mutate newer state? | Yes, Derive does not check an event's attempt against the latest dispatch; a late event from an older attempt wins. |
| Q18 | Progress measurable without conversations? | Yes, attempts, status and counts expose progress. |

- Gaps: no stall detection (no liveness timestamps consulted); state.json is a full rewrite, not
  incremental; no history retained beyond the last status per task; two processes writing
  state.json race (last rename wins); Derive does not check an event's attempt against the
  latest dispatch, so a late finished or validated event from an older attempt overwrites the
  newer attempt's status.

### Work orders

A work order is a brief file whose header carries the keys owns, needs, gate, exclusive, review.
`ParseBriefHeader` reads the key: value block at the top of the brief (until the first blank line
followed by a '#' heading, or the first 40 lines), splits comma-separated owns values with
indented continuation lines, strips trailing parenthesised annotations like "(new)", and keeps
gate lines one command per line in order; the header also carries the SHA-256 of the whole brief
file (internal/flywheel/brief.go:ParseBriefHeader, internal/flywheel/brief.go:stripAnnotation).
Each `gate:` line is a shell
command run against the worktree by `runGate` (bash -c, cmd /C on Windows, or sh -c), reporting
exit code, elapsed time and output (internal/flywheel/gauges.go:runGate). Gates are the contract
`flywheel validate` enforces: `ValidateTask` runs each gate, checks that changed paths stay
inside owns, and records a validated event with the tree hash (internal/flywheel/gauges.go:ValidateTask).

- State owned: the brief file (arbitrary path per event); planned/amended events carry brief,
  needs, owns; validated events carry gate and tree (internal/flywheel/events.go:Validate).
- Guarantees: the brief header has a stable parse (40-line cap, annotation stripping); gate
  order is preserved; a full-brief SHA-256 is available to detect edits; no gate runs outside
  `flywheel validate`'s shell runner.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, header parsing is order-stable and SHA-256 is fixed. |
| Q7 | Needs an agent alive? | No, parsing and gate running are CLI-side. |
| Q8 | A worker dies? | Yes, the brief file is inert between runs. |
| Q9 | The orchestrating process dies? | Yes, the brief and its SHA-256 survive on disk. |
| Q10 | All workers die? | Yes, orders are data, not processes. |
| Q11 | All LLM providers unavailable? | Yes, gate checks are local shell commands. |
| Q12 | State reconstructable deterministically? | Partial, the brief is hashed but not re-read into state. |
| Q13 | Stalled work detected automatically? | No, nothing watches order age. |
| Q14 | Retried automatically? | No, redispatch is a lead decision via log. |
| Q15 | Retries survive a restart? | Yes, the brief file persists; only the SHA-256 pins it. |
| Q16 | Duplicate work possible? | Yes, nothing prevents dispatching the same brief twice. |
| Q17 | Stale worker results mutate newer state? | Partial, gates run against the worktree as found. |
| Q18 | Progress measurable without conversations? | Partial, gate pass/fail is visible; order age is not surfaced. |

- Gaps: no ordering dependency (needs) is enforced at dispatch time, only declared; the brief
  text is never stored in the event log, only its path and hash; no timeout or budget on gate
  commands; no change tracking from amended to new owns.

### Config and init

Configuration lives in `.flywheel/config.json`: version, a workers list (adapter "opencode" or
"sim", model, variant, max_parallel, approved/fallback models), limits (per_host, budget),
feedback (upstream, submit). `LoadConfig` reads it with unknown fields rejected and full
validation, falling back to `DefaultConfig` when the file is missing
(internal/flywheel/config.go:LoadConfig, internal/flywheel/config.go:DefaultConfig). `Validate`
reports every problem at once (worker name pattern, duplicate names, adapter/model rules,
fallbacks must differ, per_host >= 0, submit in {ask, never}); `Set` mutates keys like model or
limits.per_host, and `WriteConfig` writes atomically via temp file plus rename
(internal/flywheel/config.go:Validate, internal/flywheel/config.go:WriteConfig). `InitSeeded`
scaffolds the runtime: flywheel.md (status markers), state.json (from `Derive` of no events),
events.jsonl, config.json and .flywheel/.gitignore; it preflights destinations, stages payloads,
creates files with O_EXCL so racing creators are detected, and rolls back its own footprint on
failure; events.jsonl and config.json are never overwritten, even with --force
(internal/flywheel/init.go:InitSeeded, internal/flywheel/init.go:publishFile).

- State owned: `.flywheel/config.json`, `.flywheel/.gitignore`; init creates flywheel.md,
  events.jsonl, state.json; `IgnoredStateFiles` reports which of those git would ignore
  (internal/flywheel/init.go:IgnoredStateFiles).
- Guarantees: missing config behaves as the built-in default; config writes are validated and
  atomic; init never truncates existing events.jsonl or config.json; failed init restores
  preexisting bytes.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, defaults and scaffolding output are fixed byte-for-byte. |
| Q7 | Needs an agent alive? | No, config and init are pure CLI operations. |
| Q8 | A worker dies? | Yes, config.json is inert state. |
| Q9 | The orchestrating process dies? | Yes, writes are atomic; init rollback is best-effort per call. |
| Q10 | All workers die? | Yes, scaffolding needs nothing live. |
| Q11 | All LLM providers unavailable? | Yes, init and config never call providers. |
| Q12 | State reconstructable deterministically? | Partial, defaults regenerate config; events cannot be recreated. |
| Q13 | Stalled work detected automatically? | No, no liveness logic here. |
| Q14 | Retried automatically? | No. |
| Q15 | Retries survive a restart? | Yes, config and initialized files persist on disk. |
| Q16 | Duplicate work possible? | Partial, init's O_EXCL prevents duplicate scaffolding. |
| Q17 | Stale worker results mutate newer state? | No, init writes once and never overwrites the log. |
| Q18 | Progress measurable without conversations? | No, config and init carry no progress data. |

- Gaps: no per-run budget enforcement in code (budget is declared but unused); fallbacks
  require manual editing of config.json; no schema migration path beyond version 1; init is
  documented as not crash-atomic across files.

### Dispatch (`flywheel run`) and the worker process

`Run` is the whole dispatch: it loads config, resolves the worker and model, requires a planned
event carrying the brief path (T1), numbers the attempt (r<n+1> fresh, c<m+1> resume), writes an
embedded OpenCode permission policy when missing (git writes denied, OPENCODE_CONFIG pointed at
it), appends a dispatched event pointing at `.flywheel/runs/<task>.<attempt>.jsonl` with the
prompt SHA-256, starts the worker, streams stdout into the run file while parsing, and records
started, worker_plan, report and finished events (internal/flywheel/run.go:Run,
internal/flywheel/run.go:workerPolicySHA). A watchdog kills the child if no stdout line arrives
within the start timeout (default 60s) and the run finishes as reason "silent"
(internal/flywheel/run.go:Run). Every path after dispatched records a finished event: an error
records reason "error" or "start-failed" with the stderr diagnostic; a worker that exits before
any completed step is start-failed (internal/flywheel/run.go:firstStderrLine,
internal/flywheel/run.go:clipNote).
The adapter builds the command (`opencode run --pure -m <model> --auto --format json` with the
brief attached via --file, session passed only on resume) and parses the JSONL stream into
start/text/tool/step/error observations (internal/flywheel/adapter.go:Command,
internal/flywheel/adapter.go:Parse). Exit codes: 0 worker rc 0 with reason stop, 4 nonzero or
capped/error, 3 silent start timeout (internal/flywheel/run.go:ExitCode).

- State owned: `.flywheel/runs/<task>.<attempt>.jsonl` (raw stream), `.err`, `.plan.md`,
  `.report.md`; dispatched/started/worker_plan/report/finished events; attempts are the
  rN/cN counter in the log (internal/flywheel/run.go:attemptNum).
- Guarantees: fresh runs never reuse a session (resume only continues via --session); dispatch
  without a planned event is refused; the worker permission policy forbids git writes; run
  files are hashed and the hash recorded in finished; every outcome after dispatched is
  recorded as an event.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Partial, attempt numbering and events are deterministic; LLM output is not. |
| Q7 | Needs an agent alive? | Yes, dispatch starts a worker process (opencode) to do the work. |
| Q8 | A worker dies? | Yes, exit code, reason and stderr note are recorded as finished. |
| Q9 | The orchestrating process dies? | Partial, events before death persist; the child may be orphaned. |
| Q10 | All workers die? | Yes, each run is independent; finished records the failure. |
| Q11 | All LLM providers unavailable? | Yes, start-failed or silent records it; resume can retry later. |
| Q12 | State reconstructable deterministically? | Partial, events replay; run files and session output are opaque. |
| Q13 | Stalled work detected automatically? | Partial, only the start timeout fires; mid-run stalls are not detected. |
| Q14 | Retried automatically? | No, retries are manual (resume via delta). |
| Q15 | Retries survive a restart? | Yes, resume reads the last session from the log and re-runs. |
| Q16 | Duplicate work possible? | Yes, nothing stops two dispatches of the same task. |
| Q17 | Stale worker results mutate newer state? | Partial, finished events stamp the log; run files are written in place. |
| Q18 | Progress measurable without conversations? | Yes, steps, tokens, cost and run-file growth are recorded. |

- Gaps: no mid-run stall detection (watchdog only covers the start); no concurrency control or
  max_parallel enforcement in Run; no retry policy (resume is manual); no cap on run duration or
  cost; session output is only parsed, never summarized.

### Run states and andon (`flywheel factory`)

`Watcher.Refresh` draws the live floor from the config, the event log and the run files,
reading only bytes appended since the last refresh (offsets remembered per file; a truncated
file is re-read from zero) and building a Floor with lines, staffing, units, andon and output
(internal/flywheel/factory.go:Refresh, internal/flywheel/factory.go:readEvents). Each unit's run
state is classified from its run file's signals — error, finish reason "length", done event,
size, age since last growth, step count, files read, edits — into running, exploring, long-step
(>5min), stalled (>10min), silent (no output after 60s), capped, provider-error, blocked (a clean
stop after a permission-denied signal), no-writes (a floor state, not a signal: a clean stop whose
finished event wrote no file; blocked and no-writes apply only while the unit is awaiting
judgement, issue #364) or done
(internal/flywheel/factory.go:classifyRun). The andon lists units in silent, stalled, no-writes,
blocked, capped or provider-error, oldest first, plus per-line busy counts and the output summary (first-pass rate
from first verdicts, rework ratio, tokens, cost, landed today) (internal/flywheel/factory.go:buildAndon,
internal/flywheel/factory.go:buildOutput). The CLI render (`flywheel factory` with `--once` or
watch mode) calls Refresh and prints the floor; rendering never calls a model and never runs a
git write (internal/flywheel/factory.go:NewWatcher).

- State owned: only reads — events.jsonl, config.json, run files; keeps in-memory offsets,
  per-run step/file/edit/error/reason tallies (internal/flywheel/factory.go:NewWatcher).
- Guarantees: incremental reads (only appended bytes, only complete lines); the floor is fully
  derivable from log plus run files; andon thresholds are fixed (60s silent, 300s long-step,
  600s stalled); live units (running, exploring, long-step, silent, stalled) occupy busy slots,
  capped and provider-error do not (internal/flywheel/factory.go:liveRun).

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, classification is pure; only now (clock) is injected. |
| Q7 | Needs an agent alive? | No, the floor only reads files. |
| Q8 | A worker dies? | Yes, death is visible as done, capped or provider-error. |
| Q9 | The orchestrating process dies? | Yes, the floor is rebuilt from files on the next refresh. |
| Q10 | All workers die? | Yes, the floor shows every unit's final state. |
| Q11 | All LLM providers unavailable? | Yes, rendering needs no provider. |
| Q12 | State reconstructable deterministically? | Partial, events and run files replay; the floor is derived, not stored. |
| Q13 | Stalled work detected automatically? | Yes, silent/long-step/stalled states raise an andon from ages. |
| Q14 | Retried automatically? | No, the andon only signals; a human or lead must act. |
| Q15 | Retries survive a restart? | Yes, run files and offsets re-read after truncation. |
| Q16 | Duplicate work possible? | Yes, nothing dedupes; the floor just displays attempts. |
| Q17 | Stale worker results mutate newer state? | No, the floor reads the latest attempt only. |
| Q18 | Progress measurable without conversations? | Yes, steps, ages, sizes and token aggregates are all file-derived. |

- Gaps: andon is display-only — no alerting, no automatic re-dispatch; watch mode and andon
  persistence are not in this package; stall classification trusts mtime, which a touched file
  can spoof; per-unit cost is not surfaced on units, only aggregated.

### Gauges (`flywheel validate`)

`ValidateTask` loads the task's planned brief (refusing a task with no planned event or a brief
with no gate lines), hashes the exact tree as found using a throwaway git index (`treeHash`:
read-tree HEAD, add -A, drop `.flywheel/` and `flywheel.md` from the index, write-tree), runs
each declared `gate:` line through bash -c (cmd /C on Windows, sh elsewhere) with a one-shot
rerun when Windows Smart App Control blocked the freshly built binary, writes the combined
output to `.flywheel/evidence/<task>/<attempt>/gate-<n>.log`, and records one validated event
per gate carrying gate, command, tree, rc, duration, output SHA-256 and log path
(internal/flywheel/gauges.go:ValidateTask, internal/flywheel/gauges.go:treeHash,
internal/flywheel/gauges.go:runGate). The owns check lists every path differing from HEAD plus
untracked files and flags those outside the owns boundary, then records owns_checked and
refreshes state (internal/flywheel/gauges.go:changedPaths,
internal/flywheel/gauges.go:ownsContains, internal/flywheel/gauges.go:finishValidate). The CLI
prints pass/fail per gate and exits 0 on `OK()`, 5 when a gate failed or a path sits outside
owns, 1 on error, 2 on usage; the task id is accepted before or after flags
(cmd/flywheel/validate_cmd.go:runValidate, cmd/flywheel/main.go:parseArgs).

- State owned: `.flywheel/evidence/<task>/<attempt>/gate-<n>.log`; validated events (gate,
  command, tree, rc, duration_ms, sha256, path) and owns_checked events (tree, outside).
- Guarantees: the tree hash never touches the shared index (throwaway GIT_INDEX_FILE); every
  gate's combined output is kept verbatim with a hash, so a reading can be re-verified; owns is
  checked against the tree as found; a host-blocked gate is rerun once before it counts.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, tree hash, owns set and gate pass/fail are deterministic; only duration and output text vary. |
| Q7 | Needs an agent alive? | No, gates are local shell commands. |
| Q8 | A worker dies? | Yes, readings are event records needing no worker. |
| Q9 | The orchestrating process dies? | Partial, evidence logs persist; a killed validate records no validated event for unfinished gates. |
| Q10 | All workers die? | Yes, validation is CLI-side. |
| Q11 | All LLM providers unavailable? | Yes, gates run locally. |
| Q12 | State reconstructable deterministically? | Partial, evidence files are not re-derived; only their hashes live in events. |
| Q13 | Stalled work detected automatically? | No, validate is pull-based. |
| Q14 | Retried automatically? | No, the lead re-runs validate. |
| Q15 | Retries survive a restart? | Yes, evidence and events persist on disk. |
| Q16 | Duplicate work possible? | Yes, nothing prevents validating the same tree twice. |
| Q17 | Stale worker results mutate newer state? | No, the hash is of the tree as found; a stale result simply will not match. |
| Q18 | Progress measurable without conversations? | Partial, per-gate rc and duration are surfaced; no trend analysis. |

- Gaps: no timeout or budget on gate commands (a hanging gate hangs validate); gates run with
  the caller's environment; no diff of gate output between attempts; the owns check has no
  exemption list beyond flywheel's own files.

### Inspection (`flywheel inspect`)

`InspectTask` is the poka-yoke gate on verdicts. It refuses a verdict outside {pass, rework,
scrap, escalate} (T8), refuses a missing session or one that belongs to a worker of the task
(T4, via `sessionClash` — the worker-event set matches verify's ruleT4, so the inspector's own
inspected events never count), and for a pass verdict demands supervisor readings: a passing
validated event for every gate in the brief header plus a clean owns_checked, all on the same
tree hash and all recorded after the task's latest finished event (`requireReadings`,
`latestFinished`, `hasPassingValidated`, `hasCleanOwnsChecked`). On success it appends an
inspected event carrying verdict, tree, session and note, and refreshes state
(internal/flywheel/inspect.go:InspectTask, internal/flywheel/inspect.go:requireReadings,
internal/flywheel/inspect.go:sessionClash). A refusal is a RuleRefusal error: the CLI exits 6
with the rule id and the fix, 1 for any other error (cmd/flywheel/inspect_cmd.go:runInspect,
internal/flywheel/inspect.go:IsRuleRefusal). State maps verdicts pass/rework/scrap/escalate to passed/needs-correction/rejected/blocked (internal/flywheel/state.go:Derive).

- State owned: inspected events (verdict, tree, session, note); status transitions in state.json.
- Guarantees: no pass without a supervisor reading on the same tree after the latest finished;
  the inspector session can never collide with a worker session; the tree recorded is the one
  hashed at inspection time.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, all checks compare events and tree hashes. |
| Q7 | Needs an agent alive? | No, only a --session string is supplied. |
| Q8 | A worker dies? | Yes, inspection reads events only. |
| Q9 | The orchestrating process dies? | Partial, a refused inspection records no event; only the final state is in the log. |
| Q10 | All workers die? | Yes, inspection needs no worker. |
| Q11 | All LLM providers unavailable? | Yes, verdicts are local decisions. |
| Q12 | State reconstructable deterministically? | Yes, inspected events replay into status. |
| Q13 | Stalled work detected automatically? | No, inspection is triggered by the lead. |
| Q14 | Retried automatically? | No, the lead re-inspects after a rework. |
| Q15 | Retries survive a restart? | Yes, the recorded session and note persist. |
| Q16 | Duplicate work possible? | Partial, re-inspecting the same tree appends another event; nothing dedupes. |
| Q17 | Stale worker results mutate newer state? | Partial, T3 binds readings to the tree after the latest finished, but Derive never checks the inspected tree against the latest dispatch. |
| Q18 | Progress measurable without conversations? | No, verdicts are binary snapshots. |

- Gaps: nothing checks that a rework delta exists before the next correction dispatch; a pass on
  an old tree recorded after a newer dispatch still updates state (Derive ignores the tree);
  the reviewed event kind exists but inspect only writes inspected.

### Verify rules (`flywheel verify`)

`VerifyTasks` replays the event log and checks every requested task (or every task with --all,
with an empty but valid log verifying clean) against the five implemented poka-yoke rules,
producing one VerifyItem per check with a reason: T1 every dispatched event's SHA-256 matches
the prompt it was dispatched with (fresh runs against the planned brief, a later amendment
explains a change; corrections against the delta file their dispatched.Brief names, and
amendments never waive a correction's hash); T3 every inspected pass has a passing validated
event per gate plus a clean owns_checked on the same tree after the latest finished before that
inspection (each inspection uses its own window, so a later correction attempt does not fail an
earlier legitimate pass); T4 no inspected event carries a worker session; T5 no landed event
without an earlier inspected pass; T8 validated and owns_checked carry persona supervisor and
inspected carries inspector or lead (internal/flywheel/verify.go:VerifyTasks,
internal/flywheel/verify.go:ruleT1, internal/flywheel/verify.go:ruleT3,
internal/flywheel/verify.go:ruleT4, internal/flywheel/verify.go:ruleT5,
internal/flywheel/verify.go:ruleT8). The CLI prints PASS/FAIL per check or emits --json, and
exits 0 when every check passes, 6 on any failure, 2 on usage, 1 on error
(cmd/flywheel/verify_cmd.go:runVerify).

- State owned: none — verify only reads the log, the brief and the delta files; the result is
  printed, never stored.
- Guarantees: the rules are pure functions of the log plus files on disk; T3 windows are
  per-inspection; a passing reading always requires the same tree the inspection recorded.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, a pure replay of log and files. |
| Q7 | Needs an agent alive? | No. |
| Q8 | A worker dies? | Yes, verify reads events only. |
| Q9 | The orchestrating process dies? | Yes, verify is a stateless read. |
| Q10 | All workers die? | Yes. |
| Q11 | All LLM providers unavailable? | Yes. |
| Q12 | State reconstructable deterministically? | Partial, checks current files against recorded hashes; it restores nothing. |
| Q13 | Stalled work detected automatically? | No, no liveness checks exist here. |
| Q14 | Retried automatically? | No. |
| Q15 | Retries survive a restart? | Yes, the log is the only input. |
| Q16 | Duplicate work possible? | Partial, T1 flags a mismatched brief, but two identical dispatches both pass. |
| Q17 | Stale worker results mutate newer state? | No, verify only reports; it never mutates state. |
| Q18 | Progress measurable without conversations? | No, only pass/fail per rule. |

- Gaps: only T1, T3, T4, T5 and T8 exist in code (the other rules of the design doc are not
  found in code); needs-before-dispatch ordering is never enforced; a failed verify leaves no
  event behind.

### Staffing (`flywheel staff`)

`flywheel staff --role <r> --session <s>` registers a factory role on the floor: it appends a
staffed event carrying session, persona (the role), model and note, re-derives state, and
prints `<role> <session>`; a missing --role or --session exits 2, other errors exit 1
(cmd/flywheel/staff_cmd.go:runStaff, cmd/flywheel/staff_cmd.go:staffFlags). A staffed event
carries no task, so `Derive` skips it and it changes no task status — staffing is a floor-level
fact visible only in the log (internal/flywheel/state.go:Derive). The floor shows the latest
staffed event per role — the lead line `<session> (<model>)` or "not registered"
(internal/flywheel/factory.go:buildStaffing). The lead skill instructs
`flywheel staff --role lead --session <your session> --model <model>` at session start
(skills/flywheel/SKILL.md).

- State owned: staffed events; nothing in state.json (no staff list is derived into task state).
- Guarantees: staffing is append-only like every event; the latest staffed event per role wins
  on the floor; validation requires a session (and defaults the persona when empty).

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, an append with fixed fields. |
| Q7 | Needs an agent alive? | No, it records a claim. |
| Q8 | A worker dies? | Yes, staffing is inert. |
| Q9 | The orchestrating process dies? | Yes, the event persists. |
| Q10 | All workers die? | Yes. |
| Q11 | All LLM providers unavailable? | Yes. |
| Q12 | State reconstructable deterministically? | Partial, staffed events replay, but state.json carries no staff list. |
| Q13 | Stalled work detected automatically? | No. |
| Q14 | Retried automatically? | No. |
| Q15 | Retries survive a restart? | Yes, the event persists on disk. |
| Q16 | Duplicate work possible? | Partial, re-staffing the same role appends another event; latest wins, nothing dedupes. |
| Q17 | Stale worker results mutate newer state? | No, staffed events are never status-bearing. |
| Q18 | Progress measurable without conversations? | No, purely a register. |

- Gaps: no expiry or liveness check on registered sessions; a role can staff twice with
  different sessions; staffing claims are never verified against a live process.

### Git and worktrees

The CLI never creates worktrees and never mutates the shared git index. `treeHash` computes the
SHA-1 tree id with a throwaway temp index: git read-tree HEAD, git add -A, drop `.flywheel/`
and `flywheel.md` from the index so flywheel's own bookkeeping never enters a unit's tree, then
git write-tree, all with GIT_INDEX_FILE pointed at the temp file (internal/flywheel/gauges.go:treeHash,
internal/flywheel/gauges.go:gitRun). The owns check reads the tree with read-only commands —
git diff --name-only HEAD plus git ls-files --others --exclude-standard
(internal/flywheel/gauges.go:changedPaths, internal/flywheel/gauges.go:gitRead).
`flywheel validate --workdir` and `flywheel inspect --workdir` hash another tree while the log
and evidence stay in --dir. init consults git only to report which state files git would ignore
(git check-ignore) (internal/flywheel/init.go:IgnoredStateFiles). Worker isolation: every
dispatch writes the embedded OpenCode permission policy when missing and points OPENCODE_CONFIG
at it, denying git stash/reset/checkout/restore/clean/switch/commit/rebase/merge/cherry-pick/
pull/push and git -C/--work-tree/--git-dir in the worker's shell
(internal/flywheel/run.go:workerPolicySHA, internal/flywheel/run.go:workerEnv).

- State owned: a temp index file during hashing (removed on return); reads the shared index
  only through read-tree/write-tree.
- Guarantees: validate and inspect never modify the shared index or worktree; worker git writes
  are denied by the permission policy; flywheel's own files never pollute a tree hash.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, write-tree output depends only on the tree contents. |
| Q7 | Needs an agent alive? | No. |
| Q8 | A worker dies? | Yes, hashing is momentary. |
| Q9 | The orchestrating process dies? | Partial, a killed hash may orphan a temp index file; the shared index is untouched. |
| Q10 | All workers die? | Yes. |
| Q11 | All LLM providers unavailable? | Yes. |
| Q12 | State reconstructable deterministically? | Partial, tree ids are recomputed on demand, not stored. |
| Q13 | Stalled work detected automatically? | No. |
| Q14 | Retried automatically? | No. |
| Q15 | Retries survive a restart? | Yes, the working tree is the durable object. |
| Q16 | Duplicate work possible? | Yes, nothing stops hashing the same tree twice. |
| Q17 | Stale worker results mutate newer state? | No, hashes describe the tree as found. |
| Q18 | Progress measurable without conversations? | No. |

- Gaps: no worktree isolation at all — in-flight workers share one working tree, so disjoint
  owns is a brief-level convention, never enforced; no check that the workdir is a git
  repository before read-tree fails; git must be on PATH.

### Skills (skills/*/SKILL.md)

skills/flywheel/SKILL.md is the lead's operating manual, written as prose: the five-step loop
Plan → Brief → Dispatch → Review → Correct-or-land, the personas table (lead, planner, foreman,
worker, supervisor-as-CLI, inspector, auditor, steward, operator), the invariants (approved
worker model only, orchestrator never implements, worker unavailable → report the blocker and
halt, no unrequested commits or secrets, every dispatch carries the deny policy), and the
compact loop with the canonical commands (`flywheel log`/`flywheel run`, OPENCODE_CONFIG
pointed at skills/flywheel/references/worker-permissions.json, `--variant low`, the session id
from the JSONL). Its references: references/worker-brief.md (brief template, concurrency and
dirty-edit rules, run states and failures, review traps, the correction loop, the
blocker/do-not-take-over protocol), references/factory.md (the shared factory model), and
references/worker-permissions.json (the deny policy, byte-identical to the embedded policy in
run.go). The role skills each describe one persona: skills/flywheel-worker/SKILL.md (execute
exactly the brief, run its gates, report evidence, never commit), skills/flywheel-planner/
SKILL.md (turn goals into owns/needs/gate work orders, never dispatch),
skills/flywheel-foreman/SKILL.md (dispatch, watch run states, apply the retry policy, pull the
andon), skills/flywheel-inspector/SKILL.md (QC verdicts pass/rework/scrap/escalate from gauge
readings on the same tree), skills/flywheel-auditor/SKILL.md (independent re-measurement, never
the same session or model as the lead/planner/inspector), skills/flywheel-steward/SKILL.md
(triage signals into learnings with corrective actions), skills/flywheel-operator/SKILL.md
(install flywheel, check it is healthy, and drive the loop from any role).

- State owned: none — skills are static files read by agents; skills/flywheel/evals/evals.json
  holds skill evaluations.
- Guarantees: the brief contract in prose matches what the CLI enforces (gate lines, owns);
  the permissions JSON matches the embedded policy byte-for-byte; the loop is documented as
  prose — the CLI enforces only the subset in verify and inspect.

| Q | Question | Answer |
| --- | --- | --- |
| Q6 | Deterministic? | Yes, static text. |
| Q7 | Needs an agent alive? | Yes, the loop is run by an agent following the prose. |
| Q8 | A worker dies? | Partial, the skills describe classification and retry; nothing executes them. |
| Q9 | The orchestrating process dies? | Yes, the files persist for the next session. |
| Q10 | All workers die? | Yes. |
| Q11 | All LLM providers unavailable? | Yes, reading a skill needs no provider. |
| Q12 | State reconstructable deterministically? | Partial, the loop is documented, not stored as state. |
| Q13 | Stalled work detected automatically? | No, the skill points at the andon; no code watches. |
| Q14 | Retried automatically? | No, the skill documents manual retry. |
| Q15 | Retries survive a restart? | Yes, skills are files. |
| Q16 | Duplicate work possible? | No, the disjoint-ownership rule is prose only. |
| Q17 | Stale worker results mutate newer state? | No, skills never mutate state. |
| Q18 | Progress measurable without conversations? | No. |

- Gaps: the retry policy the foreman skill promises is not implemented in code (redispatch is a
  manual lead decision); the independence rules (auditor never the lead's session or model) are
  prose, unenforced by the CLI; the worker-permissions.json duplicates the policy embedded in
  run.go.

## Crash walk-throughs

(a) **A worker is killed mid-run.** Events written: dispatched (before spawn), started when the
first JSON line arrived, worker_plan when a PLAN line arrived, report when a final text message
arrived, then finished with rc = the kill's exit code and reason = the last observed step reason,
or "error"/"start-failed" when no step completed; nothing is missing. `flywheel state` shows the
task finished with rc and attempts=1; `flywheel factory` shows the unit done with its finish
reason and it does not appear on the andon. The lead reads the run file and either resumes
(c1) or scraps (internal/flywheel/run.go:Run).

(b) **`flywheel run` itself is killed.** dispatched is appended before the child starts;
started/worker_plan/report may or may not follow; the finished event is missing because the
deferred recorder never runs, and the opencode child is orphaned until it dies or is killed.
`flywheel state` keeps the last status-bearing event — running if started arrived, dispatched
otherwise. `flywheel factory` classifies the unit from run-file signals: once the file stops
growing, the unit shows stalled past 600s (or silent if it never produced output) and the andon
lists it. The lead kills the leftover process, then resumes with c1 (the recorded session makes
resume work) (internal/flywheel/factory.go:classifyRun, internal/flywheel/run.go:Run).

(c) **The lead session ends with units queued.** No event kind exists for a lead ending; the
planned and dispatched events persist untouched. `flywheel state` and `flywheel factory` keep
showing planned/dispatched/running units exactly as before — nothing expires queued units. A new
lead session reads the same log and picks up where the old one stopped (state is the repo, not
any vendor session).

(d) **Every provider returns errors for an hour.** Each dispatched worker exits with provider
errors: the run records finished with reason "error" (error observations), "start-failed" (exit
before a step, first stderr line as note) or "silent" (start timeout), and `flywheel run` exits
4, 3 or 4 respectively (internal/flywheel/run.go:firstStderrLine, internal/flywheel/run.go:ExitCode).
`flywheel state` shows each task finished with its reason; `flywheel factory` classifies the
units provider-error and the andon lists them oldest first. No retry policy exists, so nothing
re-dispatches; the lead waits and resumes with cN once providers recover
(internal/flywheel/factory.go:buildAndon).

(e) **The machine restarts.** events.jsonl survives except possibly a torn final byte, repaired
with a newline prefix on the next append; state.json and flywheel.md were written atomically, so
a crash mid-write leaves the old version (internal/flywheel/events.go:needsNewlinePrefix,
internal/flywheel/state.go:atomicWrite). In-flight workers die with the machine without a
finished event — exactly like case (b): `flywheel state` shows running or dispatched, and
`flywheel factory` shows stalled once the run file stops growing. The next `flywheel state`
re-derives from the log; the next `flywheel factory` rebuilds the floor from files. Recorded
sessions survive, so the lead resumes with c1.

## Summary

| Concept | Exists? | Where | Note |
| --- | --- | --- | --- |
| goal | no | — | no goal record in code; the lead holds it (not found in code) |
| plan | yes | worker_plan events + .plan.md files (internal/flywheel/run.go:Run) | recorded when the worker prints a PLAN line |
| task | yes | task ids in events and state.json (internal/flywheel/state.go:Derive) | pattern ^[A-Za-z0-9._-]+$ |
| attempt | yes | rN/cN attempt ids on events (internal/flywheel/run.go:attemptNum) | fresh vs correction numbering |
| worker | yes | workers list in config.json, adapter per run (internal/flywheel/run.go:Run) | opencode or sim |
| lease | no | — | no lease concept found in code |
| heartbeat | partial | silent/long-step/stalled signals from run-file age and growth (internal/flywheel/factory.go:classifyRun) | no explicit heartbeat event |
| retry policy | no | — | no retry policy exists; redispatch is a manual lead decision |
| reconciler | no | — | nothing reconciles log against files; the lead does it by hand |
| health | partial | andon list (internal/flywheel/factory.go:buildAndon) | display-only, no alerting |
| progress vs activity | partial | steps/tokens/cost on finished (internal/flywheel/run.go:Run); growth/age/reads/edits in the factory | both measured, not reconciled into one signal |
| tree-bound evidence | yes | validated events carry the tree hash; T3 binds readings to it (internal/flywheel/inspect.go:requireReadings) | gates and owns are bound to a tree id |
| stale-result rejection | partial | T3 per-inspection windows (internal/flywheel/verify.go:ruleT3) | Derive ignores attempt, so a late event from an older attempt wins |
