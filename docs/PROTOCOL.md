# flywheel protocol v1

This is the protocol flywheel enforces today, in code — not the fuller factory model it is
building toward. The authority for everything below is the code itself:
`internal/flywheel/events.go` (the `kinds` map and `Validate`), `internal/flywheel/state.go`
(`Derive`'s status transitions), and `internal/flywheel/verify.go` (rules T1, T3, T4, T5, T8, the
only ones implemented). `docs/design/autonomous-shipping.md` describes a larger design — audits,
nonconformances, andon signals, a hash-chained log — that this repo has not built yet; §3 below
says exactly which parts of that design are still aspiration, so a reader never has to guess.

Every record is one JSON line appended to `.flywheel/events.jsonl` by `AppendEvent`, and the log is
never rewritten — `flywheel log` (or a command that calls `AppendEvent` internally) is the only way
to add a line. `flywheel state` derives `.flywheel/state.json` and the status block in
`flywheel.md` from the log alone (`Derive`): the log is the one source of truth, everything else is
a read-only projection of it.

Every skill in `skills/` that drives this loop cites `protocol v1` and links back here;
`cmd/flywheel/docs_test.go` fails the build the moment a skill stops citing it, or this file's
first line stops matching `^# flywheel protocol v`.

## 1. Required entries per task

Twenty-one event kinds exist; `events.go`'s `kinds` map is the authority for the list, and
`Validate` rejects anything else. Nine of them carry a task's status (`state.go`'s `kindRank`
orders them for replay); the rest — `worker_plan`, `no-plan`, `off-course`, `report`, `validated`,
`owns_checked`, `amended` — change other fields but never the status itself. `staffed`, `goal`,
`session_start`, `session_command` and `session_end` are the five kinds that carry no `task` at
all.

