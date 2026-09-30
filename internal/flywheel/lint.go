package flywheel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// LintResult is every problem and warning found in one brief. Problems make
// the lint command exit 1; warnings alone keep it at 0.
type LintResult struct {
	Problems []string
	Warnings []string
}

// LintBrief checks one brief file for the problems that broke briefs before:
// a missing owns line, no gate line, no # TASK goal and no ## Checks report
// contract, and owns paths that do not exist under dir. A (new) owns entry
// skips the existence check; an entry ending in "/" must exist as a
// directory. An entry containing '*', '?' or '[' is a pattern (the same
// syntax ownsContains matches at validate time) and is checked with
// filepath.Glob instead of os.Stat: a pattern filepath.Glob rejects is
// invalid syntax, while a valid pattern matching nothing is reported like a
// missing path, with the (new) remedy. A (new) entry skips only the
// existence check; a (new) pattern still has its syntax validated. A negated
// entry ("!path", issue #388) is never existence-checked. Warnings cover the
// missing write rule, a missing needs line and a negated entry no positive
// entry covers.
//
// Owns resolve against the integration ref when the checkout is behind it
// (issue #694): when integrationRef(dir) names a ref and HEAD is neither its
// commit nor a descendant of it, that is a warning, entries are looked up in
// one git ls-tree of the ref (patterns with path.Match, as validate matches
// them), and a path only in the checkout is a problem naming the ref.
// Otherwise (no ref, or HEAD at the ref or ahead of it, as a stacked unit's
// worktree is): the checkout, as described above, with no ls-tree.
//
// Two more warnings guard the gates and owns against what CI catches later
// (issue #462). No gate matching the full-suite pattern (config
// lint.full_suite, else go test over ./... when dir has go.mod, else a
// package manager's test script when package.json defines one) is a warning,
// and a problem (zero gates included) when lint.full_suite_required is set
// (issue #652); owns under lint.full_suite_paths prefixes need each selected
// prefix's pattern instead, one finding per miss (fullSuiteMissing);
// an invalid lint.full_suite or lint.full_suite_paths pattern is a problem. When dir has go.mod and
// lint.importers is not false, go list finds each owned Go package's direct
// importers, and one whose tests owns does not cover is a warning. A gate
// that invokes a JavaScript test runner the repository does not use (config
// lint.test_runners, else the runners package.json names) is a warning too
// (issue #646). A gate whose command word is not a command (placeholder
// text) is a problem (issue #662, gateCommandProblems).
func LintBrief(dir, path string) (LintResult, error) {
	return lintBrief(dir, path, goList)
}

// lintBrief is LintBrief with the go list call injected, for tests.
func lintBrief(dir, path string, list func(string) (string, error)) (LintResult, error) {
	res, err := lintStructure(dir, path)
	if err != nil {
		return res, err
	}
	header, err := ParseBriefHeader(path)
	if err != nil {
		return res, fmt.Errorf("parse brief %s: %w", path, err)
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("config not read, lint defaults apply: %v", err))
	}
	res.Problems = append(res.Problems, kindProblems(path, header.Kind, cfg.LintKinds())...)
	lc := cfg.Lint
	if lc == nil {
		lc = &LintConfig{}
	}
	if missing, err := fullSuiteMissing(dir, lc, header.Gates, header.Owns); err != nil {
		res.Problems = append(res.Problems, err.Error())
	} else {
		for _, w := range missing {
			switch {
			case lc.FullSuiteRequired:
				res.Problems = append(res.Problems, w.String()+"; lint.full_suite_required is set)")
			case len(header.Gates) > 0 && w.Prefix == "":
				res.Warnings = append(res.Warnings, w.String()+"; set lint.full_suite to change it)")
			case len(header.Gates) > 0:
				res.Warnings = append(res.Warnings, w.String()+"; set lint.full_suite_paths to change it)")
			}
		}
	}
	res.Warnings = append(res.Warnings, gateRunnerWarnings(dir, header.Gates, lc.TestRunners)...)
	res.Problems = append(res.Problems, gateCommandProblems(dir, header.Gates, lc.GateCommands)...)
	if fileExists(filepath.Join(dir, "go.mod")) && (lc.Importers == nil || *lc.Importers) {
		b, err := os.ReadFile(path)
		if err != nil {
			return res, fmt.Errorf("read brief %s: %w", path, err)
		}
		res.Warnings = append(res.Warnings, importerWarnings(dir, header.Owns, ownsEntries(string(b)), list)...)
	}
	if b, err := os.ReadFile(path); err == nil {
		if w := webToolsWarning(string(b), cfg.DefaultWorker()); w != "" {
			res.Warnings = append(res.Warnings, w)
		}
	}
	// .claude/ owns (issue #696): the worker is resolved as Run resolves it
	// without --worker, the default replaced by the product line's worker.
	worker := cfg.DefaultWorker()
	if line, ok, lerr := cfg.LineFor(header); lerr == nil && ok {
		if lw, found := cfg.Worker(line.Worker); found {
			worker = lw
		}
	}
	if p := claudeDirProblem(worker, header.Owns); p != "" {
		res.Problems = append(res.Problems, p)
	}
	return res, nil
}

