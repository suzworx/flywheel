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
a read-only projection of it. A command whose `--dir` is inside a unit's worktree
(`<root>/.flywheel/worktrees/<task>`, made by `flywheel run --worktree`) uses the main checkout's
ledger at `<root>`, never the worktree's stale copy (issue #395).

Every skill in `skills/` that drives this loop cites `protocol v1` and links back here;
`cmd/flywheel/docs_test.go` fails the build the moment a skill stops citing it, or this file's
first line stops matching `^# flywheel protocol v`.

## 1. Required entries per task

Twenty-two event kinds exist; `events.go`'s `kinds` map is the authority for the list, and
`Validate` rejects anything else. Nine of them carry a task's status (`state.go`'s `kindRank`
orders them for replay); the rest — `worker_plan`, `no-plan`, `off-course`, `report`, `validated`,
`owns_checked`, `amended`, `sharded` — change other fields but never the status itself. `staffed`, `goal`,
`session_start`, `session_command` and `session_end` are the five kinds that carry no `task` at
all.

### `planned`
- Written by: the planner or lead, via `flywheel log --task <id> --kind planned --brief <path> [--session S --model M] [--goal G] [--note TEXT]`.
- Carries: `task`, `brief` (the brief file's path), `header` (the parsed brief header — owns,
  needs, needs-state, gates, live-gates, exclusive, review and sha256 — as recorded when the
  event was appended), `persona` (`planner`), `session` and `model` (the planner's identity, from
  `--session`/`--model`), `goal_id` (from `--goal`; an unknown goal is refused with exit 1 and
  nothing is appended), and `note`. `owns`/`needs` are copied from the brief header when the event
  is appended. When `header` is present it is authoritative over the brief file, and `owns`/`needs`
  are its summary.
