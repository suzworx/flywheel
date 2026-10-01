package flywheel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSkillsLoadedClaudeObservation(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ line, want string }{
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"u1","name":"Skill","input":{"skill":" superpowers:tdd "}}]}}`, "superpowers:tdd"},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"u2","name":"Read","input":{"file_path":"a.go"}}]}}`, ""},
	} {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.line), &m); err != nil {
			t.Fatal(err)
		}
		obs, ok := claudeAssistantObs(m)
		if !ok || obs.Kind != "tool" || obs.Skill != c.want {
			t.Errorf("claudeAssistantObs(%s) = %+v ok=%v, want Skill %q", c.line, obs, ok, c.want)
		}
	}
}

// skillsEvents is a planned, a dispatched naming skills on adapter, and one
// finished event per loaded list.
func skillsEvents(adapter string, skills []string, loaded ...[]string) []Event {
	ev := []Event{
		{Task: "T1", Kind: "planned"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: adapter, Skills: skills},
	}
	for i, l := range loaded {
		ev = append(ev, Event{Task: "T1", Kind: "finished", Attempt: "r" + string(rune('1'+i)), SkillsLoaded: l})
	}
	return ev
}

func TestSkillsLoadedNotLoaded(t *testing.T) {
	t.Parallel()
	replan := append(skillsEvents("claude", []string{"tdd"}, []string{"tdd"}),
		Event{Task: "T1", Kind: "planned"},
		Event{Task: "T1", Kind: "dispatched", Attempt: "r2", Adapter: "claude", Skills: []string{"tdd"}},
		Event{Task: "T1", Kind: "finished", Attempt: "r2"})
	cases := []struct {
		name      string
		events    []Event
		want      []string
		checkable bool
	}{
		{"all loaded", skillsEvents("claude", []string{"tdd", "go"}, []string{"go", "tdd"}), nil, true},
		{"one missing", skillsEvents("claude", []string{"tdd", "go", "x"}, []string{"go"}), []string{"tdd", "x"}, true},
		{"plain named, plugin loaded", skillsEvents("claude", []string{"tdd"}, []string{"superpowers:tdd"}), nil, true},
		{"plugin named, plain loaded", skillsEvents("claude", []string{"superpowers:tdd"}, []string{"tdd"}), nil, true},
		{"other plugin", skillsEvents("claude", []string{"a:tdd"}, []string{"b:tdd"}), []string{"a:tdd"}, true},
		{"case-sensitive", skillsEvents("claude", []string{"TDD"}, []string{"tdd"}), []string{"TDD"}, true},
		{"across a resume", skillsEvents("claude", []string{"tdd", "go"}, []string{"tdd"}, []string{"go"}), nil, true},
		{"opencode", skillsEvents("opencode", []string{"tdd"}, nil), nil, false},
		{"no finished", skillsEvents("claude", []string{"tdd"}), nil, false},
		{"re-plan ignores older loads", replan, []string{"tdd"}, true},
		{"no skills named", skillsEvents("claude", nil, nil), nil, false},
		{"no dispatched", []Event{{Task: "T1", Kind: "planned"}, {Task: "T1", Kind: "finished"}}, nil, false},
	}
	for _, c := range cases {
		got, ok := skillsNotLoaded(c.events, "T1")
		if !reflect.DeepEqual(got, c.want) || ok != c.checkable {
			t.Errorf("%s: skillsNotLoaded() = %q, %v, want %q, %v", c.name, got, ok, c.want, c.checkable)
		}
	}
	if fix := skillsNotLoadedFix("T1", []string{"a", "b"}); !strings.Contains(fix, "a, b: the worker never loaded these skills") ||
		!strings.Contains(fix, "flywheel run T1 --delta <file>") {
		t.Errorf("skillsNotLoadedFix() = %q", fix)
	}
}