// claudeDirOwns returns, in order, the owns entries (patterns too) any of
// whose slash-separated path segments is exactly ".claude", after normalising
// `\` to `/` (issue #696). Negated entries ("!...") never count.
func claudeDirOwns(owns []string) []string {
	var out []string
	for _, o := range owns {
		if strings.HasPrefix(o, "!") {
			continue
		}
		if slices.Contains(strings.Split(strings.ReplaceAll(o, `\`, "/"), "/"), ".claude") {
			out = append(out, o)
		}
	}
	return out
}

// claudeDirProblem names the owns entries under .claude/ when w is a claude
// worker (issue #696): Claude Code protects .claude/, so every Write or Edit
// there is denied, even with bypassPermissions. Empty means no problem. Lint
// reports it as a problem; Run refuses with rule claude-dir.
func claudeDirProblem(w Worker, owns []string) string {
	if w.Adapter != "claude" {
		return ""
	}
	hits := claudeDirOwns(owns)
	if len(hits) == 0 {
		return ""
	}
	return fmt.Sprintf("owns %s under .claude/: worker %q (claude) cannot write there (Claude Code protects .claude/, even with bypassPermissions); own a staging path (e.g. staging/claude/...) and let the lead move the files after inspection, or staff the unit with a non-claude worker", strings.Join(hits, ", "), w.Name)
}

// webResearchWords are the brief phrases that ask for web research (issue
// #526), matched case-insensitively.
var webResearchWords = []string{"websearch", "webfetch", "web search", "search the web", "web fetch"}

// webToolsWarning warns when the brief's text below its header asks for web
// research and w, the worker that would run it (a brief names no worker, so
// the config's default), is a claude worker that cannot use the web tools:
// its permission_mode is not bypassPermissions and its allowed_tools have no
// entry starting with WebSearch or WebFetch. The harness would deny every call
// (issue #526). Empty means no warning.
func webToolsWarning(brief string, w Worker) string {
	if w.Adapter != "claude" || w.PermissionMode == "bypassPermissions" {
		return ""
	}
	body := brief
	if i := strings.Index(brief, "\n#"); i >= 0 {
		body = brief[i:]
	}
	body = strings.ToLower(body)
	if !slices.ContainsFunc(webResearchWords, func(s string) bool { return strings.Contains(body, s) }) {
		return ""
	}
	for _, t := range w.allowedTools() {
		if strings.HasPrefix(t, "WebSearch") || strings.HasPrefix(t, "WebFetch") {
			return ""
		}
	}
	return fmt.Sprintf("the brief asks for web research but worker %q (claude, permission_mode %s) cannot use WebSearch or WebFetch: every call would be denied; add WebSearch/WebFetch to its allowed_tools or set its permission_mode", w.Name, w.permissionMode())
}

// kindProblems checks the brief's kind: lines against allowed (issue #475): an
// empty kind: line is a problem, and so is a kind (the parsed, last one) not
// in allowed. A brief without kind: has none; nothing infers a kind.
func kindProblems(path, kind string, allowed []string) []string {
	var problems []string
	if b, err := os.ReadFile(path); err == nil {
		raw := strings.Split(string(b), "\n")
		for i := 0; i < len(raw) && i < 40; i++ {
			line := strings.TrimSuffix(raw[i], "\r")
			if strings.TrimSpace(line) == "" {
				if j := i + 1; j < len(raw) && strings.HasPrefix(strings.TrimSpace(raw[j]), "#") {
					break
				}
				continue
			}
			if key, val, ok := cutKey(line); ok && line[0] != ' ' && line[0] != '\t' && key == "kind" && val == "" {
				problems = append(problems, fmt.Sprintf("kind: line is empty; use one of: %s", strings.Join(allowed, ", ")))
			}
		}
	}
	if kind != "" && !slices.ContainsFunc(allowed, func(a string) bool { return strings.ToLower(a) == kind }) {
		problems = append(problems, fmt.Sprintf("kind %q is not one of: %s", kind, strings.Join(allowed, ", ")))
	}
	return problems
}

// lintStructure is LintBrief's structural checks: the header lines, headings,
// owns paths and gate quoting.
func lintStructure(dir, path string) (LintResult, error) {
	var res LintResult
	b, err := os.ReadFile(path)
	if err != nil {
		return res, fmt.Errorf("read brief %s: %w", path, err)
	}
	header, err := ParseBriefHeader(path)
	if err != nil {
		return res, fmt.Errorf("parse brief %s: %w", path, err)
	}
	content := string(b)
	if !hasHeading(content, "# TASK") {
		res.Problems = append(res.Problems, "no # TASK heading")
	}
	if len(header.Gates) == 0 {
		res.Problems = append(res.Problems, "no gate: line")
	}
	if !strings.Contains(content, "## Checks") {
		res.Problems = append(res.Problems, "no ## Checks section")
	}
	// `owns: none` (or `-`) is a present owns line that owns no paths (issue
	// #693); mixed with real paths it is a contradiction.
	var entries []ownsEntry
	none := false
	for _, e := range ownsEntries(content) {
		if isOwnsNone(e.path) {
			none = true
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 && !none {
		res.Problems = append(res.Problems, "missing owns: line")
	}
	if none && len(entries) > 0 {
		res.Problems = append(res.Problems, "owns: none cannot be combined with paths")
	}
	// Owns resolve against the integration ref units are based on when the
	// checkout is behind it (issue #694); otherwise (no ref, HEAD at the ref
	// or ahead of it, as a stacked unit is) against the checkout.
	var rt *refTree
	if ref := integrationRef(dir); ref != "" {
		if behind, w := headBehind(dir, ref); behind {
			rt = &refTree{dir: dir, ref: ref}
			res.Warnings = append(res.Warnings, w)
		}
	}
	for _, e := range entries {
		if neg, ok := negatedEntry(e.path); ok {
			if !negationExcludes(entries, neg) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("owns: !%s excludes nothing: no positive entry covers it", neg))
			}
			continue
		}
		if rt != nil {
			if ps, ok := rt.check(e); ok {
				res.Problems = append(res.Problems, ps...)
				continue
			}
		}
		if isOwnsPattern(e.path) {
			matches, err := filepath.Glob(filepath.Join(dir, e.path))
			if err != nil {
				res.Problems = append(res.Problems, fmt.Sprintf("owns pattern %s is invalid: %v; correct the pattern", e.path, err))
			} else if e.annotation != "new" && len(matches) == 0 {
				res.Problems = append(res.Problems, fmt.Sprintf("owns pattern %s matches no file; if the unit creates it, annotate it: %s (new)", e.path, e.path))
			}
			continue
		}
		if e.annotation == "new" {
			continue
		}
		st, err := os.Stat(filepath.Join(dir, e.path))
		if err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("owns path %s does not exist; if the unit creates it, annotate it: %s (new)", e.path, e.path))
			continue
		}
		if strings.HasSuffix(e.path, "/") && !st.IsDir() {
			res.Problems = append(res.Problems, fmt.Sprintf("owns path %s is not a directory", e.path))
		}
	}
	for i, lg := range header.LiveGates {
		for j, g := range header.Gates {
			if lg == g {
				res.Problems = append(res.Problems, fmt.Sprintf("live-gate %d repeats gate %d; a live gate must run the real path, not the mocked one", i+1, j+1))
				break
			}
		}
	}
	for _, e := range header.Exclusive {
		if strings.TrimSpace(e) == "" {
			res.Problems = append(res.Problems, "exclusive: entry is empty")
		}
	}
	// needs-env: (issue #534) is checked for shape only; lint never reads the
	// environment.
	for _, n := range header.needsEnvInvalid {
		res.Problems = append(res.Problems, fmt.Sprintf("needs-env name %q is not a valid environment variable name", n))
	}
	if header.needsEnvEmpty {
		res.Problems = append(res.Problems, "needs-env: line is empty; name variables or write needs-env: none")
	}
	// preflight: (issue #635) is checked for shape only; lint never runs it.
	if header.preflightEmpty {
		res.Problems = append(res.Problems, "preflight: line is empty; name a command or remove the line")
	}
	for i, g := range header.Gates {
		if gateBacktickInDoubleQuotes(g) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("gate %d has a backtick inside double quotes: bash runs it as command substitution; use single quotes or a script file", i+1))
		}
		if gateDiffCheckAgainstHead(g) {
			res.Warnings = append(res.Warnings, fmt.Sprintf(`gate %d runs "git diff" against HEAD; after the attempt commit it sees nothing — diff against "$FLYWHEEL_BASE"`, i+1))
		}
		if f := gateMaskingFilter(g); f != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf(`gate %d pipes into %s: the gate's exit status is %s's, not the command's; start it with "set -o pipefail;" or drop the filter`, i+1, f, f))
		}
	}
	for i, lg := range header.LiveGates {
		if gateBacktickInDoubleQuotes(lg) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("live-gate %d has a backtick inside double quotes: bash runs it as command substitution; use single quotes or a script file", i+1))
		}
	}
	// gate[quiet]: and live-gate[quiet]: are the known markers (issue #411);
	// any other marker parses as a plain gate, so it is flagged, not refused.
	for i, line := range strings.Split(content, "\n") {
		if i >= 40 {
			break
		}
		if key, _, ok := cutKey(strings.TrimSuffix(line, "\r")); ok {
			if base, marker, found := gateMarker(key); found && marker != "quiet" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s has unknown marker [%s]; the known marker is [quiet], and the line runs as a plain %s", key, marker, base))
			}
		}
	}
	if !header.NeedsDeclared {
		res.Warnings = append(res.Warnings, "no needs: line")
	}
	if !strings.Contains(content, "At most one write per response") {
		res.Warnings = append(res.Warnings, `write rule "At most one write per response" is absent`)
	}
	return res, nil
}

