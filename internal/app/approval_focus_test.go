package app

import (
	"fmt"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/terminalview"
)

func pendingApprovalTestApp(t *testing.T, inline bool) (*App, *interruptCapturePTY) {
	t.Helper()
	a := mouseTestApp(t)
	t.Cleanup(a.cancel)
	a.agent = approvalTestProvider{}
	a.atPrompt = true
	a.proposal = &agent.Proposal{ID: "pending", Command: "echo approved", Risk: "high"}
	a.focus = focusApproval
	a.terminalHistory = make([]terminalview.Line, 100)
	if inline {
		a.inlineAI = &inlineAIState{Prompt: "check", AnchorY: 1, Proposal: a.proposal}
		if !a.inlineApprovalAvailable() {
			t.Fatal("inline approval is not visible")
		}
	}
	terminal := &interruptCapturePTY{}
	a.sshPTY = terminal
	return a, terminal
}

func TestApprovalRetainsInputAfterTerminalScroll(t *testing.T) {
	for _, inline := range []bool{false, true} {
		for _, compact := range []bool{false, true} {
			for _, action := range []string{"wheel", "scrollbar-click", "scrollbar-drag"} {
				t.Run(fmt.Sprintf("inline=%t/compact=%t/%s", inline, compact, action), func(t *testing.T) {
					a, terminal := pendingApprovalTestApp(t, inline)
					a.compact = compact
					switch action {
					case "wheel":
						a.handleInput([]byte("\x1b[<64;10;5M"))
					case "scrollbar-click":
						a.handleLeftClick(a.terminalScrollbarCol(), 2)
					case "scrollbar-drag":
						a.handleMouse(mouseEvent{Button: 0, X: a.terminalScrollbarCol(), Y: a.rows - 1})
						a.handleMouse(mouseEvent{Button: 32, X: a.terminalScrollbarCol(), Y: 2, Motion: true})
					}
					if a.focus != focusApproval || a.terminalScroll == 0 {
						t.Fatalf("after scroll: focus=%v scroll=%d", a.focus, a.terminalScroll)
					}
					if a.inlineApprovalAvailable() {
						t.Fatal("hidden inline approval prevented the fallback card from rendering")
					}
					for _, key := range []string{"y", "e", "s"} {
						a.handleInput([]byte(key))
					}
					if got := string(a.approvalInput); got != "yes" || terminal.Len() != 0 {
						t.Fatalf("approval input=%q SSH input=%q", got, terminal.String())
					}
					a.handleInput([]byte("\r"))
					select {
					case event := <-a.events:
						if _, ok := unwrapAgentTestEvent(event).(approvalEvent); !ok {
							t.Fatalf("expected approval, got %T", event)
						}
					case <-time.After(time.Second):
						t.Fatal("yes did not request approval")
					}
					if terminal.Len() != 0 {
						t.Fatalf("confirmation leaked to SSH: %q", terminal.String())
					}
				})
			}
		}
	}
}

func TestApprovalRegainsFocusFromSidebar(t *testing.T) {
	for _, inline := range []bool{false, true} {
		for _, status := range []string{"", "manual confirmation required"} {
			for _, action := range []string{"input-click", "terminal-click", "ai-escape", "monitor-escape", "menu-escape", "custom-escape"} {
				t.Run(fmt.Sprintf("inline=%t/status=%s/%s", inline, status, action), func(t *testing.T) {
					a, terminal := pendingApprovalTestApp(t, inline)
					a.approvalStatus = status
					a.focus = focusAI
					switch action {
					case "input-click":
						top, left, _, height := a.approvalGeometry()
						x, y := left+2, top+height-2
						if inline {
							y, x, _ = a.inlineApprovalCursor()
						}
						clickMouse(a, x, y)
					case "terminal-click":
						clickMouse(a, 1, 2)
					default:
						switch action {
						case "monitor-escape":
							a.focus = focusMonitor
						case "menu-escape":
							a.focus = focusPanelMenu
						case "custom-escape":
							a.focus = focusCustomPanel
						}
						a.handleInput([]byte{0x1b})
					}
					if a.focus != focusApproval {
						t.Fatalf("focus=%v, want approval", a.focus)
					}
					a.handleInput([]byte("no\r"))
					select {
					case event := <-a.events:
						if _, ok := unwrapAgentTestEvent(event).(rejectionEvent); !ok {
							t.Fatalf("expected rejection, got %T", event)
						}
					case <-time.After(time.Second):
						t.Fatal("no did not request rejection")
					}
					if terminal.Len() != 0 {
						t.Fatalf("confirmation leaked to SSH: %q", terminal.String())
					}
				})
			}
		}
	}
}

func TestPendingApprovalGuardsTerminalInput(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%t", busy), func(t *testing.T) {
			a, terminal := pendingApprovalTestApp(t, false)
			a.focus = focusTerminal // Simulate a stale focus from an asynchronous event.
			a.agentBusy = busy
			a.handleInput([]byte("yes\r"))
			if terminal.Len() != 0 || a.focus != focusApproval {
				t.Fatalf("focus=%v SSH input=%q", a.focus, terminal.String())
			}
		})
	}
}

func TestHiddenApprovalDoesNotCaptureCompactSidebarClick(t *testing.T) {
	a, _ := pendingApprovalTestApp(t, false)
	a.compact = true
	a.focus = focusAI
	top, left, _, height := a.approvalGeometry()
	clickMouse(a, left+2, top+height-2)
	if a.focus != focusAI {
		t.Fatalf("hidden approval stole focus: %v", a.focus)
	}
}
