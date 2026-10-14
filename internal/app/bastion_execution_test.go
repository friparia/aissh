package app

import (
	"io"
	"strings"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
)

func TestBastionMonitorSendsRawScriptAndCollectsWithoutInterrupt(t *testing.T) {
	a, terminal := newMonitorTestApp(t)
	a.config.BastionCompatibility = true
	item := a.panels.ByID("system")
	item.Command = "printf 'CPU sample\\n'"
	r := a.panelMonitorAudit(item)
	a.ensureMonitorAudit(r)
	a.decideMonitorAudit(true)
	a.startInBandCollection(item, time.Now(), "", time.Second)
	if got := terminal.String(); got != rawApprovedShellInput(item.Command) {
		t.Fatalf("monitor added a wrapper: %q", got)
	}
	a.processSSHOutput([]byte("CPU sample\r\nroot@server:~# "))
	a.checkInBandMonitorTimeout(time.Now().Add(time.Hour))
	if a.inBandMonitor == nil || !a.canCollectExecutionOutput() {
		t.Fatal("regex prompt or timeout collected compatibility monitor")
	}
	state := a.inBandMonitor
	before := terminal.String()
	if err := a.collectExecutionOutput(); err != nil {
		t.Fatal(err)
	}
	if terminal.String() != before || a.inBandMonitor != nil || state.exitCode != nil || !strings.Contains(state.rawContent, "CPU sample") {
		t.Fatal("manual monitor collection wrote input or invented exit status")
	}
	a.config.DesktopUI = true
	item.Command = "printf 'next CPU sample\\n'"
	a.ensureMonitorAudit(a.panelMonitorAudit(item))
	if !a.desktopState().CanApprove {
		t.Fatal("next approval remained blocked after manually collecting a monitor at the shell prompt")
	}
	a.approveProposal()
	a.monitorLast[item.ID] = time.Time{}
	a.scheduleMonitors()
	if a.inBandMonitor == nil || !strings.HasSuffix(terminal.String(), rawApprovedShellInput(item.Command)) {
		t.Fatal("newly approved monitor remained paused after manual collection")
	}
}

func TestBastionManualCollectionRestoresNextDesktopApproval(t *testing.T) {
	for _, prompt := range []string{"[deployer@node ~]$ ", "$ "} {
		for _, alt := range []bool{false, true} {
			if alt && prompt == "$ " {
				continue // A generic prompt cannot identify a gateway shell.
			}
			a, requests := executionTestApp(t)
			a.config.DesktopUI, a.config.BastionCompatibility = true, true
			a.screenOut = io.Discard
			state := a.execution
			a.execution = nil
			prefix := ""
			if alt {
				prefix = "\x1b[?1049h"
			}
			a.processSSHOutput([]byte(prefix + prompt))
			a.execution, a.atPrompt = state, false
			terminal := &recoveryTestPTY{}
			a.sshPTY = terminal
			a.sendApprovedCommand(state)
			awaitExecutionWrite(t, a)
			a.processSSHOutput([]byte("\r\nRESULT\r\n" + prompt))
			if state.finished() || a.atPrompt {
				t.Fatal("regex prompt automatically collected the command")
			}
			before := terminal.String()
			if err := a.collectExecutionOutput(); err != nil {
				t.Fatal(err)
			}
			if terminal.String() != before || state.ExitCode != nil {
				t.Fatal("manual collection wrote remote input or invented an exit code")
			}
			select {
			case <-requests:
			case <-time.After(time.Second):
				t.Fatal("missing execution result")
			}
			deadline := time.After(time.Second)
			for a.agentBusy {
				select {
				case e := <-a.events:
					a.handleEvent(e)
				case <-deadline:
					t.Fatal("result analysis did not finish")
				}
			}
			a.finishAgentTurn(agent.Turn{Proposal: &agent.Proposal{ID: "next", Command: "echo next", Risk: "read_only"}}, nil)
			if !a.desktopState().CanApprove {
				t.Fatalf("next approval blocked without new PTY output: prompt=%q alt=%t", prompt, alt)
			}
			if err := a.handleDesktopCommand(credentialui.WorkbenchCommand{Kind: "approve", ProposalID: "next", Deadline: time.Now().Add(time.Minute).UnixMilli()}); err != nil {
				t.Fatalf("next approval failed: %v", err)
			}
			deadline = time.After(time.Second)
			for a.execution == nil {
				select {
				case e := <-a.events:
					a.handleEvent(e)
				case <-deadline:
					t.Fatal("approved second command did not start")
				}
			}
			awaitExecutionWrite(t, a)
			if !strings.HasSuffix(terminal.String(), "echo next\r") {
				t.Fatal("approved second command was not sent verbatim")
			}
		}
	}
}

