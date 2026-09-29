#!/usr/bin/env bash
# demo-web.sh regenerates docs/demo/demo.json, the data behind the Pages demo
# (docs/demo.html): a real session of this CLI in a throwaway project, run
# with the offline sim adapter (no model, no network), and a tour of the real
# interactive factory view rendered headlessly by `flywheel factory --keys
# ... --frames`. Every command and frame is real output; paths are masked to
# ~/demo, gate timings normalised and the tour's ledger re-timed onto a fixed
# clock, so the file only changes when the CLI's output does.
#
#   bash scripts/demo-web.sh          # write docs/demo/demo.json
#   bash scripts/demo-web.sh --check  # exit 1 when the committed file drifted
set -euo pipefail

check=false
case "${1:-}" in
  --check) check=true ;;
  "") ;;
  *) echo "usage: bash scripts/demo-web.sh [--check]" >&2; exit 2 ;;
esac

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
target="docs/demo/demo.json"

py=""
for p in python3 python; do
  if command -v "$p" >/dev/null 2>&1 && "$p" -c 'import json' >/dev/null 2>&1; then py="$p"; break; fi
done
[ -n "$py" ] || { echo "demo-web.sh: needs python3 (or python) on PATH" >&2; exit 1; }

work="$(mktemp -d)"
if [ -n "${DEMO_WEB_KEEP:-}" ]; then
  echo "demo-web.sh: keeping the scratch projects in $work" >&2
else
  trap 'rm -rf "$work"' EXIT
fi
mkdir -p "$work/bin" "$work/steps" "$work/demo"

# Build the CLI from this tree like scripts/demo.sh: the stamped version, a
# unique -buildid (Windows Smart App Control), and no VCS stamp, so the view's
# header names the same build on every run.
version="$(git describe --tags --abbrev=0 2>/dev/null || echo dev)"
go build -buildvcs=false -o "$work/bin/flywheel" \
  -ldflags "-X main.version=$version -buildid=demo$(date +%s%N)" ./cmd/flywheel 1>&2
fw="$work/bin/flywheel"
[ -f "$fw.exe" ] && fw="$fw.exe"

# The transcript runs `flywheel` and `git` exactly as it shows them: these
# wrappers only add the binary's path and a fixed identity and date, so the
# commit ids are the same on every run.
flywheel() { "$fw" "$@"; }
git() { command git -c core.autocrlf=false -c user.name=demo -c user.email=demo@example.com "$@"; }
export GIT_AUTHOR_DATE="2026-09-27T09:00:00Z" GIT_COMMITTER_DATE="2026-09-27T09:00:00Z"
export -f flywheel git
export fw

# step records one transcript entry: the command as shown, run by bash in the
# demo project, its combined output and exit code, and an optional note.
n=0
step() {
  n=$((n + 1))
  local f="$work/steps/$(printf '%03d' "$n")"
  printf '%s' "$1" >"$f.cmd"
  printf '%s' "${2:-}" >"$f.note"
  set +e
  (cd "$work/demo" && bash -c "$1") >"$f.out" 2>&1
  echo $? >"$f.rc"
  set -e
}

# fixture writes a recorded OpenCode run the sim adapter replays: a plan, the
# file the worker wrote, the gate it ran, one step and a report.
fixture() { # name session path gate report [final finish reason]
  local s="$2" reason="${6:-stop}"
  mkdir -p "$work/demo/.flywheel/sim"
  cat >"$work/demo/.flywheel/sim/$1.jsonl" <<EOF
{"type":"step_start","sessionID":"$s","part":{"type":"step_start","step":1,"startTime":"2026-09-27T09:00:00Z"},"time":{"startedAt":"2026-09-27T09:00:00Z"}}
{"type":"text","sessionID":"$s","part":{"type":"text","text":"PLAN read the brief, edit $3, run the gate."}}
{"type":"tool_use","sessionID":"$s","part":{"type":"tool_use","tool":"read","state":{"input":{"filePath":"$3"}}}}
{"type":"tool_use","sessionID":"$s","part":{"type":"tool_use","tool":"write","state":{"input":{"filePath":"$3"}}}}
{"type":"tool_use","sessionID":"$s","part":{"type":"tool_use","tool":"bash","state":{"input":{"command":"$4"}}}}
{"type":"step_finish","sessionID":"$s","part":{"type":"step_finish","reason":"stop","tokens":{"input":2400,"output":310,"reasoning":120,"cache":{"read":1800,"write":0}},"cost":0.0021}}
{"type":"text","sessionID":"$s","part":{"type":"text","text":"$5"}}
{"type":"step_finish","sessionID":"$s","part":{"type":"step_finish","reason":"$reason","tokens":{"input":600,"output":90,"reasoning":20,"cache":{"read":2200,"write":0}},"cost":0.0006}}
EOF
}

