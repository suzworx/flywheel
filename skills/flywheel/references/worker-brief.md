# Worker Brief — how to run the flywheel loop safely

This is the operating manual for the orchestrator (Codex or Claude Code). The worker is the OpenCode
CLI running the approved model (`$MODEL`). `$MODEL` is the approved worker model, set once in
`skills/flywheel/SKILL.md` → Invariants. Everything below is a rule, not a suggestion. The event
records this loop produces are checked against
[protocol v1](../../../docs/PROTOCOL.md).

## 1. Precise, bounded briefs

The worker sees **only** the brief text plus the working tree — no chat history, no shared context.
It also auto-loads the repo's `AGENTS.md` / `CLAUDE.md` into its system prompt, so a brief carries
only what those cannot know. One task per brief. Each brief must state, in plain text:

- **Goal** — the single outcome, stated as a verifiable result.
- **owns:** / **needs:** — the files this task may write and the task ids that must land first
  (rules live in §4). An entry may be a literal path, a path ending in `/` (everything under that
  directory), or a shell pattern containing `*`, `?` or `[` — for example `src/voice/*.test.ts`
  for a file whose exact name is not known yet. `flywheel validate` matches all three forms the
  same way at owns-check time, and `flywheel lint` checks a pattern by globbing it against the
  worktree instead of statting a literal path, so a pattern that currently matches nothing is
  reported the same as a missing path. A literal path the unit will create carries the annotation
  `(new)` — `src/voice.ts (new)` — and `flywheel lint` skips the existence check for it. An entry
  starting with `!` is an exception: `owns: apps/inc/**, !apps/inc/wake.h` owns every header but
  `wake.h`. A negated entry takes the same three forms, so it can be a literal path, a `dir/` or a
  pattern. It is matched the same way at validate time and by the dispatch owns-collision check,
  so the unit does not collide with another in-flight unit that owns `wake.h`. Lint never checks
  a negated entry for existence, and warns when no positive entry covers it. List every
  file a unit may create up front, in `owns:`, rather than inviting it to add one later. (`needs:`
  takes a comma-separated list, or one line per id; `needs: none` — or no `needs:` line — means no
  dependencies). An optional `line: <name>` puts the unit on a product line from `.flywheel/config.json`
  `lines` (otherwise the line whose `owns` cover the unit's is used).
- **exclusive:** — an optional **named resource** this task alone may hold while it runs: a shared
  database, build cache or device. `flywheel run` refuses (exit 6) a dispatch whose `exclusive:`
  name an in-flight task already holds, before any event is recorded — the same guard `owns:`
  gives files, but for a resource, not a file: files are `owns:`. `--allow-overlap` dispatches
  anyway and records the crossing on the dispatched event's note, so a deliberate overlap stays
  visible in the ledger.
- **needs-state:** — machine state the gates need that the repo does not carry: a database, a
  local stack, git-ignored env files. A repo-relative path or directory (trailing `/` for a
  directory), comma-separated or repeated across lines. `flywheel validate` refuses, before
  running any gate, when a declared path is missing from an isolated `--workdir`; give it with
  `--carry <path>` (repeatable) to copy that state from the repo into the workdir first. With no
  `--workdir` the declaration is a no-op — the tree already is the repo.
  An entry annotated `(link)` — `needs-state: node_modules/ (link), apps/web/node_modules/ (link)`
  — is also linked from the repo into the task's worktree on every `flywheel run --worktree`
  dispatch (a directory junction on Windows, a symlink elsewhere), before the `worktree.setup`
  command runs there (issue #430). A linked dependency tree is **shared** with the repo and every
  other worktree: never `npm install` (or `pip install`) in it. A unit that changes dependencies
  runs its own install in `worktree.setup` or a gate, into a tree it does not link.
- **gate:** lines — the header carries one or more `gate:` lines, each a single shell command
  that `flywheel validate` runs to re-measure the brief's claims on the exact tree as built; a
  brief without one is refused. The `gate:` lines list **every** gate the gauges must run on the
  built tree, including the project's default build/lint/test commands — repeat them from
  AGENTS.md/CLAUDE.md. (The "state only non-default checks" rule below applies to the brief's
  Checks section for the worker, not to gates.) For Go projects built on one OS, add cross-OS vet
  gates (`GOOS=linux go vet ./...`, `GOOS=darwin go vet ./...`): files behind a
  `//go:build !windows` constraint are never compiled on Windows, and a missing import there passed
  every local gate and failed CI.
- **live-gate:** lines — optional, alongside `gate:`, for a unit whose deliverable is a
  provider-facing contract that a mock cannot prove. A `live-gate:` command runs ONLY in the
  lead's verification pass, via `flywheel validate <task> --live`, never in the worker's own
  dispatch or an ordinary `flywheel validate <task>`. A `pass` verdict is refused (T3) until a
  passing live reading exists on the same tree.
- **gate[quiet]:** / **live-gate[quiet]:** — the same one-command line for a gate that host
  contention distorts (device timing, hardware in the loop). It waits, up to
  `limits.quiet_wait` (default `30m`), until no other task's worker runs on this host, and
  `flywheel run` refuses new dispatches while it runs; a host that never goes idle records the
  reading `inconclusive` (`host busy: <tasks>`), never a failure. `flywheel lint` warns on any
  other `[marker]`.
- **No long-running or silent commands** — a brief never instructs a worker to run a gate that
  builds an app before crawling, or any command that emits nothing for minutes: the stall
  detector then reads the worker as hung. One unit died twice with `rc=-1 reason=stalled` at 4
  and 5 steps this way. The lead runs the gauges — asking the worker to verify by running the
  gate breaks that separation and trips the stall detector.
- **Working directory** — the first body line names the absolute path the worker may touch: "Work
  only in <abs path>". The brief file lives inside that directory (`<workdir>/.flywheel/briefs/`),
  never in another checkout — a brief attached from a different checkout made a worker edit that
  other checkout instead.
- **Named parts** — for a large file, request it in named parts ("file f: part 1 — those ranges,
  then part 2 — ..."), building after each part, so no single write passes the 120-line budget.
- **Exact change** — what to modify and the intended approach; leave no ambiguity about scope.
- **Don't-touch list** — every file with uncommitted, in-flight changes the worker could clobber
  (the orchestrator's own work and any other worker's; see §4).
- **Write rule** — every brief includes, verbatim, "At most one write per response and at most 120
  lines per write; batch read-only calls (read, grep, glob) together in one response." Output caps
  come from large writes, never from reads; serialising reads made every read its own round trip
  (measured: 23 s per step, only 44 % of it in the model). A worker that drafts a whole large file
  in one response hits the model's output cap; the run exits rc 0 with the last `step_finish`
  reason `length` and nothing written. In the field run all three output-cap failures came from
  briefs without this rule, and none happened after it was added.
- **Task-specific tests** — tests only this task can define, beyond what the repo's documented
  gates already cover.
- **Report contract** — what to return: files changed, tests run, exact output of those tests,
  exit status, and anything it left undone or uncertain, plus a "Findings outside `owns:`"
  section: real problems noticed outside the task, reported not fixed. One such finding became
  a new task.
- **Plan check-in** — the brief says "before step 20, state your plan in one text message in this
  fixed shape, one line each, verbatim:" followed by `PLAN files-to-read: ...`,
  `PLAN files-to-change: ...`, `PLAN order: ...`, `PLAN checks: ...` (flywheel also recognises the lines
  when markdown-formatted or preceded by prose, but ask for plain text at the start of a message),
  so the orchestrator can check direction without interrupting (a 53-step exploration was otherwise
  unreadable). A run that reaches step 20 with no `PLAN `-prefixed line is flagged with a `no-plan`
  event (issue #65, #284).
- **Moves and renames** — when a task moves or renames a file, grant "files that reference it
  (list them with grep first)" in `owns:`. Moves break every test that reads the file by path;
  workers handled it correctly, but had to flag it instead of being allowed.
- **State-changing routes** — require one failure-injection test per state-changing route (see
  the traps in §6).
- **Docs tasks** — "document what is in the code; flag what is not". A docs worker told to
  document a parallel task caught a code/doc mismatch this way; the docs task becomes a cheap
  second reviewer. A docs task's gates include one that resolves every path or symbol the
  document names, never only a grep for removed strings.
- **Build after each file written** — run a build (or typecheck) after every file you write so a
  broken intermediate state surfaces immediately; run the full tests and the formatter once, at
  the end (each is a slow tool call).
- **Library APIs** — for a task that needs specific library APIs, list them in the brief; the
  worker looks those up with the language's doc tool, never by reading or grepping library source.

Large scope per brief is fine; large single writes are not.

The header of a brief looks like this:

```text
owns: skills/flywheel/references/worker-brief.md
needs: none
gate: go build ./... && go vet ./...
gate: d=$(mktemp -d) && go test -c -ldflags=-buildid=fw$(date +%s%N) -o "$d/t.test.exe" ./<pkg> && (cd <pkg> && "$d/t.test.exe")

Work only in /abs/path/to/the/build/tree
```

State a check command explicitly only when the task needs a **non-default** check: the worker already
knows the documented build/lint/test from `AGENTS.md`/`CLAUDE.md`, so a brief that restates them is
dead weight (this cut one brief from 166 to 81 lines with no loss of quality). That "state only
non-default checks" rule applies to the brief's Checks section, never to `gate:` lines. Example from
a real session: the repo doc said only `clippy`, but plain `cargo clippy` misses lints in test code —
the brief had to say `cargo clippy --all-targets`.

Keep it bounded: a bug fix, a single feature slice, one migration. If a request is bigger than one
brief, split it and run the pieces as separate, ordered tasks. For a large task, deliver it as
**increments** — one delta per increment — each increment ending with its checks, a short report
and STOP. Evidence: a worker's first edit moved from step 56 to step 2 once the task was split,
and each increment's first edit came within three steps.

## 2. Dispatch: canonical `flywheel run`, the worker adapter's own CLI as fallback

The first choice is `flywheel run <task>` after `flywheel log --task <id> --kind planned --brief
<path>`: it attaches the brief with `--file`, applies the deny policy, and records every event
(`flywheel run <task> -h` for its flags). Dispatch each increment with `flywheel run <task> --increment N`: a fresh session told to do increment N only; the attempt is an ordinary r<n> and the dispatched event records the increment. The brief must define increment N (an `## Increments` list with item N, or an `Increment N` heading), or the dispatch is refused. Keep the worker adapter's own CLI as the hand-built
fallback — e.g. dispatching one increment of a brief. Hand-built dispatches add `--variant low`:
on large increments the default reasoning variant spent 17-30 k reasoning tokens planning in one
step and hit the output cap with nothing written (3 of 3 attempts); `--variant low` did the same
increment in 493 s with at most ~1 k reasoning tokens per step.

> **OpenCode adapter note.** In phase 1 the worker adapter's CLI is OpenCode. Everything from here
> to the end of §2 — the flags, `OPENCODE_CONFIG`, the run-file format — is OpenCode-specific. A
> different worker adapter needs its own equivalents; look them up from that CLI's own help, never
> from memory. The `claude` adapter (issue #49) is one such worker: it drives the Claude CLI's own
> `-p`/`--output-format stream-json` flags directly and carries no `OPENCODE_CONFIG` permission
> policy — see `claudeAdapter` in `internal/flywheel/adapter.go` for its exact command and parsing.

Before relying on raw `opencode run` options, run:

```bash
opencode run --help
```

A **fresh run** must label the task with `--title "<id>-r1"` (human-readable), auto-approve
permissions with `--auto`, and emit `--format json` so the session id comes back in the output.
Every dispatch — fresh and resume — sets `OPENCODE_CONFIG` to the worker permission policy, which
denies the tree-rewriting git commands (§9), then dispatches the brief **as a file**, never on the
command line: the message first, then the brief attached with `--file`. On Windows the npm
`opencode.cmd` shim sends arguments through cmd.exe, which cuts a multi-line argument at the first
newline (a worker once received only the brief's first line) and is a command-injection risk;
Windows also caps a command line near 32K characters. The message must come BEFORE `--file` (an
array option that swallows later positionals; even `--file=path` does). Resumes carry `--title`
too, so a resumed worker can be found by its title; `flywheel run` (#20) does all of this itself
once it lands:

```bash
mkdir -p .flywheel/runs
OPENCODE_CONFIG=skills/flywheel/references/worker-permissions.json \
  opencode run --pure -m "$MODEL" --auto --format json --title "<id>-r1" --variant low \
  "Follow the attached brief exactly." --file .flywheel/briefs/<id>.txt < /dev/null > .flywheel/runs/<id>.r1.jsonl; rc=$?
```

`OPENCODE_CONFIG` loads the worker permission policy
(`skills/flywheel/references/worker-permissions.json`, relative to wherever the skill is installed;
consumers may copy the file into their repo and point the variable at the copy). In that file the
catch-all `"*": "allow"` comes **first** and the `deny` rules after it, because OpenCode's **last
matching rule wins** — with the catch-all last, nothing is denied (verified 2026-09-13, OpenCode
1.18.30). A `deny` blocks the command even under `--auto`, and OpenCode checks **each command in a
chain**, so `cd . && git stash list` and `echo ok; git reset --soft HEAD` are denied too.
`git -C .` slipped past a policy without a `git -C*` rule, which is why the policy below also
denies `git -C*`, `git --work-tree*` and `git --git-dir*`. `flywheel run` attaches the brief from
inside the worktree and the worker policy denies external directories (issue #87).

- `-m "$MODEL"` is the **approved default**. Never switch providers or models silently, and never
  assert a metered model is free. Cost is small but real: a one-line probe on the approved model on
  2026-09-12 used 46,324 tokens, 46,310 of them cache reads, and cost $0.0004; the field run used
  32.7 M fresh input tokens vs 289 M cache reads (~90 % of input), 0.7 M output, and 1.5 M reasoning
  across 99 runs. An earlier measurement on a different setup saw no caching at all. Check
  `part.tokens.cache.read` on `step_finish` events in your own runs rather than trust either number.
  If cost is a question, ask the human which models are on their flat-rate plan before dispatching.
- `--auto` is **required for non-interactive dispatch**: without it the worker hangs on a permission
  prompt nobody can answer the first time it tries to write a file. If you prefer not to auto-approve,
  configure `opencode.jsonc` permission settings as the narrower alternative.
- `--pure` runs without external plugins, so the worker always gets OpenCode's default `build` agent
  whatever the user's global config loads. In one run a global plugin replaced the agent and both
  workers looped on reads without editing; `--pure` fixed it. If a project needs a plugin, pin the
  agent with `--agent build` instead and watch for the same loop. Resuming a session first started
  without `--pure` under `--pure` works — it held across ten dispatches in a consumer run, fresh
  and resumed. In another setup the same global plugin did not swap the agent, so the effect depends
  on the plugin and its config; `--pure` removes the variable either way. If a plugin is what
  supplies provider auth, `--pure` drops it: set credentials with `opencode auth login` instead.
  `flywheel run` writes the worker rules to `.flywheel/worker-rules.md` (once, never overwriting)
  and points the embedded config's `instructions` at it (issue #31), so every run loads them without
  a tool call — `instructions` applies under `--pure` too, since `--pure` skips only plugins.
- `--title "<id>-r1"` gives the fresh run a human-readable label — and is the kill handle in §3;
  resumes carry `--title "<id>-c<n>"` so a resumed worker is found by its title too;
  `--format json` is what emits the **actual session id** in the output.
- `< /dev/null` closes stdin and is required on every dispatch and every resume. In a non-TTY shell
  (an agent's shell tool, CI), `opencode run` waits on an open stdin and writes nothing after
  startup, which looks exactly like a stall. Run dispatches from bash (Git Bash on Windows);
  PowerShell 5.1 has no `/dev/null` and no `&&`.
- `rc=$?` captures the **actual exit status** — save it; it is evidence.
- Run files are per attempt: `.flywheel/runs/<id>.r1.jsonl` for the first fresh run (`r2` if you
  ever re-dispatch fresh) and `<id>.c1.jsonl`, `<id>.c2.jsonl`, ... for each correction resume.
  Per-attempt steps, tokens and finish reasons then stay separate, so a correction never pollutes
  the fresh run's stats.
- Read the run file and record the emitted `sessionID` — every JSONL event carries it:

  ```bash
  grep -o '"sessionID":"[^"]*"' .flywheel/runs/<id>.r1.jsonl | head -1
  ```

  That value — and only that value — is what you pass to `--session` later. `--session` accepts an
  existing emitted session id, never an invented `flywheel-<id>` string.

## 3. Run states and failures

Each line of the run file is one event with a top-level `type` and `sessionID`. Verified on a probe:
`step_start`, `text`, `step_finish`. Tool calls add tool events; failures appear as `error` events
(field run). `step_finish` carries `part.reason` (`stop` on a normal finish, `length` when the
output cap was hit), `part.tokens` `{total, input, output, reasoning, cache: {read, write}}`, and
`part.cost`.

| state | how to detect | what to do |
| --- | --- | --- |
| starting | no output yet | wait — a healthy run writes its first event within about 30 s (the probe took 25 s end to end). |
| silent | no output after 60 s | check, in order: was stdin closed? is there a provider error in the opencode log? Only then treat it as stalled. |
| stalled | no output for ~10 min while the process is alive | a hung provider stream (a step started but never finished). Stop the process tree by PID, never by name (§3 processes), and redispatch fresh; a quarter-hour without a step_finish is never healthy. |
| running | events arriving | do nothing; let it run. |
| exploring | distinct files read keeps rising, zero edits, no file read over and over | healthy for large tasks — one run read for 53 steps, about 40 minutes, then made 50 edits steadily. Compare the plan message the brief asked for (see §1) with what it is reading; if off course, stop it by PID and resume with a delta, otherwise leave it. |
| off-course | reads or greps of paths outside the task's worktree (library source), or probe files written outside `owns:` | unlike `exploring` (relevant files inside the worktree), stop it by PID and resume with a delta that lists the APIs it needs. |
| long step | events stop for 5-10 min during a large generation | not a stall; do not kill it. |
| read loop | the same file read again and again, no edits (compare `"tool":"read"` with `"tool":"edit"`/`"tool":"write"` counts in the run file) | stop it by PID, check which agent the opencode log shows for the session (`agent=` on its lines), and re-dispatch with `--pure`. |
| capped | rc 0 and the last reason is `length` | rerun fresh with `--variant low` and the file in named parts (the default reasoning variant plans so hard it caps with nothing written — see §2). |
| provider error | an `error` event in the JSONL, or errors only in the opencode log | see §8. |
| rate-limited | finish reason `rate-limited` (a claude 429 or rate/usage/session-limit message); the note names `limit resets <time>` | `flywheel run` already waits for the reset and resumes the same session (`limits.rate_limit_retries`, default 3; `limits.rate_limit_max_wait`, default 5h); if it gave up, resume it yourself after the reset. |
| abandoned-job | finish reason `abandoned-job`: a clean stop that left a background shell it started (Bash `run_in_background`) uncollected, so the job died with the session; the note names `background job never collected: <cmd>` | `flywheel run` already resumed the same session once with `.flywheel/briefs/<task>.job-1.txt` (run the job in the foreground and wait); if it is abandoned again, resume with a delta naming the command and a foreground timeout, or re-run the job yourself. |
| denied | a bash tool `error` event carrying the rule message: "The user has specified a rule which prevents you from using this specific tool call" | the foreman treats a worker trying to get around it as a signal — stop it and triage; never help it around the block. |
| blocked | rc 0, reason `stop`, and a `permission-denied` signal (the claude result line's `permission_denials`, named in the finished note) | the harness denied a tool the unit needs: fix the path or the policy, then redispatch; never help it around the block. |
| no-writes | rc 0, reason `stop`, no permission denial, and nothing written, while awaiting judgement (a floor state from the finished event's empty `wrote`, not a signal; it blocks nothing) | the worker finished without writing a file: read its report; if the unit had to write, resume with a delta or redispatch. |
| done | rc 0 and the last reason is `stop` | review it (§6). |

`flywheel run` now records the off-course signal itself: one `off-course` event, naming the paths, when a read, grep or glob call names the 5th distinct path outside the worktree (issue #72).

A gate or test that fails with "An Application Control policy has blocked this file" is the
**host**, not the code — that message is Windows Smart App Control blocking a freshly built
binary. Rerun, don't rework. A **persistent** block is the host rejecting a freshly built test
binary by its content hash: identical code rebuilds to the identical binary, so rerunning never
helps. The fix is the compile-then-run gate form (`go test -c -o <dir>/x.test.exe <pkg> &&
<dir>/x.test.exe`) or running the gate in CI — never the security setting (#101).

Detection commands:

```bash
wc -c < .flywheel/runs/<id>.<attempt>.jsonl                               # 0 after 60 s = silent
grep -o '"reason":"[^"]*"' .flywheel/runs/<id>.<attempt>.jsonl | tail -1  # stop | length
grep '"type":"error"' .flywheel/runs/<id>.<attempt>.jsonl                 # provider error in the run
grep '<sessionID>' ~/.local/share/opencode/log/opencode.log | tail -20
```

`<attempt>` is the run being classified (`r1` for the fresh run, `c<n>` for a correction).

The shared log is `~/.local/share/opencode/log/opencode.log`
(`%USERPROFILE%\.local\share\opencode\log\opencode.log` on Windows). It mixes every session on the
machine, so filter by session id. `opencode run --print-logs` also writes logs to stderr, which can
be captured per run with `2> .flywheel/runs/<id>.log`.

Provider failures from the field run (each first looked like silence or no progress):

- per-key limit: "Key limit exceeded", visible only in opencode.log; cost about 3 hours.
- credits: HTTP 402 "can only afford N tokens", as a JSONL error event.
- consent gate: the fallback model refused with "requires explicit opt in" (a China-hosted provider),
  discovered only at dispatch.

**Processes.** Never kill `opencode serve` or any opencode process while a dispatch is in flight —
the server is shared, and killing it takes down healthy work (that mistake looked exactly like a
mysterious race condition; it was self-inflicted). Reap orphans only between batches, when nothing
is in flight. On Unix count with `pgrep -x opencode`, never `pgrep -f`: a bare `-f` matches full
command lines, so any brief mentioning opencode inflates the count — observed: 67 apparent
processes when the true state was one server and zero orphans; `pgrep -x` matches the process name
exactly and is immune. A killed background dispatch exits 144, which is expected and not a worker
failure.

There is no zero-process precondition before dispatching. Parallel workers are normal — 7 at once in
the field run — and on 2026-09-12 a dispatch with stdin closed returned in 25 s while two unrelated
opencode runs were in flight. The zero-byte stalls the old text could not explain are most likely
the open-stdin hang from §2; orphan cleanup is hygiene, not the fix.

Other opencode processes may be the user's own work in other repos. Never stop a process you did not
start.

On Windows, `pgrep`/`pkill` in Git Bash see only MSYS processes, not native `opencode.exe`.
OpenCode Desktop can share the `opencode` process name, so never kill by name (`pkill opencode`,
`Stop-Process -Name opencode`, `taskkill /IM opencode.exe`). Find your dispatch by its unique title
and stop that PID only:

```powershell
Get-CimInstance Win32_Process -Filter "Name like 'opencode%'" |
  Where-Object { $_.CommandLine -like '*--title*<id>*' } |
  Select-Object ProcessId, ExecutablePath, CommandLine
Stop-Process -Id <pid>
```

The CLI binary is `node_modules\opencode-ai\bin\opencode.exe` under the npm global prefix; check
ExecutablePath before stopping anything.

## 4. Concurrency: disjoint file ownership, preserve dirty edits

Parallel workers are allowed only under **disjoint file ownership**: no two concurrently running
workers may touch the same file. Partition the change set up front and state each worker's owned
files explicitly in its brief. If two tasks would overlap, serialize them or split them differently —
do not let two sessions race on one file.

**Ready filter.** Before each dispatch, a task is ready when every `needs:` task has landed, its
`owns:` is disjoint from every in-flight task's `owns:`, and it shares no choke-point file with
in-flight work. Recompute it from the brief headers every time. A scheduler is not needed; the filter
is (the field run had 44 tasks and 5 choke-point files). A read-only `flywheel next` is planned for
this.

**Early dispatch with a follow-up delta.** Docs, audit and verification tasks whose dependencies are
still in flight can dispatch early. Add this addendum to the brief: "Cover only what has landed in
the working tree; list anything still missing under 'planned, not found'." After the dependency
lands, resume the same session with a short delta to cover the rest (typically 10-15 steps). In a
consumer run this paid off four times and saved roughly one full worker cycle (30-60 minutes) per
task.

**Contract dependency.** Write ownership is not the only dependency. Task B may compile against a
function signature that task A is creating, in a file B must not edit — files are disjoint, tasks are
not. Put the **exact signature** in both briefs, and tell B explicitly: if the contract is missing or
the file is mid-edit, wait and retry the build; never write your own copy, never edit A's file.
Registration files (a `lib.rs`, a module index, a route table) are structural choke points because
almost every task wants to add a line — serialize on them or give one task sole ownership.

**Task manifest (a convention, not tooling).** Have each brief declare two lines at the top —
`owns:` (files this task may write) and `needs:` (task ids that must land first). That makes the two
real coordination mistakes mechanically checkable: two concurrent tasks sharing an `owns:` entry, and
dispatching before a `needs:` task is done. Do not build a graph runner — it violates the skill's own
DRY rule, and coordination was not the observed bottleneck; reliability was.

**Exclusive resources.** Tasks that share a build cache, database or device (for example PlatformIO's
`.pio/`) must not run together even with disjoint `owns:`. Declare an optional `exclusive: <resource>`
header line: `flywheel run` refuses (exit 6) a dispatch whose `exclusive:` name an in-flight task
already holds, before any event is recorded — the same guard `owns:` gives files, but for a **named
resource, not a file**. A deliberate crossing dispatches under `--allow-overlap`, which records it on
the dispatched event's note (`exclusive-overlap: <name> with <task>`) so it stays visible in the
ledger.

**Gate scoping.** Under concurrency, a worker's full-repo gate can fail on another worker's
half-written files. Give workers a scoped gate (the affected tests plus typecheck) and run the full
gate yourself only when nothing is in flight.

**Preserve dirty edits.** The orchestrator's own uncommitted work — and any other worker's
uncommitted work — is not free real estate. A brief's don't-touch list must name every file with
in-flight changes the worker could otherwise clobber. If you can't guarantee disjoint ownership for a
change, don't dispatch it in parallel.

**Shared tree means no tree-rewriting commands.** In a consumer field run, a worker ran
`git stash push` on the whole shared working tree to check whether a typecheck error was its own.
It took three other workers' and the orchestrator's uncommitted edits with it; the pop failed
because another worker had edited a file meanwhile, and the restore that followed overwrote newer
edits and dropped the stash. Another worker saw its edits vanish mid-run. A shared tree makes any
tree-rewriting command — `git stash`, `git checkout`, `git restore`, `git reset`, `git clean`,
`git switch`, `git commit`, `git rebase`, `git merge`, `git cherry-pick`, `git pull`, `git push` —
destructive to other workers' in-flight work, which is why §9 forbids them and every dispatch
carries the deny policy (§2). A per-task `git worktree add` (#45) removes the shared tree entirely.

**Check for orphans between batches.** Orphan accumulation is silent and only shows up as unexplained
stalls later, so a long orchestration session should run the orphan check from §3 between batches —
reap orphans only when nothing is in flight, and never clean up while a dispatch is running.

## 5. Process and session handles

Keep the process handle and the session id for the lifetime of the task:

- **Exit status** (`$?` after `opencode run`): nonzero means the run failed to *execute* (bad args,
  missing binary, auth failure, crash). Zero means the run *completed* — nothing more.
- **Session id** (the `sessionID` emitted in `--format json` output on a fresh run): the handle used
  to resume the same worker context on correction. Never lose it; a correction without the emitted
  session id restarts the worker from zero, and you must never substitute an invented
  `flywheel-<id>` string.

Record rc, session id, and model for every run — in the run file plus `flywheel.md`. Keep helper
scripts and state in the repo, never in a per-session scratch directory: a session restart lost the
field run's helpers.

## 6. Review: exit status + diff, and independent validation

The worker executes tests; **you judge the evidence**. Never accept "tests passed" as self-report:

1. Check the exit status. Nonzero → investigate the run failure before anything else.
2. Read `git diff` against the brief: correct change, no scope creep, no clobbered dirty edits,
   nothing on the don't-touch list touched.
3. **Independent validation when needed**: re-run the gate commands yourself on changes that are
   security-, money-, or schema-sensitive, or whenever the worker's own test output looks suspicious.
   The worker runs tests as part of the work; you re-run them as the judge. You may run these
   validation commands yourself — but any resulting implementation change still goes to the worker.

**Recurring traps:**
- **Gate fails in files I don't own**: usually another worker's in-flight edit. Re-run the gate at a
  quiet point before blaming either task.
- **Edits outside `owns:`**: a rename or tooling workaround can force them. Compare
  `git diff --name-only` with the brief's `owns:`, then accept after review or send a correction.
- **Tests that pass as a superuser but fail under the production role**: row-level security and
  permission bugs hide there. Check which role the tests run as.
- **Assumptions the worker never questions** (multi-tenant isolation, token lifecycle, idempotency).
  In the field run, diff review caught a cross-account existence oracle, a token-loss design flaw,
  and a revoke that deleted the row the revoked state depended on. No worker flagged any of them.
- **Debug and test-only surfaces under the production flag**: look for them in every auth diff. A
  test-only token endpoint was still built under the production flag, so anyone could mint a
  session; no worker flagged it.
- **Contract prose that disagrees with its test rows**: cross-check them. Two contract rows
  contradicted each other about the same header.
- **Error paths that fall through**: for every `catch` that sends a response, check that it returns,
  and inject one store failure per state-changing route. A handler sent the error but carried on to
  close a device socket, publish a change event and journal a revoke that never happened; its own 72
  tests passed because none injected a failure.

## 7. Correct, don't implement

When the diff fails review, the orchestrator **sends a correction to the worker** — it does not write
the implementation itself. Resume the worker's session using the **emitted session id** with a delta
brief (only the correction, not a restated task):

A correction to work the session **just did** may resume it. A late or small fix, or one touching
other files, goes to a **fresh** session with a self-contained brief: the owns/needs header, the
defects, and one test per defect. Evidence: a resumed session grew from 104 k to 279 k tokens and
from 18 to 63-79 s per step, while fresh fix sessions ran 11-36 steps in 96-431 s.

- **Corrections cite evidence:** a correction quotes the gauge or run id (e.g. `validated` on
  `<id>.r1`) and the exact failing lines, and names the suspected cause; it never restates the
  task. The worker gets the same evidence you judged, so corrections cite evidence, not a
  summary of it — in a consumer's waves every such correction produced a precise root-cause fix.

```bash
OPENCODE_CONFIG=skills/flywheel/references/worker-permissions.json \
  opencode run --pure -m "$MODEL" --auto --format json --title "<id>-c<n>" --variant low --session "<emitted-sessionID>" \
  "Apply the attached correction to the same task." --file .flywheel/briefs/<id>.delta.txt < /dev/null > .flywheel/runs/<id>.c<n>.jsonl; rc=$?
```

Each correction resume writes a new attempt file (`c1`, `c2`, ...) with `>` — never `>>` — so
per-attempt steps, tokens and finish reasons stay separate (§2). Large tasks are delivered the same
way, as **increments** — one delta per increment, each ending with its checks, a short report and
STOP — and the next increment resumes the same session with the next delta.

`--auto` is required here too (same non-interactive permission prompt), and stdin must be closed per
§2.

Review the result again. Repeat until the diff passes. If you ever find yourself typing the fix, you
have broken the loop — stop and dispatch it instead.

## 8. Blocker protocol: do not take over

If the worker is unavailable — `opencode` CLI missing, model unauthenticated, session cannot be
resumed, the approved worker model is not reachable, or a provider failure (§3) — **report the
blocker and halt**. Do not implement the task yourself to "keep moving". Surfacing the blocker is the
correct outcome; silently taking over violates the orchestrator/worker boundary.

**Recovery.** With the user's explicit OK, resume the same session on a different model by changing
only `-m` (`--session <emitted id> -m <other model>`). The worker keeps its context; this rescued the
field run after credit exhaustion. Never pick the fallback yourself, and never accept a consent gate
(China hosting, training on request data) on the user's behalf.

## 9. Hard rules

- Workers never rewrite the shared tree or index; dispatches carry the deny policy (§2).
- No commits or pushes unless the user asks; standing instructions in the repo's `CLAUDE.md` or
  `AGENTS.md` count as asking. Workers never commit.
- No secrets, keys, tokens, or credentials in a brief or on any command line.
- DRY: drive the `opencode` CLI directly. Do not copy scripts, do not scaffold a framework. The
  `opencode-delegate` skill is an optional integration you may call; it is never something to clone.
- On OpenCode Go, keep "Allow models that train on request data" off when the worker reads a private
  repo.
