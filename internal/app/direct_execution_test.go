package app

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestApprovedShellInputMatchesITermExecutionPaths(t *testing.T) {
	for _, tc := range []struct{ command, marker, want string }{
		{"pwd", "", "pwd\r"},
		{"pwd\r\nls", "", "eval 'pwd\nls'\r"},
		{"pwd", "token", "eval 'pwd'; echo '-- FINISHED' 'token'\r"},
		{"echo 'hello' # comment", "token", "eval 'echo '\"'\"'hello'\"'\"' # comment'; echo '-- FINISHED' 'token'\r"},
	} {
		if got := approvedShellInput(tc.command, tc.marker); got != tc.want {
			t.Fatalf("command=%q marker=%q got=%q want=%q", tc.command, tc.marker, got, tc.want)
		}
	}
}

func TestMultilineEvalRunsInCurrentShell(t *testing.T) {
	input := approvedShellInput("aissh_test_value='hello world'\necho \"$aissh_test_value\"", "")
	cmd := exec.Command("sh")
	cmd.Stdin = strings.NewReader(strings.TrimSuffix(input, "\r") + "\nprintf '%s' \"$aissh_test_value\"\n")
	output, err := cmd.Output()
	if err != nil || string(output) != "hello world\nhello world" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestFallbackEchoReceiptSurvivesSplitReadsAndRejectsCommandEcho(t *testing.T) {
	marker := "test-random-token"
	data := approvedShellInput("pwd # trailing comment", marker) + "\n/root\r\n-- FINISHED " + marker + "\r\nroot@node:~# "
	for split := 0; split <= len(data); split++ {
		state := &executionState{Marker: marker, EchoMarker: true}
		var output, after []byte
		for _, part := range []string{data[:split], data[split:]} {
			if state.ReceiptReceived {
				after = append(after, part...)
				continue
			}
			visible, rest, code := state.consume([]byte(part))
			if code != nil {
				t.Fatal("echo receipt invented an exit code")
			}
			output = append(output, visible...)
			after = append(after, rest...)
		}
		if !state.ReceiptReceived || !strings.HasSuffix(string(output), "/root\r\n") || string(after) != "root@node:~# " {
			t.Fatalf("split=%d completion=%q output=%q after=%q", split, state.Completion, output, after)
		}
	}
}

func TestFallbackReceiptCompletesExecutionAndFiltersLateCopies(t *testing.T) {
	a, requests := executionTestApp(t)
	a.sshPTY = &recoveryTestPTY{}
	state := a.execution
	a.sendApprovedCommand(state)
	awaitExecutionWrite(t, a)
	if state.Direct || !state.EchoMarker {
		t.Fatal("unrecognised prompt did not select echo fallback")
	}
	a.processSSHOutput([]byte("RESULT\r\n-- FINISHED test\r\nroot@node:~# "))
	select {
	case request := <-requests:
		if request.Completion != "completion_marker_received; exit_status=unknown" || request.Output != "RESULT" {
			t.Fatalf("result=%+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("echo receipt did not complete execution")
	}
	if got := string(a.filterRetiredExecutionMarkers([]byte("-- FINISHED test\r\nAFTER"))); got != "AFTER" {
		t.Fatalf("late receipt leaked: %q", got)
	}
}

func TestFallbackReceiptAfterInterruptPreservesCancelledResult(t *testing.T) {
	a, requests := executionTestApp(t)
	a.sshPTY = &recoveryTestPTY{}
	a.sendApprovedCommand(a.execution)
	awaitExecutionWrite(t, a)
	a.handleInput([]byte{3})
	deadline := time.After(time.Second)
	for a.executionInterrupt != nil && !a.executionInterrupt.done.Load() {
		select {
		case event := <-a.events:
			a.handleEvent(event)
		case <-deadline:
			t.Fatal("interrupt did not drain")
		}
	}
	a.processSSHOutput([]byte("\r\n-- FINISHED test\r\nroot@node:~# "))
	select {
	case request := <-requests:
		if request.Completion != "cancelled; exit_status=unknown" || strings.Contains(request.Output, "-- FINISHED") {
			t.Fatalf("cancelled result=%+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled result was not reported")
	}
}

func TestDirectExecutionSendsApprovedCommandAndWaitsForFreshPrompt(t *testing.T) {
	for _, alt := range []bool{false, true} {
		a, requests := executionTestApp(t)
		state := a.execution
		a.execution = nil
		prefix := ""
		if alt {
			prefix = "\x1b[?1049h"
		}
		a.processSSHOutput([]byte(prefix + "[deployer@node ~]$ "))
		a.execution = state
		a.atPrompt = false
		terminal := &recoveryTestPTY{}
		a.sshPTY = terminal
		state.Command = "ps -eo pid,%cpu,comm --sort=-%cpu | head -n 11"
		a.sendApprovedCommand(state)
		awaitExecutionWrite(t, a)
		if terminal.String() != state.Command+"\r" {
			t.Fatalf("extra commands sent: %q", terminal.String())
		}
		a.checkExecutionWait(time.Now().Add(time.Second))
		if state.finished() {
			t.Fatal("stale pre-command prompt completed execution")
		}
		a.processSSHOutput([]byte("\x1b[?2004h"))
		a.checkExecutionWait(time.Now().Add(time.Second))
		if state.finished() {
			t.Fatal("terminal mode reply reused the pre-command prompt")
		}
		a.processSSHOutput([]byte("\r\n授权模式:\r\n1>远程授权\r\n输入 Q 退出"))
		a.checkExecutionWait(time.Now().Add(time.Second))
		if state.finished() {
			t.Fatal("approval menu completed execution")
		}
		a.processSSHOutput([]byte("\r\nPID CPU COMMAND\r\n123 99 worker\r\n[deployer@no"))
		a.checkExecutionWait(time.Now().Add(time.Second))
		if state.finished() {
			t.Fatal("partial prompt completed execution")
		}
		a.processSSHOutput([]byte("de ~]$ "))
		a.checkExecutionWait(state.LastOutputAt)
		if state.finished() {
			t.Fatal("prompt completed execution before quiet period")
		}
		a.checkExecutionWait(state.LastOutputAt.Add(executionPromptQuietDelay))
		a.submitExecutionResult()
		select {
		case request := <-requests:
			if request.Completion != "shell_prompt_returned; exit_status=unknown" || !strings.Contains(request.Output, "123 99 worker") {
				t.Fatalf("result=%+v", request)
			}
		case <-time.After(time.Second):
			t.Fatal("result not submitted after prompt returned")
		}
		if !a.atPrompt || a.execution != nil {
			t.Fatal("terminal did not become ready after command")
		}
	}
}

func TestDirectPromptCannotFinishWhileCommandIsStillSending(t *testing.T) {
	a, _ := executionTestApp(t)
	state := a.execution
	state.Direct = true
	state.WriteJob = &sshWriteJob{}
	a.processSSHOutput([]byte("\r\n[root@node ~]# "))
	a.checkExecutionWait(state.LastOutputAt.Add(time.Second))
	if state.finished() {
		t.Fatal("prompt ended command before queued bytes drained")
	}
	state.WriteJob.done.Store(true)
	a.checkExecutionWait(state.LastOutputAt.Add(time.Second))
	if state.Completion != "shell_prompt_returned" || state.ExitCode != nil {
		t.Fatalf("completion=%q exit=%v", state.Completion, state.ExitCode)
	}
}
