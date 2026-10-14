package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hinshun/vt10x"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
)

type approvalTestProvider struct{}

func (approvalTestProvider) Name() string     { return "test" }
func (approvalTestProvider) Configured() bool { return true }
func (approvalTestProvider) Stream(context.Context, agent.Request, agent.Callbacks) (agent.Turn, error) {
	return agent.Turn{}, nil
}
func (approvalTestProvider) Action(_ context.Context, request agent.Request) (agent.Turn, error) {
	return agent.Turn{Type: "approved", Session: agent.Session{ID: request.SessionID}}, nil
}

type alwaysYesTestProvider struct {
	mu          sync.Mutex
	risks       []string
	assessments int
	actions     []agent.Request
	errAt       int
}

func (provider *alwaysYesTestProvider) Name() string     { return "test" }
func (provider *alwaysYesTestProvider) Configured() bool { return true }
func (provider *alwaysYesTestProvider) Stream(context.Context, agent.Request, agent.Callbacks) (agent.Turn, error) {
	return agent.Turn{}, nil
}
func (provider *alwaysYesTestProvider) Action(_ context.Context, request agent.Request) (agent.Turn, error) {
	provider.mu.Lock()
	provider.actions = append(provider.actions, request)
	provider.mu.Unlock()
	return agent.Turn{Type: "approved", Session: agent.Session{ID: request.SessionID}}, nil
}
func (provider *alwaysYesTestProvider) AssessRisk(_ context.Context, _ agent.RiskAssessmentRequest) (agent.RiskAssessment, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.assessments++
	if provider.errAt > 0 && provider.assessments == provider.errAt {
		return agent.RiskAssessment{}, fmt.Errorf("review unavailable")
	}
	risk := "read_only"
	if provider.assessments <= len(provider.risks) {
		risk = provider.risks[provider.assessments-1]
	}
	return agent.RiskAssessment{Risk: risk, Reason: "independent review"}, nil
}

func (provider *alwaysYesTestProvider) counts() (int, int) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.assessments, len(provider.actions)
}

type executionAnalysisProvider struct {
	started chan struct{}
	release chan struct{}
}

type turnTestProvider struct{ turn agent.Turn }

func (turnTestProvider) Name() string     { return "test" }
func (turnTestProvider) Configured() bool { return true }
func (provider turnTestProvider) Stream(context.Context, agent.Request, agent.Callbacks) (agent.Turn, error) {
	return provider.turn, nil
}
func (turnTestProvider) Action(context.Context, agent.Request) (agent.Turn, error) {
	return agent.Turn{}, nil
}

func (executionAnalysisProvider) Name() string     { return "test" }
func (executionAnalysisProvider) Configured() bool { return true }
func (provider executionAnalysisProvider) Stream(_ context.Context, _ agent.Request, callbacks agent.Callbacks) (agent.Turn, error) {
	callbacks.OnStatus("正在复核命令输出")
	close(provider.started)
	<-provider.release
	return agent.Turn{Type: "finished", Message: "命令执行成功，结果正常。"}, nil
}
func (executionAnalysisProvider) Action(context.Context, agent.Request) (agent.Turn, error) {
	return agent.Turn{}, nil
}

func TestApprovalChoice(t *testing.T) {
	for _, value := range []string{"yes", "Y", "是", "执行"} {
		choice, ok := approvalChoice(value)
		if !ok || !choice {
			t.Fatalf("expected approval for %q", value)
		}
	}
	for _, value := range []string{"no", "N", "否", "拒绝"} {
		choice, ok := approvalChoice(value)
		if !ok || choice {
			t.Fatalf("expected rejection for %q", value)
		}
	}
}

func TestChineseAIInputIsCapturedBeforeSSH(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.atPrompt = true
	application.handleTerminalInput([]byte("帮"))
	if !application.aiLineCapture || string(application.lineInput) != "帮" {
		t.Fatalf("capture=%v input=%q", application.aiLineCapture, application.lineInput)
	}
}

func TestBracketedMultilineAIPasteDoesNotLeakIntoSSH(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	application.sshPTY = writer
	application.agent = turnTestProvider{}
	application.atPrompt = true

	paste := "\x1b[200~帮我复核这段命令：\nprintf '%s\\n' ok\ndate\x1b[201~"
	application.handleTerminalInput([]byte(paste[:len(paste)-2]))
	application.handleTerminalInput([]byte(paste[len(paste)-2:]))
	_ = writer.Close()
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("AI paste leaked into SSH: %q", written)
	}
	if application.inlineAI == nil || application.inlineAI.Prompt != "帮我复核这段命令：\nprintf '%s\\n' ok\ndate" {
		t.Fatalf("inline AI prompt=%#v", application.inlineAI)
	}
}

func TestBracketedShellPasteStillPassesThrough(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	application.sshPTY = writer
	application.atPrompt = true

	paste := "\x1b[200~printf 'one\\ntwo\\n'\x1b[201~"
	application.handleTerminalInput([]byte(paste))
	_ = writer.Close()
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != paste {
		t.Fatalf("shell paste=%q, want %q", written, paste)
	}
}