func TestBastionManualCollectionDoesNotEnableApprovalInMenus(t *testing.T) {
	for _, output := range []string{"", "\r\n选择授权模式\r\n1>", "\r\nPassword: ", "\r\n> ", "\r\nworking..."} {
		a, _ := executionTestApp(t)
		a.config.DesktopUI, a.config.BastionCompatibility = true, true
		a.screenOut = io.Discard
		state := a.execution
		a.execution = nil
		a.processSSHOutput([]byte("[root@node ~]# "))
		a.execution, a.atPrompt = state, false
		a.sshPTY = &recoveryTestPTY{}
		a.sendApprovedCommand(state)
		awaitExecutionWrite(t, a)
		a.processSSHOutput([]byte(output))
		if err := a.collectExecutionOutput(); err != nil {
			t.Fatal(err)
		}
		a.agentBusy = false
		a.proposal = &agent.Proposal{ID: "next", Command: "echo next"}
		if a.atPrompt || a.desktopCanApprove() {
			t.Fatalf("unsafe or stale prompt enabled approval: %q", output)
		}
	}
}

func TestBastionCommandsRemainRawAndRequireManualCollection(t *testing.T) {
	for _, command := range []string{"ps -eo pid,%cpu,comm | head -n 11", "echo first\necho second"} {
		a, requests := executionTestApp(t)
		a.config.BastionCompatibility = true
		terminal := &recoveryTestPTY{}
		a.sshPTY = terminal
		state := a.execution
		state.Command = command
		a.sendApprovedCommand(state)
		awaitExecutionWrite(t, a)
		if got, want := terminal.String(), rawApprovedShellInput(command); got != want {
			t.Fatalf("input=%q want=%q", got, want)
		}
		// Changing the switch cannot change how an already sent command finishes.
		a.config.BastionCompatibility = false
		a.processSSHOutput([]byte("\r\nRESULT\r\n[root@node ~]# "))
		a.checkExecutionWait(time.Now().Add(time.Second))
		if state.finished() || !a.canCollectExecutionOutput() {
			t.Fatal("regex prompt completed a compatibility command")
		}
		if state.Multiline {
			a.processSSHOutput([]byte("\x1b]133;C\a\x1b]133;A\a"))
			if state.finished() {
				t.Fatal("intermediate integration prompt completed multiline command")
			}
		}
		before := terminal.String()
		a.handleInput([]byte{0x19}) // CLI collection shortcut.
		if terminal.String() != before {
			t.Fatal("collecting output sent input to the remote terminal")
		}
		select {
		case request := <-requests:
			if request.Completion != "output_collected_manually; exit_status=unknown" || !strings.Contains(request.Output, "RESULT") {
				t.Fatalf("result=%+v", request)
			}
		case <-time.After(time.Second):
			t.Fatal("manual result not submitted")
		}
	}
}

func TestBastionShellIntegrationHandlesSplitSequences(t *testing.T) {
	for _, end := range []string{"\a", "\x1b\\"} {
		data := "\x1b]133;A" + end + "\x1b]133;C" + end + "RESULT\r\n\x1b]133;D;0" + end + "\x1b]133;A" + end
		for split := 0; split <= len(data); split++ {
			state := &executionState{}
			state.observeShellIntegration([]byte(data[:split]))
			state.observeShellIntegration([]byte(data[split:]))
			if !state.IntegrationPrompt {
				t.Fatalf("split=%d terminator=%q prompt not detected", split, end)
			}
		}
	}
	state := &executionState{}
	state.observeShellIntegration([]byte("\x1b]133;A\a"))
	if state.IntegrationPrompt {
		t.Fatal("stale prompt without a command-start event was accepted")
	}
}

func TestBastionIntegrationPromptCollectsAfterWriteDrains(t *testing.T) {
	a, requests := executionTestApp(t)
	state := a.execution
	state.BastionCompatibility, state.Direct = true, true
	state.WriteJob = &sshWriteJob{}
	a.processSSHOutput([]byte("\x1b]133;C\aRESULT\r\n\x1b]133;A\a[root@node ~]# "))
	if state.finished() {
		t.Fatal("collection finished while command was still sending")
	}
	state.WriteJob.done.Store(true)
	a.checkExecutionWait(time.Now())
	a.submitExecutionResult()
	select {
	case request := <-requests:
		if request.Completion != "shell_integration_prompt_returned; exit_status=unknown" || !strings.Contains(request.Output, "RESULT") {
			t.Fatalf("result=%+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("integration result not submitted")
	}
}
