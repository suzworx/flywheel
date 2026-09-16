package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeBrief(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brief.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	return path
}

func TestParseBriefHeaderOwnsContinuationAndAnnotations(t *testing.T) {
	path := writeBrief(t, "owns: internal/a.go (new), internal/b.go,\n"+
		"      internal/c/ (new fixtures),\n"+
		"      README.md (the CLI table only)\n"+
		"needs: none\n\n# TASK: something\n## Context\nbody\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	want := []string{"internal/a.go", "internal/b.go", "internal/c/", "README.md"}
	if len(h.Owns) != len(want) {
		t.Fatalf("owns = %v, want %v", h.Owns, want)
	}
	for i := range want {
		if h.Owns[i] != want[i] {
			t.Errorf("owns[%d] = %q, want %q", i, h.Owns[i], want[i])
		}
	}
	if len(h.Needs) != 1 || h.Needs[0] != "none" {
		t.Errorf("needs = %v, want [none]", h.Needs)
	}
}

func TestParseBriefHeaderIndentedLineAfterNonOwnsKey(t *testing.T) {
	// "go test ./..." is indented under a gate line; it must not become an
	// owns entry, and the gate keeps its value as written.
	path := writeBrief(t, "owns: a.go\ngate: go build ./... &&\n"+
		"  go test ./...\nneeds: none\n\n# TASK: gates\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if len(h.Owns) != 1 || h.Owns[0] != "a.go" {
		t.Errorf("owns = %v, want [a.go]", h.Owns)
	}
	want := []string{"go build ./... &&"}
	if len(h.Gates) != len(want) {
		t.Fatalf("gates = %v, want %v", h.Gates, want)
	}
	for i := range want {
		if h.Gates[i] != want[i] {
			t.Errorf("gates[%d] = %q, want %q", i, h.Gates[i], want[i])
		}
	}
	if len(h.Needs) != 1 || h.Needs[0] != "none" {
		t.Errorf("needs = %v, want [none]", h.Needs)
	}
}

func TestParseBriefHeaderGatesInOrder(t *testing.T) {
	path := writeBrief(t, "owns: a.go\nneeds: none\ngate: go build ./...\ngate: go vet ./...\n"+
		"gate: go test ./...\n\n# TASK: gates\nreview: after the header, must be ignored\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	want := []string{"go build ./...", "go vet ./...", "go test ./..."}
	if len(h.Gates) != len(want) {
		t.Fatalf("gates = %v, want %v", h.Gates, want)
	}
	for i := range want {
		if h.Gates[i] != want[i] {
			t.Errorf("gates[%d] = %q, want %q", i, h.Gates[i], want[i])
		}
	}
	if len(h.Review) != 0 {
		t.Errorf("review = %v, want empty (line after the header block)", h.Review)
	}
}

func TestParseBriefHeaderLiveGatesInOrder(t *testing.T) {
	path := writeBrief(t, "owns: a.go\nneeds: none\ngate: go build ./...\nlive-gate: go run ./cmd/real\n"+
		"gate: go test ./...\nlive-gate: go run ./cmd/real2\n\n# TASK: live\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	wantGates := []string{"go build ./...", "go test ./..."}
	if len(h.Gates) != len(wantGates) {
		t.Fatalf("gates = %v, want %v", h.Gates, wantGates)
	}
	for i := range wantGates {
		if h.Gates[i] != wantGates[i] {
			t.Errorf("gates[%d] = %q, want %q", i, h.Gates[i], wantGates[i])
		}
	}
	wantLive := []string{"go run ./cmd/real", "go run ./cmd/real2"}
	if len(h.LiveGates) != len(wantLive) {
		t.Fatalf("liveGates = %v, want %v", h.LiveGates, wantLive)
	}
	for i := range wantLive {
		if h.LiveGates[i] != wantLive[i] {
			t.Errorf("liveGates[%d] = %q, want %q", i, h.LiveGates[i], wantLive[i])
		}
	}
}

func TestParseBriefHeaderLiveGatesAbsentIsEmpty(t *testing.T) {
	path := writeBrief(t, "owns: a.go\nneeds: none\ngate: go build ./...\n\n# TASK: x\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if len(h.LiveGates) != 0 {
		t.Errorf("liveGates = %v, want empty", h.LiveGates)
	}
}

func TestParseBriefHeaderExclusiveAndReview(t *testing.T) {
	path := writeBrief(t, "owns: a.go\nexclusive: .pio/\nreview: lead\n\n# TASK: x\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if len(h.Exclusive) != 1 || h.Exclusive[0] != ".pio/" {
		t.Errorf("exclusive = %v, want [.pio/]", h.Exclusive)
	}
	if len(h.Review) != 1 || h.Review[0] != "lead" {
		t.Errorf("review = %v, want [lead]", h.Review)
	}
}

func TestParseBriefHeaderNeedsState(t *testing.T) {
	path := writeBrief(t, "owns: a.go\nneeds: none\nneeds-state: .env, data/db.sqlite\n"+
		"needs-state: sub/\ngate: go build ./...\n\n# TASK: x\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	want := []string{".env", "data/db.sqlite", "sub/"}
	if len(h.NeedsState) != len(want) {
		t.Fatalf("needsState = %v, want %v", h.NeedsState, want)
	}
	for i := range want {
		if h.NeedsState[i] != want[i] {
			t.Errorf("needsState[%d] = %q, want %q", i, h.NeedsState[i], want[i])
		}
	}
	// needs-state must not disturb owns/needs/gate parsing.
	if len(h.Owns) != 1 || h.Owns[0] != "a.go" {
		t.Errorf("owns = %v, want [a.go]", h.Owns)
	}
	if len(h.Needs) != 1 || h.Needs[0] != "none" {
		t.Errorf("needs = %v, want [none]", h.Needs)
	}
	if len(h.Gates) != 1 || h.Gates[0] != "go build ./..." {
		t.Errorf("gates = %v, want [go build ./...]", h.Gates)
	}
}

func TestParseBriefHeaderNeedsStateAbsentIsEmpty(t *testing.T) {
	path := writeBrief(t, "owns: a.go\nneeds: none\ngate: go build ./...\n\n# TASK: x\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if len(h.NeedsState) != 0 {
		t.Errorf("needsState = %v, want empty", h.NeedsState)
	}
}

func TestParseBriefHeaderMissingHeader(t *testing.T) {
	path := writeBrief(t, "# TASK: no header keys here\n\n## Context\nbody\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if len(h.Owns) != 0 || len(h.Needs) != 0 || len(h.Gates) != 0 ||
		len(h.Exclusive) != 0 || len(h.Review) != 0 {
		t.Errorf("header = %+v, want all empty lists", h)
	}
	if h.SHA256 == "" {
		t.Error("missing SHA256")
	}
}

func TestParseBriefHeaderSHA256(t *testing.T) {
	content := "owns: a.go\nneeds: none\ngate: go build ./...\n\n# TASK: x\n"
	path := writeBrief(t, content)
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	if h.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %q, want %q", h.SHA256, hex.EncodeToString(sum[:]))
	}
}

func TestParseBriefHeaderMissingFile(t *testing.T) {
	if _, err := ParseBriefHeader(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Error("ParseBriefHeader() accepted a missing file")
	}
}
