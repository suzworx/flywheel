package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ledgerDirEnv overrides config ledger.dir when non-empty (issue #464).
const ledgerDirEnv = "FLYWHEEL_LEDGER_DIR"

// LedgerConfig places the event log (issue #464).
type LedgerConfig struct {
	// Dir moves the event log outside the checkout: "home" (a directory
	// under ~/.flywheel/ledgers/ shared by every worktree of one clone), an
	// absolute path, or ~/<path>. Unset keeps the log in .flywheel/.
	Dir string `json:"dir,omitempty"`
}

// ledgerDirProblem is why a ledger.dir value is refused, or "".
func ledgerDirProblem(v string) string {
	switch {
	case v == "":
		return "must not be empty"
	case v == "home", strings.HasPrefix(v, "~/") && len(v) > 2, filepath.IsAbs(v):
		return ""
	}
	return fmt.Sprintf("%q must be \"home\", an absolute path or ~/<path>, not a relative path", v)
}

// EventLogDir is the directory holding the event log of dir (events.jsonl
// or events/): dir/.flywheel, external false, when neither FLYWHEEL_LEDGER_DIR
// nor config ledger.dir is set; else the resolved directory, external true.
// It reads only the ledger key of config.json — never LoadConfig, so an
// unrelated broken key cannot hide the log — and creates nothing.
func EventLogDir(dir string) (root string, external bool, err error) {
	return resolveLedgerDir(dir, true)
}

// resolveLedgerDir is EventLogDir; checkInTree false skips the two-live-logs
// refusal, so flywheel ledger move can resolve where the in-tree log goes.
func resolveLedgerDir(dir string, checkInTree bool) (root string, external bool, err error) {
	value, source := os.Getenv(ledgerDirEnv), ledgerDirEnv
	if value == "" {
		path := filepath.Join(dir, ".flywheel", configFileName)
		b, rerr := os.ReadFile(path)
		if rerr != nil && !os.IsNotExist(rerr) {
			return "", false, fmt.Errorf("read %s: %w", path, rerr)
		}
		var c struct {
			Ledger *LedgerConfig `json:"ledger"`
		}
		if len(bytes.TrimSpace(b)) > 0 {
			if err := json.Unmarshal(b, &c); err != nil {
				return "", false, fmt.Errorf("parse %s for ledger.dir: %w", path, err)
			}
		}
		if c.Ledger == nil {
			return filepath.Join(dir, ".flywheel"), false, nil
		}
		value, source = c.Ledger.Dir, path+" ledger.dir"
	}
	if why := ledgerDirProblem(value); why != "" {
		return "", false, fmt.Errorf("%s: ledger.dir %s", source, why)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false, fmt.Errorf("resolve %q: %w", dir, err)
	}
	// One git call: the work tree and the common dir (shared by every
	// worktree of a clone). Outside git only dir itself is fenced.
	fences := []string{abs}
	common := ""
	if out, gerr := exec.Command("git", "-C", abs, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir").Output(); gerr == nil {
		lines := strings.Fields(strings.ReplaceAll(string(out), "\r", ""))
		if len(lines) == 2 {
			fences = append(fences, filepath.Clean(lines[0]), filepath.Clean(lines[1]))
			common = filepath.Clean(lines[1])
		}
	}
	switch {
	case value == "home":
		if common == "" {
			return "", false, fmt.Errorf("%s: ledger.dir \"home\" needs a git repository at %s", source, abs)
		}
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", false, fmt.Errorf("%s: ledger.dir \"home\": %w", source, herr)
		}
		sum := sha256.Sum256([]byte(filepath.ToSlash(common)))
		root = filepath.Join(home, ".flywheel", "ledgers", hex.EncodeToString(sum[:])[:16])
	case strings.HasPrefix(value, "~/"):
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", false, fmt.Errorf("%s: ledger.dir %q: %w", source, value, herr)
		}
		root = filepath.Join(home, value[2:])
	default:
		root = filepath.Clean(value)
	}
	for _, f := range fences {
		if pathWithin(root, f) {
			return "", false, fmt.Errorf("%s: ledger.dir %s is inside %s; it must be outside the checkout and its git directory", source, root, f)
		}
	}
	// Two live logs: the in-tree log must be moved before the ledger dir is used.
	for _, name := range []string{legacyLogName, shardDirName} {
		inTree := filepath.Join(dir, ".flywheel", name)
		if _, serr := os.Lstat(inTree); checkInTree && serr == nil {
			return "", false, fmt.Errorf("%s: ledger.dir is %s but the in-tree log %s still exists (two live logs): run \"flywheel ledger move\" to move it into %s", source, root, inTree, root)
		}
	}
	return root, true, nil
}

// EventLogPresent is EventLogDir plus whether root holds a log (events.jsonl
// or events/). An external directory that holds none is a checkout whose log
// lives on another host: the CI audit of it is inconclusive (issue #464).
func EventLogPresent(dir string) (root string, external, present bool, err error) {
	root, external, err = EventLogDir(dir)
	if err != nil {
		return "", false, false, err
	}
	for _, name := range []string{legacyLogName, shardDirName} {
		if _, serr := os.Lstat(filepath.Join(root, name)); serr == nil {
			return root, external, true, nil
		}
	}
	return root, external, false, nil
}

// ledgerEventsRoot is EventLogDir's directory alone.
func ledgerEventsRoot(dir string) (string, error) {
	root, _, err := EventLogDir(dir)
	return root, err
}

// pathWithin reports whether p is parent or below it, comparing whole path
// segments of cleaned absolute paths (isPathEqual: case-insensitive on Windows).
func pathWithin(p, parent string) bool {
	for {
		if isPathEqual(p, parent) {
			return true
		}
		up := filepath.Dir(p)
		if up == p {
			return false
		}
		p = up
	}
}
