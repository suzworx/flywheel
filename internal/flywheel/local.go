package flywheel

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// LocalProviderID is the OpenCode provider InitLocal writes (issue #44).
const LocalProviderID = "flywheel-local"

// DefaultLocalURL is Ollama's OpenAI-compatible endpoint.
const DefaultLocalURL = "http://localhost:11434/v1"

// InitLocal points OpenCode workers at a local OpenAI-compatible server: it
// merges a provider block {"npm": "@ai-sdk/openai-compatible", "name": "Local
// (flywheel)", "options": {"baseURL": baseURL}, "models": {model: {"name":
// model}}} under "provider"."flywheel-local" into .flywheel/opencode-worker.json
// (writing the embedded worker permission policy first when the file is
// missing, and keeping every other key), and adds — or updates the model of —
// a worker named "local" (adapter opencode, model "flywheel-local/<model>",
// max_parallel 1) in .flywheel/config.json. baseURL must be http or https with a
// host; model must be non-empty. It returns one piece per file.
func InitLocal(dir, model, baseURL string) ([]ScaffoldPiece, error) {
	if model == "" {
		return nil, fmt.Errorf("model must be non-empty")
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse url %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url must have a host")
	}

	// Read the config before writing anything, so a bad config leaves both
	// files untouched (#327 review).
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}

	dotFlywheel := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dotFlywheel, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dotFlywheel, err)
	}

	workerJSONPath := filepath.Join(dotFlywheel, "opencode-worker.json")
	workerJSONAdded := false

	b, err := os.ReadFile(workerJSONPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", workerJSONPath, err)
		}
		b = []byte(workerPermissionPolicy)
		workerJSONAdded = true
	}
	prevPolicy := b // restored when the config write fails (#327 review)

	var policy map[string]any
	if err := json.Unmarshal(b, &policy); err != nil {
		return nil, fmt.Errorf("parse %s: %w", workerJSONPath, err)
	}
	if policy == nil {
		return nil, fmt.Errorf("parse %s: the top level must be a JSON object", workerJSONPath)
	}

	if policy["provider"] == nil {
		policy["provider"] = make(map[string]any)
	}
	providerMap, ok := policy["provider"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse %s: provider must be an object", workerJSONPath)
	}

	providerMap[LocalProviderID] = map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "Local (flywheel)",
		"options": map[string]any{
			"baseURL": baseURL,
		},
		"models": map[string]any{
			model: map[string]any{
				"name": model,
			},
		},
	}

	b, err = json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", workerJSONPath, err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(dotFlywheel, ".opencode-worker.json.tmp*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, workerJSONPath); err != nil {
		return nil, fmt.Errorf("rename %s to %s: %w", tmpName, workerJSONPath, err)
	}

	modelStr := LocalProviderID + "/" + model
	found := false
	for i := range cfg.Workers {
		if cfg.Workers[i].Name == "local" {
			cfg.Workers[i].Adapter = "opencode"
			cfg.Workers[i].Model = modelStr
			found = true
			break
		}
	}
	if !found {
		cfg.Workers = append(cfg.Workers, Worker{
			Name:        "local",
			Adapter:     "opencode",
			Model:       modelStr,
			MaxParallel: 1,
		})
	}

	if err := WriteConfig(dir, cfg); err != nil {
		// Not half-applied: put the policy back as it was (#327 review).
		if workerJSONAdded {
			_ = os.Remove(workerJSONPath)
		} else {
			_ = os.WriteFile(workerJSONPath, prevPolicy, 0o644)
		}
		return nil, err
	}

	pieces := []ScaffoldPiece{
		{Path: ".flywheel/opencode-worker.json", Added: workerJSONAdded},
		{Path: ".flywheel/config.json", Added: !found},
	}
	return pieces, nil
}
