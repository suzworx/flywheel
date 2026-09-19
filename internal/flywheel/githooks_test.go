package flywheel

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitHooksWritesBothOnce(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	abs, pieces, err := InitGitHooks(dir)
	if err != nil {
		t.Fatalf("first InitGitHooks: %v", err)
	}
	if len(pieces) != 2 {
		t.Fatalf("expected 2 pieces, got %d", len(pieces))
	}
	if !pieces[0].Added || !pieces[1].Added {
		t.Fatalf("expected both pieces Added=true, got %v", pieces)
	}

	// Read the created files
	commitMsgPath := filepath.Join(abs, pieces[0].Path)
	prePushPath := filepath.Join(abs, pieces[1].Path)
	commitMsgBytes, err := os.ReadFile(commitMsgPath)
	if err != nil {
		t.Fatalf("read commit-msg: %v", err)
	}
	prePushBytes, err := os.ReadFile(prePushPath)
	if err != nil {
		t.Fatalf("read pre-push: %v", err)
	}

	// Second call should report Added=false
	abs2, pieces2, err := InitGitHooks(dir)
	if err != nil {
		t.Fatalf("second InitGitHooks: %v", err)
	}
	if pieces2[0].Added || pieces2[1].Added {
		t.Fatalf("expected both pieces Added=false on second call, got %v", pieces2)
	}
	if abs != abs2 {
		t.Fatalf("abs changed: %s -> %s", abs, abs2)
	}

	// Verify bytes are identical
	commitMsgBytes2, err := os.ReadFile(filepath.Join(abs2, pieces2[0].Path))
	if err != nil {
		t.Fatalf("read commit-msg second time: %v", err)
	}
	prePushBytes2, err := os.ReadFile(filepath.Join(abs2, pieces2[1].Path))
	if err != nil {
		t.Fatalf("read pre-push second time: %v", err)
	}
	if !bytes.Equal(commitMsgBytes, commitMsgBytes2) {
		t.Fatal("commit-msg bytes changed on second InitGitHooks")
	}
	if !bytes.Equal(prePushBytes, prePushBytes2) {
		t.Fatal("pre-push bytes changed on second InitGitHooks")
	}
}

func TestGitHooksLeavesExistingHookUntouched(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Create pre-push with custom content before InitGitHooks
	hooksDir := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	prePushPath := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(prePushPath, []byte("custom"), 0o755); err != nil {
		t.Fatalf("write custom pre-push: %v", err)
	}

	abs, pieces, err := InitGitHooks(dir)
	if err != nil {
		t.Fatalf("InitGitHooks: %v", err)
	}
	if len(pieces) != 2 {
		t.Fatalf("expected 2 pieces, got %d", len(pieces))
	}

	// commit-msg should be added
	if !pieces[0].Added {
		t.Fatal("expected commit-msg Added=true")
	}
	// pre-push should not be added
	if pieces[1].Added {
		t.Fatal("expected pre-push Added=false")
	}

	// Verify pre-push content is unchanged
	content, err := os.ReadFile(filepath.Join(abs, pieces[1].Path))
	if err != nil {
		t.Fatalf("read pre-push: %v", err)
	}
	if !bytes.Equal(content, []byte("custom")) {
		t.Fatalf("pre-push content changed: %q", content)
	}
}

func TestGitHooksCommitMsgRequiresTrailer(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()
	if err := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	abs, pieces, err := InitGitHooks(dir)
	if err != nil {
		t.Fatalf("InitGitHooks: %v", err)
	}
	commitMsgPath := filepath.Join(abs, pieces[0].Path)

	tests := []struct {
		name    string
		msg     string
		wantErr bool
	}{
		{"no trailer", "feat: x\n", true},
		{"with trailer", "feat: x\n\nFlywheel-Task: T1\n", false},
		{"merge commit exempt", "Merge branch 'a'\n", false},
		{"revert exempt", "Revert 'something'\n", false},
		{"fixup exempt", "fixup! previous\n", false},
		{"squash exempt", "squash! previous\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgFile := filepath.Join(dir, "msg")
			if err := os.WriteFile(msgFile, []byte(tt.msg), 0o644); err != nil {
				t.Fatalf("write msg: %v", err)
			}
			defer os.Remove(msgFile)

			cmd := exec.Command(sh, commitMsgPath, msgFile)
			cmd.Dir = dir
			err := cmd.Run()
			if (err != nil) != tt.wantErr {
				t.Errorf("hook returned err=%v, want err=%v", err != nil, tt.wantErr)
			}
		})
	}
}

func TestGitHooksNotARepo(t *testing.T) {
	dir := t.TempDir()
	// Not a repository: and git must not find one above the temp dir.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	_, _, err := InitGitHooks(dir)
	if err == nil {
		t.Fatal("expected error for non-repo directory")
	}
}

func TestGitHooksPrePushSkipsDeletes(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()
	if err := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	abs, pieces, err := InitGitHooks(dir)
	if err != nil {
		t.Fatalf("InitGitHooks: %v", err)
	}
	prePushPath := filepath.Join(abs, pieces[1].Path)

	// Run pre-push hook with delete stdin (all zeros for rsha)
	stdin := "refs/heads/x 0000000000000000000000000000000000000000 refs/heads/x 1111111111111111111111111111111111111111\n"
	cmd := exec.Command(sh, prePushPath)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader([]byte(stdin))
	if err := cmd.Run(); err != nil {
		t.Errorf("pre-push with delete stdin returned error: %v", err)
	}
}
