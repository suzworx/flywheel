package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"slices"
	"strings"
)

// envNameRE matches a valid environment variable name for needs-env: (#534).
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// BriefHeader is the parsed key: value block at the top of a brief file, plus
// the SHA-256 of the whole file. Keys: owns, needs, needs-state, gate,
// live-gate, exclusive, review, line, kind.
type BriefHeader struct {
	Owns []string // comma-separated, annotations stripped, "none" and "-" dropped
	// OwnsNone is true when the header has an owns line and every entry on
	// every owns line is "none" or "-" (issue #693): the unit owns no paths,
	// as opposed to a brief with no owns line at all.
	OwnsNone bool `json:",omitempty"`
	Needs    []string
	// NeedsDeclared is true when the header has at least one needs: line, even needs: none (issue #307).
	NeedsDeclared bool `json:",omitempty"`
	// NeedsState lists repo-relative paths or directories (a trailing '/' for
	// a directory) that the gates need but an isolated --workdir will not
	// have: a database, a stack, git-ignored env files (issue #136).
	NeedsState []string // comma-separated, accumulated across repeated lines
	// NeedsStateLink is the subset of NeedsState annotated "(link)": paths
	// run --worktree links from the repo into the task's worktree (issue #430).
	NeedsStateLink []string `json:",omitempty"`
	// NeedsStateCopy is the subset of NeedsState annotated "(copy)": files
	// run --worktree copies from the repo into the task's worktree at every
	// dispatch, a git-ignored .env say (issue #471).
	NeedsStateCopy []string `json:",omitempty"`
	// NeedsStateInstall is the subset of NeedsState annotated "(install)":
	// paths run --worktree fills by running the package manager's offline
	// install in the task's worktree, a workspace's node_modules/ say (issue
	// #460).
	NeedsStateInstall []string `json:",omitempty"`
	Gates             []string // one shell command per line, order kept
	// LiveGates are `live-gate:` lines, one shell command per line, order
	// kept: gates that run only in the lead's verification pass
	// (`flywheel validate <task> --live`), never on a worker's own mocked
	// run (issue #152).
	LiveGates []string
	// QuietGates and QuietLiveGates are the 1-based indices into Gates and
	// LiveGates of the lines marked `[quiet]` (issue #411): gates that wait
	// for an idle host before they run.
	QuietGates     []int `json:",omitempty"`
	QuietLiveGates []int `json:",omitempty"`
	// Resources lists the shared host resources a `resources:` line names
	// (issue #697), accumulated across lines, each kept once, order kept:
	// validate holds an exclusive per-repository lock on each while a gate
	// that uses them runs. ResourceGates and ResourceLiveGates are the
	// 1-based indices of the gates marked `[resources]`; when none of a list
	// is marked, every gate of that list holds the locks.
	Resources         []string `json:",omitempty"`
	ResourceGates     []int    `json:",omitempty"`
	ResourceLiveGates []int    `json:",omitempty"`
	// resourcesInvalid holds names that are not lower-case resource names,
	// resourcesEmpty records an empty resources: line and resourcesDeclared
	// any resources: line; flywheel lint reports them.
	resourcesInvalid  []string
	resourcesEmpty    bool
	resourcesDeclared bool
	Exclusive         []string
	// NeedsEnv lists the environment variables a `needs-env:` line names
	// (issue #534), accumulated across lines, each kept once, order kept:
	// run and validate refuse while one is unset or empty. Values are never
	// read into the header.
	NeedsEnv []string `json:",omitempty"`
	// needsEnvInvalid holds needs-env entries that are not valid variable
	// names and needsEnvEmpty records an empty needs-env: line; flywheel lint
	// reports both. Neither is recorded in a dispatched event.
	needsEnvInvalid []string
	needsEnvEmpty   bool
	// Preflight lists the commands `preflight:` lines name (issue #635), in
	// order: flywheel run runs each before dispatch and refuses on the first
	// that exits non-zero, before any attempt is recorded.
	Preflight []string `json:",omitempty"`
	// preflightEmpty records an empty preflight: line; flywheel lint reports it.
	preflightEmpty bool
	// Skills lists the agent skills a `skills:` line names (issue #695),
	// accumulated across lines, each kept once, order kept: lint and run
	// refuse one that is not installed where the worker loads skills, and the
	// dispatch prompt tells the worker to load them.
	Skills []string `json:",omitempty"`
	// skillsEmpty records an empty skills: line; flywheel lint reports it.
	skillsEmpty bool
	// Agent is the Claude Code agent an `agent:` line names (issue #755), one
	// per brief, the first line kept: lint and run refuse one whose file is
	// missing, and the claude adapter passes it inline with --agents/--agent.
	Agent string `json:",omitempty"`
	// agentEmpty and agentRepeated record an empty and a second agent: line;
	// flywheel lint reports them.
	agentEmpty    bool
	agentRepeated bool
	Review        []string
	Line          string `json:",omitempty"`
	// Kind is the task's kind of work, the `kind:` line trimmed and
	// lowercased, the last one winning (issue #475): routing scores models per
	// kind. flywheel lint checks it against lint.kinds.
	Kind   string `json:",omitempty"`
	SHA256 string
}

