package flywheel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ReleaseAuditOptions drives `flywheel audit --release` (issue #420): the
// release quality station. Every repository file is read at the tag.
type ReleaseAuditOptions struct {
	// Version to audit, "0.21.1" or "v0.21.1"; required.
	Version string
	// Prev is the previous release tag; empty means the highest vA.B.C tag
	// lower than Version (none: the whole history up to the tag).
	Prev string
	// Session is the auditor session; required.
	Session string
	// Artifact and Checksums are local files standing in for the download;
	// both or neither.
	Artifact  string
	Checksums string
	// Notes are extra release-notes files whose flywheel spans are checked.
	Notes []string
	// Calibration is a `flywheel review calibrate` report; MinRecall < 0
	// skips the calibration check.
	Calibration string
	MinRecall   float64
	// DownloadBase, Repo, GOOS and GOARCH are as UpgradeOptions.
	DownloadBase string
	Repo         string
	GOOS         string
	GOARCH       string
	// Run runs the audited binary; default exec with a 60s timeout.
	Run func(bin string, args ...string) ([]byte, error)
	// Now stamps the event; default time.Now.
	Now func() time.Time
}

// ReleaseCheck is one release audit check: Status is pass, fail, skipped or
// inconclusive.
type ReleaseCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ReleaseAuditResult is the audit of one release: Verdict is fail when any
// check failed, else inconclusive when any was inconclusive, else pass.
type ReleaseAuditResult struct {
	Version string         `json:"version"`
	Tag     string         `json:"tag"`
	Prev    string         `json:"prev,omitempty"`
	Checks  []ReleaseCheck `json:"checks"`
	Verdict string         `json:"verdict"`
}

// defaultRun runs bin with a 60s timeout and returns its combined output.
func defaultRun(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, bin, args...).CombinedOutput()
}

// AuditRelease runs the release checks in order (tag, changelog, binary,
// commands, docs, calibration), records one release_audited event and returns
// the result. A missing session or a usage problem returns before any check
// and records nothing.
func AuditRelease(dir string, o ReleaseAuditOptions) (ReleaseAuditResult, error) {
	if o.Session == "" {
		return ReleaseAuditResult{}, &RuleRefusal{Rule: "T4", Fix: "an auditor --session is required"}
	}
	ver := trimV(o.Version)
	if _, ok := parseSemver(ver); !ok {
		return ReleaseAuditResult{}, fmt.Errorf("release version %q is not X.Y.Z", o.Version)
	}
	if (o.Artifact == "") != (o.Checksums == "") {
		return ReleaseAuditResult{}, fmt.Errorf("--artifact and --checksums go together")
	}
	if o.MinRecall >= 0 && o.Calibration == "" {
		return ReleaseAuditResult{}, fmt.Errorf("--min-recall needs --calibration")
	}
	up := UpgradeOptions{DownloadBase: o.DownloadBase, Repo: o.Repo, GOOS: o.GOOS, GOARCH: o.GOARCH}
	up.applyDefaults()
	if o.Run == nil {
		o.Run = defaultRun
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	res := ReleaseAuditResult{Version: ver, Tag: "v" + ver}
	tmp, err := os.MkdirTemp("", "flywheel-release-audit-*")
	if err != nil {
		return ReleaseAuditResult{}, err
	}
	defer os.RemoveAll(tmp)

	tc, tagOK := checkReleaseTag(dir, res.Tag)
	res.Checks = append(res.Checks, tc)
	noTag := "no tag"
	if tc.Status == "inconclusive" {
		noTag = "tag not in this clone"
	}
	var section string
	if tagOK {
		var c ReleaseCheck
		res.Prev, section, c = checkChangelog(dir, res.Tag, ver, o.Prev)
		res.Checks = append(res.Checks, c)
	} else {
		res.Checks = append(res.Checks, ReleaseCheck{"changelog", "skipped", noTag})
	}
	bin, c := checkBinary(up, res.Tag, ver, o, tmp)
	res.Checks = append(res.Checks, c)
	if c.Status == "pass" {
		cmds, c := checkCommands(o, bin, section)
		res.Checks = append(res.Checks, c)
		if tagOK {
			res.Checks = append(res.Checks, checkReleaseDocs(dir, res.Tag, cmds))
		} else {
			res.Checks = append(res.Checks, ReleaseCheck{"docs", "skipped", noTag})
		}
	} else {
		res.Checks = append(res.Checks, ReleaseCheck{"commands", "skipped", "binary check did not pass"})
		detail := "binary check did not pass"
		if !tagOK {
			detail = noTag
		}
		res.Checks = append(res.Checks, ReleaseCheck{"docs", "skipped", detail})
	}
	res.Checks = append(res.Checks, checkCalibration(o))
	return res, recordReleaseAudit(dir, o, &res)
}

// checkReleaseTag checks tag resolves to a commit here. When it does not, it
// asks origin read-only (never fetching): a tag only on origin means a stale
// clone, and an origin that cannot be asked establishes nothing; both are
// inconclusive. Only a tag missing here and on origin fails.
func checkReleaseTag(dir, tag string) (ReleaseCheck, bool) {
	if _, err := gitWith(dir, nil, "rev-parse", "--verify", "--quiet", tag+"^{commit}"); err == nil {
		return ReleaseCheck{"tag", "pass", tag}, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "origin", "refs/tags/"+tag)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%v: %s", err, msg)
		}
		return ReleaseCheck{"tag", "inconclusive", tag + " is not in this clone and origin could not be checked, no violation established: " + err.Error()}, false
	}
	if strings.TrimSpace(string(out)) != "" {
		return ReleaseCheck{"tag", "inconclusive", tag + " exists on origin but not in this clone, no violation established; run git fetch origin tag " + tag}, false
	}
	return ReleaseCheck{"tag", "fail", tag + " does not exist here or on origin"}, false
}