func TestYesUsesCompletionReceiptWithoutRecognizedPrompt(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	t.Cleanup(application.cancel)
	application.agent = approvalTestProvider{}
	application.sshPTY = writer
	application.atPrompt = true
	application.focus = focusApproval
	application.agentSessionID = "s1"
	command := "if ! command -v strace >/dev/null 2>&1; then\n  echo missing strace\n  exit 2\nfi\nstrace -V"
	application.proposal = &agent.Proposal{ID: "p1", Command: command, Risk: "read_only"}
	application.handleApprovalInput([]byte("yes\r"))
	select {
	case event := <-application.events:
		application.handleEvent(event)
	case <-time.After(time.Second):
		t.Fatal("approval action timed out")
	}
	awaitExecutionWrite(t, application)
	if application.execution == nil || application.execution.Command != command {
		t.Fatalf("execution did not preserve original command: %#v", application.execution)
	}
	_ = writer.Close()
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if want := approvedShellInput(command, application.execution.Marker); string(written) != want {
		t.Fatalf("ssh input = %q, want %q", written, want)
	}
	conversation := strings.Join(application.aiLines(60), "\n")
	if !strings.Contains(conversation, "已批准执行命令：") || !strings.Contains(conversation, "exit 2") || !strings.Contains(conversation, "strace -V") {
		t.Fatalf("approval was not synchronized: %q", conversation)
	}
}

func TestAlwaysYesRunsNonHighRiskCommandAfterThreeReviews(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}, AlwaysYes: true})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	t.Cleanup(application.cancel)
	provider := &alwaysYesTestProvider{risks: []string{"read_only", "low", "medium"}}
	application.agent = provider
	application.sshPTY = writer
	application.atPrompt = true
	application.finishAgentTurn(agent.Turn{
		Type:    "command_proposal",
		Session: agent.Session{ID: "s1"},
		Proposal: &agent.Proposal{
			ID: "p1", Command: "uptime", Explanation: "查看负载", Risk: "read_only",
		},
	}, nil)

	deadline := time.After(time.Second)
	for application.execution == nil {
		select {
		case event := <-application.events:
			application.handleEvent(event)
		case <-deadline:
			t.Fatal("always-yes approval timed out")
		}
	}
	assessments, actions := provider.counts()
	if assessments != AlwaysYesRiskAssessmentPasses || actions != 1 {
		t.Fatalf("assessments=%d actions=%d", assessments, actions)
	}
	awaitExecutionWrite(t, application)
	if application.execution.Command != "uptime" {
		t.Fatalf("execution=%#v", application.execution)
	}
	_ = writer.Close()
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if want := approvedShellInput("uptime", application.execution.Marker); string(written) != want {
		t.Fatalf("ssh input=%q want=%q", written, want)
	}
	conversation := strings.Join(application.aiLines(120), "\n")
	if !strings.Contains(conversation, "通过 3 次独立风险复核") {
		t.Fatalf("conversation=%q", conversation)
	}
}

func TestAlwaysYesNeverReviewsOrApprovesInitiallyHighRiskCommand(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}, AlwaysYes: true})
	if err != nil {
		t.Fatal(err)
	}
	provider := &alwaysYesTestProvider{}
	application.agent = provider
	application.atPrompt = true
	application.finishAgentTurn(agent.Turn{
		Type:    "command_proposal",
		Session: agent.Session{ID: "s1"},
		Proposal: &agent.Proposal{
			ID: "danger", Command: "systemctl restart nginx", Risk: "high",
		},
	}, nil)

	assessments, actions := provider.counts()
	if assessments != 0 || actions != 0 || application.agentBusy || application.proposal == nil {
		t.Fatalf("assessments=%d actions=%d busy=%v proposal=%#v", assessments, actions, application.agentBusy, application.proposal)
	}
	if !strings.Contains(application.approvalStatus, "手动确认") {
		t.Fatalf("approval status=%q", application.approvalStatus)
	}
}

func TestAlwaysYesFallsBackToManualWhenAnyReviewIsHighRisk(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}, AlwaysYes: true})
	if err != nil {
		t.Fatal(err)
	}
	provider := &alwaysYesTestProvider{risks: []string{"low", "high", "read_only"}}
	application.agent = provider
	application.atPrompt = true
	application.finishAgentTurn(agent.Turn{
		Type:    "command_proposal",
		Session: agent.Session{ID: "s1"},
		Proposal: &agent.Proposal{
			ID: "p1", Command: "deploy-app", Risk: "low",
		},
	}, nil)

	deadline := time.After(time.Second)
	for application.agentBusy {
		select {
		case event := <-application.events:
			application.handleEvent(event)
		case <-deadline:
			t.Fatal("always-yes risk reviews timed out")
		}
	}
	assessments, actions := provider.counts()
	if assessments != AlwaysYesRiskAssessmentPasses || actions != 0 {
		t.Fatalf("assessments=%d actions=%d", assessments, actions)
	}
	if application.proposal == nil || application.proposal.Risk != "high" || !strings.Contains(application.approvalStatus, "高风险") {
		t.Fatalf("proposal=%#v approval status=%q", application.proposal, application.approvalStatus)
	}
}

func TestAlwaysYesFallsBackToManualWhenRiskReviewFails(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}, AlwaysYes: true})
	if err != nil {
		t.Fatal(err)
	}
	provider := &alwaysYesTestProvider{errAt: 2}
	application.agent = provider
	application.atPrompt = true
	application.finishAgentTurn(agent.Turn{
		Type:    "command_proposal",
		Session: agent.Session{ID: "s1"},
		Proposal: &agent.Proposal{
			ID: "p1", Command: "uptime", Risk: "read_only",
		},
	}, nil)

	deadline := time.After(time.Second)
	for application.agentBusy {
		select {
		case event := <-application.events:
			application.handleEvent(event)
		case <-deadline:
			t.Fatal("always-yes risk failure timed out")
		}
	}
	assessments, actions := provider.counts()
	if assessments != 2 || actions != 0 || !strings.Contains(application.approvalStatus, "复核失败") {
		t.Fatalf("assessments=%d actions=%d approval status=%q", assessments, actions, application.approvalStatus)
	}
}

