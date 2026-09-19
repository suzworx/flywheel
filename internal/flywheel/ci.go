package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ciAuditWorkflow is the .github/workflows/flywheel-audit.yml InitCI writes
// (issue #56). @FLYWHEEL_DIR@ (a YAML double-quoted scalar, so any path git
// accepts is safe in YAML and, as "$FLYWHEEL_DIR", in the shell) and
// @FLYWHEEL_VERSION@ are replaced.
const ciAuditWorkflow = `# flywheel-audit: verify the factory's records on every pull request (issue #56).
# Written by "flywheel init --ci"; a rerun never overwrites this file.
# Make "flywheel-audit" a required status check in the branch ruleset so it cannot be skipped.
# The event log (.flywheel/events.jsonl) must be committed for this job to have anything to verify.
name: flywheel-audit
on:
  pull_request:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  flywheel-audit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: stable
      - name: Install flywheel
        run: go install github.com/suzworx/flywheel/cmd/flywheel@@FLYWHEEL_VERSION@
      - name: Verify every unit and the event log's hash chain
        env:
          FLYWHEEL_DIR: @FLYWHEEL_DIR@
        run: |
          if [ ! -f "$FLYWHEEL_DIR/.flywheel/events.jsonl" ] && [ ! -d "$FLYWHEEL_DIR/.flywheel/events" ]; then
            echo "::error::the event log is not committed; flywheel-audit needs the event log in the repository"
            exit 1
          fi
          export PATH="$(go env GOPATH)/bin:$PATH"
          # --workdir .: resolve trees in this checkout, not the paths the ledger recorded on the author's machine.
          set +e
          flywheel verify --all --log --dir "$FLYWHEEL_DIR" --workdir .
          rc=$?
          if [ "$rc" -eq 8 ]; then
            echo "::warning::some checks were inconclusive (a pass whose tree this checkout cannot resolve); no violation was established"
            exit 0
          fi
          exit "$rc"
`

// InitCI writes .github/workflows/flywheel-audit.yml at the root of the git
// repository containing dir (git rev-parse --show-toplevel), created only when
// missing and never overwritten. The job verifies the factory in dir — its
// path relative to the repository root, from git rev-parse --show-prefix
// ("." at the root) — with the flywheel release matching version ("latest"
// unless version looks like a release, e.g. "v0.17.0" or "0.17.0", which pins
// "v0.17.0"). It returns the repository root and one piece whose Path is
// ".github/workflows/flywheel-audit.yml".
func InitCI(dir, version string) (string, []ScaffoldPiece, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %q: %w", dir, err)
	}

	cmd := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	repoRoot := strings.TrimSpace(string(out))

	// The factory's directory relative to the repository root, from git itself
	// (--show-prefix), so no path comparison can trip on 8.3 short names or
	// symlinks; "" at the root.
	prefix, err := exec.Command("git", "-C", abs, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return "", nil, fmt.Errorf("git rev-parse --show-prefix: %w", err)
	}
	flywheelDir := strings.TrimSuffix(strings.TrimSpace(string(prefix)), "/")
	if flywheelDir == "" {
		flywheelDir = "."
	}

	// Determine the version to pin: "latest" or "v"+version if it's three dot-separated decimals
	releaseVersion := "latest"
	cleanVersion := strings.TrimPrefix(version, "v")
	parts := strings.Split(cleanVersion, ".")
	if len(parts) == 3 && isDecimal(parts[0]) && isDecimal(parts[1]) && isDecimal(parts[2]) {
		releaseVersion = "v" + cleanVersion
	}

	// Replace placeholders in the workflow template
	quotedDir, err := json.Marshal(flywheelDir) // a JSON string is a valid YAML double-quoted scalar
	if err != nil {
		return "", nil, fmt.Errorf("quote %q: %w", flywheelDir, err)
	}
	workflow := strings.ReplaceAll(ciAuditWorkflow, "@FLYWHEEL_DIR@", string(quotedDir))
	workflow = strings.ReplaceAll(workflow, "@FLYWHEEL_VERSION@", releaseVersion)

	// Create .github/workflows directory
	workflowDir := filepath.Join(repoRoot, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create %s: %w", workflowDir, err)
	}

	workflowPath := filepath.Join(workflowDir, "flywheel-audit.yml")
	created, err := createIfMissing(workflowPath, []byte(workflow))
	if err != nil {
		return "", nil, fmt.Errorf("write %s: %w", workflowPath, err)
	}

	// Return the piece with relative path from repo root
	rel, err := filepath.Rel(repoRoot, workflowPath)
	if err != nil {
		rel = filepath.ToSlash(workflowPath)
	} else {
		rel = filepath.ToSlash(rel)
	}

	return repoRoot, []ScaffoldPiece{
		{Path: rel, Added: created},
	}, nil
}

// isDecimal returns true if s contains only digits 0-9
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
