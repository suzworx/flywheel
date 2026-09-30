package flywheel

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DoctorProbe is one configured model's classification from a doctor run.
type DoctorProbe struct {
	Model string
	Class string
	// Detail says why the class is not ClassOK: a start failure, the error
	// observation's message, or the exit code and first stderr line (#637).
	Detail string
	At     time.Time // when the probe finished; RecordProbes stamps its event with it (#334 review)
}

// Doctor classes.
const (
	ClassOK      = "ok"
	ClassConsent = "consent required"
	ClassLimit   = "key limit"
	ClassCredits = "credits"
	ClassAuth    = "auth missing"
	ClassError   = "error"
)

// Doctor probes the selected worker's model, then its fallbacks, in config
// order — an exact duplicate model string is probed once, at its first
// position — through the worker's own adapter exactly as Run dispatches: the
// sim adapter replays the fixture named by the model string. No events are
// recorded.
func Doctor(dir string) ([]DoctorProbe, error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	return DoctorWorker(dir, cfg.DefaultWorker().Name)
}

// ErrUnknownWorker is wrapped by DoctorWorker when no worker has the name, so
// the command can tell a usage error from a broken config.
var ErrUnknownWorker = errors.New("unknown worker")

// DoctorWorker probes the named worker's model, then its fallbacks, then its
// routing candidates (issue #474), in config order — an exact duplicate model
// string is probed once, at its first
// position — through the worker's own adapter exactly as Run dispatches: the
// sim adapter replays the fixture named by the model string. No events are
// recorded. Returns an error if the worker is not found.
func DoctorWorker(dir, name string) ([]DoctorProbe, error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	worker, ok := cfg.Worker(name)
	if !ok {
		return nil, fmt.Errorf("no worker named %q in .flywheel/config.json: %w", name, ErrUnknownWorker)
	}
	adap, err := AdapterFor(worker.Adapter)
	if err != nil {
		return nil, err
	}
	var order []string
	seen := map[string]bool{}
	add := func(m string) {
		if !seen[m] {
			seen[m] = true
			order = append(order, m)
		}
	}
	add(worker.Model)
	for _, f := range worker.Fallbacks {
		add(f.Model)
	}
	if worker.Routing != nil {
		for _, m := range worker.Routing.Candidates {
			add(m)
		}
	}
	probes := make([]DoctorProbe, len(order))
	for i, m := range order {
		class, detail := probeModel(dir, worker, adap, m)
		probes[i] = DoctorProbe{Model: m, Class: class, Detail: detail, At: now()}
	}
	return probes, nil
}

// DoctorShellWarning is the local shell check (issue #471): when PATH's first
// bash on this Windows host is the WSL launcher, one line naming it and the
// shell gates and worktree.setup use instead; "" otherwise.
func DoctorShellWarning() string {
	return shellWarning(realShellHost())
}

// shellWarning is DoctorShellWarning for host h.
func shellWarning(h shellHost) string {
	wsl, chosen, ok := h.wslOnPath()
	if !ok {
		return ""
	}
	return fmt.Sprintf("bash on PATH is the WSL launcher (%s); gates and worktree.setup use %s", wsl, chosen)
}

