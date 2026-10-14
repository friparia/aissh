package app

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/monitorplan"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
	"git.example.com/ai/wrd-ai-cli/internal/policy"
	"git.example.com/ai/wrd-ai-cli/internal/trigger"
	"git.example.com/ai/wrd-ai-cli/internal/widgets"
)

var inBandMonitorScreenRestore = []byte("\x1b[2K\r")

type inBandMonitorState struct {
	compatibility   *executionState
	writeJob        *sshWriteJob
	visible         bool
	shown           int
	after           []byte
	auditID         string
	pipelineID      string
	timeout         time.Duration
	exitCode        *int
	rawContent      string
	failureStatus   string
	binding         string
	revision, runID uint64

	panelID      string
	command      string
	marker       string
	startedAt    time.Time
	buffer       []byte
	sawBegin     bool
	pendingInput []byte
}

func (a *App) scheduleInBandMonitor(now time.Time) {
	if !a.canStartInBandMonitor() || now.Sub(a.terminalInputAt) < 750*time.Millisecond {
		return
	}
	for _, item := range a.panels.Panels() {
		if item.Kind != panel.KindMonitor && item.Kind != panel.KindTable && item.Kind != panel.KindLog {
			continue
		}
		if item.Pipeline != nil || item.Command == "" || a.monitorActive[item.ID] {
			continue
		}
		interval := item.Interval
		if interval < time.Second {
			interval = time.Second
		}
		if last := a.monitorLast[item.ID]; !last.IsZero() && now.Sub(last) < interval {
			continue
		}
		if err := policy.ValidateReadOnlyMonitorCommand(item.Command); err != nil {
			a.panels.SetError(item.ID, fmt.Errorf("%s", a.t("监控命令被只读策略拒绝：%s", err.Error())))
			a.monitorLast[item.ID] = now
			continue
		}
		if !a.ensureMonitorAudit(a.panelMonitorAudit(item)) {
			if a.proposal != nil {
				return
			}
			continue
		}
		a.startInBandMonitor(item, now)
		return
	}
}

func (a *App) canStartInBandMonitor() bool {
	return a.config.SystemCollection && a.canStartInBandCollection()
}

func (a *App) canStartInBandCollection() bool {
	return !a.monitorPrimaryPaused && a.monitorWorkspaceVisible() && a.monitorShellReady()
}

func (a *App) monitorShellReady() bool {
	return !a.monitorAwaitingPrompt &&
		!a.executionRecovery && a.execution == nil && a.proposal == nil && a.inBandMonitor == nil && len(a.monitorDeferredInput) == 0 && a.atPrompt && !a.sensitive &&
		!a.agentBusy && !a.terminalDone && a.zmodem == nil && a.sshPTY != nil && len(a.lineInput) == 0 &&
		a.lineReliable && !a.aiLineCapture && !a.terminalPasteMode
}

// Timeouts and user interrupts pause automatic collection. Only an explicit
// refresh may resume it, after a fresh, empty shell prompt has been observed.
func (a *App) resumePausedMonitoring() error {
	if !a.monitorPrimaryPaused {
		return nil
	}
	if !a.monitorShellReady() {
		return fmt.Errorf("%s", a.t("监控已暂停；请先让 SSH 终端回到空闲提示符并清空未提交的输入，再点击刷新"))
	}
	a.monitorPrimaryPaused = false
	return nil
}

