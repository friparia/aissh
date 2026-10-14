package main

import (
	"testing"

	"git.example.com/ai/wrd-ai-cli/internal/app"
	"git.example.com/ai/wrd-ai-cli/internal/catalog"
)

func TestManagedAISelectionOverridesDefault(t *testing.T) {
	t.Setenv("AISSH_CONFIG_DIR", t.TempDir())
	store := catalog.Default()
	first, err := store.SaveAIProfile(catalog.AIProfile{Name: "default", Provider: "model", URL: "http://default.internal/v1", Model: "first"}, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SaveAIProfile(catalog.AIProfile{Name: "selected", Provider: "model", URL: "http://selected.internal/v1", Model: "second", Models: []string{"third"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"", second.ID, catalog.BuiltinCodexProfileID} {
		config := app.Config{AIProfileID: selected}
		cleanup, err := prepareManaged(&config)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		expected := selected
		if expected == "" {
			expected = first.ID
		}
		if config.AIProfileID != expected {
			t.Fatalf("profile=%q want=%q", config.AIProfileID, expected)
		}
		if selected == second.ID && (len(config.ModelOptions) != 2 || config.ModelOptions[1] != "third") {
			t.Fatal("saved model choices were not passed to the conversation")
		}
		if selected == catalog.BuiltinCodexProfileID {
			if config.AgentProvider != "codex" {
				t.Fatal("builtin Codex did not override custom default")
			}
		} else if config.AgentProvider != "model" || (selected == second.ID && (config.ModelURL != second.URL || config.ModelName != second.Model)) {
			t.Fatalf("selected model was not applied: %+v", config)
		}
	}
}