func TestIsolateApprovedCommandUsesWindowsEnterForEveryLine(t *testing.T) {
	command := "printf 'one\\n'\r\nprintf 'two\\n'\nexit 1"
	windows := isolateApprovedCommandWithLineEnding(command, "test", "\r")
	unix := isolateApprovedCommandWithLineEnding(command, "test", "\n")
	if strings.Contains(windows, "\n") || strings.ReplaceAll(windows, "\r", "\n") != strings.ReplaceAll(unix, "\r", "\n") {
		t.Fatalf("inconsistent line endings: Windows=%q Unix=%q", windows, unix)
	}
}

func TestProposalMovesToLeftApprovalCard(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.atPrompt = true
	application.mainCols = 80
	application.contentRows = 22
	application.rows = 24
	application.finishAgentTurn(agent.Turn{
		Type:    "command_proposal",
		Session: agent.Session{ID: "s1"},
		Proposal: &agent.Proposal{
			ID:          "p1",
			Command:     "uptime",
			Explanation: "查看当前系统负载",
			Risk:        "read_only",
		},
	}, nil)
	if application.focus != focusApproval || application.panels.Active().ID != "system" {
		t.Fatalf("focus=%v active=%s", application.focus, application.panels.Active().ID)
	}
	card := application.renderApprovalCard()
	for _, want := range []string{"AI 命令确认", "uptime", "yes", "no"} {
		if !strings.Contains(card, want) {
			t.Fatalf("approval card missing %q: %q", want, card)
		}
	}
}

func TestHighRiskProposalExpandsReviewPane(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.atPrompt = true
	application.finishAgentTurn(agent.Turn{
		Type:     "command_proposal",
		Proposal: &agent.Proposal{ID: "p1", Command: "systemctl restart nginx", Risk: "high"},
	}, nil)
	if !application.aiExpanded {
		t.Fatal("high-risk proposal should expand the AI review pane")
	}
}

func TestHighRiskForegroundProposalAppearsRedOnLeftAndRight(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.ensureSidebarVisible()
	application.mainCols = 80
	application.contentRows = 22
	application.rows = 24
	application.atPrompt = true
	application.inlineAI = &inlineAIState{Prompt: "上一条内联回答", Done: true, Started: time.Now()}
	application.agent = turnTestProvider{turn: agent.Turn{
		Type: "command_proposal",
		Proposal: &agent.Proposal{
			ID: "danger", Command: "systemctl restart nginx", Explanation: "重启线上服务", Risk: "high",
		},
	}}

	application.startAI("重启 nginx", true)
	select {
	case event := <-application.events:
		application.handleEvent(event)
	case <-time.After(time.Second):
		t.Fatal("foreground proposal timed out")
	}

	if application.inlineAI != nil || len(application.inlineHistory) != 1 {
		t.Fatalf("stale inline answer captured foreground proposal: inline=%#v history=%d", application.inlineAI, len(application.inlineHistory))
	}
	if application.inlineApprovalAvailable() {
		t.Fatal("foreground proposal was incorrectly hidden as an inline approval")
	}
	left := application.renderApprovalCard()
	for _, want := range []string{"高风险", "systemctl restart nginx", "yes", "no", "\x1b[48;5;124m", "\x1b[1;38;5;203m"} {
		if !strings.Contains(left, want) {
			t.Fatalf("left high-risk card missing %q: %q", want, left)
		}
	}

	titleRed, commandRed := false, false
	for _, line := range application.aiDisplayLines(60) {
		if strings.Contains(line.Text, "命令审查 · 高风险") && line.Style == aiErrorTopStyle {
			titleRed = true
		}
		if strings.Contains(line.Text, "systemctl restart nginx") && line.Style == aiDangerCodeStyle {
			commandRed = true
		}
	}
	if !titleRed || !commandRed {
		t.Fatalf("right high-risk styles title=%v command=%v", titleRed, commandRed)
	}
}

func TestHighRiskInlineProposalUsesRedCommandStyle(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	state := &inlineAIState{Proposal: &agent.Proposal{Command: "rm -rf /tmp/example", Risk: "dangerous"}}
	lines := application.inlineProposalLinesFor(state, 60, 7)
	titleRed, commandRed := false, false
	for _, line := range lines {
		style := application.inlineLineStyle(state, line)
		if strings.Contains(line, "[高风险]") && style == "\x1b[1;38;5;203m" {
			titleRed = true
		}
		if strings.Contains(line, "rm -rf /tmp/example") && style == "\x1b[1;38;5;203m" {
			commandRed = true
		}
	}
	if !titleRed || !commandRed {
		t.Fatalf("inline high-risk styles title=%v command=%v lines=%q", titleRed, commandRed, lines)
	}
}

func TestAIConversationIsExpandedByDefault(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	if !application.aiExpanded {
		t.Fatal("AI conversation should be expanded by default")
	}
	if application.focus != focusTerminal {
		t.Fatalf("default focus=%v, want terminal", application.focus)
	}
	monitorHeight, aiHeight := application.sidebarHeights(28)
	if aiHeight < 7 || aiHeight <= monitorHeight {
		t.Fatalf("expanded monitor=%d ai=%d", monitorHeight, aiHeight)
	}
	application.inlineAI = &inlineAIState{Prompt: "检查服务", Started: time.Now()}
	application.agentBusy = true
	_, inlineHeight := application.sidebarHeights(28)
	if inlineHeight != aiHeight {
		t.Fatalf("inline command-line AI changed expanded pane height: before=%d after=%d", aiHeight, inlineHeight)
	}
}