// recordReleaseAudit sets res.Verdict and appends the release_audited event.
func recordReleaseAudit(dir string, o ReleaseAuditOptions, res *ReleaseAuditResult) error {
	res.Verdict = "pass"
	var checks []string
	for _, c := range res.Checks {
		switch {
		case c.Status == "fail":
			res.Verdict = "fail"
		case c.Status == "inconclusive" && res.Verdict == "pass":
			res.Verdict = "inconclusive"
		}
		checks = append(checks, c.Name+"="+c.Status)
	}
	return AppendEvent(dir, Event{
		TS: o.Now().UTC().Format(time.RFC3339Nano), Kind: "release_audited", Session: o.Session,
		Version: res.Tag, Verdict: res.Verdict, Checks: checks, Note: strings.Join(checks, ", "),
	})
}

// parseSemver parses X.Y.Z (no leading v) into its three numbers.
func parseSemver(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || strings.TrimLeft(p, "0123456789") != "" {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// semverLess reports whether a sorts before b.
func semverLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// prevReleaseTag returns the highest vA.B.C tag lower than ver, or "".
func prevReleaseTag(dir, ver string) (string, error) {
	cur, _ := parseSemver(ver)
	out, err := gitWith(dir, nil, "tag", "--list", "v*")
	if err != nil {
		return "", err
	}
	best, bestV := "", [3]int{}
	for _, t := range strings.Fields(out) {
		v, ok := parseSemver(strings.TrimPrefix(t, "v"))
		if !ok || !strings.HasPrefix(t, "v") || !semverLess(v, cur) {
			continue
		}
		if best == "" || semverLess(bestV, v) {
			best, bestV = t, v
		}
	}
	return best, nil
}

var (
	conventionalRE = regexp.MustCompile(`^(feat|fix|perf|revert)(\([^)]*\))?!?: `)
	compareRE      = regexp.MustCompile(`compare/([^)\s]+?)\.\.\.([^)\s]+)`)
	citeRE         = regexp.MustCompile(`\[([0-9a-f]{7,40})\]\([^)]*/commit/([0-9a-f]{7,40})\)`)
)

// changelogSection returns CHANGELOG.md's section for ver: its `## [ver]`
// heading line through the line before the next `## ` heading.
func changelogSection(text, ver string) (heading, section string, ok bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "## ["+ver+"]") {
			continue
		}
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(lines[j], "## ") {
			j++
		}
		return l, strings.Join(lines[i:j], "\n"), true
	}
	return "", "", false
}

