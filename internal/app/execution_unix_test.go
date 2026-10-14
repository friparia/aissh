//go:build !windows

package app

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestIsolateApprovedCommandReportsStatusAndProtectsOuterShell(t *testing.T) {
	for _, shell := range []string{"sh", "bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		for _, tc := range []struct {
			name, command, output string
			status                int
		}{
			{"multiline", "printf 'first\\n'\nprintf 'second\\n'", "first\nsecond\n", 0},
			{"exit", "printf 'missing\\n'\nexit 7", "missing\n", 7},
			{"exec", "exec printf 'replaced\\n'", "replaced\n", 0},
			// POSIX shells differ in the exact syntax-error code (e.g. 1 or 2).
			{"syntax_error", "if then", "", -1},
			{"errexit", "set -e; false; printf WRONG", "", 1},
			{"own_trap", "trap 'printf own-trap' 0; exit 9", "own-trap", 9},
			{"redirect_stdout", "exec >/dev/null; printf hidden; exit 3", "", 3},
			{"heredoc", "cat <<'END'\nquoted 'text' and $HOME\nEND", "quoted 'text' and $HOME\n", 0},
		} {
			t.Run(shell+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				script := isolateApprovedCommandWithLineEnding(tc.command, "test", "\n")
				cmd := exec.CommandContext(ctx, path)
				cmd.Stdin = strings.NewReader(strings.TrimSuffix(script, "\r") + "\nprintf 'OUTER_ALIVE\\n'\n")
				data, err := cmd.Output()
				if err != nil {
					t.Fatalf("outer shell failed: %v", err)
				}
				state := &executionState{Marker: "test"}
				output, after, status := state.consume(data)
				statusMatches := status != nil && (*status == tc.status || tc.status == -1 && *status != 0)
				if !statusMatches || string(output) != tc.output || string(after) != "OUTER_ALIVE\n" {
					t.Fatalf("command=%q output=%q after=%q status=%v raw=%q", tc.command, output, after, status, data)
				}
			})
		}
	}
}

func TestGatewayCommandReportsStatusWithoutOSC(t *testing.T) {
	for _, shell := range []string{"sh", "bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			script := isolateGatewayApprovedCommand("printf RESULT; exit 7", "test")
			if strings.Contains(script, executionTextMarkerPrefix("test")) {
				t.Fatal("command echo contains the text marker")
			}
			command := exec.Command(path)
			command.Stdin = strings.NewReader(strings.TrimSuffix(script, "\r") + "\nprintf OUTER_ALIVE\n")
			data, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			state := &executionState{Marker: "test", TextMarker: true}
			output, after, status := state.consume(data)
			if status == nil || *status != 7 || string(output) != "RESULT\n" || string(after) != "OUTER_ALIVE" {
				t.Fatalf("output=%q after=%q status=%v raw=%q", output, after, status, data)
			}
		})
	}
}

