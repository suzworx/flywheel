package flywheel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gateCmdDir is a temp dir holding sub/ and an existing scripts/check.sh.
func gateCmdDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "check.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// onlyGoGit is a lookPath that finds go and git and nothing else.
func onlyGoGit(w string) (string, error) {
	if w == "go" || w == "git" {
		return "/bin/" + w, nil
	}
	return "", errors.New("not found")
}

// TestGateCommandPlaceholder: "(as the brief)" is a problem naming "as", and
// a word repeated across gates reaches the shell once (issue #662). The
// lookups are injected so a host with an assembler named as cannot hide it.
func TestGateCommandPlaceholder(t *testing.T) {
	t.Parallel()
	dir := gateCmdDir(t)
	var calls [][]string
	resolve := func(_ string, words []string) []string {
		calls = append(calls, words)
		return words
	}
	got := gateCommandProblemsWith(dir, []string{"go build ./...", "(as the brief)", "as the brief"}, nil, onlyGoGit, resolve)
	want := []string{
		`gate 2: "(as the brief)" looks like placeholder text, not a command; a delta repeats the brief's gate: lines, or declares none to inherit them`,
		`gate 3: "as" is not a command (placeholder text?); a delta repeats the brief's gate: lines, or declares none to inherit them`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %q, want %q", got, want)
	}
	if len(calls) != 1 || !slices.Equal(calls[0], []string{"as"}) {
		t.Errorf("shell resolution calls = %q, want one with [as]", calls)
	}
}

// TestGateCommandPlaceholderPhrases: a wrapped gate with a placeholder phrase
// is a problem even where as resolves (a host with the GNU assembler), a
// wrapped gate whose first word does not resolve is one, and wrapped real
// commands and unwrapped gates are not placeholders (issue #662).
func TestGateCommandPlaceholderPhrases(t *testing.T) {
	t.Parallel()
	dir := gateCmdDir(t)
	withAs := func(w string) (string, error) {
		if w == "as" || w == "grep" {
			return "/usr/bin/" + w, nil
		}
		return onlyGoGit(w)
	}
	resolveNone := func(_ string, words []string) []string { return words }
	msg := func(n int, g string) string {
		return fmt.Sprintf("gate %d: %q looks like placeholder text, not a command; a delta repeats the brief's gate: lines, or declares none to inherit them", n, g)
	}
	for g, want := range map[string][]string{
		"(as the brief)":            {msg(1, "(as the brief)")},
		"<same gates>":              {msg(1, "<same gates>")},
		"(Unchanged)":               {msg(1, "(Unchanged)")},
		"(TBD)":                     {msg(1, "(TBD)")},
		"(cd sub && go test ./...)": nil,
		"(go test ./...)":           nil,
		"grep -q unchanged f":       nil,
		"[ -f go.mod ]":             nil,
	} {
		if got := gateCommandProblemsWith(dir, []string{g}, nil, withAs, resolveNone); !slices.Equal(got, want) {
			t.Errorf("gate %q: problems = %q, want %q", g, got, want)
		}
	}
}

// TestGateCommandAccepted: the command forms a real gate takes are not
// problems, on the real host lookups (issue #662).
func TestGateCommandAccepted(t *testing.T) {
	t.Parallel()
	dir := gateCmdDir(t)
	for _, g := range []string{
		"(cd sub && go test ./...)",
		"cd sub; git status",
		"! grep -q x f",
		"GOOS=linux go vet ./...",
		`A="x y" env B=1 go version`,
		"./scripts/check.sh",
		"timeout 60 go test ./...",
		"timeout -k 5 60 git status",
		"for f in a; do true; done",
		"[[ -f go.mod ]]",
		"$GO test ./...",
		"flywheel-custom-tool-662 --check",
	} {
		if got := gateCommandProblems(dir, []string{g}, []string{"flywheel-custom-tool-662"}); len(got) != 0 {
			t.Errorf("gate %q: problems = %q, want none", g, got)
		}
	}
}

// TestGateCommandWord: the first simple command's word after the skipped
// prefixes (issue #662).
func TestGateCommandWord(t *testing.T) {
	t.Parallel()
	for gate, want := range map[string]string{
		"(as the brief)":            "as",
		"(cd sub && go test ./...)": "go",
		"cd sub; make":              "make",
		"cd sub":                    "cd",
		"{ go vet; }":               "go",
		"env A=1 timeout 60 go x":   "go",
		`"./my tool" --x`:           "./my tool",
		"":                          "",
	} {
		if got := gateCommandWord(gate); got != want {
			t.Errorf("gateCommandWord(%q) = %q, want %q", gate, got, want)
		}
	}
}

// TestGateCommandShell: a word no lookup resolves is reported through the real
// gate shell, and lint reports it as a problem (issue #662).
func TestGateCommandShell(t *testing.T) {
	t.Parallel()
	if ShellArgv("")[0] == "cmd" {
		t.Skip("no bash: the shell resolution step is skipped")
	}
	word := "flywheel-no-such-command-662"
	if _, err := exec.LookPath(word); err == nil {
		t.Skipf("%s exists on this host", word)
	}
	res := lintWith(t, map[string]string{"README.md": "x\n"}, suiteBrief("README.md", word+" the brief"), noGoList)
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0], `gate 1: "`+word+`" is not a command`) {
		t.Errorf("problems = %q, want one naming %s", res.Problems, word)
	}
}