// rangeCommit is one commit in prev..tag.
type rangeCommit struct{ sha, subject string }

// checkChangelog checks CHANGELOG.md at tag against the commits in prev..tag
// and returns the prev used and ver's section (for the commands check).
func checkChangelog(dir, tag, ver, prev string) (string, string, ReleaseCheck) {
	c := ReleaseCheck{Name: "changelog"}
	fail := func(d string) (string, string, ReleaseCheck) {
		c.Status, c.Detail = "fail", d
		return prev, "", c
	}
	if prev == "" {
		p, err := prevReleaseTag(dir, ver)
		if err != nil {
			return fail("list tags: " + err.Error())
		}
		prev = p
	}
	text, err := gitWith(dir, nil, "show", tag+":CHANGELOG.md")
	if err != nil {
		return fail("CHANGELOG.md at " + tag + ": " + err.Error())
	}
	heading, section, ok := changelogSection(text, ver)
	if !ok {
		return fail("CHANGELOG.md at " + tag + " has no ## [" + ver + "] section")
	}
	var problems []string
	if m := compareRE.FindStringSubmatch(heading); m != nil && prev != "" && m[1] != prev {
		problems = append(problems, fmt.Sprintf("heading compares from %s, want %s", m[1], prev))
	}
	rng := tag
	if prev != "" {
		rng = prev + ".." + tag
	}
	out, err := gitWith(dir, nil, "log", "--format=%H%x09%s", rng)
	if err != nil {
		return fail("git log " + rng + ": " + err.Error())
	}
	var commits []rangeCommit
	for _, l := range strings.Split(out, "\n") {
		if sha, subj, ok := strings.Cut(l, "\t"); ok {
			commits = append(commits, rangeCommit{sha, subj})
		}
	}
	inRange := func(cited string) bool {
		for _, rc := range commits {
			if strings.HasPrefix(rc.sha, cited) {
				return true
			}
		}
		return false
	}
	var cited []string
	for _, l := range strings.Split(section, "\n") {
		for _, m := range citeRE.FindAllStringSubmatch(l, -1) {
			cited = append(cited, m[1], m[2])
			if !inRange(m[2]) {
				problems = append(problems, "entry cites a commit outside "+rng+": "+strings.TrimSpace(l))
			}
		}
	}
	for _, rc := range commits {
		if !conventionalRE.MatchString(rc.subject) || strings.HasPrefix(rc.subject, "chore(main): release") {
			continue
		}
		found := false
		for _, s := range cited {
			if s == rc.sha || s == rc.sha[:7] {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, "no entry for "+rc.sha[:7]+" "+rc.subject)
		}
	}
	if len(problems) > 0 {
		c.Status, c.Detail = "fail", strings.Join(problems, "; ")
		return prev, section, c
	}
	c.Status, c.Detail = "pass", fmt.Sprintf("%d commit(s) in %s, every feat/fix/perf/revert cited", len(commits), rng)
	return prev, section, c
}