// refTree is the integration ref lint checks owns against (issue #694):
// workers are based on its tree, not the checkout's. The tree is listed once,
// with one git ls-tree, on the first entry that needs it.
type refTree struct {
	dir, ref string
	listed   bool
	failed   bool
	types    map[string]string // path -> object type (blob, tree, commit)
	names    []string
}

// refName is ref without refs/remotes/ or refs/heads/, e.g. origin/dev.
func refName(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/"), "refs/heads/")
}

// load lists the ref's tree once; false when git ls-tree failed, and lint
// then checks the checkout instead.
func (rt *refTree) load() bool {
	if !rt.listed {
		rt.listed = true
		out, err := gitRead(rt.dir, []string{"ls-tree", "-r", "-t", "-z", "--full-tree", rt.ref})
		if err != nil {
			rt.failed = true
			return false
		}
		rt.types = map[string]string{}
		for _, rec := range strings.Split(out, "\x00") {
			meta, p, ok := strings.Cut(rec, "\t")
			if f := strings.Fields(meta); ok && len(f) == 3 {
				rt.types[p] = f[1]
				rt.names = append(rt.names, p)
			}
		}
	}
	return !rt.failed
}

// check is one positive owns entry's problems against the ref, with the
// checkout's texts naming the ref; ok is false when the ref's tree could not
// be listed. A (new) literal entry is never checked and lists nothing; a
// pattern has its syntax checked with path.Match, the matcher validate uses.
func (rt *refTree) check(e ownsEntry) (problems []string, ok bool) {
	name := refName(rt.ref)
	if isOwnsPattern(e.path) {
		if _, err := path.Match(e.path, ""); err != nil {
			return []string{fmt.Sprintf("owns pattern %s is invalid: %v; correct the pattern", e.path, err)}, true
		}
		if e.annotation == "new" {
			return nil, true
		}
		if !rt.load() {
			return nil, false
		}
		if !slices.ContainsFunc(rt.names, func(p string) bool { return ownsEntryMatches(e.path, p) }) {
			return []string{fmt.Sprintf("owns pattern %s matches no file on %s; if the unit creates it, annotate it: %s (new)", e.path, name, e.path)}, true
		}
		return nil, true
	}
	if e.annotation == "new" {
		return nil, true
	}
	if !rt.load() {
		return nil, false
	}
	typ, found := rt.types[strings.TrimSuffix(filepath.ToSlash(e.path), "/")]
	switch {
	case !found:
		return []string{fmt.Sprintf("owns path %s does not exist on %s; if the unit creates it, annotate it: %s (new)", e.path, name, e.path)}, true
	case strings.HasSuffix(e.path, "/") && typ != "tree":
		return []string{fmt.Sprintf("owns path %s is not a directory on %s", e.path, name)}, true
	}
	return nil, true
}

