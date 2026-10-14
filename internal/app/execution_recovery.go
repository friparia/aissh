package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hinshun/vt10x"

	"git.example.com/ai/wrd-ai-cli/internal/trigger"
)

const executionWaitNoticeDelay = 30 * time.Second

func (state *executionState) finished() bool {
	return state.ExitCode != nil || state.Completion != ""
}

func (state *executionState) sending() bool {
	return state.WriteJob != nil && !state.WriteJob.done.Load()
}

func (state *executionState) completionDescription() string {
	status := "exit_status=unknown"
	if state.ExitCode != nil {
		status = fmt.Sprintf("exit_status=%d", *state.ExitCode)
	}
	if state.Completion != "" {
		return state.Completion + "; " + status
	}
	return status
}

func (a *App) sendApprovedCommand(state *executionState) {
	if a.sshInput == nil && a.sshPTY != nil {
		a.sshInput = newSSHInputWriter(a.ctx, a.sshPTY, a.events)
	}
	if a.sshInput == nil {
		a.stopExecutionWait("transport_error")
		return
	}
	state.BastionCompatibility = a.config.BastionCompatibility
	state.Multiline = strings.ContainsAny(state.Command, "\r\n")
	state.ShellPrompt = ""
	if a.virtual != nil {
		a.virtual.Lock()
		line, ok := a.terminalCursorLineLocked()
		if ok && a.virtual.CursorVisible() && a.safePromptScreenLocked() && trigger.LooksLikePrompt(line) {
			state.ShellPrompt = strings.TrimSpace(line)
		}
		a.virtual.Unlock()
	}
	state.Direct = state.BastionCompatibility || state.ShellPrompt != ""
	state.EchoMarker = !state.Direct
	marker := ""
	if state.EchoMarker {
		marker = state.Marker
	}
	input := approvedShellInput(state.Command, marker)
	if state.BastionCompatibility {
		input = rawApprovedShellInput(state.Command)
	}
	data := []byte(input)
	a.outputTail.value = ""
	a.debugInput("execution send marker=%s command_bytes=%d prompt_mode=%t", state.Marker, len(data), state.Direct)
	state.WriteJob = a.sshInput.enqueueApprovedCommand(data, state.Marker, a.config.BastionCompatibility)
}

// Like iTerm2's shellInput, keep multiline scripts in the current shell and
// append an echo receipt only when a primary prompt cannot be recognised.
func approvedShellInput(command, marker string) string {
	command = strings.ReplaceAll(strings.ReplaceAll(command, "\r\n", "\n"), "\r", "\n")
	if strings.Contains(command, "\n") || marker != "" {
		command = "eval " + quotePOSIXShell(command)
	}
	if marker != "" {
		command += "; echo '-- FINISHED' " + quotePOSIXShell(marker)
	}
	return strings.ReplaceAll(command, "\n", approvedCommandLineEnding()) + "\r"
}

const executionPromptQuietDelay = 300 * time.Millisecond

func (a *App) checkDirectExecutionPrompt(now time.Time) {
	state := a.execution
	if state == nil || state.BastionCompatibility || !state.Direct || state.finished() || state.sending() || a.executionRecovery || a.sensitive || a.zmodem != nil || state.LastOutputAt.IsZero() || now.Sub(state.LastOutputAt) < executionPromptQuietDelay || a.virtual == nil || !trigger.LooksLikePrompt(a.outputTail.String()) {
		return
	}
	a.virtual.Lock()
	line, ok := a.terminalCursorLineLocked()
	visible := a.virtual.CursorVisible()
	alt := a.virtual.Mode()&vt10x.ModeAltScreen != 0
	a.virtual.Unlock()
	if !ok || !visible || !trigger.LooksLikePrompt(line) {
		return
	}
	// A menu's numbered options and PS2 must not look like completion. Accept
	// the prompt seen before sending, or a primary user@host shell prompt (which
	// may change after cd). Alternate-screen menus require the latter.
	known := state.ShellPrompt != "" && strings.TrimSpace(line) == state.ShellPrompt
	if !trigger.LooksLikeShellPrompt(line) && (!known || alt) {
		return
	}
	state.Completion = "shell_prompt_returned"
	a.atPrompt = true
	a.promptIdleSince = now
	a.pendingError, a.errorCarry = nil, ""
	a.dirty = true
}