func TestSkillsLoadedEventValidate(t *testing.T) {
	t.Parallel()
	if err := Validate(Event{Task: "T1", Kind: "finished", SkillsLoaded: []string{"tdd"}}); err != nil {
		t.Errorf("Validate(finished with skills_loaded) = %v, want nil", err)
	}
	err := Validate(Event{Task: "T1", Kind: "dispatched", SkillsLoaded: []string{"tdd"}})
	if err == nil || !strings.Contains(err.Error(), "only a finished event may carry them") {
		t.Errorf("Validate(dispatched with skills_loaded) = %v, want the finished-only error", err)
	}
}

func TestSkillsLoadedInspect(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{true, false} {
		dir, err := initTask(t, []string{"exit 0"})
		if err != nil {
			t.Fatalf("initTask() error = %v", err)
		}
		if !on {
			cfg, _, err := LoadConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Set("skills.require_loaded", "false"); err != nil {
				t.Fatal(err)
			}
			if err := WriteConfig(dir, cfg); err != nil {
				t.Fatal(err)
			}
		}
		if err := AppendEvent(dir, Event{TS: "2026-09-12T00:30:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Skills: []string{"tdd", "go"}}); err != nil {
			t.Fatal(err)
		}
		rc := 0
		if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1", RC: &rc, SkillsLoaded: []string{"go"}}); err != nil {
			t.Fatal(err)
		}
		res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		if err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		events, err := ReadEvents(dir)
		if err != nil {
			t.Fatal(err)
		}
		x, err := Explain(events, "T1")
		if err != nil {
			t.Fatal(err)
		}
		var md strings.Builder
		if err := RenderExplanation(&md, x); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(md.String(), "- skills loaded: go\n") || !strings.Contains(md.String(), "- skills-not-loaded: tdd\n") {
			t.Errorf("explain = %q, want the skills lines", md.String())
		}
		err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
		if !on {
			if res.SkillsNotLoaded != nil || err != nil {
				t.Errorf("toggle off: SkillsNotLoaded = %q, InspectTask() = %v, want nil, nil", res.SkillsNotLoaded, err)
			}
			continue
		}
		if !reflect.DeepEqual(res.SkillsNotLoaded, []string{"tdd"}) || !res.OK() {
			t.Errorf("SkillsNotLoaded = %q OK=%v, want [tdd] with green gates", res.SkillsNotLoaded, res.OK())
		}
		if err == nil {
			t.Fatal("InspectTask() accepted a pass whose worker never loaded tdd")
		}
		if got := refusalRule(t, err); got != "skills-not-loaded" || !strings.Contains(err.Error(), "tdd: the worker never loaded these skills") {
			t.Errorf("InspectTask() = %v (rule %q), want rule skills-not-loaded naming tdd", err, got)
		}
	}
}

func TestSkillsLoadedConfig(t *testing.T) {
	t.Parallel()
	if !(Config{}).SkillsRequireLoaded() || !(Config{Skills: &SkillsConfig{}}).SkillsRequireLoaded() {
		t.Error("SkillsRequireLoaded() = false when unset, want true")
	}
	var c Config
	if err := c.Set("skills.require_loaded", "false"); err != nil || c.SkillsRequireLoaded() {
		t.Errorf("Set(false) err=%v, SkillsRequireLoaded()=%v, want nil, false", err, c.SkillsRequireLoaded())
	}
	if got, err := c.Get("skills.require_loaded"); err != nil || got != "false" {
		t.Errorf("Get() = %q, %v, want false", got, err)
	}
	if err := c.Set("skills.require_loaded", "maybe"); err == nil || !strings.Contains(err.Error(), "must be true or false") {
		t.Errorf("Set(maybe) = %v, want a must-be-boolean error", err)
	}
	if err := c.Set("skills.require_loaded", "true"); err != nil || !c.SkillsRequireLoaded() {
		t.Errorf("Set(true) err=%v, SkillsRequireLoaded()=%v, want nil, true", err, c.SkillsRequireLoaded())
	}
}
