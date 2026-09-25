package flywheel

import (
	"bufio"
	_ "embed" // review_prompt.md
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ReviewFinding is one defect the review agent reports (issue #389): its
// severity (blocker, major, minor or nit), a category, the file and line, the
// claim, the failure scenario that shows it, and a fix hint.
type ReviewFinding struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Claim    string `json:"claim"`
	Scenario string `json:"scenario"`
	Fix      string `json:"fix"`
}

// findingsObject opens a bare {"findings": ...} object in the reviewer's answer.
var findingsObject = regexp.MustCompile(`\{\s*"findings"\s*:`)

// parseReviewFindings extracts the reviewer's findings from its final answer:
// the LAST fenced ```json block, or failing that the last bare top-level
// {"findings":[...]} object. Severities are normalised to lower case and an
// unknown one is refused; an empty findings list is a valid, clean answer.
func parseReviewFindings(text string) ([]ReviewFinding, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var raw string
	if i := strings.LastIndex(text, "```json"); i >= 0 {
		body := text[i+len("```json"):]
		end := strings.Index(body, "```")
		if end < 0 {
			return nil, errors.New("review answer: the last ```json block is not closed")
		}
		raw = body[:end]
	} else if locs := findingsObject.FindAllStringIndex(text, -1); len(locs) > 0 {
		raw = text[locs[len(locs)-1][0]:]
	} else {
		return nil, errors.New("review answer holds no ```json block and no {\"findings\": [...]} object")
	}
	var out struct {
		Findings *[]ReviewFinding `json:"findings"`
	}
	// A Decoder reads the one object and ignores any prose after it.
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&out); err != nil {
		return nil, fmt.Errorf("review answer: findings JSON: %w", err)
	}
	if out.Findings == nil {
		return nil, errors.New("review answer: the JSON object has no \"findings\" list")
	}
	findings := *out.Findings
	for i := range findings {
		f := &findings[i]
		f.Severity = strings.ToLower(strings.TrimSpace(f.Severity))
		if !findingSeverityOK(f.Severity) {
			return nil, fmt.Errorf("review answer: finding %d severity %q is not one of %s", i+1, f.Severity, strings.Join(FindingSeverities, ", "))
		}
		if strings.TrimSpace(f.File) == "" || strings.TrimSpace(f.Claim) == "" {
			return nil, fmt.Errorf("review answer: finding %d must name a file and a claim", i+1)
		}
		if f.Line < 0 {
			f.Line = 0
		}
	}
	return findings, nil
}

// maxReviewNits is how many nits one review answer may carry (issue #389).
const maxReviewNits = 3