func TestSidebarShrinksSingleShortMonitorAboveExpandedAI(t *testing.T) {
	application := layoutTestApp(t)
	application.panels.UpdateContent("system", "主机  web-9\n负载  0.8\n内存  2.4G/8G\n磁盘  26%\n进程  207", "实时", true)

	monitorHeight, aiHeight := application.sidebarHeights(application.contentRows)
	if monitorHeight != 8 {
		t.Fatalf("monitor height=%d, want content-sized height 8", monitorHeight)
	}
	if aiHeight != application.contentRows-monitorHeight-1 || aiHeight <= monitorHeight {
		t.Fatalf("monitor=%d ai=%d contentRows=%d", monitorHeight, aiHeight, application.contentRows)
	}

	application.focus = focusMonitor
	focusedMonitorHeight, focusedAIHeight := application.sidebarHeights(application.contentRows)
	if focusedMonitorHeight <= focusedAIHeight {
		t.Fatalf("focused monitor=%d ai=%d", focusedMonitorHeight, focusedAIHeight)
	}
}

func TestAIConversationStaysExpandedAfterUserOpensIt(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.openAIConversation()
	if application.focus != focusAI || !application.aiExpanded {
		t.Fatalf("AI did not expand: focus=%v expanded=%v", application.focus, application.aiExpanded)
	}

	application.handleAIInput([]byte{0x1b})
	if application.focus != focusTerminal || !application.aiExpanded {
		t.Fatalf("Esc collapsed AI: focus=%v expanded=%v", application.focus, application.aiExpanded)
	}

	application.agentBusy = true
	application.startInlineAI("检查服务", "检查服务", true)
	if !application.aiExpanded {
		t.Fatal("inline AI start collapsed an already-open conversation")
	}
	application.agentBusy = false
	application.finishAgentTurn(agent.Turn{Type: "finished", Message: "服务正常"}, nil)
	if !application.aiExpanded {
		t.Fatal("inline AI completion collapsed an already-open conversation")
	}

	application.atPrompt = true
	application.finishAgentTurn(agent.Turn{
		Type: "command_proposal",
		Proposal: &agent.Proposal{
			ID: "p1", Command: "uptime", Risk: "read_only",
		},
	}, nil)
	if !application.aiExpanded {
		t.Fatal("low-risk proposal collapsed an already-open conversation")
	}
}

func TestCtrlOPassesThroughInsideRemoteInteractiveProgram(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	application.sshPTY = writer
	application.atPrompt = false
	application.focus = focusTerminal

	application.handleInput([]byte{0x0f})
	forwarded := make([]byte, 1)
	if count, err := reader.Read(forwarded); err != nil || count != 1 || forwarded[0] != 0x0f {
		t.Fatalf("Ctrl+O passthrough count=%d data=%v err=%v", count, forwarded[:count], err)
	}
	if application.focus != focusTerminal {
		t.Fatalf("Ctrl+O stole focus inside remote program: %v", application.focus)
	}
}

func TestCtrlOPassesThroughAtShellPromptWithoutFoldedCommand(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	application.sshPTY = writer
	application.atPrompt = true
	application.focus = focusTerminal

	application.handleInput([]byte{0x0f})
	forwarded := make([]byte, 1)
	if count, err := reader.Read(forwarded); err != nil || count != 1 || forwarded[0] != 0x0f {
		t.Fatalf("Ctrl+O passthrough count=%d data=%v err=%v", count, forwarded[:count], err)
	}
	if application.focus != focusTerminal {
		t.Fatalf("Ctrl+O changed focus at prompt: %v", application.focus)
	}
}

func TestCtrlOExpandsInlineCommandWithoutOpeningAIConversation(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 72
	application.contentRows = 24
	application.rows = 26
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("root@server:~# "))
	cursor := application.virtual.Cursor()
	proposal := &agent.Proposal{
		ID:          "long-inline",
		Command:     "journalctl -u nginx --since today --no-pager | grep error | tail -n 200 && systemctl status nginx --no-pager && ss -lntp | grep nginx && ps aux --sort=-%cpu | head -n 20",
		Explanation: "检查服务、日志、端口和进程",
		Risk:        "read_only",
	}
	application.inlineAI = &inlineAIState{
		Prompt: "全面检查 nginx", AnchorX: cursor.X, AnchorY: cursor.Y, Proposal: proposal,
	}
	application.proposal = proposal
	application.focus = focusApproval
	before := strings.Join(application.inlineProposalLines(application.mainCols-cursor.X, application.contentRows-1), "\n")
	if !strings.Contains(before, "[展开全部]") {
		t.Fatalf("inline command was not folded: %q", before)
	}

	application.handleInput([]byte{0x0f})
	if !application.inlineAI.CommandExpanded || application.focus != focusApproval {
		t.Fatalf("expanded=%v focus=%v", application.inlineAI.CommandExpanded, application.focus)
	}
	after := strings.Join(application.inlineProposalLines(application.mainCols-cursor.X, application.contentRows-1), "\n")
	if strings.Contains(after, "[展开全部]") || !strings.Contains(after, "ps aux") {
		t.Fatalf("expanded inline command=%q", after)
	}
}

