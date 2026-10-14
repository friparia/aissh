package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	chat_history "git.example.com/ai/wrd-ai-cli/internal/history"
)

type historyRequestProvider struct {
	requests chan agent.Request
}

func (historyRequestProvider) Name() string     { return "history-test" }
func (historyRequestProvider) Configured() bool { return true }
func (provider historyRequestProvider) Stream(_ context.Context, request agent.Request, _ agent.Callbacks) (agent.Turn, error) {
	provider.requests <- request
	return agent.Turn{Type: "finished", Session: agent.Session{ID: "continued"}, Message: "继续完成"}, nil
}
func (historyRequestProvider) Action(context.Context, agent.Request) (agent.Turn, error) {
	return agent.Turn{}, nil
}

func TestChatHistoryLoadsForCurrentTargetAndStaysNonActionable(t *testing.T) {
	directory := t.TempDir()
	store := chat_history.New(directory)
	createdAt := time.Date(2026, 8, 24, 12, 34, 0, 0, time.FixedZone("CST", 8*60*60))
	if err := store.Append("root@server", chat_history.Message{
		Role: "user", Content: "检查 nginx", Time: createdAt.Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append("root@server", chat_history.Message{
		Role: "proposal", Content: "查看服务状态", Time: createdAt.Add(time.Minute).Format(time.RFC3339Nano),
		Proposal: &agent.Proposal{ID: "old", Command: "systemctl status nginx", Risk: "read_only"},
	}); err != nil {
		t.Fatal(err)
	}

	application, err := New(Config{SSHArgs: []string{"root@server"}, HistoryDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	if len(application.chat) != 2 || len(application.historyConversations) != 1 {
		t.Fatalf("chat=%#v", application.chat)
	}
	if application.proposal != nil {
		t.Fatalf("historical proposal became actionable: %#v", application.proposal)
	}
	if !application.openAIHistory() || application.focus != focusAIHistory {
		t.Fatalf("history picker did not open: focus=%v", application.focus)
	}
	if !application.selectHistoryConversation(1) {
		t.Fatal("history conversation was not selected")
	}
	if len(application.chat) != 3 || !application.chat[0].Historical || !application.chat[1].Historical {
		t.Fatalf("selected chat=%#v", application.chat)
	}
	conversation := strings.Join(application.aiLines(100), "\n")
	for _, want := range []string{"2026-08-24 12:34", "检查 nginx", "systemctl status nginx", "已切换到历史会话"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("history view missing %q: %q", want, conversation)
		}
	}

	other, err := New(Config{SSHArgs: []string{"root@other"}, HistoryDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.chat) != 1 || len(other.historyConversations) != 0 || strings.Contains(strings.Join(other.aiLines(100), "\n"), "检查 nginx") {
		t.Fatalf("history leaked across targets: %#v", other.chat)
	}
}

func TestPersistentChatIsAvailableToNextSession(t *testing.T) {
	directory := t.TempDir()
	first, err := New(Config{SSHArgs: []string{"server"}, HistoryDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	first.appendPersistentChat(chatMessage{Role: "user", Content: "查看磁盘", Time: time.Now()})
	first.appendPersistentChat(chatMessage{Role: "assistant", Content: "磁盘使用率正常", Time: time.Now()})

	second, err := New(Config{SSHArgs: []string{"server"}, HistoryDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	if !second.openAIHistory() || !second.selectHistoryConversation(1) {
		t.Fatal("persisted session could not be selected")
	}
	conversation := strings.Join(second.aiLines(90), "\n")
	if !strings.Contains(conversation, "查看磁盘") || !strings.Contains(conversation, "磁盘使用率正常") {
		t.Fatalf("persisted conversation=%q", conversation)
	}
}

func TestStartupResumesOnlyRequestedConversation(t *testing.T) {
	dir := t.TempDir()
	store := chat_history.New(dir)
	for _, message := range []chat_history.Message{
		{ConversationID: "selected", Role: "user", Content: "检查磁盘", Time: time.Now().Format(time.RFC3339Nano)},
		{ConversationID: "selected", Role: "proposal", Content: "建议检查", Proposal: &agent.Proposal{Command: "df -h"}},
		{ConversationID: "other", Role: "user", Content: "另一个问题"},
	} {
		if err := store.Append("profile:host", message); err != nil {
			t.Fatal(err)
		}
	}
	var changed []string
	config := Config{SSHArgs: []string{"server"}, HistoryDir: dir, HistoryTarget: "profile:host", ConversationID: "selected", ConversationChanged: func(id string) { changed = append(changed, id) }}
	a, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer a.cancel()
	if a.historyConversationID != "selected" || !strings.Contains(a.agentSeedContext, "检查磁盘") || strings.Contains(a.agentSeedContext, "另一个问题") {
		t.Fatalf("wrong context: %q", a.agentSeedContext)
	}
	if a.proposal != nil || a.focus != focusAI || a.sidebarCollapsed {
		t.Fatal("history became actionable or AI entry was not focused")
	}
	if len(changed) == 0 || changed[len(changed)-1] != "selected" {
		t.Fatal("desktop association was not announced")
	}
	a.startNewAIConversation()
	if a.agentSeedContext != "" || a.historyConversationID == "selected" || changed[len(changed)-1] != a.historyConversationID {
		t.Fatal("new conversation retained context or stale desktop association")
	}
	config.HistoryTarget = "profile:other-host"
	if _, err := New(config); err == nil {
		t.Fatal("startup silently resumed a different server")
	}
	config.HistoryDir = ""
	if _, err := New(config); err == nil {
		t.Fatal("startup accepted resume with disabled history")
	}
}

func TestSelectedHistoryConversationSeedsNextAgentRequest(t *testing.T) {
	directory := t.TempDir()
	store := chat_history.New(directory)
	for _, message := range []chat_history.Message{
		{ConversationID: "nginx-session", Role: "user", Content: "检查 nginx", Time: "2026-08-24T12:34:00+08:00"},
		{ConversationID: "nginx-session", Role: "assistant", Content: "发现上游超时", Time: "2026-08-24T12:35:00+08:00"},
	} {
		if err := store.Append("server", message); err != nil {
			t.Fatal(err)
		}
	}
	application, err := New(Config{SSHArgs: []string{"server"}, HistoryDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan agent.Request, 1)
	application.agent = historyRequestProvider{requests: requests}
	if !application.openAIHistory() || !application.selectHistoryConversation(1) {
		t.Fatal("history conversation could not be selected")
	}
	application.startAI("继续查最近错误", true)
	select {
	case request := <-requests:
		context := request.SystemContext["conversation_history"]
		if request.Event != "start" || !strings.Contains(context, "检查 nginx") || !strings.Contains(context, "发现上游超时") {
			t.Fatalf("request=%#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("history continuation request timed out")
	}
	select {
	case event := <-application.events:
		application.handleEvent(event)
	case <-time.After(time.Second):
		t.Fatal("history continuation turn timed out")
	}
	if application.agentSessionID != "continued" || application.agentSeedContext != "" {
		t.Fatalf("session=%q seed=%q", application.agentSessionID, application.agentSeedContext)
	}
}

func TestAIHistoryPickerRendersAndSupportsKeyboardSelection(t *testing.T) {
	directory := t.TempDir()
	store := chat_history.New(directory)
	for _, message := range []chat_history.Message{
		{ConversationID: "disk", Role: "user", Content: "查看磁盘空间", Time: "2026-08-25T10:00:00+08:00"},
		{ConversationID: "nginx", Role: "user", Content: "检查 nginx", Time: "2026-08-24T10:00:00+08:00"},
	} {
		if err := store.Append("server", message); err != nil {
			t.Fatal(err)
		}
	}
	application, err := New(Config{SSHArgs: []string{"server"}, HistoryDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	application.cols = 140
	application.rows = 36
	application.mainCols = 89
	application.contentRows = 34
	application.focus = focusAI
	application.handleAIInput([]byte{0x12})
	if application.focus != focusAIHistory {
		t.Fatalf("focus=%v", application.focus)
	}
	output := application.renderAIWorkspace(20, 90, 50, 12)
	for _, want := range []string{"[会话]", "+ 新建会话", "查看磁盘空间", "检查 nginx", "Enter 打开"} {
		if !strings.Contains(output, want) {
			t.Fatalf("history picker missing %q: %q", want, output)
		}
	}
	application.handleAIHistoryInput([]byte("\x1b[B"))
	application.handleAIHistoryInput([]byte{'\r'})
	if application.focus != focusAI || application.historyConversationID != "disk" || !strings.Contains(strings.Join(application.aiLines(80), "\n"), "查看磁盘空间") {
		t.Fatalf("selected id=%q focus=%v chat=%q", application.historyConversationID, application.focus, strings.Join(application.aiLines(80), "\n"))
	}
}

func TestCodexRecoveryCommandPersistsOnlyWhenEnabled(t *testing.T) {
	for _, keep := range []bool{false, true} {
		directory := t.TempDir()
		first, err := New(Config{SSHArgs: []string{"server"}, HistoryDir: directory, KeepCodexSession: keep})
		if err != nil {
			t.Fatal(err)
		}
		id := "00000000-0000-0000-0000-000000000001"
		first.rememberCodexThread(id)
		first.rememberCodexThread(id)
		conversations, err := chat_history.New(directory).LoadConversations("server")
		if err != nil {
			t.Fatal(err)
		}
		if !keep {
			if len(conversations) != 0 || first.desktopState().CodexThreadID != "" {
				t.Fatal("disabled retention exposed a recovery command")
			}
			continue
		}
		if len(conversations) != 1 || conversations[0].CodexThreadID != id || len(conversations[0].Messages) != 1 {
			t.Fatalf("conversations=%#v", conversations)
		}
		second, err := New(Config{SSHArgs: []string{"server"}, HistoryDir: directory})
		if err != nil {
			t.Fatal(err)
		}
		if !second.openAIHistory() || !second.selectHistoryConversation(1) {
			t.Fatal("cannot select saved conversation")
		}
		if second.desktopState().CodexThreadID != id || !strings.Contains(strings.Join(second.aiLines(100), "\n"), "codex resume "+id) {
			t.Fatal("recovery command was not restored")
		}
		second.startNewAIConversation()
		if second.desktopState().CodexThreadID != "" {
			t.Fatal("recovery ID leaked into new conversation")
		}
	}
}