### `planned`
- Written by: the planner or lead, via `flywheel log --task <id> --kind planned --brief <path>`.
- Carries: `task`, `brief` (the brief file's path). `needs`/`owns` only land on the event through
  `flywheel log --json`; the everyday flag form has no `--needs`/`--owns` flag, so in practice
  those live in the brief file's own header and are read from there (`ParseBriefHeader`) whenever a
  command needs them.
- Effect: `Derive` sets status `planned`. Verify's T1 (`plannedBriefOnly`) uses the task's *latest*
  `planned` event's brief path, deliberately ignoring any `amended` events, as the hash a fresh
  dispatch must match.

### `dispatched`
- Written by: the CLI only, via `flywheel run <task>` — never by hand.
- Carries: `task`, `attempt` (`r1`, `r2`, ... for a fresh run; `c1`, `c2`, ... for a correction),
  `adapter`, `model`, `path` (the run file), `sha256` (of the exact prompt sent), `brief` (for a
  correction attempt: the delta file's path), `baseline` (paths already dirty at dispatch, so a
  later owns check can excuse pre-existing dirt it didn't cause), `note`.
- Effect: `Derive` sets status `dispatched`, increments `Attempts`, and fixes this as the task's
  *current* attempt — every later `started`, `worker_plan`, `report`, `finished`, `validated`,
  `owns_checked` or `lost` event whose own `attempt` differs is stale and ignored (listed under
  `Stale` instead of changing anything). Verify's T1 (`ruleT1`) checks this event's `sha256`
  against the current brief (fresh attempts) or the delta file it names (`c*` attempts); an
  `amended` event between the dispatch and now excuses a fresh attempt's mismatch, but never a
  correction's.

### `started`
- Written by: the CLI, from the run's first parsed `start` observation.
- Carries: `task`, `session` (the emitted OpenCode session id). No `model` — that rides only on
  `dispatched` and `finished`.
- Effect: `Derive` sets status `running`. Verify's T4 (`ruleT4`) treats any session that ever wrote
  a task's `started`, `finished`, `dispatched`, `report` or `worker_plan` event as a **worker**
  session — one an `inspected` event must never reuse.

### `worker_plan`
- Written by: the CLI, the first time the run's text output has a line starting `PLAN `.
- Carries: `task`, `path` (`.flywheel/runs/<id>.<attempt>.plan.md`), `sha256` of that file.
- Effect: no status change. Its presence before step 20 is what a missing `no-plan` event
  certifies.

### `no-plan`
- Written by: the CLI, at most once per attempt, at the 20th completed step, only if no `PLAN
  `-prefixed line has appeared yet (issue #65).
- Carries: `task`, `attempt`.
- Effect: `Derive` ignores it for status exactly like `worker_plan`; it never changes `rc` or
  `reason` — it is a flag, not a verdict.

### `off-course`
- Written by: the CLI, at most once per attempt, the moment a `read`, `grep` or `glob` tool call
  (`offCourseTools`) names the 5th distinct path outside the worktree — library source a worker
  reached for instead of `go doc` (issue #72, #154).
- Carries: `task`, `attempt`, `note` (the offending paths, in the order first seen, comma-joined
  and capped at 200 characters by `clipNote`).
- Effect: `Derive` has no case for it, so — like `worker_plan` and `no-plan` — it never touches
  `Status`; it is a record for a human (or a future gauge) to notice, not a verdict.

### `finished`
- Written by: the CLI, exactly once per attempt, on every code path out of a run (clean stop,
  silent, stalled, provider error, output cap, start failure).
- Carries: `task`, `session`, `attempt`, `model` (every `finished` event carries the model, issue
  #134), `rc`, `reason` (`stop` clean; `length` output-capped; `error`; `start-failed`; `silent`;
  `stalled` — the run-file gap watchdog killed a run that had started but stopped producing lines
  for the worker's stall timeout, issue #158), `note`, `steps`, `tokens`, `cost`, `peak_reasoning`
  (the largest single-step reasoning figure seen in the run, omitted from the line when 0, issue
  #156), `sha256` (of the whole run file).
- `reason` is the provider's own finish reason, passed through verbatim by the adapter rather than
  normalized by flywheel; `stop` is the only clean value. Other values seen in practice: `length`,
  `error`, `start-failed`, `silent`, `stalled` (above) and `unknown` — unknown meaning the provider
  reported no reason the adapter recognised, which is information, not a bug (issue #176).
- Effect: `Derive` sets status `finished`. `stageOf` (`factory.go`) then reads `reason`: `stop` (or
  empty) is stage `finished`; `length` is stage **cut-off**; anything else, `stalled` included, is
  stage **failed** — both cut-off and failed units reach the andon and `flywheel status`'s
  Attention list (issue #131). `peak_reasoning` changes no stage: the factory floor (`render.go`)
  prints it next to a **capped** unit's state, and a `length` finish's own progress line names it
  in the hint suggesting smaller steps (`run.go`).

### `report`
- Written by: the CLI, only when the attempt's `reason` is `stop` and its last text was non-empty.
- Carries: `task`, `path` (`.flywheel/runs/<id>.<attempt>.report.md`), `sha256`.
- Effect: no status change. Any other finish reason keeps the last reply as
  `.flywheel/runs/<id>.<attempt>.partial.md` on disk instead, and appends **no** `report` event —
  so a cut-off or failed run can never be mistaken for a done one.

### `reviewed`
- Written by: nothing in this codebase today. `flywheel log --task <id> --kind reviewed --verdict
  pass|correct|reject` remains valid input, and `Derive` still understands it.
- Carries: `task`, `verdict` (`pass`, `correct`, or `reject` — enforced by `Validate`).
- Effect: `Derive` maps `pass`→`passed`, `correct`→`needs-correction`, `reject`→`rejected`. Unlike
  `inspected`, verify's T8 does not restrict who may write a `reviewed` event — `inspected` (§4) is
  the path every current command actually takes.

### `blocked`
- Written by: the controller (`flywheel controller`), when a task's `needs:` target is scrapped.
- Carries: `task`, `reason` (names the needs target).
- Effect: `Derive` sets status `blocked`.

### `lost`
- Written by: the controller, when an attempt's lease has expired.
- Carries: `task`, `attempt`, `reason` (`lease-expired`), `note` (the lease evidence).
- Effect: a stale-kind event, ignored unless its `attempt` matches the task's current one;
  otherwise `Derive` sets status `lost`.

### `landed`
- Written by: the CLI only, via `flywheel land <task> --commit <sha>`.
- Carries: `task`, `commit`, `note`.
- Effect: `Derive` sets status `landed`. Verify's T5 (`ruleT5`) requires an earlier `inspected pass`
  for the task; `LandTask` itself refuses **live** (exit 6) unless the task's derived status is
  already `passed`, and refuses to re-land the same task under a different commit than it already
  recorded.

### `amended`
- Written by: the planner or lead, via `flywheel log --task <id> --kind amended --brief <path>`.
- Carries: `task`, `brief`, optionally `needs`/`owns` (same `--json`-only caveat as `planned`).
- Effect: `Derive` updates only `brief`/`needs`/`owns` on the task, never its status. Verify's T1
  treats a `dispatched` hash mismatch as explained when an `amended` event for the task falls
  between that dispatch and now.

### `validated`
- Written by: the CLI only, via `flywheel validate <task>`, once per declared `gate:` line.
- Carries: `task`, `attempt`, `gate` (1-based index, as a string), `command`, `tree` (git tree
  hash), `rc`, `duration_ms`, `sha256` (of the gate's combined output), `path`
  (`.flywheel/evidence/<task>/<attempt>/gate-<n>.log`), `persona` (always `"supervisor"`,
  hardcoded — see §4), `reason`/`note` (`host-blocked` when Windows Smart App Control blocked the
  freshly built binary twice in a row).
- Effect: no status change. `Validate` requires `gate` and `tree` to be non-empty. A `host-blocked`
  reading never counts as passing for T3, matching `ValidateTask`'s own `GatesOK=false` for it.
- A gate that fails for a reason attributable entirely to a changed path outside the unit's own
  `owns:` — another unit's half-written file in the same tree, not this unit's own work — is
  recorded with `reason` `inconclusive` and a `note` of `blocked by <paths>` (issue #162). T3 still
  requires a *passing* reading for every declared gate: an `inconclusive` reading is not a pass, and
  `flywheel validate` still exits 5 for it, exactly like an ordinary failure.

### `owns_checked`
- Written by: the CLI only, via `flywheel validate <task>`, once per pass.
- Carries: `task`, `attempt`, `tree`, `outside` (changed paths not covered by `owns:`), `baselined`
  (changed paths excused because they were already dirty at dispatch and are byte-identical now),
  `attributed` (changed paths blamed on another in-flight task instead — see below), `persona`
  (`"supervisor"`).
- Effect: no status change. T3 requires an `owns_checked` with an empty `outside` on the same tree.
- A changed path outside `owns:` and not baselined is **attributed** rather than outside when some
  other task's brief `owns:` it (`ownsContains`, the matching `flywheel validate` already uses) and
  that task is currently in flight (`Derive` status `dispatched`, `running`, or `finished` — never
  `landed`, `passed`, or `rejected`): a neighbour's own work in progress on a shared checkout, not
  this task's stray file (issue #117). Attribution never excuses a path this task's own `owns:`
  already covers — such a path was never outside to begin with — and never hides a path no in-flight
  task owns: that path is still `outside`, and T3 still fails it. `flywheel validate` prints
  attributed paths as `<task> owns: attributed <path> -> <task>[, ...]` before the outside line.

### `inspected`
- Written by: the CLI only, via `flywheel inspect <task> --verdict ... --session ...`.
- Carries: `task`, `verdict` (`pass`, `rework`, `scrap`, or `escalate`), `tree`, `session`, `note`,
  `persona` (always `"inspector"`, hardcoded by `InspectTask` — see §4 for the only way a `"lead"`
  ever appears there).
- Effect: `Derive` maps `pass`→`passed`, `rework`→`needs-correction`, `scrap`→`rejected`,
  `escalate`→`blocked`. `InspectTask` enforces T4, and for a `pass` verdict T3 too, **before** the
  event is even appended — a refused inspection never reaches the log at all.

### `staffed`
- Written by: `flywheel staff --role <role> --session <session> [--model M]`.
- Carries: `session` (required — floor-level kinds are the ones allowed an empty `task`), `persona`
  (defaults to `"lead"` when not given).
- Effect: task-less; `Derive` skips it outright (`if e.Task == "" { continue }`). It only feeds
  `flywheel factory`'s floor view.

### `session_start`
- Written by: `flywheel log --kind session_start --session <id>`, invoked by the Claude Code hook
  or the OpenCode plugin (`flywheel-session.mjs`) that `flywheel init --hooks` installs, on a
  session's first turn (issue #157).
- Carries: `session` (required, like `staffed`) and no `task`.
- Effect: floor-level; `Derive` skips it outright, the same way it skips `staffed`. `flywheel trace
  <session>` is its only reader.

### `session_command`
- Written by: the same hook or plugin, once for every `flywheel`-prefixed command the session runs.
- Carries: `session` and `note` (the command line) — both required; `Validate` rejects the event if
  either is empty.
- Effect: floor-level like `session_start`. `flywheel trace <session>` prints its `note` in the
  trace line's detail column.

### `session_end`
- Written by: the same hook or plugin, when the session ends.
- Carries: `session` (required) and no `task`.
- Effect: floor-level like `session_start`.

### `goal`
- Written by: `flywheel goal add`/`flywheel goal set`.
- Carries: `goal` (a `GoalSpec`: `id`, `title`, `acceptance`, `required` tasks, `status` — one of
  `active`, `met`, `failed`, `abandoned`, checked by `Validate`).
- Effect: task-less, like `staffed`.

## 2. Transitions the code enforces

`flywheel verify` runs five rules against every task it is asked about — `VerifyTasks` calls
`ruleT1`, `ruleT3`, `ruleT4`, `ruleT5`, `ruleT8` in that order — and the same rules are enforced
**live**, before the record is written, inside `InspectTask` (T3, T4, T8) and `LandTask` (T5).
`ValidateTask` produces the readings T3 needs but enforces nothing itself; it can fail its own
gates (exit 5) without touching the log's legality.

- **T1 — dispatch matches its brief.** A fresh (`r*`) `dispatched` event's `sha256` must match the
  brief named by the task's latest `planned` event, unless an `amended` event for the task landed
  in between — then the mismatch is explained, not flagged. A correction (`c*`) `dispatched` event
  hashes its own delta file instead (the path in its own `brief` field); no `amended` event ever
  excuses a tampered or missing delta. `verify` reports this per-task as rule `T1`.
- **T3 — a pass needs current readings, not old ones.** `inspected pass` (or a live `--verdict
  pass` inspection) needs, for the *same git tree hash* the unit actually built: a passing
  `validated` event from the supervisor for **every** gate the current attempt's prompt declares,
  and a clean (`outside`-empty) `owns_checked` — both recorded after the latest `finished` event
  that precedes the check. "The current attempt's prompt" is `AttemptBrief`'s result (issue #133):
  the base brief plus, when the current attempt dispatched a different file (a correction delta),
  that file's `gate:` lines replacing the base's and its `owns:` unioned with the base's — an
  `amended` event replaces which brief counts as the base outright. `owns:` entries are matched as
  a literal path, a `dir/` prefix, or a `path.Match` shell pattern, all three checked the same way
  (issue #135, `ownsContains`). Each inspection uses its own window, so a later correction attempt
  never invalidates an earlier legitimate pass.
- **T4 — no self-inspection.** An `inspected` event's `session` must never be a session that wrote
  that task's `started`, `finished`, `dispatched`, `report`, or `worker_plan` event. `InspectTask`
  checks this **before** T3, so a worker-session inspection is refused as T4 even when its readings
  are also missing.
- **T5 — no landing without a pass.** A `landed` event needs an earlier `inspected pass` for the
  same task. `LandTask` additionally refuses to land a task whose derived status is not `passed`,
  and refuses a second `landed` event for the same task under a different commit than the one
  already recorded (the same commit is a silent no-op, exit 0).
- **T8 — personas write only their own kinds.** `validated` and `owns_checked` must carry `persona
  "supervisor"`; `inspected` must carry `"inspector"` or `"lead"`. No other kind is persona-checked
  by this rule (§4 has the full picture, including what is and is not mechanically enforced).

## 3. Designed, not enforced

`docs/design/autonomous-shipping.md` describes ten transition rules, T1-T10, and a fuller event
vocabulary (`audited`, `signal`, `dismissed`, `learning`, `allow_untriaged`, "by" attribution
blocks). Only T1, T3, T4, T5 and T8 exist in `verify.go`, and only the twenty-one kinds in
`events.go` exist at all — `Validate` rejects any other kind by name, so an event carrying
`audited` or `signal` today is simply a validation error, not a recognized-but-unchecked record.
Concretely, still design-only:

- **T2** (a step-20 `worker_plan` or a signal) — the `no-plan` half landed (§1); nothing reads a
  `no-plan` event as a rule violation today, and there is no signal kind for it to raise.
- **T6** (sensitive domains need the lead's sign-off and an audit before landing) — nothing detects
  a "sensitive domain," and no command asks for a sign-off.
- **T7** (a wave's first article needs `audited conforms` before the rest lands; an open
  nonconformance stops its kind of task) — there is no `audited` kind, no auditor command, and
  nothing gates landing on it.
- **T9** (checkpoint/land/handoff refuse while signals are untriaged, unless `allow_untriaged`) —
  there is no `signal`, `dismissed`, or `allow_untriaged` kind, and `flywheel land`/`flywheel
  handoff` do not check for one.
- **T10** (the log is append-only with a hash chain per shard) — the log is append-only in
  practice (`AppendEvent` only ever opens with `O_APPEND`, and `ParseEvents` treats an unresolved
  git conflict marker as a hard error), but there is no hash chain: nothing computes or checks a
  per-record or per-shard digest, so a hand-edited historical line is not detected as tampering by
  this rule (T1 still catches a brief that no longer matches its recorded hash, which is a
  different check).

Until these land, the factory-role table, the andon cord, sampling, and nonconformance handling in
`docs/design/autonomous-shipping.md` and `skills/flywheel/references/factory.md` describe intent
and skill-level convention, not something `flywheel verify` can fail on.

## 4. Who may write which event

Verify's T8 is the only persona check in the code, and it covers exactly three kinds:

- `validated` / `owns_checked` → persona must be `"supervisor"`. `flywheel validate` hardcodes this
  on every event it writes (`gauges.go`), regardless of which session or identity ran the command —
  `ValidateTask` takes no session argument and the event carries none, so nothing ties a reading to
  who typed it. "A worker never runs the gauges" is a skill-level convention here, not a mechanical
  block: the OpenCode worker permission policy
  (`skills/flywheel/references/worker-permissions.json`) denies only tree-rewriting git commands,
  not `flywheel validate`.
- `inspected` → persona must be `"inspector"` or `"lead"`. `flywheel inspect` (`InspectTask`)
  always writes `"inspector"`; the only way an `inspected` event ever carries `"lead"` is a
  hand-crafted `flywheel log --json` line with an explicit `"persona":"lead"` field — the ordinary
  flag form of `flywheel log` has no `--persona` flag at all.
- Every other kind (`planned`, `dispatched`, `started`, `worker_plan`, `no-plan`, `finished`,
  `report`, `reviewed`, `blocked`, `lost`, `landed`, `amended`, `staffed`, `goal`) carries no
  persona restriction in `ruleT8`. In practice most of them are written only by a specific CLI
  command (`dispatched`/`started`/`worker_plan`/`no-plan`/`finished`/`report` only by `flywheel
  run`; `landed` only by `flywheel land`; `blocked`/`lost` only by `flywheel controller`), which is
  what keeps them honest — not a persona field.

The one place "a worker never inspects its own work" is a real, live check rather than a skill
convention is T4: `InspectTask` and `ruleT4` both refuse an `inspected` event whose `--session` was
ever a worker session (one that wrote that task's `started`, `finished`, `dispatched`, `report`, or
`worker_plan` event). Put plainly, in this codebase: **a worker never writes a `validated`,
`owns_checked`, `inspected`, or `landed` event** because no worker-facing tool writes them and the
worker's own permission policy does not need to block commands it is never given; **the supervisor
(the gauges) never writes an `inspected` event** because `flywheel validate` has no verdict to
record; and **the inspector never writes a `validated` event** because `flywheel inspect` never
touches gate output — only `flywheel validate` does, and it always signs its own readings
`"supervisor"`, never `"inspector"`.

## 5. `flywheel verify` and exit codes

`flywheel verify [<task>...|--all] [--json]` runs T1/T3/T4/T5/T8 for the requested tasks (`--all`
derives the task list from every `task` seen in the log) and prints one `PASS`/`FAIL` line per
rule per task, or the same result as JSON (`{"passed": bool, "items": [{"task","rule","pass",
"reason"}]}`). Naming a task explicitly still runs every rule for it even if the log has never
heard of it — a missing planned brief, for instance, fails T3 by name rather than being skipped.
An empty log verified with `--all` passes vacuously; verifying with no tasks and no `--all` is a
usage error.

Exit codes follow the repo-wide convention from `AGENTS.md`: 0 ok, 1 error, 2 usage, 5 gauges
failed, 6 rule refusal. The enforcing commands:

| Command | Success (0) | Refusal | Other |
| --- | --- | --- | --- |
| `flywheel validate <task>` | every gate passed, nothing outside `owns:` | **5** — a gate failed, stayed host-blocked after one rerun, or a changed path is outside `owns:` | 2 usage, 1 other error |
| `flywheel inspect <task> --verdict ... --session ...` | inspection recorded | **6** — `RuleRefusal` naming T3, T4, or T8 and its fix | 2 usage, 1 other error |
| `flywheel verify [...] [--json]` | every requested check passes | **6** — any check fails (`FAIL <task> <rule>: <reason>`) | 2 usage, 1 other error |
| `flywheel land <task> --commit <sha>` | landing recorded, or repeats an already-landed commit | **6** — `RuleRefusal` naming T5 | 2 usage, 1 other error |
| `flywheel run <task>` | `rc == 0` and finish `reason` was `stop` | — | **3** silent (no output within the start timeout); **7** stalled (the run-file gap watchdog fired mid-stream, issue #158); **4** any other outcome (nonzero `rc`, or `reason` `length`/`error`/`start-failed`); 2 usage or no worker configured; 1 other error |

`flywheel run`'s own three codes (3, 4, 7) are not in `AGENTS.md`'s repo-wide list above — they are
`ExitCode`'s reading of one `Result`, keyed by exit number instead of by command:

| Exit | `flywheel run` reason |
| --- | --- |
| 3 | `silent` — no stdout line arrived within the start timeout |
| 4 | any other non-clean outcome — nonzero `rc`, or finish `reason` `length`, `error`, or `start-failed` |
| 7 | `stalled` — the run had started but the run file stopped growing for the stall timeout (issue #158) |

Everything upstream of these five commands — writing a brief, deciding what belongs in `owns:`,
choosing which task to dispatch next — is judgment the protocol does not check; the CLI enforces
only what is above.