func (a *App) startInBandMonitor(item *panel.Panel, now time.Time) {
	a.startInBandCollection(item, now, "", 0)
}
func (a *App) startInBandCollection(item *panel.Panel, now time.Time, pipelineID string, timeout time.Duration) {
	r := a.panelMonitorAudit(item)
	if pipelineID != "" {
		b := a.pipelineBatches[pipelineID]
		if b == nil || b.index >= len(b.sources) {
			return
		}
		r = a.pipelineMonitorAudit(b)
	}
	if !a.ensureMonitorAudit(r) {
		return
	}
	a.monitorSequence++
	item.RunID++
	token, err := newExecutionMarker()
	if err != nil {
		return
	}
	marker := "__AISSH_MONITOR_" + token + "_"
	decision := a.monitorDecisions[r.scope()]
	state := &inBandMonitorState{visible: !isBuiltinMonitorCommand(r.Command) && !decision.shown, auditID: decision.proposalID, pipelineID: pipelineID, timeout: timeout,
		panelID: item.ID,
		binding: item.BindingID, revision: item.SourceRevision, runID: item.RunID,
		command:   item.Command,
		marker:    marker,
		startedAt: now,
	}
	command := inBandMonitorCommand(marker, item.Command)
	if a.config.BastionCompatibility {
		state.compatibility = &executionState{BastionCompatibility: true, Multiline: strings.ContainsAny(item.Command, "\r\n")}
		if a.virtual != nil {
			a.virtual.Lock()
			if line, ok := a.terminalCursorLineLocked(); ok && a.safePromptScreenLocked() && trigger.LooksLikePrompt(line) {
				state.compatibility.ShellPrompt = strings.TrimSpace(line)
			}
			a.virtual.Unlock()
		}
		state.visible = true
		command = rawApprovedShellInput(item.Command)
	}
	a.debugInput("primary_monitor start panel=%s cols=%d rows=%d", item.ID, a.mainCols, a.contentRows)
	a.inBandMonitor = state
	a.monitorAwaitingPrompt = true
	a.monitorActive[item.ID] = true
	a.atPrompt = false
	a.promptIdleSince = time.Time{}
	if state.visible {
		decision.shown = true
		a.monitorDecisions[r.scope()] = decision
		a.renderSSHOutput([]byte("\r\n" + strings.ReplaceAll(a.safeText(a.t("[监控 %s · %s]\n$ %s\n", firstText(item.Title, r.Title, r.PanelID), now.Format("15:04:05"), item.Command)), "\n", "\r\n")))
		a.updateSSHActivity(state.auditID, "running", a.t("正在 SSH 终端采集"))
	}
	// Only the fresh prompt after the completion marker can release user input.
	a.outputTail.Append("\n[aissh monitor]\n")
	if a.sshInput != nil {
		state.writeJob = a.sshInput.enqueueApprovedCommand([]byte(command), "", state.compatibility != nil)
	} else if _, err := a.writeSSH([]byte(command)); err != nil {
		a.pauseInBandMonitor(fmt.Errorf("%s: %w", a.t("主会话监控已暂停"), err))
	}
}

func inBandMonitorCommand(marker, command string) string {
	// The complete begin/end markers are assembled by printf and do not appear
	// contiguously in the echoed command line. This lets the output filter safely
	// ignore shell echo and start collecting only after the command really runs.
	return "(__aissh_monitor_marker=" + quotePOSIXShell(marker) +
		"; printf '%sBEGIN__\\n' \"$__aissh_monitor_marker\"; sh -c " + quotePOSIXShell(strings.TrimSpace(command)) +
		"; __aissh_monitor_status=$?; printf '\\n%sEND__:%s__\\n' \"$__aissh_monitor_marker\" \"$__aissh_monitor_status\")\r"
}

func quotePOSIXShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (a *App) processInBandMonitorOutput(data []byte) bool {
	if a.monitorPrompt != nil {
		visible, ready := a.monitorPrompt.consume(data)
		if ready {
			a.monitorPrompt = nil
			a.processSSHOutput(visible)
		}
		return true
	}
	state := a.inBandMonitor
	if state == nil {
		return false
	}
	if len(state.buffer)+len(data) > widgets.MaxOutput {
		state.failureStatus = "too_large"
		a.pauseInBandMonitor(fmt.Errorf("%s", a.t("监控输出超过 256 KiB，主通道采集已暂停")))
		return true
	}
	if state.compatibility != nil {
		state.buffer = append(state.buffer, data...)
		state.compatibility.observeShellIntegration(data)
		a.renderSSHOutput(data)
		a.checkBastionMonitorPrompt()
		return true
	}
	visible, complete, content, err := state.consume(data)
	if state.visible {
		a.renderSSHOutput(visible)
	}
	if !complete {
		return true
	}

	a.inBandMonitor = nil
	a.debugInput("primary_monitor complete panel=%s duration=%s pending_bytes=%d error=%t", state.panelID, time.Since(state.startedAt), len(state.pendingInput), err != nil)
	a.monitorDeferredInput = append(a.monitorDeferredInput, state.pendingInput...)
	if err == nil {
		content = a.monitor.Format(state.command, content)
	}
	a.applyMonitorResult(monitorResultEvent{pipelineID: state.pipelineID, capture: state.capture(err), panelID: state.panelID, command: state.command, binding: state.binding, revision: state.revision, runID: state.runID, content: content, err: err})
	if state.pipelineID != "" {
		delete(a.monitorActive, state.panelID)
	}
	a.outputTail.value = ""
	a.pendingError, a.errorCarry = nil, ""
	if state.visible {
		status, detail := "completed", a.t("退出状态未知")
		if state.exitCode != nil {
			detail = a.t("退出码：%d", *state.exitCode)
		}
		if err != nil {
			status = "failed"
		}
		a.renderSSHOutput([]byte("\r\n" + a.t("[监控完成 · %s]", detail) + "\r\n"))
		a.updateSSHActivity(state.auditID, status, detail)
		// consume keeps only the fresh prompt separate from collector bytes.
		a.monitorPrompt = &monitorPromptState{}
		a.processSSHOutput(state.after)
	} else {
		a.monitorPrompt = &monitorPromptState{}
		if len(visible) > 0 {
			a.processSSHOutput(visible)
		}
	}
	a.flushDeferredMonitorInput()
	return true
}