func TestCtrlOExpandsFallbackApprovalCard(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 64
	application.contentRows = 20
	application.rows = 22
	application.focus = focusApproval
	application.proposal = &agent.Proposal{
		ID:          "long-card",
		Command:     "journalctl -u nginx --since today --no-pager | grep error | tail -n 200 && systemctl status nginx --no-pager && ss -lntp | grep nginx && ps aux --sort=-%cpu | head -n 20 && cat /etc/os-release && uname -a && df -h",
		Explanation: "检查 nginx",
		Risk:        "read_only",
	}
	before := application.renderApprovalCard()
	if !strings.Contains(before, "[展开全部]") {
		t.Fatalf("approval command was not folded: %q", before)
	}
	_, _, _, beforeHeight := application.approvalGeometry()

	application.handleInput([]byte{0x0f})
	_, _, _, afterHeight := application.approvalGeometry()
	if !application.approvalCommandExpanded || application.focus != focusApproval || afterHeight <= beforeHeight {
		t.Fatalf("expanded=%v focus=%v heights=%d->%d", application.approvalCommandExpanded, application.focus, beforeHeight, afterHeight)
	}
	if after := application.renderApprovalCard(); strings.Contains(after, "[展开全部]") || !strings.Contains(after, "df -h") {
		t.Fatalf("expanded approval command=%q", after)
	}
}

func TestCtrlOExpandsAIConversationCommandWithoutChangingPane(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.cols = 140
	application.rows = 36
	application.mainCols = 89
	application.contentRows = 34
	application.focus = focusAI
	application.chat = []chatMessage{{
		Role: "proposal",
		Proposal: &agent.Proposal{
			ID:      "chat-command",
			Command: "journalctl -u nginx --since today --no-pager | grep error | tail -n 200 && systemctl status nginx --no-pager && ss -lntp | grep nginx && ps aux --sort=-%cpu | head -n 20",
			Risk:    "read_only",
		},
	}}
	if !application.formatCommandForDisplay(application.chat[0].Proposal.Command, application.cols-application.mainCols-7, 6).Truncated {
		t.Fatal("test command did not fold in AI pane")
	}

	application.handleInput([]byte{0x0f})
	if !application.chat[0].CommandExpanded || application.focus != focusAI {
		t.Fatalf("expanded=%v focus=%v", application.chat[0].CommandExpanded, application.focus)
	}
}

func TestInlineAILoadingAndAnswerStayBelowPrompt(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 80
	application.contentRows = 22
	application.agentBusy = true
	application.inlineAI = &inlineAIState{
		Prompt:  "检查 nginx 状态",
		AnchorX: 20,
		AnchorY: 2,
		Status:  "正在调用本机 Codex",
		Started: time.Now().Add(-2 * time.Second),
	}
	application.agentInline = application.inlineAI
	loading := application.renderInlineAI()
	for _, want := range []string{"检查 nginx 状态", "正在调用本机 Codex"} {
		if !strings.Contains(loading, want) {
			t.Fatalf("loading output missing %q: %q", want, loading)
		}
	}
	application.finishAgentTurn(agent.Turn{Type: "finished", Message: "nginx 当前运行正常。"}, nil)
	if !application.inlineAI.Done || application.focus != focusTerminal {
		t.Fatalf("inline=%#v focus=%v", application.inlineAI, application.focus)
	}
	answer := application.renderInlineAI()
	if !strings.Contains(answer, "AI: nginx 当前运行正常。") {
		t.Fatalf("answer = %q", answer)
	}
}

func TestInlineConversationSynchronizesToRightPaneWithoutDuplicateAnswer(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.chat = append(application.chat, chatMessage{Role: "user", Content: "检查 nginx 状态"})
	application.inlineAI = &inlineAIState{Prompt: "检查 nginx 状态", Started: time.Now()}
	application.agentInline = application.inlineAI
	application.agentBusy = true
	application.handleEvent(agentAnswerEvent{content: "nginx 正常"})
	application.finishAgentTurn(agent.Turn{Type: "finished", Message: "nginx 正常"}, nil)

	conversation := strings.Join(application.aiLines(60), "\n")
	for _, want := range []string{"╭─ 你", "│ 检查 nginx 状态", "╭─ ✦ AI", "│ nginx 正常"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("right conversation missing %q: %q", want, conversation)
		}
	}
	if strings.Count(conversation, "│ nginx 正常") != 1 {
		t.Fatalf("streamed answer was duplicated: %q", conversation)
	}
	if collapsed := application.renderAIWorkspace(1, 1, 40, 2); !strings.Contains(collapsed, "已同步") {
		t.Fatalf("collapsed pane does not show sync status: %q", collapsed)
	}
}

func TestCompletedInlineAnswerPersistsWhenSSHInputContinues(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 80
	application.contentRows = 22
	application.rows = 24
	application.atPrompt = true
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("root@server:~# "))
	cursor := application.virtual.Cursor()
	application.inlineAI = &inlineAIState{
		Prompt: "检查 nginx 状态", AnchorX: cursor.X, AnchorY: cursor.Y,
		Response: "nginx 当前运行正常。", Done: true, Started: time.Now(),
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	application.sshPTY = writer

	before, _, _ := application.renderTerminalWorkspace()
	if !strings.Contains(before, "AI: nginx 当前运行正常。") {
		t.Fatalf("initial inline output missing: %q", before)
	}
	application.handleTerminalInput([]byte("e"))
	if application.inlineAI != nil || len(application.inlineHistory) != 1 {
		t.Fatalf("inline=%#v history=%d", application.inlineAI, len(application.inlineHistory))
	}
	_, cursorRow, _ := application.renderTerminalWorkspace()
	visible := cachedTerminalRows(application.terminalRows, application.screenRows())
	if !strings.Contains(visible, "检查 nginx 状态") || !strings.Contains(visible, "AI: nginx 当前运行正常。") {
		t.Fatalf("archived inline output disappeared: %q", visible)
	}
	if cursorRow <= 3 {
		t.Fatalf("SSH cursor was not moved below the persistent answer: row=%d", cursorRow)
	}
}

