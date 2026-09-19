# Quickstart

Flywheel is a factory for AI coding work: a small Go CLI that plans, dispatches, measures and
inspects bounded coding tasks. A **lead** writes a **work order**, a cheap disposable **worker**
builds it, machine **gauges** re-run the work order's gates on the exact tree, an **inspector**
signs it off, and the whole story is appended to a durable event log you own. No vendor session
is the source of truth — the repository files are.

This guide takes you from "I have a binary" to "I have landed one unit through the loop." Every
command in it is real: run them in a fresh git repo and they do what is printed here. A refusal
you hit is the tool working, not a bug — the last section tells you each exit code and the exact
command that fixes it.

## Install it

Download the release for your platform from the [Releases page](https://github.com/suzworx/flywheel/releases):
a zip named `flywheel-v<version>-<os>-<arch>.zip` (Windows binaries ship as
`flywheel-v<version>-windows-amd64.exe.zip`), plus the `checksums.txt` beside it.

Verify the download before trusting it: the printed hash must match the matching line in
`checksums.txt` exactly. If it does not, the download is corrupt or tampered with — re-download,
never install it.

On Linux or macOS, the release publishes four assets — `linux-amd64`, `linux-arm64`,
`darwin-amd64`, `darwin-arm64` — so the block below derives the one for your machine from `uname`
and uses it for both the checksum and the install:

```sh
# set V to the release you are installing
V=v0.14.0
case "$(uname -s)" in Linux) OS=linux;; Darwin) OS=darwin;; *) exit 1;; esac
case "$(uname -m)" in x86_64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) exit 1;; esac
BIN=flywheel-$V-$OS-$ARCH

shasum -a 256 "$BIN.zip"   # compare with checksums.txt
unzip -o "$BIN.zip"
sudo install -m 0755 "$BIN" /usr/local/bin/flywheel
```

On Windows (PowerShell), the zip holds `flywheel-$V-windows-amd64.exe`. Extract it into
`%USERPROFILE%\flywheel` and add that directory to your user PATH, so `flywheel` is callable
from any directory:

```powershell
# set $V to the release you are installing
$V = 'v0.14.0'
Get-FileHash "flywheel-$V-windows-amd64.exe.zip" -Algorithm SHA256
New-Item -ItemType Directory -Force "$env:USERPROFILE\flywheel" | Out-Null
Expand-Archive "flywheel-$V-windows-amd64.exe.zip" -DestinationPath "$env:USERPROFILE\flywheel"
Rename-Item "$env:USERPROFILE\flywheel\flywheel-$V-windows-amd64.exe" flywheel.exe
[Environment]::SetEnvironmentVariable('Path', "$env:USERPROFILE\flywheel;" + [Environment]::GetEnvironmentVariable('Path', 'User'), 'User')
$env:Path = "$env:USERPROFILE\flywheel;$env:Path"
```

Confirm it runs:

```sh
flywheel version
```

When a new release comes out, `flywheel upgrade` self-updates with the same checksum
verification (`flywheel upgrade --check` prints the current and latest versions first). Before
you dispatch anything, `flywheel doctor` probes the model you configured and tells you whether it
is actually reachable — a missing or broken provider shows up there, not as a mysteriously stuck
worker.

## 1. Scaffold the factory

Run `flywheel init` inside a git repository. It creates `flywheel.md` (the shared status page)
and the `.flywheel/` state directory:

```text
added: flywheel.md
added: .flywheel/state.json
added: .flywheel/events.jsonl
added: .flywheel/config.json
added: .flywheel/.gitignore
added: .flywheel/.gitattributes
added: .flywheel/briefs/
next: flywheel log --task <id> --kind planned --brief <path>
```

What each piece is for:

- `flywheel.md` — the status page every head reads to continue the same work.
- `.flywheel/events.jsonl` — the **event log**: one JSON line per event, append-only, the single
  source of truth.
- `.flywheel/state.json` — a read-only projection of the log, never edited by hand.
- `.flywheel/config.json` — the workers and their models.
- `.flywheel/briefs/` — where work orders live.
- `.flywheel/.gitignore` and `.gitattributes` — keep run transcripts untracked and line endings
  stable.

Your `.gitignore` matters: the state files must be **committed** — the log is the only source of
truth — while `.flywheel/runs/` (the transcripts) stays ignored. `flywheel init` warns you when
git would hide a state file; it prints the exact `!.flywheel/...` lines to add so the log, state
and config are tracked again.

## 2. Write the first work order

A work order is one text file with a header and a goal. Create `.flywheel/briefs/hello.txt`:

```text
owns: hello.txt (new)
needs: none
gate: test -f hello.txt
gate: grep -q "Hello from flywheel" hello.txt

# TASK: hello — write a greeting file

Write hello.txt containing the exact line "Hello from flywheel".

At most one write per response and at most 120 lines per write; batch read-only calls together.

## Checks

Report the file you wrote, the commands you ran, and their real exit status.
```

Each header line sets one rule:

- `owns:` — the paths this unit may write. `(new)` means the file may be created; a trailing `/`
  claims a directory; `*`, `?` and `[` make a glob pattern.
- `needs:` — the task ids that must land first; `none` means this unit depends on nothing.
- `gate:` — a shell command that must exit 0 on the built tree. The lead re-runs every gate at
  measure time; the worker's word is never taken for it.
- `# TASK:` — the goal, one sentence, stated as a verifiable result.
- `## Checks` — the report contract: what the worker must hand back when it finishes.

Check the brief, then record it as a planned event:

```sh
flywheel lint .flywheel/briefs/hello.txt
flywheel log --task hello --kind planned --brief .flywheel/briefs/hello.txt
```

`flywheel lint` exits 0 only when the header is complete — an `owns:` line, at least one `gate:`
line, a `# TASK` goal and a `## Checks` section. `flywheel log` appends a `planned` event. The
brief is hashed at dispatch, so editing it afterwards is detectable (rule T1).

## 3. Dispatch a worker

`flywheel run` dispatches the task. It reads `.flywheel/config.json` for the worker — a real
config names the worker, its adapter and its model:

```json
{
  "version": 1,
  "workers": [
    {
      "name": "default",
      "adapter": "opencode",
      "model": "openrouter/deepseek/deepseek-v4-flash-0731",
      "max_parallel": 4
    }
  ]
}
```

There are three adapters: `opencode`, `claude`, and `sim`. The first two call a real provider.
`sim` needs **no provider at all** — it replays a recorded run — so if you have no API key you
can still drive every command in this guide. What a simulated worker produces is a replay, not
real work: it writes no files, so a `sim` unit's own gates fail on purpose. Use `opencode` (or
`claude`) with a reachable model for the file to actually be written; confirm the model first
with `flywheel doctor`.

