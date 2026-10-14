package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
)

func unwrapAgentTestEvent(event interface{}) interface{} {
	if scoped, ok := event.(agentOperationEvent); ok {
		return scoped.event
	}
	return event
}

func TestManualTakeoverReturnsQToTerminalRegardlessOfMenuText(t *testing.T) {
	const menu = "\r\n请选择授权流程:\r\nA>管理员二次认证\r\n输入退出取消当前请求\r\n"
	for _, desktop := range []bool{false, true} {
		for _, monitoring := range []bool{false, true} {
			t.Run(fmt.Sprintf("desktop=%t/monitoring=%t", desktop, monitoring), func(t *testing.T) {
				a, requests := executionTestApp(t)
				a.config.DesktopUI = desktop
				a.screenOut = &bytes.Buffer{}
				terminal := &interruptCapturePTY{}
				a.sshPTY = terminal
				a.agentBusy = true
				a.focus = focusAI
				a.inlineAI = &inlineAIState{}
				a.gatewayShellPrompt = true
				if monitoring {
					a.execution = nil
					a.inBandMonitor = &inBandMonitorState{panelID: "system", marker: "__TEST_MONITOR_", pendingInput: []byte("old input"), writeJob: &sshWriteJob{}}
					a.monitorAwaitingPrompt = true
				}
				for _, b := range []byte(menu) {
					a.processSSHOutput([]byte{b})
				}
				if a.terminalTakeover {
					t.Fatal("menu text unexpectedly triggered terminal takeover")
				}
				if desktop {
					if err := a.handleDesktopCommand(credentialui.WorkbenchCommand{Kind: "terminal-takeover", Deadline: time.Now().Add(time.Second).UnixMilli()}); err != nil {
						t.Fatal(err)
					}
				} else {
					a.handleInput([]byte{7})
				}
				if !a.terminalTakeover || a.agentBusy || a.execution != nil || a.executionRecovery || a.inBandMonitor != nil || a.monitorAwaitingPrompt || a.focus != focusTerminal {
					t.Fatal("manual takeover did not release terminal")
				}
				a.handleInput([]byte("Q\r"))
				if terminal.String() != "Q\r" || len(a.monitorDeferredInput) != 0 {
					t.Fatalf("input=%q deferred=%q", terminal.String(), a.monitorDeferredInput)
				}
				a.startAI("不要自动继续", false)
				if a.agentBusy || len(requests) > 0 {
					t.Fatal("AI resumed inside approval menu")
				}
				a.processSSHOutput([]byte("\r\n[deployer@node ~]$ "))
				if a.terminalTakeover || !a.atPrompt || !a.monitorPrimaryPaused {
					t.Fatal("fresh shell did not restore AI safely")
				}
			})
		}
	}
}

type takeoverBlockingProvider struct{ started, stopped chan struct{} }

func (p takeoverBlockingProvider) Name() string     { return "blocking" }
func (p takeoverBlockingProvider) Configured() bool { return true }
func (p takeoverBlockingProvider) Action(context.Context, agent.Request) (agent.Turn, error) {
	return agent.Turn{}, nil
}
func (p takeoverBlockingProvider) Stream(ctx context.Context, _ agent.Request, cb agent.Callbacks) (agent.Turn, error) {
	close(p.started)
	<-ctx.Done()
	if cb.OnAnswer != nil {
		cb.OnAnswer("late answer")
	}
	close(p.stopped)
	return agent.Turn{Proposal: &agent.Proposal{ID: "late", Command: "echo unwanted"}}, nil
}

func TestTerminalTakeoverCancelsAIAndIgnoresLateApproval(t *testing.T) {
	a := desktopTestApp(t)
	provider := takeoverBlockingProvider{started: make(chan struct{}), stopped: make(chan struct{})}
	a.agent = provider
	a.startAI("question", false)
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("AI did not start")
	}
	generation := a.agentGeneration
	if err := a.handleDesktopCommand(credentialui.WorkbenchCommand{Kind: "terminal-takeover", Deadline: time.Now().Add(time.Second).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.stopped:
	case <-time.After(time.Second):
		t.Fatal("AI context was not cancelled")
	}
	a.handleEvent(agentOperationEvent{generation: generation, event: approvalEvent{command: "echo unwanted"}})
	a.handleEvent(agentOperationEvent{generation: generation, event: agentTurnEvent{turn: agent.Turn{Proposal: &agent.Proposal{ID: "late"}}}})
	if a.agentBusy || a.proposal != nil || a.execution != nil || !a.desktopState().TerminalTakeover {
		t.Fatal("late AI result retook control")
	}
	for _, m := range a.chat {
		if strings.Contains(m.Content, "late answer") {
			t.Fatal("cancelled callback reached chat")
		}
	}
}

func TestTerminalTakeoverShortcutPreservesRemoteInput(t *testing.T) {
	a := desktopTestApp(t)
	terminal := &interruptCapturePTY{}
	a.sshPTY = terminal
	a.agentBusy = true
	a.handleInput([]byte{7})
	a.handleInput([]byte("Q\r"))
	if terminal.String() != "Q\r" || !a.terminalTakeover {
		t.Fatalf("input=%q takeover=%v", terminal.String(), a.terminalTakeover)
	}
}
