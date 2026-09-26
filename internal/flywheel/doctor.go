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
	At    time.Time // when the probe finished; RecordProbes stamps its event with it (#334 review)
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
		class := probeModel(dir, worker, adap, m)
		probes[i] = DoctorProbe{Model: m, Class: class, At: now()}
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
// resolve in dir, "" otherwise.
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
	return line, warning
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
// class, Note "flywheel doctor"), in one AppendEvents batch.
func RecordProbes(dir string, probes []DoctorProbe) error {
	events := make([]Event, len(probes))
	for i, p := range probes {
		ts := ""
		if !p.At.IsZero() {
			ts = p.At.UTC().Format(time.RFC3339Nano)
		}
		events[i] = Event{
			TS:     ts,
			Kind:   "probed",
			Model:  p.Model,
			Reason: p.Class,
			Note:   "flywheel doctor",
		}
	}
	return AppendEvents(dir, events)
}

// probeModel runs one unrecorded probe of model through adap: the sim
// adapter replays the fixture named by model, any other adapter is launched
// exactly as Run launches it. The result classifies from the first error
// observation's message, or ClassOK when the run ends with reason "stop" and
// no error was seen; a probe that fails to open or start classifies
// ClassError.
func probeModel(dir string, worker Worker, adap Adapter, model string) string {
	if class, ok := localEndpointClass(dir, model, &http.Client{Timeout: 3 * time.Second}); !ok {
		return class
	}

	req := RunRequest{Task: "doctor", Attempt: "probe", Model: model, Variant: worker.Variant, Title: "doctor-probe"}
	var stream io.Reader
	var cmd *exec.Cmd
	if worker.Adapter == "sim" {
		src := model
		if !filepath.IsAbs(src) {
			src = filepath.Join(dir, src)
		}
		f, err := os.OpenFile(src, os.O_RDONLY, 0)
		if err != nil {
			return ClassError
		}
		defer f.Close()
		stream = f
	} else {
		bin, args := adap.Command(req)
		cmd = exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = workerEnv(dir)
		out, err := cmd.StdoutPipe()
		if err != nil {
			return ClassError
		}
		stream = out
		if err := cmd.Start(); err != nil {
			return ClassError
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
	if cmd != nil {
		_ = cmd.Wait()
	}
	if haveErr {
		return classify(errMsg)
	}
	if reason == "stop" {
		return ClassOK
	}
	return ClassError
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
