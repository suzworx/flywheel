#!/usr/bin/env bash
# demo.sh builds the flywheel CLI, scaffolds a throwaway project, and prints a
# short deterministic transcript of real command output to stdout. The temp
# project path is masked as ~/demo so screenshots never leak a local path.
#
#   bash scripts/demo.sh              # full transcript
#   bash scripts/demo.sh --part init  # only the version + init blocks
#   bash scripts/demo.sh --part log-state  # only the log + state blocks
#   bash scripts/demo.sh --part gauges     # validate -> inspect -> verify
#   bash scripts/demo.sh --part factory    # the factory floor at a glance
#   bash scripts/demo.sh --part stats      # the factory's own numbers
set -euo pipefail

part="all"
if [ "${1:-}" = "--part" ]; then
  part="${2:-all}"
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

build_dir="$(mktemp -d)"
proj="$(mktemp -d)"
trap 'rm -rf "$build_dir" "$proj"' EXIT

# Build the CLI from this tree, stamping the real version (git describe) into
# it. Build chatter goes to stderr, never into the transcript. The -buildid is
# stamped unique per run because Windows Smart App Control blocks an unsigned
# binary by content hash: an identical rebuild is blocked identically, so a
# fresh id is what makes a retry meaningful.
go build -o "$build_dir/flywheel"   -ldflags "-X main.version=$(git describe --tags --abbrev=0 2>/dev/null || echo dev) -buildid=demo$(date +%s%N)"   ./cmd/flywheel 1>&2
if [ -f "$build_dir/flywheel.exe" ]; then
  fw="$build_dir/flywheel.exe"
else
  fw="$build_dir/flywheel"
fi

# A fresh project. The CLI prints the project's absolute path (with Windows
# separators under Git Bash); mask it to ~/demo before anything is printed.
if command -v cygpath >/dev/null 2>&1; then
  proj_abs="$(cygpath -w "$proj")"
else
  proj_abs="$proj"
fi
mask_target="$(printf '%s\\demo' "$proj_abs" | tr '\\' '/')"
mask_repl='~/demo'

# mask rewrites one transcript line: normalize path separators, then swap the
# temp project path for ~/demo (literal substring, no regex surprises).
mask() {
  while IFS= read -r line; do
    line="$(printf '%s' "$line" | tr '\\' '/')"
    printf '%s\n' "${line//$mask_target/$mask_repl}"
  done
}

say() { printf '$ %s\n' "$*"; }

# log_event prints one flywheel log invocation (command plus the JSON event fed
# on stdin) and then runs it, so the transcript shows exactly what was logged.
log_event() {
  say "flywheel log --dir demo --json - <<'EOF'"
  printf '> %s\n' "$1"
  printf '> %s\n' 'EOF'
  printf '%s\n' "$1" | "$fw" log --dir demo --json -
}

run_version() {
  say "flywheel version"
  "$fw" version
}

run_init() {
  say "flywheel init --dir demo"
  "$fw" init --dir demo
}

run_log_state() {
  log_event '{"ts":"2026-09-12T09:00:00.000Z","task":"T1","kind":"planned","brief":".flywheel/briefs/T1.txt","needs":["T0"],"owns":["docs/assets/"]}'
  log_event '{"ts":"2026-09-12T09:10:00.000Z","task":"T1","kind":"dispatched","session":"sess-01H2J3K4L5M6N7P","model":"openrouter/deepseek/deepseek-v4-flash-0731","attempt":"r1"}'
  log_event '{"ts":"2026-09-12T09:40:00.000Z","task":"T1","kind":"finished","rc":0,"session":"sess-01H2J3K4L5M6N7P","attempt":"r1","reason":"done"}'
  say "flywheel state --dir demo"
  "$fw" state --dir demo
}

# run_gauges shows the machine gauges: a supervisor's validate pass records the
# readings a passing inspect requires, and a worker-session inspect is refused.
# The one-commit git repo and the two gates (test -e, test -s) make the runs
# pass deterministically; gate durations are masked to a fixed (12ms).
run_gauges() {
  mkdir -p demo/.flywheel/briefs demo/.flywheel/runs
  cd demo
  git init -q
  git config core.autocrlf false
  printf '%s\n' '## a.go' '' 'fn a() -> 1' > a.go
  git add a.go
  git -c user.name=demo -c user.email=demo@example.com commit -q -m init
  cat > .flywheel/briefs/T1.txt <<'EOF'
owns: a.go
gate: test -e a.go
gate: test -s a.go

# T1

Make a return 1.
EOF
  cat > .flywheel/events.jsonl <<'EOF'
{"ts":"2026-09-12T09:00:00.000Z","task":"T1","kind":"planned","brief":".flywheel/briefs/T1.txt","owns":["a.go"]}
{"ts":"2026-09-12T09:10:00.000Z","task":"T1","kind":"dispatched","attempt":"r1"}
{"ts":"2026-09-12T09:11:00.000Z","task":"T1","kind":"started","session":"w1"}
{"ts":"2026-09-12T09:20:00.000Z","task":"T1","kind":"finished","attempt":"r1","session":"w1","rc":0,"reason":"stop"}
EOF
  printf '%s\n' '## a.go' '' 'fn a() -> 1' '' 'fn b() -> 2' >> a.go
  say "flywheel validate T1"
  "$fw" validate T1 2>&1 | sed -E 's/\([0-9]+ms\)/(12ms)/'
  say "flywheel inspect T1 --verdict pass --session w1"
  "$fw" inspect T1 --verdict pass --session w1 2>&1 || echo "(exit 6)"
  say "flywheel inspect T1 --verdict pass --session i1"
  "$fw" inspect T1 --verdict pass --session i1 2>&1
  say "flywheel verify T1"
  "$fw" verify T1 2>&1
  cd ..
}

