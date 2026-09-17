# AGENTS.md — house rules for every agent working on this repo

This is the build manual for flywheel: a factory for AI coding agents. OpenCode loads this file
into every worker's context; Claude Code reads CLAUDE.md, which imports this file.

## What this is

- flywheel: a Go CLI (module `flywheel`, Go 1.27, standard library only — never add a dependency)
  plus agent skills (skills/) and design docs (docs/design/).
- The factory loop: a lead plans, briefs and judges; cheap disposable OpenCode workers build;
  the CLI measures and enforces.

## Layout

- cmd/flywheel — one file per subcommand; each registers itself from init(); main.go holds the registry.
- internal/flywheel — event log, state, config, run and the OpenCode adapter, gauges/inspect/verify,
  the factory view and rendering, brief headers, init.
- internal/flywheel/testdata — fixtures; testdata/**/.flywheel/ is re-included in .gitignore.
- scripts/demo.sh + scripts/termshot.mjs — README screenshots.
- skills/, docs/, .github/workflows — ci.yml (test on Linux/macOS/Windows + lint), release.yml
  (release-please and binaries).

## Checks (the same ones CI runs)

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- Cross-OS vet: `GOOS=linux go vet ./...`
- Cross-OS vet: `GOOS=darwin go vet ./...` — files behind `//go:build !windows` never compile on
  Windows; a missing import there once passed every local gate and failed CI.
- gofmt must be clean.
- Report every check's real exit status (`cmd ; echo "exit=$?"`), never behind a pipe such as `| tail`.

## Windows hosts

- `core.autocrlf=true` checks text files out with CRLF (.gitattributes keeps *.go, *.jsonl and *.sh
  LF); so check gofmt with `for f in <files>; do tr -d '\r' < "$f" | go run cmd/gofmt -l; done`
  (`go run cmd/gofmt`, because gofmt.exe may be blocked), and compare text files with `\r` stripped.
- Smart App Control sometimes blocks a freshly built binary ("An Application Control policy has
  blocked this file"): that is the host, not the code; rerun. Never change the security setting.
- Smart App Control can reject a test binary by its hash, persistently (identical code rebuilds
  to the identical binary, so reruns never help); run a package's tests compiled then executed
  from its directory: `d=$(mktemp -d); go test -c -o "$d/t.test.exe" ./<pkg> && (cd <pkg> &&
  "$d/t.test.exe")` (see #101). Never change the security setting.

## Tests

- Standard library `testing` only; temp dirs with t.TempDir().
- Temp git repos with `git -c core.autocrlf=false ...`; commits with
  `git -c user.name=test -c user.email=test@example.com commit ...`; never touch this repository's
  own git index.
- Golden files compare after normalising "\r\n"; inject clocks (a `now` parameter) instead of
  reading the real time; fixtures end with a newline.

## Code conventions

- Wrap errors with %w and name the file or command.
- Exit codes: 0 ok, 1 error, 2 usage, 5 gauges failed, 6 rule refusal, 8 inconclusive (a check
  that could not be established — `flywheel verify` on a pass whose tree no repository this
  verifier can see resolves, distinct from 6 because no violation is established).
- `flywheel run` adds its own outcome codes for the dispatch it measured: 3 silent (no output
  within the start timeout), 4 failed (any other non-clean outcome), 7 stalled (the run file
  stopped growing for the stall timeout).
- Commands accept the task id before or after flags.
- Never mutate the shared git index (use a temporary GIT_INDEX_FILE).
- Write state files atomically (temp file + rename); the event log (.flywheel/events.jsonl) is
  append-only; read only complete lines from files other processes append to.
- Bind flags with `fs.StringVar(&o.x, ...)` (or keep the pointer and dereference after Parse);
  `o.x = *fs.String(...)` freezes the default and silently ignores the flag (it broke every
  flag of four commands once). Commands share one flag definition between help and run.

## Commits and PRs

- Conventional Commit titles (feat:, fix:, docs:, test:, chore:, ci:, build:; CI checks the PR
  title); squash merges.
- release-please opens the release PRs and the owner publishes.
- Workers never commit, stash, reset, checkout or push (the worker permission policy denies every
  git write command); the lead commits after inspection.

## How flywheel is built (it builds itself)

- The lead writes a brief whose header has `owns:`, `needs:` and one `gate:` line per check
  (list every gate: `flywheel validate` runs only these).
- Records it with `flywheel log --task <id> --kind planned --brief <path>`.
- Dispatches with `flywheel run <id>` (an OpenCode worker; its model and reasoning variant come from .flywheel/config.json).
- Measures with `flywheel validate <id>`.
- Rules with `flywheel inspect <id> --verdict ... --session <own>`.
- Checks the log with `flywheel verify`; watches with `flywheel factory --once`.
- `flywheel run` records the files already dirty at dispatch, and the owns check ignores them
  unless the unit changed them (owns-baseline, #96); commit your own edits before
  `flywheel validate`.
- `flywheel help <command>` prints any command's flags.

## Rules for workers

- Stay inside your worktree and your `owns:`; at most one write per response (at most 120 lines);
  batch read-only calls.
- Look up APIs with `go doc pkg.Symbol`, never by reading library source, and never write probe
  programs.
- Build after each file; run the full checks at the end.
- Report the commands you ran and their real exit codes; a claim is not evidence, the gauges
  re-measure it.

## Where to read more

- skills/flywheel/SKILL.md — the loop.
- skills/flywheel/references/worker-brief.md — briefs, dispatch, run states.
- docs/design/autonomous-shipping.md — the factory model and rules T1-T10.
- README.md.