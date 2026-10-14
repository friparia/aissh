package app

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
)

type recoveryTestPTY struct{ bytes.Buffer }

func (*recoveryTestPTY) Close() error { return nil }

func TestKnownGatewayShellRecoversInAlternateScreen(t *testing.T) {
	a, requests := executionTestApp(t)
	a.sshPTY = &recoveryTestPTY{}
	execution := a.execution
	a.execution = nil
	a.processSSHOutput([]byte("\x1b[?1049h[root@node ~]# "))
	if !a.atPrompt || !a.gatewayShellPrompt {
		t.Fatal("gateway shell was not recognised before execution")
	}
	a.execution = execution
	a.atPrompt = false
	a.processSSHOutput([]byte("\r\ncommand still running\r\n"))
	a.handleInput([]byte{3})
	if !a.executionRecovery || a.atPrompt {
		t.Fatal("interrupt did not wait for a fresh shell prompt")
	}
	a.processSSHOutput([]byte("\r\n[root@node ~]# "))
	if a.executionRecovery || a.execution != nil || !a.atPrompt {
		t.Fatal("known gateway shell failed to recover in its alternate screen")
	}
	select {
	case request := <-requests:
		if request.Completion != "cancelled; exit_status=unknown" {
			t.Fatalf("unexpected completion: %q", request.Completion)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled command result was not reported")
	}
}

func TestCtrlCRecoversMissingMarkerAndReportsUnknownOnce(t *testing.T) {
	a, requests := executionTestApp(t)
	terminal := &recoveryTestPTY{}
	a.sshPTY = terminal
	a.processSSHOutput([]byte("OUTPUT BEFORE CANCEL\r\n[root@WRDNMS wrd]# "))
	a.refreshPromptFromScreen()
	if a.atPrompt || a.execution == nil {
		t.Fatal("ordinary prompt-looking output ended execution")
	}
	a.handleInput([]byte{3})
	a.refreshPromptFromScreen()
	if a.atPrompt || !a.executionRecovery {
		t.Fatal("stale screen prompt bypassed resynchronization")
	}
	a.proposal = &agent.Proposal{ID: "next", Command: "echo next"}
	a.approveProposal()
	if a.agentBusy {
		t.Fatal("approval started before a fresh prompt")
	}
	a.proposal = nil
	a.handleInput([]byte{3})
	a.processSSHOutput([]byte("\r\n[root@WRD"))
	if a.atPrompt {
		t.Fatal("partial prompt enabled input")
	}
	a.processSSHOutput([]byte("NMS wrd]# "))
	if a.execution != nil || a.executionRecovery || !a.atPrompt {
		t.Fatalf("did not recover: execution=%+v recovery=%v prompt=%v", a.execution, a.executionRecovery, a.atPrompt)
	}
	select {
	case request := <-requests:
		if request.Completion != "cancelled; exit_status=unknown" || !strings.Contains(request.Output, "OUTPUT BEFORE CANCEL") {
			t.Fatalf("result=%+v", request)
		}
		if strings.Count(request.Output, "[root@WRDNMS wrd]#") != 1 {
			t.Fatal("recovery prompt leaked into the cancelled command's output")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled result was not submitted")
	}
	// Finish analysis, then verify the next Chinese line is captured locally.
	select {
	case event := <-a.events:
		a.handleEvent(event)
	case <-time.After(time.Second):
		t.Fatal("analysis did not finish")
	}
	a.handleInput([]byte("测试"))
	if !a.aiLineCapture || string(a.lineInput) != "测试" || terminal.String() != "\x03\x03" {
		t.Fatalf("Chinese input leaked: capture=%v input=%q terminal=%q", a.aiLineCapture, a.lineInput, terminal.String())
	}
	a.submitExecutionResult()
	select {
	case request := <-requests:
		t.Fatalf("duplicate result: %+v", request)
	default:
	}
	a.handleInput([]byte{3}) // Clear the locally captured Chinese line.
	a.proposal = &agent.Proposal{ID: "next", Command: "echo next"}
	a.approveProposal()
	select {
	case event := <-a.events:
		a.handleEvent(event)
	case <-time.After(time.Second):
		t.Fatal("approval did not resume after recovery")
	}
	awaitExecutionWrite(t, a)
	if a.execution == nil || a.execution.Command != "echo next" || !strings.HasSuffix(terminal.String(), approvedShellInput("echo next", "")) {
		t.Fatal("next approved command was not sent after recovery")
	}
}

func TestLateCancelledMarkerCannotFinishNextCommand(t *testing.T) {
	a, requests := executionTestApp(t)
	a.sshPTY = &recoveryTestPTY{}
	a.handleInput([]byte{3})
	a.processSSHOutput([]byte("root@server:~# "))
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("missing cancelled result")
	}
	a.agentBusy = false
	a.atPrompt = false
	a.execution = &executionState{Marker: "next", Command: "next", Output: newContextBuffer(4096)}
	late := executionMarkerPrefix("test") + "0\a"
	for _, b := range []byte(late + late + "NEXT OUTPUT\r\n") {
		a.processSSHOutput([]byte{b})
	}
	if a.execution == nil || a.execution.finished() || a.atPrompt || strings.Contains(a.execution.Output.ContextString(), "aissh-test") {
		t.Fatalf("late marker corrupted next execution: %+v", a.execution)
	}
	a.processSSHOutput([]byte(executionMarkerPrefix("next") + "7\aroot@server:~# "))
	select {
	case request := <-requests:
		if request.Completion != "exit_status=7" || request.Output != "NEXT OUTPUT" {
			t.Fatalf("next result=%+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("next result missing")
	}
}

func TestExecutionWriteFailureRequiresFreshPromptAndKeepsUnknownStatus(t *testing.T) {
	a, requests := executionTestApp(t)
	target := &shortSSHWriter{limit: 7, err: io.ErrClosedPipe}
	a.sshInput = newSSHInputWriter(a.ctx, target, a.events)
	a.sendApprovedCommand(a.execution)
	awaitExecutionWrite(t, a)
	if !a.executionRecovery || a.execution.ExitCode != nil || a.execution.Completion != "transport_error" || a.atPrompt {
		t.Fatalf("write failure left invalid state: %+v", a.execution)
	}
	a.processSSHOutput([]byte("root@server:~# "))
	select {
	case request := <-requests:
		if request.Completion != "transport_error; exit_status=unknown" {
			t.Fatalf("transport error fabricated an exit code: %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("transport result missing")
	}
}

func TestRecoveryProtectsPasswordAndFullscreenInput(t *testing.T) {
	for _, output := range []string{"\r\nPassword: ", "\x1b[?1049hroot@server:~# ", "\x1b[?25lroot@server:~# "} {
		t.Run(output, func(t *testing.T) {
			a, _ := executionTestApp(t)
			a.sshPTY = &recoveryTestPTY{}
			a.handleInput([]byte{3})
			a.processSSHOutput([]byte(output))
			a.refreshPromptFromScreen()
			if a.atPrompt || !a.executionRecovery {
				t.Fatal("unsafe screen restored a shell prompt")
			}
			a.handleInput([]byte("密码"))
			if a.aiLineCapture {
				t.Fatal("password or full-screen input was captured by AI")
			}
			restore := "\x1b[?25h\r\nroot@server:~# "
			if strings.Contains(output, "?1049h") {
				restore = "\x1b[?1049l" + restore
			}
			a.processSSHOutput([]byte(restore))
			select {
			case event := <-a.events:
				a.handleEvent(event)
			case <-time.After(time.Second):
				t.Fatalf("result analysis did not finish: execution=%+v recovery=%v sensitive=%v safe=%v tail=%q", a.execution, a.executionRecovery, a.sensitive, a.safePromptScreen(), a.outputTail.String())
			}
			a.handleInput([]byte("测试"))
			if !a.aiLineCapture || string(a.lineInput) != "测试" {
				t.Fatalf("old application input polluted AI input after recovery: %q", a.lineInput)
			}
		})
	}
}

func TestExecutionWaitNoticeDoesNotCompleteOrInjectInput(t *testing.T) {
	a, _ := executionTestApp(t)
	terminal := &recoveryTestPTY{}
	a.sshPTY = terminal
	a.execution.StartedAt = time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		a.checkExecutionWait(time.Now())
		a.processSSHOutput([]byte("\r\nordinary output # "))
	}
	if a.execution.finished() || a.atPrompt || terminal.Len() != 0 {
		t.Fatal("timeout or prompt-looking output completed or interrupted a command")
	}
	if !a.execution.WaitWarned || !strings.Contains(a.inputStatus(), "Ctrl+C") {
		t.Fatal("missing recovery hint")
	}
}

func TestRecoveryRetainsPromptThatArrivesBeforeInterruptWriteReturns(t *testing.T) {
	a, requests := executionTestApp(t)
	a.stopExecutionWait("cancelled")
	job := &sshWriteJob{data: []byte{3}, marker: "test", interrupt: true}
	job.started.Store(true)
	a.executionInterrupt = job
	a.processSSHOutput([]byte("\r\nroot@server:~# "))
	if a.atPrompt || !a.executionRecovery || !a.executionRecoveryOutput {
		t.Fatal("prompt was lost or recovery completed before the write returned")
	}
	job.done.Store(true)
	a.handleEvent(sshWriteEvent{job: job, written: 1})
	if !a.atPrompt || a.executionRecovery || a.execution != nil {
		t.Fatal("write acknowledgement did not recover the retained prompt")
	}
	select {
	case request := <-requests:
		if request.Completion != "cancelled; exit_status=unknown" {
			t.Fatalf("result=%+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("missing cancelled result")
	}
}
