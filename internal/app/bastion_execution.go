package app

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/trigger"
)

// Only terminal line endings are translated; no shell syntax is added.
func rawApprovedShellInput(command string) string {
	command = strings.ReplaceAll(strings.ReplaceAll(command, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(command, "\n", approvedCommandLineEnding()) + "\r"
}

// Observe actual OSC 133 Shell Integration events, including split PTY reads.
// Prompt-looking text and local regex triggers never enter this parser.
func (state *executionState) observeShellIntegration(data []byte) {
	if state.Multiline || state.finished() {
		return
	}
	buffer := append(state.IntegrationCarry, data...)
	state.IntegrationCarry = nil
	for len(buffer) > 0 {
		start := bytes.Index(buffer, []byte("\x1b]"))
		if start < 0 {
			if buffer[len(buffer)-1] == '\x1b' {
				state.IntegrationCarry = []byte{'\x1b'}
			}
			return
		}
		buffer = buffer[start+2:]
		end, size := -1, 0
		for i, b := range buffer {
			if b == '\a' {
				end, size = i, 1
				break
			}
			if b == '\x1b' && i+1 < len(buffer) && buffer[i+1] == '\\' {
				end, size = i, 2
				break
			}
		}
		if end < 0 {
			if len(buffer) < 1024 {
				state.IntegrationCarry = append([]byte("\x1b]"), buffer...)
			}
			return
		}
		fields := strings.Split(string(buffer[:end]), ";")
		if len(fields) >= 2 && fields[0] == "133" {
			switch fields[1] {
			case "C":
				state.IntegrationStarted = true
			case "D":
				if state.IntegrationStarted && len(fields) == 3 {
					if code, err := strconv.Atoi(fields[2]); err == nil && code >= 0 && code <= 255 {
						state.IntegrationExitCode = &code
					}
				}
			case "A":
				if state.IntegrationStarted {
					state.IntegrationPrompt = true
				}
			}
		}
		buffer = buffer[end+size:]
	}
}

func (a *App) checkBastionExecutionPrompt() {
	state := a.execution
	if state == nil || !state.BastionCompatibility || state.Multiline || !state.IntegrationPrompt || state.sending() || state.finished() || a.executionRecovery || a.zmodem != nil {
		return
	}
	state.Completion = "shell_integration_prompt_returned"
	state.ExitCode = state.IntegrationExitCode
	a.atPrompt = true
	a.promptIdleSince = time.Now()
	a.dirty = true
}

func (a *App) canCollectExecutionOutput() bool {
	if state := a.inBandMonitor; state != nil && state.compatibility != nil {
		return (state.writeJob == nil || state.writeJob.done.Load()) && !a.agentBusy && a.zmodem == nil
	}
	state := a.execution
	return state != nil && state.BastionCompatibility && !state.finished() && !state.sending() && !a.executionRecovery && !a.agentBusy && a.zmodem == nil
}

func (a *App) collectExecutionOutput() error {
	if !a.canCollectExecutionOutput() {
		return errors.New("当前没有可手动收集的命令输出，或命令仍在发送")
	}
	if a.inBandMonitor != nil && a.inBandMonitor.compatibility != nil {
		a.finishBastionMonitor(false)
		return nil
	}
	state := a.execution
	state.Completion = "output_collected_manually"
	a.appendPersistentChat(chatMessage{Role: "system", Content: a.t("已收集当前输出；远端命令的退出状态未知，未发送中断。"), Time: time.Now()})
	a.submitExecutionResult()
	a.restorePromptAfterManualCollection(a.outputTail.String(), state.ShellPrompt)
	a.dirty = true
	return nil
}

func (a *App) checkBastionMonitorPrompt() {
	s := a.inBandMonitor
	if s == nil || s.compatibility == nil || s.compatibility.Multiline || !s.compatibility.IntegrationPrompt || (s.writeJob != nil && !s.writeJob.done.Load()) {
		return
	}
	a.finishBastionMonitor(true)
}

func (a *App) finishBastionMonitor(prompt bool) {
	s := a.inBandMonitor
	s.rawContent = strings.TrimSpace(trigger.StripANSI(string(s.buffer)))
	a.inBandMonitor = nil
	a.monitorActive[s.panelID] = false
	a.monitorAwaitingPrompt = false
	a.atPrompt = prompt
	if !prompt {
		a.monitorPrimaryPaused = true
		a.restorePromptAfterManualCollection(string(s.buffer), s.compatibility.ShellPrompt)
	}
	a.monitorDeferredInput = append(a.monitorDeferredInput, s.pendingInput...)
	if prompt {
		s.exitCode = s.compatibility.IntegrationExitCode
	}
	capture := s.capture(nil)
	capture.Complete = true
	if s.exitCode == nil {
		capture.Status = "collected_unknown"
	}
	// Never invent a successful exit code from a manual snapshot or a prompt.
	a.applyMonitorResult(monitorResultEvent{pipelineID: s.pipelineID, capture: capture, panelID: s.panelID, command: s.command, binding: s.binding, revision: s.revision, runID: s.runID, content: a.monitor.Format(s.command, s.rawContent)})
	if s.pipelineID != "" {
		delete(a.monitorActive, s.panelID)
	}
	a.updateSSHActivity(s.auditID, "unknown", a.t("已收集输出；远端命令退出状态未知")+"\n"+s.rawContent)
	a.appendPersistentChat(chatMessage{Role: "system", Content: a.t("已收集当前输出；远端命令的退出状态未知，未发送中断。"), Time: time.Now()})
	a.flushDeferredMonitorInput()
	a.dirty = true
}

// Manual collection is the explicit boundary for the local capture. Recheck
// the already rendered prompt so another PTY read is not required to approve
// the next command. This never completes a capture or proves an exit status.
func (a *App) restorePromptAfterManualCollection(output, previousPrompt string) {
	if a.virtual == nil || a.executionRecovery || a.zmodem != nil || len(a.lineInput) != 0 || !a.lineReliable || a.terminalPasteMode || !trigger.LooksLikePrompt(output) {
		return
	}
	a.refreshGatewayShellPrompt()
	a.virtual.Lock()
	line, ok := a.terminalCursorLineLocked()
	safe := a.safePromptScreenLocked()
	a.virtual.Unlock()
	if !ok || !safe || !trigger.LooksLikePrompt(line) {
		return
	}
	// Do not treat a numbered menu option or PS2 as an idle shell. Generic
	// prompts are accepted only when they match the pre-command shell prompt.
	trimmed := strings.TrimSpace(line)
	known := previousPrompt != "" && trimmed == strings.TrimSpace(previousPrompt) && (strings.HasSuffix(trimmed, "$") || strings.HasSuffix(trimmed, "#") || strings.HasSuffix(trimmed, "%"))
	if !trigger.LooksLikeShellPrompt(line) && !known {
		return
	}
	a.atPrompt, a.sensitive = true, false
	a.monitorAwaitingPrompt = false
	a.promptIdleSince = time.Now()
	a.pendingError, a.errorCarry = nil, ""
}