func TestTerminalWorkspaceDoesNotPreClearEverySSHRow(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 80
	application.contentRows = 22
	application.rows = 24
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("root@server:~# "))

	output, _, _ := application.renderTerminalWorkspace()
	rowStart := "\x1b[2;1H\x1b[0m"
	if count := strings.Count(output, rowStart); count != 1 {
		t.Fatalf("first SSH row rendered %d times; expected one in-place overwrite", count)
	}
	unchanged, _, _ := application.renderTerminalWorkspace()
	if strings.Contains(unchanged, rowStart) {
		t.Fatalf("unchanged SSH row was rendered again: %q", unchanged)
	}
}

func TestInlineAnswerClearsOnlyItsInsertedRow(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 80
	application.contentRows = 22
	application.rows = 24
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("root@server:~# "))
	cursor := application.virtual.Cursor()
	application.inlineAI = &inlineAIState{
		Prompt: "检查服务", AnchorX: cursor.X, AnchorY: cursor.Y,
		Response: "服务正常。", Done: true, Started: time.Now(),
	}

	layout, ok := application.inlineLayoutFor(application.inlineAI)
	if !ok || len(layout.Lines) == 0 {
		t.Fatal("inline answer layout was not created")
	}
	output, _, _ := application.renderTerminalWorkspace()
	insertedRow := layout.Top + 1
	rowStart := fmt.Sprintf("\x1b[%d;1H\x1b[0m", insertedRow)
	if count := strings.Count(output, rowStart); count != 1 {
		t.Fatalf("inserted answer row clear count=%d, expected one", count)
	}
}

func TestInlineAnswerAtTerminalBottomUsesDynamicContentRows(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 64
	application.contentRows = 12
	application.rows = 14
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte(strings.Repeat("line\r\n", application.contentRows-1) + "root@server:~# "))
	cursor := application.virtual.Cursor()
	if cursor.Y != application.contentRows-1 {
		t.Fatalf("cursor is not at terminal bottom: %#v", cursor)
	}
	state := &inlineAIState{
		Prompt:  "检查服务",
		AnchorX: cursor.X,
		AnchorY: cursor.Y,
		Status:  "正在分析",
		Started: time.Now(),
	}
	application.inlineAI = state
	loading := application.buildTerminalLayout()
	if len(loading.Blocks) != 1 || loading.Blocks[0].Height != 1 || len(loading.Blocks[0].Lines) != 1 {
		t.Fatalf("loading layout=%#v", loading)
	}
	loadingTop := loading.Blocks[0].Top
	if loadingTop <= 2 {
		t.Fatalf("loading block jumped to top: top=%d", loadingTop)
	}

	state.Done = true
	state.Status = ""
	state.Response = strings.Repeat("服务状态正常，继续检查日志。", 12)
	completed := application.buildTerminalLayout()
	if len(completed.Blocks) != 1 {
		t.Fatalf("completed layout=%#v", completed)
	}
	if completed.Blocks[0].Height != len(completed.Blocks[0].Lines) || completed.Blocks[0].Height <= loading.Blocks[0].Height {
		t.Fatalf("completed block did not grow to actual content: loading=%d completed=%d lines=%d",
			loading.Blocks[0].Height, completed.Blocks[0].Height, len(completed.Blocks[0].Lines))
	}
	cursorRow := 2 + completed.CursorLogical - completed.Viewport
	if cursorRow != application.screenRows()-1 {
		t.Fatalf("terminal cursor did not stay visible at bottom: row=%d want=%d", cursorRow, application.screenRows()-1)
	}
	if completed.Blocks[0].Top < 2 || completed.Blocks[0].Top >= loadingTop {
		t.Fatalf("completed block did not expand upward from loading row: loading=%d completed=%d", loadingTop, completed.Blocks[0].Top)
	}
}

func cachedTerminalRows(rows map[int]string, screenRows int) string {
	var output strings.Builder
	for row := 2; row < screenRows; row++ {
		output.WriteString(rows[row])
	}
	return output.String()
}

func TestInlineHistoryMovesWithTerminalScroll(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.inlineHistory = []*inlineAIState{
		{Prompt: "旧问题", AnchorY: 1, Done: true},
		{Prompt: "较新问题", AnchorY: 4, Done: true},
	}
	application.scrollInlineBlocks(2)
	if len(application.inlineHistory) != 1 || application.inlineHistory[0].Prompt != "较新问题" || application.inlineHistory[0].AnchorY != 2 {
		t.Fatalf("history=%#v", application.inlineHistory)
	}
}

func TestInlineCommandShowsLoadingWhileAIAnalyzesExecutionResult(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 80
	application.contentRows = 22
	application.rows = 24
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("root@server:~# "))
	provider := executionAnalysisProvider{started: make(chan struct{}), release: make(chan struct{})}
	application.agent = provider
	resultOutput := newContextBuffer(application.config.AIContextChars)
	resultOutput.Append("load average: 0.1")
	application.execution = &executionState{Command: "uptime", Output: resultOutput, StartedAt: time.Now(), Inline: true, Marker: "test"}

	application.processSSHOutput([]byte(executionMarkerPrefix("test") + "0\aroot@server:~# "))
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("execution analysis did not start")
	}
	if !application.agentBusy || application.inlineAI == nil || application.inlineAI.Done {
		t.Fatalf("busy=%v inline=%#v", application.agentBusy, application.inlineAI)
	}
	loading := application.renderInlineAI()
	for _, want := range []string{"AI 复核命令：uptime", "正在分析命令执行结果"} {
		if !strings.Contains(loading, want) {
			t.Fatalf("loading missing %q: %q", want, loading)
		}
	}
	_, aiHeight := application.sidebarHeights(28)
	if aiHeight < 7 {
		t.Fatalf("inline execution analysis collapsed the default AI pane: %d", aiHeight)
	}

	close(provider.release)
	deadline := time.After(time.Second)
	for application.agentBusy {
		select {
		case event := <-application.events:
			application.handleEvent(event)
		case <-deadline:
			t.Fatal("execution analysis did not finish")
		}
	}
	if application.inlineAI == nil || !application.inlineAI.Done || application.inlineAI.Response != "命令执行成功，结果正常。" {
		t.Fatalf("inline result=%#v", application.inlineAI)
	}
}

