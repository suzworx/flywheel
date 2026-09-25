package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseCommits are the shas of the release test repo's commits.
type releaseCommits struct{ init, feat, fix string }

// defaultChangelog is a release-please CHANGELOG.md citing feat and fix.
func defaultChangelog(c releaseCommits) string {
	return fmt.Sprintf(`# Changelog

## [0.2.0](https://github.com/suzworx/flywheel/compare/v0.1.0...v0.2.0) (2026-09-25)


### Features

* add audit ([%.7s](https://github.com/suzworx/flywheel/commit/%s)), run `+"`flywheel audit --release 0.2.0 --session aud`"+`


### Bug Fixes

* repair audit ([%.7s](https://github.com/suzworx/flywheel/commit/%s))

## [0.1.0](https://github.com/suzworx/flywheel/compare/v0.0.1...v0.1.0) (2026-09-01)
`, c.feat, c.feat, c.fix, c.fix)
}

const releaseDocs = "Run flywheel audit, then flywheel version.\n"

// releaseRepo builds a git repo tagged v0.1.0 (init) and v0.2.0 (a feat, a
// fix, a docs commit and the release commit carrying changelog and readme).
func releaseRepo(t *testing.T, changelog func(releaseCommits) string, readme string) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	var c releaseCommits
	commit := func(file, body, msg string) string {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, []string{"add", "-A"})
		git(t, dir, []string{"commit", "-q", "-m", msg})
		return git(t, dir, []string{"rev-parse", "HEAD"})
	}
	c.init = commit("a.txt", "a", "chore: init")
	git(t, dir, []string{"tag", "v0.1.0"})
	c.feat = commit("b.txt", "b", "feat: add audit")
	c.fix = commit("c.txt", "c", "fix(audit): repair audit")
	commit("d.txt", "d", "docs: words")
	commit("skills/flywheel-operator/SKILL.md", releaseDocs, "docs: skill")
	commit("README.md", readme, "docs: readme")
	commit("CHANGELOG.md", changelog(c), "chore(main): release 0.2.0")
	git(t, dir, []string{"tag", "v0.2.0"})
	return dir
}

// cannedRun answers version, help and help <cmd> like a flywheel binary
// printing version ver.
func cannedRun(ver string) func(string, ...string) ([]byte, error) {
	return func(bin string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "version":
			return []byte("flywheel " + ver + "\n"), nil
		case "help":
			return []byte("usage: flywheel <subcommand>\n\nsubcommands:\n  audit      re-measure a unit\n" +
				"    get <key>          print a value\n    set <key> <value>  set a value (model,\n                       variant)\n" +
				"  version    print the version\n\nRun 'flywheel help <command>' for its flags.\n"), nil
		case "help audit":
			return []byte("usage: flywheel audit --release VERSION --session S\n\nflags:\n  -release string\n  -session string\n"), nil
		case "help version":
			return []byte("usage: flywheel version\n"), nil
		}
		return nil, fmt.Errorf("unexpected args %v", args)
	}
}

// releaseOpts are options auditing v0.2.0 against srv.
func releaseOpts(srv *upgradeServer) ReleaseAuditOptions {
	return ReleaseAuditOptions{Version: "0.2.0", Session: "aud", MinRecall: -1, DownloadBase: srv.URL,
		Repo: srv.opts.Repo, GOOS: "linux", GOARCH: "amd64", Run: cannedRun("0.2.0")}
}

// releaseCheck returns the named check of res.
func releaseCheck(t *testing.T, res ReleaseAuditResult, name string) ReleaseCheck {
	t.Helper()
	for _, c := range res.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s check in %+v", name, res.Checks)
	return ReleaseCheck{}
}

// auditRelease runs AuditRelease on a default repo with changelog cl.
func auditRelease(t *testing.T, cl func(releaseCommits) string, mut func(*ReleaseAuditOptions)) (string, ReleaseAuditResult) {
	t.Helper()
	dir := releaseRepo(t, cl, releaseDocs)
	srv := newUpgradeServer(t, "v0.2.0", "linux", "amd64", []byte("bin"))
	t.Cleanup(srv.Close)
	o := releaseOpts(srv)
	if mut != nil {
		mut(&o)
	}
	res, err := AuditRelease(dir, o)
	if err != nil {
		t.Fatalf("AuditRelease() error = %v", err)
	}
	return dir, res
}

