package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseBriefHeaderOwnsNone checks `owns: none`, `owns: NONE` and `owns: -`
// declare a unit owning no paths (issue #693): Owns is empty and OwnsNone is
// set, while none mixed with a path keeps the path and leaves OwnsNone unset.
func TestParseBriefHeaderOwnsNone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		header   string
		wantOwns []string
		wantNone bool
	}{
		{"none", "owns: none\n", nil, true},
		{"upper", "owns: NONE\n", nil, true},
		{"dash", "owns: -\n", nil, true},
		{"none then path", "owns: none\n  a.go\n", []string{"a.go"}, false},
		{"no owns line", "needs: none\n", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := ParseBriefHeaderBytes([]byte(tc.header + "gate: true\n\n# TASK: x\n"))
			if err != nil {
				t.Fatalf("ParseBriefHeaderBytes() error = %v", err)
			}
			if !slices.Equal(h.Owns, tc.wantOwns) {
				t.Errorf("Owns = %q, want %q", h.Owns, tc.wantOwns)
			}
			if h.OwnsNone != tc.wantNone {
				t.Errorf("OwnsNone = %v, want %v", h.OwnsNone, tc.wantNone)
			}
		})
	}
}

// TestLintOwnsNone checks flywheel lint counts `owns: none` as a present owns
// line that names no missing path, flags none mixed with paths, and still
// reports a brief with no owns line (issue #693).
func TestLintOwnsNone(t *testing.T) {
	t.Parallel()
	const body = "needs: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
	cases := []struct {
		name  string
		owns  string
		files []string
		want  []string
	}{
		{"none", "owns: none\n", nil, nil},
		{"dash", "owns: -\n", nil, nil},
		{"none with path", "owns: none\n  a.go\n", []string{"a.go"}, []string{"owns: none cannot be combined with paths"}},
		{"no owns line", "", nil, []string{"missing owns: line"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := lintCheck(t, t.TempDir(), tc.files, tc.owns+body)
			want(t, res, tc.want, nil)
		})
	}
}

// TestValidateOwnsNone checks the owns check of a unit that owns no paths
// (issue #693): a clean tree passes, and any changed file outside flywheel's
// own artifacts is outside owns.
func TestValidateOwnsNone(t *testing.T) {
	t.Parallel()
	dir, err := initTaskOwns(t, []string{"none"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskOwns() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() clean error = %v", err)
	}
	if !res.OwnsOK || len(res.Outside) != 0 {
		t.Errorf("clean: OwnsOK = %v, outside = %v, want true and nothing", res.OwnsOK, res.Outside)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package y\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "none"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write none: %v", err)
	}
	res, err = ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() changed error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("changed: OwnsOK = %v, want false (a unit owning nothing wrote files)", res.OwnsOK)
	}
	if !slices.Equal(res.Outside, []string{"a.go", "none"}) {
		t.Errorf("outside = %v, want [a.go none]", res.Outside)
	}
}

// TestCollisionOwnsNone checks a unit declaring `owns: none` never collides at
// dispatch (issue #693), with another owns-none unit or with one owning a path,
// in either direction, reading the owns the way flywheel run does.
func TestCollisionOwnsNone(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, t1, t2 string }{
		{"none vs none", "none", "none"},
		{"none vs path", "none", "a.go"},
		{"path vs none", "a.go", "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if _, err := Init(dir, false); err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", tc.t1))
			planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", tc.t2))
			evs, err := ReadEvents(dir)
			if err != nil {
				t.Fatalf("ReadEvents() error = %v", err)
			}
			h, _, err := AttemptBrief(dir, evs, "T2")
			if err != nil {
				t.Fatalf("AttemptBrief(T2) error = %v", err)
			}
			if got := ownsCollisionWith(dir, evs, "T2", h.Owns); got != nil {
				t.Errorf("ownsCollisionWith(%q vs in-flight %q) = %+v, want nil", tc.t2, tc.t1, got)
			}
		})
	}
}