// DoctorLedgerWarning is the committed-ledger check (issue #464; owner
// decision 2026-09-26: the ledger is committed, so init --ci's audit can
// verify every pull request). For dir's ledger (.flywheel/events.jsonl and the
// shard files under .flywheel/events) it returns one line naming at most three
// of the paths and the fix when a ledger file is untracked (or git-ignored),
// or when a tracked one lacks merge=union in .gitattributes (#436); "" when
// every ledger file is tracked with merge=union, when there is no ledger yet,
// when dir is not a repository, or when git fails. Read-only: ls-files,
// check-ignore and check-attr never write the index.
func DoctorLedgerWarning(dir string) string {
	root, _, err := LedgerRoot(dir)
	if err != nil {
		return ""
	}
	const legacy = ".flywheel/events.jsonl"
	shardDir := ".flywheel/" + shardDirName
	var present []string
	if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(legacy))); err == nil && fi.Mode().IsRegular() {
		present = append(present, legacy)
	}
	if ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(shardDir))); err == nil {
		for _, e := range ents {
			if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
				present = append(present, shardDir+"/"+e.Name())
			}
		}
	}
	if len(present) == 0 {
		return ""
	}
	tracked, err := gitList(root, "\x00", append([]string{"ls-files", "-z", "--"}, present...)...)
	if err != nil {
		return ""
	}
	isTracked := map[string]bool{}
	for _, p := range tracked {
		isTracked[p] = true
	}
	var untracked []string
	for _, p := range present {
		if !isTracked[p] {
			untracked = append(untracked, p)
		}
	}
	// git add names the shard directory once, not every shard file.
	addPaths := func(paths []string) string {
		var out []string
		seen := map[string]bool{}
		for _, p := range paths {
			if strings.HasPrefix(p, shardDir+"/") {
				p = shardDir
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		return strings.Join(out, " ")
	}
	if len(untracked) > 0 {
		out, absent, err := gitQuery(root, append([]string{"check-ignore", "-v", "--"}, untracked...)...)
		if err != nil {
			return ""
		}
		if !absent && out != "" {
			// -v prints "<source>:<line>:<pattern>\t<path>"; name the first rule.
			rule, _, _ := strings.Cut(strings.SplitN(out, "\n", 2)[0], "\t")
			return fmt.Sprintf("the ledger is git-ignored (%s, by %s): flywheel's audit verifies pull requests against the committed ledger; "+
				"remove that ignore rule, then commit it (git add %s)", namePaths(untracked), rule, addPaths(untracked))
		}
		return fmt.Sprintf("the ledger is not tracked by git (%s): flywheel's audit verifies pull requests against the committed ledger; "+
			"commit it (git add %s)", namePaths(untracked), addPaths(untracked))
	}
	out, _, err := gitQuery(root, append([]string{"check-attr", "-z", "merge", "--"}, tracked...)...)
	if err != nil {
		return ""
	}
	// -z prints "<path>\0merge\0<value>\0" per path.
	var noUnion []string
	fields := strings.Split(out, "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		if fields[i+2] != "union" {
			noUnion = append(noUnion, fields[i])
		}
	}
	if len(noUnion) == 0 {
		return ""
	}
	var lines []string
	for _, p := range noUnion {
		l := "\"" + legacy + " merge=union\""
		if strings.HasPrefix(p, shardDir+"/") {
			l = "\"" + shardDir + "/*.jsonl merge=union\""
		}
		if !strings.Contains(strings.Join(lines, " "), l) {
			lines = append(lines, l)
		}
	}
	return fmt.Sprintf("the ledger is tracked without merge=union (%s): parallel branches' appends conflict on merge; "+
		"add %s to .gitattributes (#436)", namePaths(noUnion), strings.Join(lines, " and "))
}

// namePaths joins at most three paths, counting the rest.
func namePaths(paths []string) string {
	if len(paths) > 3 {
		return fmt.Sprintf("%s and %d more", strings.Join(paths[:3], ", "), len(paths)-3)
	}
	return strings.Join(paths, ", ")
}

// DoctorIntegrationBranch is the integration-branch check (issue #456): line
// is "integration branch: <b> (integration.branch)" when the config sets it,
// "(detected)" when main or master was found, and "integration branch: none"
// otherwise; warning names a configured branch whose refs/heads/<b> does not
// resolve in dir, or, unset, a checkout on another branch
// (integrationUnsetWarning), "" otherwise.
func DoctorIntegrationBranch(dir string) (line, warning string) {
	b, configured := IntegrationBranch(dir)
	switch {
	case configured:
		line = fmt.Sprintf("integration branch: %s (integration.branch)", b)
		if !branchResolves(dir, b) {
			warning = fmt.Sprintf("integration.branch %q does not resolve (refs/heads/%s) in %s; fetch or create it, or fix .flywheel/config.json", b, b, dir)
		}
	case b != "":
		line = fmt.Sprintf("integration branch: %s (detected)", b)
	default:
		line = "integration branch: none (no integration.branch, main or master)"
	}
	if !configured {
		warning = integrationUnsetWarning(dir)
	}
	return line, warning
}

// integrationUnsetWarning warns, with integration.branch unset, when the
// checkout's current branch is neither main, master nor the remote default
// (refs/remotes/origin/HEAD when it resolves): PRs that target it would be
// based on the wrong branch (issue #694). A detached HEAD never warns.
func integrationUnsetWarning(dir string) string {
	out, err := gitRead(dir, []string{"symbolic-ref", "-q", "--short", "HEAD"})
	cur := strings.TrimSpace(out)
	if err != nil || cur == "" || cur == "main" || cur == "master" {
		return ""
	}
	if def, err := gitRead(dir, []string{"symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"}); err == nil && strings.TrimPrefix(strings.TrimSpace(def), "origin/") == cur {
		return ""
	}
	return fmt.Sprintf("integration.branch is unset while the checkout is on %s; if PRs target %s, set it: flywheel config set integration.branch %s", cur, cur, cur)
}

// DoctorAllOK reports whether every probe classified as ClassOK.
func DoctorAllOK(probes []DoctorProbe) bool {
	for _, p := range probes {
		if p.Class != ClassOK {
			return false
		}
	}
	return true
}

// RecordProbes appends one probed event per probe (Model, Reason = the
// class, Note "flywheel doctor", or "flywheel doctor: <detail>" when the
// probe has a Detail), in one AppendEvents batch.
func RecordProbes(dir string, probes []DoctorProbe) error {
	events := make([]Event, len(probes))
	for i, p := range probes {
		ts := ""
		if !p.At.IsZero() {
			ts = p.At.UTC().Format(time.RFC3339Nano)
		}
		note := "flywheel doctor"
		if p.Detail != "" {
			note += ": " + p.Detail
		}
		events[i] = Event{
			TS:     ts,
			Kind:   "probed",
			Model:  p.Model,
			Reason: p.Class,
			Note:   note,
		}
	}
	return AppendEvents(dir, events)
}

