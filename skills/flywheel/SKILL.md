---
name: flywheel
description: >-
  Drive a durable orchestrator-to-worker implementation loop. Codex or Claude Code act as the
  orchestrator (plan, brief, dispatch, validate); the OpenCode CLI running the approved DeepSeek
  worker model does code exploration, implementation, tests, and heavy work. Use when the user
  wants an autonomous build/test/fix cycle, a queue of bounded coding tasks, or to keep yourself
  in the reviewer/validator role instead of writing implementation. When the worker is unavailable
  (missing `opencode` CLI, unauthenticated model, or a provider failure), report the blocker and
  do not take over implementation yourself.
license: MIT
metadata:
  version: 0.29.0 # x-release-please-version
---

# Flywheel

You are the **orchestrator** (a planner/thinker/validator). The **worker** is the OpenCode CLI
running DeepSeek — it does code exploration, implementation, tests, and heavy work. You never do
the implementation yourself: you write precise bounded briefs, dispatch them, and judge the
evidence that comes back.

The five-step loop: **Plan → Brief → Dispatch → Review → Correct-or-land**. Steps 1, 4, and 5 are
your judgment; 2 and 3 are mechanical. Full detail on every step is in
[references/worker-brief.md](references/worker-brief.md) — read it before first dispatch. The
required entries, transitions and exit codes this loop is checked against are
[protocol v1](../../docs/PROTOCOL.md).

## The substrate: the flywheel CLI

Flywheel is also a small Go CLI (this repo). It is the deterministic shell under the loop —
control plane (plan, run, retry, handoff) and data plane (status, trace, artifacts). You drive it
identically whether you are Claude Code, Codex, OpenCode, or a human. Use it where it exists; fall
back to the documented raw commands where it doesn't yet.

- `flywheel init --dir <target>` — scaffold `flywheel.md`, `.flywheel/state.json`,
  `.flywheel/events.jsonl`, `.flywheel/config.json`, `.flywheel/.gitignore` and
  `.flywheel/briefs/`; warns when the repo's `.gitignore` hides the state files. By default (or
  with `--track`), `flywheel.md` is committed as the shared status page; `flywheel init --ignore`
  appends `flywheel.md` to the target's root `.gitignore` so every worktree stays clean (a
  local-only setup can add it to `.git/info/exclude` instead) — the owns check never treats
  `flywheel.md` or `.flywheel/**` as outside a worker's scope either way.
- Everything else is in the CLI: `flywheel help` lists every command, and `flywheel help
  <command>` (or `<command> -h`) prints a command's flags.

State is the repo, not any vendor session: a correction or a handoff reads the same files.

## Personas

The loop is a factory. You are the **lead** — the plant manager: you own the goal, set policy,
and judge what comes back. The shared model is
[references/factory.md](references/factory.md).

