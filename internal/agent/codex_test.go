package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.example.com/ai/wrd-ai-cli/internal/i18n"
)

func TestCodexProviderReturnsProposalAndPanel(t *testing.T) {
	provider := testCodexProvider(t, `{"type":"command_proposal","message":"先查看负载。","proposal":{"id":"","command":"uptime","explanation":"检查系统负载","expectedEffect":"输出负载均值","risk":"read_only"},"panelActions":[{"action":"open","panel":{"id":"load","type":"monitor","title":"负载","collector":"system.overview","command":"","intervalMs":3000,"content":"","allowAIRead":true}}]}`)
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

func TestCodexProviderCarriesExecutionResult(t *testing.T) {
	provider := testCodexProvider(t, `{"type":"finished","message":"负载正常。","proposal":null,"panelActions":[]}`)
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

func TestParseCodexJSONL(t *testing.T) {
	response, err := parseCodexJSONL([]byte(
		"{\"type\":\"thread.started\",\"thread_id\":\"t1\"}\n" +
			"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"type\\\":\\\"finished\\\",\\\"message\\\":\\\"完成\\\",\\\"proposal\\\":null,\\\"panelActions\\\":[]}\"}}\n",
	))
	if err != nil || response.Message != "完成" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestParseCodexJSONLCapturesThreadID(t *testing.T) {
	response, threadID, err := parseCodexInvocationJSONLLocalized([]byte(
		"{\"type\":\"thread.started\",\"thread_id\":\"thread-123\"}\n"+
			"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"{\\\"type\\\":\\\"finished\\\",\\\"message\\\":\\\"完成\\\",\\\"proposal\\\":null,\\\"panelActions\\\":[]}\"}}\n",
	), i18n.New(""))
	if err != nil || response.Message != "完成" || threadID != "thread-123" {
		t.Fatalf("response=%#v thread=%q err=%v", response, threadID, err)
	}
}

func TestCodexAuthErrorIsSanitized(t *testing.T) {
	err := codexRunError(errors.New("exit status 1"), nil, []byte("401 Unauthorized: Incorrect API key provided: secret-value"))
	if strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("error = %q", err)
	}
}

func TestCodexProviderLocalizesStatusAndErrors(t *testing.T) {
	provider := NewCodexProvider("missing-codex", "")
	provider.Language = "en"
	provider.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if _, err := provider.Stream(context.Background(), Request{Event: "start"}, Callbacks{}); err == nil || !strings.Contains(err.Error(), "Local Codex CLI") {
		t.Fatalf("localized missing CLI error=%v", err)
	}

	provider = testCodexProvider(t, `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`)
	provider.Language = "en"
	var status string
	_, err := provider.Stream(context.Background(), Request{
		Event: "start", Prompt: "check", SystemContext: map[string]string{"ui_language": "en"},
	}, Callbacks{OnStatus: func(value string) { status = value }})
	if err != nil || status != "Calling local Codex" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	localized := codexRunErrorLocalized(errors.New("exit status 1"), nil, []byte("401 Unauthorized"), i18n.New("en"))
	if !strings.Contains(localized.Error(), "authentication failed") {
		t.Fatalf("localized auth error=%q", localized)
	}
}

func TestCodexPromptFollowsQuestionLanguageInsteadOfUILanguage(t *testing.T) {
	provider := testCodexProvider(t, `{"type":"finished","message":"可以，先检查服务状态。","proposal":null,"panelActions":[]}`)
	provider.Language = "en"
	provider.run = func(_ context.Context, _ string, _ []string, prompt string) ([]byte, []byte, error) {
		if !strings.Contains(prompt, "本轮用户使用中文提问") || !strings.Contains(prompt, "response_language=zh-CN") {
			t.Fatalf("prompt did not select Chinese response: %s", prompt)
		}
		response := `{"type":"finished","message":"可以，先检查服务状态。","proposal":null,"panelActions":[]}`
		line := `{"type":"thread.started","thread_id":"thread-language"}` + "\n" +
			`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"
		return []byte(line), nil, nil
	}
	_, err := provider.Stream(context.Background(), Request{
		Event: "start", Prompt: "帮我检查 nginx", SystemContext: map[string]string{"ui_language": "en"},
	}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCodexPromptContinuesSelectedLocalHistory(t *testing.T) {
	prompt := buildCodexPrompt(Request{
		Prompt: "继续查",
		SystemContext: map[string]string{
			"conversation_history": "用户：检查 nginx\n\n助手：发现上游超时",
		},
	}, nil, "用户请求：继续查")
	for _, want := range []string{"继续一个本机历史会话", "用户：检查 nginx", "助手：发现上游超时", "不要声称仍连接着旧的运行时 Agent Session"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("history prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "conversation_history=") {
		t.Fatalf("history context leaked into generic environment block: %s", prompt)
	}
}

func TestCodexProviderUsesReadOnlyLocalWorkspace(t *testing.T) {
	workspace := t.TempDir()
	provider := NewCodexProvider("codex", "")
	provider.Workspace = workspace
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	provider.run = func(_ context.Context, _ string, args []string, prompt string) ([]byte, []byte, error) {
		if !containsArgs(args, "-C", workspace) || !containsArgs(args, "--sandbox", "read-only") {
			t.Fatalf("workspace args=%#v", args)
		}
		if containsArgs(args, "--ephemeral") {
			t.Fatalf("persistent Codex session unexpectedly used --ephemeral: %#v", args)
		}
		for _, want := range []string{"local_workspace=" + workspace, "本机工作区严格只读", "command_proposal 永远表示将在目标服务器", "不得使用 exit、logout", "命令按原文直接输入"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("workspace prompt missing %q: %s", want, prompt)
			}
		}
		response := `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`
		line := `{"type":"thread.started","thread_id":"thread-workspace"}` + "\n" +
			`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"
		return []byte(line), nil, nil
	}
	if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "check this deployment"}, Callbacks{}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexProviderCanUseWritableLocalWorkspace(t *testing.T) {
	workspace := t.TempDir()
	provider := NewCodexProvider("codex", "")
	provider.SetLocalWorkspaceWritable(true)
	provider.SetLocalWorkspace(workspace)
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	provider.run = func(_ context.Context, _ string, args []string, prompt string) ([]byte, []byte, error) {
		if !containsArgs(args, "-C", workspace) || !containsArgs(args, "--sandbox", "workspace-write") {
			t.Fatalf("workspace args=%#v", args)
		}
		if containsArgs(args, "--sandbox", "read-only") {
			t.Fatalf("writable workspace remained read-only: %#v", args)
		}
		for _, want := range []string{"local_workspace=" + workspace, "local_workspace_mode=read-write", "用户已显式开启本机工作区写入", "修改必须限制在配置的工作区内", "command_proposal 永远表示将在目标服务器"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("writable workspace prompt missing %q: %s", want, prompt)
			}
		}
		response := `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`
		line := `{"type":"thread.started","thread_id":"thread-writable-workspace"}` + "\n" +
			`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"
		return []byte(line), nil, nil
	}
	if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "fix the local project"}, Callbacks{}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexProviderResumesOneThreadForOneSSHSession(t *testing.T) {
	provider := NewCodexProvider("codex", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, prompt string) ([]byte, []byte, error) {
		calls++
		response := `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`
		switch calls {
		case 1:
			if !containsArgs(args, "exec", "--json") || containsArgs(args, "exec", "resume") || containsArgs(args, "--ephemeral") {
				t.Fatalf("initial args=%#v", args)
			}
			if !strings.Contains(prompt, "不能直接操作目标服务器") {
				t.Fatalf("initial prompt=%s", prompt)
			}
			return []byte(`{"type":"thread.started","thread_id":"thread-ssh-1"}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"), nil, nil
		case 2:
			if !containsArgs(args, "exec", "resume") || !containsArgs(args, "thread-ssh-1", "-") {
				t.Fatalf("resume args=%#v", args)
			}
			if containsArgs(args, "-C") || containsArgs(args, "--sandbox") || containsArgs(args, "--ephemeral") {
				t.Fatalf("resume unexpectedly replaced saved session settings: %#v", args)
			}
			if strings.Contains(prompt, "此前对话") || strings.Contains(prompt, "不能直接操作目标服务器") || !strings.Contains(prompt, "用户请求：继续检查") {
				t.Fatalf("resume prompt replayed the session instead of sending a delta: %s", prompt)
			}
			return []byte(`{"type":"thread.started","thread_id":"thread-ssh-1"}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"), nil, nil
		default:
			t.Fatalf("unexpected call %d", calls)
			return nil, nil, nil
		}
	}

	first, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "检查服务"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Session.ID != "thread-ssh-1" {
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

func TestCodexProviderSendsApprovalAndResultToResumedThread(t *testing.T) {
	provider := NewCodexProvider("codex", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, prompt string) ([]byte, []byte, error) {
		calls++
		if calls == 1 {
			response := `{"type":"command_proposal","message":"查看负载","proposal":{"id":"proposal-1","command":"uptime","explanation":"检查负载","expectedEffect":"输出负载","risk":"read_only"},"panelActions":[]}`
			return []byte(`{"type":"thread.started","thread_id":"thread-approval"}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"), nil, nil
		}
		if !containsArgs(args, "exec", "resume") || !strings.Contains(prompt, "用户批准执行命令：uptime") || !strings.Contains(prompt, "远端命令执行结果") {
			t.Fatalf("resume args=%#v prompt=%s", args, prompt)
		}
		response := `{"type":"finished","message":"负载正常","proposal":null,"panelActions":[]}`
		return []byte(`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"), nil, nil
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

func TestCodexProviderClosesThreadWithSSHSession(t *testing.T) {
	provider := NewCodexProvider("codex", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, _ string) ([]byte, []byte, error) {
		calls++
		if calls == 1 {
			response := `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`
			return []byte(`{"type":"thread.started","thread_id":"00000000-0000-0000-0000-000000000001"}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"), nil, nil
		}
		if !containsArgs(args, "delete", "--force", "00000000-0000-0000-0000-000000000001") {
			t.Fatalf("delete args=%#v", args)
		}
		return nil, nil, nil
	}

	if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "检查"}, Callbacks{}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "再次检查"}, Callbacks{}); err == nil || !strings.Contains(err.Error(), "会话已关闭") {
		t.Fatalf("closed stream error=%v", err)
	}
}

