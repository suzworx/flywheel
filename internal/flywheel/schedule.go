package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// DefaultScheduleEvery is how often a scheduled task runs the controller
// once when `flywheel schedule install` is given no --every.
const DefaultScheduleEvery = 15 * time.Minute

// SchedulePlan is one OS scheduled task, platform-neutral: every Every it
// runs Exe with Args (`controller --once --dir Dir`) so the factory wakes
// even when no flywheel process is alive (issue #572).
type SchedulePlan struct {
	Name  string        // unique per repository, see ScheduleName
	Every time.Duration // whole minutes, at least one
	Exe   string        // absolute path of the flywheel binary
	Dir   string        // absolute repository root
	Args  []string      // controller --once --dir Dir
	Log   string        // where a run's output goes; empty is Dir/.flywheel/schedule.log
}

// Scheduler registers, removes and inspects a SchedulePlan with the host's
// scheduler: Task Scheduler on Windows, the user crontab on Linux, launchd
// on macOS.
type Scheduler interface {
	Install(p SchedulePlan) error
	Remove(name string) error
	Status(name string) (installed bool, detail string, err error)
}

// Runner runs a command and returns its combined output; tests inject one so
// they never touch the real OS scheduler.
type Runner func(name string, args ...string) ([]byte, error)

// ExecRunner is the Runner that really runs the command.
func ExecRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// runSched runs name args through r and, on failure, returns an error naming
// the command and its output.
func runSched(r Runner, name string, args ...string) ([]byte, error) {
	out, err := r(name, args...)
	if err != nil {
		return out, fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// ScheduleName is the task name for the repository at absDir:
// flywheel-<base name>-<first 8 hex of sha256(absDir)>, so two checkouts
// with the same base name never share a task. The base name keeps only
// [A-Za-z0-9._-] so every scheduler accepts it.
func ScheduleName(absDir string) string {
	sum := sha256.Sum256([]byte(absDir))
	return "flywheel-" + taskWord(filepath.Base(absDir)) + "-" + hex.EncodeToString(sum[:])[:8]
}

// taskWord keeps only [A-Za-z0-9._-] of s, every other rune becoming '_'.
func taskWord(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}

// NewSchedulePlan builds the plan for the repository at dir with the running
// binary as Exe; every 0 means DefaultScheduleEvery.
func NewSchedulePlan(dir string, every time.Duration) (SchedulePlan, error) {
	exe, err := os.Executable()
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("schedule: locate the flywheel binary: %v", err)
	}
	return planFor(dir, exe, every)
}

// planFor is NewSchedulePlan with the binary given. every is truncated to
// whole minutes and refused below one minute.
func planFor(dir, exe string, every time.Duration) (SchedulePlan, error) {
	if every == 0 {
		every = DefaultScheduleEvery
	}
	if every < time.Minute {
		return SchedulePlan{}, fmt.Errorf("schedule: --every must be at least 1m, got %v", every)
	}
	every = every.Truncate(time.Minute)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("schedule: %v", err)
	}
	absExe, err := filepath.Abs(exe)
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("schedule: %v", err)
	}
	return SchedulePlan{
		Name:  ScheduleName(absDir),
		Every: every,
		Exe:   absExe,
		Dir:   absDir,
		Args:  []string{"controller", "--once", "--dir", absDir},
	}, nil
}

// minutes is the plan's interval in whole minutes.
func (p SchedulePlan) minutes() int { return int(p.Every / time.Minute) }

// scheduleLog is where a cron run's output goes.
func (p SchedulePlan) scheduleLog() string {
	if p.Log != "" {
		return p.Log
	}
	return filepath.Join(p.Dir, ".flywheel", "schedule.log")
}

// DefaultFleetWatchEvery is how often the fleet watcher's task runs when
// `flywheel schedule install --fleet` is given no --every.
const DefaultFleetWatchEvery = 5 * time.Minute

// FleetScheduleName is the fleet watcher's task name for user:
// flywheel-fleet-<user>, one per user whatever the repositories; a domain
// prefix (DOMAIN\user) is dropped.
func FleetScheduleName(user string) string {
	if i := strings.LastIndexAny(user, `\/`); i >= 0 {
		user = user[i+1:]
	}
	return "flywheel-fleet-" + taskWord(user)
}

// NewFleetSchedulePlan builds the fleet watcher's plan for the current user,
// the running binary and the registry at FleetPath; every 0 means
// DefaultFleetWatchEvery; notify, when set, is passed as --notify.
func NewFleetSchedulePlan(every time.Duration, notify string) (SchedulePlan, error) {
	exe, err := os.Executable()
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("schedule: locate the flywheel binary: %v", err)
	}
	u, err := user.Current()
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("schedule: %v", err)
	}
	file, err := FleetPath()
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("schedule: %v", err)
	}
	return fleetPlanFor(file, exe, u.Username, every, notify)
}