func (state *inBandMonitorState) consume(data []byte) (visible []byte, complete bool, content string, err error) {
	if len(state.buffer)+len(data) > widgets.MaxOutput {
		return nil, true, "", fmt.Errorf("monitor output exceeds 256 KiB")
	}
	state.buffer = append(state.buffer, data...)
	output := decodeMonitorOutput(state.buffer)
	beginMarker := []byte(state.marker + "BEGIN__")
	if !state.sawBegin {
		index, end := findMonitorMarker(output.text, beginMarker)
		if index < 0 {
			start := output.suffixStart(len(beginMarker) - 1)
			state.buffer = append(state.buffer[:0], state.buffer[start:]...)
			return nil, false, "", nil
		}
		state.buffer = append(state.buffer[:0], state.buffer[output.ends[end-1]:]...)
		state.sawBegin = true
		output = decodeMonitorOutput(state.buffer)
	}

	endMarker := []byte(state.marker + "END__:")
	endIndex, statusStart := findMonitorMarker(output.text, endMarker)
	if endIndex < 0 {
		if state.visible {
			end := output.beforeMarkerSuffix(endMarker)
			if end > state.shown {
				visible = state.visibleSample(output, end)
			}
		}
		return visible, false, "", nil
	}
	statusEndOffset, completionEnd := findMonitorMarker(output.text[statusStart:], []byte("__"))
	if statusEndOffset < 0 {
		return nil, false, "", nil
	}
	statusEnd := statusStart + statusEndOffset
	rawContent := trimInBandWrapperNewlines(output.text[:endIndex])
	statusText := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, strings.TrimSpace(string(output.text[statusStart:statusEnd])))
	status, parseErr := strconv.Atoi(statusText)
	// ConPTY may consume or rewrite erase/cursor controls. Complete on the
	// printable delimiter and restore the local prompt row ourselves. The
	// remote marker remains on its own line so a repaint cannot erase it.
	rawEnd := output.ends[statusStart+completionEnd-1]
	if state.visible {
		end := 0
		if endIndex > 0 {
			end = output.ends[endIndex-1]
		}
		if end > state.shown {
			visible = state.visibleSample(output, end)
		}
		state.after = append([]byte(nil), state.buffer[rawEnd:]...)
	} else {
		visible = append(append([]byte(nil), inBandMonitorScreenRestore...), state.buffer[rawEnd:]...)
	}
	if parseErr != nil {
		return visible, true, "", fmt.Errorf("invalid monitor exit status %q", statusText)
	}
	state.exitCode = &status
	state.rawContent = string(rawContent)
	if status != 0 {
		message := strings.TrimSpace(string(rawContent))
		if message == "" {
			message = fmt.Sprintf("monitor command exited with status %d", status)
		}
		return visible, true, "", fmt.Errorf("%s", message)
	}
	return visible, true, string(rawContent), nil
}

// A collector produces a text snapshot. Render its decoded text as new lines,
// not ConPTY cursor coordinates which could overwrite earlier audit records.
func (state *inBandMonitorState) visibleSample(output monitorOutputText, end int) []byte {
	var visible []byte
	for index, rawEnd := range output.ends {
		if rawEnd <= state.shown || rawEnd > end {
			continue
		}
		value := output.text[index]
		if value == '\r' {
			continue
		}
		if value == '\n' {
			visible = append(visible, '\r')
		}
		visible = append(visible, value)
	}
	state.shown = end
	return visible
}

func trimInBandWrapperNewlines(value []byte) []byte {
	value = trimOneLeadingNewline(value)
	value = trimOneTrailingNewline(value)
	return value
}

func trimOneLeadingNewline(value []byte) []byte {
	if bytes.HasPrefix(value, []byte("\r\n")) {
		return value[2:]
	}
	if len(value) > 0 && (value[0] == '\r' || value[0] == '\n') {
		return value[1:]
	}
	return value
}