// checkBinary verifies the published artifact for up's platform against
// checksums.txt, extracts it into tmp and checks its version output. It
// returns the extracted binary's path when the check passed.
func checkBinary(up UpgradeOptions, tag, ver string, o ReleaseAuditOptions, tmp string) (string, ReleaseCheck) {
	c := ReleaseCheck{Name: "binary"}
	done := func(status, detail string) (string, ReleaseCheck) {
		c.Status, c.Detail = status, detail
		return "", c
	}
	asset := assetName(tag, up.GOOS, up.GOARCH)
	var body, sums []byte
	var err error
	if o.Artifact != "" {
		asset = filepath.Base(o.Artifact)
		if body, err = os.ReadFile(o.Artifact); err == nil {
			sums, err = os.ReadFile(o.Checksums)
		}
	} else {
		base := up.DownloadBase + "/" + up.Repo + "/releases/download/" + tag
		if body, err = downloadBytes(base + "/" + asset); err == nil {
			sums, err = downloadBytes(base + "/checksums.txt")
		}
	}
	if err != nil {
		return done("inconclusive", "could not fetch the artifact, no violation established: "+err.Error())
	}
	want, err := checksumFor(string(sums), asset)
	if err != nil {
		return done("fail", err.Error())
	}
	if got := sha256.Sum256(body); !bytes.Equal(got[:], want) {
		return done("fail", fmt.Sprintf("checksum mismatch for %s: checksums.txt %x, artifact %x", asset, want, got))
	}
	payload, err := unzipSingle(body)
	if err != nil {
		return done("fail", asset+": "+err.Error())
	}
	bin := filepath.Join(tmp, "flywheel")
	if up.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(bin, payload, 0o755); err != nil {
		return done("inconclusive", "write the extracted binary, no violation established: "+err.Error())
	}
	out, err := o.Run(bin, "version")
	if err != nil {
		return done("inconclusive", "run version, no violation established: "+err.Error())
	}
	if !strings.Contains(string(out), ver) {
		return done("fail", fmt.Sprintf("%s version printed %q, want %s", asset, strings.TrimSpace(string(out)), ver))
	}
	c.Status, c.Detail = "pass", asset+" verifies and reports "+ver
	return bin, c
}

var (
	spanRE     = regexp.MustCompile("`([^`\n]+)`")
	spanFlagRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9-])(--[A-Za-z][A-Za-z0-9-]*)`)
	helpFlagRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9-])--?([A-Za-z][A-Za-z0-9-]*)`)
	// commandWordRE is a span's second word when it names a command.
	commandWordRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// binaryCommands parses `flywheel help`: the first word of each line indented
// exactly two spaces under "subcommands:". Deeper lines (a command's nested
// sub-listing and its continuations) and blank lines are skipped; the section
// ends at the first non-empty line with no leading space.
func binaryCommands(help string) []string {
	var cmds []string
	in := false
	for _, l := range strings.Split(strings.ReplaceAll(help, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) == "subcommands:" {
			in = true
			continue
		}
		if !in || strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			in = false
			continue
		}
		if !strings.HasPrefix(l, "  ") || strings.HasPrefix(l, "   ") {
			continue
		}
		cmds = append(cmds, strings.Fields(l)[0])
	}
	return cmds
}

// checkCommands checks every `flywheel ...` span in section and the notes
// files against the audited binary's commands and each command's help flags.
// It returns the binary's command list for the docs check.
func checkCommands(o ReleaseAuditOptions, bin, section string) ([]string, ReleaseCheck) {
	c := ReleaseCheck{Name: "commands"}
	out, err := o.Run(bin, "help")
	cmds := binaryCommands(string(out))
	if err != nil || len(cmds) == 0 {
		c.Status, c.Detail = "inconclusive", fmt.Sprintf("no command list from `help`, no violation established (err %v)", err)
		return nil, c
	}
	known := map[string]bool{"help": true}
	for _, k := range cmds {
		known[k] = true
	}
	sources := []struct{ name, text string }{{"CHANGELOG.md", section}}
	var bad []string
	for _, n := range o.Notes {
		b, err := os.ReadFile(n)
		if err != nil {
			bad = append(bad, "read notes "+n+": "+err.Error())
			continue
		}
		sources = append(sources, struct{ name, text string }{n, string(b)})
	}
	// helps holds each command's help flags; nil when `help <cmd>` failed.
	helps := map[string]map[string]bool{}
	var unsure []string
	spans := 0
	for _, s := range sources {
		for _, m := range spanRE.FindAllStringSubmatch(s.text, -1) {
			span := m[1]
			f := strings.Fields(span)
			if !strings.HasPrefix(span, "flywheel ") || len(f) < 2 || !commandWordRE.MatchString(f[1]) {
				continue
			}
			spans++
			cmd := f[1]
			if !known[cmd] {
				bad = append(bad, fmt.Sprintf("%s: `%s` names unknown command %s", s.name, span, cmd))
				continue
			}
			if cmd == "help" {
				continue
			}
			flags, ok := helps[cmd]
			if !ok {
				h, err := o.Run(bin, "help", cmd)
				if err != nil || strings.TrimSpace(string(h)) == "" {
					unsure = append(unsure, fmt.Sprintf("`help %s` gave no flags (err %v)", cmd, err))
				} else {
					flags = map[string]bool{}
					for _, hf := range helpFlagRE.FindAllStringSubmatch(string(h), -1) {
						flags[hf[1]] = true
					}
				}
				helps[cmd] = flags
			}
			if flags == nil {
				continue
			}
			for _, sf := range spanFlagRE.FindAllStringSubmatch(span, -1) {
				if !flags[strings.TrimPrefix(sf[1], "--")] {
					bad = append(bad, fmt.Sprintf("%s: `%s` uses %s, not in `help %s`", s.name, span, sf[1], cmd))
				}
			}
		}
	}
	if len(bad) > 0 {
		c.Status, c.Detail = "fail", strings.Join(bad, "; ")
		return cmds, c
	}
	if len(unsure) > 0 {
		c.Status, c.Detail = "inconclusive", "flags not established, no violation established: "+strings.Join(unsure, "; ")
		return cmds, c
	}
	c.Status, c.Detail = "pass", fmt.Sprintf("%d flywheel span(s) name known commands and flags", spans)
	return cmds, c
}

