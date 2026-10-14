package app

import (
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"git.example.com/ai/wrd-ai-cli/internal/history"
)

func TestConversationModelSwitchPersistsAndRestores(t *testing.T) {
	directory := t.TempDir()
	id := history.NewConversationID()
	if err := history.New(directory).Append("server", history.Message{ConversationID: id, Role: "user", Content: "earlier question", Time: time.Now().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	config := Config{DesktopUI: true, AgentProvider: "model", ModelURL: "http://models.internal/v1", ModelName: "first", ModelOptions: []string{"first", "second"}, SSHArgs: []string{"server"}, HistoryDir: directory, ConversationID: id}
	a, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.cancel)
	provider := a.agent.(*agent.DirectModelProvider)
	command := credentialui.WorkbenchCommand{Kind: "model", Text: "second", Deadline: time.Now().Add(time.Second).UnixMilli()}
	a.agentBusy = true
	if err := a.handleDesktopCommand(command); err == nil || provider.CurrentModel() != "first" {
		t.Fatal("busy model changed")
	}
	a.agentBusy = false
	a.proposal = &agent.Proposal{ID: "pending"}
	if err := a.handleDesktopCommand(command); err == nil {
		t.Fatal("model changed with pending proposal")
	}
	a.proposal = nil
	if err := a.switchConversationModel("unknown"); err == nil {
		t.Fatal("unconfigured model accepted")
	}
	if err := a.handleDesktopCommand(command); err != nil {
		t.Fatal(err)
	}
	if a.agent != provider || a.historyConversationID != id || a.desktopState().Model != "second" {
		t.Fatal("switch replaced provider, conversation or model")
	}
	b, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.cancel)
	if b.config.ModelName != "second" || b.desktopState().Model != "second" {
		t.Fatal("selected model was not restored")
	}
	config.ModelOptions = []string{"first"}
	c, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.cancel)
	if c.desktopState().Model != "first" {
		t.Fatal("removed model did not fall back to current default")
	}
}
