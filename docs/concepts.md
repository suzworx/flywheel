# Concepts

This page defines the vocabulary flywheel is built on — the words the quickstart uses and the
rules every command enforces. It is the map; [PROTOCOL.md](PROTOCOL.md) is the territory, and the
code is the authority. Where a term names a machine check, the check is real: run the command
and it happens.

The core idea in one sentence: **the repository is the session.** All state — work orders, the
event log, the readings, the verdicts — lives in files, never inside a vendor's session. That is
what makes agents swappable and the loop crash-safe: any head, human or model, reads the same
files and continues the same work.

## The work order (brief)

A work order, called a **brief**, is one text file that turns a goal into a bounded unit of
work. It has a header block of rules and a body that states the goal. A minimal example:

```text
owns: docs/hello.md (new)
needs: none
gate: test -f docs/hello.md
gate: grep -q "Hello" docs/hello.md

# TASK: hello — write the welcome page
```

The header is what the machine reads. `owns:` and `needs:` scope the unit; `gate:` lines are the
contract the gauges re-run. The brief is **hashed at dispatch** — `flywheel run` records the
SHA-256 of the exact prompt sent — so editing the brief after dispatch is detectable: rule T1
compares the dispatched hash with the current brief, and `flywheel verify` flags a mismatch. An
`amended` event records a deliberate change of the brief later.

## owns:

`owns:` is the unit's boundary: the paths this unit may write. An entry is one of three forms,
matched the same way at owns-check time:

- a **literal path** — `docs/hello.md`;
- a path ending in `/` — a whole directory, `docs/`;
- a **glob pattern** — `docs/*.md` for a file whose exact name is not known yet.

`(new)` marks a file the unit is allowed to create. A path that changed outside `owns:` fails
the check: `flywheel validate` compares every changed path against the unit's `owns:` and
reports the strays as `outside`, making the unit fail (exit 5) even when every gate passes.
Pre-existing dirty files the unit did not touch are baselined — excused because they were
already dirty at dispatch.

`owns:` exists so two workers never fight over one file: units run in parallel only when their
`owns:` sets are disjoint, and the boundary is what makes "you edited a file you were not given"
a machine failure instead of a conversation.

## gate: and live-gate:

A `gate:` line is one shell command that must exit 0 on the built tree. It is the machine's
contract for the unit's claims. The worker's report is not enough — the lead **re-runs every
gate** with `flywheel validate`, which records a supervisor reading per gate bound to the tree's
hash. A gate is not "trust me, it works"; it is "here is the command that proves it, re-run on
the exact tree." A brief with no `gate:` line is refused before it is ever dispatched.

A `live-gate:` is a gate that runs **only** in the lead's verification pass, via
`flywheel validate <task> --live`, never in the worker's own dispatch or an ordinary validate.
It is for a unit whose deliverable is a **provider-facing contract** — a real API, a real
provider — where a mocked gate proves the mock and not the contract. The worker proves what it
can with the mocked path; the lead proves the contract against the real thing, and a `pass`
verdict is refused until that live reading exists.

## The event log

Everything that happens to a unit is appended, one JSON line at a time, to
`.flywheel/events.jsonl`. The log is **append-only**: nothing is edited, deleted, or rewritten.
State is **derived** from the log — `flywheel state` (or the status block in `flywheel.md`)
replays the events and computes where every unit stands, and `.flywheel/state.json` is a
read-only projection of it. If the log and the state ever disagree, the log wins.

That one property does the heavy lifting: it makes the loop crash-safe (a dead session loses
nothing, the next head replays the same events), auditable (every command that wrote the log is
traceable), and honest (a hand-edited historical line does not silently change the story — rule
T1 catches a brief that no longer matches its recorded hash).

## The poka-yoke rules

The log is not just a diary; it is checked. `flywheel verify` (and the enforcing commands
themselves) apply transition rules — T1 through T10 in the design, of which five are implemented
in code: T1, T3, T4, T5, T8. They are **poka-yoke** — mistake-proofing, devices that make the
wrong action impossible rather than hoping nobody does it. The two a newcomer meets first:

- **T3 — a pass needs current readings, not old ones.** `inspected pass` requires a passing
  `validated` reading for every gate and a clean `owns_checked` on the same tree the unit
  actually built, both recorded after the latest `finished` event. This is why the gauges run
  before inspection, and why a pass with no readings is refused (exit 6) rather than trusted.
- **T4 — no self-inspection.** An `inspected` event's session must never be a session that wrote
  that task's work — a worker session, by this rule, cannot pass its own unit. Separate the
  session that builds from the session that judges, or the refusal tells you to.

The full rules, and which are still design-only, are in [PROTOCOL.md](PROTOCOL.md) and
`docs/design/autonomous-shipping.md`.

## Who does what

The factory runs on separation of duties — each step is a different persona, so the checks do
not collapse into one voice:

- **planner** — turns a spec into work orders (`owns:`/`needs:`/gates); never dispatches.
- **foreman** — runs a line of workers, retries by policy, pulls the cord; never plans.
- **worker** — builds one unit at its own station, in its own worktree; never plans, inspects,
  or commits.
- **supervisor** — the gauges, the CLI itself with no model: runs gates, checks `owns:`, records
  readings; never judges intent.
- **inspector** — a verdict against the work order; never runs the gauges or fixes the unit.
- **steward** — turns signals and nonconformances into learnings; never changes units.
- **lead** — the plant manager: plans, briefs, judges, signs off; never implements.

The one rule that matters above all: **the session that inspects is never the session that
built.** That separation — T4, and the persona boundaries that mirror it — is what makes
inspection an independent check instead of a rubber stamp.