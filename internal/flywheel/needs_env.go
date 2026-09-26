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
