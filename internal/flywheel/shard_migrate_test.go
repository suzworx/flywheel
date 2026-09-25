package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigLogShardsGetAndRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Log = &LogConfig{Shards: true}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	loaded, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if loaded.Log == nil || !loaded.Log.Shards {
		t.Errorf("Log.Shards = %+v, want true", loaded.Log)
	}
	v, err := loaded.Get("log.shards")
	if err != nil {
		t.Fatalf("Get(\"log.shards\") error = %v", err)
	}
	if v != "true" {
		t.Errorf("Get(\"log.shards\") = %q, want \"true\"", v)
	}

	cfg2 := DefaultConfig()
	v2, err := cfg2.Get("log.shards")
	if err != nil {
		t.Fatalf("Get(\"log.shards\") on default config error = %v", err)
	}
	if v2 != "false" {
		t.Errorf("Get(\"log.shards\") on default config = %q, want \"false\"", v2)
	}
}

func TestConfigLogShardsNotSettable(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	err := cfg.Set("log.shards", "true")
	if err == nil {
		t.Errorf("Set(\"log.shards\", \"true\") = nil, want error")
	}
	if !strings.Contains(err.Error(), "flywheel log --shard") {
		t.Errorf("error message = %q, should mention \"flywheel log --shard\"", err)
	}
}

func TestInitGitignoreIncludesLocks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, _, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(b), "locks/") {
		t.Errorf(".gitignore = %q, want to contain \"locks/\"", b)
	}
}

func TestIgnoredStateFilesReportsShards(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/*\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	gitInit(t, dir)
	_, _, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	ignored := IgnoredStateFiles(dir)
	hasShards := false
	for _, p := range ignored {
		if strings.Contains(p, "events") && strings.Contains(p, "@floor") {
			hasShards = true
			break
		}
	}
	if !hasShards {
		t.Errorf("IgnoredStateFiles() = %v, want to include .flywheel/events/@floor.jsonl", ignored)
	}
}

func TestCITemplateAcceptsShardedLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitInit(t, dir)
	_, _, err := InitCI(dir, "latest")
	if err != nil {
		t.Fatalf("InitCI() error = %v", err)
	}
	workflowPath := filepath.Join(dir, ".github", "workflows", "flywheel-audit.yml")
	b, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	workflow := string(b)
	if !strings.Contains(workflow, "[ ! -d ") || !strings.Contains(workflow, ".flywheel/events") {
		t.Errorf("workflow doesn't accept sharded log directory test")
	}
}

func TestGitHooksAcceptShardedLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitInit(t, dir)
	_, _, err := InitGitHooks(dir)
	if err != nil {
		t.Fatalf("InitGitHooks() error = %v", err)
	}
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-push")
	b, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read pre-push hook: %v", err)
	}
	hook := string(b)
	if !strings.Contains(hook, "[ -d ") || !strings.Contains(hook, ".flywheel/events") {
		t.Errorf("hook doesn't accept sharded log directory test")
	}
}