func TestCodexProviderRetainsThreadsAcrossNewConversationAndClose(t *testing.T) {
	provider := NewCodexProvider("codex", "")
	provider.KeepSession = true
	provider.lookPath = func(string) (string, error) { return "codex", nil }
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, _ string) ([]byte, []byte, error) {
		if len(args) > 0 && args[0] == "delete" {
			t.Fatalf("retained thread was deleted: %v", args)
		}
		calls++
		id := []string{"first-thread", "second-thread"}[calls-1]
		return []byte(`{"type":"thread.started","thread_id":"` + id + `"}` + "\n" +
			`{"type":"item.completed","item":{"type":"agent_message","text":"{\"type\":\"finished\",\"message\":\"done\",\"proposal\":null,\"panelActions\":[]}"}}` + "\n"), nil, nil
	}
	for i := 0; i < 2; i++ {
		if _, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "check"}, Callbacks{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestCodexProviderKeepsOnlyOneThreadPerSSHSession(t *testing.T) {
	provider := NewCodexProvider("codex", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, _ string) ([]byte, []byte, error) {
		calls++
		response := `{"type":"finished","message":"done","proposal":null,"panelActions":[]}`
		switch calls {
		case 1:
			return []byte(`{"type":"thread.started","thread_id":"00000000-0000-0000-0000-000000000001"}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"), nil, nil
		case 2:
			return []byte(`{"type":"thread.started","thread_id":"00000000-0000-0000-0000-000000000002"}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"), nil, nil
		case 3:
			if !containsArgs(args, "delete", "--force", "00000000-0000-0000-0000-000000000001") {
				t.Fatalf("stale delete args=%#v", args)
			}
			return nil, nil, nil
		default:
			t.Fatalf("unexpected call %d", calls)
			return nil, nil, nil
		}
	}

	first, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "第一次"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Stream(context.Background(), Request{Event: "start", Prompt: "显式新会话"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	sessionCount := len(provider.sessions)
	ownedCount := len(provider.owned)
	provider.mu.Unlock()
	if first.Session.ID == second.Session.ID || second.Session.ID != "00000000-0000-0000-0000-000000000002" || sessionCount != 1 || ownedCount != 1 || calls != 3 {
		t.Fatalf("first=%q second=%q sessions=%d owned=%d calls=%d", first.Session.ID, second.Session.ID, sessionCount, ownedCount, calls)
	}
}

func TestCodexProviderRecreatesMissingThreadWithHistory(t *testing.T) {
	provider := NewCodexProvider("codex", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	provider.sessions["missing-thread"] = &codexSession{
		ID:        "missing-thread",
		History:   []codexHistory{{Role: "assistant", Content: "此前发现 nginx 超时"}},
		Proposals: make(map[string]Proposal),
	}
	calls := 0
	provider.run = func(_ context.Context, _ string, args []string, prompt string) ([]byte, []byte, error) {
		calls++
		if calls == 1 {
			if !containsArgs(args, "exec", "resume") {
				t.Fatalf("missing-thread call did not resume: %#v", args)
			}
			return nil, []byte("session not found"), errors.New("exit status 1")
		}
		if containsArgs(args, "exec", "resume") || !strings.Contains(prompt, "此前发现 nginx 超时") {
			t.Fatalf("replacement call args=%#v prompt=%s", args, prompt)
		}
		response := `{"type":"finished","message":"continued","proposal":null,"panelActions":[]}`
		return []byte(`{"type":"thread.started","thread_id":"replacement-thread"}` + "\n" +
			`{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"), nil, nil
	}

	turn, err := provider.Stream(context.Background(), Request{
		Event: "user_message", SessionID: "missing-thread", Prompt: "继续",
	}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Session.ID != "replacement-thread" || calls != 2 {
		t.Fatalf("turn=%#v calls=%d", turn, calls)
	}
}

func TestCodexProviderWorkspaceCanBeUpdatedBetweenTurns(t *testing.T) {
	provider := NewCodexProvider("codex", "")
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

func testCodexProvider(t *testing.T, response string) *CodexProvider {
	t.Helper()
	provider := NewCodexProvider("codex", "")
	provider.lookPath = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	provider.run = func(_ context.Context, _ string, args []string, prompt string) ([]byte, []byte, error) {
		resuming := containsArgs(args, "exec", "resume")
		if !resuming {
			if !strings.Contains(prompt, "不能直接操作目标服务器") {
				t.Fatalf("missing safety instruction: %s", prompt)
			}
			if !strings.Contains(prompt, "不要调用任何本机工具") {
				t.Fatalf("unconfigured workspace unexpectedly enabled local tools: %s", prompt)
			}
			if !containsArgs(args, "--sandbox", "read-only") {
				t.Fatalf("unsafe initial Codex args: %#v", args)
			}
		}
		if containsArgs(args, "--ephemeral") {
			t.Fatalf("persistent Codex session unexpectedly used --ephemeral: %#v", args)
		}
		line := ""
		if !resuming {
			line = `{"type":"thread.started","thread_id":"thread-test"}` + "\n"
		}
		line += `{"type":"item.completed","item":{"type":"agent_message","text":` + string(mustJSON(response)) + `}}` + "\n"
		return []byte(line), nil, nil
	}
	return provider
}

func mustJSON(value string) []byte {
	result := make([]byte, 0, len(value)+2)
	result = append(result, '"')
	for _, current := range []byte(value) {
		switch current {
		case '\\', '"':
			result = append(result, '\\', current)
		case '\n':
			result = append(result, '\\', 'n')
		default:
			result = append(result, current)
		}
	}
	return append(result, '"')
}

func containsArgs(args []string, want ...string) bool {
	if len(want) == 0 || len(want) > len(args) {
		return false
	}
	for index := 0; index <= len(args)-len(want); index++ {
		match := true
		for offset := range want {
			if args[index+offset] != want[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
