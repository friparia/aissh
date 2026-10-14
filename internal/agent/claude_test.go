package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.example.com/ai/wrd-ai-cli/internal/i18n"
)

func claudeResultJSON(sessionID, structured string) []byte {
	return []byte(`{"type":"result","subtype":"success","is_error":false,"session_id":"` + sessionID + `","result":"ok","structured_output":` + structured + `}` + "\n")
}

func TestClaudeProviderReturnsProposalAndPanel(t *testing.T) {
	provider := testClaudeProvider(t, `{"type":"command_proposal","message":"先查看负载。","proposal":{"id":"","command":"uptime","explanation":"检查系统负载","expectedEffect":"输出负载均值","risk":"read_only"},"panelActions":[{"action":"open","panel":{"id":"load","type":"monitor","title":"负载","collector":"system.overview","command":"","intervalMs":3000,"content":"","allowAIRead":true}}]}`)
	var action PanelAction
	turn, err := provider.Stream(context.Background(), Request{
		Event:  "start",
		Prompt: "帮我看看负载",
		Target: &Target{HostLabel: "root@server", Protocol: "ssh"},
	}, Callbacks{OnPanelAction: func(value PanelAction) { action = value }})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Session.ID == "" || turn.Proposal == nil || turn.Proposal.Command != "uptime" {
		t.Fatalf("turn = %#v", turn)
	}
	if turn.Proposal.ID == "" || action.Panel.ID != "load" {
		t.Fatalf("proposal=%#v action=%#v", turn.Proposal, action)
	}

	approved, err := provider.Action(context.Background(), Request{
		Event: "approve", SessionID: turn.Session.ID, ProposalID: turn.Proposal.ID,
	})
	if err != nil || approved.Type != "approved" {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
}

func TestClaudeProviderCarriesExecutionResult(t *testing.T) {
	provider := testClaudeProvider(t, `{"type":"finished","message":"负载正常。","proposal":null,"panelActions":[]}`)
	provider.mu.Lock()
	provider.sessions["s1"] = &codexSession{
		ID: "s1",
		Proposals: map[string]Proposal{
			"p1": {ID: "p1", Command: "uptime"},
		},
	}
	provider.mu.Unlock()
	turn, err := provider.Stream(context.Background(), Request{
		Event: "result", SessionID: "s1", ProposalID: "p1", Output: "load average: 0.10", Completion: "prompt",
	}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Message != "负载正常。" {
		t.Fatalf("turn = %#v", turn)
	}
}

func TestParseClaudeResultStructuredOutput(t *testing.T) {
	response, sessionID, err := parseClaudeInvocationJSONLocalized(
		claudeResultJSON("session-1", `{"type":"finished","message":"完成","proposal":null,"panelActions":[]}`), i18n.New(""))
	if err != nil || response.Message != "完成" || sessionID != "session-1" {
		t.Fatalf("response=%#v session=%q err=%v", response, sessionID, err)
	}
}

func TestParseClaudeResultFallsBackToResultText(t *testing.T) {
	payload := `{"type":"result","subtype":"success","is_error":false,"session_id":"session-2","result":"` + "```json\\n{\\\"type\\\":\\\"finished\\\",\\\"message\\\":\\\"完成\\\",\\\"proposal\\\":null,\\\"panelActions\\\":[]}\\n```" + `"}`
	response, sessionID, err := parseClaudeInvocationJSONLocalized([]byte(payload), i18n.New(""))
	if err != nil || response.Message != "完成" || sessionID != "session-2" {
		t.Fatalf("response=%#v session=%q err=%v", response, sessionID, err)
	}
}

func TestParseClaudeResultReportsErrorTurn(t *testing.T) {
	payload := `{"type":"result","subtype":"error_max_turns","is_error":true,"session_id":"session-3","result":"budget exhausted"}`
	_, sessionID, err := parseClaudeInvocationJSONLocalized([]byte(payload), i18n.New("en"))
	if err == nil || sessionID != "session-3" || !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("session=%q err=%v", sessionID, err)
	}
}

func TestClaudeAuthErrorIsSanitized(t *testing.T) {
	err := claudeRunErrorLocalized(errors.New("exit status 1"), nil, []byte("Invalid API key: sk-secret-value. Please run /login"), i18n.New(""))
	if strings.Contains(err.Error(), "sk-secret-value") || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("error = %q", err)
	}
}