// ParseBriefHeader reads the file at path and parses its header block; it is
// ParseBriefHeaderBytes after the read, the one parsing implementation.
func ParseBriefHeader(path string) (BriefHeader, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return BriefHeader{}, err
	}
	return ParseBriefHeaderBytes(b)
}

// ParseBriefHeaderBytes parses the header block at the top of a brief's bytes:
// the lines before the first blank line that is followed by a '#' heading, or
// the first 40 lines, whichever comes first. owns values are comma-separated
// and may continue on indented following lines; a trailing parenthesised
// annotation such as "(new)" is stripped from each entry. gate lines are one
// command per line and keep their order. The returned header carries the
// SHA-256 of the whole bytes, so a caller that already holds the exact bytes
// it sent (a dispatch hashing its prompt) gets a header and a hash describing
// the same immutable content.
func ParseBriefHeaderBytes(b []byte) (BriefHeader, error) {
	sum := sha256.Sum256(b)
	h := BriefHeader{SHA256: hex.EncodeToString(sum[:])}
	raw := strings.Split(string(b), "\n")
	var owns []string
	lastKey := ""
	for i := 0; i < len(raw) && i < 40; i++ {
		line := strings.TrimSuffix(raw[i], "\r")
		if strings.TrimSpace(line) == "" {
			if j := i + 1; j < len(raw) && strings.HasPrefix(strings.TrimSpace(raw[j]), "#") {
				break
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if lastKey == "owns" {
				owns = append(owns, line)
			}
			continue
		}
		key, val, ok := cutKey(line)
		if !ok {
			continue
		}
		lastKey = key
		// A `gate[quiet]:` or `live-gate[quiet]:` line is a gate that needs the
		// host to itself (issue #411): its command joins Gates/LiveGates like
		// any other and its 1-based index is recorded as quiet. An unknown
		// marker is kept as the plain key's line; flywheel lint warns on it.
		// Markers are a comma list (issue #697): `gate[quiet,resources]:` is
		// both quiet and a gate that holds the brief's resource locks.
		if base, marker, found := gateMarker(key); found {
			key = base
			for _, m := range strings.Split(marker, ",") {
				switch strings.TrimSpace(m) {
				case "quiet":
					if base == "gate" {
						h.QuietGates = append(h.QuietGates, len(h.Gates)+1)
					} else {
						h.QuietLiveGates = append(h.QuietLiveGates, len(h.LiveGates)+1)
					}
				case "resources":
					if base == "gate" {
						h.ResourceGates = append(h.ResourceGates, len(h.Gates)+1)
					} else {
						h.ResourceLiveGates = append(h.ResourceLiveGates, len(h.LiveGates)+1)
					}
				}
			}
		}
		switch key {
		case "owns":
			owns = append(owns, val)
		case "needs":
			h.NeedsDeclared = true
			h.Needs = append(h.Needs, NeedTargets(val)...)
		case "needs-state":
			// An entry annotated "(link)" is also linked from the repo into a
			// run --worktree tree (issue #430), one annotated "(copy)" is copied
			// there (issue #471), one annotated "(install)" is filled by the
			// package manager's offline install in that tree (issue #460);
			// NeedsState keeps the plain path.
			for _, entry := range strings.Split(val, ",") {
				e := strings.TrimSpace(entry)
				linked, copied, installed := false, false, false
				if p, ok := strings.CutSuffix(e, "(link)"); ok {
					e, linked = strings.TrimSpace(p), true
				} else if p, ok := strings.CutSuffix(e, "(copy)"); ok {
					e, copied = strings.TrimSpace(p), true
				} else if p, ok := strings.CutSuffix(e, "(install)"); ok {
					e, installed = strings.TrimSpace(p), true
				}
				if e != "" {
					h.NeedsState = append(h.NeedsState, e)
					if linked {
						h.NeedsStateLink = append(h.NeedsStateLink, e)
					}
					if copied {
						h.NeedsStateCopy = append(h.NeedsStateCopy, e)
					}
					if installed {
						h.NeedsStateInstall = append(h.NeedsStateInstall, e)
					}
				}
			}
		case "gate":
			h.Gates = append(h.Gates, val)
		case "live-gate":
			h.LiveGates = append(h.LiveGates, val)
		case "needs-env":
			// Environment variables the gates or live round read (issue #534):
			// run and validate refuse while one is unset. A name that is not a
			// valid variable name is kept apart for flywheel lint.
			if val == "" {
				h.needsEnvEmpty = true
			}
			for _, entry := range strings.Split(val, ",") {
				e := strings.TrimSpace(entry)
				if e == "" || strings.EqualFold(e, "none") {
					continue
				}
				if !envNameRE.MatchString(e) {
					h.needsEnvInvalid = append(h.needsEnvInvalid, e)
				} else if !slices.Contains(h.NeedsEnv, e) {
					h.NeedsEnv = append(h.NeedsEnv, e)
				}
			}
		case "preflight":
			// A command that must exit 0 before run dispatches (issue #635).
			if c := strings.TrimSpace(val); c != "" {
				h.Preflight = append(h.Preflight, c)
			} else {
				h.preflightEmpty = true
			}
		case "skills":
			// Agent skills the worker loads before any other work (issue #695).
			if strings.TrimSpace(val) == "" {
				h.skillsEmpty = true
			}
			for _, entry := range strings.Split(val, ",") {
				if e := strings.TrimSpace(entry); e != "" && !slices.Contains(h.Skills, e) {
					h.Skills = append(h.Skills, e)
				}
			}
		case "agent":
			// The Claude Code agent the worker runs as (issue #755).
			switch v := strings.TrimSpace(val); {
			case h.Agent != "" || h.agentEmpty:
				h.agentRepeated = true
			case v == "":
				h.agentEmpty = true
			default:
				h.Agent = v
			}
		case "resources":
			// Shared host resources the heavy gates use (issue #697).
			h.resourcesDeclared = true
			if strings.TrimSpace(val) == "" {
				h.resourcesEmpty = true
			}
			for _, entry := range strings.Split(val, ",") {
				e := strings.TrimSpace(entry)
				switch {
				case e == "":
				case !resourceNameRE.MatchString(e):
					h.resourcesInvalid = append(h.resourcesInvalid, e)
				case !slices.Contains(h.Resources, e):
					h.Resources = append(h.Resources, e)
				}
			}
		case "exclusive":
			h.Exclusive = append(h.Exclusive, val)
		case "review":
			h.Review = append(h.Review, val)
		case "line":
			h.Line = val
		case "kind":
			h.Kind = strings.ToLower(val)
		}
	}
	// `owns: none` (or `-`, any case) declares a unit that owns no paths
	// (issue #693): the entry is dropped, never kept as a path named "none".
	sawNone, sawPath := false, false
	for _, part := range owns {
		for _, entry := range strings.Split(part, ",") {
			e := stripAnnotation(strings.TrimSpace(entry))
			switch {
			case e == "":
			case isOwnsNone(e):
				sawNone = true
			default:
				sawPath = true
				h.Owns = append(h.Owns, e)
			}
		}
	}
	h.OwnsNone = sawNone && !sawPath
	// One pass over every needs: line, so an id repeated across lines is kept
	// once (#310 review).
	h.Needs = NeedTargets(h.Needs...)
	return h, nil
}

// cutKey splits a "key: value" line. It reports false for lines without a
// colon or with an empty key.
func cutKey(line string) (key, val string, ok bool) {
	k, v, found := strings.Cut(line, ":")
	if !found || strings.TrimSpace(k) == "" {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}

// gateMarker splits a `gate[m]` or `live-gate[m]` key into its base key and
// marker m (issue #411). found is false for any other key.
func gateMarker(key string) (base, marker string, found bool) {
	for _, b := range []string{"gate", "live-gate"} {
		if rest, ok := strings.CutPrefix(key, b+"["); ok && strings.HasSuffix(rest, "]") {
			return b, strings.TrimSpace(strings.TrimSuffix(rest, "]")), true
		}
	}
	return "", "", false
}

// isQuiet reports whether the 1-based index n is listed in quiet.
func isQuiet(quiet []int, n int) bool {
	for _, q := range quiet {
		if q == n {
			return true
		}
	}
	return false
}

// stripAnnotation removes a trailing parenthesised annotation: "a.go (new)"
// becomes "a.go". Lines without one pass through unchanged.
func stripAnnotation(s string) string {
	if i := strings.Index(s, " ("); i >= 0 && strings.HasSuffix(s, ")") {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// isOwnsNone reports whether an owns entry declares no owned paths: "none"
// (any case) or "-" (issue #693).
func isOwnsNone(e string) bool {
	return e == "-" || strings.EqualFold(e, "none")
}

// NeedTargets turns needs: values into task ids (issue #307): each value is
// split on commas and trimmed; empty entries, "-" and "none" (any case) are
// dropped, and a repeated id is kept once, in first-seen order. nil when
// nothing remains.
func NeedTargets(values ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, val := range values {
		for _, entry := range strings.Split(val, ",") {
			e := strings.TrimSpace(entry)
			if e == "" || e == "-" || strings.EqualFold(e, "none") {
				continue
			}
			if !seen[e] {
				result = append(result, e)
				seen[e] = true
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