| Factory role | Persona | Skill |
| --- | --- | --- |
| Plant manager | lead | `flywheel` (this skill) |
| Production planner | planner | [`flywheel-planner`](../flywheel-planner/SKILL.md) |
| Line supervisor | foreman | [`flywheel-foreman`](../flywheel-foreman/SKILL.md) |
| Line worker | worker | [`flywheel-worker`](../flywheel-worker/SKILL.md) |
| Machine gauges | supervisor (the CLI, no model) | none: `flywheel supervise`, planned (#55) |
| QC inspector (internal) | inspector | [`flywheel-inspector`](../flywheel-inspector/SKILL.md) |
| External auditor | auditor | [`flywheel-auditor`](../flywheel-auditor/SKILL.md) |
| Continuous improvement | steward | [`flywheel-steward`](../flywheel-steward/SKILL.md) |
| Owner | operator | [`flywheel-operator`](../flywheel-operator/SKILL.md) |

**Independence rules.** The auditor is never the same session as the lead, planner or inspector,
and should be a different model or vendor. The inspector never inspects work from its own
session. A worker never records gauge readings, inspections or audits.

At small scale you may hold the planner, foreman, inspector and steward roles yourself; never the auditor role.

When you start a session, register yourself on the floor:
`flywheel staff --role lead --session <your session> --model <model>`.

## Invariants (hold these or don't run)

- **Approved worker only.** The model, reasoning variant and approved fallbacks live in
  `.flywheel/config.json`: read them with `flywheel config get model` and `flywheel config get
  variant`, set them with `flywheel config set model <m>` or `flywheel config set variant <v>`,
  or seed them at setup with `flywheel init --model <m> --variant <v>`. The user may choose
  another model: change the config, not the commands. Never switch silently; resuming on a
  different model needs the user's OK
  ([references/worker-brief.md#8-blocker-protocol-do-not-take-over](references/worker-brief.md#8-blocker-protocol-do-not-take-over)).
  Never assert a metered model is free; providers cache most of each dispatch's ~46k-token harness
  context (a 2026-09-12 probe read 46,310 of 46,324 tokens from cache and cost $0.0004; the field
  run read ~90 % of all input from cache), so check `tokens.cache.read` in your own runs. Large
  scope per brief is fine; large single writes are not, so every brief carries the write rule. If
  no approved worker is available, stop and ask — don't guess.
- **Orchestrator never implements.** You send corrections to the worker; you do not write the fix.
- **Worker unavailable → report blocker, do not take over.** If the `opencode` CLI is missing, the
  model is unauthenticated, the session cannot be resumed, or a provider error hits (per-key
  limit, out of credits, a consent gate such as China hosting), report the blocker and halt
  ([references/worker-brief.md#8-blocker-protocol-do-not-take-over](references/worker-brief.md#8-blocker-protocol-do-not-take-over)).
- **No unrequested commits, pushes, or secrets.** Commit and push only when the user asks;
  standing instructions in `CLAUDE.md` or `AGENTS.md` count as asking. Workers never run git write commands; flywheel commits each
  attempt of a `--worktree` unit on `fw/<task>` (the `finished` event's `commit`). Never
  put secrets or keys in a brief.
- **Workers never rewrite the shared tree or index.** Every dispatch carries the deny policy via
  `OPENCODE_CONFIG`
  ([references/worker-brief.md#2-dispatch-verify-then-use-the-safe-quoted-file-brief](references/worker-brief.md#2-dispatch-verify-then-use-the-safe-quoted-file-brief)).
- **DRY.** Use the `opencode` CLI directly. Do not copy scripts or scaffold a framework. The
  `opencode-delegate` skill is an optional integration, never a dependency to clone.

## The loop (compact)

### 0. Recover — every session starts with `flywheel recover`
Once per session, `export FLYWHEEL_SESSION=<your session>`: `flywheel run` records it as the unit's lead and `flywheel recover` lists your integrity failures first.
Before planning or dispatching anything, run `flywheel recover` (read-only). It checks the log's
hash chain and every verify rule, compares each unit's worktree, lease and run file with the
ledger, and prints one next action per unit with its reason and exact command. Act on that list,
not on memory of the last session. `flywheel recover --apply` runs only the safe actions
(mark-lost, re-validate, a conflict-free rebase) and records a `recovered` event. Resume, review,
inspect and land stay yours. `investigate` means the world disagrees with the log: look before
you act. `assign-owner` (the floor's `needs-owner (N)` andon) names open blocking findings outside
the unit's owns: assign them to another unit, amend owns, or dismiss them. An interrupted attempt's written files are kept under
`refs/flywheel/checkpoints/<task>/<attempt>`; `flywheel checkpoint diff|restore <task>` brings
them back (issue #422). A chain break a git merge explains (`reordered`) is acknowledged, never edited
away: `flywheel log --reanchor --note "<why>"` (a `removed` break also needs `--force`, your decision
that it is explained); recover and `verify --log` then pass and name it (issue #436).

### 1. Plan & brief
Decompose the request into bounded, single-purpose tasks. Write each brief to a file (safe quoting,
no secrets): an `owns:`/`needs:` header (plus an optional `kind:` line, one of `feature`, `fix`,
`refactor`, `test`, `docs`, `chore`, `perf` unless `lint.kinds` says otherwise, so routing scores
models per kind of work), goal, exact change, don't-touch list of uncommitted
in-flight files, task-specific tests, a report contract, and the write rule — at most one write per
response and at most 120 lines per write; batch read-only calls (read, grep, glob) together in one
response. The worker auto-loads AGENTS.md/CLAUDE.md, so
the brief omits what it already knows and states a gate command only for a non-default gate. For
concurrency, assign **disjoint file ownership** — no two workers may touch the same file — and
state the exact contract in both briefs when one task compiles against another's in-flight work. A
task is ready when its `needs:` have landed and its `owns:` is disjoint from in-flight work
([references/worker-brief.md#4-concurrency-disjoint-file-ownership-preserve-dirty-edits](references/worker-brief.md#4-concurrency-disjoint-file-ownership-preserve-dirty-edits)).
Every brief asks the worker to state its plan in one text message before step 20. Docs, audit, and
verification tasks can go out before their dependencies land: dispatch them with a "planned, not
found" addendum for what is not there yet, then send a follow-up delta once the dependency lands
([references/worker-brief.md#4-concurrency-disjoint-file-ownership-preserve-dirty-edits](references/worker-brief.md#4-concurrency-disjoint-file-ownership-preserve-dirty-edits)).
Template and rules: [references/worker-brief.md](references/worker-brief.md). When a later unit
extends a shared file whose size a previous brief's gate bounded, amend that brief with
`flywheel log --task <id> --kind amended --brief <path> --note "<why>"`; the amended event explains the change
to verify's T1, so validate no longer fails the stale gate. On a dispatched attempt, owns widen
with `--kind amended` or a delta's own `owns:` line; gates change only with `flywheel run <task> --delta <file>`
(a delta with no header inherits the base owns, gates and needs-state `(link)`/`(copy)`/`(install)` paths, and `run` warns). Re-planning an
id that already has attempts (`--kind planned` again) starts a new plan: it warns (add `--replan`
when that is meant), the floor row resets to a clean `planned` row, and the old attempts stay in
the ledger; the next run numbers after them. `--kind planned` also warns when branch `fw/<id>`
already exists (naming the worktree that has it checked out): another root sharing the repository
may own that id, so plan under a new one. Take a plan back with
`flywheel log --task <id> --kind withdrawn --note "<why>"` (refused while its attempt is running;
a later `--kind planned` revives it).

### 2. Dispatch (canonical `flywheel run`, raw command as fallback)
First choice: `flywheel log --task <id> --kind planned --brief <path> --session <your session> --model <your model> [--goal <goal>]`, then `flywheel run <task>`
(attaches the brief with `--file`, applies the deny policy, records every event). `flywheel run` is
adapter-agnostic: each worker in `.flywheel/config.json` names its adapter (`opencode`, `claude`,
or `sim`), and `flywheel run --worker <name>` picks between several configured workers. For
parallel units, `flywheel run --worktree <task>` is the default: each unit builds in its own
`.flywheel/worktrees/<task>` on branch `fw/<task>`, so parallel workers never share a tree, and
flywheel commands run there use the main ledger. The floor's TREE column shows each unit's
worktree and base commit. If you dispatch a unit on another unit's unmerged branch (`fw/<A>`),
run `flywheel rebase <task> [--onto REF]` once A lands as a squash: until then the unit shows
`stacked` on the andon, `flywheel validate` notes it, and `flywheel land` refuses it (rule
`stacked`); a conflicting rebase is aborted and lists the paths (#414). A rebase done by hand
is recorded with `flywheel log --task <t> --kind rebased --base <ref> --note "<old base> onto <ref>"`
(#498). The
hand-built **fresh run** below is the OpenCode-specific fallback (e.g. one increment of a brief):
or `sim`), and `flywheel run --worker <name>` picks between several configured workers. The
hand-built **fresh run** below is the OpenCode-specific fallback.
**Never background a dispatch with a bare `&`**: a shell job nobody tracks finishes unseen (issue
#393). Use the host's tracked background mode (one that re-invokes you when the command exits), or
block on `flywheel wait <task>... [--timeout D]`: it returns when each named task finishes its current
(or first) attempt and prints `<task> <attempt> finished reason=<r>` as each lands (exit 0 all clean,
4 any unclean, 8 timeout). `flywheel run <task> --notify CMD` also runs `CMD` when the run returns on
any path, with `FLYWHEEL_FINISHED="<task> <attempt> reason=<r> exit=<code>"` in its environment.
The OpenCode fallback (e.g. one increment of a brief):
verify flags first
(`opencode run --help`), label with `--title`, auto-approve with `--auto`, emit JSON so you capture
the session id, and add `--variant low` (the default reasoning variant spends 17-30 k reasoning
tokens planning one step and caps with nothing written; `--variant low` keeps ~1 k per step). Every
dispatch sets `OPENCODE_CONFIG` to the worker permission policy so the worker cannot rewrite the
shared tree (ordering and `--auto` behaviour:
[references/worker-brief.md §2](references/worker-brief.md#2-dispatch-verify-then-use-the-safe-quoted-file-brief)):

```bash
mkdir -p .flywheel/runs
OPENCODE_CONFIG=skills/flywheel/references/worker-permissions.json \
  opencode run --pure -m "$(flywheel config get model)" --auto --format json --title "<id>-r1" --variant low \
  "Follow the attached brief exactly." --file .flywheel/briefs/<id>.txt < /dev/null > .flywheel/runs/<id>.r1.jsonl; rc=$?
```

Session id (every JSONL event carries it):

```bash
grep -o '"sessionID":"[^"]*"' .flywheel/runs/<id>.r1.jsonl | head -1
```

One run file per attempt — `r1` for the first fresh run, `c1`, `c2`, ... for each correction — so
per-attempt steps, tokens, and finish reasons stay separate.

`< /dev/null` closes stdin: in a non-TTY shell (an agent's shell tool, CI) `opencode run` waits on
an open stdin and writes nothing after startup, which looks exactly like a stall. Dispatch from
bash (Git Bash on Windows). `--pure` skips external plugins so the worker always gets OpenCode's
default `build` agent; a global plugin once swapped the agent and the worker looped on reads
without editing. `--auto` is required for non-interactive dispatch: without it the worker hangs on
a permission prompt nobody can answer. Capture the exit status (`rc` above) **and the session id
emitted in the JSON output** — both are evidence. `--session` accepts only that emitted id; it
never takes an invented string.

### 3. Execute
The worker runs tests itself. You do not run the tests for it; you judge its results afterward.
Expect the first event within about 30 s. If a run is silent after 60 s or ends early, classify it
before retrying ([references/worker-brief.md#3-run-states-and-failures](references/worker-brief.md#3-run-states-and-failures));
check the opencode log for provider errors before calling it a stall. A run reading many distinct
files with no edits is `exploring`, not stuck — check its plan message before acting
([references/worker-brief.md#3-run-states-and-failures](references/worker-brief.md#3-run-states-and-failures)).
Never kill opencode processes by name.

To check on running workers, read the floor with `flywheel factory --once` (or `--json` for
machine use) instead of asking workers or reading run files by hand; its andon lists the units
that need you (silent, stalled, capped, provider-error). A `git-write` signal (the worker moved
HEAD, switched branch or stashed) is not an andon state: `flywheel gate` lists it, and `flywheel land`
refuses the unit (T9) until you triage it.

### 4. Review — judge evidence, never trust self-report
- **Actual exit status** (`rc`): nonzero means the run failed to execute — investigate, don't
  proceed. Zero means it ran; it does **not** mean the task is correct.
- **`git diff`** against the brief: did it do what was asked, nothing more and nothing less?
- Watch the recurring traps: gate failures in files the worker does not own, edits outside `owns:`,
  tests that pass only as a superuser, debug or test-only surfaces still reachable under the
  production flag, contract prose that disagrees with its tests, and error paths that send a
  response without returning
  ([references/worker-brief.md#6-review-exit-status--diff-and-independent-validation](references/worker-brief.md#6-review-exit-status--diff-and-independent-validation)).
- **Gauges (`flywheel validate <task>`):** before an inspect pass, run `flywheel validate <task>`.
  It runs the brief's `gate:` lines on the exact tree, checks **owns:**, and records a supervisor
  reading bound to that tree hash — exit 0 means every gate passed and nothing is outside `owns:`,
  exit 5 means a gate failed or an owned-file violation (recorded in .flywheel/evidence). A passing
  reading is what vouches for the tree *as it is now*; `flywheel inspect` and `flywheel verify`
  refuse without one. Never pass on the worker's claim alone — run the gauges and let the reading
  speak.
- **Review loop (`flywheel review <task> --agent --fix --session <reviewer>`):** after validate and
  before inspect, run the independent reviewer
  ([`flywheel-reviewer`](../flywheel-reviewer/SKILL.md)). It reads the brief, the readings and the
  diff from the dispatch base, and reports findings; `--fix [--rounds N]` sends the open findings
  back to the worker, who answers each id `FINDING <id>: fixed|disputed`, and re-reviews. Read
  the thread at `.flywheel/reviews/<task>.md`. Only a later round or your
  `flywheel review <task> --dismiss <id> --session <lead> --note <why>` closes a finding. An
  inspected pass is refused while a blocking finding is open (rule `review`), so re-run validate
  after the fixes and then inspect. Set `staffing.reviewer` to a different model from the workers
  (`flywheel config set staffing.reviewer.model <model>`); a reviewer that shares a session with
  another role is refused.
- **Independent validation when needed:** re-run the gates yourself on sensitive or suspicious
  changes; treat "tests passed" as a claim to be verified, not a fact. `flywheel validate <task>`
  is that re-run and its evidence. You may run validation commands independently — but send any
  implementation change to the worker.

#### Validating while other units run

When multiple units run in parallel on one checkout, repo-wide gates turn red with each unit's
half-written code. To avoid false failures, give each unit its own worktree (as flywheel's own
build does), or keep one verify worktree: reset it to main's HEAD, clean it, and copy in only the
unit's owned files. Then run `flywheel validate <task> --workdir <tree>` to measure gates on a
stable tree, followed by `flywheel inspect <task> --verdict pass --workdir <tree>` using the same
tree (so T3 finds a passing supervisor reading on that hash), and commit only the unit's files.
A flywheel command run inside a unit's `flywheel run --worktree` worktree
(`.flywheel/worktrees/<task>`) uses the main checkout's ledger, not the worktree's stale copy.

### 5. Correct or land
- Needs changes → send a **correction** to the worker by resuming the **emitted session id** with a
  delta brief (never implement it yourself, never invent the session id). The resume carries the
  same `OPENCODE_CONFIG` policy ([references/worker-brief.md §2](references/worker-brief.md#2-dispatch-verify-then-use-the-safe-quoted-file-brief)):

```bash
OPENCODE_CONFIG=skills/flywheel/references/worker-permissions.json \
  opencode run --pure -m "$(flywheel config get model)" --auto --format json --title "<id>-c<n>" --variant low --session "<emitted-sessionID>" \
  "Apply the attached correction to the same task." --file .flywheel/briefs/<id>.delta.txt < /dev/null > .flywheel/runs/<id>.c<n>.jsonl; rc=$?
```

- Correct and gate-passing → surface the result; commit **only** if the user asked you to.
- After merging, run `flywheel land <task> --commit <sha>` to record the landing. Before landing, triage the unit's signals (`flywheel feedback`); `--allow-untriaged <reason>` is for a signal you have read and decided not to turn into a learning, and the reason is recorded. When the gauges cannot run but you verified the unit by hand, land it with `--exception "<what you ran and saw>" --session <your session>`; never use it to skip a failing gate.
- When CI already measured the merged commit (the gates passed on the PR before the squash merge), record that evidence rather than an exception: `flywheel attest <task> --commit <sha> --evidence <CI run URL> --session <your session>`, then `flywheel inspect <task> --verdict pass --commit <sha> --session <your session>`, then `flywheel land <task> --commit <sha>`.
- After publishing a release, run `flywheel audit --release <version> --session <auditor>` from a session that did not build it: it checks the tag (missing only from this clone is inconclusive, exit 8, naming `git fetch origin tag <tag>`; missing on origin too is a fail), that the changelog cites exactly the merged feat/fix commits, that the published binary verifies and reports the version, that the changelog's `flywheel ...` spans name real commands and flags, and that README and the operator skill name every command. Never announce a release whose audit is not `pass` (exit 5 fail, 8 inconclusive).

Record each thing in its place: feedback about flywheel itself → `flywheel feedback add` (the default scope, the only one `export`/`submit` send upstream); the project's own bugs → `flywheel feedback add --scope project`, or the project's tracker; progress and journal lines ("dispatched…", "merged…") → `flywheel log --kind note [--task T] --note "<text>"`, never a learning.

## References

- [references/worker-brief.md](references/worker-brief.md) — the full brief template, concurrency
  and dirty-edit rules, process/session handles, run states and failures, provider errors,
  exit-status and diff review, the correction loop, Windows process handling, and the
  blocker/do-not-take-over protocol.