func TestClaudeProviderLocalizesStatusAndErrors(t *testing.T) {
	provider := NewClaudeProvider("missing-claude", "")
	provider.Language = "en"
	provider.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if _, err := provider.Stream(context.Background(), Request{Event: "start"}, Callbacks{}); err == nil || !strings.Contains(err.Error(), "Local Claude CLI") {
		t.Fatalf("localized missing CLI error=%v", err)
	}

	provider = testClaudeProvider(t, `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`)
	provider.Language = "en"
	var status string
	_, err := provider.Stream(context.Background(), Request{
		Event: "start", Prompt: "check", SystemContext: map[string]string{"ui_language": "en"},
	}, Callbacks{OnStatus: func(value string) { status = value }})
	if err != nil || status != "Calling local Claude" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestClaudeProviderUsesReadOnlyLocalWorkspace(t *testing.T) {
	workspace := t.TempDir()
	provider := NewClaudeProvider("claude", "")
	provider.Workspace = workspace
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	provider.run = func(_ context.Context, _ string, args []string, dir, prompt string) ([]byte, []byte, error) {
		if dir != workspace {
			t.Fatalf("dir=%q want=%q", dir, workspace)
		}
		if !containsArgs(args, "--allowedTools", claudeReadOnlyTools) || !containsArgs(args, "--permission-mode", "dontAsk") || !containsArgs(args, "--disallowedTools", claudeDisallowedTools) {
			t.Fatalf("workspace args=%#v", args)
		}
		if containsArgs(args, "--dangerously-skip-permissions") {
			t.Fatalf("unsafe Claude args: %#v", args)
		}
		for _, want := range []string{"local_workspace=" + workspace, "本机工作区严格只读", "command_proposal 永远表示将在目标服务器", "不得使用 exit、logout", "命令按原文直接输入"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("workspace prompt missing %q: %s", want, prompt)
			}
		}
		return claudeResultJSON("session-workspace", `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`), nil, nil
	}
	if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "check this deployment"}, Callbacks{}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeProviderCanUseWritableLocalWorkspace(t *testing.T) {
	workspace := t.TempDir()
	provider := NewClaudeProvider("claude", "")
	provider.SetLocalWorkspaceWritable(true)
	provider.SetLocalWorkspace(workspace)
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	provider.run = func(_ context.Context, _ string, args []string, dir, prompt string) ([]byte, []byte, error) {
		if dir != workspace {
			t.Fatalf("dir=%q want=%q", dir, workspace)
		}
		if !containsArgs(args, "--tools", claudeWritableTools) || !containsArgs(args, "--allowedTools", claudeWritableTools) || !containsArgs(args, "--disallowedTools", claudeWritableDisallowedTools) {
			t.Fatalf("workspace args=%#v", args)
		}
		if strings.Contains(strings.Join(args, ","), "dangerously-skip-permissions") {
			t.Fatalf("unsafe Claude args: %#v", args)
		}
		for _, want := range []string{"local_workspace=" + workspace, "local_workspace_mode=read-write", "用户已显式开启本机工作区写入", "修改必须限制在配置的工作区内", "command_proposal 永远表示将在目标服务器"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("writable workspace prompt missing %q: %s", want, prompt)
			}
		}
		return claudeResultJSON("session-writable-workspace", `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`), nil, nil
	}
	if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "fix the local project"}, Callbacks{}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeProviderResumesOneSessionForOneSSHSession(t *testing.T) {
	provider := NewClaudeProvider("claude", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, _, prompt string) ([]byte, []byte, error) {
		calls++
		switch calls {
		case 1:
			if containsArgs(args, "--resume") || !containsArgs(args, "--output-format", "json") || !containsArgs(args, "--json-schema", codexResponseSchema) {
				t.Fatalf("initial args=%#v", args)
			}
			if !strings.Contains(prompt, "不能直接操作目标服务器") {
				t.Fatalf("initial prompt=%s", prompt)
			}
			return claudeResultJSON("session-ssh-1", `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`), nil, nil
		case 2:
			if !containsArgs(args, "--resume", "session-ssh-1") {
				t.Fatalf("resume args=%#v", args)
			}
			if strings.Contains(prompt, "此前对话") || strings.Contains(prompt, "不能直接操作目标服务器") || !strings.Contains(prompt, "用户请求：继续检查") {
				t.Fatalf("resume prompt replayed the session instead of sending a delta: %s", prompt)
			}
			return claudeResultJSON("session-ssh-1", `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`), nil, nil
		default:
			t.Fatalf("unexpected call %d", calls)
			return nil, nil, nil
		}
	}

	first, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "检查服务"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Session.ID != "session-ssh-1" {
		t.Fatalf("initial session=%q", first.Session.ID)
	}
	second, err := provider.Stream(context.Background(), Request{
		Event: "user_message", SessionID: first.Session.ID, Prompt: "继续检查",
	}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Session.ID != first.Session.ID || calls != 2 {
		t.Fatalf("first=%q second=%q calls=%d", first.Session.ID, second.Session.ID, calls)
	}
}

func TestClaudeProviderAdoptsRotatedSessionID(t *testing.T) {
	provider := NewClaudeProvider("claude", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, _, _ string) ([]byte, []byte, error) {
		calls++
		switch calls {
		case 1:
			return claudeResultJSON("session-old", `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`), nil, nil
		case 2:
			if !containsArgs(args, "--resume", "session-old") {
				t.Fatalf("resume args=%#v", args)
			}
			return claudeResultJSON("session-new", `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`), nil, nil
		default:
			if !containsArgs(args, "--resume", "session-new") {
				t.Fatalf("rotated resume args=%#v", args)
			}
			return claudeResultJSON("session-new", `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`), nil, nil
		}
	}

	first, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "第一次"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Stream(context.Background(), Request{Event: "user_message", SessionID: first.Session.ID, Prompt: "继续"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Session.ID != "session-new" {
		t.Fatalf("rotated session=%q", second.Session.ID)
	}
	if _, err := provider.Stream(context.Background(), Request{Event: "user_message", SessionID: "session-new", Prompt: "再继续"}, Callbacks{}); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	_, oldExists := provider.sessions["session-old"]
	sessionCount := len(provider.sessions)
	provider.mu.Unlock()
	if oldExists || sessionCount != 1 || calls != 3 {
		t.Fatalf("oldExists=%v sessions=%d calls=%d", oldExists, sessionCount, calls)
	}
}

func TestClaudeProviderSendsApprovalAndResultToResumedSession(t *testing.T) {
	provider := NewClaudeProvider("claude", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, _, prompt string) ([]byte, []byte, error) {
		calls++
		if calls == 1 {
			return claudeResultJSON("session-approval", `{"type":"command_proposal","message":"查看负载","proposal":{"id":"proposal-1","command":"uptime","explanation":"检查负载","expectedEffect":"输出负载","risk":"read_only"},"panelActions":[]}`), nil, nil
		}
		if !containsArgs(args, "--resume", "session-approval") || !strings.Contains(prompt, "用户批准执行命令：uptime") || !strings.Contains(prompt, "远端命令执行结果") {
			t.Fatalf("resume args=%#v prompt=%s", args, prompt)
		}
		return claudeResultJSON("session-approval", `{"type":"finished","message":"负载正常","proposal":null,"panelActions":[]}`), nil, nil
	}

	first, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "检查负载"}, Callbacks{})
	if err != nil || first.Proposal == nil {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	if _, err := provider.Action(context.Background(), Request{
		Event: "approve", SessionID: first.Session.ID, ProposalID: first.Proposal.ID,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := provider.Stream(context.Background(), Request{
		Event: "result", SessionID: first.Session.ID, ProposalID: first.Proposal.ID,
		Output: "load average: 0.10", Completion: "prompt",
	}, Callbacks{})
	if err != nil || result.Message != "负载正常" || calls != 2 {
		t.Fatalf("result=%#v calls=%d err=%v", result, calls, err)
	}
}

func TestClaudeProviderRecreatesMissingSessionWithHistory(t *testing.T) {
	provider := NewClaudeProvider("claude", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	provider.sessions["missing-session"] = &codexSession{
		ID:        "missing-session",
		History:   []codexHistory{{Role: "assistant", Content: "此前发现 nginx 超时"}},
		Proposals: make(map[string]Proposal),
	}
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, _, prompt string) ([]byte, []byte, error) {
		calls++
		if calls == 1 {
			if !containsArgs(args, "--resume", "missing-session") {
				t.Fatalf("missing-session call did not resume: %#v", args)
			}
			return nil, []byte("No conversation found with session ID: missing-session"), errors.New("exit status 1")
		}
		if containsArgs(args, "--resume") || !strings.Contains(prompt, "此前发现 nginx 超时") {
			t.Fatalf("replacement call args=%#v prompt=%s", args, prompt)
		}
		return claudeResultJSON("replacement-session", `{"type":"finished","message":"continued","proposal":null,"panelActions":[]}`), nil, nil
	}

	turn, err := provider.Stream(context.Background(), Request{
		Event: "user_message", SessionID: "missing-session", Prompt: "继续",
	}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Session.ID != "replacement-session" || calls != 2 {
		t.Fatalf("turn=%#v calls=%d", turn, calls)
	}
}

func TestClaudeProviderCloseRejectsFurtherStreams(t *testing.T) {
	provider := testClaudeProvider(t, `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`)
	if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "检查"}, Callbacks{}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "再次检查"}, Callbacks{}); err == nil || !strings.Contains(err.Error(), "会话已关闭") {
		t.Fatalf("closed stream error=%v", err)
	}
}

func TestClaudeProviderWorkspaceCanBeUpdatedBetweenTurns(t *testing.T) {
	provider := NewClaudeProvider("claude", "")
	workspace := t.TempDir()
	provider.SetLocalWorkspace(workspace)
	if got := provider.LocalWorkspace(); got != workspace {
		t.Fatalf("workspace=%q want=%q", got, workspace)
	}
	provider.SetLocalWorkspace("")
	if got := provider.LocalWorkspace(); got != "" {
		t.Fatalf("disabled workspace=%q", got)
	}
}

func testClaudeProvider(t *testing.T, response string) *ClaudeProvider {
	t.Helper()
	provider := NewClaudeProvider("claude", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	provider.run = func(_ context.Context, _ string, args []string, _, prompt string) ([]byte, []byte, error) {
		resuming := containsArgs(args, "--resume")
		if !resuming {
			if !strings.Contains(prompt, "不能直接操作目标服务器") {
				t.Fatalf("missing safety instruction: %s", prompt)
			}
			if !strings.Contains(prompt, "不要调用任何本机工具") {
				t.Fatalf("unconfigured workspace unexpectedly enabled local tools: %s", prompt)
			}
		}
		if !containsArgs(args, "--permission-mode", "dontAsk") || !containsArgs(args, "--disallowedTools", claudeDisallowedTools+","+claudeReadOnlyTools) {
			t.Fatalf("unsafe Claude args: %#v", args)
		}
		if containsArgs(args, "--allowedTools") || containsArgs(args, "--dangerously-skip-permissions") {
			t.Fatalf("unsafe Claude args: %#v", args)
		}
		sessionID := "session-test"
		return claudeResultJSON(sessionID, response), nil, nil
	}
	return provider
}
