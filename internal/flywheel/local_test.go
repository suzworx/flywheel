package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInitLocalWritesProviderAndWorker(t *testing.T) {
	dir := t.TempDir()
	Init(dir, false)

	pieces, err := InitLocal(dir, "qwen3-coder:30b", DefaultLocalURL)
	if err != nil {
		t.Fatalf("InitLocal: %v", err)
	}
	if len(pieces) != 2 {
		t.Fatalf("InitLocal returned %d pieces, want 2", len(pieces))
	}

	policyPath := filepath.Join(dir, ".flywheel", "opencode-worker.json")
	policyData, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read policy file: %v", err)
	}

	var policy map[string]any
	if err := json.Unmarshal(policyData, &policy); err != nil {
		t.Fatalf("parse policy: %v", err)
	}

	if policy["permission"] == nil {
		t.Fatal("policy missing 'permission' key")
	}

	providers, ok := policy["provider"].(map[string]any)
	if !ok {
		t.Fatal("policy['provider'] is not a map")
	}

	providerBlock, ok := providers[LocalProviderID].(map[string]any)
	if !ok {
		t.Fatalf("provider[%q] is not a map", LocalProviderID)
	}

	baseURL, ok := providerBlock["options"].(map[string]any)["baseURL"].(string)
	if !ok || baseURL != DefaultLocalURL {
		t.Fatalf("provider baseURL = %q, want %q", baseURL, DefaultLocalURL)
	}

	models, ok := providerBlock["models"].(map[string]any)
	if !ok {
		t.Fatal("provider['models'] is not a map")
	}
	if models["qwen3-coder:30b"] == nil {
		t.Fatal("provider['models'] missing 'qwen3-coder:30b'")
	}

	cfg, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !exists {
		t.Fatal("config.json not found")
	}

	w, ok := cfg.Worker("local")
	if !ok {
		t.Fatal("config missing worker 'local'")
	}
	if w.Adapter != "opencode" {
		t.Fatalf("local worker adapter = %q, want opencode", w.Adapter)
	}
	if w.Model != "flywheel-local/qwen3-coder:30b" {
		t.Fatalf("local worker model = %q, want flywheel-local/qwen3-coder:30b", w.Model)
	}
}

func TestInitLocalRerunReplaces(t *testing.T) {
	dir := t.TempDir()
	Init(dir, false)

	_, err := InitLocal(dir, "qwen3-coder:30b", DefaultLocalURL)
	if err != nil {
		t.Fatalf("InitLocal first call: %v", err)
	}

	_, err = InitLocal(dir, "m2", "http://127.0.0.1:1234/v1")
	if err != nil {
		t.Fatalf("InitLocal second call: %v", err)
	}

	policyPath := filepath.Join(dir, ".flywheel", "opencode-worker.json")
	policyData, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read policy file: %v", err)
	}

	var policy map[string]any
	if err := json.Unmarshal(policyData, &policy); err != nil {
		t.Fatalf("parse policy: %v", err)
	}

	providers, ok := policy["provider"].(map[string]any)
	if !ok {
		t.Fatal("policy['provider'] is not a map")
	}

	providerBlock, ok := providers[LocalProviderID].(map[string]any)
	if !ok {
		t.Fatalf("provider[%q] is not a map", LocalProviderID)
	}

	baseURL, ok := providerBlock["options"].(map[string]any)["baseURL"].(string)
	if !ok || baseURL != "http://127.0.0.1:1234/v1" {
		t.Fatalf("provider baseURL = %q, want http://127.0.0.1:1234/v1", baseURL)
	}

	models, ok := providerBlock["models"].(map[string]any)
	if !ok {
		t.Fatal("provider['models'] is not a map")
	}
	if models["qwen3-coder:30b"] != nil {
		t.Fatal("provider['models'] should not have 'qwen3-coder:30b' after rerun")
	}
	if models["m2"] == nil {
		t.Fatal("provider['models'] missing 'm2'")
	}

	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	w, ok := cfg.Worker("local")
	if !ok {
		t.Fatal("config missing worker 'local'")
	}
	if w.Model != "flywheel-local/m2" {
		t.Fatalf("local worker model = %q, want flywheel-local/m2", w.Model)
	}

	localCount := 0
	for _, worker := range cfg.Workers {
		if worker.Name == "local" {
			localCount++
		}
	}
	if localCount != 1 {
		t.Fatalf("found %d 'local' workers, want 1", localCount)
	}
}

func TestInitLocalKeepsOtherKeys(t *testing.T) {
	dir := t.TempDir()
	Init(dir, false)

	policyPath := filepath.Join(dir, ".flywheel", "opencode-worker.json")
	prePolicy := map[string]any{
		"permission": map[string]any{
			"bash": map[string]any{
				"*": "allow",
			},
		},
		"theme": "x",
	}

	policyData, err := json.MarshalIndent(prePolicy, "", "  ")
	if err != nil {
		t.Fatalf("encode policy: %v", err)
	}
	policyData = append(policyData, '\n')

	if err := os.WriteFile(policyPath, policyData, 0o644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	_, err = InitLocal(dir, "m1", DefaultLocalURL)
	if err != nil {
		t.Fatalf("InitLocal: %v", err)
	}

	policyData, err = os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read policy file: %v", err)
	}

	var policy map[string]any
	if err := json.Unmarshal(policyData, &policy); err != nil {
		t.Fatalf("parse policy: %v", err)
	}

	theme, ok := policy["theme"].(string)
	if !ok || theme != "x" {
		t.Fatalf("policy['theme'] = %q, want x", theme)
	}
}

func TestInitLocalRejectsBadURL(t *testing.T) {
	dir := t.TempDir()
	Init(dir, false)

	badURLs := []string{"ftp://x", "localhost:11434", ""}
	for _, badURL := range badURLs {
		_, err := InitLocal(dir, "model", badURL)
		if err == nil {
			t.Fatalf("InitLocal(%q) succeeded, want error", badURL)
		}
	}
}

func TestInitLocalRejectsEmptyModel(t *testing.T) {
	dir := t.TempDir()
	Init(dir, false)

	_, err := InitLocal(dir, "", DefaultLocalURL)
	if err == nil {
		t.Fatal("InitLocal with empty model succeeded, want error")
	}
}

// TestInitLocalNullPolicyIsAnError checks that a policy file holding JSON null
// is reported, not a panic (#327 review).
func TestInitLocalNullPolicyIsAnError(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "opencode-worker.json"), []byte("null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InitLocal(dir, "m", DefaultLocalURL); err == nil {
		t.Error("InitLocal with a null policy: no error")
	}
}

// TestInitLocalKeepsTunedConcurrency checks that a rerun keeps a max_parallel
// the operator tuned for the host, changing only the adapter and model.
func TestInitLocalKeepsTunedConcurrency(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := InitLocal(dir, "m1", DefaultLocalURL); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Workers {
		if cfg.Workers[i].Name == "local" {
			cfg.Workers[i].MaxParallel = 2
		}
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := InitLocal(dir, "m2", DefaultLocalURL); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ = LoadConfig(dir)
	w, ok := cfg.Worker("local")
	if !ok || w.MaxParallel != 2 || w.Model != LocalProviderID+"/m2" {
		t.Errorf("local worker after rerun = %+v, want model m2 and max_parallel 2 kept", w)
	}
}