Dispatch the unit:

```sh
flywheel run hello
```

The CLI prints each event as it records it:

```text
hello r1 dispatched opencode openrouter/deepseek/deepseek-v4-flash-0731
hello r1 started ses_abc123
hello r1 plan recorded
hello r1 report recorded
hello r1 finished rc=0 reason=stop model=openrouter/deepseek/deepseek-v4-flash-0731 steps=2 tokens=285 cost=$0.0040
```

`rc=0` means the worker ran to a clean stop — it says nothing yet about whether the task is
correct. That is the next step's job.

## 4. Measure it yourself

The worker's report is a claim, not evidence. `flywheel validate` re-runs the brief's `gate:`
lines on the exact tree as it is now and checks that nothing changed outside `owns:`:

```sh
flywheel validate hello
```

```text
validate: brief .flywheel/briefs/hello.txt
hello gate 1: pass (38ms)
hello gate 2: pass (46ms)
hello file hello.txt: 1 lines
hello owns: ok
```

Two things are happening here that the worker's own self-report cannot provide:

- **The gates are re-run, not replayed.** `validate` executes each `gate:` command against the
  tree and records a `validated` supervisor reading bound to the tree's hash. A gate that fails,
  or a `host-blocked` run that never produced a passing reading, makes it exit 5.
- **The owns boundary is checked against git.** Any changed path not covered by `owns:` is
  reported as `outside` and fails the unit — a worker that edited a file it was not given is
  caught here, even when every gate passes. Pre-existing dirty files that the unit did not touch
  are excused (baselined).

