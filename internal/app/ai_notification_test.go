package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
)

func TestAICompletionNotifiesOnceAfterStreamingOnEverySurface(t *testing.T) {
	for _, surface := range []string{"sidebar", "inline", "collapsed", "execution"} {
		t.Run(surface, func(t *testing.T) {
			a, err := New(Config{SSHArgs: []string{"server"}, Language: "zh"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.cancel)
			a.screenOut = io.Discard
			a.agentBusy = true
			a.sidebarCollapsed = surface == "collapsed"
			if surface == "inline" || surface == "collapsed" || surface == "execution" {
				a.inlineAI = &inlineAIState{Prompt: "检查服务"}
				a.agentInline = a.inlineAI
			}
			calls := 0
			a.notifyAI = func(title, message string) {
				calls++
				if title != "aissh · AI 已完成" || !strings.HasPrefix(message, "server\n") {
					t.Fatalf("notification=%q %q", title, message)
				}
				if strings.Contains(message, "private answer") {
					t.Fatal("desktop notification exposed the AI answer")
				}
				if a.agentBusy || (a.inlineAI != nil && !a.inlineAI.Done) {
					t.Fatal("notified before the result was ready")
				}
			}
			a.handleEvent(agentStatusEvent{status: "thinking"})
			a.handleEvent(agentThoughtEvent{content: "analysis"})
			a.handleEvent(agentAnswerEvent{content: "private answer"})
			if calls != 0 {
				t.Fatal("streaming triggered an early notification")
			}
			turn := agent.Turn{Type: "finished", Message: "private answer"}
			if surface == "execution" {
				a.handleEvent(executionResultEvent{turn: turn})
			} else {
				a.handleEvent(agentTurnEvent{turn: turn})
			}
			if calls != 1 || !strings.HasPrefix(a.statusLine(100), "AI 已完成 · ") {
				t.Fatalf("calls=%d status=%q", calls, a.statusLine(100))
			}
			a.handleEvent(agentStatusEvent{status: ""})
			if calls != 1 {
				t.Fatal("status update repeated the notification")
			}
			a.handleInput([]byte{0x1d}) // A local shortcut acknowledges the notice.
			if a.aiNotice != "" || !a.dirty {
				t.Fatal("input did not dismiss and redraw the status notice")
			}
		})
	}
}

func TestDesktopTaskNoticesWithoutOSNotifier(t *testing.T) {
	for _, test := range []struct {
		name string
		turn agent.Turn
		err  error
		kind string
	}{
		{"completed", agent.Turn{Type: "finished", Message: "private answer"}, nil, "completed"},
		{"failed", agent.Turn{Type: "failed", Error: "private error"}, nil, "failed"},
		{"request error", agent.Turn{}, errors.New("private transport error"), "failed"},
		{"approval", agent.Turn{Proposal: &agent.Proposal{ID: "p", Command: "private command"}}, nil, "approval"},
		{"reply", agent.Turn{Type: "ask_user", Message: "private question"}, nil, "reply"},
		{"canceled", agent.Turn{}, context.Canceled, ""},
		{"approved", agent.Turn{Type: "approved"}, nil, ""},
		{"rejected", agent.Turn{Type: "rejected"}, nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := desktopTestApp(t)
			a.notifyAI = nil // macOS/Linux have no native AI notifier.
			a.agentBusy = true
			a.handleEvent(agentAnswerEvent{content: "private streamed text"})
			if len(a.desktopState().Notices) != 0 {
				t.Fatal("streaming produced a completion notice")
			}
			a.handleEvent(agentTurnEvent{turn: test.turn, err: test.err})
			notices := a.desktopState().Notices
			if test.kind == "" {
				if len(notices) != 0 {
					t.Fatalf("unexpected notice: %+v", notices)
				}
				return
			}
			if len(notices) != 1 || notices[0].Kind != test.kind || notices[0].Sequence != 1 {
				t.Fatalf("notices=%+v", notices)
			}
			if _, err := time.Parse(time.RFC3339Nano, notices[0].Time); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(notices)
			if strings.Contains(string(data), "private") {
				t.Fatalf("task notice exposed content: %s", data)
			}
			a.handleEvent(agentStatusEvent{status: ""})
			if len(a.desktopState().Notices) != 1 {
				t.Fatal("status snapshot repeated notice")
			}
		})
	}
}

func TestDesktopCommandRemindsForManualReviewButNotExecution(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		name := "manual approval"
		if automatic {
			name = "automatic approval"
		}
		t.Run(name, func(t *testing.T) {
			a := desktopTestApp(t)
			a.config.AlwaysYes = automatic
			a.agent = &alwaysYesTestProvider{}
			a.atPrompt = true
			a.sshInput = newSSHInputWriter(a.ctx, io.Discard, a.events)
			a.finishAgentTurn(agent.Turn{Proposal: &agent.Proposal{ID: "p", Command: "uptime", Risk: "read_only"}}, nil)
			before := len(a.desktopState().Notices)
			if automatic && before != 0 {
				t.Fatal("automatic risk review produced a reminder")
			}
			if !automatic {
				if before != 1 || a.desktopNotices[0].Kind != "approval" {
					t.Fatal("manual review reminder missing")
				}
				a.approveProposal()
			}
			deadline := time.After(time.Second)
			for a.execution == nil {
				select {
				case event := <-a.events:
					a.handleEvent(event)
				case <-deadline:
					t.Fatal("command did not start")
				}
			}
			awaitExecutionWrite(t, a)
			a.handleEvent(agentStatusEvent{status: ""})
			state := a.desktopState()
			if !state.Executing || len(state.Notices) != before {
				t.Fatalf("command execution produced an extra reminder: %+v", state.Notices)
			}
		})
	}
}

