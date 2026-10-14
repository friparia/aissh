package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/externals"
	"git.example.com/ai/wrd-ai-cli/internal/history"
	"git.example.com/ai/wrd-ai-cli/internal/redact"
	"git.example.com/ai/wrd-ai-cli/internal/resources"
)

type resourceTestPTY struct{ bytes.Buffer }

func (*resourceTestPTY) Close() error { return nil }
func TestMalformedResourceProposalNeverReachesSSH(t *testing.T) {
	pty := &resourceTestPTY{}
	a := &App{config: Config{}, ctx: context.Background(), agent: approvalTestProvider{}, atPrompt: true, sshPTY: pty, proposal: &agent.Proposal{ID: "p", Command: "@resource invalid; echo leaked"}}
	a.finishApproval(approvalEvent{command: a.proposal.Command})
	if pty.Len() != 0 || a.execution != nil {
		t.Fatal("resource operation reached SSH shell")
	}
	if a.agentBusy || a.proposal != nil {
		t.Fatal("invalid resource request left UI blocked")
	}
}

type resourceFlowProvider struct {
	approvalTestProvider
	approved bool
	result   agent.Request
}

func (p *resourceFlowProvider) Action(_ context.Context, request agent.Request) (agent.Turn, error) {
	p.approved = request.Event == "approve"
	return agent.Turn{}, nil
}
func (p *resourceFlowProvider) Stream(_ context.Context, request agent.Request, _ agent.Callbacks) (agent.Turn, error) {
	p.result = request
	return agent.Turn{Type: "finished"}, nil
}
func TestGrantedResourceUsesAdapterAndReturnsResultToAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "system-result") }))
	defer server.Close()
	store := catalog.New(t.TempDir(), nil)
	r, err := store.SaveResource(catalog.Resource{Name: "test", Kind: "http", Endpoint: server.URL, AllowLocal: true, Grant: "read"})
	if err != nil {
		t.Fatal(err)
	}
	external := externals.NewStore(store)
	st, err := external.Ensure(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	def := externals.Definition{ID: "read-status", Name: "Read service status", Kind: "http", Lifecycle: "active", InputSchema: externals.EmptyInput(), Request: externals.Request{Method: "GET", Path: "/"}, Assessment: externals.Assessment{Effects: externals.Effects{Access: "read"}, Risk: "low"}}
	st, err = external.SaveOperation(r.ID, st.Revision, def, "review")
	if err != nil {
		t.Fatal(err)
	}
	_, err = external.SavePermissions(r.ID, st.Revision, st.Permissions, []externals.Grant{{Name: "reader", Subject: externals.Subject{Kind: "local"}, Operations: map[string]uint64{def.ID: 1}, Permissions: []string{"read"}, AutoRead: true}}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	provider := &resourceFlowProvider{}
	pty := &resourceTestPTY{}
	a := &App{ctx: context.Background(), config: Config{Resources: resources.New(store, "", "", nil)}, agent: provider, sshPTY: pty, events: make(chan interface{}, 10)}
	op := resources.Operation{ResourceID: r.ID, Action: "system.invoke", OperationID: def.ID, OperationRevision: 1, Arguments: map[string]any{}}
	a.executeResource(op.Command(), "proposal", "session", false)
	select {
	case event := <-a.events:
		result, ok := unwrapAgentTestEvent(event).(executionResultEvent)
		if !ok || result.err != nil {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resource result timed out")
	}
	if !provider.approved || provider.result.Event != "result" || !strings.Contains(provider.result.Output, "system-result") {
		t.Fatal("provider did not receive approved result")
	}
	if pty.Len() != 0 {
		t.Fatal("structured resource operation reached SSH shell")
	}
}
func TestRequestsAndHistoryScrubKnownSecrets(t *testing.T) {
	filter := redact.New("saved-secret-value")
	a := &App{config: Config{SecretFilter: filter}, historyStore: history.New(t.TempDir()), historyTarget: "host"}
	request := agent.Request{Prompt: "saved-secret-value", RecentOutput: "split-" + "saved-secret-value", Output: "saved-secret-value", SystemContext: map[string]string{"conversation_history": "saved-secret-value"}}
	a.prepareAgentRequest(&request)
	if strings.Contains(request.Prompt+request.RecentOutput+request.Output+request.SystemContext["conversation_history"], "saved-secret-value") {
		t.Fatal("secret in model request")
	}
	a.persistChatMessage(chatMessage{Role: "assistant", Content: "saved-secret-value", Proposal: &agent.Proposal{Command: "echo saved-secret-value", Explanation: "saved-secret-value"}, Time: time.Now()})
	messages, err := a.historyStore.Load("host")
	if err != nil || len(messages) != 1 {
		t.Fatal(err)
	}
	if strings.Contains(messages[0].Content+messages[0].Proposal.Command+messages[0].Proposal.Explanation, "saved-secret-value") {
		t.Fatal("secret in persisted history")
	}
}