// headBehind reports whether the checkout's HEAD is neither ref's commit nor
// a descendant of it (issue #694), with the warning to give: owns read from
// that tree may not exist where workers start. A HEAD at the ref or ahead of
// it (a stacked unit, local commits) is not behind. One rev-parse, then at
// most one merge-base; any git failure means not behind.
func headBehind(dir, ref string) (behind bool, warning string) {
	out, err := gitRead(dir, []string{"rev-parse", "HEAD", ref + "^{commit}"})
	f := strings.Fields(out)
	if err != nil || len(f) != 2 || f[0] == f[1] {
		return false, ""
	}
	rc, _, _, err := runCmdSplit(dir, gitArgs([]string{"merge-base", "--is-ancestor", f[1], f[0]}), nil)
	if err != nil || rc != 1 {
		return false, ""
	}
	name := refName(ref)
	return true, fmt.Sprintf("checkout HEAD %s is not at %s (%s): workers are based on %s; read and write owns from that tree", f[0][:7], name, f[1][:7], name)
}

// gateDiffCheckAgainstHead reports whether a gate runs `git diff --check`
// with no revision (issue #470). flywheel run commits the attempt before
// validate, so such a gate diffs against the new HEAD and sees nothing. The
// detection is deliberately simple: the gate is split into commands at `&&`,
// `||`, `;` and `|`, and a command warns when it is `git diff` with --check
// among its args and no arg that is neither a flag (leading "-") nor after
// `--` (a pathspec). Any such arg, "$FLYWHEEL_BASE" included, counts as a
// revision. `git diff --no-index` compares files, not revisions, and never
// warns; `git diff --exit-code` without --check (a regenerated tree checked
// against HEAD) never warns either.
func gateDiffCheckAgainstHead(gate string) bool {
	s := gate
	for _, sep := range []string{"&&", "||", "|", ";"} {
		s = strings.ReplaceAll(s, sep, " ; ")
	}
	toks := strings.Fields(s)
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] != "git" || toks[i+1] != "diff" {
			continue
		}
		check, rev, noIndex, dashdash := false, false, false, false
		for _, a := range toks[i+2:] {
			if a == ";" {
				break
			}
			switch {
			case dashdash:
			case a == "--":
				dashdash = true
			case a == "--check":
				check = true
			case a == "--no-index":
				noIndex = true
			case strings.HasPrefix(a, "-"):
			default:
				rev = true
			}
		}
		if check && !rev && !noIndex {
			return true
		}
	}
	return false
}

// gateFilters are the command words whose exit status says nothing about the
// command piped into them (issue #704).
var gateFilters = []string{"grep", "egrep", "fgrep", "rg", "tail", "head", "sed", "awk", "cut", "sort", "uniq", "tee", "cat", "wc", "tr", "findstr"}

// gateFilterMasks reports whether a gate's exit status is a filter's rather
// than the checked command's (issue #704): gates run under `bash -c` without
// pipefail, so `go test ./... | tail -5` passes when go test fails. See
// gateMaskingFilter for the rules.
func gateFilterMasks(gate string) bool {
	return gateMaskingFilter(gate) != ""
}

// gateMaskingFilter is the base name of the filter ending the first pipeline
// of two or more stages in gate, or "" (issue #704). Commands split at ;, &&,
// ||, & and newlines, and stages at | and |&, only outside quotes and $( ) or
// backtick substitutions, whose text is never inspected. A pipeline that
// starts with echo or printf filters captured text and never counts, and a
// gate that mentions pipefail or PIPESTATUS never counts.
func gateMaskingFilter(gate string) string {
	if strings.Contains(gate, "pipefail") || strings.Contains(gate, "PIPESTATUS") {
		return ""
	}
	var cmds [][]string
	var stages []string
	var cur strings.Builder
	endStage := func() { stages = append(stages, cur.String()); cur.Reset() }
	endCmd := func() { endStage(); cmds = append(cmds, stages); stages = nil }
	inSingle, inDouble, inBacktick, depth := false, false, false, 0
	for i := 0; i < len(gate); i++ {
		c := gate[i]
		visible := depth == 0 && !inBacktick
		switch {
		case inSingle:
			inSingle = c != '\''
		case c == '\\':
			if visible && i+1 < len(gate) {
				cur.WriteString(gate[i : i+2])
			}
			i++
			continue
		case inBacktick:
			inBacktick = c != '`'
		case inDouble:
			inDouble = c != '"'
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '`' || c == '$' && i+1 < len(gate) && gate[i+1] == '(':
			if visible {
				cur.WriteString("$S")
			}
			if c == '`' {
				inBacktick = true
			} else {
				depth++
				i++
			}
		case depth > 0:
			switch c {
			case '(':
				depth++
			case ')':
				depth--
			}
		case c == ';' || c == '\n':
			endCmd()
		case c == '|' && i+1 < len(gate) && gate[i+1] == '|':
			endCmd()
			i++
		case c == '|':
			endStage()
			if i+1 < len(gate) && gate[i+1] == '&' {
				i++
			}
		case c == '&' && i+1 < len(gate) && gate[i+1] == '&':
			endCmd()
			i++
		case c == '&' && (i == 0 || !strings.ContainsRune("<>", rune(gate[i-1]))) && (i+1 >= len(gate) || gate[i+1] != '>'):
			endCmd()
		default:
			cur.WriteByte(c)
		}
		// Quoted text stays in the stage, so a quoted command word still reads.
		if visible && (inSingle || inDouble || c == '\'' || c == '"') {
			cur.WriteByte(c)
		}
	}
	endCmd()
	for _, p := range cmds {
		if len(p) < 2 {
			continue
		}
		if first := stageCommandWord(p[0]); first == "echo" || first == "printf" {
			continue
		}
		if last := stageCommandWord(p[len(p)-1]); slices.Contains(gateFilters, last) {
			return last
		}
	}
	return ""
}