The lead re-measures because a worker's claim is not evidence: the gauges are deterministic, the
worker is not. `flywheel validate` exits 0 only when every gate passed and nothing sits outside
`owns:`; anything else is 5.

## 5. Inspect and land

Measurement is not judgment. A passing `validate` records readings; `flywheel inspect` turns
them into a verdict. The inspector is a separate session from the worker:

```sh
flywheel inspect hello --verdict pass --session <your-session>
```

```text
hello inspected pass
```

`--session` must be a session that was **never** the worker's — rule T4 refuses self-inspection:
an inspector cannot pass work its own session built, because then the two independent checks
(the one that built, the one that judged) collapse into one. `inspect` also refuses (T3) a
`pass` verdict unless a passing `validated` reading and a clean `owns_checked` exist for the
same tree the unit actually built — a pass with no readings is refused before it is even
recorded. Either refusal exits 6 and names the rule and the fix.

A pass is not the end. The work still has to land, and landing is recorded against a commit:

```sh
git add hello.txt && git commit -m "hello: write the greeting"
flywheel land hello --commit <sha>
```

```text
hello landed <sha>
```

`flywheel land` refuses (exit 6, rule T5) unless the task has an `inspected pass` on record. When you verified the unit by hand but the gauges cannot run, use `--exception "<evidence>" --session <your session>` to land on a recorded exception.
`flywheel verify --all` then checks the whole log against the poka-yoke rules, and
`flywheel state` prints the floor derived from the log. The event log now holds the unit's whole
story — planned, dispatched, started, finished, validated, owns_checked, inspected, landed —
and the next head reads it from the files.

## What to do when a step refuses

Exit codes follow one convention across the CLI:

| Exit | Meaning | What to do |
| --- | --- | --- |
| 0 | ok | nothing |
| 1 | error | read the message; the command did not complete |
| 2 | usage | a flag or argument was wrong; run `flywheel help <command>` |
| 5 | gauges failed | a gate failed or a change sits outside `owns:` |
| 6 | rule refusal | a poka-yoke rule refused the action; the message names the rule and the fix |

Three refusals a beginner actually hits, and the exact command that fixes each:

**A brief with no `gate:` line.** `flywheel lint` reports it and exits 1, and `flywheel run`
refuses to dispatch it.

```text
lint: no gate: line
```

Fix: add at least one `gate:` line to the brief header, then re-lint:

```sh
flywheel lint .flywheel/briefs/hello.txt
```

**A pass verdict with no passing reading for the current tree.** `flywheel inspect hello
--verdict pass` refuses under T3 until the gauges have recorded a passing `validated` and a
clean `owns_checked` on the same tree hash.

```text
flywheel inspect: T3: no passing validated reading for tree <hash> after the latest finished event; run: flywheel validate hello
```

Fix: run the gauges, then inspect again:

```sh
flywheel validate hello
flywheel inspect hello --verdict pass --session <your-session>
```

**A changed file outside `owns:`.** `flywheel validate` exits 5 and prints the offending paths.

```text
hello owns: outside hello.txt
```

Fix: either move the file inside the unit's boundary (change the brief's `owns:` and re-record
it with `flywheel log --task hello --kind amended --brief <path> --note <why>`), or revert the
stray change. Then re-validate:

```sh
flywheel validate hello
```

A refusal is the tool working, not a bug: every one of these is the factory refusing to let an
unmeasured or out-of-bounds unit pass. Read the message — it names the rule and the fix.

## Where to go next

- [Concepts](concepts.md) — the vocabulary: work order, `owns:`, gates, the event log, the
  poka-yoke rules, and who does what.
- `skills/` — the persona skills the loop drives: `flywheel` (the lead), `flywheel-planner`,
  `flywheel-worker`, `flywheel-inspector`, and the rest.
- `docs/design/` — the factory model and the rules behind it (`autonomous-shipping.md`,
  `flywheel-at-scale.md`), and `docs/PROTOCOL.md` for exactly what the code enforces today.
- The [issue tracker](https://github.com/suzworx/flywheel/issues) — what is built, what is
  planned, and what is still design-only.