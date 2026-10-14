package catalog

import (
	"slices"
	"testing"
)

func TestAIProfileMultipleModelsPersist(t *testing.T) {
	s, _ := testStore(t)
	p, err := s.SaveAIProfile(AIProfile{Name: "models", Provider: "model", URL: "http://models.internal/v1", Model: "first", Models: []string{" second ", "first", "second", "third"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := d.AIProfile(p.ID)
	if err != nil || !slices.Equal(saved.ModelChoices(), []string{"first", "second", "third"}) {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if !slices.Equal((AIProfile{Model: "legacy"}).ModelChoices(), []string{"legacy"}) {
		t.Fatal("legacy single model lost")
	}
	p.Models = []string{"bad\nmodel"}
	if _, err := s.SaveAIProfile(p, false); err == nil {
		t.Fatal("invalid model accepted")
	}
	p.Models = []string{"second"}
	p.Provider, p.CredentialID, p.URL = "codex", "", ""
	if _, err := s.SaveAIProfile(p, false); err == nil {
		t.Fatal("CLI profile accepted API model list")
	}
}