- Effect: `Derive` sets status `planned`. Verify's T1 (`plannedBriefOnly`) uses the task's *latest*
  `planned` event's brief path, deliberately ignoring any `amended` events, as the hash a fresh
  dispatch must match. Acceptance criteria belong to the goal (`flywheel goal add --accept CMD`),
  not to a unit: a planned event links to its goal with `goal_id`. Re-planning an id that already
  has `dispatched` attempts starts a new plan: `flywheel log` warns on stderr (exit 0; `--replan`
  silences it, and is a usage error with any other kind), and `Derive` resets the row's session,
  attempt, rc, reason, verdict, model and stale list so the floor shows a clean `planned` row. The
  attempt count and every earlier event stay, so the next `flywheel run` numbers after the old
  attempts. Branches are shared by every worktree of a repository, so when branch `fw/<task>`
  already exists `flywheel log` also warns that another flywheel root may own the id (issue #479):
  `warning: branch fw/<id> already exists (checked out in <path>); another root may own this id
  (--replan silences this)`, the parenthesised path only when a worktree other than this root's own
  task worktree has it checked out. It runs read-only git only, and none outside a repository.
  Take a duplicate plan back with a `withdrawn` event. `flywheel run --worktree` on such an id
  fails naming that worktree.

### `dispatched`
- Written by: the CLI only, via `flywheel run <task>` — never by hand.
- Carries: `task`, `attempt` (`r1`, `r2`, ... for a fresh run; `c1`, `c2`, ... for a correction),
  `adapter`, `model`, `path` (the run file), `sha256` (of the exact prompt sent), `brief` (for a
  correction attempt: `.flywheel/briefs/<task>.<attempt>.delta.txt`, the per-attempt snapshot of
  the delta taken atomically at dispatch; the operator's `--delta` file is left untouched and may be
  edited or reused for the next correction without breaking this one's T1, issue #452; a failed
  snapshot fails the dispatch), `header` (the parsed brief header of the exact
  prompt dispatched — the planned brief on a fresh attempt, the delta on a correction —
  authoritative over the file it names), `baseline` (paths already dirty at dispatch, so a
  later owns check can excuse pre-existing dirt it didn't cause), `base` (the commit HEAD pointed at in the worker's tree when the attempt was dispatched, so the owns check can count changes the unit's own commits since then, issue #332), `increment` (N when
  `flywheel run --increment N` sent only increment N of the brief as a fresh session; the
  attempt is an ordinary `r<n>`; 0 or omitted means the whole brief; `Validate` accepts it only on a `dispatched` event and only >= 1, and `flywheel run` refuses (exit 6, rule `increment`) a brief that defines no increment N — an "Increments" section with item N, or an "Increment N" heading), `note`.
- Effect: `Derive` sets status `dispatched`, increments `Attempts`, and fixes this as the task's
  *current* attempt — every later `started`, `worker_plan`, `report`, `finished`, `validated`,
  `owns_checked` or `lost` event whose own `attempt` differs is stale and ignored (listed under
  `Stale` instead of changing anything). Verify's T1 (`ruleT1`) checks this event's `sha256`
  against the current brief (fresh attempts) or the delta file it names (`c*` attempts); an
  `amended` event between the dispatch and now excuses a fresh attempt's mismatch, but never a
  correction's.

### `worktree_setup`
- Written by: the CLI only, via `flywheel run <task> --worktree` (issue #430), after the task's
  worktree (`.flywheel/worktrees/<task>`) exists and before `dispatched`, on every dispatch that has
  something to prepare: the effective brief's `needs-state: <path> (link)` entries, each linked from
  the repo into the worktree (a directory junction on Windows, a symlink elsewhere; a path already
  present is left alone), then the `worktree.setup` command, run with bash (the gates' shell) in the
  worktree with `FLYWHEEL_TASK`, `FLYWHEEL_WORKTREE` and `FLYWHEEL_ROOT` set and killed at
  `worktree.setup_timeout` (default `10m`). Setup runs on every dispatch, so it must be idempotent.
  A relative script path (the first word, or the second after `node`/`python`/`bash`/`sh`/`pwsh`)
  missing from the worktree but present in the root resolves against the root; prefer
  `"$FLYWHEEL_ROOT/<script>"`. On Windows gates and setup run in Git for Windows' bash, never the WSL launcher.
- Carries: `task`, `attempt` (the attempt being dispatched), `linked` (the linked paths), `command`,
  `rc`, `duration_ms`, `note` (the last 20 lines of output, or the link error), `escaped` (issue
  #460: the entries of a linked path, one level deep plus one level inside each `@` scope, that are
  links or junctions resolving into the main checkout outside the linked path itself and outside
  the worktree, e.g. a workspace's `node_modules/@acme/web -> packages/web`; the `.pnpm` store and
  broken links are not).
- Effect: no status change. A link error (a `(link)` path missing in the repo) or a setup that does
  not exit 0 refuses the dispatch (exit 6, rule `setup`, the fix naming the path or command and the
  output tail): no `dispatched` event is recorded and no worker starts. A non-empty `escaped` prints
  `warning: needs-state link <path> holds links into the main checkout (<n>: ...)` on the dispatch's
  stderr, since the unit's gates would import the main checkout's copies (use `worktree.setup` with
  an offline install instead); with `worktree.strict_links` true (default `false`) it also refuses
  the dispatch (rule `setup`, the event still recorded with `escaped` and a `note` saying it was
  refused, and setup does not run).

### `started`
- Written by: the CLI, from the run's first parsed `start` observation.
- Carries: `task`, `session` (the emitted OpenCode session id). No `model` — that rides only on
  `dispatched` and `finished`.
- Effect: `Derive` sets status `running`. Verify's T4 (`ruleT4`) treats any session that ever wrote
  a task's `started`, `finished`, `dispatched`, `report` or `worker_plan` event as a **worker**
  session — one an `inspected` event must never reuse.

### `worker_plan`
- Written by: the CLI, the first time the run's text output has a line that, after stripping leading whitespace, list/quote markers (-, *, +, >, #) and markdown emphasis (*, _, `), starts with `PLAN ` (issue #284).
- Carries: `task`, `path` (`.flywheel/runs/<id>.<attempt>.plan.md`), `sha256` of that file. The recorded plan is the text from that matched line onward, with leading whitespace and list markers stripped but emphasis markers preserved.
- Effect: no status change. Its presence before step 20 is what a missing `no-plan` event
  certifies.

### `no-plan`
- Written by: the CLI, at most once per attempt, at the 20th completed step, only if no `PLAN
  `-prefixed line (as detected above) has appeared yet (issue #65, #284).
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
  #134), `rc`, `reason` (`stop` clean; `length` output-capped; `error`; `rate-limited` — the
  provider's rate or usage limit cut the run off (a claude 429 or limit message); the note names
  `limit resets <time>`, no signal is recorded, the breaker does not count it, and `flywheel run`
  resumes the same session after the reset (`limits.rate_limit_retries`,
  `limits.rate_limit_max_wait`), issue #380. A rate-limited finish whose reset parses also carries
  `reset_at` (RFC 3339): the limit belongs to the subscription, so until then the model is paused —
  `flywheel run` refuses a fresh attempt on it (exit 6, rule `rate-limit`), while a `--resume` waits
  for the reset plus a minute before dispatching (bounded by `limits.rate_limit_max_wait`; a wait
  beyond it is refused, exit 6, rule `rate-limit`, naming the reset), and a `--resume` with no delta file after a rate-limited or abandoned-job
  finish uses the automatic continue delta (the next unused `<task>.limit-<n>.txt`), issue #472,
  `flywheel next` HOLDs with `rate-limit: <model> paused until <time>`, and the floor shows the
  unit `rate-limited until HH:MM` and an andon entry `model/<model>` `paused until HH:MM`; a later
  clean `stop` finish on the model ends the pause early, issue #383. A claude stream's
  `rate_limit_event` lines carry the exact reset epoch and the share of the window used: a
  rate-limited finish takes `reset_at` from the latest one (else the parsed message), and EVERY
  finish that saw one carries `limit_utilization` (0..1), `limit_reset_at` (RFC 3339) and
  `limit_window` (e.g. `five_hour`). Pause before the wall: when a model's LATEST finish has
  `limit_utilization` at or above `limits.rate_limit_pause_at` (default 0.95; negative disables) and
  `limit_reset_at` is ahead, the model is paused until then exactly as for a hit limit, the refusal
  naming `(<n>% of the <window> window used)` and the andon `paused until HH:MM (<n>% used)`; a later
  finish below the threshold, or the reset passing, releases it, issue #417; `abandoned-job` — a clean
  stop that left a background shell it started (claude Bash `run_in_background`, whose
  tool_result reports `running in background with ID: <id>` or `agentId: <id>`) never collected
  — no later tool call's input names that id (`BashOutput`, `KillShell`, a `Read` of its output
  file, ...) — so the job died with the session; the note names
  `background job never collected: <cmd>`, no signal is recorded, the floor shows the unit
  `abandoned-job` on the andon, and `flywheel run` resumes the same session once, immediately,
  with `.flywheel/briefs/<task>.job-1.txt` telling the worker to run the job in the foreground; a
  second `abandoned-job` is returned as is, issue #390; `start-failed`; `silent`;
  `stalled` — the run-file gap watchdog killed a run that had started but stopped producing lines
  for the worker's stall timeout, issue #158), `note`, `steps`, `tokens`, `cost`, `peak_reasoning`
  (the largest single-step reasoning figure seen in the run, omitted from the line when 0, issue
  #156), `sha256` (of the whole run file), `wrote` (the attempt's distinct edit/write paths, sorted,
  at most 50, omitted when the attempt made no edits, issue #163; it also holds the worktree-relative
  paths the worker changed in the tree since dispatch — changed at finish and not dirty at dispatch,
  or with a sha that differs from the dispatch baseline, `.flywheel/` excluded — so MultiEdit,
  NotebookEdit and shell writes count, issue #463), `wrote_from_tree` (the subset of `wrote` that
  came only from the tree, not from a tool observation; omitted when empty, issue #463), `commands` (the shell commands
  the worker ran, in order, at most 100, each clipped to 300 characters; omitted when it ran none,
  issue #365), `gates_unrun` (on a `stop` finish only: the ids `1`, `2`, .. of the attempt's
  effective `gate:` lines that no recorded command contains, whitespace collapsed, or contains the
  gate's first 40 characters; live gates are not checked; omitted when empty, issue #365),
  `commit` (on a `stop` finish of a `--worktree` unit only, issue #391: workers never run git write
  commands, so flywheel commits the attempt itself on `fw/<task>` — built in a temporary index from
  HEAD plus every changed path inside the attempt's owns — the resolved brief's owns plus this
  dispatch's own prompt's `owns:` lines, so a correction delta that widens owns is committed
  (issue #477), the same set validate measures; checkpoints use it too — flywheel's own bookkeeping
  excluded, message `<task> <attempt>` with a `Flywheel-Task: <task>` trailer, author and committer
  `flywheel <flywheel@localhost>`, the branch moved by compare-and-swap, and the worktree's index
  refreshed for the committed paths; omitted when nothing owned changed. Changed paths outside owns
  stay uncommitted, are named on the note as `left uncommitted (outside owns): <paths>`, printed as
  `warning: <task> <attempt>: attempt commit left <n> changed path(s) uncommitted (outside owns):
  <paths>` and recorded as `uncommitted`; a commit failure never fails the run and is noted as
  `attempt commit failed: <err>`).
- `uncommitted` (issue #477): the changed paths the attempt commit left out. Only a `finished` event
  may carry it. While any of the task's latest `finished` event's `uncommitted` paths is still
  changed (dirty or untracked) in the tree validate measures, the owns check fails with the single
  outside entry `left uncommitted by the attempt commit: <paths>` (replacing those bare paths,
  and even when a baseline or claim would excuse them): a PR cut from `fw/<task>` would lack them.
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
- `classifyRun` (`factory.go`) reads `wrote` alongside `reason`: a done attempt with a non-`stop`
  reason and a non-empty `wrote` classifies run state **failed-dirty** instead of plain `failed` (or
  `capped`, when `reason` is `length`) — a failed attempt that left files behind, needing a human
  decision (revert, resume, or re-dispatch) that a clean failure or a cut-off run that wrote nothing
  does not (issue #163). `failed-dirty` reaches the andon and is dead, exactly as `failed` is.
- A `stop` finish is not automatically done (issue #364): when the claude result line carried
  `permission_denials`, `note` adds `permission denied: <tool [path], ...>` and a
  `permission-denied` signal follows the `finished` event (run state **blocked**). Otherwise, when
  `wrote` is empty, no signal is recorded (an untriaged signal blocks landing, and some units
  legitimately write nothing): the factory view derives the floor state **no-writes** from the
  `finished` event alone. Both states apply only while the unit is awaiting judgement (status
  `finished`); once passed, rejected or landed it shows done. Both reach the andon; no-writes
  blocks nothing.
- A non-empty `gates_unrun` adds `gates never run by the worker: <ids>` to `note` and prints a
  `<task> <attempt> never ran gate(s) <ids>` progress line; no signal is recorded (the lead
  re-measures every gate). `flywheel validate` prints `<task> note: the worker never ran gate(s)
  <ids> itself; its report's claims about them are unmeasured` after the gate lines (issue #365).
- Workers never write git (issue #423). The guarantee is two layers that do not depend on PATH:
  **denied at dispatch** — the claude adapter's default `--disallowedTools` denies `git` commit,
  push, stash, reset, checkout, rebase, merge, add, rm, mv, restore, update-index, apply, tag,
  branch (so `git branch --show-current` too; use `git rev-parse --abbrev-ref HEAD`), switch,
  cherry-pick, revert, am, worktree, clean, notes, replace, update-ref and gc — and **detected after
  each attempt** — on every exit path, before flywheel's own attempt commit (so its index refresh is
  never charged to the worker), flywheel compares the worktree's HEAD, branch, stash, index (`git
  diff --cached --name-only`) and tags with the state captured at dispatch, and `note` names what
  changed: `HEAD: <old> -> <new>`, `stash`, `index: staged <paths>`, `index: unstaged <paths>`,
  `tags: +<name>/-<name>`. An index change, a local-only tag or a deleted tag is a `git-write`
  signal whatever the guard logged; a HEAD or stash move is one only when the git guard logged a
  worker write (issue #361; otherwise another process moved it and the note says so). Tags are
  shared by every worktree like the stash, so a tag added or moved onto a commit a remote-tracking
  ref contains (a fetch; the note marks it `(on a remote-tracking commit)`) is a shared-ref change
  and needs guard evidence too (issue #442). Paths the worker staged are unstaged by flywheel
  (`git reset -q -- <paths>`, content kept in the working tree) and the note adds `index restored:
  <paths>`. The PATH git guard is defence in depth, not the guarantee: a shell that puts the real
  `git` first on PATH never reaches it.
- `checkpoint` (issue #422): on an unclean finish (`error`, `rate-limited`, `stalled`, `silent`,
  `abandoned-job`, `length`) of an attempt that wrote files, the sha of the snapshot of its changed
  owned paths at `refs/flywheel/checkpoints/<task>/<attempt>` (see `recovered` below). Only a
  `finished` event may carry it.
- Workers load no MCP servers unless the worker config lists them (issue #425). Every claude
  dispatch passes `--strict-mcp-config` with `--mcp-config` set to the worker's `mcp` value (the
  Claude CLI's `{"mcpServers": {...}}` shape, compacted), or to the empty set `{"mcpServers":{}}`
  when `mcp` is unset — so the user-level MCP servers (mail, calendar, drive, trackers, chat,
  plugins) that `--setting-sources user` would still load never reach a worker. The review agent
  sets no `mcp` and gets the empty set. `flywheel` rejects a config whose `mcp` is not a JSON
  object with an `mcpServers` object.

### `report`
- Written by: the CLI, only when the attempt's `reason` is `stop` and its last text was non-empty.
- Carries: `task`, `path` (`.flywheel/runs/<id>.<attempt>.report.md`), `sha256`.
- Effect: no status change. Any other finish reason keeps the last reply as
  `.flywheel/runs/<id>.<attempt>.partial.md` on disk instead, and appends **no** `report` event —
  so a cut-off or failed run can never be mistaken for a done one.

### `reviewed`
- Written by: `flywheel review <task> --verdict pass|correct|reject --session S [--model M]`, from
  an isolated copy of the tree; by the review agent, `flywheel review <task> --agent --session S
  [--worker NAME] [--round N]` (issue #389); `flywheel log --kind reviewed` remains valid input.
- Carries: `task`, `verdict` (`pass`, `correct`, or `reject` — enforced by `Validate`), `session`,
  `model` (the reviewer's identity), `tree`, `note`, `persona` (`reviewer`). The review agent's
  event also carries `adapter` (the agent that reviewed; a verdict passed in by hand names none, and
  the next agent round is one more than the task's `reviewed` events that do), verdict `correct`
  when any finding is a blocker or major and `pass` otherwise, and the note
  `<n> finding(s): <b> blocker, <m> major, <k> minor`.
- Effect: `Derive` maps `pass`→`passed`, `correct`→`needs-correction`, `reject`→`rejected`, except
  that a `reviewed` event written by the review agent (persona `reviewer` with an `adapter`) never
  sets `passed` — its `pass` leaves the status unchanged, because the agent reads and the gauges
  measure: only validate+inspect (or a hand-recorded review, which re-runs the gates) pass a unit
  (issue #389). Unlike
  `inspected`, verify's T8 does not restrict who may write a `reviewed` event — `inspected` (§4) is
  the path every current command actually takes.

### `review_finding`
- Written by: the review agent only, `flywheel review <task> --agent --session S` (issue #389). The
  agent (the staffing `reviewer` role's adapter and model, else the default worker, or `--worker`)
  runs in the unit's worktree under the git guard with a read-only tool policy (claude: `Read`,
  `Grep`, `Glob`, `git diff/log/show`, `go vet/test`; `Edit`, `Write` and `NotebookEdit` refused),
  on a prompt holding its instructions, the unit's effective brief, the attempt's gate readings and
  the diff from the dispatch base (capped at 200 KB), kept at `.flywheel/reviews/<task>.<round>.prompt.md`
  beside its stream `.flywheel/reviews/<task>.<round>.jsonl`. The findings contract is checked,
  not trusted: an answer whose file does not exist (and is not a changed path), whose `line` is not
  0 or a real line, with an empty claim or scenario, or with more than 3 nits is refused, and the
  agent runs once more, fresh, into `<task>.<round>b.jsonl`; a second refusal records nothing and
  `flywheel review --agent` exits 1 naming both transcripts. `flywheel review calibrate --cases FILE
  --session S` runs the same agent over a sample of past PR states in temporary worktrees and synthetic
  ledgers (never this ledger) and reports its recall against the case file (docs/calibration/README.md).
- Carries: `task`, `attempt` (the unit's latest), `session` (the reviewer, never a worker session
  of the task: refused T4), `model`, `tree`, `severity` (`blocker`, `major`, `minor` or `nit`),
  `category`, `title` (the claim), `observed` (the failure scenario), `ask` (the fix hint), `path`
  (the file), `line_no` (the file line; `line` is already the product line), and `finding`, a stable
  id `<task>-r<round>-<n>`. `Validate` requires the task, session, a severity in the set, the title
  and the path.
- Effect: no status change. Every finding and the round's closing `reviewed` event go out in one
  `AppendEvents` write; an answer without a parsable `{"findings": [...]}` block records nothing.
- Open and closed (`OpenFindings`, issue #389): a finding opens when it is raised. It closes only on
  the framework's evidence, never on an agent's word: a LATER agent review round (a `reviewed`
  event with persona `reviewer` and an adapter) completes without re-reporting it — same `path`
  and the same claim, case- and space-insensitive; a re-report is still open under its new id —
  or the lead dismisses it (a `finding_response` below). A worker's `fixed` never closes a finding
  by itself. `blocker` and `major` findings block the unit.

### `finding_response`
- Written by: `flywheel review <task> --agent --fix` (`ReviewLoop`, issue #389) after each
  correction, one per open blocking finding it sent — the worker's answer parsed from the
  attempt's report (`.flywheel/runs/<task>.<attempt>.report.md`), under the worker's session; or,
  for an id the report does not answer, `disputed` with note `missing: the worker gave no answer`,
  so the next round re-reviews it anyway. Responses are recorded only for the findings sent: a
  finding outside the unit's effective owns is never sent and never gets one (issue #458). And by the lead, `flywheel review <task> --dismiss <id>
  --session S --note WHY`: `disputed` with note `dismissed: WHY`; a worker session of the task is
  refused (T4, exit 6), and the id must be a finding the task's reviewer raised.
- Carries: `task`, `attempt` (the correction), `session`, `finding` (the id it answers), `verdict`
  `fixed` or `disputed`, and `note` (the worker's evidence or reason). `Validate` requires the
  task, the finding and a verdict in the set.
- Effect: no status change. Only a lead dismissal — `disputed`, note starting `dismissed:`, from a
  non-worker session — closes a finding, and with it any later re-report of the same file and claim.
- The loop: review; no open blocking finding → verdict `pass` (exit 0). Else flywheel writes the
  findings delta `.flywheel/briefs/<task>.review-<round>.txt` — the effective brief's `owns:`,
  `needs:` and every `gate:` line, `# TASK: fix the review findings`, one block per open blocking
  finding (`FINDING <id> [<severity>] <file>:<line> — <claim>`, `scenario: …`, `fix hint: …`), then
  the contract: fix each finding, change nothing unrelated, re-run every gate, and end the report
  with one line per finding, `FINDING <id>: fixed <evidence>` or `FINDING <id>: disputed <reason>`
  — resumes the worker's session on it, records the answers and reviews again. After `--rounds`
  reviews (default 3) the open blocking findings are printed and the command exits 1.
- The thread (issue #389): `.flywheel/reviews/<task>.md` is GENERATED from the event log
  (`RenderReviewThread`) after every agent review round, every recorded `finding_response` and
  every dismissal — never on GitHub. It holds one `## Round <n> — <verdict>, <reviewer session>,
  <model>, tree <sha7>` section per round listing its findings, each with its current status (`open`,
  `closed in round <m>`, `re-reported in round <m> as <id>`, or `dismissed by <session>: <note>`)
  and the answers under it (`<session> <attempt>: fixed|disputed — <note>`), then `## Open blocking
  findings`. It carries a `<!-- generated by flywheel from the event log; do not edit -->` marker; a
  file there without it is never overwritten, and a failed write is only a progress warning.
- Enforced: `flywheel inspect --verdict pass` is refused (rule `review`, exit 6) while the task has
  an open blocking finding, and `flywheel verify` fails rule R1 for an `inspected` pass recorded
  while one was open (§2). `flywheel floor` shows such a unit as `review-open (<count>)` on the
  andon, and `flywheel stats` reports a review block.
- The review panel (issue #420): `flywheel review <task> --agent --panel --session S [--round N]
  [--fix [--rounds N] [--fix-worker NAME] [--worktree]]` runs the review agent once per member of
  `review.panel`, sequentially, each a persona owning one dimension. The personas are embedded
  (`internal/flywheel/review_personas/<dimension>.md`): `correctness`, `security`, `tests`, `errors`
  (error handling and resources), `cross-os`, `contract` (flags, event kinds, exit codes, config keys,
  backwards compatibility) and `docs`. A member's prompt is `review_prompt.md` plus its persona file,
  kept at `.flywheel/reviews/<task>.<round>.<dimension>.prompt.md`. Every finding in its answer must
  carry its dimension as `category`; one outside it refuses the answer (the same refuse-and-retry-once
  path). All members of one run share a round; their findings are `<task>-r<round>-<dimension>-<n>`.
  A member's `reviewed` event keeps persona `reviewer` (so status derivation and every agent-review
  check are unchanged) and records its dimension in `category`; `Validate` refuses a `reviewed`
  category that is not a persona, and accepts a persona `reviewer:<dimension>` only for a known one.
  A member's round closes only its own dimension's findings; a general round closes all. Rounds are
  counted with a panel run as one round until a dimension repeats. With `--fix` each loop round is a
  whole panel. The command prints the findings (or, with `--fix`, the open blocking ones), then the
  verdict matrix, and exits 0 when every dimension is `pass`, else 1 (6 on a refusal, 2 on usage).
- The verdict matrix (`VerdictMatrix`): per panel dimension, the verdict of the latest agent `reviewed`
  event of that dimension on the tree — `pass` or `correct` — else `missing`; a `correct` whose
  blocking findings are all dismissed counts as `pass`. The thread groups a panel round as one
  `## Round <n> — review panel` section with one line per member and its findings under a
  `### <dimension>` heading, then `## Panel verdict matrix — tree <sha7>`. When `review.panel` is
  configured, the floor shows each unit's matrix after its run state as `panel <cells>`, one cell per
  dimension in panel order on the unit's measured tree (its latest `owns_checked` or `validated`
  reading): `✓` pass, `✗` correct (an open blocking finding), `·` not reviewed on that tree. The
  cells' room comes out of MODEL, then SESSION; when they cannot give it, the cells are dropped and
  the task id is never cut. `--json` carries them as a unit's `panel`.
- Numbers: `flywheel stats` breaks the review down per persona (`review.by_persona`: persona, level
  `unit` or `group`, reviews, findings, `by_severity`, and how the findings were answered — `fixed`
  or `disputed` by the worker's latest `finding_response`, `dismissed` by a lead) and per level
  (`review.by_level`: `unit`, `group`, and `release` when a release audit ran — rounds, rounds not
  pass, findings, blocking; a release audit's findings are its failed checks). A finding counts
  for a panel persona only when its id is `<task>-r<n>-<dimension>-<i>`; others are `general`.
  `flywheel review calibrate --panel [dims]` (bare: the configured `review.panel`) calibrates each
  persona over the same sampled PR states (same seed) and reports, per persona, the PR states it
  reviewed, hits, misses, extra findings and recall, then the panel, which hits a case when any
  persona hit it; the report's `**Total: ...**` line is the panel's. A persona whose review fails
  is skipped for that PR state alone.
- Config: `review.panel` — the members, `[{"persona": ..., "worker"|"adapter"+"model": ...}]`;
  `config get/set review.panel` reads and writes a comma-separated persona list (a member keeps its
  reviewer). Unset, the panel is `correctness, tests, errors, contract, docs`; `security` and `cross-os`
  are opt-in. `review.required` (default `false`) makes a complete panel a condition of every pass.
- Enforced: when the task has been reviewed by the panel, or `review.required` is set, `flywheel
  inspect --verdict pass` is refused (rule `panel`, exit 6) unless every configured dimension is
  `pass` on the tree being inspected, naming each `<dimension>=missing|correct` and the command; and
  `flywheel verify` fails rule P1 for such a pass (§2).

### `group_reviewed`
- Written by: the CLI only, via `flywheel review --group <goal|tasks:a,b> --agent --session S
  [--base REF] [--worker NAME]` (issue #420): a group of units validated together. A group is a goal
  id (every task planned with that `goal_id`) or an explicit `tasks:<a>,<b>,...`; its own records
  carry task `group:<id>` (the goal id, or the list with `:` and `,` turned into `-`:
  `tasks:a,b` is `group:tasks-a-b`), which `Validate` accepts wherever a task id is required.
- The integration tree: a detached `git worktree add` of the base (default `main`) in a temp dir,
  then each member's work merged in member order with `git merge --no-ff --no-edit` as flywheel's
  identity — its task branch `fw/<task>` when it exists, else the `commit` of its latest `finished`
  event. A member with neither is reported as missing and skipped; a merge that conflicts is aborted,
  its paths recorded, and the next member continues. The tree is removed afterwards.
- Group gates: each `review.group_gates` command runs in the integration tree like a gate (bash -c,
  captured output) and is recorded as a `validated` event with task `group:<id>` and gate `g<n>`.
- The integration reviewer: the embedded `integration` persona (never a panel dimension) reviews the
  combined diff base..tree, with a prompt section listing each member and its effective owns, the
  missing members, the conflicts and the gate readings; its prompt and stream are
  `.flywheel/reviews/group-<id>.<round>.prompt.md` and `.jsonl`. It reports only defects that
  involve more than one unit's change or the merge itself (merge seams, duplicated logic,
  conflicting assumptions, contract drift between units); every finding must carry category
  `integration` (the same refuse-and-retry-once path as the review agent).
- Routing: each finding becomes a `review_finding` (persona `reviewer:integration`, category
  `integration`, `reason` the group task, id `group:<id>-r<round>-<n>`) on the first member whose
  effective owns contain its file, else on `group:<id>`. Each conflicting path becomes a `blocker`
  on the member whose merge conflicted.
- Carries: `task` (`group:<id>`), `verdict` (`correct` when any blocker or major finding or any
  failed group gate, else `pass`), `tree` (the integration tree), `commit` (its HEAD), `session`,
  `model`, `adapter`, persona `reviewer:integration`, `note` (`members ...; missing ...; conflicts
  ...; gates g1=<rc> ...; <n> finding(s)`). The gate readings, the findings and this event are one
  append. A session that is a worker session of a member is refused (T4).
- Effect: no status change. An integration finding is closed only by a later review of the same
  group (which re-reports a defect still present under a new id) or by a lead's dismissal
  (`flywheel review <task> --dismiss <id>`); a unit's own review never closes one. The thread is
  `.flywheel/reviews/group-<id>.md` (same marker rule as a task's thread), and each member a finding
  was routed to has its own thread refreshed.
- Config: `review.group_gates` (default none); `config get/set review.group_gates` reads and writes
  the commands separated by `;;` (set also splits on newlines; an empty value clears them).
- Enforced: `flywheel land` refuses (rule `group`, below).
- State: a group task is not a unit. `Derive` leaves `group:<id>` out of `tasks` (and `counts`) and
  lists it under `groups` in `.flywheel/state.json`: `id`, `task`, `members` (from the latest
  `group_reviewed` note), `verdict` (the latest), `rounds`, `open` (its open blocking integration
  findings, on its members or on the group task) and `updated_at`; `groups` is omitted when there is
  none. The floor (`flywheel factory`, text and `--json`) shows a `groups (<n>)` section, one row per
  group — id, verdict, `open <n>`, members — and an andon entry `group:<id>` `group-open (<n>)` while
  one is open.
- Exit: 0 on `pass`, 1 on `correct` or an error, 6 on a refusal, 2 on usage.

### `blocked`
- Written by: the controller (`flywheel controller`), when a task's `needs:` target is scrapped.
- Carries: `task`, `reason` (names the needs target).
- Effect: `Derive` sets status `blocked`.

### `lost`
- Written by: `flywheel controller`, `flywheel run` (before its owns and exclusive collision
  checks) and `flywheel next` (before it recommends), for the current attempt of a dispatched or
  running task that is abandoned (issue #402): its lease has expired, or no lease exists and its
  run file `.flywheel/runs/<task>.<attempt>.jsonl` (with no run file, its `dispatched` event) is
  older than `limits.lost_after` (a Go duration, default `24h`). A live lease is never lost, and a
  lost attempt is not in flight, so it never blocks a dispatch.
- Carries: `task`, `attempt`, `reason` (`lease-expired` or `idle`), `note` (the evidence:
  `lease expired at <time>`, `no live lease; run file idle since <time>`, or
  `no live lease; no run file; dispatched at <time>`).
- Effect: a stale-kind event, ignored unless its `attempt` matches the task's current one;
  otherwise `Derive` sets status `lost`.

### `withdrawn`
- Written by: the lead, via `flywheel log --task <id> --kind withdrawn --note "<why>"` (issue
  #479), to take a plan back — typically one planned under an id another flywheel root already
  claimed. `--note` is required (exit 2 without it). `flywheel log` refuses it (exit 6, rule `W1`)
  while the task's current attempt is `dispatched` or `running`; the same check holds for a
  `--json` line.
- Carries: `task`, `note` (why), optionally `session`.
- Effect: `Derive` sets status `withdrawn` (same-instant rank after `blocked`, before `lost`). It
  is terminal like `landed` and `lost`: station `scrap`, floor stage `withdrawn`, never offered by
  `flywheel next`, and it holds no owns or exclusive claims. A later `planned` event revives the id.
- Verify: rule `W1` (section 2).

### `rebased`
- Written by: the CLI only, via `flywheel rebase <task> [--onto REF]` (issue #414), after
  `git rebase --onto <onto> <base> fw/<task>` succeeded in the unit's task worktree
  (`.flywheel/worktrees/<task>`; anywhere else the command refuses). `onto` defaults to `main`, else
  `master`. On a conflict the rebase is aborted, the branch is left as it was, the conflicting paths
  are listed (exit 1) and nothing is recorded.
- Carries: `task`, optionally `attempt`, `base` (the new base: `git rev-parse <onto>`), `note`
  (`was <old base>, onto <onto>`). `Validate` requires the base and the note.
- Effect: no status change. The unit's base (`dispatchBase`) becomes the latest `rebased` event's
  `base` instead of the first `dispatched` event's, so the owns check and the review ranges measure
  from there.
- A unit is **stacked** when its base is not an ancestor of `main` (else `master`) and a commit on
  main after their merge-base carries a `Flywheel-Task: <T>` trailer for another unit `T` whose
  branch `fw/<T>` contains the base (or, with `fw/<T>` gone, `T` is `landed`): `T` landed as a
  squash and the unit still carries `T`'s pre-squash commits. `flywheel validate` then records the
  warning `base <sha7> (unit <T>) was squash-merged as <sha7>; run: flywheel rebase <task>` as the
  `owns_checked` note (the reading still runs), `flywheel land` refuses (rule `stacked`, below), and
  the floor shows the done unit's run state as `stacked` on the andon.

### `recovered`
- Written by: the CLI only, via `flywheel recover --apply` (issue #422), when it applied at least
  one safe action.
- Carries: no `task`; `note` (the actions applied, `; `-separated, e.g. `mark-lost T r1
  (lease-expired) checkpoint 1a2b3c4`, `re-validate T`, `rebase T onto 5d6e7f8`), `paths` (the
  tasks they touched, sorted). `Validate` requires the note and refuses a task.
- Effect: no status change; the applied actions record their own events (`lost`, `validated`,
  `owns_checked`, `rebased`).
- `flywheel recover [--json] [--apply] [--all] [--dormant-after DUR]` (read-only without `--apply`)
  is where every lead session starts. It checks integrity: the log's hash chain and every §2 rule
  over every task. A rule failure on a task that is not `landed` fails integrity. A failure on a
  `landed` task is **history**: it can no longer be acted on, so it is reported and counted
  (`integrity.history`; the text shows the count and the first five, `--all` every one) and never
  fails integrity or the exit status. Then per task (as `Derive` sees it) it checks:
  - the task worktree (`.flywheel/worktrees/<task>`, else the attempt's recorded workdir). Its HEAD
    must contain the attempt's `finished.commit` (`git merge-base --is-ancestor`). A lead commit or
    merge on top is consistent; a later `rebased` event explains a move; a git failure is unknown,
    never a mismatch. Its uncommitted paths are compared with the attempt's `wrote` list. The
    dispatch baseline with unchanged content and the paths `worktree_setup` linked are excused; the
    rest are `unexplained`.
  - the lease (`live`, `dead`, `none`).
  - the run file (`complete` when its last byte is a newline, `torn`, `missing`).
  - a stacked base, a paused model, and the task's checkpoints.

  A task not `landed` whose latest event is older than `--dormant-after` (default `168h`; `0`
  disables) is **dormant** (JSON `dormant: true`). Its next action is still computed, but the text
  shows dormant tasks as one summary line (count and ids) unless `--all`, and `--apply` never acts
  on one: it is listed as `dormant`. Landed tasks are likewise one summary line (`N landed units`)
  unless `--all`. Exit 0 when integrity passes and no task's next action is `investigate`, else 1.
- The next action per task, first match wins:

  | Condition | Action | Command |
  | --- | --- | --- |
  | `landed` | `none` | |
  | not in flight, worktree HEAD is not the attempt's commit | `investigate` | |
  | not in flight, uncommitted paths no attempt wrote | `investigate` | |
  | dispatched/running, lease live | `none` | |
  | dispatched/running, lost by the `lost` rules above | `mark-lost` | `flywheel recover --apply` |
  | dispatched/running, otherwise | `none` | |
  | unclean finish or `lost`, model paused | `wait-reset` | `flywheel run <task> --resume` (waits for the reset) |
  | finished `rate-limited` or `abandoned-job` | `resume-session` | `flywheel run <task> --resume` |
  | `lost`, or any other unclean finish | `none` (dispatch or correct) | |
  | finished `stop` or `passed`, stacked | `rebase` | `flywheel rebase <task>` |
  | `passed` | `land` | `flywheel land <task>` |
  | any other status than `finished` | `none` | |
  | no `owns_checked` reading since the finish | `re-validate` | `flywheel validate <task>` |
  | the tree changed since that reading | `re-validate` | `flywheel validate <task>` |
  | readings complete and passing, review panel applies and is incomplete | `review` | `flywheel review <task> --agent --panel ...` |
  | readings complete and passing | `inspect` | `flywheel inspect <task> --verdict pass ...` |
  | otherwise (readings failed on the current tree) | `none` (correct) | |

- `--apply` runs only `mark-lost` (as the controller does, and it checkpoints the lost attempt's
  changed owned files, since a killed process never reached its `finished` event),
  `re-validate` (a measurement) and `rebase` when the base is certainly squashed and `git rebase`
  reports no conflict (a conflict is aborted and listed). `resume-session`, `wait-reset`, `review`,
  `inspect`, `land` and `investigate` are never run; they are listed for the lead.
- **Checkpoints.** An attempt that ends uncleanly (`error`, `rate-limited`, `stalled`, `silent`,
  `abandoned-job`, `length`) after writing files has its changed owned paths snapshotted: a
  temporary index reads HEAD, adds those paths, writes a tree, and `commit-tree` makes
  `checkpoint <task> <attempt>` on top of HEAD, kept at `refs/flywheel/checkpoints/<task>/<attempt>`
  — never the branch, never the real index. The `finished` event carries the sha as `checkpoint`; a
  checkpoint failure goes on its note and never fails the run. `flywheel checkpoint list|diff|restore|drop`
  manages them; `restore` refuses over uncommitted changes to the checkpoint's paths unless `--force`.

### `landed`
- Written by: the CLI only, via `flywheel land <task> --commit <sha>`.
- Carries: `task`, `commit`, `note`.
- Effect: `Derive` sets status `landed`. Verify's T5 (`ruleT5`) requires an earlier `inspected pass`
  or a recorded `excepted` event for the task; `LandTask` itself refuses **live** (exit 6, rule T5)
  unless the task's derived status is already `passed` or an exception is provided, and refuses
  (exit 6, rule T9) while the task has untriaged signals unless `--allow-untriaged <reason>`
  records why, refuses (exit 6, rule `stacked`, issue #414) a unit whose base landed as a squash
  (see `rebased` above; the fix is `flywheel rebase <task>`, and an exception landing overrides it),
  refuses (exit 6, rule `group`, issue #420) while an open blocking finding with category
  `integration` is on the task or, when it was planned under a goal, on `group:<goal>` (see
  `group_reviewed` above; close it by a new group review or a lead's dismissal),
  and refuses to re-land the same task under a different commit than it already
  recorded. The read, the checks and the append(s) run under `.flywheel/dispatch.lock` (the lock
  `run` and `amended` take) and then `.flywheel/feedback.lock` (the lock learning writers take;
  always in that order), so two concurrent landings of one task can never both pass the
  already-landed check, and no learning can change the task's signals mid-decision.

### `excepted`
- Written by: the CLI only, via `flywheel land <task> --commit <sha> --exception TEXT --session S`.
- Carries: `task`, `commit` (the commit the evidence covers), `session` (the lead), `note` (the
  evidence), `reason` (the status it overrode). `Validate` requires the note, the session and a
  valid commit on every write path.
- Effect: no status change by itself. It is appended in the same single write as the `landed`
  event it permits (`AppendEvents`), so a failure never leaves an exception without its landing.
  Verify's T5 accepts a landing on an exception only when the exception names the **same commit**
  and reports it as "landed on a recorded exception" (a deliberate, visible exception to T5, never
  a silent bypass). T4 fails an `excepted` event from a worker session (one
  that wrote the task's `started`, `finished`, `dispatched`, `report` or `worker_plan` event).

### `allow_untriaged`
- Written by: the CLI only, via `flywheel land <task> --commit <sha> --allow-untriaged REASON`.
- Carries: `task`, `commit` (the commit the landing covers), `note` (the reason), `signals` (the
  condition names the landing allows — the untriaged signal names at the time the landing was
  recorded). `Validate` requires the task, the note and a valid commit on every write path.
- Effect: no status change by itself. It is appended in the same single write as the `landed`
  event it permits (`AppendEvents`), so a failure never leaves an `allow_untriaged` without its
  landing. T9 enforces that a landing refusing to triage signals must be recorded with this event;
  the signals are not triaged by this event (they stay listed by `flywheel feedback` until a
  learning names them), and the event is purely for auditability and transparency.

### `audited`
- Written by: `flywheel audit <task> --session S`, from a session that did not plan, build or inspect the unit.
- Carries: `task`, `verdict` (`conforms` / `nonconformance`), `session`, `tree`, `note` (the findings), `persona` (`auditor`).
- Effect: no status change, and its verdict never replaces the unit's QC verdict in derived state.
  The `tree` is the one the gates were re-run on: captured before the clean copy is made and
  re-checked before recording (a tree that changed mid-audit records nothing). A record check that
  cannot be established (`INCONCLUSIVE`) is a finding: an audit that cannot confirm does not pass.
  The auditor's independence is checked again right before the event is appended. T7 gating is opt-in: see T7 below.
  In config, `Validate` refuses a `staffing.auditor` on the same agent and model as another role
  unless its RoleConfig `independence` is `"session"` (issue #463): a single-model factory then
  relies on this fresh-session check; a shared session is refused either way, and `independence`
  on any other role, or any other value, is a config problem.

### `release_audited`
- Written by: `flywheel audit --release <version> --session S` (issue #420), after a release is published.
- Floor level: carries no `task`.
- Carries: `session`, `version` (the tag, `v0.21.1`), `verdict` (`pass`, `fail` or `inconclusive`),
  `checks` (one `name=status` string per check, in order: `tag`, `changelog`, `binary`, `commands`,
  `docs`, `calibration`; status `pass`, `fail`, `skipped` or `inconclusive`) and `note` (the same
  list joined by `, `). `Validate` requires the session, the version, the verdict and at least one
  check; no other kind may carry `version` or `checks`.
- Effect: no status change. Every repository file is read at the tag, never the working tree. The
  verdict is `fail` when any check failed, else `inconclusive` when any could not be established (a
  download or a run of the binary failed, or the tag is missing only from this clone or origin could
  not be asked; a tag missing on origin too fails `tag`), else `pass`. It is recorded whatever the verdict; a
  missing session or a usage error records nothing.

### `probed`
- Written by: `flywheel doctor --record`.
- Carries: `model`, `reason` (the doctor class: ok, credits, key limit, consent required, auth missing, error, local endpoint down, model not pulled), `note` (`flywheel doctor`).
- Effect: an `ok` probe newer than the model's latest provider error closes its breaker at once instead of waiting for the cooldown to expire (issue #46).

### `amended`
- Written by: the planner or lead, via `flywheel log --task <id> --kind amended --brief <path> [--session S --model M] --note <why>`; a
  `--json`-ingested `amended` event lands through the same check.
- Carries: `task`, `brief`, `header` (the parsed brief header as recorded when the amendment was
  appended, as for `planned`), `session` and `model` (the planner's identity, recorded as for
  `planned`), `note`, `persona` (`planner`), and `owns`/`needs` copied from the brief header, as for
  `planned`. The `--goal` flag is a usage error (exit 2) with `--kind amended`.
- Effect: `Derive` updates only `brief`/`needs`/`owns` on the task, never its status. Verify's T1
  treats a `dispatched` hash mismatch as explained when an `amended` event for the task falls
  between that dispatch and now.
- Limit: an amendment cannot change the gate set an attempt has already been dispatched with — a
  pass is measured against the attempt's effective header, so the command refuses (exit 6) an
  amendment that would change the effective gate set instead of recording one that changes
  nothing. The comparison is against the effective set `AttemptBrief` merges, not the dispatched
  header alone: a correction whose delta declares no `gate:` lines inherits the base gates, so
  amending them does change what validation runs and is allowed; a correction's delta never
  replaces `live-gate:` lines, so an amendment touching only `live-gate:` takes effect on a
  correction attempt but is inert — and refused — on a fresh dispatched one. Change the gates of
  a dispatched attempt with a correction delta: `flywheel run <task> --delta <file>`. An
  amendment that widens `owns:` after a fresh dispatch takes effect: the attempt's effective
  owns are the dispatched header's plus every later amendment's (issue #281). One that would
  narrow `owns:` or `exclusive:` cannot take effect — both are unioned — so it is refused (exit
  6) like an inert gate change. Narrowing is judged by coverage, with the matching the owns check
  uses: replacing `src/` with `src/main.go` stops covering `src/other.go`, so it is a narrowing.
  A JSON-ingested amendment without a `header` is stored with the header parsed from its brief,
  so it takes effect the same way. A legacy dispatch that recorded no header is measured against
  the latest amendment, where a narrowing does take effect, and is not refused. Fixing prose is
  allowed.
  The refusal and the append run under `.flywheel/dispatch.lock`, the same lock file `flywheel
  run` holds across its own read-check-append, so an amendment and a dispatch serialise.
- Acknowledging a lost delta: `flywheel log --task <id> --kind amended --attempt c<n> --note <why>`
  (no `--brief`) records that correction `c<n>`'s delta is not retained — for ledgers written
  before the per-attempt snapshot, where a later correction overwrote a shared delta file (issue
  #452). `Validate` requires a correction attempt (`c*`) and a non-empty `note`; `flywheel log`
  (the flag path and `--json` alike) also requires a `dispatched` event for that attempt and
  refuses a `brief` or `header`. It amends no brief: it never excuses a fresh attempt's mismatch.
  T1 passes that one correction's mismatched or unreadable delta only when the acknowledgement was
  recorded after its dispatch, with reason `acknowledged: delta for c<n> not retained (<note>)`;
  it never waives a correction silently, and never one with no acknowledgement.

Because `planned`, `amended` and `dispatched` events carry the parsed `header`, the log is
self-contained: a pass is measured against the header recorded in it, so a brief edited on disk
after the fact — even one re-recorded through `flywheel log --kind amended` with the same path —
no longer changes what any recorded pass is measured against. On a dispatched attempt, owns widen
with `--kind amended`; gates change only with `flywheel run <task> --delta <file>`, which is what
`flywheel run`'s brief-drift message names (issue #387). An event without a `header` falls
back to reading the file at its `brief` path, so ledgers written before this field existed keep
working exactly as before.

### `validated`
- Written by: the CLI only, via `flywheel validate <task>`, once per declared `gate:` line, or
  `flywheel attest` (an external reading, below).
- Carries: `task`, `attempt`, `gate` (1-based index, as a string), `command`, `tree` (git tree
  hash), `commit` (the repository HEAD at the moment **this** reading was taken, not at the start
  of the pass — each gate resolves it independently, so two readings in one pass may carry
  different commits and that is correct, not a bug; empty when the workdir is not a git repository
  or HEAD cannot be read, so a consumer must treat `commit` as optional and never assume a
  non-empty value, issue #240), `workdir` (the git working tree the reading was taken in,
  recorded in canonical absolute form — symlinks resolved, DOS 8.3 short names expanded — and
  only when it differs from the flywheel root: an external `--workdir` clone, so a verifier can
  resolve the tree object in the right repository, issue #244; omitted on same-dir readings,
  including aliases of the root),
  `rc`, `duration_ms`, `sha256` (of the gate's combined output),
  `path` (`.flywheel/evidence/<task>/<attempt>/gate-<n>.log`), `persona` (always `"supervisor"`,
  hardcoded — see §4), `reason`/`note` (`host-blocked` when Windows Smart App Control blocked the
  freshly built binary twice in a row).
- Effect: no status change. `Validate` requires `gate` and `tree` to be non-empty. A `host-blocked`
  reading never counts as passing for T3, matching `ValidateTask`'s own `GatesOK=false` for it.
- A gate that fails for a reason attributable entirely to a changed path outside the unit's own
  `owns:` — another unit's half-written file in the same tree, not this unit's own work — is
  recorded with `reason` `inconclusive` and a `note` of `blocked by <paths>` (issue #162). T3 still
  requires a *passing* reading for every declared gate: an `inconclusive` reading is not a pass, and
  `flywheel validate` still exits 5 for it, exactly like an ordinary failure.
- A **quiet gate** (issue #411) is a `gate[quiet]:` or `live-gate[quiet]:` header line: a gate
  that host contention distorts (device timing, hardware in the loop). The parsed header keeps
  its command among the gates or live gates as usual and lists its 1-based index in
  `QuietGates` / `QuietLiveGates`. Before it runs, `flywheel validate` takes the exclusive
  `.flywheel/locks/quiet.lock` (recording task, gate, pid, host and `started_at`) and polls until
  no other task has a live lease on this host and no other process holds a gate marker; the
  validating task's own lease is ignored. Every ordinary gate holds a shared marker
  (`.flywheel/locks/gates/<pid>-<n>`) while it runs, and first waits while another process holds
  `quiet.lock`; after the budget it runs anyway with the note `ran during a quiet gate (<task>
  gate <n>)`. Both waits are bounded by `limits.quiet_wait` (a Go duration, default `30m`). A lock
  or marker whose pid is dead on this host is stale and ignored. When the host never goes idle,
  the quiet gate does not run: it is recorded with `reason` `inconclusive`, no `rc` and the note
  `host busy: <tasks>` — unmeasured, never a failure, and not a pass for T3. While `quiet.lock` is
  held by a live process, `flywheel run` refuses every dispatch (exit 6, rule `quiet`: `a quiet
  gate (<task> gate <n>) is running on this host; dispatch after it ends`).
- An **external** reading (`source` `"external"`, issue #367) is one flywheel did not measure:
  `flywheel attest <task> --commit <sha> --evidence <url> --session <lead>` records that a named
  run elsewhere (CI on the unit's PR) passed every gate on a commit. It writes one `validated` per
  `gate:` and `live-gate:` (rc 0) and one clean `owns_checked`, in one `AppendEvents` call, on the
  commit's tree, each carrying `source`, `evidence` (the run's URL or reference), `commit` and
  `session`; `Validate` refuses an external reading missing any of the three. Only the lead writes
  them, never a worker: `attest` refuses a worker session of the task (T4, and verify's T4 fails
  one), a task with no dispatched attempt (T5), and a commit that changed a path outside the
  unit's `owns:` against its first parent (T3). An external reading counts for T3 exactly like a
  measured one; verify fails one missing its evidence, session or commit, and names the evidence
  of a pass it relied on (`attested: <evidence>` on the T3 line).

### `owns_checked`
- Written by: the CLI only, via `flywheel validate <task>`, once per pass, or `flywheel attest`
  (an external reading, see `validated` above).
- Carries: `task`, `attempt`, `tree`, `commit` (the repository HEAD at the moment the owns check
  ran, resolved independently of the gates — each reading carries the HEAD at the time it was
  taken, so it may differ from the gates' commits and that is correct, not a bug; empty when the
  workdir is not a git repository or HEAD cannot be read, so a consumer must treat `commit` as
  optional and never assume a non-empty value, issue #240), `workdir` (as on `validated` — where
  the reading was taken, canonical absolute form, recorded only when it differs from the flywheel
  root, issue #244),
  `outside` (changed paths not covered
  by `owns:`), `baselined` (changed paths excused because they were already dirty at dispatch and
  are byte-identical now), `attributed` (changed paths blamed on another in-flight task instead —
  see below), `persona` (`"supervisor"`).
- Effect: no status change. T3 requires an `owns_checked` with an empty `outside` on the same tree.
- The changed paths are the unit's changes since the attempt was dispatched — uncommitted and untracked
  files plus files touched by the branch's own commits since the `base` commit of the unit's first
  dispatched event, or of its latest `rebased` event (issue #414) (a correction attempt's check still
  counts what an earlier attempt committed)
  (files merged in from another branch are not the unit's) — so committing a stray edit does not hide
  it (issue #332).
- A changed path outside `owns:` and not baselined is **attributed** rather than outside when some
  other task's brief `owns:` it (`ownsContains`, the matching `flywheel validate` already uses) and
  that task is currently in flight (`Derive` status `dispatched`, `running`, or `finished` — never
  `landed`, `passed`, or `rejected`): a neighbour's own work in progress on a shared checkout, not
  this task's stray file (issue #117). Attribution never excuses a path this task's own `owns:`
  already covers — such a path was never outside to begin with — and never hides a path no in-flight
  task owns: that path is still `outside`, and T3 still fails it. `flywheel validate` prints
  attributed paths as `<task> owns: attributed <path> -> <task>[, ...]` before the outside line.
- In a **sibling worktree** (another git worktree of the same repository, compared against the
  dispatch-time snapshot), a changed path is attributed to a task of THAT worktree's own ledger
  whose brief owns it, which was dispatched, and which has not landed (any status except `landed`)
  — its own ledger is the authority for its own worktree, and a unit that passed inspection still
  owns the uncommitted edits made in its worktree afterwards (issue #278). A task that was only
  planned never ran and attributes nothing; a path no such task owns is still `outside`.

### `inspected`
- Written by: the CLI only, via `flywheel inspect <task> --verdict ... --session ...`.
- Carries: `task`, `verdict` (`pass`, `rework`, `scrap`, or `escalate`), `tree`, `session`, `note`,
  `commit` (with `--commit <sha>`: the tree inspected is that commit's, not the working tree's —
  how an attested, already-merged commit is inspected, issue #367),
  `workdir` (the git working tree inspected, canonical absolute form, recorded only when it
  differs from the flywheel root, issue #244), `persona` (always `"inspector"`, hardcoded by
  `InspectTask` — see §4 for the only way a `"lead"` ever appears there).
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
  session's first turn (issue #157). The same `--hooks` also installs a Claude Code `Stop` hook that runs `flywheel gate` and blocks ending the session while units are finished but not inspected or signals are untriaged (issue #56).
- Carries: `session` (required, like `staffed`) and no `task`.
- Effect: floor-level; `Derive` skips it outright, the same way it skips `staffed`. `flywheel trace
  <session>` is its only reader.

`flywheel init --git-hooks` adds the git layer (issue #56): a `commit-msg` hook refuses a commit
without a `Flywheel-Task: <id>` trailer (merges, reverts and fixup/squash commits are exempt), and a
`pre-push` hook refuses a push while a unit named in the pushed commits fails `flywheel verify`, or
`flywheel verify --log` finds the event log's hash chain broken.

`flywheel init --ci` adds the CI layer: a `flywheel-audit` job running
`flywheel verify --all --log` on every pull request. Made a required status check in the branch
ruleset, it cannot be bypassed locally; it needs the event log committed, and an inconclusive check
(exit 8) only warns.

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

### `learning`
- Written by: `flywheel feedback add --task <id> --severity P0|P1|P2 --title T --observed O
  --evidence E --ask A [--signals a,b] [--scope flywheel|project]`, or a `flywheel log --json` batch.
- Carries: `task`, `severity`, `title`, `observed`, `evidence`, `ask` (all required), `signals`, and
  `scope` (issue #409): `flywheel` — the default, and what an event without `scope` means — is
  feedback about flywheel; `project` is the project's own learning (its product bugs, say).
  `Validate` refuses any other scope, and a scope on any other kind.
- Effect: numbered L-01, L-02, … in log order whatever the scope; the generated
  `.flywheel/learnings.md` renders a `## Feedback for flywheel` and a `## Project learnings` section.
  `flywheel feedback export` and `submit` carry only undismissed flywheel-scoped learnings and say
  how many project ones stayed local.

### `note`
- Written by: anyone keeping a journal, via `flywheel log --kind note [--task T] --note "<text>"
  [--session S]` (issue #409). `--note` is required.
- Carries: `note` (required), and optionally `task` and `session`.
- Effect: none — a journal line (`action: dispatched…`, `result: … merged`) is never a learning,
  so it never reaches learnings.md or an upstream report. `flywheel explain` shows it as
  `note: <text>`.

### `lead_edit`
- Written by: the lead, via `flywheel claim-edit --paths <p1,p2> --session <session> [--note ...]`,
  to declare an edit it made itself after a unit's dispatch (issue #228).
- Carries: `session` (the declaring session, required), `owns` (the claimed repo-relative paths,
  reusing the field that means "these paths belong to this declaration"), `baseline` (path ->
  sha256 of the content the claim declared, the same map field a dispatched event uses; `"deleted"`
  marks a path that was absent at claim time), `note`, and no `task`: the claim is
  repository-wide, not per task. Only literal paths may be claimed: `claim-edit` refuses a pattern
  (`*`, `?`, `[`) or a trailing `/` directory prefix with exit 2, because a pattern cannot be
  bound to one content hash and would exempt a whole tree.
- Effect: no status change — `Derive` skips it like every other floor-level event. The owns check
  (`attributeOutside` in `gauges.go`) attributes a changed path a claim covers as `"<path> -> lead
  <session>"` instead of `outside`, so the reading stays clean; a path the claim does not cover is
  still `outside`. It is a **declaration, not an exemption**: the path still appears in the ledger,
  attributed to a named session, and the claim expires the moment the path's content no longer
  hashes to the recorded value (or the path reappears, for a deletion marker) — the lead declared
  that edit, not the file forever (issue #258).
- Three guards a consumer can rely on. A `lead_edit` never covers a path when (1) the declaring
  `session` is a worker session of the task being validated — one that wrote that task's `started`,
  `finished`, `dispatched`, `report`, or `worker_plan` event, the same worker-event set T4 uses —
  (2) the claim's `ts` is not strictly before the reading being computed: a claim never
  retroactively blesses a stray an earlier validation already reported, or (3) the path's current
  content does not hash to the recorded `baseline` value: an unbound claim (no entry for the path)
  never excuses anything. The owns check re-reads the event log immediately before attributing, so
  a claim appended while the gates ran still qualifies for that reading.
- With `--worktree <dir>` the claim names another session's edit in a sibling worktree: the paths
  are hashed relative to that worktree and the event carries it as `workdir`, so it excuses
  `"<worktree>: <path>"` (attributed `"<worktree>: <path> -> lead <session>"`) under the same
  guards, and never the same relative path in the unit's own tree (issue #362).
- Sibling worktrees (issue #339): a path changed in another worktree recorded at the unit's
  dispatch, owned by no in-flight unit's brief there, is attributed `"<worktree>: <path> -> lead
  <session>"` when that worktree's own ledger has a `lead_edit` claim covering it under the same
  three guards (the worker-session guard against each of that worktree's in-flight units), and only
  while that worktree still has a dispatched, unlanded unit. A sibling that is another unit's
  `run --worktree` worktree, `<repo>/.flywheel/worktrees/<task>`, is read against the MAIN ledger
  instead: while `<task>` is dispatched and unlanded there, every changed path in it is attributed
  `"<worktree>: <path> -> <task>"` (issue #386).

### `goal`
- Written by: `flywheel goal add`/`flywheel goal set`.
- Carries: `goal` (a `GoalSpec`: `id`, `title`, `acceptance`, `required` tasks, `status` — one of
  `active`, `met`, `failed`, `abandoned`, checked by `Validate`).
- Effect: task-less, like `staffed`.

## 2. Transitions the code enforces

`flywheel verify` runs eight rules against every task it is asked about — `VerifyTasks` calls
`ruleT1`, `ruleT3`, `ruleT4`, `ruleT5`, `ruleT8`, `ruleR1`, `ruleW1`, `ruleP1` in that order — and the same
rules are enforced **live**, before the record is written, inside `InspectTask` (T3, T4, T8, R1 as
the refusal rule `review`, and P1 as the refusal rule `panel`) and `LandTask` (T5).
`ValidateTask` produces the readings T3 needs but enforces nothing itself; it can fail its own
gates (exit 5) without touching the log's legality.

- **T1 — dispatch matches its brief.** A fresh (`r*`) `dispatched` event's `sha256` must match the
  brief named by the task's latest `planned` event, unless an `amended` event for the task landed
  in between — then the mismatch is explained, not flagged. A correction (`c*`) `dispatched` event
  hashes its own delta file instead (the path in its own `brief` field, the per-attempt snapshot);
  no brief amendment ever excuses a tampered or missing delta. The one exception is explicit: an
  `amended` event naming that `c*` attempt with a note, recorded after its dispatch, acknowledges
  a delta lost before the snapshot existed, and T1 passes it with the reason `acknowledged: delta
  for c<n> not retained (<note>)`. `verify` reports this per-task as rule `T1`.
- **T3 — a pass needs current readings, not old ones.** `inspected pass` (or a live `--verdict
  pass` inspection) needs, for the *same git tree hash* the unit actually built: a passing
  `validated` event from the supervisor for **every** gate the current attempt's prompt declares,
  and a clean (`outside`-empty) `owns_checked` — both recorded after the latest `finished` event
  that precedes the check. On a shared working tree the hash can move between `validate` and
  `inspect` because other units keep writing, so the rule is relaxed (issue #218): when no reading
  exists on the current tree, the reading may be taken on another tree `T` whose difference from
  the current tree lies entirely **outside the unit**'s `owns:` — every one of the task's readings
  must come from that same `T`, and the recorded `inspected` event's note then names `T` (`; reading
  from tree <T> (diff outside owns)`). This is sound because every file the unit owns is
  byte-identical between the measured tree and the inspected one, so the unit's own work *was*
  measured; it costs a little because a gate broader than the owned files could be broken by a
  neighbour's later change, and T3 accepts that risk deliberately — the alternative is that correct
  work cannot land at all. That is not a licence to put whole-workspace gates on narrow units. "The
  current attempt's prompt" is `AttemptBrief`'s result (issue #133):
  the base brief plus, when the current attempt dispatched a different file (a correction delta),
  that file's `gate:` lines replacing the base's and its `owns:` unioned with the base's — an
  `amended` event replaces which brief counts as the base outright. `owns:` entries are matched as
  a literal path, a `dir/` prefix, or a `path.Match` shell pattern, all three checked the same way
  (issue #135, `ownsContains`). An entry starting with `!` is negated (issue #388) and takes the
  same three forms: a path is owned when some positive entry matches it and no negated entry does,
  so `apps/inc/**, !apps/inc/wake.h` owns `apps/inc/a.h` but not `apps/inc/wake.h`, and a negation
  with no positive entry owns nothing. flywheel's own bookkeeping stays owned. The dispatch owns
  collision check applies the same rule, so that header does not collide with an in-flight task
  owning `apps/inc/wake.h`; an amendment adding a negation that removes a covered path is a
  narrowing; and `flywheel lint` never checks a negated entry for existence, but warns when no
  positive entry covers it. Each inspection uses its own window, so a later correction attempt
  never invalidates an earlier legitimate pass. A pass measured in an external `--workdir` — a
  separate clone, not a worktree of the verifying repository — is verifiable from its own repo:
  `flywheel verify --workdir <path>` resolves tree objects there, and without the flag a `workdir`
  recorded on the task's reading events is used when that path still exists (issue #244). When the
  pass's tree cannot be resolved in any repository the verifier can see, the strict reading check
  still runs on the ledger's own evidence (it needs no git): a complete reading passes T3, and an
  incomplete one is **inconclusive** (exit 8) rather than a violation — a verifier that cannot see
  the tree must not claim a violation it has not established.
- **T4 — no self-inspection.** An `inspected` or `excepted` event's `session` must never be a session
  that wrote that task's `started`, `finished`, `dispatched`, `report`, or `worker_plan` event.
  `InspectTask` checks this **before** T3, so a worker-session inspection is refused as T4 even when
  its readings are also missing.
- **T5 — no landing without a pass.** A `landed` event needs an earlier `inspected pass` or a
  recorded `excepted` event for the same task. `LandTask` additionally refuses to land a task whose
  derived status is not `passed` (unless an exception is provided), and refuses a second `landed`
  event for the same task under a different commit than the one already recorded (the same commit is
  a silent no-op, exit 0).
- **T8 — personas write only their own kinds.** `validated` and `owns_checked` must carry `persona
  "supervisor"`; `inspected` must carry `"inspector"` or `"lead"`. No other kind is persona-checked
  by this rule (§4 has the full picture, including what is and is not mechanically enforced).
- **R1 — no inspected pass while a blocking review finding was open** (issue #389). An `inspected`
  pass fails when `OpenFindings` over the events BEFORE it held a `blocker` or `major` finding for
  the task; a finding raised after the pass never fails it retroactively, and a task never reviewed
  passes. Live, `InspectTask` refuses such a pass as rule `review`, before T3: `open blocking review
  findings: <ids>; fix them (flywheel review <task> --agent --fix) or dismiss one (flywheel review
  <task> --dismiss <id> --session <you> --note "<why>")`. A non-pass verdict is never refused by it.
- **P1 — no inspected pass without a complete review panel** (issue #420). Where the panel applied —
  `review.required` is set, or an agent `reviewed` event carrying a dimension preceded the pass — an
  `inspected` pass fails when `VerdictMatrix` over the events BEFORE it, on the pass's tree, has a
  configured dimension that is not `pass`; the reason names each `<dimension>=missing|correct`. The
  panel and `review.required` are read from today's configuration. Live, `InspectTask` refuses such
  a pass as rule `panel`, after T3 (the tree it checks is the one T3 measured): `the review panel is
  not complete on tree <tree>: <dimension>=<verdict>, ...; run flywheel review <task> --agent --panel
  --session <reviewer> (add --fix to correct and re-review)`.
- **W1 — no withdrawal of a live attempt** (issue #479). A `withdrawn` event is legal after a
  `planned` or `amended` event or after a finished attempt (any status but `dispatched` or
  `running`); one the task's events derive to `dispatched` or `running` just before it (derivation
  order) fails with `withdrawn event at <ts> while attempt <attempt> was <status>; stop the run (or
  wait for it to finish) before withdrawing`. Live, `flywheel log` refuses such a withdrawal as rule
  `W1` (exit 6) before anything is written.

## 3. Designed, not enforced

`docs/design/autonomous-shipping.md` describes ten transition rules, T1-T10, and a fuller event
vocabulary (`audited`, `signal`, `dismissed`, `learning`, "by" attribution blocks). Only T1, T3, T4,
T5 and T8 exist in `verify.go` (T9 is enforced live by `flywheel land`, below), and only the kinds in `events.go`'s known-kinds map exist
at all — `Validate` rejects any other kind by name, so an event carrying `audited` today is simply a
validation error, not a recognized-but-unchecked record. Concretely, still design-only:

- **T2** (a step-20 `worker_plan` or a signal) — the `no-plan` half landed (§1); `flywheel run` now
  records a `signal` event for `no-plan` and the other conditions, but nothing treats one as a
  rule violation.
- **T6** (sensitive domains need the lead's sign-off and an audit before landing) — nothing detects
  a "sensitive domain," and no command asks for a sign-off.
- **T7** (a wave's first article needs `audited conforms` before the rest lands; an open
  nonconformance stops its kind of task) — `flywheel audit` now records `audited` (issue #61),
  and `flywheel audit --first-article` / `--sample RATE` select first articles and a seeded sample (issue #61), and T7 is enforced by `flywheel land` when .flywheel/config.json sets `audit.first_article`: a unit is refused (exit 6) until its worker line has a conforming audit, and while the line's latest audit is a nonconformance.
- **T9** (checkpoint/land/handoff refuse while signals are untriaged, unless `allow_untriaged`) —
  now enforced **live** by `flywheel land` for a task's own untriaged signals (refused with exit 6
  unless `--allow-untriaged <reason>` records an `allow_untriaged` event). `flywheel handoff` does
  not refuse yet, and `flywheel verify` does not re-check T9, because ledgers written before the
  rule existed have landings with untriaged signals and would all fail. The signals are not triaged
  by `allow_untriaged` — they stay listed by `flywheel feedback` until a learning names them with
  `--signals`, and they recur untriaged again if the condition reappears after the learning.
- **T10** (the log is append-only with a hash chain per shard) — the log is append-only in
  practice (`AppendEvent` only ever opens with `O_APPEND`, and `ParseEvents` treats an unresolved
  git conflict marker as a hard error). Every event carries `prev`, the SHA-256 of the log's last
  complete line when it was appended (issue #57); `flywheel verify --log` checks that every `prev`
  matches the hash of some earlier line, failing (exit 6) at the first line whose `prev` matches
  none. Appends are serialised by `.flywheel/events.lock` (held only for the read of the last line
  and the write), so within one ledger the chain is linear: every record but the last is the
  predecessor of the next, and removing or editing any of them is detected. "Some earlier line" is
  accepted so a git merge, which interleaves two branches' lines, stays valid. Limits: removing the
  LAST line, or editing a line written before the chain existed, is not detected (issue #47 tracks
  per-shard chains).

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
  not `flywheel validate`. `flywheel attest` also signs its external readings `"supervisor"`, and
  they are the one kind of reading that does carry a `session`: the lead's, never a worker's (T4).
- `inspected` → persona must be `"inspector"` or `"lead"`. `flywheel inspect` (`InspectTask`)
  always writes `"inspector"`; the only way an `inspected` event ever carries `"lead"` is a
  hand-crafted `flywheel log --json` line with an explicit `"persona":"lead"` field — the ordinary
  flag form of `flywheel log` has no `--persona` flag at all.
- Every other kind (`planned`, `dispatched`, `started`, `worker_plan`, `no-plan`, `finished`,
  `report`, `reviewed`, `blocked`, `lost`, `withdrawn`, `landed`, `amended`, `staffed`, `goal`)
  carries no persona restriction in `ruleT8`; `withdrawn` is the lead's, through `flywheel log`. In practice most of them are written only by a specific CLI
  command (`dispatched`/`started`/`worker_plan`/`no-plan`/`finished`/`report`/`worktree_setup` only by `flywheel
  run`; `landed` only by `flywheel land`; `blocked`/`lost` only by `flywheel controller`;
  `review_finding` only by `flywheel review --agent`; `finding_response` only by `flywheel review
  --agent --fix` and a lead's `--dismiss`), which is what keeps them honest — not a
  persona field. The staffing `reviewer` role (`staffing.reviewer.adapter|model|session`) names who
  run`; `landed` only by `flywheel land`; `blocked` only by `flywheel controller`; `lost` only by
  `flywheel controller`, `flywheel run` and `flywheel next`; `review_finding` only by
  `flywheel review --agent`), which is what keeps them honest — not a persona field. The staffing `reviewer` role (`staffing.reviewer.adapter|model|session`) names who
  runs the review agent; `Validate` refuses a reviewer session that also holds the lead or
  inspector role, and the agent itself refuses (T4) a session that is a worker session of the task.

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

## Log layout

The event log has two layouts, both append-only: the legacy single-file layout (`.flywheel/events.jsonl`) and the sharded layout (files under `.flywheel/events/`). Both are opt-in; `flywheel log --shard` is the only way to switch, and the switch is one-way: once a repository uses the sharded layout, older binaries cannot read it safely (they reject the unknown `log.shards` config field). The config key `log.shards` is written by `--shard` and fences out older binaries.

### Legacy layout

The legacy layout is a single `.flywheel/events.jsonl` file, one JSON event object per line, appended by `AppendEvent`.

### Sharded layout

The sharded layout puts events under `.flywheel/events/`:

- **Per-task shards:** one file per task, named `<task>.jsonl`, holds all events for that task (every event with that task id in the `task` field).
- **Global events:** `@floor.jsonl` holds events that have no task (`session_start`, `session_end`, `session_command`, `staffed`, `goal`, `lead_edit`, and `amended` events whose brief is in the global briefs directory).
- **Session boundary events:** `@session-<id>.jsonl` holds every `session_start`, `session_command`, and `session_end` event for that session, so a reader can reconstruct a session's view efficiently.

Readers merge events in this order: legacy file first, then stable by each shard's running-max timestamp (the timestamp of the last appended event in that shard), which preserves the global linearized order while allowing shards to operate concurrently. Each shard has its own hash chain with a genesis block (the first event appended to that shard) and a `sharded` event at the moment the layout is switched (kind `sharded`, with fields identical to other seal events). `flywheel verify --log` checks each shard's chain separately and also the merged order across shards.

Transient locks under `.flywheel/locks/` guard concurrent writes (one per shard, named `<task>.lock`); they are git-ignored. The locks enforce per-shard write order and are consulted by readers to detect in-flight appends, but readers never wait — a slow reader may observe partial state, and consistency is per-file, not cross-shard. Deletions and trimmed tails in a shard are not detected by the chain (the seal block lives at insertion time, not at mutation time); only appends are tracked.

## 5. `flywheel verify` and exit codes

`flywheel verify [<task>...|--all] [--json] [--log] [--workdir PATH]` runs T1/T3/T4/T5/T8/R1/W1/P1 for the requested
tasks (`--all` derives the task list from every `task` seen in the log) and prints one
`PASS`/`FAIL`/`INCONCLUSIVE` line per rule per task, or the same result as JSON (`{"passed": bool,
"items": [{"task","rule","pass","inconclusive","reason"}]}`; `inconclusive` is omitted when
false, so `--json` consumers of the existing fields keep working). `--workdir` names the
repository to resolve tree objects in when the readings were taken in an external clone (issue
#244); without it, a `workdir` recorded on the task's reading events is used when that path still
exists. `--log` checks the event log's hash chain (T10, issue #57): every event's `prev` must match
the SHA-256 of some earlier complete line; `--log` fails (exit 6) at the first line whose `prev`
matches none, indicating a line was edited or removed. When that dangling `prev` is the hash of a
LATER line in the same file, the break reason is `reordered: line N chains to line M, which comes
after it (a git merge or an edit reordered the log; no record is missing)`. A git merge or conflict
resolution of a committed ledger does this. It is still a break, but no record is missing (issue
#422); otherwise the reason is `prev matches no earlier line`. Either layout prints
`<file> line N: <reason> (prev <12 hex>)`.

**Acknowledged breaks (issue #436).** The ledger is append-only, so an explained break is never
repaired by an edit: `flywheel log --reanchor --note "<why>" [--force] [--session S] [--dir DIR]`
appends a `reanchored` event (floor level, no task) through the normal append path, so the
acknowledgement is itself chained. It carries `file` (the log file as the chain check names it:
`events.jsonl` or `events/<task>.jsonl`), `line_no` (the 1-based break line, display only),
`break_prev` (the dangling `prev`, full hex), `sha256` (the break line's hash — the identity of the
acknowledged line), `reason` (`reordered` when the break reason starts `reordered:`, else `removed`)
and `note` (why, required). The command refuses (exit 6) when the chain is intact, when the break is
not a dangling `prev` (a missing seal, a line with no `prev`, a wrong shard genesis), and when the
break classifies as `removed` without `--force`: a `removed` break may be a real edit or deletion,
and `--force` records the decision that it is explained. `--reanchor` does not combine with
`--kind`, `--task` or `--json`, and `--note` is required (exit 2). The chain check (all layouts) first
reads the log's `reanchored` events; a break is acknowledged when one has the same `file`, the same
`break_prev`, a `sha256` equal to the break line's hash, and a `reason` equal to the classification
computed now — a `reordered` acknowledgement never covers a line that now classifies as `removed`.
An acknowledged break does not stop the scan; each later break needs its own acknowledgement.
`--json` lists them under `acknowledged` (`file`, `line`, `reason`, `session`, `note`), and a passing
`--log` and `flywheel recover`'s integrity line append `; acknowledged break at <file> line N
(<reason>), by <session>: <note>` for each.

**Preventing reorders.** A repository that commits `.flywheel/` should merge the event log with
git's union driver, which keeps each side's appended lines in order, so every `prev` still resolves
to an earlier line. `flywheel init` writes `.flywheel/.gitattributes` with `events.jsonl merge=union`
and `events/*.jsonl merge=union`; it never rewrites an existing file, so an older repository adds
those two lines to `.flywheel/.gitattributes` by hand and commits it. An `INCONCLUSIVE` item is `pass:false` with
`inconclusive:true`: the pass's tree could not be resolved in any repository this verifier can
see, so T3 can neither confirm the readings nor assert a breach. Naming a task explicitly still
runs every rule for it even if the log has never heard of it — a missing planned brief, for
instance, fails T3 by name rather than being skipped. An empty log verified with `--all` passes
vacuously; verifying with no tasks and no `--all` is a usage error — except `--log` alone, which
checks only the chain.

Exit codes follow the repo-wide convention from `AGENTS.md`: 0 ok, 1 error, 2 usage, 5 gauges
failed, 6 rule refusal, 8 inconclusive. The enforcing commands:

| Command | Success (0) | Refusal | Other |
| --- | --- | --- | --- |
| `flywheel validate <task>` | every gate passed, nothing outside `owns:` | **5** — a gate failed, stayed host-blocked after one rerun, or a changed path is outside `owns:` | 2 usage, 1 other error |
| `flywheel inspect <task> --verdict ... --session ...` | inspection recorded | **6** — `RuleRefusal` naming T3, T4, T8, or `review` (an open blocking review finding) and its fix | 2 usage, 1 other error |
| `flywheel attest <task> --commit <sha> --evidence URL --session S` | external readings recorded | **6** — `RuleRefusal` naming T3, T4 or T5 | 2 usage, 1 other error (e.g., the commit is not in the repository) |
| `flywheel verify [...] [--json]` | every requested check passes | **6** — any check fails (`FAIL <task> <rule>: <reason>`) | **8** — every failing check is `INCONCLUSIVE` (no violation established, the tree could not be resolved); 2 usage, 1 other error |
| `flywheel land <task> --commit <sha> [--exception TEXT --session S]` | landing recorded, or repeats an already-landed commit | **6** — `RuleRefusal` naming T5 or T4 | 2 usage (e.g., --exception without --session), 1 other error |
| `flywheel run <task>` | `rc == 0` and finish `reason` was `stop` | — | **3** silent (no output within the start timeout); **7** stalled (the run-file gap watchdog fired mid-stream, issue #158); **4** any other outcome (nonzero `rc`, or `reason` `length`/`error`/`start-failed`); 2 usage or no worker configured; 1 other error |

`flywheel run`'s own three codes (3, 4, 7) are not part of the repo-wide list: they are
`ExitCode`'s reading of one `Result`, keyed by exit number instead of by command, and `AGENTS.md`
records them as `run`-specific. Exit 7 means **stalled** — a mid-stream gap — and nothing else, so
a consumer scripts exit codes per command, never globally:

| Exit | `flywheel run` reason |
| --- | --- |
| 3 | `silent` — no stdout line arrived within the start timeout |
| 4 | any other non-clean outcome — nonzero `rc`, or finish `reason` `length`, `error`, `rate-limited` (after any resumes), `abandoned-job` (after its one resume), or `start-failed` |
| 7 | `stalled` — the run had started but the run file stopped growing for the stall timeout (issue #158) |

Everything upstream of these five commands — writing a brief, deciding what belongs in `owns:`,
choosing which task to dispatch next — is judgment the protocol does not check; the CLI enforces
only what is above.
