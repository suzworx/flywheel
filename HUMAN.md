# HUMAN.md — run the factory yourself

[AGENTS.md](AGENTS.md) is the house rules for the agents. This page is for you: a person at a
terminal who wants AI coding agents to do the building while you keep the judgment. You play
the **lead**. You write the work orders, flywheel dispatches cheap disposable agents
(Claude Code, Codex or OpenCode) to build them, the gauges re-measure what the agents claim,
and you decide what lands.

No agent session is needed on your side. Everything below is plain shell commands, and the
state lives in files in your repository, so you can stop at any point and pick up later (or
hand the floor to a lead agent) without losing anything.

New to the words *work order*, *owns:*, *gate:* or *the event log*? Read
[Concepts](docs/concepts.md) first; [Quickstart](docs/quickstart.md) covers installing the
binary.

> Every transcript on this page is real output from one session on `main`, in a scratch repo
> called `greeter`. To keep the page reproducible and free, the agent runs were dispatched to
> the offline `sim` worker described in [Rehearse for free](#rehearse-for-free), so they show
> `sim .flywheel/rehearsal.jsonl` where yours will show `claude claude-haiku-4-5`, and a
> replayed session id and cost where yours will show real ones.

## Who does what

| Step | You | The agent | flywheel |
| --- | --- | --- | --- |
| Plan | write a brief: `owns:`, `needs:`, `gate:` lines, the goal | | lints it, records it, hashes it at dispatch |
| Build | | edits files inside `owns:`, runs the gates, reports | dispatches it, streams and records the run, blocks git writes |
| Measure | | | re-runs every gate on the exact tree, checks `owns:` |
| Judge | read the diff; pass, rework, scrap or escalate | | refuses a pass without passing readings, or from the builder |
| Land | commit, then record the landing | never commits | refuses a landing without a pass or with untriaged signals |

## The loop on one screen

```sh
flywheel log --task <id> --kind planned --brief .flywheel/briefs/<id>.txt --session "$ME"  # plan
flywheel run <id>                                           # an agent builds it
flywheel validate <id>                                      # the gauges measure it
git diff                                                    # you read it
flywheel inspect <id> --verdict pass --session "$ME"        # you judge it
git commit --trailer "Flywheel-Task: <id>" -m "..."         # you commit it
flywheel land <id> --commit <sha>                           # you record the landing
```

`flywheel next` tells you which of these steps is due for which unit, and bare `flywheel` opens
the live floor. The rest of this page walks the loop once, then shows the rework path, a
parallel wave, and handing over.

## Set up once

You need three things: the `flywheel` binary on your PATH ([Quickstart](docs/quickstart.md)),
one agent CLI installed and logged in (`claude`, `codex` or `opencode`), and a git repository.
flywheel shells out to the agent CLI you already use, which keeps its own login; flywheel stores
no API keys.

### Scaffold the factory

```sh
flywheel init
```

```text
added: flywheel.md
added: .flywheel/state.json
added: .flywheel/events.jsonl
added: .flywheel/config.json
added: .flywheel/.gitignore
added: .flywheel/.gitattributes
added: .flywheel/briefs/

factory: ~/greeter
  workers: default (opencode openrouter/deepseek/deepseek-v4-flash-0731, max_parallel 4)
  limits: per_host none · budget none · tokens none · breaker none · rate none
  audit: first-article gate off
  enforcement: agent hooks missing — flywheel init --hooks · git hooks: missing — flywheel init --git-hooks · CI audit missing — flywheel init --ci
  view: flywheel (the live floor) · flywheel watch · flywheel next
next: flywheel log --task <id> --kind planned --brief <path>
```

### Choose the agents you rent

The default worker is an OpenCode agent on DeepSeek. Point it at the agent CLI you have:

```sh
flywheel config set adapter claude             # or codex, or opencode
flywheel config set model claude-haiku-4-5     # e.g. gpt-5-codex for codex
flywheel config validate
flywheel doctor
```

`flywheel doctor` sends one small real prompt through the adapter and prints `<model>: ok`, or
why not: `auth missing`, `credits`, `key limit`, `consent required` or `error`. Fix anything
that is not `ok` before you dispatch; a broken provider shows up here instead of as a worker
that never starts. For a model on your own machine, `flywheel init --local <model>` adds an
OpenCode worker named `local` pointed at Ollama (or `--local-url` for LM Studio or llama.cpp).

Whatever the adapter, the agent can edit files and run its own gates, but it cannot write git
history: `flywheel run` puts a git guard first on the agent's PATH that refuses commit, push,
stash, reset, checkout and the rest, and flags any attempt that moved HEAD anyway.

### Commit the factory, then add the guardrails

The event log is the source of truth, so it must be tracked; `.flywheel/runs/` (the agents'
transcripts) stays ignored. `flywheel init --ci` writes a GitHub job that runs `flywheel verify`
on every pull request. Commit both before you install the git hooks:

```sh
flywheel init --ci
git add flywheel.md .flywheel .github && git commit -m "chore: set up flywheel"
flywheel init --git-hooks   # every commit names its unit; a push re-verifies the units it carries
```

From then on, every commit needs a `Flywheel-Task: <id>` trailer naming a unit that is on
record, planned or later. A push re-verifies each unit its commits name, so a trailer naming a
task that was never planned makes the push fail. Commit a brief with the trailer of the unit it
plans.

### Sign in on the floor

A **session** is just a name, and every judging command asks for one. Pick yours once and reuse
it. The rules compare these names: the session that built a unit can never pass it, and an
auditor must be a session that never planned, built or inspected it. Agent sessions get ids
from their own CLI, so yours is always distinct.

```sh
ME=alex
flywheel staff --role lead --session "$ME" --model human
```

```text
lead alex
```

Optionally, give the work a goal so the floor can say when it is done:

```sh
flywheel goal add 'Ship a greeting script' --id greet --require greet-script
```

## 1. Write a work order

A work order (a **brief**) is one text file under `.flywheel/briefs/`. The agent sees only this
file and the repository, so everything it needs is in here. `.flywheel/briefs/greet-script.txt`:

```text
owns: greet.sh (new)
needs: none
gate: test -x greet.sh
gate: sh greet.sh world | grep -qx "hello, world"
gate: sh greet.sh | grep -qx "hello, stranger"

# TASK: greet-script — a greeting script

Write greet.sh, a POSIX sh script, executable, that prints "hello, <name>" for its first
argument, and "hello, stranger" when called with no argument. Touch no other file.

At most one write per response and at most 120 lines per write; batch read-only calls together.
Before your 20th step, state your plan in one text message.

## Checks

Run every gate: line above. Report the file you wrote, the commands you ran, and each command's
real exit status.
```

What makes a brief an agent can finish:

- **One outcome per brief**, stated so a command can prove it. If you cannot write the `gate:`
  line, the task is not ready.
- **`owns:` lists every file the agent may write**, with `(new)` on files it creates. Anything
  else it touches fails the unit.
- **`gate:` lines are the whole contract.** Include the project's own build and test commands,
  not just the new behaviour. Keep them fast and chatty: a gate that is silent for minutes looks
  like a stalled agent.
- **The write rule and the plan line** keep cheap models from writing huge files in one go and
  let you see early whether the agent understood the task.
- **The Checks section** asks for evidence: commands and real exit codes.

Lint it, then record it:

```sh
flywheel lint .flywheel/briefs/greet-script.txt
flywheel log --task greet-script --kind planned --brief .flywheel/briefs/greet-script.txt \
  --session "$ME" --model human --goal greet
```

Both are silent on success (exit 0). `lint` refuses a brief with no `gate:` line, and so does
`flywheel run`.

Commit your own edits before you dispatch. flywheel notes which files are already dirty at
dispatch, but a clean tree means the owns check measures only what the agent did.

## 2. Put an agent to work

Ask the floor what is due:

```sh
flywheel next
```

```text
1. DISPATCH  greet-script  ready, needs met, capacity 4 free
```

Dispatch it:

```sh
flywheel run greet-script
```

```text
greet-script r1 dispatched sim .flywheel/rehearsal.jsonl
greet-script r1 started ses_test_clean_001
greet-script r1 plan recorded
greet-script r1 report recorded
greet-script r1 finished rc=0 reason=stop model=.flywheel/rehearsal.jsonl steps=2 tokens=285 cost=$0.0040
```

`flywheel run` stays in the foreground until the agent finishes, which for a real unit takes
minutes. A healthy agent prints its first event within about 30 seconds. Each attempt gets its own
label: `r1` is the first fresh run, and `c1`, `c2` are corrections. Everything lands in
`.flywheel/runs/`: the raw stream (`greet-script.r1.jsonl`), the agent's plan
(`.r1.plan.md`) and its closing report (`.r1.report.md`).

Its exit code says how the attempt went:

| Exit | Meaning | What to do |
| --- | --- | --- |
| 0 | the agent finished cleanly | measure it (next section); a clean finish is not a correct one |
| 3 | silent: no output before the start timeout | run `flywheel doctor`; check the agent CLI works on its own |
| 4 | failed: nonzero exit, output cap, or provider error | read the run file and the report, then re-dispatch or correct |
| 6 | refused before dispatch: owns overlap with a running unit, budget, rate | the message names the rule and the fix |
| 7 | stalled: the agent went quiet mid-run | re-dispatch fresh; if it repeats, split the brief |

### Watch it from a second terminal

```sh
flywheel                 # the live floor, interactive
flywheel watch           # every event as one line, as it happens
flywheel factory --once  # one snapshot of the floor
```

In a terminal the floor is interactive, like k9s: `:` switches view (`:units`, `:workers`,
`:andon`, `:events`, `:lines`), `/` filters, <kbd>enter</kbd> explains the unit under the cursor,
`l` shows its log, `?` lists the keys and `q` quits. `flywheel factory --plain` keeps the plain
redraw, and a piped or redirected run prints one snapshot and exits.

```text
flywheel factory
repo  .
refreshed 15:44:16 UTC

floor
  default       claude    claude-haiku-4-5  max 4  busy 0
  rehearsal     sim       .flywheel/rehearsal.jsonl  max 0  busy 0
  lead  alex (human)

units (1)
  TASK               STAGE     ATT  SESSION      MODEL                    STEPS   AGE RUN
  greet-script       finished  r1   ses_test_cl~ .flywheel/rehearsal.jso~    2    0s done

andon (0)

output
  landed today 0  finished 1  first-pass n/a  rework 0.00  tokens 285  cost $0.0040
```

The **andon** lists the units that need you: silent, stalled, capped, provider errors. An empty
andon means leave the agents alone. An agent reading many files without editing is exploring,
not stuck; compare what it reads with the plan it stated before you intervene.

## 3. Check the work: measure, don't trust

The agent says it is done. That is a claim, and a claim is not evidence. Read what it said and
what it did:

```sh
cat .flywheel/runs/greet-script.r1.plan.md .flywheel/runs/greet-script.r1.report.md
git status --short && git diff
```

Then let the gauges re-run every gate on the tree as it is now:

```sh
flywheel validate greet-script
```

```text
validate: brief .flywheel/briefs/greet-script.txt
greet-script gate 1: pass (3ms)
greet-script gate 2: pass (15ms)
greet-script gate 3: pass (9ms)
greet-script file greet.sh: 2 lines
greet-script owns: ok
```

Exit 0 means every gate passed and nothing changed outside `owns:`. Exit 5 means one did not
(the rework section below shows one). Each gate's output is kept under `.flywheel/evidence/`.

Passing gates are necessary, not sufficient. You still read the diff against the brief: did it
do what was asked, nothing more and nothing less? Then give your verdict:

```sh
flywheel inspect greet-script --verdict pass --session "$ME"
```

```text
greet-script inspected pass
```

The verdict is refused (exit 6) unless a passing reading exists for this exact tree. It is also
refused for the session that built the unit, which is why an agent can never pass its own work:

```text
$ flywheel inspect greet-script --verdict pass --session ses_test_clean_001
flywheel inspect: T4: session "ses_test_clean_001" is a worker session of task "greet-script"; use a distinct inspector --session
```

## 4. Land it

Agents never commit. You do, naming the unit in a trailer (the `--git-hooks` commit hook
refuses a commit without one):

```sh
git add greet.sh .flywheel flywheel.md
git commit -m "feat: greet.sh" --trailer "Flywheel-Task: greet-script"
flywheel land greet-script --commit "$(git rev-parse --short HEAD)"
```

```text
greet-script landed 2907b66
```

`land` is refused without an inspected pass on record. Then check the unit's whole record
against the rules, and read its story:

```sh
flywheel verify greet-script
flywheel explain greet-script
```

```text
PASS greet-script T1: every dispatched event matches its brief
PASS greet-script T3: every inspected pass has readings
PASS greet-script T4: no inspected or excepted event from a worker session
PASS greet-script T5: no landed event without a prior inspected pass
PASS greet-script T8: personas are correct
```

```text
# greet-script — landed

- brief: .flywheel/briefs/greet-script.txt (sha256 2c063b64)
- owns: greet.sh
- gates: 3
- planner: alex (human)
- goal: greet
- attempts: r1
- steps: 2, cost: $0.0040
- landed: 2907b66 (tree 82090046)
```

`flywheel explain` goes on to print a timeline of every event, from planned to landed.

## 5. When the work is wrong: send it back

Second unit: add a `--shout` flag. The brief owns `greet.sh`, needs `greet-script`, and gates
on `sh greet.sh --shout world | grep -qx "HELLO, WORLD"`. The agent finishes cleanly (exit 0),
and the gauges disagree:

```text
$ flywheel validate shout
validate: brief .flywheel/briefs/shout.txt
shout gate 1: pass (26ms)
shout gate 2: failed (rc=1)
shout file greet.sh: 3 lines
shout owns: outside README.md
```

Two findings: the flag does not upper-case, and the agent edited `README.md`, which it was never
given. Do not fix it yourself; the fix goes back to the agent. Record the verdict, undo the stray
edit, and write a **delta brief** that states only what is wrong:

```sh
flywheel inspect shout --verdict rework --session "$ME" \
  --note 'gate 2: --shout does not upper-case; README.md is outside owns'
git checkout README.md
```

`.flywheel/briefs/shout.delta.txt`:

```text
# CORRECTION: shout

Gate 2 fails: `sh greet.sh --shout world` prints "hello, world"; it must print "HELLO, WORLD".
Upper-case the whole greeting when --shout is given.

You edited README.md, which is outside owns:. It has been reverted; do not touch it again.

Re-run both gate: lines and report their real exit status.
```

Resume the same agent session with it (`--resume` reads `.flywheel/briefs/<id>.delta.txt` by
default; `--delta <file>` names another):

```sh
flywheel run shout --resume
```

```text
shout c1 dispatched sim .flywheel/rehearsal.jsonl
shout c1 started ses_test_clean_001
shout c1 plan recorded
shout c1 report recorded
shout c1 finished rc=0 reason=stop model=.flywheel/rehearsal.jsonl steps=2 tokens=285 cost=$0.0040
```

`--resume` dispatches to the default worker. If the unit ran on another worker, pass
`--worker <name>` too: a different agent CLI cannot resume another one's session.

Measure again. The gauges now read the brief plus the delta:

```text
$ flywheel validate shout
validate: brief .flywheel/briefs/shout.txt + delta .flywheel/briefs/shout.delta.txt
shout gate 1: pass (68ms)
shout gate 2: pass (64ms)
shout file greet.sh: 3 lines
shout owns: ok
```

Then inspect, commit and land as before. The other two verdicts: `scrap` abandons the unit
(units that need it are blocked), and `escalate` hands it up when the brief is ambiguous or you
cannot reconcile the diff with it.

**Signals.** When an attempt goes wrong in a way worth learning from (silent, stalled, capped,
a provider error, no plan, off course, a git write), flywheel records a signal, and `flywheel land`
refuses the unit (rule T9) until you triage it:

```sh
flywheel feedback                     # list untriaged signals
flywheel feedback add --task shout --severity P2 --signals provider-error \
  --title "..." --observed "..." --evidence .flywheel/runs/shout.c1.jsonl --ask "..."
```

A learning turns friction into something the next brief can avoid. If you have read the signal
and it teaches nothing, `flywheel land <id> --commit <sha> --allow-untriaged "<why>"` records
your reason instead.

## 6. Run a wave in parallel

This is where agents earn their keep: one person, several agents building at once. Plan a batch
of briefs, then ask the floor what can run together:

```text
$ flywheel next
1. WAIT  color  owns greet.sh overlaps lang
2. WAIT  tests  needs lang
3. DISPATCH  usage-doc  ready, needs met, capacity 4 free
4. DISPATCH  bye  ready, needs met, capacity 4 free
5. DISPATCH  lang  ready, needs met, capacity 4 free
```

Units run in parallel only when their `owns:` sets are disjoint and their `needs:` have landed.
Here `color` waits because it would write `greet.sh` while `lang` does, and `tests` waits for
`lang` to land. `flywheel run` itself refuses (exit 6) a unit whose `owns:` overlaps one that is
still running.

Dispatch each ready unit in its own terminal tab, or in the background. `--worktree` gives each
agent its own git worktree (`.flywheel/worktrees/<task>`, on branch `fw/<task>`), so no agent
sees another's half-written files:

```sh
for t in usage-doc bye lang; do flywheel run "$t" --worktree > ".flywheel/runs/$t.out" 2>&1 & done
flywheel      # watch the floor while they work
```

The attempt records its worktree, so `flywheel validate <id>` and `flywheel inspect <id>`
measure that tree with no extra flags. To land a unit, commit in its worktree, merge its branch,
and record the landing:

```sh
git -C .flywheel/worktrees/lang add -A
git -C .flywheel/worktrees/lang commit -m "feat: greet.sh --lang" --trailer "Flywheel-Task: lang"
flywheel land lang --merge
```

`land --merge` is the local queue: one unit at a time, it rebases `fw/lang` onto your branch,
re-runs the unit's gates on the rebased tree, fast-forwards, records the landing and removes the
worktree. A conflict is refused with a correction brief to dispatch
(`.flywheel/briefs/lang.land-delta.txt`) and the conflict markers left in the unit's worktree for
the agent to resolve; you then commit the merge and land again. To land by hand instead, merge the
branch yourself and use `flywheel land lang --commit "$(git rev-parse --short HEAD)"`.

`max_parallel` in `.flywheel/config.json` is how many dispatches `flywheel next` offers at once.
`flywheel run` itself does not count, so staying under it when you launch by hand is up to you.
Let the gauges measure finished units without you asking, once or on a loop:

```text
$ flywheel supervise --once
lang r1 fail
usage-doc r1 pass
```

Then work the inspection queue:

```text
$ flywheel context --role inspector
# flywheel context — inspector

## Needs a verdict or triage
- lang uninspected: finished r1, not yet inspected
- usage-doc uninspected: finished r1, not yet inspected

Worker model: claude-haiku-4-5
```

Without `--worktree`, the agents share your checkout, and a repo-wide gate can go red on another
unit's half-written files. In that case keep each unit's gates scoped to the unit.

To cap spend and pace, set `limits` in `.flywheel/config.json`: `budget.wave_tokens` stops new
dispatches once a wave has used that many tokens, `rate_per_minute` limits dispatches of one
model, and `breaker` pauses a model after repeated provider errors (switching to an approved
fallback if you listed one); once the provider is back, a passing `flywheel doctor --record`
closes the breaker without waiting out the cooldown. `flywheel cost` and `flywheel stats`
report where the money went.

## Rehearse for free

The `sim` adapter replays a recorded agent run instead of calling a model. It edits nothing and
costs nothing, which makes it the safe way to learn the commands. That is how every transcript
on this page was made. Save a recording into the factory:

```sh
curl -fsSL -o .flywheel/rehearsal.jsonl \
  https://raw.githubusercontent.com/suzworx/flywheel/main/internal/flywheel/testdata/clean.jsonl
```

Add a second worker to the `workers` list in `.flywheel/config.json` (the model of a `sim`
worker is the recording's path):

```json
{ "name": "rehearsal", "adapter": "sim", "model": ".flywheel/rehearsal.jsonl" }
```

Dispatch with `flywheel run <id> --worker rehearsal`, make the edit the agent would have made
yourself, and carry on with `validate`, `inspect` and `land`. Rehearsal units are recorded in
the event log like any others, so rehearse in a scratch repository, not the one you ship from.

## Stepping away, and handing over

Before you stop, ask whether anything is left unjudged:

```text
$ flywheel gate
gate: 1 blocker(s)
  greet-script uninspected: finished r1, not yet inspected
  inspect: flywheel validate <task> && flywheel inspect <task> --verdict ... --session <s>; triage: flywheel feedback add --task <task> ... --signals <signal>
```

It exits 6 while a finished unit is uninspected or a signal is untriaged, and 0 (`gate: clear`)
when the floor is clean.

Nothing lives in your head or your terminal: it is all in the repository. Whoever picks up next,
whether you tomorrow, a teammate, or a lead agent with the `flywheel` skill loaded, starts from
the same files:

```text
$ flywheel handoff --stdout
in-flight: none
blocked: none
ready: bye color
untriaged signals: none
model: claude-haiku-4-5
```

Without `--stdout`, the summary is written into `flywheel.md`, the status page you commit.
`flywheel status` gives the counts, `flywheel context` gives the full pack, and
`flywheel verify --all --log` checks every unit's record and that no line of the event log was
edited or removed from the middle.

For a check you did not do yourself, have someone who never touched a unit audit it. The auditor
re-runs its gates in a clean copy and checks its record:

```sh
flywheel audit greet-script --session <auditor>   # refused for any session that planned, built or inspected it
flywheel audit --wave --session <auditor>         # every passed, unaudited unit
```

## When flywheel says no

A refusal is the factory working. The message names the rule and the fix.

| Exit | Meaning | Typical cause |
| --- | --- | --- |
| 0 | ok | |
| 1 | error | a missing file, a broken config, a brief with no `gate:` line (`lint`) |
| 2 | usage | a wrong flag; `flywheel help <command>` prints them all |
| 3 | silent run | the agent printed nothing before the start timeout |
| 4 | failed run | the agent exited nonzero, hit its output cap, or hit a provider error |
| 5 | gauges failed | a gate failed, or a file changed outside `owns:` |
| 6 | rule refusal | a pass without readings (T3), self-inspection (T4), landing without a pass (T5), untriaged signals (T9), an owns overlap, a spent budget |
| 7 | stalled run | the agent went quiet mid-run |
| 8 | inconclusive | `verify` could not see the tree a pass was measured on |

`flywheel help` lists every command; the full table, with what each one does, is in the
[README](README.md#cli).