func trimOneTrailingNewline(value []byte) []byte {
	if bytes.HasSuffix(value, []byte("\r\n")) {
		return value[:len(value)-2]
	}
	if len(value) > 0 && (value[len(value)-1] == '\r' || value[len(value)-1] == '\n') {
		return value[:len(value)-1]
	}
	return value
}

func (a *App) checkInBandMonitorTimeout(now time.Time) {
	state := a.inBandMonitor
	if state == nil {
		return
	}
	if state.compatibility != nil {
		a.checkBastionMonitorPrompt()
		return
	}
	timeout := state.timeout
	if timeout <= 0 {
		timeout = a.monitor.Timeout
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	if now.Sub(state.startedAt) < timeout {
		return
	}
	a.debugInput("primary_monitor timeout panel=%s begin_seen=%t buffered_bytes=%d pending_bytes=%d", state.panelID, state.sawBegin, len(state.buffer), len(state.pendingInput))
	state.failureStatus = "timeout"
	a.pauseInBandMonitor(fmt.Errorf("%s", a.t("监控查询超时，主会话监控已暂停")))
}

// A missing marker does not prove the monitor still owns the remote shell.
// Stop automatic queries without injecting Ctrl+C into the user's session.
func (a *App) pauseInBandMonitor(err error) {
	state := a.inBandMonitor
	if state == nil {
		return
	}
	a.inBandMonitor = nil
	if state.visible {
		a.renderSSHOutput([]byte("\r\n" + a.t("[监控采集暂停 · %s · 退出状态未知]", a.safeText(err.Error())) + "\r\n"))
		a.updateSSHActivity(state.auditID, "unknown", a.safeText(err.Error()))
	}
	a.monitorPrimaryPaused = true
	a.monitorAwaitingPrompt = true
	a.monitorDeferredInput = append(a.monitorDeferredInput, state.pendingInput...)
	a.atPrompt = false
	a.outputTail.value = ""
	a.applyMonitorResult(monitorResultEvent{pipelineID: state.pipelineID, capture: state.capture(err), panelID: state.panelID, command: state.command, binding: state.binding, revision: state.revision, runID: state.runID, err: err})
	if state.pipelineID != "" {
		delete(a.monitorActive, state.panelID)
	}
	a.dirty = true
}

func (a *App) handleInBandMonitorInput(data []byte) bool {
	if a.inBandMonitor == nil && !a.monitorAwaitingPrompt {
		return false
	}
	if len(data) == 1 && data[0] == 0x03 {
		a.pauseInBandMonitor(fmt.Errorf("%s", a.t("主会话监控已暂停")))
		a.monitorPrompt = nil
		// An explicit user interrupt cancels any queued typing too.
		a.monitorDeferredInput = nil
		if a.sshPTY != nil {
			_, _ = a.writeSSH(data)
		}
	} else if a.inBandMonitor != nil {
		a.inBandMonitor.pendingInput = append(a.inBandMonitor.pendingInput, data...)
	} else {
		a.monitorDeferredInput = append(a.monitorDeferredInput, data...)
	}
	return true
}

func (a *App) flushDeferredMonitorInput() {
	if a.inBandMonitor != nil || a.monitorAwaitingPrompt || !a.atPrompt || len(a.monitorDeferredInput) == 0 {
		return
	}
	data := append([]byte(nil), a.monitorDeferredInput...)
	a.monitorDeferredInput = nil
	// The user may have opened a local editor while SSH input was waiting.
	// Replay to the original terminal destination without stealing that focus.
	focus := a.focus
	a.focus = focusTerminal
	a.handleInput(data)
	if a.focus == focusTerminal {
		a.focus = focus
	}
}

func (s *inBandMonitorState) capture(err error) *monitorplan.Capture {
	now := time.Now()
	c := &monitorplan.Capture{StartedAt: s.startedAt, FinishedAt: now, DurationMS: now.Sub(s.startedAt).Milliseconds(), Transport: "ssh-primary", Streams: "merged", Stdout: s.rawContent, ExitCode: s.exitCode, Status: "disconnected"}
	if s.exitCode != nil {
		c.Complete = true
		c.Status = "exit_error"
		if *s.exitCode == 0 && err == nil {
			c.Status = "ok"
		}
	}
	if s.failureStatus != "" {
		c.Status = s.failureStatus
		c.Complete = false
	}
	return c
}