func TestInlineExecutionAnalysisAppearsAfterCompletedOutput(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 48
	application.contentRows = 12
	application.rows = 14
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	application.processRegularSSHOutput([]byte("root@server:~# "))
	provider := executionAnalysisProvider{started: make(chan struct{}), release: make(chan struct{})}
	application.agent = provider
	defer close(provider.release)
	application.beginCommandOutput("printf 'done\\n'")
	application.atPrompt = false
	execution := &executionState{Command: "printf 'done\\n'", Output: newContextBuffer(4096), Inline: true, Marker: "test"}
	application.execution = execution

	// Each read can end at PS2, in a redirection, or just after a terminal mode
	// change. None of these means the isolated command has finished.
	chunks := []string{
		"\x1b[?2004l", "(\r\n> ",
		"printf '" + strings.Repeat("x", 120) + "' > ",
		"/dev/null\r\n> ", ")\r\n", "done\r\n",
	}
	for index, chunk := range chunks {
		application.processRegularSSHOutput([]byte(chunk))
		application.refreshPromptFromScreen()
		if application.execution != execution || application.inlineAI != nil || application.atPrompt {
			t.Fatalf("chunk %d started analysis before the primary prompt: execution=%v inline=%#v atPrompt=%v", index, application.execution != nil, application.inlineAI, application.atPrompt)
		}
	}
	application.processRegularSSHOutput([]byte(executionMarkerPrefix("test") + "0\aroot@server:~# "))
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("analysis did not start at the final shell prompt")
	}
	if application.inlineAI == nil || application.inlineAI.AnchorY != application.virtual.Cursor().Y {
		t.Fatalf("analysis is not anchored after the command output: %#v", application.inlineAI)
	}
	if output := execution.Output.ContextString(); !strings.Contains(output, "done") {
		t.Fatalf("analysis missed the final command output: %q", output)
	}
	frame, _, _ := application.renderTerminalWorkspace()
	outputAt := strings.Index(frame, "done")
	analysisAt := strings.Index(frame, "AI 复核命令")
	if outputAt < 0 || analysisAt <= outputAt {
		t.Fatalf("analysis was rendered before the final command output: %q", frame)
	}
}

func TestInlineProposalKeepsYesNoUnderQuestion(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 80
	application.contentRows = 22
	application.rows = 24
	application.atPrompt = true
	application.inlineAI = &inlineAIState{Prompt: "查看负载", AnchorX: 20, AnchorY: 1, Started: time.Now()}
	application.agentInline = application.inlineAI
	application.finishAgentTurn(agent.Turn{
		Type: "command_proposal",
		Proposal: &agent.Proposal{
			ID: "p1", Command: "uptime", Explanation: "只读查看系统负载", Risk: "read_only",
		},
	}, nil)
	if !application.inlineApprovalAvailable() || application.focus != focusApproval {
		t.Fatalf("inline approval unavailable, focus=%v", application.focus)
	}
	output := application.renderInlineAI()
	for _, want := range []string{"查看负载", "$ uptime", "yes", "no"} {
		if !strings.Contains(output, want) {
			t.Fatalf("inline proposal missing %q: %q", want, output)
		}
	}
	conversation := strings.Join(application.aiLines(60), "\n")
	for _, want := range []string{"⚑ 命令审查 · 只读", "$ uptime"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("proposal conversation missing %q: %q", want, conversation)
		}
	}
	row, _, ok := application.inlineApprovalCursor()
	if !ok || row <= 3 || row >= application.rows {
		t.Fatalf("cursor row=%d ok=%v", row, ok)
	}
}

func TestAIConversationUsesCardsAndMarkdownStyles(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.chat = []chatMessage{
		{Role: "user", Content: "检查 nginx", Time: time.Date(2026, 8, 25, 14, 30, 0, 0, time.Local)},
		{Role: "assistant", Content: "# 结论\n\n- **服务正常**\n\n```sh\nsystemctl status nginx\n```", Time: time.Date(2026, 8, 25, 14, 31, 0, 0, time.Local)},
	}
	display := application.aiDisplayLines(48)
	var textLines []string
	codeStyled := false
	for _, line := range display {
		textLines = append(textLines, line.Text)
		if strings.Contains(line.Text, "systemctl status nginx") && line.Style == aiCodeStyle {
			codeStyled = true
		}
	}
	conversation := strings.Join(textLines, "\n")
	for _, want := range []string{"╭─ 你 · 14:30", "╭─ ✦ AI · 14:31", "│ ◆ 结论", "│ • 服务正常", "│  systemctl status nginx"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("styled conversation missing %q: %q", want, conversation)
		}
	}
	if strings.Contains(conversation, "**") || strings.Contains(conversation, "```") || !codeStyled {
		t.Fatalf("markdown was not presented as styled terminal content: %#v", display)
	}
}

