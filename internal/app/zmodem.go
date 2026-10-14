package app

import (
	"bytes"
	"io"
	"os"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/terminalview"
)

var (
	zmodemStartMarker  = []byte{'*', '*', 0x18, 'B', '0'}
	zmodemFinishMarker = []byte{'*', '*', 0x18, 'B', '0', '8'}
	zmodemCancelMarker = []byte{0x18, 0x18, 0x18, 0x18, 0x18}
)

const zmodemFinishDelay = 750 * time.Millisecond

type zmodemPassthrough struct {
	clientStarted    bool
	handshakeSent    bool
	handshake        []byte
	handshakeTrailer int
	inputTail        []byte
	outputTail       []byte
	finishHeaderSeen bool
	cancelled        bool
	finishSeenAt     time.Time
}

func (a *App) outputWriter() io.Writer {
	if a.screenOut != nil {
		return a.screenOut
	}
	return os.Stdout
}

// processSSHOutput keeps all terminal mutation on the main event loop. This is
// important for ZMODEM: once a transfer starts, protocol bytes must bypass both
// the VT emulator and the normal TUI renderer without racing an in-flight read.
func (a *App) processSSHOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	data = a.filterRetiredExecutionMarkers(data)
	if len(data) == 0 {
		return
	}
	if a.processInBandMonitorOutput(data) {
		return
	}
	if a.zmodem != nil {
		a.processZmodemOutput(data)
		return
	}

	combined := append(append([]byte(nil), a.zmodemTail...), data...)
	marker := bytes.Index(combined, zmodemStartMarker)
	if marker < 0 {
		a.processRegularSSHOutput(data)
		a.rememberZmodemTail(data)
		return
	}

	dataMarker := marker - len(a.zmodemTail)
	if dataMarker > 0 {
		a.processRegularSSHOutput(data[:dataMarker])
	}
	start := dataMarker
	if start < 0 {
		if a.config.DesktopUI {
			// The prefix was already streamed to the desktop in the earlier read.
			a.enterZmodemPassthrough(data)
		} else {
			// The CLI only painted that prefix; its transfer helper still needs it.
			a.enterZmodemPassthrough(combined[marker:])
		}
	} else {
		a.enterZmodemPassthrough(data[start:])
	}
	a.zmodemTail = nil
}

func (a *App) processRegularSSHOutput(data []byte) {
	if state := a.execution; state != nil && state.BastionCompatibility {
		state.observeShellIntegration(data)
		a.processVisibleSSHOutput(data)
		a.checkBastionExecutionPrompt()
		a.submitExecutionResult()
		return
	}
	if state := a.execution; state != nil && !state.Direct && state.ExitCode == nil && (!state.EchoMarker || !state.ReceiptReceived) {
		output, after, exitCode := state.consume(data)
		a.processVisibleSSHOutput(output)
		if exitCode != nil || state.ReceiptReceived {
			state.ExitCode = exitCode
			if state.ReceiptReceived && state.Completion == "" {
				state.Completion = "completion_marker_received"
			}
			a.debugInput("execution marker=%s reason=%s", state.Marker, state.Completion)
			// Prompt detection must only see fresh output after completion.
			a.outputTail.value = ""
			a.processVisibleSSHOutput(after)
		}
		a.submitExecutionResult()
		return
	}
	a.processVisibleSSHOutput(data)
	a.submitExecutionResult()
}

func (a *App) processVisibleSSHOutput(data []byte) {
	if len(data) == 0 || a.virtual == nil {
		return
	}
	a.renderSSHOutput(data)
	a.handleOutput(data)
	a.flushDeferredMonitorInput()
}

// Render collector output without treating it as a prompt or AI context.
func (a *App) renderSSHOutput(data []byte) {
	if len(data) == 0 || a.virtual == nil {
		return
	}
	if a.config.DesktopUI {
		// Preserve the original bytes and terminal modes so xterm owns scrollback,
		// selection and mouse hit testing. Execution/monitor markers are already
		// removed before this point; the emulator still observes output below.
		_, _ = a.outputWriter().Write(data)
	}
	a.captureInlineResizeAnchors()
	tokens := a.transformer.TransformTokens(data)
	historyLines := a.writeTerminalOutput(tokens)
	scrolls := len(historyLines)
	a.appendTerminalHistory(historyLines)
	a.scrollInlineBlocks(scrolls)
	a.resolveInlineResizeAnchors()
	a.dirty = true
}

func (a *App) rememberZmodemTail(data []byte) {
	keep := len(zmodemStartMarker) - 1
	if keep <= 0 {
		a.zmodemTail = nil
		return
	}
	if len(data) > keep {
		data = data[len(data)-keep:]
	}
	a.zmodemTail = append(a.zmodemTail[:0], data...)
}

func (a *App) enterZmodemPassthrough(data []byte) {
	a.zmodem = &zmodemPassthrough{}
	a.mouseCarry = nil
	a.mouseReporting = false
	a.mouseScrollbarDrag = scrollbarDragState{}
	a.dirty = false
	if !a.config.DesktopUI {
		_, _ = io.WriteString(a.outputWriter(), "\x1b[?2026l"+mouseReportingDisable+"\x1b[0m\x1b[?25h\x1b[?1049l")
	}
	a.processZmodemOutput(data)
}

