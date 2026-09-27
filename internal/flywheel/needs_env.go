package flywheel

import (
	"fmt"
	"os"
	"strings"
)

// lookupEnv reads flywheel's environment for the needs-env: check (issue
// #534); tests swap it for a fake.
var lookupEnv = os.LookupEnv

// MissingEnv returns the names in names that lookup reports unset or empty
// (whitespace-only counts as empty), in order (issue #534). It never returns
// or formats a value.
func MissingEnv(names []string, lookup func(string) (string, bool)) []string {
	var missing []string
	for _, n := range names {
		if v, ok := lookup(n); !ok || strings.TrimSpace(v) == "" {
			missing = append(missing, n)
		}
	}
	return missing
}

// needsEnvRefusal is the needs-env RuleRefusal for the names in needs that
// flywheel's environment lacks, or nil when every one is set (issue #534).
// run and validate return it before anything is recorded or any gate runs.
func needsEnvRefusal(needs []string) *RuleRefusal {
	missing := MissingEnv(needs, lookupEnv)
	if len(missing) == 0 {
		return nil
	}
	return &RuleRefusal{
		Rule: "needs-env",
		Fix:  fmt.Sprintf("environment variable(s) %s unset or empty in flywheel's environment; export each as its own statement in the dispatching shell (not chained into a backgrounded command), then rerun", strings.Join(missing, ", ")),
	}
}

// runPreflight runs one preflight: command for the check (issue #635); tests
// swap it for a fake.
var runPreflight = runGate

// preflightRefusal runs cmds in order in dir and returns the preflight
// RuleRefusal for the first that exits non-zero or cannot start, or nil when
// every one passes (issue #635). Only run calls it, before anything is
// recorded.
func preflightRefusal(dir string, cmds []string) *RuleRefusal {
	for _, c := range cmds {
		rc, _, out, err := runPreflight(dir, c)
		if err == nil && rc == 0 {
			continue
		}
		detail := ""
		if err != nil {
			detail = fmt.Sprintf("could not start: %v", err)
		} else {
			detail = fmt.Sprintf("exited %d", rc)
			if line := firstOutputLine(out); line != "" {
				detail += ": " + line
			}
		}
		return &RuleRefusal{
			Rule: "preflight",
			Fix:  fmt.Sprintf("preflight command %q %s; rerun `flywheel run <task>` once the precondition holds", c, detail),
		}
	}
	return nil
}

// firstOutputLine is out's first non-empty line, trimmed, cut to 200 chars.
func firstOutputLine(out []byte) string {
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > 200 {
				l = l[:200]
			}
			return l
		}
	}
	return ""
}