// fleetPlanFor is NewFleetSchedulePlan with the registry, binary and user
// given: the task runs `fleet watch --once --registry <file>` (plus --notify)
// and logs to fleet-watch.log beside the registry.
func fleetPlanFor(fleetFile, exe, userName string, every time.Duration, notify string) (SchedulePlan, error) {
	if every == 0 {
		every = DefaultFleetWatchEvery
	}
	if every < time.Minute {
		return SchedulePlan{}, fmt.Errorf("schedule: --every must be at least 1m, got %v", every)
	}
	absFile, err := filepath.Abs(fleetFile)
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("schedule: %v", err)
	}
	absExe, err := filepath.Abs(exe)
	if err != nil {
		return SchedulePlan{}, fmt.Errorf("schedule: %v", err)
	}
	args := []string{"fleet", "watch", "--once", "--registry", absFile}
	if notify != "" {
		args = append(args, "--notify", notify)
	}
	dir := filepath.Dir(absFile)
	return SchedulePlan{Name: FleetScheduleName(userName), Every: every.Truncate(time.Minute), Exe: absExe, Dir: dir,
		Args: args, Log: filepath.Join(dir, "fleet-watch.log")}, nil
}

// schtasksTR is the /TR command line Task Scheduler runs: each word holding
// a space is double-quoted, the form schtasks /TR parses.
func schtasksTR(p SchedulePlan) string {
	words := append([]string{p.Exe}, p.Args...)
	for i, w := range words {
		if strings.ContainsAny(w, " \t") {
			words[i] = `"` + w + `"`
		}
	}
	return strings.Join(words, " ")
}

// schtasksArgs is the argv after "schtasks" for op ("install", "remove" or
// "status") on p; remove and status read only p.Name.
func schtasksArgs(op string, p SchedulePlan) []string {
	switch op {
	case "install":
		return []string{"/Create", "/F", "/SC", "MINUTE", "/MO", fmt.Sprint(p.minutes()), "/TN", p.Name, "/TR", schtasksTR(p)}
	case "remove":
		return []string{"/Delete", "/F", "/TN", p.Name}
	default:
		return []string{"/Query", "/TN", p.Name, "/FO", "LIST"}
	}
}

// cronMarker is the comment line above a plan's crontab entry.
func cronMarker(name string) string { return "# flywheel-schedule " + name }

// cronEntry is p's crontab line; intervals past an hour must be whole hours
// under a day, the steps cron can express.
func cronEntry(p SchedulePlan) (string, error) {
	m := p.minutes()
	var when string
	switch {
	case m < 60:
		when = fmt.Sprintf("*/%d * * * *", m)
	case m%60 == 0 && m/60 < 24:
		when = fmt.Sprintf("0 */%d * * *", m/60)
	default:
		return "", fmt.Errorf("schedule: cron cannot run every %v; use under 1h or whole hours under 24h", p.Every)
	}
	words := []string{cronWord(p.Exe)}
	for _, a := range p.Args {
		words = append(words, cronWord(a))
	}
	return when + " " + strings.Join(words, " ") + " >> " + cronWord(p.scheduleLog()) + " 2>&1", nil
}

// cronWord leaves a word of only [A-Za-z0-9/._-] as is and shell-quotes
// anything else, so a path with a space stays one word.
func cronWord(s string) string {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-", r)) {
			return shellQuote(s)
		}
	}
	return s
}

// cronRemove drops name's marker and the entry line after it from a crontab,
// leaving every other line as it was.
func cronRemove(content, name string) string {
	lines := strings.Split(content, "\n")
	var kept []string
	for i := 0; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == cronMarker(name) {
			i++ // skip the entry too
			continue
		}
		kept = append(kept, lines[i])
	}
	return strings.Join(kept, "\n")
}

// cronInstall replaces or adds p's marked entry in a crontab.
func cronInstall(content string, p SchedulePlan) (string, error) {
	entry, err := cronEntry(p)
	if err != nil {
		return "", err
	}
	out := strings.TrimRight(cronRemove(content, p.Name), "\n")
	if out != "" {
		out += "\n"
	}
	return out + cronMarker(p.Name) + "\n" + entry + "\n", nil
}

// cronFind reports whether name has an entry and returns that entry line.
func cronFind(content, name string) (bool, string) {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if strings.TrimRight(l, "\r") == cronMarker(name) {
			if i+1 < len(lines) {
				return true, strings.TrimRight(lines[i+1], "\r")
			}
			return true, ""
		}
	}
	return false, ""
}

// launchdLabel is the reverse-DNS launchd label for a task name.
func launchdLabel(name string) string { return "io.github.suzworx." + name }

// launchdPlist is p's LaunchAgent property list: StartInterval in seconds,
// ProgramArguments the binary and its args, output to the schedule log.
func launchdPlist(p SchedulePlan) string {
	var b strings.Builder
	str := func(s string) { b.WriteString("\t\t<string>" + xmlEscape(s) + "</string>\n") }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n" +
		"<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + xmlEscape(launchdLabel(p.Name)) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	str(p.Exe)
	for _, a := range p.Args {
		str(a)
	}
	b.WriteString("\t</array>\n")
	fmt.Fprintf(&b, "\t<key>StartInterval</key>\n\t<integer>%d</integer>\n", int(p.Every/time.Second))
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + xmlEscape(p.scheduleLog()) + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + xmlEscape(p.scheduleLog()) + "</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// xmlEscape escapes s for XML character data.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
