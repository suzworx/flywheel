package flywheel

import (
	"reflect"
	"strings"
	"testing"
)

// TestSkillAllowAppended checks a claude dispatch whose brief names skills
// adds Skill to the --allowedTools patterns (issue #832).
func TestSkillAllowAppended(t *testing.T) {
	t.Parallel()
	req := RunRequest{Task: "T1", Model: "m", Skills: []string{"x"}, AllowedTools: []string{"Bash"}}
	_, args := claudeAdapter{}.Command(req)
	if got := flagValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Bash", "Skill"}) {
		t.Errorf("--allowedTools = %v, want [Bash Skill]", got)
	}
	if !reflect.DeepEqual(req.AllowedTools, []string{"Bash"}) {
		t.Errorf("request AllowedTools mutated to %v", req.AllowedTools)
	}
}

// TestSkillAllowOnce checks Skill is not repeated when the allow list
// already carries it (issue #832).
func TestSkillAllowOnce(t *testing.T) {
	t.Parallel()
	req := RunRequest{Task: "T1", Model: "m", Skills: []string{"x"}, AllowedTools: []string{"Bash", "Skill"}}
	_, args := claudeAdapter{}.Command(req)
	if got := flagValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Bash", "Skill"}) {
		t.Errorf("--allowedTools = %v, want [Bash Skill]", got)
	}
}

// TestSkillAllowEmptyList checks an allow list that allows nothing still
// passes --allowedTools Skill when skills are named (issue #832).
func TestSkillAllowEmptyList(t *testing.T) {
	t.Parallel()
	_, args := claudeAdapter{}.Command(RunRequest{Task: "T1", Model: "m", Skills: []string{"x"}})
	if got := flagValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Skill"}) {
		t.Errorf("--allowedTools = %v, want [Skill]", got)
	}
}

// TestSkillAllowNoSkills checks a dispatch with no skills is unchanged: the
// allow list is passed as configured and no Skill argument appears.
func TestSkillAllowNoSkills(t *testing.T) {
	t.Parallel()
	req := RunRequest{Task: "T1", Model: "m", AllowedTools: []string{"Bash"}, DisallowedTools: (Worker{}).disallowedTools()}
	_, args := claudeAdapter{}.Command(req)
	if got := flagValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Bash"}) {
		t.Errorf("--allowedTools = %v, want [Bash]", got)
	}
	for _, a := range args {
		if a == "Skill" {
			t.Errorf("args carry Skill with no skills named: %v", args)
		}
	}
	_, none := claudeAdapter{}.Command(RunRequest{Task: "T1", Model: "m"})
	if got := flagValues(none, "--allowedTools"); got != nil {
		t.Errorf("empty allow list, no skills: --allowedTools = %v, want no flag", got)
	}
}

// TestSkillAllowResumed checks a resumed dispatch with skills also carries
// Skill, and the git-stash deny stays.
func TestSkillAllowResumed(t *testing.T) {
	t.Parallel()
	req := RunRequest{Task: "T1", Model: "m", Skills: []string{"x"}, AllowedTools: []string{"Bash"},
		DisallowedTools: (Worker{}).disallowedTools(), Resume: true, Session: "s1"}
	_, args := claudeAdapter{}.Command(req)
	if got := flagValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Bash", "Skill"}) {
		t.Errorf("resumed: --allowedTools = %v, want [Bash Skill]", got)
	}
	if got := flagValues(args, "--resume"); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Errorf("resumed: --resume = %v, want [s1]", got)
	}
	deny := strings.Join(flagValues(args, "--disallowedTools"), " ")
	if !strings.Contains(deny, "Bash(git stash:*)") {
		t.Errorf("resumed: --disallowedTools = %q, want Bash(git stash:*) kept", deny)
	}
}

// TestWorkerRulesCheckpoint checks the worker rules name the alternative to
// stash: copy files in the worktree; flywheel checkpoints (issue #832).
func TestWorkerRulesCheckpoint(t *testing.T) {
	t.Parallel()
	if !strings.Contains(workerRules, "checkpoints") {
		t.Errorf("workerRules has no checkpoints sentence")
	}
	if !strings.Contains(workerRules, "stash") {
		t.Errorf("workerRules lost its stash ban")
	}
}