func TestDesktopTaskNoticesWaitForFinalOutcomeAndStayBounded(t *testing.T) {
	a := desktopTestApp(t)
	a.agentBusy = true
	a.notifyAgentTurn(agent.Turn{Type: "finished"}, nil)
	a.agentBusy = false
	a.execution = &executionState{}
	a.notifyAgentTurn(agent.Turn{Type: "finished"}, nil)
	a.execution = nil
	a.executionRecovery = true
	a.notifyAgentTurn(agent.Turn{Type: "finished"}, nil)
	a.executionRecovery = false
	if len(a.desktopState().Notices) != 0 {
		t.Fatal("task still running produced a notice")
	}
	a.handleEvent(executionResultEvent{turn: agent.Turn{Type: "finished"}})
	snapshot := a.desktopState()
	for i := 0; i < 25; i++ {
		a.notifyAgentTurn(agent.Turn{Type: "finished"}, nil)
	}
	current := a.desktopState().Notices
	if len(current) != 20 || current[0].Sequence != 7 || current[19].Sequence != 26 || len(snapshot.Notices) != 1 || snapshot.Notices[0].Sequence != 1 {
		t.Fatalf("unbounded or mutated history: old=%+v current=%+v", snapshot.Notices, current)
	}
	a.cancel()
	a.notifyAgentTurn(agent.Turn{Type: "finished"}, nil)
	if a.desktopNoticeSequence != 26 {
		t.Fatal("shutdown produced a notice")
	}
}

func TestAITurnNotificationDistinguishesOutcomes(t *testing.T) {
	for _, test := range []struct {
		name string
		turn agent.Turn
		err  error
		want string
	}{
		{"finished", agent.Turn{Type: "finished"}, nil, "AI finished"},
		{"approval", agent.Turn{Proposal: &agent.Proposal{ID: "p1", Command: "uptime"}}, nil, "AI awaiting command approval"},
		{"question", agent.Turn{Type: "ask_user"}, nil, "AI awaiting your reply"},
		{"transport error", agent.Turn{}, errors.New("offline"), "AI request failed"},
		{"provider error", agent.Turn{Error: "failed"}, nil, "AI request failed"},
		{"failed turn", agent.Turn{Type: "failed"}, nil, "AI request failed"},
		{"timeout", agent.Turn{}, context.DeadlineExceeded, "AI request failed"},
		{"canceled", agent.Turn{}, context.Canceled, ""},
		{"approved", agent.Turn{Type: "approved"}, nil, ""},
		{"rejected", agent.Turn{Type: "rejected"}, nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, err := New(Config{SSHArgs: []string{"server"}, Language: "en"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.cancel)
			var titles []string
			a.notifyAI = func(title, _ string) { titles = append(titles, title) }
			a.agentBusy = true
			a.handleEvent(agentTurnEvent{turn: test.turn, err: test.err})
			if test.want == "" {
				if len(titles) != 0 || a.aiNotice != "" {
					t.Fatalf("unexpected notification: %q", titles)
				}
			} else if len(titles) != 1 || titles[0] != "aissh · "+test.want || a.aiNotice != test.want {
				t.Fatalf("titles=%q status=%q, want %q", titles, a.aiNotice, test.want)
			}
		})
	}
}

func TestAlwaysYesNotificationWaitsForReviewOutcome(t *testing.T) {
	for _, risk := range []string{"read_only", "high", "error"} {
		t.Run(risk, func(t *testing.T) {
			a, err := New(Config{SSHArgs: []string{"server"}, AlwaysYes: true, Language: "en"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.cancel)
			provider := &alwaysYesTestProvider{risks: []string{risk}}
			if risk == "error" {
				provider.errAt = 1
			}
			a.agent = provider
			a.atPrompt = true
			var titles []string
			a.notifyAI = func(title, _ string) { titles = append(titles, title) }
			a.finishAgentTurn(agent.Turn{Proposal: &agent.Proposal{ID: "p1", Command: "uptime", Risk: "read_only"}}, nil)
			if !a.agentBusy || len(titles) != 0 {
				t.Fatal("notification interrupted an automatic risk review")
			}
			deadline := time.After(time.Second)
			for {
				select {
				case event := <-a.events:
					a.handleEvent(event)
					if _, done := unwrapAgentTestEvent(event).(alwaysYesRiskEvent); !done {
						continue
					}
					if risk == "read_only" {
						if !a.agentBusy || len(titles) != 0 {
							t.Fatal("automatic approval triggered a premature notification")
						}
					} else if a.agentBusy || len(titles) != 1 || titles[0] != "aissh · AI awaiting command approval" {
						t.Fatalf("manual approval did not notify: %q", titles)
					}
					return
				case <-deadline:
					t.Fatal("risk review did not finish")
				}
			}
		})
	}
}

func TestAICompletionDoesNotNotifyDuringShutdownOrExecution(t *testing.T) {
	for _, state := range []string{"shutdown", "execution", "disabled"} {
		t.Run(state, func(t *testing.T) {
			a, err := New(Config{SSHArgs: []string{"server"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.cancel)
			if state != "disabled" {
				a.notifyAI = func(string, string) { t.Fatal("unexpected notification") }
			}
			if state == "shutdown" {
				a.cancel()
			} else if state == "execution" {
				a.execution = &executionState{}
			}
			a.finishAgentTurn(agent.Turn{Type: "finished"}, nil)
			if a.aiNotice != "" {
				t.Fatalf("unexpected notice: %q", a.aiNotice)
			}
		})
	}
}