func (a *App) finishSSHWrite(event sshWriteEvent) {
	a.debugInput("pty_write marker=%s interrupt=%t requested=%d written=%d error=%v", event.job.marker, event.job.interrupt, len(event.job.data), event.written, event.err)
	state := a.execution
	if event.job.marker != "" && (state == nil || event.job.marker != state.Marker) {
		return
	}
	if event.err != nil && !errors.Is(event.err, context.Canceled) {
		if state != nil {
			a.stopExecutionWait("transport_error")
		} else if a.inBandMonitor != nil {
			a.pauseInBandMonitor(event.err)
		} else {
			a.atPrompt = false
		}
		a.appendPersistentChat(chatMessage{Role: "error", Content: a.t("SSH 写入失败（%d/%d 字节）：%s", event.written, len(event.job.data), event.err.Error()), Time: time.Now()})
	}
	a.tryRecoverExecutionPrompt()
	a.submitExecutionResult()
	a.dirty = true
}

// Ending local collection says nothing about the remote process's exit status.
// Keep the result until a fresh prompt is observed and all queued command bytes
// have drained, so output from a later command cannot be attributed to this one.
func (a *App) stopExecutionWait(reason string) {
	state := a.execution
	if state != nil {
		if state.WriteJob != nil {
			state.WriteJob.cancelled.Store(true)
		}
		if state.Completion == "" && state.ExitCode == nil {
			if state.Output != nil {
				state.Output.Append(trigger.StripANSI(string(state.Pending)))
			}
			state.Completion = reason
			a.debugInput("execution stop marker=%s reason=%s sending=%t", state.Marker, reason, state.sending())
			a.appendPersistentChat(chatMessage{Role: "system", Content: a.t("命令结果尚未确认；等待新的 Shell 提示符，可按 Ctrl+C 重新同步。"), Time: time.Now()})
		}
	}
	a.executionRecovery = true
	a.executionRecoveryOutput = false
	a.atPrompt = false
	a.promptIdleSince = time.Time{}
	a.outputTail.value = ""
	a.pendingError = nil
	a.errorCarry = ""
	a.lineInput = nil
	a.lineCursor = 0
	a.lineAnchored = false
	a.lineReliable = true
	a.aiLineCapture = false
	a.terminalKeyCarry = nil
	a.focus = focusTerminal
	a.dirty = true
}

// Ctrl+C remains reachable when atPrompt is false. Pasted control bytes must
// remain ordinary paste input.
func (a *App) handleExecutionControl(data []byte) bool {
	if (a.execution == nil && !a.executionRecovery) || a.terminalPasteMode || a.aiPasteMode || bytes.Contains(data, terminalPasteStart) {
		return false
	}
	interrupts := bytes.Count(data, []byte{3})
	if interrupts == 0 {
		return false
	}
	// Drop typing in the same read as the interrupt, including repeated Ctrl+C
	// delivered together by the terminal. Only explicit interrupt bytes survive.
	a.stopExecutionWait("cancelled")
	data = bytes.Repeat([]byte{3}, interrupts)
	if a.sshInput != nil {
		marker := ""
		if a.execution != nil {
			marker = a.execution.Marker
		}
		a.executionInterrupt = a.sshInput.enqueue(data, marker, true)
	} else {
		_, err := a.writeSSH(data)
		if err != nil {
			a.debugInput("execution interrupt error=%v", err)
		}
	}
	return true
}

// PTY output may arrive before Write returns. Retain a prompt observed after
// the interrupt starts, then wait for its write acknowledgement before recovery.
func (a *App) executionRecoveryCanObserveOutput() bool {
	return (a.execution == nil || !a.execution.sending()) &&
		(a.executionInterrupt == nil || a.executionInterrupt.started.Load())
}

func (a *App) executionWritesDrained() bool {
	return (a.execution == nil || !a.execution.sending()) &&
		(a.executionInterrupt == nil || a.executionInterrupt.done.Load())
}

func (a *App) safePromptScreen() bool {
	if a.virtual == nil {
		return true
	}
	a.virtual.Lock()
	defer a.virtual.Unlock()
	return a.safePromptScreenLocked()
}