// probePrompt is the one-line task every probe dispatches.
const probePrompt = "Reply with the single word ok and stop. Do not use any tool.\n"

// probeStderrCap bounds the stderr a probe keeps for its detail.
const probeStderrCap = 4 << 10

// probeModel runs one unrecorded probe of model through adap: the sim
// adapter replays the fixture named by model; any other adapter is launched
// as Run launches it — the probe prompt in a temp PromptFile (and on stdin
// for an adapter that prompts there), the worker's permission mode, tools
// and MCP config — with max turns 1 and without Run's git guard or ledger.
// The class comes from the first error observation's message, or ClassOK
// when the run ends with reason "stop" and no error was seen; a probe that
// fails to open or start, or ends without a stop, classifies ClassError.
// detail says why whenever the class is not ClassOK (issue #637).
func probeModel(dir string, worker Worker, adap Adapter, model string) (class, detail string) {
	if class, ok := localEndpointClass(dir, model, &http.Client{Timeout: 3 * time.Second}); !ok {
		return class, ""
	}

	req := RunRequest{
		Task: "doctor", Attempt: "probe", Model: model, Variant: worker.Variant, Title: "doctor-probe",
		PermissionMode: worker.PermissionMode, AllowedTools: worker.allowedTools(),
		DisallowedTools: worker.disallowedTools(), MCPConfig: worker.mcpConfig(), MaxTurns: 1,
	}
	var stream io.Reader
	var cmd *exec.Cmd
	stderr := &capWriter{max: probeStderrCap}
	if worker.Adapter == "sim" {
		src := model
		if !filepath.IsAbs(src) {
			src = filepath.Join(dir, src)
		}
		f, err := os.OpenFile(src, os.O_RDONLY, 0)
		if err != nil {
			return ClassError, clipDetail("start: " + err.Error())
		}
		defer f.Close()
		stream = f
	} else {
		pf, err := os.CreateTemp("", "flywheel-doctor-probe-*.md")
		if err != nil {
			return ClassError, clipDetail("start: " + err.Error())
		}
		defer os.Remove(pf.Name())
		_, werr := pf.WriteString(probePrompt)
		if cerr := pf.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return ClassError, clipDetail("start: " + werr.Error())
		}
		req.PromptFile = pf.Name()
		bin, args := adap.Command(req)
		cmd = exec.Command(bin, args...)
		cmd.Dir = dir
		if sr := promptStdin(adap, req); sr != nil {
			cmd.Stdin = sr
		}
		cmd.Env = workerEnv(dir)
		cmd.Stderr = stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return ClassError, clipDetail("start: " + err.Error())
		}
		stream = out
		if err := cmd.Start(); err != nil {
			return ClassError, clipDetail("start: " + err.Error())
		}
	}

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	reason, errMsg := "", ""
	haveErr := false
	for sc.Scan() {
		obs, ok := adap.Parse(sc.Bytes())
		if !ok {
			continue
		}
		switch obs.Kind {
		case "step":
			if obs.Reason != "" {
				reason = obs.Reason
			}
		case "error":
			if !haveErr {
				haveErr = true
				errMsg = obs.Error
			}
		}
	}
	exitCode := 0
	if cmd != nil {
		var ee *exec.ExitError
		if err := cmd.Wait(); errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
	}
	if haveErr {
		class := classify(errMsg)
		if class == ClassOK {
			return class, ""
		}
		return class, clipDetail(errMsg)
	}
	if reason == "stop" {
		return ClassOK, ""
	}
	switch {
	case exitCode != 0:
		d := fmt.Sprintf("exit %d", exitCode)
		for _, l := range strings.Split(stderr.buf.String(), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				d += ": " + l
				break
			}
		}
		return ClassError, clipDetail(d)
	case reason != "":
		return ClassError, clipDetail("no stop (last reason: " + reason + ")")
	default:
		return ClassError, "no stop (no step)"
	}
}

// clipDetail is s's first line, cut to 200 characters.
func clipDetail(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// capWriter keeps the first max bytes written to it and discards the rest,
// never failing the write.
type capWriter struct {
	buf strings.Builder
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

// classify maps an error observation's message to a doctor class, matched
// case-insensitively as substrings; the first match wins.
func classify(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "requires explicit opt in"):
		return ClassConsent
	case strings.Contains(m, "rate limit"), strings.Contains(m, "429"), strings.Contains(m, "per-day"), strings.Contains(m, "key limit"):
		return ClassLimit
	case strings.Contains(m, "credits"), strings.Contains(m, "402"), strings.Contains(m, "billing"):
		return ClassCredits
	case strings.Contains(m, "api key"), strings.Contains(m, "401"), strings.Contains(m, "unauthorized"), strings.Contains(m, "authentication"):
		return ClassAuth
	default:
		return ClassError
	}
}
