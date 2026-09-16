package flywheel

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DoctorProbe is one configured model's classification from a doctor run.
type DoctorProbe struct {
	Model string
	Class string
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
	worker := cfg.DefaultWorker()
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
	probes := make([]DoctorProbe, len(order))
	for i, m := range order {
		probes[i] = DoctorProbe{Model: m, Class: probeModel(dir, worker, adap, m)}
	}
	return probes, nil
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

// probeModel runs one unrecorded probe of model through adap: the sim
// adapter replays the fixture named by model, any other adapter is launched
// exactly as Run launches it. The result classifies from the first error
// observation's message, or ClassOK when the run ends with reason "stop" and
// no error was seen; a probe that fails to open or start classifies
// ClassError.
func probeModel(dir string, worker Worker, adap Adapter, model string) string {
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