// stageCommandWord is the base name of a pipeline stage's command word, with
// leading !, (, { and VAR=value assignments skipped and .exe dropped.
func stageCommandWord(stage string) string {
	s := strings.TrimLeft(stage, " \t\r!({")
	for {
		w, rest := gateWord(s)
		if !envAssignRE.MatchString(w) {
			return strings.TrimSuffix(path.Base(strings.ReplaceAll(w, `\`, "/")), ".exe")
		}
		s = rest
	}
}

// gateBacktickInDoubleQuotes reports whether a gate has an unescaped backtick
// inside a double-quoted span (issue #366). Gates run under `bash -c`, where
// such a backtick is command substitution, so  node -e "... `x` ..."  fails on
// correct work. One pass tracks quote state: outside quotes, ' opens a
// single-quoted span that ends at the next ' (no escapes) and " opens a
// double-quoted span; inside it \ escapes the next character and " closes it.
// "$(...)" is not flagged: the repository's own gates rely on it.
func gateBacktickInDoubleQuotes(gate string) bool {
	inSingle, inDouble := false, false
	for i := 0; i < len(gate); i++ {
		c := gate[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			switch c {
			case '\\':
				i++
			case '"':
				inDouble = false
			case '`':
				return true
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		}
	}
	return false
}

// isOwnsPattern reports whether an owns entry is a shell pattern rather than
// a literal path: it contains '*', '?' or '['. ownsContains (gauges.go)
// already matches these with path.Match at validate time; lint checks them
// the same way against the integration ref's tree (refTree) when the
// checkout is behind it, else with
// filepath.Glob against dir instead of os.Stat.
func isOwnsPattern(p string) bool {
	return strings.ContainsAny(p, "*?[")
}

// negationExcludes reports whether some positive owns entry could contain the
// negated entry neg (issue #388): the positive entry matches neg itself (a
// "dir/" negation also without its '/'), neg covers the positive entry, or,
// when either is a pattern, one literal prefix (the text before the first
// '*', '?' or '[') starts with the other.
func negationExcludes(entries []ownsEntry, neg string) bool {
	for _, e := range entries {
		pos := e.path
		if _, n := negatedEntry(pos); n {
			continue
		}
		if ownsEntryMatches(pos, neg) || ownsEntryMatches(pos, strings.TrimSuffix(neg, "/")) || ownsEntryMatches(neg, pos) {
			return true
		}
		if isOwnsPattern(neg) || isOwnsPattern(pos) {
			np, pp := literalPrefix(neg), literalPrefix(pos)
			if strings.HasPrefix(np, pp) || strings.HasPrefix(pp, np) {
				return true
			}
		}
	}
	return false
}

// literalPrefix returns an owns entry up to its first '*', '?' or '['.
func literalPrefix(p string) string {
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		return p[:i]
	}
	return p
}

// defaultFullSuite is the full-suite gate pattern for dir's toolchain (issue
// #462): go test over ./... when go.mod exists, else a package manager's test
// script when package.json has one, else "" (no check).
func defaultFullSuite(dir string) string {
	if fileExists(filepath.Join(dir, "go.mod")) {
		return `go test\b.*\./\.\.\.`
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pj struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &pj) == nil && strings.TrimSpace(pj.Scripts["test"]) != "" {
		return `\b(npm|pnpm|yarn|bun)( run)? test\b`
	}
	return ""
}

// fullSuiteWant is one full-suite pattern a brief's gates must match (issue
// #652): Prefix is the lint.full_suite_paths key that selected it, "" for the
// global pattern.
type fullSuiteWant struct {
	Prefix, Pattern string
}

// String is the want's "full suite" phrase and pattern, as lint and run
// report a miss.
func (w fullSuiteWant) String() string {
	if w.Prefix == "" {
		return "no gate runs the full suite (want a gate matching " + w.Pattern
	}
	return "no gate runs the full suite for " + w.Prefix + " (want a gate matching " + w.Pattern
}

// fullSuiteMissing returns the full-suite wants no gate in gates matches
// (issue #652), sorted by prefix. Each owns path (slash form) selects the
// pattern of the longest lint.full_suite_paths key that prefixes it; when at
// least one did, the selected patterns are the wants. Otherwise the one want
// is lc.FullSuite, else defaultFullSuite(dir), and "" means no check. An
// invalid pattern returns an error naming its config key, keys tried in
// sorted order. No gates at all misses every want; lintBrief only warns on
// that when there are gates.
func fullSuiteMissing(dir string, lc *LintConfig, gates, owns []string) ([]fullSuiteWant, error) {
	if lc == nil {
		lc = &LintConfig{}
	}
	keys := slices.Sorted(maps.Keys(lc.FullSuitePaths))
	for _, k := range keys {
		if _, err := regexp.Compile(lc.FullSuitePaths[k]); err != nil {
			return nil, fmt.Errorf("config lint.full_suite_paths[%q] %q is not a valid regular expression: %v", k, lc.FullSuitePaths[k], err)
		}
	}
	var wants []fullSuiteWant
	for _, o := range owns {
		o = filepath.ToSlash(o)
		best := ""
		for _, k := range keys {
			if strings.HasPrefix(o, k) && len(k) > len(best) {
				best = k
			}
		}
		if best != "" && !slices.ContainsFunc(wants, func(w fullSuiteWant) bool { return w.Prefix == best }) {
			wants = append(wants, fullSuiteWant{Prefix: best, Pattern: lc.FullSuitePaths[best]})
		}
	}
	if len(wants) == 0 {
		pattern := lc.FullSuite
		if pattern == "" {
			pattern = defaultFullSuite(dir)
		}
		if pattern == "" {
			return nil, nil
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("config lint.full_suite %q is not a valid regular expression: %v", pattern, err)
		}
		wants = []fullSuiteWant{{Pattern: pattern}}
	}
	slices.SortFunc(wants, func(a, b fullSuiteWant) int { return strings.Compare(a.Prefix, b.Prefix) })
	var missing []fullSuiteWant
	for _, w := range wants {
		if !slices.ContainsFunc(gates, regexp.MustCompile(w.Pattern).MatchString) {
			missing = append(missing, w)
		}
	}
	return missing, nil
}

// jsRunnerDeps maps the package.json dependency that brings a JavaScript test
// runner to the runner's name (issue #646).
var jsRunnerDeps = map[string]string{"vitest": "vitest", "jest": "jest", "mocha": "mocha", "ava": "ava", "@playwright/test": "playwright"}

// envAssignRE matches a shell VAR=value assignment ahead of a command.
var envAssignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// jsRunners returns the JavaScript test runners the shell command s invokes
// (issue #646). s is split into commands at ; & | ( ) { } and newlines; a
// command runs a runner when its program (after VAR=value assignments and an
// npx, bunx, yarn or pnpm exec prefix) is vitest, jest, mocha or ava, is
// playwright followed by test, or is node with --test among its leading
// flags. A runner named as an argument (grep jest file) does not count.
func jsRunners(s string) []string {
	var out []string
	for _, cmd := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(";&|(){}\n", r) }) {
		f := strings.Fields(cmd)
		for len(f) > 0 && envAssignRE.MatchString(f[0]) {
			f = f[1:]
		}
		switch {
		case len(f) > 0 && (f[0] == "npx" || f[0] == "bunx" || f[0] == "yarn"):
			f = f[1:]
		case len(f) > 1 && f[0] == "pnpm" && f[1] == "exec":
			f = f[2:]
		}
		if len(f) == 0 {
			continue
		}
		var r string
		switch prog := path.Base(strings.ReplaceAll(f[0], `\`, "/")); prog {
		case "vitest", "jest", "mocha", "ava":
			r = prog
		case "playwright":
			if len(f) > 1 && f[1] == "test" {
				r = "playwright"
			}
		case "node":
			for _, a := range f[1:] {
				if a == "--test" {
					r = "node:test"
				}
				if !strings.HasPrefix(a, "-") {
					break
				}
			}
		}
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// gateRunnerWarnings warns, once per gate, about a gate that invokes a
// JavaScript test runner the repository does not use (issue #646). The
// repository's runners are override when non-empty (lint.test_runners), else
// those dir/package.json's test and test:* scripts invoke plus those its
// dependencies and devDependencies bring. No package.json, one that does not
// parse, or one naming no runner means no check. A gate that calls the
// package manager's test script (npm test) invokes no runner and never warns.
func gateRunnerWarnings(dir string, gates []string, override []string) []string {
	repo := override
	if len(repo) == 0 {
		b, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			return nil
		}
		var pj struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]any    `json:"dependencies"`
			DevDependencies map[string]any    `json:"devDependencies"`
		}
		if json.Unmarshal(b, &pj) != nil {
			return nil
		}
		for k, v := range pj.Scripts {
			if k == "test" || strings.HasPrefix(k, "test:") {
				repo = append(repo, jsRunners(v)...)
			}
		}
		for dep, r := range jsRunnerDeps {
			if _, ok := pj.Dependencies[dep]; ok {
				repo = append(repo, r)
			}
			if _, ok := pj.DevDependencies[dep]; ok {
				repo = append(repo, r)
			}
		}
		slices.Sort(repo)
		repo = slices.Compact(repo)
	}
	if len(repo) == 0 {
		return nil
	}
	var out []string
	for i, g := range gates {
		for _, r := range jsRunners(g) {
			if !slices.Contains(repo, r) {
				out = append(out, fmt.Sprintf("gate %d runs %s but the repository's tests use %s: run them through its test script (for example npm test -- <file>) or that runner; set lint.test_runners to change it", i+1, r, strings.Join(repo, ", ")))
				break
			}
		}
	}
	return out
}

// gateBuiltins are the bash builtins and keywords a gate may start with; they
// resolve without a process (issue #662).
var gateBuiltins = []string{"true", "false", "test", "[", "[[", "!", "for", "if", "while", "until", "case", "echo", "printf", "cd", "export", "set", "exit", "command", "type", "read", "source", ".", ":"}

// gateResolveScript prints each argument the shell cannot resolve as a
// command; the words are arguments, never interpolated into the script.
const gateResolveScript = `for w in "$@"; do command -v -- "$w" >/dev/null 2>&1 || printf '%s\n' "$w"; done`

// gateWord reads one shell word from s after leading blanks, quotes honoured
// and removed; it ends at a blank or one of ;&|()<>, and rest follows it.
func gateWord(s string) (word, rest string) {
	s = strings.TrimLeft(s, " \t")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'' || c == '"':
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				b.WriteString(s[i+1:])
				return b.String(), ""
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case strings.IndexByte(" \t\r\n;&|()<>", c) >= 0:
			return b.String(), s[i:]
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), ""
}

// gateCommandWord is the command word of gate's first simple command (issue
// #662): leading !, ( and {, VAR=value assignments, env, timeout <N> (and its
// flags) and a leading cd <dir> && or cd <dir>; are skipped.
func gateCommandWord(gate string) string {
	s := gate
	for {
		s = strings.TrimLeft(s, " \t\r\n!({")
		w, rest := gateWord(s)
		switch {
		case envAssignRE.MatchString(w), w == "env":
			s = rest
		case w == "timeout":
			for {
				a, r := gateWord(rest)
				if a == "" || (!strings.HasPrefix(a, "-") && (a[0] < '0' || a[0] > '9')) {
					break
				}
				rest = r
			}
			s = rest
		case w == "cd":
			_, r := gateWord(rest)
			r = strings.TrimLeft(r, " \t")
			if n, ok := strings.CutPrefix(r, "&&"); ok {
				s = n
			} else if n, ok := strings.CutPrefix(r, ";"); ok {
				s = n
			} else {
				return w
			}
		default:
			return w
		}
	}
}

// gateCommandProblems reports each gate whose command word is not a command,
// placeholder text like "(as the brief)" (issue #662); no gate is executed. A
// word is a command when allowed (lint.gate_commands) lists it, it is a
// builtin or keyword, it names a path that exists under dir, or
// exec.LookPath finds it; only the words still unresolved go, once, to
// gateResolveScript in the shell gates run in (ShellArgv). Without bash (cmd)
// that step is skipped and nothing is reported. A gate that is placeholder
// text by gateWrapped's rule is reported as such instead, never also as not a
// command.
func gateCommandProblems(dir string, gates, allowed []string) []string {
	return gateCommandProblemsWith(dir, gates, allowed, exec.LookPath, shellUnresolved)
}

// gateCommandProblemsWith is gateCommandProblems with the lookups injected,
// for tests.
func gateCommandProblemsWith(dir string, gates, allowed []string, lookPath func(string) (string, error), resolve func(string, []string) []string) []string {
	words := make([]string, len(gates))
	wrapped := make([]bool, len(gates))
	placeholder := make([]bool, len(gates))
	var pending []string
	for i, g := range gates {
		w := gateCommandWord(g)
		if inner, ok := gateWrapped(g); ok {
			wrapped[i], w = true, gateCommandWord(inner)
			lower := strings.ToLower(g)
			for _, p := range gatePlaceholderPhrases {
				if strings.Contains(lower, p) {
					placeholder[i] = true
				}
			}
			if placeholder[i] {
				continue
			}
		}
		if w == "" || strings.ContainsAny(w, "$`") || slices.Contains(allowed, w) || slices.Contains(gateBuiltins, w) {
			continue
		}
		if strings.Contains(w, "/") {
			if _, err := os.Stat(filepath.Join(dir, w)); err == nil {
				continue
			}
		} else if _, err := lookPath(w); err == nil {
			continue
		}
		words[i] = w
		if !slices.Contains(pending, w) {
			pending = append(pending, w)
		}
	}
	var unresolved []string
	if len(pending) > 0 {
		unresolved = resolve(dir, pending)
	}
	var out []string
	for i, w := range words {
		unres := w != "" && slices.Contains(unresolved, w)
		switch {
		case placeholder[i] || (wrapped[i] && unres):
			out = append(out, fmt.Sprintf("gate %d: %q looks like placeholder text, not a command; a delta repeats the brief's gate: lines, or declares none to inherit them", i+1, gates[i]))
		case unres:
			out = append(out, fmt.Sprintf("gate %d: %q is not a command (placeholder text?); a delta repeats the brief's gate: lines, or declares none to inherit them", i+1, w))
		}
	}
	return out
}

// gatePlaceholderPhrases are the lower-case phrases that mark a wrapped gate
// as placeholder text (issue #662).
var gatePlaceholderPhrases = []string{"as the brief", "as in the brief", "same as", "see brief", "see the brief", "as above", "unchanged", "inherit", "tbd", "todo"}

// gateWrapped reports whether gate is a placeholder candidate and returns the
// text inside its wrapper (issue #662). The rule: the gate is wrapped wholly in
// (...) or <...> (never [...]: [ -f x ] is a real test) and contains none of
// &&, ||, ;, |, $. A candidate is placeholder text when its text contains one
// of gatePlaceholderPhrases (case-insensitive) or its first word inside the
// wrapper does not resolve; gateCommandProblemsWith applies both, independent
// of whether the word resolves on this host for the phrases. An unwrapped gate
// is left to the command check (go test ./... or grep -q unchanged f never are
// placeholders).
func gateWrapped(gate string) (inner string, ok bool) {
	g := strings.TrimSpace(gate)
	if len(g) < 2 || strings.ContainsAny(g, ";|$") || strings.Contains(g, "&&") {
		return "", false
	}
	if (g[0] == '(' && g[len(g)-1] == ')') || (g[0] == '<' && g[len(g)-1] == '>') {
		return g[1 : len(g)-1], true
	}
	return "", false
}

// shellUnresolved runs gateResolveScript once in dir with words as arguments
// and returns the words it printed. A cmd shell (no bash) or a shell that
// fails resolves every word: nothing is reported that was not checked.
func shellUnresolved(dir string, words []string) []string {
	argv := ShellArgv(gateResolveScript)
	if argv[0] == "cmd" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := append(append(argv[1:], "flywheel-gatecheck"), words...)
	cmd := exec.CommandContext(ctx, argv[0], args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var bad []string
	for _, line := range strings.Split(string(out), "\n") {
		if w := strings.TrimSuffix(line, "\r"); w != "" {
			bad = append(bad, w)
		}
	}
	return bad
}

// fileExists reports whether p exists and is not a directory.
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// goListFormat is the go list template the importer check parses: one package
// per line, tab-separated, list fields comma-joined.
const goListFormat = "{{.ImportPath}}\t{{.Dir}}\t{{join .Imports \",\"}}\t{{join .TestImports \",\"}}\t{{join .XTestImports \",\"}}\t{{join .TestGoFiles \",\"}}\t{{join .XTestGoFiles \",\"}}"

// goList runs go list over every package under dir, bounded by 60 seconds.
func goList(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-f", goListFormat, "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return "", fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return string(out), err
}

// goPackage is one go list line: the package, everything it and its tests
// import, and its test file names.
type goPackage struct {
	importPath string
	imports    []string
	testFiles  []string
}

// parseGoList parses goListFormat output; a line without seven fields is
// skipped.
func parseGoList(out string) []goPackage {
	split := func(s string) []string { return strings.FieldsFunc(s, func(r rune) bool { return r == ',' }) }
	var pkgs []goPackage
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(f) != 7 {
			continue
		}
		p := goPackage{importPath: f[0]}
		for _, s := range f[2:5] {
			p.imports = append(p.imports, split(s)...)
		}
		for _, s := range f[5:7] {
			p.testFiles = append(p.testFiles, split(s)...)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// goModulePath returns the module path go.mod in dir declares, "" if none.
func goModulePath(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// importerWarnings warns, per owned Go package, about its direct importers
// whose test files owns does not cover (issue #462). Owned packages are the
// directories of literal and (new) non-test .go entries; a test file a negated
// entry covers is a deliberate exclusion. A go list failure is a warning.
func importerWarnings(dir string, owns []string, entries []ownsEntry, list func(string) (string, error)) []string {
	var dirs []string
	for _, e := range entries {
		p := filepath.ToSlash(e.path)
		if _, neg := negatedEntry(p); neg || isOwnsPattern(p) || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			continue
		}
		if d := path.Dir(p); !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		return nil
	}
	out, err := list(dir)
	if err != nil {
		return []string{fmt.Sprintf("importer check skipped: go list: %v", err)}
	}
	mod := goModulePath(dir)
	if mod == "" {
		return []string{"importer check skipped: go.mod declares no module"}
	}
	pkgs := parseGoList(out)
	var warns []string
	for _, d := range dirs {
		p := mod
		if d != "." {
			p = mod + "/" + d
		}
		var unowned []string
		for _, q := range pkgs {
			qd, inMod := strings.CutPrefix(q.importPath, mod+"/")
			if q.importPath == mod {
				qd, inMod = ".", true
			}
			if q.importPath == p || !inMod || !slices.Contains(q.imports, p) {
				continue
			}
			for _, f := range q.testFiles {
				if fp := path.Join(qd, f); !ownsNegated(owns, fp) && !ownsContains(owns, fp) {
					unowned = append(unowned, q.importPath)
					break
				}
			}
		}
		if len(unowned) > 0 {
			warns = append(warns, fmt.Sprintf("owns changes %s, imported by %s whose tests are not owned", p, strings.Join(unowned, ", ")))
		}
	}
	return warns
}

// hasHeading reports whether content has a line starting with prefix, e.g. a
// "# TASK" heading.
func hasHeading(content, prefix string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// ownsEntry is one owns value with its trailing parenthesised annotation kept,
// so (new) entries can skip the existence check.
type ownsEntry struct {
	path       string
	annotation string
}

// ownsEntries parses the owns lines the way ParseBriefHeader does but keeps
// each entry's trailing parenthesised annotation.
func ownsEntries(content string) []ownsEntry {
	lines := strings.Split(content, "\n")
	var owned []string
	lastKey := ""
	for i := 0; i < len(lines) && i < 40; i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if strings.TrimSpace(line) == "" {
			if j := i + 1; j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "#") {
				break
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if lastKey == "owns" {
				owned = append(owned, line)
			}
			continue
		}
		key, val, ok := cutKey(line)
		if !ok {
			continue
		}
		lastKey = key
		if key == "owns" {
			owned = append(owned, val)
		}
	}
	var entries []ownsEntry
	for _, part := range owned {
		for _, e := range strings.Split(part, ",") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			path, annotation := e, ""
			if i := strings.Index(e, " ("); i >= 0 && strings.HasSuffix(e, ")") {
				path = strings.TrimSpace(e[:i])
				annotation = e[i+2 : len(e)-1]
			}
			entries = append(entries, ownsEntry{path: path, annotation: annotation})
		}
	}
	return entries
}
