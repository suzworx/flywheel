package flywheel

import (
	"strings"
	"testing"
)

// TestModelCatalog checks CheckModel per adapter: claude's known IDs and
// aliases pass, "haiku" (no CLI alias) and a typo fail naming the closest
// known ID; opencode needs <provider>/<model>; codex needs a model (issue
// #275).
func TestModelCatalog(t *testing.T) {
	t.Parallel()
	cases := []struct {
		adapter, model string
		ok             bool
		want           string // substring of the error
	}{
		{"claude", "claude-fable-5-1", true, ""},
		{"claude", "claude-opus-5-5", true, ""},
		{"claude", "claude-sonnet-5", true, ""},
		{"claude", "claude-haiku-4-5-20251001", true, ""},
		{"claude", "fable", true, ""},
		{"claude", "opus", true, ""},
		{"claude", "sonnet", true, ""},
		{"claude", "haiku", false, `did you mean "claude-haiku-4-5-20251001"`},
		{"claude", "claude-haiku-4-6", false, `did you mean "claude-haiku-4-5-20251001"`},
		{"opencode", "deepseek", false, "<provider>/<model>"},
		{"opencode", "open router/x", false, "<provider>/<model>"},
		{"opencode", "openrouter/x", true, ""},
		{"codex", "", false, "must not be empty"},
		{"codex", "gpt 5", false, "spaces"},
		{"codex", "gpt-5-codex", true, ""},
		{"pi", "anthropic/claude-sonnet-5", true, ""},
		{"pi", "anthropic/claude-sonnet-5:high", true, ""},
		{"pi", "openai/gpt-5", true, ""},
		{"pi", "claude-sonnet-5", false, "<provider>/<id>"},
		{"pi", "/claude-sonnet-5", false, "<provider>/<id>"},
		{"pi", "anthropic/", false, "<provider>/<id>"},
		{"pi", "anthropic/:high", false, "<provider>/<id>"},
		{"pi", "anthropic/claude sonnet", false, "<provider>/<id>"},
		{"pi", "", false, "must not be empty"},
		{"sim", "testdata/script.jsonl", true, ""},
	}
	for _, tc := range cases {
		err := CheckModel(tc.adapter, tc.model)
		if tc.ok {
			if err != nil {
				t.Errorf("CheckModel(%s, %q) = %v, want nil", tc.adapter, tc.model, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CheckModel(%s, %q) = %v, want an error containing %q", tc.adapter, tc.model, err, tc.want)
		}
	}
	err := CheckModel("claude", "claude-haiku-4-6")
	if err == nil || !strings.Contains(err.Error(), "known: claude-fable-5-1, claude-opus-5-5, claude-sonnet-5, claude-haiku-4-5-20251001, fable, opus, sonnet") {
		t.Errorf("CheckModel(claude, typo) = %v, want the full known list", err)
	}
}
