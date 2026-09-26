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
	"sort"
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

// escapingLinks lists the links inside one needs-state "(link)" path that
// resolve into the main checkout (issue #460): an npm/pnpm/yarn workspace's
// node_modules/@acme/web -> packages/web, which makes the unit's gates import
// the main checkout's copy. It reads <root>/<linked> one level deep, plus one
// level inside each "@" scope. A link escapes when it resolves inside root but
// neither inside <root>/<linked> itself (pnpm's .pnpm store) nor inside wt.
// A broken link is skipped; an unreadable directory is an error naming it.
// The result is <linked>/<entry> slash paths, sorted.
func escapingLinks(root, wt, linked string) ([]string, error) {
	rel := strings.TrimSuffix(linked, "/")
	if rel == "" {
		return nil, nil
	}
	realRoot, err := resolvedAbs(root)
	if err != nil {
		return nil, fmt.Errorf("needs-state %s: %w", linked, err)
	}
	realWT, err := resolvedAbs(wt)
	if err != nil {
		return nil, fmt.Errorf("needs-state %s: %w", linked, err)
	}
	base := filepath.Join(realRoot, filepath.FromSlash(rel))
	realBase, err := resolvedAbs(base)
	if err != nil {
		return nil, fmt.Errorf("needs-state %s: %w", linked, err)
	}
	if fi, err := os.Stat(realBase); err == nil && !fi.IsDir() {
		return nil, nil
	}
	var out []string
	var scan func(dir, prefix string, scopes bool) error
	scan = func(dir, prefix string, scopes bool) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("needs-state %s: read %s: %w", linked, dir, err)
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			name := prefix + "/" + e.Name()
			fi, err := os.Lstat(p)
			if err != nil {
				continue
			}
			if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				target, ok := linkTarget(p)
				if !ok {
					continue
				}
				if within(target, realRoot) && !within(target, realBase) && !within(target, realWT) {
					out = append(out, name)
				}
				continue
			}
			if scopes && fi.IsDir() && strings.HasPrefix(e.Name(), "@") {
				if err := scan(p, name, false); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := scan(base, rel, true); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// linkTarget resolves the link or junction at p; false when it is broken.
// A Windows junction reads as ModeIrregular and EvalSymlinks may leave it
// unresolved, so when EvalSymlinks returns p itself its Readlink target is
// resolved instead.
func linkTarget(p string) (string, bool) {
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	target, err := filepath.EvalSymlinks(p)
	if err == nil && !within(target, p) {
		return target, true
	}
	dest, err := os.Readlink(p)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(p), dest)
	}
	if resolved, err := filepath.EvalSymlinks(dest); err == nil {
		return resolved, true
	}
	return filepath.Clean(dest), true
}

// resolvedAbs is p absolute with every link resolved.
func resolvedAbs(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// within reports whether p is dir or below it, case-insensitively on Windows.
func within(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		p, dir = strings.ToLower(p), strings.ToLower(dir)
	}
	if p == dir {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// escapeWarning is the one-line warning for a linked path's escaping entries.
func escapeWarning(linked string, escaped []string) string {
	names := escaped
	more := ""
	if len(names) > 5 {
		names, more = names[:5], "..."
	}
	return fmt.Sprintf("warning: needs-state link %s holds links into the main checkout (%d: %s%s) — the unit's gates would import the main checkout's copies; set worktree.setup to an offline install instead (see issue #460)", linked, len(escaped), strings.Join(names, ", "), more)
}

// prepareWorktree is run --worktree's setup step (issue #430): it links the
// effective brief's needs-state "(link)" paths into wt, runs worktree.setup
// when configured, and appends one worktree_setup event. A link error or a
// setup that does not exit 0 is a RuleRefusal with rule "setup"; with no
// links and no setup command it records nothing. Linked paths holding links
// into the main checkout (issue #460) are recorded as Escaped and returned as
// warning lines for the caller to print; with worktree.strict_links they are
// a RuleRefusal too, and setup does not run.
func prepareWorktree(dir, wt, task, attempt string, cfg Config, links []string) ([]string, error) {
	command := cfg.SetupCommand()
	if len(links) == 0 && command == "" {
		return nil, nil
	}
	ev := Event{Task: task, Kind: "worktree_setup", Attempt: attempt, Linked: links, Command: command}
	if lerr := linkNeedsState(dir, wt, links); lerr != nil {
		ev.Linked, ev.Note = nil, clipSetupNote(lerr.Error())
		if err := AppendEvent(dir, ev); err != nil {
			return nil, err
		}
		return nil, &RuleRefusal{Rule: "setup", Fix: fmt.Sprintf("%v; create it in %s (or drop the (link) annotation) and dispatch again", lerr, dir)}
	}
	var warnings []string
	for _, l := range links {
		esc, err := escapingLinks(dir, wt, l)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("warning: could not check needs-state link %s for links into the main checkout: %v", l, err))
			continue
		}
		if len(esc) > 0 {
			ev.Escaped = append(ev.Escaped, esc...)
			warnings = append(warnings, escapeWarning(l, esc))
		}
	}
	if len(ev.Escaped) > 0 && cfg.StrictLinks() {
		ev.Note = clipSetupNote("refused: worktree.strict_links and needs-state links resolve into the main checkout: " + strings.Join(ev.Escaped, ", "))
		if err := AppendEvent(dir, ev); err != nil {
			return warnings, err
		}
		return warnings, &RuleRefusal{Rule: "setup", Fix: fmt.Sprintf("needs-state links hold links into the main checkout (%s) and worktree.strict_links is true; drop the (link) annotation and set worktree.setup to an offline install (pnpm install --offline --frozen-lockfile, npm ci --prefer-offline --no-audit), then dispatch again (issue #460)", strings.Join(ev.Escaped, ", "))}
	}
	if command != "" {
		timeout, err := cfg.SetupTimeoutDuration()
		if err != nil {
			return warnings, fmt.Errorf("worktree.setup_timeout: %w", err)
		}
		rc, tail, dur, serr := runWorktreeSetup(dir, wt, task, command, timeout)
		ev.RC, ev.DurationMS, ev.Note = &rc, dur.Milliseconds(), clipSetupNote(tail)
		if serr != nil {
			ev.Note = clipSetupNote(serr.Error() + "\n" + tail)
		}
		if err := AppendEvent(dir, ev); err != nil {
			return warnings, err
		}
		if serr != nil || rc != 0 {
			why := fmt.Sprintf("exited %d", rc)
			if serr != nil {
				why = serr.Error()
			}
			return warnings, &RuleRefusal{Rule: "setup", Fix: fmt.Sprintf("worktree.setup %q %s in %s; fix it (flywheel config set worktree.setup ...) and dispatch again; output tail:\n%s", command, why, wt, tail)}
		}
		return warnings, nil
	}
	return warnings, AppendEvent(dir, ev)
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
