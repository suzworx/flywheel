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

// DoctorLedgerWarning is the local tracked-ledger check (issue #464): when
// git tracks dir's ledger (.flywheel/events.jsonl or anything under the
// shard directory .flywheel/events), one line naming at most three of the
// paths and the fix; "" otherwise, and "" when dir is not a repository or git
// fails. Read-only: ls-files never writes the index.
func DoctorLedgerWarning(dir string) string {
	root, _, err := LedgerRoot(dir)
	if err != nil {
		return ""
	}
	shardDir := ".flywheel/" + shardDirName
	paths, err := gitList(root, "\x00", "ls-files", "-z", "--", ".flywheel/events.jsonl", shardDir)
	if err != nil || len(paths) == 0 {
		return ""
	}
	named := strings.Join(paths, ", ")
	if len(paths) > 3 {
		named = fmt.Sprintf("%s and %d more", strings.Join(paths[:3], ", "), len(paths)-3)
	}
	// The untrack command names the shard directory once (rm -r takes it),
	// not every shard file.
	var rm []string
	seen := map[string]bool{}
	for _, p := range paths {
		if strings.HasPrefix(p, shardDir+"/") {
			p = shardDir
		}
		if !seen[p] {
			seen[p] = true
			rm = append(rm, p)
		}
	}
	return fmt.Sprintf("the ledger is tracked by git (%s): branch switches and merges rewrite it; "+
		"untrack it (git rm --cached -r %s) and add .flywheel/ to .gitignore, "+
		"or add \".flywheel/events.jsonl merge=union\" to .gitattributes (#436)", named, strings.Join(rm, " "))
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
