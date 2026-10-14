package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func TestDirectModelSwitchUsesNewModelAndKeepsContext(t *testing.T) {
	requests := make(chan directChatRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body directChatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Header.Get("Authorization") != "Bearer same-token" {
			t.Error("credential changed")
		}
		requests <- body
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"type":"finished","message":"first answer","proposal":null,"panelActions":[]}`}}}})
	}))
	defer server.Close()
	provider := NewDirectModelProvider("Model", server.URL, "same-token", "first", true)
	turn, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "earlier question"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	provider.SetModel("second")
	next, err := provider.Stream(context.Background(), Request{Event: "message", SessionID: turn.Session.ID, Prompt: "follow up"}, Callbacks{})
	if err != nil || next.Session.ID != turn.Session.ID {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	first, second := <-requests, <-requests
	if first.Model != "first" || second.Model != "second" || !strings.Contains(second.Messages[0].Content, "earlier question") || !strings.Contains(second.Messages[0].Content, "first answer") {
		t.Fatalf("requests lost model or context: %+v %+v", first, second)
	}
}

func TestDirectModelProviderCallsHTTPService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"type":"finished","message":"HTTP works","proposal":null,"panelActions":[]}`}}}})
	}))
	defer server.Close()
	provider := NewDirectModelProvider("Model", server.URL+"/v1", "test-token", "custom-model", true)
	turn, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "test"}, Callbacks{})
	if err != nil || turn.Message != "HTTP works" {
		t.Fatalf("turn=%+v err=%v", turn, err)
	}
}

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDirectModelProviderReturnsStructuredProposalAndPanel(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/chat/completions" {
			t.Fatalf("path=%q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		var body directChatRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "deepseek-test" || body.ResponseFormat["type"] != "json_object" || len(body.Messages) != 1 {
			t.Fatalf("body=%#v", body)
		}
		for _, want := range []string{"JSON Schema", "不能直接操作目标服务器", "SSH 最近输出", "不要调用任何本机工具", "不得使用 exit、logout", "命令按原文直接输入"} {
			if !strings.Contains(body.Messages[0].Content, want) {
				t.Fatalf("prompt missing %q: %s", want, body.Messages[0].Content)
			}
		}
		content := `{"type":"command_proposal","message":"先查看负载。","proposal":{"id":"","command":"uptime","explanation":"检查负载","expectedEffect":"输出负载","risk":"read_only"},"panelActions":[{"action":"open","panel":{"id":"load","type":"monitor","title":"负载","collector":"system.overview","command":"","intervalMs":3000,"content":"","allowAIRead":true}}]}`
		responseData, err := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(responseData))),
		}, nil
	})

	provider := NewDirectModelProvider("DeepSeek", "https://model.example", "secret", "deepseek-test", true)
	provider.HTTP = &http.Client{Transport: transport}
	var panel PanelAction
	turn, err := provider.Stream(context.Background(), Request{
		Event: "start", Prompt: "检查服务器", RecentOutput: "load average: 1.2",
	}, Callbacks{OnPanelAction: func(action PanelAction) { panel = action }})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Session.ID == "" || turn.Proposal == nil || turn.Proposal.ID == "" || turn.Proposal.Command != "uptime" || panel.Panel.ID != "load" {
		t.Fatalf("turn=%#v panel=%#v", turn, panel)
	}
	approved, err := provider.Action(context.Background(), Request{
		Event: "approve", SessionID: turn.Session.ID, ProposalID: turn.Proposal.ID,
	})
	if err != nil || approved.Type != "approved" {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
}

func TestDirectModelProviderRequiresDeepSeekTokenAndSanitizesErrors(t *testing.T) {
	provider := NewDirectModelProvider("DeepSeek", "https://api.deepseek.com", "", "deepseek-v4-pro", true)
	if provider.Configured() {
		t.Fatal("provider unexpectedly configured without token")
	}
	if _, err := provider.Stream(context.Background(), Request{Event: "start"}, Callbacks{}); err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("missing token error=%v", err)
	}

	provider = NewDirectModelProvider("Model", "https://model.example", "secret-value", "model", false)
	provider.HTTP = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid key secret-value"}}`)),
		}, nil
	})}
	_, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "check"}, Callbacks{})
	if err == nil || strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("sanitized error=%v", err)
	}
}
