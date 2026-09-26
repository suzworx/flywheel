package flywheel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// linkNeedsState links each needs-state "(link)" path from root into the
// task's worktree wt (issue #430): a directory junction on Windows (no
// symlink privilege needed), a symlink elsewhere. A path already present in
// wt (a link from an earlier dispatch, or a real directory) is left alone,
// so the call is idempotent; a path missing under root is an error naming it.
func linkNeedsState(root, wt string, paths []string) error {
	for _, p := range paths {
		rel := strings.TrimSuffix(p, "/")
		if rel == "" {
			continue
		}
		target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("needs-state %s: %w", p, err)
		}
		link := filepath.Join(wt, filepath.FromSlash(rel))
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		if _, err := os.Stat(target); err != nil {
			return fmt.Errorf("needs-state %s (link): not found under %s", p, root)
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return fmt.Errorf("needs-state %s (link): %w", p, err)
		}
		if runtime.GOOS == "windows" {
			out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
			if err != nil {
				return fmt.Errorf("needs-state %s (link): mklink /J %s %s: %v: %s", p, link, target, err, strings.TrimSpace(string(out)))
			}
			continue
		}
		if err := os.Symlink(target, link); err != nil {
			return fmt.Errorf("needs-state %s (link): %w", p, err)
		}
	}
	return nil
}

// prepareWorktree is run --worktree's setup step (issue #430): it links the
// effective brief's needs-state "(link)" paths into wt, runs worktree.setup
// when configured, and appends one worktree_setup event. A link error or a
// setup that does not exit 0 is a RuleRefusal with rule "setup"; with no
// links and no setup command it records nothing.
func prepareWorktree(dir, wt, task, attempt string, cfg Config, links []string) error {
	command := cfg.SetupCommand()
	if len(links) == 0 && command == "" {
		return nil
	}
	ev := Event{Task: task, Kind: "worktree_setup", Attempt: attempt, Linked: links, Command: command}
	if lerr := linkNeedsState(dir, wt, links); lerr != nil {
		ev.Linked, ev.Note = nil, clipSetupNote(lerr.Error())
		if err := AppendEvent(dir, ev); err != nil {
			return err
		}
		return &RuleRefusal{Rule: "setup", Fix: fmt.Sprintf("%v; create it in %s (or drop the (link) annotation) and dispatch again", lerr, dir)}
	}
	if command != "" {
		timeout, err := cfg.SetupTimeoutDuration()
		if err != nil {
			return fmt.Errorf("worktree.setup_timeout: %w", err)
		}
		rc, tail, dur, serr := runWorktreeSetup(dir, wt, task, command, timeout)
		ev.RC, ev.DurationMS, ev.Note = &rc, dur.Milliseconds(), clipSetupNote(tail)
		if serr != nil {
			ev.Note = clipSetupNote(serr.Error() + "\n" + tail)
		}
		if err := AppendEvent(dir, ev); err != nil {
			return err
		}
		if serr != nil || rc != 0 {
			why := fmt.Sprintf("exited %d", rc)
			if serr != nil {
				why = serr.Error()
			}
			return &RuleRefusal{Rule: "setup", Fix: fmt.Sprintf("worktree.setup %q %s in %s; fix it (flywheel config set worktree.setup ...) and dispatch again; output tail:\n%s", command, why, wt, tail)}
		}
		return nil
	}
	return AppendEvent(dir, ev)
}

// clipSetupNote keeps the last 2000 bytes of a setup note.
func clipSetupNote(s string) string {
	if len(s) > 2000 {
		s = s[len(s)-2000:]
	}
	return s
}

// setupTailLines is how many trailing output lines a setup run keeps.
const setupTailLines = 20

// setupInterpreters are the first words whose second word resolveSetupCommand
// treats as a script path.
var setupInterpreters = map[string]bool{"node": true, "python": true, "python3": true, "bash": true, "sh": true, "pwsh": true, "powershell": true}

// resolveSetupCommand rewrites a relative script path in command's first word
// (or its second, after an interpreter from setupInterpreters) to the absolute
// path under root, forward slashes, quoted when it holds a space, when the
// path is missing under wt but exists under root (issue #471): a setup script
// committed to the main checkout after the unit branched. Every other command
// (flags, absolute paths, paths present in wt, quotes or shell operators in
// the first two words) is returned unchanged.
func resolveSetupCommand(command, wt, root string, exists func(string) bool) string {
	type span struct{ start, end int }
	var words []span
	for i := 0; i < len(command) && len(words) < 2; {
		for i < len(command) && (command[i] == ' ' || command[i] == '\t') {
			i++
		}
		start := i
		for i < len(command) && command[i] != ' ' && command[i] != '\t' {
			i++
		}
		if i > start {
			words = append(words, span{start, i})
		}
	}
	for _, w := range words {
		if strings.ContainsAny(command[w.start:w.end], "\"'`|&;<>$()*?\n") {
			return command
		}
	}
	var target span
	switch {
	case len(words) >= 1 && strings.ContainsAny(command[words[0].start:words[0].end], `/\`):
		target = words[0]
	case len(words) == 2 && setupInterpreters[command[words[0].start:words[0].end]]:
		target = words[1]
	default:
		return command
	}
	word := command[target.start:target.end]
	if strings.HasPrefix(word, "-") || strings.HasPrefix(word, "/") || strings.HasPrefix(word, `\`) || filepath.IsAbs(word) || filepath.VolumeName(word) != "" {
		return command
	}
	rel := filepath.FromSlash(word)
	if exists(filepath.Join(wt, rel)) || !exists(filepath.Join(root, rel)) {
		return command
	}
	abs := filepath.ToSlash(filepath.Join(root, rel))
	if strings.ContainsAny(abs, " \t") {
		abs = `"` + abs + `"`
	}
	return command[:target.start] + abs + command[target.end:]
}

// runWorktreeSetup runs the worktree.setup command in the task's worktree wt
// through the shell gates use (ShellArgv: Git for Windows' bash on Windows,
// never the WSL launcher), after resolveSetupCommand, with FLYWHEEL_TASK,
// FLYWHEEL_WORKTREE and FLYWHEEL_ROOT (both absolute) added to the
// environment. The command is killed at timeout. It returns the exit code, the last 20 lines of combined
// output and the elapsed time; a spawn failure or a timeout is an error.
func runWorktreeSetup(dir, wt, task, command string, timeout time.Duration) (rc int, tail string, dur time.Duration, err error) {
	absWT, err := filepath.Abs(wt)
	if err != nil {
		return 0, "", 0, err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return 0, "", 0, err
	}
	command = resolveSetupCommand(command, absWT, absDir, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	argv := ShellArgv(command)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = absWT
	cmd.Env = append(os.Environ(), "FLYWHEEL_TASK="+task, "FLYWHEEL_WORKTREE="+absWT, "FLYWHEEL_ROOT="+absDir)
	// A grandchild holding the output pipe open must not outlive the kill.
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		return 0, "", 0, fmt.Errorf("start %v: %w", argv, err)
	}
	werr := cmd.Wait()
	dur = time.Since(t0)
	tail = outputTail(out.String(), setupTailLines)
	rc = -1
	if cmd.ProcessState != nil {
		rc = cmd.ProcessState.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return -1, tail, dur, fmt.Errorf("worktree.setup timed out after %s", timeout)
	}
	if rc == -1 && werr != nil {
		return rc, tail, dur, werr
	}
	return rc, tail, dur, nil
}

// outputTail returns the last n non-empty-trailing lines of s.
func outputTail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