func TestAIInputStaysPinnedBelowScrolledConversation(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.focus = focusAI
	application.aiExpanded = true
	application.aiScroll = 4
	application.editor = []rune("继续检查日志")
	for index := 0; index < 8; index++ {
		application.chat = append(application.chat, chatMessage{Role: "assistant", Content: fmt.Sprintf("第 %d 条较长的分析结果", index+1), Time: time.Now()})
	}
	output := application.renderAIWorkspace(3, 90, 50, 12)
	for _, want := range []string{"✦ AI 助手", "╭─ 输入", "› 继续检查日志"} {
		if !strings.Contains(output, want) {
			t.Fatalf("AI workspace missing pinned input %q: %q", want, output)
		}
	}
}

func TestInlineLongProposalUsesCommandCardLayout(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 80
	application.contentRows = 22
	application.rows = 24
	application.atPrompt = true
	application.inlineAI = &inlineAIState{Prompt: "检查 nginx 错误", AnchorX: 20, AnchorY: 1, Started: time.Now()}
	application.agentInline = application.inlineAI
	application.finishAgentTurn(agent.Turn{
		Type: "command_proposal",
		Proposal: &agent.Proposal{
			ID:      "p-long",
			Command: "journalctl -u nginx --since '30 minutes ago' --no-pager | grep -Ei 'error|timeout' && systemctl status nginx --no-pager",
			Risk:    "read_only",
		},
	}, nil)
	output := application.renderInlineAI()
	for _, want := range []string{"AI 建议命令", "[只读]", "$ journalctl", "↳ | grep", "yes", "no"} {
		if !strings.Contains(output, want) {
			t.Fatalf("long proposal missing %q: %q", want, output)
		}
	}
}

func TestProposalReanchorsAfterHeavySSHOutput(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 64
	application.contentRows = 12
	application.rows = 14
	application.atPrompt = true
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte(strings.Repeat("old output\r\n", application.contentRows-1) + "root@server:~# "))
	cursor := application.virtual.Cursor()
	state := &inlineAIState{
		Prompt: "分析刚才的大量输出", AnchorX: cursor.X, AnchorY: cursor.Y,
		Status: "正在分析", Started: time.Now(),
	}
	application.inlineAI = state
	application.agentInline = state
	application.agentBusy = true
	_, _, _ = application.renderTerminalWorkspace()
	if application.terminalRows == nil {
		t.Fatal("initial loading frame did not seed terminal row cache")
	}

	application.scrollInlineBlocks(application.contentRows + 8)
	if state.AnchorY >= 0 {
		t.Fatalf("test did not move inline anchor offscreen: %d", state.AnchorY)
	}
	application.finishAgentTurn(agent.Turn{
		Type: "command_proposal",
		Proposal: &agent.Proposal{
			ID: "review-after-output", Command: "systemctl restart nginx", Explanation: "重启服务", Risk: "high",
		},
	}, nil)

	if state.AnchorY != application.virtual.Cursor().Y {
		t.Fatalf("proposal anchor=%d cursor=%d", state.AnchorY, application.virtual.Cursor().Y)
	}
	if application.terminalRows != nil {
		t.Fatal("proposal transition did not force a clean terminal redraw")
	}
	if !application.inlineApprovalAvailable() {
		t.Fatal("reanchored command review is not fully visible")
	}
	_, _, _ = application.renderTerminalWorkspace()
	visible := cachedTerminalRows(application.terminalRows, application.screenRows())
	for _, want := range []string{"AI 建议命令", "systemctl restart nginx", "yes", "no"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("reanchored review missing %q: %q", want, visible)
		}
	}
}

func TestOffscreenInlineReviewFallsBackToApprovalCard(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 64
	application.contentRows = 12
	application.rows = 14
	application.proposal = &agent.Proposal{ID: "fallback", Command: "reboot", Risk: "high"}
	application.inlineAI = &inlineAIState{
		Prompt: "旧问题", AnchorX: 0, AnchorY: -20,
		Proposal: application.proposal,
	}
	if application.inlineApprovalAvailable() {
		t.Fatal("offscreen inline review should not suppress the fallback card")
	}
	card := application.renderApprovalCard()
	for _, want := range []string{"AI 命令确认", "reboot", "yes", "no"} {
		if !strings.Contains(card, want) {
			t.Fatalf("fallback card missing %q: %q", want, card)
		}
	}
}

func TestAIControlsOwnPanels(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.applyPanelAction(agent.PanelAction{
		Action: "open",
		Panel: agent.PanelSpec{
			ID:         "cpu",
			Type:       "monitor",
			Title:      "高 CPU 进程",
			Collector:  "process.top",
			IntervalMS: 3000,
		},
	})
	created := application.panels.ByID("cpu")
	if created == nil || created.Origin != panel.OriginAI || created.Command == "" {
		t.Fatalf("created = %#v", created)
	}

	application.applyPanelAction(agent.PanelAction{
		Action: "open",
		Panel: agent.PanelSpec{
			ID:      "dangerous",
			Type:    "monitor",
			Title:   "危险",
			Command: "systemctl restart nginx",
		},
	})
	if application.panels.ByID("dangerous") != nil {
		t.Fatal("mutating AI monitor command should be rejected")
	}
}

func TestAIReportPanelIsIgnored(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.applyPanelAction(agent.PanelAction{
		Action: "open",
		Panel: agent.PanelSpec{
			ID:      "report",
			Type:    "report",
			Content: "# 调查报告\n\n根因：上游超时",
		},
	})
	if application.panels.ByID("report") != nil {
		t.Fatal("AI report panel should not be created")
	}
}