func TestApprovedCommandCompletesInInteractivePTY(t *testing.T) {
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	for _, name := range []string{"normal", "ctrl_c"} {
		interrupted := name == "ctrl_c"
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, path, "--noprofile", "--norc", "-i")
			cmd.Env = append(os.Environ(), "PS1=AISSH_TEST_READY> ", "PS2=> ", "TERM=xterm")
			terminal, err := pty.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = terminal.Close()
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}()
			chunks := make(chan []byte, 32)
			go func() {
				defer close(chunks)
				buffer := make([]byte, 4096)
				for {
					n, err := terminal.Read(buffer)
					if n > 0 {
						select {
						case chunks <- append([]byte(nil), buffer[:n]...):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			a, requests := executionTestApp(t)
			var transcript string
			read := func() []byte {
				t.Helper()
				select {
				case event := <-a.events:
					a.handleEvent(event)
					return nil
				case data, ok := <-chunks:
					if !ok {
						t.Fatalf("shell exited early: %q", transcript)
					}
					transcript += string(data)
					return data
				case <-time.After(50 * time.Millisecond):
					a.checkExecutionWait(time.Now())
					a.submitExecutionResult()
					return nil
				case <-ctx.Done():
					t.Fatalf("PTY timed out: execution=%+v recovery=%v recovery_output=%v interrupt=%+v tail=%q safe=%v sensitive=%v transcript=%q", a.execution, a.executionRecovery, a.executionRecoveryOutput, a.executionInterrupt, a.outputTail.String(), a.safePromptScreen(), a.sensitive, transcript)
					return nil
				}
			}
			for !strings.Contains(transcript, "AISSH_TEST_READY> ") {
				a.processSSHOutput(read())
			}
			transcript = ""
			command := "printf '\\nSTARTED\\n'\nsleep 0.05\nprintf 'DONE\\n'"
			wantStatus := "shell_prompt_returned; exit_status=unknown"
			if interrupted {
				command = "printf '\\nSTARTED\\n'; sleep 30"
				wantStatus = "cancelled; exit_status=unknown"
			}

			a.sshPTY = terminal
			a.execution.Command = command
			a.sendApprovedCommand(a.execution)
			for !strings.Contains(transcript, "\r\nSTARTED\r\n") {
				a.processSSHOutput(read())
			}
			if interrupted {
				a.handleInput([]byte{3})
			}
			for a.execution != nil || !strings.Contains(transcript, "AISSH_TEST_READY> ") {
				a.processSSHOutput(read())
			}
			select {
			case request := <-requests:
				if request.Completion != wantStatus || (!interrupted && !strings.Contains(request.Output, "\r\nDONE")) {
					t.Fatalf("result=%#v transcript=%q", request, transcript)
				}
			case <-ctx.Done():
				t.Fatal("PTY result was not submitted")
			}
		})
	}
}

func TestCtrlCDuringLongCommandParsingRestoresAIInRealPTY(t *testing.T) {
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--noprofile", "--norc", "-i")
	cmd.Env = append(os.Environ(), "PS1=AISSH_TEST_READY> ", "PS2=> ", "TERM=xterm")
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = terminal.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	a, requests := executionTestApp(t)
	a.sshPTY = terminal
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				select {
				case a.events <- outputEvent{data: append([]byte(nil), buffer[:n]...)}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var transcript string
	readEvent := func() {
		t.Helper()
		select {
		case event := <-a.events:
			if output, ok := event.(outputEvent); ok {
				transcript += string(output.data)
			}
			a.handleEvent(event)
		case <-ctx.Done():
			t.Fatalf("PTY timed out: recovery=%v prompt=%v transcript=%q", a.executionRecovery, a.atPrompt, transcript)
		}
	}
	for !strings.Contains(transcript, "AISSH_TEST_READY> ") {
		readEvent()
	}
	transcript = ""
	gate := &gatedSSHWriter{target: terminal, entered: make(chan struct{}), release: make(chan struct{})}
	a.sshInput = newSSHInputWriter(a.ctx, gate, a.events)
	command := "printf '%s' '" + strings.Repeat("QUJDREVGR0g=", 20000) + "'; printf MUST_NOT_RUN"
	a.execution.Command = command
	a.sendApprovedCommand(a.execution)
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("command did not start writing")
	}
	for !strings.Contains(transcript, "printf") {
		readEvent()
	}
	// The shell is still reading the approved command. Ctrl+C must discard the
	// unsent suffix before recovering, so the trailing command cannot execute.
	a.handleInput([]byte{3})
	close(gate.release)
	for a.execution != nil || a.agentBusy || !a.atPrompt {
		readEvent()
	}
	select {
	case request := <-requests:
		if request.Completion != "cancelled; exit_status=unknown" {
			t.Fatalf("parser interrupt invented a status: %+v", request)
		}
	case <-ctx.Done():
		t.Fatal("missing parser-interruption result")
	}
	if strings.Contains(transcript, executionMarkerPrefix("test")) || strings.Contains(transcript, "MUST_NOT_RUN") {
		t.Fatalf("cancelled suffix executed or emitted a completion marker: %q", transcript)
	}
	a.handleInput([]byte("测试"))
	if !a.aiLineCapture || string(a.lineInput) != "测试" {
		t.Fatal("Chinese AI input was not restored after parsing interruption")
	}
}
