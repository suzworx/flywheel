package flywheel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitCommitMsgHook and gitPrePushHook are the hook scripts InitGitHooks
// writes (issue #56). POSIX sh only; the flywheel binary resolved from PATH.
const gitCommitMsgHook = `#!/bin/sh
# flywheel commit-msg: every commit names its unit with a Flywheel-Task trailer (issue #56).
# Written by "flywheel init --git-hooks"; a rerun never overwrites this file.
head -n 1 "$1" | grep -q -E '^(Merge |Revert |fixup! |squash! )' && exit 0
git interpret-trailers --parse "$1" | grep -q -E '^Flywheel-Task: *[A-Za-z0-9._-]+ *$' && exit 0
echo "flywheel: name the unit this commit belongs to with a trailer, e.g. git commit --trailer 'Flywheel-Task: <id>'" >&2
exit 1
`

// gitPrePushHook is a template: @FLYWHEEL_DIR@ is the flywheel directory, shell-quoted and
// relative to the repository root (git runs pre-push from the worktree root),
// so a factory scaffolded below the root verifies its own ledger (#308 review).
const gitPrePushHook = `#!/bin/sh
# flywheel pre-push: a push is refused while a unit named in the pushed commits fails
# flywheel verify, or the event log's hash chain is broken (issue #56).
# Written by "flywheel init --git-hooks"; a rerun never overwrites this file.
d=@FLYWHEEL_DIR@
fail=0
while read -r lref lsha rref rsha; do
  case "$lsha" in *[!0]*) ;; *) continue ;; esac
  case "$rsha" in *[!0]*) range="$rsha..$lsha" ;; *) range="$lsha --not --remotes" ;; esac
  for t in $(git log --format='%(trailers:key=Flywheel-Task,valueonly)' $range | sort -u); do
    flywheel verify --dir "$d" "$t" >&2 || { echo "flywheel: unit $t fails flywheel verify; fix its record before pushing" >&2; fail=1; }
  done
done
if [ -f "$d/.flywheel/events.jsonl" ]; then
  flywheel verify --dir "$d" --log >&2 || fail=1
fi
exit $fail
`

// InitGitHooks installs the commit-msg and pre-push hooks into the
// repository's hooks directory (`git rev-parse --git-path hooks`, so linked
// worktrees and core.hooksPath are honoured). Each hook is created only when
// missing and never overwritten; a failure after one is written removes what
// this call created. Like InitHooks, it returns the resolved repository
// directory and one piece per hook.
func InitGitHooks(dir string) (string, []ScaffoldPiece, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %q: %w", dir, err)
	}

	cmd := exec.Command("git", "-C", abs, "rev-parse", "--git-path", "hooks")
	out, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("git rev-parse --git-path hooks: %w", err)
	}
	hooksRel := strings.TrimSpace(string(out))
	hooksPath := hooksRel
	if !filepath.IsAbs(hooksPath) {
		hooksPath = filepath.Join(abs, hooksRel)
	}

	if err := os.MkdirAll(hooksPath, 0o755); err != nil {
		return "", nil, fmt.Errorf("create %s: %w", hooksPath, err)
	}

	// The factory's directory relative to the worktree root, from git itself
	// (--show-prefix), so no path comparison can trip on 8.3 short names or
	// symlinks; "" at the root.
	prefix, err := exec.Command("git", "-C", abs, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return "", nil, fmt.Errorf("git rev-parse --show-prefix: %w", err)
	}
	flywheelDir := strings.TrimSuffix(strings.TrimSpace(string(prefix)), "/")
	if flywheelDir == "" {
		flywheelDir = "."
	}
	prePush := strings.Replace(gitPrePushHook, "@FLYWHEEL_DIR@", shellQuote(flywheelDir), 1)

	commitMsgPath := filepath.Join(hooksPath, "commit-msg")
	prePushPath := filepath.Join(hooksPath, "pre-push")

	createdCommitMsg, err := createIfMissing(commitMsgPath, []byte(gitCommitMsgHook))
	if err != nil {
		return "", nil, fmt.Errorf("write %s: %w", commitMsgPath, err)
	}
	if createdCommitMsg {
		if err := os.Chmod(commitMsgPath, 0o755); err != nil {
			os.Remove(commitMsgPath)
			return "", nil, fmt.Errorf("chmod %s: %w", commitMsgPath, err)
		}
	}

	createdPrePush, err := createIfMissing(prePushPath, []byte(prePush))
	if err != nil {
		if createdCommitMsg {
			os.Remove(commitMsgPath)
		}
		return "", nil, fmt.Errorf("write %s: %w", prePushPath, err)
	}
	if createdPrePush {
		if err := os.Chmod(prePushPath, 0o755); err != nil {
			os.Remove(prePushPath)
			if createdCommitMsg {
				os.Remove(commitMsgPath)
			}
			return "", nil, fmt.Errorf("chmod %s: %w", prePushPath, err)
		}
	}

	return abs, []ScaffoldPiece{
		{Path: hookPiecePath(abs, commitMsgPath), Added: createdCommitMsg},
		{Path: hookPiecePath(abs, prePushPath), Added: createdPrePush},
	}, nil
}

// hookPiecePath names a hook relative to the repository, slash-separated, or
// by its absolute path when it lies elsewhere (a linked worktree's hooks live
// in the main repository; core.hooksPath can point anywhere).
func hookPiecePath(abs, path string) string {
	rel, err := filepath.Rel(abs, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// shellQuote single-quotes s for POSIX sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