# run_factory draws the factory floor at a fixed clock: five units that cover a
# landed unit, one running, one finished awaiting inspection, one capped (run
# file whose last step_finish is reason "length") and one planned. The run
# files are touched to a fixed mtime so their ages are reproducible.
run_factory() {
  mkdir -p demo/.flywheel/briefs demo/.flywheel/runs
  cd demo
  cat > .flywheel/events.jsonl <<'EOF'
{"ts":"2026-09-12T00:00:00Z","task":"landed","kind":"planned","brief":".flywheel/briefs/landed.txt","owns":["a.go"]}
{"ts":"2026-09-12T00:00:00Z","task":"landed","kind":"dispatched","attempt":"r1","model":"openrouter/deepseek/deepseek-v4-flash-0731","adapter":"opencode","path":".flywheel/runs/landed.r1.jsonl"}
{"ts":"2026-09-12T00:00:00Z","task":"landed","kind":"started","session":"w-landed"}
{"ts":"2026-09-12T00:00:01Z","task":"landed","kind":"finished","attempt":"r1","session":"w-landed","rc":0,"reason":"stop","steps":3,"tokens":{"input":18000,"output":900,"reasoning":400},"cost":0.0016}
{"ts":"2026-09-12T00:00:02Z","task":"landed","kind":"landed","commit":"abc1234"}
{"ts":"2026-09-12T00:00:02Z","task":"landed","kind":"reviewed","verdict":"pass"}
{"ts":"2026-09-12T00:00:00Z","task":"running","kind":"planned","brief":".flywheel/briefs/running.txt","owns":["b.go"]}
{"ts":"2026-09-12T00:00:00Z","task":"running","kind":"dispatched","attempt":"r1","model":"openrouter/deepseek/deepseek-v4-flash-0731","adapter":"opencode","path":".flywheel/runs/running.r1.jsonl"}
{"ts":"2026-09-12T00:00:00Z","task":"running","kind":"started","session":"w-running"}
{"ts":"2026-09-12T00:00:00Z","task":"finished","kind":"planned","brief":".flywheel/briefs/finished.txt","owns":["c.go"]}
{"ts":"2026-09-12T00:00:00Z","task":"finished","kind":"dispatched","attempt":"r1","model":"openrouter/deepseek/deepseek-v4-flash-0731","adapter":"opencode","path":".flywheel/runs/finished.r1.jsonl"}
{"ts":"2026-09-12T00:00:00Z","task":"finished","kind":"started","session":"w-finished"}
{"ts":"2026-09-12T00:00:01Z","task":"finished","kind":"finished","attempt":"r1","session":"w-finished","rc":0,"reason":"stop","steps":2,"tokens":{"input":12000,"output":600,"reasoning":300},"cost":0.0011}
{"ts":"2026-09-12T00:00:03Z","task":"finished","kind":"reviewed","verdict":"correct","session":"i-finished"}
{"ts":"2026-09-12T00:00:04Z","task":"finished","kind":"dispatched","attempt":"c1","model":"openrouter/deepseek/deepseek-v4-flash-0731","adapter":"opencode","path":".flywheel/runs/finished.c1.jsonl"}
{"ts":"2026-09-12T00:00:05Z","task":"finished","kind":"finished","attempt":"c1","session":"w-finished-c1","rc":0,"reason":"stop","steps":1,"tokens":{"input":4000,"output":200,"reasoning":100},"cost":0.0004}
{"ts":"2026-09-12T00:00:00Z","task":"capped","kind":"planned","brief":".flywheel/briefs/capped.txt","owns":["d.go"]}
{"ts":"2026-09-12T00:00:00Z","task":"capped","kind":"dispatched","attempt":"r1","model":"openrouter/deepseek/deepseek-v4-flash-0731","adapter":"opencode","path":".flywheel/runs/capped.r1.jsonl"}
{"ts":"2026-09-12T00:00:00Z","task":"capped","kind":"started","session":"w-capped"}
{"ts":"2026-09-12T00:00:00Z","task":"planned","kind":"planned","brief":".flywheel/briefs/planned.txt","owns":["e.go"]}
EOF
  cat > .flywheel/runs/landed.r1.jsonl <<'EOF'
{"type":"step_start","sessionID":"w-landed","part":{"type":"step_start","step":1,"startTime":"2026-09-12T00:00:00Z"},"time":{"startedAt":"2026-09-12T00:00:00Z"}}
{"type":"step_finish","sessionID":"w-landed","part":{"type":"step_finish","reason":"stop"}}
EOF
  cat > .flywheel/runs/running.r1.jsonl <<'EOF'
{"type":"step_start","sessionID":"w-running","part":{"type":"step_start","step":1,"startTime":"2026-09-12T00:00:00Z"},"time":{"startedAt":"2026-09-12T00:00:00Z"}}
{"type":"step_finish","sessionID":"w-running","part":{"type":"step_finish","reason":"stop"}}
EOF
  cat > .flywheel/runs/finished.r1.jsonl <<'EOF'
{"type":"step_start","sessionID":"w-finished","part":{"type":"step_start","step":1,"startTime":"2026-09-12T00:00:00Z"},"time":{"startedAt":"2026-09-12T00:00:00Z"}}
{"type":"step_finish","sessionID":"w-finished","part":{"type":"step_finish","reason":"stop"}}
EOF
  cat > .flywheel/runs/finished.c1.jsonl <<'EOF'
{"type":"step_start","sessionID":"w-finished-c1","part":{"type":"step_start","step":1,"startTime":"2026-09-12T00:00:04Z"},"time":{"startedAt":"2026-09-12T00:00:04Z"}}
{"type":"step_finish","sessionID":"w-finished-c1","part":{"type":"step_finish","reason":"stop"}}
EOF
  cat > .flywheel/runs/capped.r1.jsonl <<'EOF'
{"type":"step_start","sessionID":"w-capped","part":{"type":"step_start","step":1,"startTime":"2026-09-12T00:00:00Z"},"time":{"startedAt":"2026-09-12T00:00:00Z"}}
{"type":"step_finish","sessionID":"w-capped","part":{"type":"step_finish","reason":"length"}}
EOF
  touch -m -d "2026-09-12 00:29:55" .flywheel/runs/*.jsonl
  say "flywheel factory --once --width 100 --now 2026-09-12T00:30:00Z"
  "$fw" factory --once --width 100 --now 2026-09-12T00:30:00Z
  cd ..
}

# run_stats shows the factory's own numbers: a landed task that passed on the
# first attempt and a task that needed one correction before landing, so
# first-pass rate, corrections per task, and cost per landed task are all
# nonzero and reproducible.
run_stats() {
  mkdir -p demo/.flywheel
  cd demo
  cat > .flywheel/events.jsonl <<'EOF'
{"ts":"2026-09-12T00:00:00Z","task":"landed","kind":"planned","brief":".flywheel/briefs/landed.txt","owns":["a.go"]}
{"ts":"2026-09-12T00:00:00Z","task":"landed","kind":"dispatched","attempt":"r1","model":"openrouter/deepseek/deepseek-v4-flash-0731","adapter":"opencode"}
{"ts":"2026-09-12T00:00:00Z","task":"landed","kind":"started","session":"w-landed"}
{"ts":"2026-09-12T00:05:00Z","task":"landed","kind":"finished","attempt":"r1","session":"w-landed","rc":0,"reason":"stop","steps":3,"tokens":{"input":18000,"output":900,"reasoning":400},"cost":0.0016}
{"ts":"2026-09-12T00:05:01Z","task":"landed","kind":"reviewed","verdict":"pass"}
{"ts":"2026-09-12T00:05:02Z","task":"landed","kind":"landed","commit":"abc1234"}
{"ts":"2026-09-12T00:00:00Z","task":"finished","kind":"planned","brief":".flywheel/briefs/finished.txt","owns":["c.go"]}
{"ts":"2026-09-12T00:00:00Z","task":"finished","kind":"dispatched","attempt":"r1","model":"openrouter/deepseek/deepseek-v4-flash-0731","adapter":"opencode"}
{"ts":"2026-09-12T00:00:00Z","task":"finished","kind":"started","session":"w-finished"}
{"ts":"2026-09-12T00:06:00Z","task":"finished","kind":"finished","attempt":"r1","session":"w-finished","rc":0,"reason":"stop","steps":2,"tokens":{"input":12000,"output":600,"reasoning":300},"cost":0.0011}
{"ts":"2026-09-12T00:06:01Z","task":"finished","kind":"reviewed","verdict":"correct","session":"i-finished"}
{"ts":"2026-09-12T00:06:02Z","task":"finished","kind":"dispatched","attempt":"c1","model":"openrouter/deepseek/deepseek-v4-flash-0731","adapter":"opencode"}
{"ts":"2026-09-12T00:10:00Z","task":"finished","kind":"finished","attempt":"c1","session":"w-finished-c1","rc":0,"reason":"stop","steps":1,"tokens":{"input":4000,"output":200,"reasoning":100},"cost":0.0004}
EOF
  say "flywheel stats"
  "$fw" stats
  cd ..
}

{
  cd "$proj"
  case "$part" in
    all|init) run_version; run_init ;;
  esac
  case "$part" in
    all|log-state) run_log_state ;;
  esac
  case "$part" in
    all|gauges) run_gauges ;;
  esac
  case "$part" in
    all|factory) run_factory ;;
  esac
  case "$part" in
    all|stats) run_stats ;;
  esac
} | mask