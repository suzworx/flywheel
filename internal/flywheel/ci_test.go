package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitCIWritesAtRepoRoot tests that InitCI writes the file at the repository root
func TestInitCIWritesAtRepoRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initGitRepoAt(t, root)

	_, pieces, err := InitCI(root, "dev")
	if err != nil {
		t.Fatalf("InitCI: %v", err)
	}

	if len(pieces) != 1 {
		t.Fatalf("expected 1 piece, got %d", len(pieces))
	}
	if !pieces[0].Added {
		t.Fatalf("expected piece to be Added")
	}

	// Check the file path contains flywheel-audit.yml
	if !strings.Contains(pieces[0].Path, "flywheel-audit.yml") {
		t.Fatalf("expected path to contain flywheel-audit.yml, got %q", pieces[0].Path)
	}

	// Check the file exists
	filePath := filepath.Join(root, ".github", "workflows", "flywheel-audit.yml")
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file not found at %s: %v", filePath, err)
	}

	// Check content contains "--dir ." and "@latest"
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	contentStr := string(content)
	if !strings.Contains(contentStr, `FLYWHEEL_DIR: "."`) || !strings.Contains(contentStr, `--dir "$FLYWHEEL_DIR"`) {
		t.Fatalf("expected content to target FLYWHEEL_DIR \".\", got %q", contentStr)
	}
	if !strings.Contains(contentStr, "@latest") {
		t.Fatalf("expected content to contain '@latest', got %q", contentStr)
	}
}

// TestInitCISubdirTargetsFactory tests that InitCI places the file at the repo root
// even when called from a subdirectory
func TestInitCISubdirTargetsFactory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initGitRepoAt(t, root)

	svcDir := filepath.Join(root, "svc", "api")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, pieces, err := InitCI(svcDir, "dev")
	if err != nil {
		t.Fatalf("InitCI: %v", err)
	}

	if len(pieces) != 1 {
		t.Fatalf("expected 1 piece, got %d", len(pieces))
	}

	// Check the file is at the root
	filePath := filepath.Join(root, ".github", "workflows", "flywheel-audit.yml")
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file not found at %s: %v", filePath, err)
	}

	// Check content contains "--dir svc/api"
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	contentStr := string(content)
	if !strings.Contains(contentStr, `FLYWHEEL_DIR: "svc/api"`) {
		t.Fatalf("expected content to contain FLYWHEEL_DIR \"svc/api\", got %q", contentStr)
	}
}

// TestInitCIPinsReleaseVersion tests that InitCI pins release versions correctly
func TestInitCIPinsReleaseVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version  string
		expected string
	}{
		{"v0.17.0", "@v0.17.0"},
		{"0.18.1", "@v0.18.1"},
		{"dev", "@latest"},
		{"", "@latest"},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			root := t.TempDir()
			initGitRepoAt(t, root)

			_, _, err := InitCI(root, tt.version)
			if err != nil {
				t.Fatalf("InitCI: %v", err)
			}

			filePath := filepath.Join(root, ".github", "workflows", "flywheel-audit.yml")
			content, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatalf("read file: %v", err)
			}
			contentStr := string(content)
			if !strings.Contains(contentStr, tt.expected) {
				t.Fatalf("version %q: expected content to contain %q, got %q", tt.version, tt.expected, contentStr)
			}
		})
	}
}

// TestInitCINeverOverwrites tests that InitCI never overwrites an existing file
func TestInitCINeverOverwrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initGitRepoAt(t, root)

	// Pre-write the file with custom content
	workflowDir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	filePath := filepath.Join(workflowDir, "flywheel-audit.yml")
	customContent := "custom"
	if err := os.WriteFile(filePath, []byte(customContent), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, pieces, err := InitCI(root, "dev")
	if err != nil {
		t.Fatalf("InitCI: %v", err)
	}

	if len(pieces) != 1 {
		t.Fatalf("expected 1 piece, got %d", len(pieces))
	}
	if pieces[0].Added {
		t.Fatalf("expected piece to be not Added")
	}

	// Check the file still has the custom content
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != customContent {
		t.Fatalf("expected custom content, got %q", string(content))
	}
}

// TestInitCINotARepo tests that InitCI errors when not in a git repository
func TestInitCINotARepo(t *testing.T) {
	// not parallel: t.Setenv GIT_CEILING_DIRECTORIES
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	_, _, err := InitCI(dir, "dev")
	if err == nil {
		t.Fatalf("InitCI: expected error, got nil")
	}
}

// TestInitCIPathWithSpace checks that any directory name git accepts is
// written safely: YAML-quoted in env, used as "$FLYWHEEL_DIR" (#315 review).
func TestInitCIPathWithSpace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initGitRepoAt(t, root)
	sub := filepath.Join(root, "order api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitCI(sub, "dev"); err != nil {
		t.Fatalf("InitCI with a space in the path: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "flywheel-audit.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `FLYWHEEL_DIR: "order api"`) {
		t.Errorf("workflow does not quote the directory:\n%s", b)
	}
}