// checkReleaseDocs checks every command appears as "flywheel <name>" in
// README.md and skills/flywheel-operator/SKILL.md at tag.
func checkReleaseDocs(dir, tag string, cmds []string) ReleaseCheck {
	c := ReleaseCheck{Name: "docs"}
	if len(cmds) == 0 {
		c.Status, c.Detail = "inconclusive", "no command list from the binary, no violation established"
		return c
	}
	var bad []string
	for _, f := range []string{"README.md", "skills/flywheel-operator/SKILL.md"} {
		text, err := gitWith(dir, nil, "show", tag+":"+f)
		if err != nil {
			bad = append(bad, f+" at "+tag+": "+err.Error())
			continue
		}
		var missing []string
		for _, k := range cmds {
			if !regexp.MustCompile(`flywheel ` + regexp.QuoteMeta(k) + `(?:$|[^A-Za-z0-9-])`).MatchString(text) {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			bad = append(bad, f+" is missing flywheel "+strings.Join(missing, ", flywheel "))
		}
	}
	if len(bad) > 0 {
		c.Status, c.Detail = "fail", strings.Join(bad, "; ")
		return c
	}
	c.Status, c.Detail = "pass", fmt.Sprintf("%d command(s) in README.md and the operator skill", len(cmds))
	return c
}

var calibrationTotalRE = regexp.MustCompile(`\*\*Total: [^*\n]*?recall ([0-9]+(?:\.[0-9]+)?)`)

// checkCalibration checks the calibration report's total recall is at least
// MinRecall; skipped when MinRecall < 0.
func checkCalibration(o ReleaseAuditOptions) ReleaseCheck {
	c := ReleaseCheck{Name: "calibration"}
	if o.MinRecall < 0 {
		c.Status, c.Detail = "skipped", "no --min-recall"
		return c
	}
	b, err := os.ReadFile(o.Calibration)
	if err != nil {
		c.Status, c.Detail = "fail", "read "+o.Calibration+": "+err.Error()
		return c
	}
	m := calibrationTotalRE.FindStringSubmatch(string(b))
	if m == nil {
		c.Status, c.Detail = "fail", o.Calibration+" has no **Total: ... recall R** line"
		return c
	}
	r, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		c.Status, c.Detail = "fail", o.Calibration+": recall "+m[1]+": "+err.Error()
		return c
	}
	if r < o.MinRecall {
		c.Status, c.Detail = "fail", fmt.Sprintf("%s recall %.2f is below the minimum %.2f", o.Calibration, r, o.MinRecall)
		return c
	}
	c.Status, c.Detail = "pass", fmt.Sprintf("recall %.2f >= %.2f", r, o.MinRecall)
	return c
}