func TestReleaseAuditAllPass(t *testing.T) {
	t.Parallel()
	dir, res := auditRelease(t, defaultChangelog, nil)
	if res.Verdict != "pass" || res.Tag != "v0.2.0" || res.Prev != "v0.1.0" {
		t.Fatalf("result = %+v", res)
	}
	want := []string{"tag=pass", "changelog=pass", "binary=pass", "commands=pass", "docs=pass", "calibration=skipped"}
	for i, c := range res.Checks {
		if got := c.Name + "=" + c.Status; i >= len(want) || got != want[i] {
			t.Errorf("check %d = %s (%s), want %v", i, got, c.Detail, want)
		}
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != "release_audited" || evs[0].Verdict != "pass" || evs[0].Version != "v0.2.0" ||
		evs[0].Session != "aud" || len(evs[0].Checks) != 6 || !strings.Contains(evs[0].Note, "binary=pass") {
		t.Errorf("events = %+v", evs)
	}
}

func TestReleaseAuditRefusesWithoutSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := AuditRelease(dir, ReleaseAuditOptions{Version: "0.2.0", MinRecall: -1}); !IsRuleRefusal(err) {
		t.Errorf("AuditRelease() without session = %v, want a rule refusal", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "events.jsonl")); err == nil {
		t.Error("a refused audit appended an event")
	}
}