// A transfer command can finish before passthrough's idle timer fires. Keep
// its completion marker out of the binary channel and restore the TUI before
// analyzing the result; otherwise the marker would be lost in raw output.
func (a *App) processZmodemOutput(data []byte) {
	if state := a.execution; state != nil && !state.Direct && state.ExitCode == nil && (!state.EchoMarker || !state.ReceiptReceived) {
		output, after, exitCode := state.consume(data)
		a.handleZmodemOutput(output)
		if exitCode != nil || state.ReceiptReceived {
			state.ExitCode = exitCode
			if state.ReceiptReceived && state.Completion == "" {
				state.Completion = "completion_marker_received"
			}
			a.debugInput("execution marker=%s reason=%s", state.Marker, state.Completion)
			a.leaveZmodemPassthrough()
			a.outputTail.value = ""
			a.processVisibleSSHOutput(after)
		}
		a.submitExecutionResult()
		return
	}
	a.handleZmodemOutput(data)
}

func (a *App) handleZmodemOutput(data []byte) {
	state := a.zmodem
	if state == nil || len(data) == 0 {
		return
	}
	state.touchFinishTimer()

	state.outputTail = appendSignalTail(state.outputTail, data)
	if hasZmodemFinish(state.outputTail) {
		state.finishHeaderSeen = true
		// The receiver's ZFIN is the final remote acknowledgement. Keep passing
		// bytes until the short idle window expires so the local helper can reply.
		state.markFinished()
	}
	if bytes.Contains(state.outputTail, zmodemCancelMarker) {
		state.cancelled = true
		state.markFinished()
	}
	if state.finishHeaderSeen && bytes.Contains(state.outputTail, []byte("OO")) {
		state.markFinished()
	}
	if state.cancelled {
		return
	}

	if !state.handshakeSent {
		_, _ = a.outputWriter().Write(data)
		state.handshake = append(state.handshake, data...)
		if end := bytes.IndexByte(state.handshake, '\r'); end >= 0 {
			state.handshakeSent = true
			state.handshakeTrailer = 2 - (len(state.handshake) - end - 1)
			if state.handshakeTrailer < 0 {
				state.handshakeTrailer = 0
			}
			state.handshake = nil
		}
		return
	}

	if !state.clientStarted {
		// rz commonly retries ZRINIT while the file picker is open. Repainting each
		// retry makes terminal integrations reopen the picker forever after cancel.
		// Forward only the LF/XON trailer belonging to the first handshake.
		if state.handshakeTrailer > 0 {
			count := state.handshakeTrailer
			if count > len(data) {
				count = len(data)
			}
			_, _ = a.outputWriter().Write(data[:count])
			state.handshakeTrailer -= count
		}
		return
	}

	_, _ = a.outputWriter().Write(data)
}

func (a *App) handleZmodemInput(data []byte) {
	state := a.zmodem
	if state == nil || len(data) == 0 {
		return
	}
	state.clientStarted = true
	state.touchFinishTimer()
	if bytes.Contains(data, []byte{0x03}) && a.execution != nil {
		a.stopExecutionWait("cancelled")
	}
	hadFinishHeader := state.finishHeaderSeen
	state.inputTail = appendSignalTail(state.inputTail, data)
	if hasZmodemFinish(state.inputTail) {
		state.finishHeaderSeen = true
	}
	if bytes.Contains(state.inputTail, zmodemCancelMarker) || bytes.Contains(data, []byte{0x03}) {
		state.cancelled = true
		state.markFinished()
	}
	if hadFinishHeader && bytes.Contains(data, []byte("OO")) {
		state.markFinished()
	}
	if a.sshPTY != nil {
		_, _ = a.writeSSH(data)
	}
}

func appendSignalTail(tail, data []byte) []byte {
	const limit = 96
	tail = append(tail, data...)
	if len(tail) > limit {
		tail = append([]byte(nil), tail[len(tail)-limit:]...)
	}
	return tail
}

func hasZmodemFinish(data []byte) bool {
	return bytes.Contains(data, zmodemFinishMarker)
}

func (state *zmodemPassthrough) markFinished() {
	state.finishSeenAt = time.Now()
}

func (state *zmodemPassthrough) touchFinishTimer() {
	if !state.finishSeenAt.IsZero() {
		state.finishSeenAt = time.Now()
	}
}

func (a *App) checkZmodemCompletion(now time.Time) {
	if a.zmodem == nil || a.zmodem.finishSeenAt.IsZero() || now.Sub(a.zmodem.finishSeenAt) < zmodemFinishDelay {
		return
	}
	a.leaveZmodemPassthrough()
}

func (a *App) leaveZmodemPassthrough() {
	if a.zmodem == nil {
		return
	}
	a.zmodem = nil
	a.zmodemTail = nil
	a.transformer = terminalview.WideTransformer{}
	a.sidebarFrame = ""
	a.terminalRows = nil
	a.terminalScroll = 0
	a.lineInput = nil
	a.lineCursor = 0
	a.lineAnchored = false
	a.lineReliable = false
	a.terminalKeyCarry = nil
	if !a.config.DesktopUI {
		_, _ = io.WriteString(a.outputWriter(), "\x1b[?1049h\x1b[2J\x1b[H"+mouseReportingDisable)
	}
	a.dirty = true
	// Ask the remote shell for a fresh prompt so the VT model, which intentionally
	// skipped binary transfer traffic, is synchronized again without replaying it.
	if a.sshPTY != nil {
		_, _ = a.writeSSH([]byte{'\r'})
	}
}