# --- the story -------------------------------------------------------------
fixture greet ses_demo_greet_r1 greet.sh "sh greet.sh | grep -q 'hello, flywheel'" \
  "Done. greet.sh now prints hello, flywheel; the gate passes."
fixture usage ses_demo_usage_r1 README.md "grep -q '^## Usage' README.md" \
  "Done. README.md has a usage section."
fixture usage-c1 ses_demo_usage_c1 README.md "grep -q '^## Usage' README.md" \
  "Fixed the heading to ## Usage; the gate passes now."
fixture flags ses_demo_flags_r1 greet.sh "sh greet.sh --name demo" \
  "Parsing --name, then the default greeting..." length

step "git init -q -b main && flywheel init" \
  "A git project and a fresh ledger (.flywheel/)."
step "cat > .flywheel/config.json <<'EOF'
{\"version\": 1, \"workers\": [
  {\"name\": \"sim\", \"adapter\": \"sim\", \"model\": \".flywheel/sim/greet.jsonl\", \"max_parallel\": 4},
  {\"name\": \"sim-docs\", \"adapter\": \"sim\", \"model\": \".flywheel/sim/usage.jsonl\"},
  {\"name\": \"sim-docs-fix\", \"adapter\": \"sim\", \"model\": \".flywheel/sim/usage-c1.jsonl\"},
  {\"name\": \"sim-flags\", \"adapter\": \"sim\", \"model\": \".flywheel/sim/flags.jsonl\"}]}
EOF" "Each sim worker replays one recorded run (its model is the recording), so this demo needs no model and no network."
step "printf '.flywheel/\nflywheel.md\n' >> .git/info/exclude && printf 'echo hi\n' > greet.sh && git add -A && git commit -qm start && git log --oneline" \
  "This demo keeps its ledger and flywheel.md (both timestamped) out of git, so its commit ids are the same on every run."
step "cat > .flywheel/briefs/greet.txt <<'EOF'
owns: greet.sh
gate: sh greet.sh | grep -q 'hello, flywheel'

# greet
Make greet.sh print: hello, flywheel
EOF
flywheel log --task greet --kind planned --brief .flywheel/briefs/greet.txt && flywheel state" \
  "A brief is a contract: the files the unit owns and the gates that must pass."
step "flywheel run greet" "Dispatch: the worker runs, the ledger records every step."
step "printf 'echo \"hello, flywheel\"\n' > greet.sh" \
  "The edit the recorded run made (sim replays the run's stream, not its writes)."
step "flywheel validate greet" "The factory runs the gates itself; a worker's claim is not evidence."
step "flywheel inspect greet --verdict pass --session inspector-1" \
  "Inspection by a session that is not the worker."
step "git add -A && git commit -qm 'greet: say hello, flywheel' && flywheel land greet --commit \$(git rev-parse --short HEAD)"
step "cat > .flywheel/briefs/usage.txt <<'EOF'
owns: README.md
gate: grep -q '^## Usage' README.md

# usage
Add a Usage section to README.md.
EOF
flywheel log --task usage --kind planned --brief .flywheel/briefs/usage.txt && flywheel run usage --worker sim-docs" \
  "A second unit. Same loop."
step "printf '# demo\n\n## usage\n\nsh greet.sh\n' > README.md" "The worker's edit: a lower-case heading."
step "flywheel validate usage" "The gate fails, so the unit cannot pass inspection or land."
step "cat > .flywheel/briefs/usage.delta.txt <<'EOF'
The gate grep -q '^## Usage' README.md fails: the heading is '## usage'. Make it '## Usage'.
EOF
flywheel run usage --delta .flywheel/briefs/usage.delta.txt --worker sim-docs-fix" \
  "A correction: a delta brief naming the failing gate, dispatched as attempt c1."
step "sed -i 's/^## usage/## Usage/' README.md && flywheel validate usage"
step "flywheel inspect usage --verdict pass --session inspector-1 && git add -A && git commit -qm 'usage: document greet.sh' && flywheel land usage --commit \$(git rev-parse --short HEAD)"
step "cat > .flywheel/briefs/flags.txt <<'EOF'
owns: greet.sh
gate: sh greet.sh --name demo | grep -q 'hello, demo'

# flags
Take the name to greet from --name.
EOF
flywheel log --task flags --kind planned --brief .flywheel/briefs/flags.txt && flywheel run flags --worker sim-flags" \
  "A third unit. This run hits its output limit mid-answer."
step "flywheel state" "Two landed, one stopped: the andon will show it."
step "flywheel stats" "The factory's own numbers, from the same ledger."

# --- the tour ----------------------------------------------------------------
# The tour's ledger is the story's, re-appended through `flywheel log` with
# each event re-timed 45s apart from 09:00Z, so ages in the view are the same
# on every run; the view is then rendered at a fixed --now.
tour="$work/t/demo"
mkdir -p "$work/t"
cp -R "$work/demo" "$tour"
"$py" - "$work/demo/.flywheel/events.jsonl" >"$work/events.jsonl" <<'PY'
import datetime, json, sys
base = datetime.datetime(2026, 9, 27, 9, 0, 0, tzinfo=datetime.timezone.utc)
lines = [l for l in open(sys.argv[1], encoding="utf-8").read().splitlines() if l.strip()]
for i, l in enumerate(lines):
    e = json.loads(l)
    e.pop("prev", None)
    if "duration_ms" in e:
        e["duration_ms"] = 120
    e["ts"] = (base + datetime.timedelta(seconds=45 * i)).strftime("%Y-%m-%dT%H:%M:%SZ")
    print(json.dumps(e, separators=(",", ":")))
PY
: >"$tour/.flywheel/events.jsonl"
"$fw" log --dir "$tour" --json "$work/events.jsonl" >/dev/null
find "$tour/.flywheel" -type f -exec touch -m -d "2026-09-27 09:20:00" {} +
tour_now="2026-09-27T09:30:00Z"

# Each tour token (a --keys token) and the caption shown with its frame.
tokens=(
  '' 'The units view: every unit, its state and its next step.'
  'j' 'j and k move the cursor, as in k9s and vim.'
  '<enter>' 'Enter opens why: the unit explained from its ledger events.'
  'y' 'y shows the brief: what the unit owns and the gates it must pass.'
  'l' 'l is the log tab: the worker run, step by step.'
  '<esc>' 'Esc goes back to the units.'
  '/' '/ starts a filter.'
  '"usage"' 'Type to filter the rows.'
  '<enter>' 'Enter keeps the filter.'
  '<esc>' 'Esc clears it.'
  ':' ': opens the command line, like k9s.'
  '"andon"' 'Views have names; Tab completes them.'
  '<enter>' 'The andon: only the units that need a person, and the next step for each.'
  ':' 'Back to the command line.'
  '"metrics"' 'The factory measures itself...'
  '<enter>' 'Metrics: first-pass rate, corrections, cost, by window.'
  ':' 'One more view.'
  '"pulse"' 'pulse is the metrics dashboard.'
  '<enter>' 'Pulse: flow, quality, reliability, cost and capacity in one screen.'
  '?' '? lists every key. That is the whole tour; the view is `flywheel factory`.'
)
keys="" caps=()
for ((i = 2; i < ${#tokens[@]}; i += 2)); do keys+="${tokens[i]} "; done
for ((i = 1; i < ${#tokens[@]}; i += 2)); do caps+=("${tokens[i]}"); done
"$fw" factory --dir "$tour" --keys "$keys" --frames --width 100 --height 30 --now "$tour_now" >"$work/frames.json"
printf '%s\n' "${caps[@]}" >"$work/captions.txt"

# --- assemble ------------------------------------------------------------------
masks=("$work/demo" "$work/t/demo" "$work")
if command -v cygpath >/dev/null 2>&1; then
  for p in "$work/demo" "$work/t/demo" "$work"; do masks+=("$(cygpath -w "$p")" "$(cygpath -m "$p")"); done
fi
out="$work/demo.json"
"$py" - "$work" "$version" "$out" "${masks[@]}" <<'PY'
import glob, json, os, re, sys
work, version, out, masks = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
def clean(s):
    s = s.replace("\r\n", "\n")
    for m in masks:
        s = s.replace(m, "~/demo" if not m.rstrip("/\\").endswith(os.path.basename(work)) else "~")
    s = re.sub(r"\((\d+)ms\)", "(12ms)", s)
    s = re.sub(r"Mean attempt seconds: \d+", "Mean attempt seconds: 3", s)
    return s
def read(p):
    with open(p, encoding="utf-8", errors="replace") as f:
        return f.read()
transcript = []
for cmd in sorted(glob.glob(os.path.join(work, "steps", "*.cmd"))):
    stem = cmd[:-4]
    step = {"cmd": read(cmd), "out": clean(read(stem + ".out")), "rc": int(read(stem + ".rc").strip())}
    note = read(stem + ".note")
    if note:
        step["note"] = note
    transcript.append(step)
frames = json.loads(read(os.path.join(work, "frames.json")))
caps = read(os.path.join(work, "captions.txt")).splitlines()
if len(frames) != len(caps):
    sys.exit("demo-web.sh: %d frames for %d captions" % (len(frames), len(caps)))
tour = [{"key": f["key"], "caption": c, "frame": clean(f["frame"])} for f, c in zip(frames, caps)]
doc = {"version": version, "generated": "2026-09-27", "transcript": transcript, "tour": tour}
with open(out, "w", encoding="utf-8", newline="\n") as f:
    json.dump(doc, f, ensure_ascii=False, indent=1)
    f.write("\n")
PY

if $check; then
  # The drift check ignores the build's version: the top-level "version" line
  # is dropped and any other occurrence of that document's version string
  # becomes <version>, so a release alone never makes the file stale. Every
  # other byte counts. A missing or unreadable committed file is stale.
  norm() { # in out
    "$py" - "$1" "$2" <<'PY'
import json, sys
text = open(sys.argv[1], encoding="utf-8").read().replace("\r\n", "\n")
v = json.loads(text).get("version")
if isinstance(v, str) and v:
    text = text.replace('\n "version": %s,' % json.dumps(v, ensure_ascii=False), "", 1)
    text = text.replace(v, "<version>")
open(sys.argv[2], "w", encoding="utf-8", newline="\n").write(text)
PY
  }
  if ! norm "$target" "$work/committed.norm" 2>/dev/null || ! norm "$out" "$work/fresh.norm" ||
    ! cmp -s "$work/committed.norm" "$work/fresh.norm"; then
    echo "demo-web.sh: $target is stale; regenerate with: bash scripts/demo-web.sh" >&2
    diff "$work/committed.norm" "$work/fresh.norm" 2>/dev/null | head -40 >&2 || true
    exit 1
  fi
  echo "demo-web.sh: $target is current"
else
  mkdir -p "$(dirname "$target")"
  cp "$out" "$target"
  echo "demo-web.sh: wrote $target (${#caps[@]} tour frames, $n transcript steps)"
fi