// Some SSH gateways keep their menu's alternate screen active after entering
// a node shell. Only a visible user@host primary prompt permits shell actions
// in that mode; generic editor and menu prompts must still pass through.
func (a *App) safePromptScreenLocked() bool {
	if !a.virtual.CursorVisible() {
		return false
	}
	if a.virtual.Mode()&vt10x.ModeAltScreen == 0 {
		return true
	}
	line, ok := a.terminalCursorLineLocked()
	return a.gatewayShellPrompt && ok && trigger.LooksLikeShellPrompt(line)
}

func (a *App) refreshGatewayShellPrompt() {
	// Never learn a gateway shell from a full-screen application encountered
	// during command execution or recovery. A previously observed shell may
	// still recover in the same alternate screen after Ctrl+C.
	if a.virtual == nil || a.execution != nil || a.executionRecovery || a.inBandMonitor != nil || a.monitorAwaitingPrompt || len(a.lineInput) != 0 || a.aiLineCapture {
		return
	}
	a.virtual.Lock()
	defer a.virtual.Unlock()
	line, ok := a.terminalCursorLineLocked()
	a.gatewayShellPrompt = a.virtual.Mode()&vt10x.ModeAltScreen != 0 && a.virtual.CursorVisible() && ok && trigger.LooksLikeShellPrompt(line)
}

func (a *App) tryRecoverExecutionPrompt() {
	if !a.executionRecovery || !a.executionRecoveryOutput || a.sensitive || !a.safePromptScreen() || a.monitorAwaitingPrompt || a.zmodem != nil {
		return
	}
	if !a.executionWritesDrained() {
		return
	}
	if !trigger.LooksLikePrompt(a.outputTail.String()) {
		return
	}
	a.executionRecovery = false
	a.executionRecoveryOutput = false
	a.executionInterrupt = nil
	a.atPrompt = true
	a.promptIdleSince = time.Now()
	// Input passed to a password reader or full-screen program during recovery
	// must not become the prefix of the next shell/AI command.
	a.lineInput = nil
	a.lineCursor = 0
	a.lineAnchored = false
	a.lineReliable = true
	a.aiLineCapture = false
	a.terminalKeyCarry = nil
	a.pendingError = nil
	a.errorCarry = ""
	a.debugInput("execution recovered prompt=true")
	a.dirty = true
}

func (a *App) checkExecutionWait(now time.Time) {
	a.checkBastionExecutionPrompt()
	a.checkDirectExecutionPrompt(now)
	state := a.execution
	if state == nil || state.finished() || state.WaitWarned || state.StartedAt.IsZero() || now.Sub(state.StartedAt) < executionWaitNoticeDelay {
		return
	}
	state.WaitWarned = true
	notice := a.t("仍未收到命令完成标记；按 Ctrl+C 中断并重新同步 Shell。")
	if state.BastionCompatibility {
		notice = a.t("等待收集输出；没有 Shell Integration 或多行命令，请点击‘收集输出’。该操作不会中断远端命令。")
	} else if state.Direct {
		notice = a.t("仍在等待 Shell 返回提示符；需要人工交互时可取消 AI 并接管终端。")
	}
	a.appendPersistentChat(chatMessage{Role: "system", Content: notice, Time: now})
	a.debugInput("execution waiting marker=%s sending=%t pending_bytes=%d", state.Marker, state.sending(), len(state.Pending))
	a.dirty = true
}

func (a *App) retireExecution(state *executionState) {
	if state.Direct {
		return
	}
	// Keep parsers for recent commands to absorb duplicate/late markers, including
	// ones split across reads, without treating them as the next command's result.
	a.retiredExecutions = append(a.retiredExecutions, &executionState{Marker: state.Marker, Pending: state.Pending, EchoMarker: state.EchoMarker, TextMarker: state.TextMarker})
	if len(a.retiredExecutions) > 32 {
		a.retiredExecutions = a.retiredExecutions[1:]
	}
}

func (a *App) filterRetiredExecutionMarkers(data []byte) []byte {
	for _, state := range a.retiredExecutions {
		var visible []byte
		for {
			if state.EchoMarker {
				state.ReceiptReceived = false
			}
			output, after, code := state.consume(data)
			visible = append(visible, output...)
			if code == nil && !state.ReceiptReceived {
				break
			}
			a.debugInput("execution ignored late marker=%s", state.Marker)
			data = after
		}
		data = visible
	}
	return data
}