// validateFindings checks the findings contract the framework enforces
// (issue #389) and returns one violation per bad finding: the file must be a
// forward-slash, repo-relative path that exists in workdir or is one of the
// changed paths (a deleted file); line must be 0 (whole file) or within the
// file's line count; claim and scenario must be non-empty; and the answer may
// carry at most maxReviewNits nits. A panel member's answer (issue #420)
// passes its dimension as category: every finding must carry exactly that
// category; "" allows any.
func validateFindings(workdir string, changed []string, findings []ReviewFinding, category string) []string {
	isChanged := map[string]bool{}
	for _, p := range changed {
		isChanged[filepath.ToSlash(p)] = true
	}
	var out []string
	nits := 0
	for i, f := range findings {
		n := i + 1
		if f.Severity == "nit" {
			nits++
		}
		var bad []string
		file := strings.TrimSpace(f.File)
		switch {
		case file == "":
			bad = append(bad, "names no file")
		case strings.Contains(file, `\`) || strings.HasPrefix(file, "/") || filepath.IsAbs(file) || filepath.VolumeName(file) != "":
			bad = append(bad, fmt.Sprintf("file %q is not a forward-slash repo-relative path", file))
		default:
			data, err := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(file)))
			switch {
			case err == nil:
				lines := strings.Count(string(data), "\n")
				if len(data) > 0 && data[len(data)-1] != '\n' {
					lines++
				}
				if f.Line < 0 || f.Line > lines {
					bad = append(bad, fmt.Sprintf("line %d is not in %s (%d lines; 0 means the whole file)", f.Line, file, lines))
				}
			case !isChanged[file]:
				bad = append(bad, fmt.Sprintf("file %s does not exist and is not a changed path", file))
			}
		}
		if strings.TrimSpace(f.Claim) == "" {
			bad = append(bad, "has an empty claim")
		}
		if strings.TrimSpace(f.Scenario) == "" {
			bad = append(bad, "has an empty scenario")
		}
		if category != "" && strings.TrimSpace(f.Category) != category {
			bad = append(bad, fmt.Sprintf("category %q is outside your dimension; report only %s findings, with category %q", f.Category, category, category))
		}
		if len(bad) > 0 {
			out = append(out, fmt.Sprintf("finding %d: %s", n, strings.Join(bad, "; ")))
		}
	}
	if nits > maxReviewNits {
		out = append(out, fmt.Sprintf("%d nits; at most %d", nits, maxReviewNits))
	}
	return out
}

// reviewPrompt is the review agent's instructions (issue #389).
//
//go:embed review_prompt.md
var reviewPrompt string

// maxReviewDiff caps the diff text the reviewer is given.
const maxReviewDiff = 200 * 1024

// ReviewAgentOptions configures one review-agent run (issue #389).
type ReviewAgentOptions struct {
	Worker  string // a worker in config; default: the staffing reviewer role, else the default worker
	Adapter string // run on this adapter instead (a panel member's adapter; issue #420); with Model
	Model   string // override the resolved worker's model (a panel member's model)
	// Dimension makes this run a panel member (issue #420): the prompt is
	// review_prompt.md plus review_personas/<Dimension>.md, every finding
	// must carry it as category, and the reviewed event records it.
	Dimension string
	Session   string    // the reviewer session label; required, never a worker session of the task
	Workdir   string    // the unit's worktree; default: the recorded workdir, else dir
	Round     int       // review round; <= 0 means the task's next round
	Progress  io.Writer // one line per step; optional
	Stdout    io.Writer // the reviewer's text as it arrives; optional
	Stderr    io.Writer // a copy of the reviewer's stderr; optional
}

// ReviewAgentResult is what one review-agent run recorded.
type ReviewAgentResult struct {
	Round      int
	Findings   []ReviewFinding
	Verdict    string // correct when any blocker or major, else pass
	Note       string // "<n> finding(s): <b> blocker, <m> major, <k> minor"
	Tree       string
	Model      string
	Prompt     string // the prompt file
	Transcript string // the reviewer's stream
}

// reviewWorker resolves who runs the review: the named worker; else the
// staffing reviewer role's adapter and model; else the default worker (with
// the reviewer role's model when only that is set). A role held by a person
// ("cli") or the offline sim adapter cannot run a review agent.
func reviewWorker(cfg Config, name string) (Worker, error) {
	var w Worker
	if name != "" {
		var ok bool
		if w, ok = cfg.Worker(name); !ok {
			return Worker{}, fmt.Errorf("no worker named %q in .flywheel/config.json", name)
		}
	} else {
		w = cfg.DefaultWorker()
		if cfg.Staffing != nil && cfg.Staffing.Reviewer != nil {
			r := cfg.Staffing.Reviewer
			switch {
			case r.Adapter == "cli":
				return Worker{}, errors.New("staffing.reviewer is \"cli\" (a person); pass --worker to run the review agent")
			case r.Adapter != "" && r.Adapter != w.Adapter:
				w = Worker{Name: "reviewer", Adapter: r.Adapter, Model: r.Model}
			case r.Model != "":
				w.Model = r.Model
			}
		}
	}
	if w.Adapter == "" || w.Adapter == "sim" {
		return Worker{}, fmt.Errorf("worker %q (adapter %q) cannot run a review agent; configure a claude, opencode or codex worker", w.Name, w.Adapter)
	}
	return w, nil
}

// resolveReviewer is who runs a review agent and on which adapter: the given
// adapter and model (a panel member's), else reviewWorker with model, when
// set, overriding the resolved worker's.
func resolveReviewer(cfg Config, worker, adapter, model string) (Worker, Adapter, error) {
	var w Worker
	if adapter != "" {
		w = Worker{Name: "reviewer", Adapter: adapter, Model: model}
		if w.Adapter == "sim" || !adapterKnown(w.Adapter, false) {
			return Worker{}, nil, fmt.Errorf("adapter %q cannot run a review agent; use claude, opencode or codex", w.Adapter)
		}
	} else {
		var err error
		if w, err = reviewWorker(cfg, worker); err != nil {
			return Worker{}, nil, err
		}
		if model != "" {
			w.Model = model
		}
	}
	adap, err := AdapterFor(w.Adapter)
	if err != nil {
		return Worker{}, nil, err
	}
	return w, adap, nil
}

// nextReviewRound is one more than the review-agent rounds already recorded
// for task: the reviewed events that name an adapter (a verdict passed in by
// hand names none). One panel round (issue #420) records one reviewed event
// per dimension: consecutive panel events count as one round until a
// dimension repeats or a general review intervenes.
func nextReviewRound(events []Event, task string) int {
	n := 0
	var seen map[string]bool // the dimensions of the current panel round; nil outside one
	for _, e := range events {
		if e.Task != task || e.Kind != "reviewed" || e.Adapter == "" {
			continue
		}
		dim := reviewDimension(e)
		switch {
		case dim == "":
			n++
			seen = nil
		case seen == nil || seen[dim]:
			n++
			seen = map[string]bool{dim: true}
		default:
			seen[dim] = true
		}
	}
	return n + 1
}

// reviewDiff is the unit's change for the reviewer: git diff from the unit's
// dispatch base (HEAD when none was recorded) over its changed paths, then
// the full text of each new untracked file, capped at maxReviewDiff.
func reviewDiff(workdir, base, task string) (string, error) {
	paths, err := unitChangedPaths(workdir, base, task)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "(no changed paths)\n", nil
	}
	from := base
	if from == "" {
		from = "HEAD"
	}
	diff, err := gitRead(workdir, append([]string{"diff", "--no-color", from, "--"}, paths...))
	if err != nil && from != "HEAD" {
		diff, err = gitRead(workdir, append([]string{"diff", "--no-color", "HEAD", "--"}, paths...))
	}
	if err != nil {
		return "", err
	}
	untracked, err := gitRead(workdir, []string{"ls-files", "-z", "--others", "--exclude-standard"})
	if err != nil {
		return "", err
	}
	changed := map[string]bool{}
	for _, p := range paths {
		changed[p] = true
	}
	var b strings.Builder
	b.WriteString(diff)
	for _, p := range strings.Split(untracked, "\x00") {
		if p = filepath.ToSlash(p); p == "" || !changed[p] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(p)))
		if err != nil {
			return "", fmt.Errorf("read new file %s: %w", p, err)
		}
		fmt.Fprintf(&b, "\n--- new file %s ---\n%s", p, data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	text := b.String()
	if len(text) > maxReviewDiff {
		text = strings.ToValidUTF8(text[:maxReviewDiff], "") + fmt.Sprintf("\n[diff truncated at %d KB of %d KB; read the changed files for the rest]\n", maxReviewDiff/1024, len(text)/1024)
	}
	return text, nil
}

// reviewReadings lists the task's latest gate reading per gate since its
// latest dispatch, one line each, for the reviewer's prompt.
func reviewReadings(events []Event, task string) string {
	start := 0
	for i, e := range events {
		if e.Task == task && e.Kind == "dispatched" {
			start = i
		}
	}
	latest := map[string]Event{}
	var order []string
	for _, e := range events[start:] {
		if e.Task != task || e.Kind != "validated" {
			continue
		}
		if _, seen := latest[e.Gate]; !seen {
			order = append(order, e.Gate)
		}
		latest[e.Gate] = e
	}
	if len(order) == 0 {
		return "(no gate readings recorded)\n"
	}
	var b strings.Builder
	for _, g := range order {
		e := latest[g]
		rc := "?"
		if e.RC != nil {
			rc = fmt.Sprint(*e.RC)
		}
		fmt.Fprintf(&b, "- gate %s rc=%s tree %s: %s\n", g, rc, e.Tree, e.Command)
	}
	return b.String()
}

// buildReviewPrompt joins the reviewer's instructions (base: review_prompt.md,
// plus a persona for a panel member), the unit's brief files (relative paths
// resolve against dir), the gate readings and the diff.
func buildReviewPrompt(base, dir string, briefs []string, readings, diff string) string {
	var b strings.Builder
	b.WriteString(base)
	for _, p := range briefs {
		path := p
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		fmt.Fprintf(&b, "\n# The brief (%s)\n\n", filepath.ToSlash(p))
		if data, err := os.ReadFile(path); err != nil {
			fmt.Fprintf(&b, "(unreadable: %v)\n", err)
		} else {
			b.Write(data)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n# Gate readings of the attempt\n\n")
	b.WriteString(readings)
	b.WriteString("\n# The diff (from the unit's dispatch base, then new files)\n\n")
	b.WriteString(diff)
	return b.String()
}

// reviewRunRequest is the reviewer's dispatch: the prompt file, and for the
// claude adapter a read-only tool policy — reading, searching, git read
// commands and go vet/test allowed; Edit, Write and NotebookEdit refused.
func reviewRunRequest(task string, round int, promptFile, model string) RunRequest {
	return RunRequest{
		Task: task, Attempt: fmt.Sprintf("rv%d", round), PromptFile: promptFile, Model: model,
		Title: fmt.Sprintf("%s-review-%d", task, round),
		AllowedTools: []string{"Read", "Grep", "Glob", "Bash(git diff:*)", "Bash(git log:*)",
			"Bash(git show:*)", "Bash(go vet:*)", "Bash(go test:*)"},
		DisallowedTools: []string{"Edit", "Write", "NotebookEdit"},
		NoWorkerRules:   true,
	}
}

// reviewEvents builds one review_finding event per finding (id
// <task>-r<round>-<n>) and the closing reviewed event: verdict correct when
// any finding is a blocker or major, else pass. It returns the events, the
// verdict and the note.
func reviewEvents(task, attempt string, round int, session, model, adapter, tree string, findings []ReviewFinding) ([]Event, string, string) {
	return dimensionReviewEvents(task, attempt, round, session, model, adapter, tree, "", findings)
}

// dimensionReviewEvents is reviewEvents for a panel member (issue #420): its
// dimension goes into the ids (<task>-r<round>-<dimension>-<n>, unique
// across one panel round) and the reviewed event's Category; persona stays
// reviewer. dimension "" is the general reviewer.
func dimensionReviewEvents(task, attempt string, round int, session, model, adapter, tree, dimension string, findings []ReviewFinding) ([]Event, string, string) {
	count := map[string]int{}
	var evs []Event
	prefix := fmt.Sprintf("%s-r%d-", task, round)
	if dimension != "" {
		prefix += dimension + "-"
	}
	for i, f := range findings {
		count[f.Severity]++
		evs = append(evs, Event{
			Task: task, Kind: "review_finding", Attempt: attempt, Session: session, Model: model, Tree: tree,
			Severity: f.Severity, Category: f.Category, Title: f.Claim, Observed: f.Scenario, Ask: f.Fix,
			Path: f.File, LineNo: f.Line, Finding: fmt.Sprintf("%s%d", prefix, i+1),
		})
	}
	verdict := "pass"
	if count["blocker"]+count["major"] > 0 {
		verdict = "correct"
	}
	note := fmt.Sprintf("%d finding(s): %d blocker, %d major, %d minor", len(findings), count["blocker"], count["major"], count["minor"])
	if count["nit"] > 0 {
		note += fmt.Sprintf(", %d nit", count["nit"])
	}
	evs = append(evs, Event{
		Task: task, Kind: "reviewed", Verdict: verdict, Persona: "reviewer", Session: session,
		Model: model, Adapter: adapter, Tree: tree, Note: note, Category: dimension,
	})
	return evs, verdict, note
}

// ReviewAgent runs an independent review agent over a unit's change (issue
// #389): it refuses (T4) a session that is a worker session of the task,
// builds a prompt from review_prompt.md, the unit's effective brief, its
// latest gate readings and its diff, writes it to
// .flywheel/reviews/<task>.<round>.prompt.md, and runs the worker's adapter
// in the unit's worktree under the git guard with a read-only tool policy,
// streaming to .flywheel/reviews/<task>.<round>.jsonl. The last assistant
// text must end with a {"findings":[...]} block: each finding becomes a
// review_finding event and a reviewed event closes the round, all in one
// append. An unparsable answer or one that breaks the findings contract
// (validateFindings) is refused and the reviewer runs once more, fresh, with
// the violations appended to the prompt file and streaming to
// <task>.<round>b.jsonl; a second refusal or a failed run records nothing and
// returns an error naming the transcripts.
func ReviewAgent(dir, task string, o ReviewAgentOptions) (ReviewAgentResult, error) {
	if o.Session == "" {
		return ReviewAgentResult{}, &RuleRefusal{Rule: "T4", Fix: "a reviewer --session is required"}
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return ReviewAgentResult{}, err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return ReviewAgentResult{}, err
	}
	if r := sessionClash(task, events, o.Session); r != "" {
		return ReviewAgentResult{}, &RuleRefusal{Rule: "T4", Fix: r}
	}
	base := reviewPrompt
	if o.Dimension != "" {
		if base, err = personaPrompt(o.Dimension); err != nil {
			return ReviewAgentResult{}, err
		}
	}
	worker, adap, err := resolveReviewer(cfg, o.Worker, o.Adapter, o.Model)
	if err != nil {
		return ReviewAgentResult{}, err
	}
	workdir := wd(o.Workdir, dir, events, task)
	_, briefs, err := AttemptBrief(dir, events, task)
	if err != nil {
		return ReviewAgentResult{}, err
	}
	res := ReviewAgentResult{Round: o.Round, Model: worker.Model}
	if res.Round <= 0 {
		res.Round = nextReviewRound(events, task)
	}
	diff, err := reviewDiff(workdir, dispatchBase(events, task, ""), task)
	if err != nil {
		return ReviewAgentResult{}, err
	}
	reviews, err := filepath.Abs(filepath.Join(dir, ".flywheel", "reviews"))
	if err != nil {
		return ReviewAgentResult{}, err
	}
	if err := os.MkdirAll(reviews, 0o755); err != nil {
		return ReviewAgentResult{}, err
	}
	name := fmt.Sprintf("%s.%d", task, res.Round)
	if o.Dimension != "" {
		name += "." + o.Dimension
	}
	stem := filepath.Join(reviews, name)
	res.Prompt, res.Transcript = stem+".prompt.md", stem+".jsonl"
	prompt := buildReviewPrompt(base, dir, briefs, reviewReadings(events, task), diff)
	if err := os.WriteFile(res.Prompt, []byte(prompt), 0o644); err != nil {
		return ReviewAgentResult{}, fmt.Errorf("write %s: %w", res.Prompt, err)
	}

	changed, err := unitChangedPaths(workdir, dispatchBase(events, task, ""), task)
	if err != nil {
		return ReviewAgentResult{}, err
	}
	progress(o.Progress, fmt.Sprintf("%s review round %d: %s %s", task, res.Round, worker.Adapter, worker.Model))
	answer, err := runReviewer(dir, workdir, task, adap, reviewRunRequest(task, res.Round, res.Prompt, worker.Model), stem, res.Transcript, o)
	if err != nil {
		return ReviewAgentResult{}, err
	}
	findings, problems := checkReviewAnswer(workdir, changed, answer, o.Dimension)
	if len(problems) > 0 {
		// The contract is checked, not trusted: one fresh run with the
		// refusal appended to the same prompt file, then nothing more.
		first := res.Transcript
		retry := prompt + "\n# Your previous answer was refused\n\n" + strings.Join(problems, "\n") +
			"\n\nAnswer again with ONE fenced json block.\n"
		if err := os.WriteFile(res.Prompt, []byte(retry), 0o644); err != nil {
			return ReviewAgentResult{}, fmt.Errorf("write %s: %w", res.Prompt, err)
		}
		res.Transcript = stem + "b.jsonl"
		progress(o.Progress, fmt.Sprintf("%s review round %d: answer refused (%d violation(s)); asking once more", task, res.Round, len(problems)))
		answer, err = runReviewer(dir, workdir, task, adap, reviewRunRequest(task, res.Round, res.Prompt, worker.Model), stem+"b", res.Transcript, o)
		if err != nil {
			return ReviewAgentResult{}, err
		}
		if findings, problems = checkReviewAnswer(workdir, changed, answer, o.Dimension); len(problems) > 0 {
			return ReviewAgentResult{}, fmt.Errorf("review answer refused twice; nothing recorded, transcripts %s and %s:\n%s", first, res.Transcript, strings.Join(problems, "\n"))
		}
	}
	res, err = recordReview(dir, task, events, workdir, worker, o.Session, o.Dimension, findings, res)
	if err == nil {
		refreshReviewThread(dir, task, o.Progress)
	}
	return res, err
}

// checkReviewAnswer parses the reviewer's answer and validates its findings;
// it returns the findings, or the reasons the answer is refused. category is
// a panel member's dimension, or "" for the general reviewer.
func checkReviewAnswer(workdir string, changed []string, answer, category string) ([]ReviewFinding, []string) {
	findings, err := parseReviewFindings(answer)
	if err != nil {
		return nil, []string{err.Error()}
	}
	if v := validateFindings(workdir, changed, findings, category); len(v) > 0 {
		return nil, v
	}
	return findings, nil
}

// runReviewer runs one reviewer dispatch in workdir under the git guard,
// streaming to transcriptPath and stderr to <stem>.err, and returns the last
// assistant text. A failed run returns an error naming the transcript.
func runReviewer(dir, workdir, task string, adap Adapter, req RunRequest, stem, transcriptPath string, o ReviewAgentOptions) (string, error) {
	if commandHook != nil {
		commandHook(req)
	}
	bin, args := adap.Command(req)
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir
	// The review prompt carries the diff, far past Windows' ~32K command-line
	// cap; an adapter that prompts on stdin keeps it off the line (issue #427).
	if sr := promptStdin(adap, req); sr != nil {
		cmd.Stdin = sr
	}
	guard, guardEnv, err := installGitGuard(workdir, task, req.Attempt)
	if err != nil {
		return "", fmt.Errorf("git guard not installed (%w); a reviewer never runs git unguarded", err)
	}
	defer os.RemoveAll(guard)
	cmd.Env = append(workerEnv(dir), guardEnv...)
	errFile, err := os.Create(stem + ".err")
	if err != nil {
		return "", err
	}
	defer errFile.Close()
	cmd.Stderr = errFile
	if o.Stderr != nil {
		cmd.Stderr = io.MultiWriter(errFile, o.Stderr)
	}
	transcript, err := os.Create(transcriptPath)
	if err != nil {
		return "", err
	}
	defer transcript.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", bin, err)
	}
	lastText := ""
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		_, _ = transcript.Write(append(append([]byte(nil), line...), '\n'))
		if obs, ok := adap.Parse(line); ok && obs.Kind == "text" && obs.Text != "" {
			lastText = obs.Text
			if o.Stdout != nil {
				fmt.Fprintln(o.Stdout, obs.Text)
			}
		}
	}
	scanErr := sc.Err()
	if werr := cmd.Wait(); werr != nil || scanErr != nil {
		return "", fmt.Errorf("review agent %s failed (%v, stream %v); transcript %s", bin, werr, scanErr, transcriptPath)
	}
	return lastText, nil
}

// recordReview records the reviewer's checked findings and verdict in one
// append.
func recordReview(dir, task string, events []Event, workdir string, worker Worker, session, dimension string, findings []ReviewFinding, res ReviewAgentResult) (ReviewAgentResult, error) {
	tree, err := treeHash(workdir)
	if err != nil {
		return ReviewAgentResult{}, err
	}
	attempt := ""
	for _, e := range events {
		if e.Task == task && e.Kind == "dispatched" && attemptOK(e.Attempt) {
			attempt = e.Attempt
		}
	}
	evs, verdict, note := dimensionReviewEvents(task, attempt, res.Round, session, worker.Model, worker.Adapter, tree, dimension, findings)
	if err := AppendEvents(dir, evs); err != nil {
		return ReviewAgentResult{}, err
	}
	_, _ = WriteState(dir)
	res.Findings, res.Verdict, res.Note, res.Tree = findings, verdict, note, tree
	return res, nil
}