func TestReleaseAuditChangelogMissingFeat(t *testing.T) {
	t.Parallel()
	_, res := auditRelease(t, func(c releaseCommits) string {
		var keep []string
		for _, l := range strings.Split(defaultChangelog(c), "\n") {
			if !strings.Contains(l, c.feat) {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}, nil)
	if c := releaseCheck(t, res, "changelog"); c.Status != "fail" || !strings.Contains(c.Detail, "feat: add audit") {
		t.Errorf("changelog = %+v, want fail naming feat: add audit", c)
	}
	if res.Verdict != "fail" {
		t.Errorf("verdict = %s, want fail", res.Verdict)
	}
}

func TestReleaseAuditChangelogCitesOutsideRange(t *testing.T) {
	t.Parallel()
	_, res := auditRelease(t, func(c releaseCommits) string {
		cl := defaultChangelog(c)
		return strings.Replace(cl, "### Bug Fixes\n", fmt.Sprintf("### Bug Fixes\n\n* old ([%.7s](https://github.com/suzworx/flywheel/commit/%s))\n", c.init, c.init), 1)
	}, nil)
	if c := releaseCheck(t, res, "changelog"); c.Status != "fail" || !strings.Contains(c.Detail, "outside v0.1.0..v0.2.0") || !strings.Contains(c.Detail, "* old") {
		t.Errorf("changelog = %+v, want fail naming the out-of-range entry", c)
	}
}

func TestReleaseAuditChangelogWrongCompareBase(t *testing.T) {
	t.Parallel()
	_, res := auditRelease(t, func(c releaseCommits) string {
		return strings.Replace(defaultChangelog(c), "compare/v0.1.0...v0.2.0", "compare/v0.0.9...v0.2.0", 1)
	}, nil)
	if c := releaseCheck(t, res, "changelog"); c.Status != "fail" || !strings.Contains(c.Detail, "compares from v0.0.9, want v0.1.0") {
		t.Errorf("changelog = %+v, want fail naming the compare base", c)
	}
}

func TestReleaseAuditMissingTag(t *testing.T) {
	t.Parallel()
	_, res := auditRelease(t, defaultChangelog, func(o *ReleaseAuditOptions) { o.Version = "v0.3.0"; o.Run = cannedRun("0.3.0") })
	if c := releaseCheck(t, res, "tag"); c.Status != "fail" {
		t.Errorf("tag = %+v, want fail", c)
	}
	for _, n := range []string{"changelog", "docs"} {
		if c := releaseCheck(t, res, n); c.Status != "skipped" || c.Detail != "no tag" {
			t.Errorf("%s = %+v, want skipped: no tag", n, c)
		}
	}
	if res.Verdict != "fail" {
		t.Errorf("verdict = %s, want fail", res.Verdict)
	}
}

func TestReleaseAuditBinary(t *testing.T) {
	t.Parallel()
	dir := releaseRepo(t, defaultChangelog, releaseDocs)
	asset := assetName("v0.2.0", "linux", "amd64")
	bad := newUpgradeServer(t, "v0.2.0", "linux", "amd64", []byte("bin"), strings.Repeat("00", 32)+"  "+asset+"\n")
	defer bad.Close()
	res, err := AuditRelease(dir, releaseOpts(bad))
	if err != nil {
		t.Fatal(err)
	}
	if c := releaseCheck(t, res, "binary"); c.Status != "fail" || !strings.Contains(c.Detail, "checksum mismatch") {
		t.Errorf("checksum mismatch: binary = %+v, want fail", c)
	}
	for _, n := range []string{"commands", "docs"} {
		if c := releaseCheck(t, res, n); c.Status != "skipped" {
			t.Errorf("checksum mismatch: %s = %+v, want skipped", n, c)
		}
	}

	srv := newUpgradeServer(t, "v0.2.0", "linux", "amd64", []byte("bin"))
	defer srv.Close()
	o := releaseOpts(srv)
	o.Run = cannedRun("0.1.9")
	if res, _ = AuditRelease(dir, o); releaseCheck(t, res, "binary").Status != "fail" || res.Verdict != "fail" {
		t.Errorf("version mismatch: %+v, want binary fail", res.Checks)
	}

	o = releaseOpts(srv)
	o.Repo = "nobody/nothing"
	res, _ = AuditRelease(dir, o)
	if c := releaseCheck(t, res, "binary"); c.Status != "inconclusive" || !strings.Contains(c.Detail, "no violation established") {
		t.Errorf("download error: binary = %+v, want inconclusive", c)
	}
	if res.Verdict != "inconclusive" {
		t.Errorf("download error: verdict = %s, want inconclusive", res.Verdict)
	}
}

func TestReleaseAuditNotesSpans(t *testing.T) {
	t.Parallel()
	notes := filepath.Join(t.TempDir(), "notes.md")
	body := "Use `flywheel audit --release 0.2.0`, `flywheel frob --x` and `flywheel audit --bogus`.\n"
	if err := os.WriteFile(notes, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, res := auditRelease(t, defaultChangelog, func(o *ReleaseAuditOptions) { o.Notes = []string{notes} })
	c := releaseCheck(t, res, "commands")
	if c.Status != "fail" || !strings.Contains(c.Detail, "unknown command frob") || !strings.Contains(c.Detail, "--bogus") ||
		strings.Contains(c.Detail, "--release") {
		t.Errorf("commands = %+v, want fail naming frob and --bogus only", c)
	}
}

func TestReleaseAuditDocsMissingCommand(t *testing.T) {
	t.Parallel()
	dir := releaseRepo(t, defaultChangelog, "Only flywheel audit here.\n")
	srv := newUpgradeServer(t, "v0.2.0", "linux", "amd64", []byte("bin"))
	defer srv.Close()
	res, err := AuditRelease(dir, releaseOpts(srv))
	if err != nil {
		t.Fatal(err)
	}
	if c := releaseCheck(t, res, "docs"); c.Status != "fail" || !strings.Contains(c.Detail, "README.md is missing flywheel version") ||
		strings.Contains(c.Detail, "SKILL.md") {
		t.Errorf("docs = %+v, want fail naming README.md's missing version", c)
	}
}

func TestReleaseAuditCalibration(t *testing.T) {
	t.Parallel()
	report := filepath.Join(t.TempDir(), "cal.md")
	if err := os.WriteFile(report, []byte("# Calibration\n\n**Total: 3/4 cases found, recall 0.75, 1 extra finding(s).**\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(t.TempDir(), "junk.md")
	if err := os.WriteFile(junk, []byte("nothing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file string
		min  float64
		want string
	}{{report, 0.8, "fail"}, {report, 0.75, "pass"}, {report, 0.5, "pass"}, {junk, 0.5, "fail"}} {
		_, res := auditRelease(t, defaultChangelog, func(o *ReleaseAuditOptions) { o.Calibration, o.MinRecall = tc.file, tc.min })
		if c := releaseCheck(t, res, "calibration"); c.Status != tc.want {
			t.Errorf("%s min %.2f: calibration = %+v, want %s", filepath.Base(tc.file), tc.min, c, tc.want)
		}
	}
	if _, err := AuditRelease(t.TempDir(), ReleaseAuditOptions{Version: "0.2.0", Session: "aud", MinRecall: 0.5}); err == nil {
		t.Error("--min-recall without --calibration was accepted")
	}
}

// realHelpShape is a verbatim excerpt of the real `flywheel help` layout.
const realHelpShape = `usage: flywheel <subcommand>

subcommands:
  attest     record gate readings a named external run measured on a commit
  checkpoint list, diff, restore or drop the snapshots of interrupted attempts
  config     read and validate the flywheel config
    get <key>          print a config value (bare keys use the default worker)
    set <key> <value>  set a config value (model, variant, adapter, max_parallel,
                       feedback.upstream, feedback.submit, limits.per_host,
                       staffing.<lead|inspector|auditor>.<adapter|model|session>)
    show               print the effective config as JSON
    validate           check the config and list every problem
  feedback   curate signals into learnings
    add       record a learning
    dismiss   dismiss a learning by id
    submit    send the report upstream (consent-first)
  recover    report where every unit is, whether the world matches the ledger, and the next safe action
  version    print the flywheel version
  wait       block until the named tasks finish, printing each finish as it lands

Run 'flywheel help <command>' for its flags.
  after      not a command
`

func TestReleaseAuditHelpNested(t *testing.T) {
	t.Parallel()
	for _, nl := range []string{"\n", "\r\n"} {
		got := binaryCommands(strings.ReplaceAll(realHelpShape, "\n", nl))
		want := []string{"attest", "checkpoint", "config", "feedback", "recover", "version", "wait"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("binaryCommands() = %v, want %v", got, want)
		}
	}
}

func TestReleaseAuditSkipsNonCommandSpans(t *testing.T) {
	t.Parallel()
	notes := filepath.Join(t.TempDir(), "notes.md")
	body := "Get `flywheel v0.2.0`, see `flywheel ` and `flywheel <subcommand>`, or `flywheel --help`.\n"
	if err := os.WriteFile(notes, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, res := auditRelease(t, defaultChangelog, func(o *ReleaseAuditOptions) { o.Notes = []string{notes} })
	if c := releaseCheck(t, res, "commands"); c.Status != "pass" || !strings.HasPrefix(c.Detail, "1 flywheel span(s)") {
		t.Errorf("commands = %+v, want pass counting only the changelog span", c)
	}
	if res.Verdict != "pass" {
		t.Errorf("verdict = %s, want pass", res.Verdict)
	}
}

func TestReleaseAuditHelpCommandFails(t *testing.T) {
	t.Parallel()
	_, res := auditRelease(t, defaultChangelog, func(o *ReleaseAuditOptions) {
		canned := o.Run
		o.Run = func(bin string, args ...string) ([]byte, error) {
			if strings.Join(args, " ") == "help audit" {
				return nil, fmt.Errorf("boom")
			}
			return canned(bin, args...)
		}
	})
	if c := releaseCheck(t, res, "commands"); c.Status != "inconclusive" || !strings.Contains(c.Detail, "help audit") ||
		strings.Contains(c.Detail, "--release") {
		t.Errorf("commands = %+v, want inconclusive naming help audit", c)
	}
	if res.Verdict != "inconclusive" {
		t.Errorf("verdict = %s, want inconclusive", res.Verdict)
	}
}
