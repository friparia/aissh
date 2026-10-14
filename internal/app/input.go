package app

import (
	"bytes"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hinshun/vt10x"

	"git.example.com/ai/wrd-ai-cli/internal/panel"
	"git.example.com/ai/wrd-ai-cli/internal/policy"
	"git.example.com/ai/wrd-ai-cli/internal/terminalview"
	"git.example.com/ai/wrd-ai-cli/internal/trigger"
)

var (
	terminalPasteStart = []byte("\x1b[200~")
	terminalPasteEnd   = []byte("\x1b[201~")
)

const (
	bracketedPasteEnable  = "\x1b[?2004h"
	bracketedPasteDisable = "\x1b[?2004l"
)

func (a *App) handleInput(data []byte) {
	if len(data) == 0 {
		return
	}
	if a.aiNotice != "" {
		a.aiNotice = ""
		a.dirty = true
	}
	if a.zmodem != nil {
		a.handleZmodemInput(data)
		return
	}
	if !a.config.DesktopUI && len(data) == 1 && data[0] == 0x19 && !a.terminalPasteMode && !a.aiPasteMode && a.canCollectExecutionOutput() {
		_ = a.collectExecutionOutput() // Ctrl+Y: local snapshot, no remote input.
		return
	}
	if len(data) == 1 && data[0] == 0x07 && !a.terminalPasteMode && !a.aiPasteMode {
		a.takeOverTerminal("已取消本次 AI 交互")
		return
	}
	if a.terminalTakeover {
		a.handleSensitiveTerminalInput(data)
		return
	}
	if a.config.DesktopUI {
		// Mouse reports and terminal shortcuts belong to the remote application;
		// the desktop has separate controls for AI and local scrolling.
		a.handleKeyboardInput(data)
		return
	}
	if len(data) == 1 && data[0] == 0x1d { // Ctrl+]
		a.toggleMouseMode()
		return
	}
	if !a.sensitive && bytes.Contains(data, []byte{0x0f}) { // Ctrl+O
		filtered := make([]byte, 0, len(data))
		for _, value := range data {
			if value == 0x0f && a.expandCommandShortcut() {
				continue
			}
			filtered = append(filtered, value)
		}
		data = filtered
		if len(data) == 0 {
			return
		}
	}
	redraw := a.focus != focusTerminal || a.aiLineCapture || a.inlineAI != nil || a.proposal != nil || a.terminalScroll > 0 || terminalInputHasLocalAction(data)
	a.handleMixedInput(data)
	if redraw || a.focus != focusTerminal || a.aiLineCapture || a.inlineAI != nil || a.proposal != nil || a.terminalScroll > 0 {
		a.dirty = true
	}
}

func terminalInputHasLocalAction(data []byte) bool {
	for _, value := range data {
		switch value {
		case 0x0c, 0x0e, 0x14, 0x1f: // Ctrl+L, Ctrl+N, Ctrl+T, Ctrl+Shift+-
			return true
		}
	}
	return false
}

func isSidebarToggleShortcut(data []byte) bool {
	return len(data) == 1 && data[0] == 0x1f // Ctrl+Shift+- (Ctrl+_)
}

// Returning to the SSH pane must keep a pending confirmation local. Sending
// "yes" to the shell instead would start the Unix yes command.
func (a *App) focusTerminalWorkspace() {
	if a.proposal != nil {
		a.focus = focusApproval
	} else {
		a.focus = focusTerminal
	}
}

func (a *App) handleKeyboardInput(data []byte) {
	if a.config.DesktopUI {
		a.focus = focusTerminal
		a.handleTerminalInput(data)
		return
	}
	if a.handleExecutionControl(data) {
		return
	}
	if a.focus == focusTerminal {
		// Also guard against a stale focus set by an asynchronous event.
		a.focusTerminalWorkspace()
	}
	diagnosticInput, diagnosticHan := false, false
	if a.inputDebug != nil {
		diagnosticHan = containsHanInput(data)
		diagnosticInput = diagnosticHan || bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, '\n') >= 0
		if diagnosticInput {
			a.debugInput("keyboard bytes=%d focus=%d prompt_before=%t reliable=%t capture=%t han=%t", len(data), a.focus, a.atPrompt, a.lineReliable, a.aiLineCapture, diagnosticHan)
		}
	}
	if a.focus == focusTerminal {
		a.refreshPromptFromScreen()
	}
	if diagnosticInput {
		a.debugInput("keyboard prompt_after=%t sensitive=%t", a.atPrompt, a.sensitive)
	}
	if isSidebarToggleShortcut(data) && !a.compact && (a.focus != focusTerminal || a.atPrompt) {
		a.toggleSidebar()
		return
	}
	switch a.focus {
	case focusApproval:
		a.handleApprovalInput(data)
	case focusAI:
		a.handleAIInput(data)
	case focusAIHistory:
		a.handleAIHistoryInput(data)
	case focusMonitor:
		a.handleMonitorInput(data)
	case focusMonitorSlot:
		a.handleMonitorSlotInput(data)
	case focusPanelMenu:
		a.handlePanelMenuInput(data)
	case focusCustomPanel:
		a.handleCustomPanelInput(data)
	default:
		a.handleTerminalInput(data)
	}
}

func (a *App) handleTerminalInput(data []byte) {
	a.terminalInputAt = time.Now()
	if a.handleExecutionControl(data) {
		return
	}
	if !a.sensitive && a.safePromptScreen() && (a.executionRecovery || (a.execution != nil && a.execution.sending())) {
		// Do not queue typing behind an incomplete or cancelled command.
		return
	}
	// Only input destined for SSH waits for a primary-session monitor.
	// Local editors, mouse events and navigation stay responsive.
	if a.handleInBandMonitorInput(data) {
		return
	}
	a.refreshPromptFromScreen()
	if a.terminalScroll > 0 {
		a.terminalScroll = 0
		a.terminalRows = nil
	}
	if a.sensitive {
		a.handleSensitiveTerminalInput(data)
		return
	}
	if a.inlineAI != nil {
		if a.agentBusy {
			if len(data) > 0 {
				switch data[0] {
				case 0x0c:
					a.clearInlineTerminalOutput()
					if a.sshPTY != nil {
						_, _ = a.writeSSH(data[:1])
					}
				}
			}
			return
		}
		if a.proposal == nil {
			a.archiveInlineAI()
		}
	}
	for len(data) > 0 {
		if a.terminalPasteMode {
			a.terminalPasteBuffer = append(a.terminalPasteBuffer, data...)
			end := bytes.Index(a.terminalPasteBuffer, terminalPasteEnd)
			if end < 0 {
				return
			}
			payload := append([]byte(nil), a.terminalPasteBuffer[:end]...)
			data = append([]byte(nil), a.terminalPasteBuffer[end+len(terminalPasteEnd):]...)
			a.terminalPasteBuffer = nil
			a.terminalPasteMode = false
			if a.finishTerminalPaste(payload) {
				return
			}
			continue
		}

		start := bytes.Index(data, terminalPasteStart)
		if start < 0 {
			a.handleTerminalBytes(data)
			return
		}
		if start > 0 {
			a.handleTerminalBytes(data[:start])
			if a.inlineAI != nil && a.agentBusy {
				return
			}
		}
		a.captureLineAnchor()
		a.terminalPasteMode = true
		a.terminalPasteBuffer = nil
		data = data[start+len(terminalPasteStart):]
	}
}

// handleSensitiveTerminalInput keeps password and passphrase entry out of the
// command/AI input state. The outer terminal has bracketed paste enabled, but
// OpenSSH's password reader does not expect those wrapper bytes, so only the
// pasted payload may be forwarded to the SSH PTY.
func (a *App) handleSensitiveTerminalInput(data []byte) {
	a.lineInput = nil
	a.lineCursor = 0
	a.lineAnchored = false
	a.lineReliable = true
	a.aiLineCapture = false
	a.terminalKeyCarry = nil

	for len(data) > 0 {
		if a.terminalPasteMode {
			a.terminalPasteBuffer = append(a.terminalPasteBuffer, data...)
			end := bytes.Index(a.terminalPasteBuffer, terminalPasteEnd)
			if end < 0 {
				return
			}
			payload := append([]byte(nil), a.terminalPasteBuffer[:end]...)
			data = append([]byte(nil), a.terminalPasteBuffer[end+len(terminalPasteEnd):]...)
			a.terminalPasteBuffer = nil
			a.terminalPasteMode = false
			if a.sshPTY != nil {
				_, _ = a.writeSSH(payload)
			}
			continue
		}

		start := bytes.Index(data, terminalPasteStart)
		if start < 0 {
			if a.sshPTY != nil {
				_, _ = a.writeSSH(data)
			}
			return
		}
		if start > 0 && a.sshPTY != nil {
			_, _ = a.writeSSH(data[:start])
		}
		a.terminalPasteMode = true
		a.terminalPasteBuffer = nil
		data = data[start+len(terminalPasteStart):]
	}
}

func (a *App) handleTerminalBytes(data []byte) {
	if len(a.terminalKeyCarry) > 0 {
		data = append(append([]byte(nil), a.terminalKeyCarry...), data...)
		a.terminalKeyCarry = nil
	}
	for len(data) > 0 {
		if a.aiLineCapture && data[0] == 0x1b {
			consumed, complete := terminalEscapeSequenceLength(data)
			if !complete {
				a.terminalKeyCarry = append([]byte(nil), data...)
				return
			}
			a.handleCapturedLineEscape(data[:consumed])
			data = data[consumed:]
			continue
		}
		size := 1
		if data[0] >= utf8.RuneSelf {
			_, decodedSize := utf8.DecodeRune(data)
			if decodedSize > 1 {
				size = decodedSize
			}
		}
		unit := append([]byte(nil), data[:size]...)
		data = data[size:]
		value := unit[0]
		if size > 1 {
			r, _ := utf8.DecodeRune(unit)
			a.captureLineAnchor()
			if !a.config.DesktopUI && a.atPrompt && a.lineReliable && len(a.lineInput) == 0 && unicode.Is(unicode.Han, r) {
				a.aiLineCapture = true
			}
			if a.lineReliable {
				if a.aiLineCapture {
					a.insertCapturedLineBytes(unit)
				} else {
					a.lineInput = append(a.lineInput, unit...)
					a.lineCursor = len(a.lineInput)
				}
			}
			if !a.aiLineCapture {
				_, _ = a.writeSSH(unit)
			}
			continue
		}
		switch value {
		case 0x03: // Ctrl+C
			if a.handleExecutionControl(unit) {
				return
			}
			captured := a.aiLineCapture
			a.lineInput = nil
			a.lineCursor = 0
			a.lineAnchored = false
			a.lineReliable = true
			a.aiLineCapture = false
			a.terminalKeyCarry = nil
			if captured {
				continue
			}
		case 0x01: // Ctrl+A
			if a.aiLineCapture {
				a.lineCursor = 0
				continue
			}
			a.lineReliable = false
		case 0x04: // Ctrl+D
			if a.aiLineCapture {
				a.deleteCapturedLineRune()
				continue
			}
		case 0x05: // Ctrl+E
			if a.aiLineCapture {
				a.lineCursor = len(a.lineInput)
				continue
			}
			a.lineReliable = false
		case 0x14: // Ctrl+T
			if a.atPrompt && !a.config.DesktopUI {
				a.ensureSidebarVisible()
				a.panels.Next()
				if a.compact {
					a.focus = focusMonitor
				}
				continue
			}
		case 0x0e: // Ctrl+N
			if a.atPrompt && !a.config.DesktopUI {
				a.ensureSidebarVisible()
				a.focus = focusPanelMenu
				a.menuStatus = ""
				continue
			}
		case 0x1f: // Ctrl+Shift+- (Ctrl+_)
			if a.atPrompt && !a.compact && !a.config.DesktopUI {
				a.toggleSidebar()
				continue
			}
		case 0x0c: // Ctrl+L
			a.clearInlineTerminalOutput()
		case '\r', '\n':
			a.submitTerminalLine(value)
			if a.inlineAI != nil && a.agentBusy {
				return
			}
			continue
		case 0x08, 0x7f:
			if a.lineReliable {
				if a.aiLineCapture {
					a.backspaceCapturedLineRune()
				} else {
					a.lineInput = removeLastUTF8Rune(a.lineInput)
					a.lineCursor = len(a.lineInput)
				}
			}
		case 0x15: // Ctrl+U
			a.lineInput = nil
			a.lineCursor = 0
			a.lineAnchored = false
			a.lineReliable = true
			a.terminalKeyCarry = nil
			if a.aiLineCapture {
				a.aiLineCapture = false
				continue
			}
		case 0x1b:
			captured := a.aiLineCapture
			a.lineInput = nil
			a.lineCursor = 0
			a.lineAnchored = false
			a.lineReliable = false
			a.aiLineCapture = false
			a.terminalKeyCarry = nil
			if captured {
				continue
			}
		}
		if value >= 0x20 && value != 0x7f && a.lineReliable {
			a.captureLineAnchor()
			if a.aiLineCapture {
				a.insertCapturedLineBytes(unit)
			} else {
				a.lineInput = append(a.lineInput, value)
				a.lineCursor = len(a.lineInput)
			}
		}
		if !a.aiLineCapture {
			_, _ = a.writeSSH(unit)
		}
	}
}

func (a *App) finishTerminalPaste(payload []byte) bool {
	value := strings.ReplaceAll(string(payload), "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	captured := a.aiLineCapture
	line := string(a.lineInput) + value
	if !a.config.DesktopUI && a.atPrompt && a.lineReliable && !a.sensitive {
		// Keep pasted local commands editable until Enter. Shell pastes
		// otherwise lose line tracking and bypass local commands.
		pastedLine := strings.TrimRight(line, "\n")
		_, sidebarCommand := parseLocalSidebarCommand(pastedLine)
		if (sidebarCommand || isOpenAIConversationCommand(pastedLine)) && !strings.Contains(pastedLine, "\n") {
			if !captured && len(a.lineInput) > 0 && a.sshPTY != nil {
				_, _ = a.writeSSH([]byte{0x15})
			}
			a.lineInput = []byte(pastedLine)
			a.lineCursor = len(a.lineInput)
			a.aiLineCapture = true
			a.terminalKeyCarry = nil
			a.dirty = true
			return false
		}
		if prompt, ok := trigger.IsAIInput(line); ok {
			if !captured && len(a.lineInput) > 0 && a.sshPTY != nil {
				_, _ = a.writeSSH([]byte{0x15})
			}
			a.lineInput = nil
			a.lineCursor = 0
			a.aiLineCapture = false
			a.lineReliable = true
			a.terminalKeyCarry = nil
			a.startInlineAI(prompt, line, true)
			return true
		}
	}

	a.lineInput = nil
	a.lineCursor = 0
	a.lineAnchored = false
	a.lineReliable = false
	a.aiLineCapture = false
	a.terminalKeyCarry = nil
	if a.sshPTY != nil {
		a.terminalResizePending = false
		a.terminalResizeAnchors = nil
		_, _ = a.writeSSH(terminalPasteStart)
		_, _ = a.writeSSH(payload)
		_, _ = a.writeSSH(terminalPasteEnd)
	}
	return false
}

func (a *App) submitTerminalLine(newline byte) {
	line := string(a.lineInput)
	a.lineInput = nil
	a.lineCursor = 0
	a.terminalKeyCarry = nil
	captured := a.aiLineCapture
	a.aiLineCapture = false
	reliable := a.lineReliable
	a.lineReliable = true
	if a.inputDebug != nil {
		_, aiSyntax := trigger.IsAIInput(line)
		a.debugInput("submit bytes=%d han=%t ai_syntax=%t prompt=%t reliable=%t captured=%t sensitive=%t", len(line), containsHanInput([]byte(line)), aiSyntax, a.atPrompt, reliable, captured, a.sensitive)
	}
	if !a.config.DesktopUI && a.atPrompt && reliable && !a.sensitive {
		if isOpenAIConversationCommand(line) {
			if !captured && a.sshPTY != nil {
				_, _ = a.writeSSH([]byte{0x15})
			}
			a.lineAnchored = false
			a.pendingError = nil
			a.openAIConversation()
			return
		}
		if _, ok := parseLocalSidebarCommand(line); ok {
			if !captured && a.sshPTY != nil {
				_, _ = a.writeSSH([]byte{0x15})
			}
			a.handleLocalSidebarCommand(line, true)
			a.lineAnchored = false
			a.pendingError = nil
			return
		}
		if _, ok := parseLocalWorkspaceCommand(line); ok {
			if !captured && a.sshPTY != nil {
				_, _ = a.writeSSH([]byte{0x15})
			}
			a.handleLocalWorkspaceCommand(line, true)
			a.pendingError = nil
			return
		}
		if prompt, ok := trigger.IsAIInput(line); ok {
			if !captured {
				_, _ = a.writeSSH([]byte{0x15})
			}
			a.startInlineAI(prompt, line, captured)
			return
		}
		if command, ok := trigger.ForceShellInput(line); ok {
			_, _ = a.writeSSH([]byte{0x15})
			a.beginCommandOutput(command)
			_, _ = a.writeSSH([]byte(command + "\r"))
			a.lastCommand = command
			a.atPrompt = false
			a.lineAnchored = false
			return
		}
		if isTerminalClearCommand(line) {
			a.clearInlineTerminalOutput()
		}
	}
	if captured {
		if newline == '\n' {
			newline = '\r'
		}
		a.beginCommandOutput(line)
		_, _ = a.writeSSH([]byte(line))
		_, _ = a.writeSSH([]byte{newline})
		a.lastCommand = strings.TrimSpace(line)
		a.atPrompt = false
		a.pendingError = nil
		a.lineAnchored = false
		return
	}
	if newline == '\n' {
		newline = '\r'
	}
	a.beginCommandOutput(line)
	_, _ = a.writeSSH([]byte{newline})
	a.lastCommand = strings.TrimSpace(line)
	a.atPrompt = false
	a.pendingError = nil
	a.lineAnchored = false
}

func terminalEscapeSequenceLength(data []byte) (int, bool) {
	if len(data) == 0 || data[0] != 0x1b {
		return 0, false
	}
	if len(data) == 1 {
		return 0, false
	}
	switch data[1] {
	case '[':
		for index := 2; index < len(data); index++ {
			if data[index] >= 0x40 && data[index] <= 0x7e {
				return index + 1, true
			}
		}
		return 0, false
	case 'O':
		if len(data) < 3 {
			return 0, false
		}
		return 3, true
	default:
		// A standalone Esc is buffered until another byte arrives. Once it is
		// followed by a non-sequence byte, consume only Esc and let that byte be
		// handled normally (notably Ctrl+C and Enter).
		return 1, true
	}
}

func (a *App) handleCapturedLineEscape(sequence []byte) {
	if len(sequence) < 2 {
		return
	}
	final := sequence[len(sequence)-1]
	parameters := ""
	if sequence[1] == '[' && len(sequence) > 3 {
		parameters = string(sequence[2 : len(sequence)-1])
	}
	switch final {
	case 'C':
		a.moveCapturedLineCursor(1)
	case 'D':
		a.moveCapturedLineCursor(-1)
	case 'H':
		a.lineCursor = 0
	case 'F':
		a.lineCursor = len(a.lineInput)
	case '~':
		switch parameters {
		case "1", "7":
			a.lineCursor = 0
		case "4", "8":
			a.lineCursor = len(a.lineInput)
		case "3":
			a.deleteCapturedLineRune()
		}
	}
}

func (a *App) clampCapturedLineCursor() {
	if a.lineCursor < 0 {
		a.lineCursor = 0
	}
	if a.lineCursor > len(a.lineInput) {
		a.lineCursor = len(a.lineInput)
	}
	for a.lineCursor > 0 && a.lineCursor < len(a.lineInput) && !utf8.RuneStart(a.lineInput[a.lineCursor]) {
		a.lineCursor--
	}
}

func (a *App) insertCapturedLineBytes(value []byte) {
	a.clampCapturedLineCursor()
	start := a.lineCursor
	a.lineInput = append(a.lineInput, make([]byte, len(value))...)
	copy(a.lineInput[start+len(value):], a.lineInput[start:len(a.lineInput)-len(value)])
	copy(a.lineInput[start:start+len(value)], value)
	a.lineCursor += len(value)
}

func (a *App) backspaceCapturedLineRune() {
	a.clampCapturedLineCursor()
	if a.lineCursor == 0 {
		return
	}
	_, size := utf8.DecodeLastRune(a.lineInput[:a.lineCursor])
	if size <= 0 {
		size = 1
	}
	start := a.lineCursor - size
	copy(a.lineInput[start:], a.lineInput[a.lineCursor:])
	a.lineInput = a.lineInput[:len(a.lineInput)-size]
	a.lineCursor = start
}

func (a *App) deleteCapturedLineRune() {
	a.clampCapturedLineCursor()
	if a.lineCursor >= len(a.lineInput) {
		return
	}
	_, size := utf8.DecodeRune(a.lineInput[a.lineCursor:])
	if size <= 0 {
		size = 1
	}
	copy(a.lineInput[a.lineCursor:], a.lineInput[a.lineCursor+size:])
	a.lineInput = a.lineInput[:len(a.lineInput)-size]
}

func (a *App) moveCapturedLineCursor(delta int) {
	a.clampCapturedLineCursor()
	if delta < 0 && a.lineCursor > 0 {
		_, size := utf8.DecodeLastRune(a.lineInput[:a.lineCursor])
		if size <= 0 {
			size = 1
		}
		a.lineCursor -= size
	}
	if delta > 0 && a.lineCursor < len(a.lineInput) {
		_, size := utf8.DecodeRune(a.lineInput[a.lineCursor:])
		if size <= 0 {
			size = 1
		}
		a.lineCursor += size
	}
}

func (a *App) captureLineAnchor() {
	if a.lineAnchored || !a.atPrompt || !a.lineReliable || len(a.lineInput) != 0 || a.virtual == nil {
		return
	}
	cursor := a.virtual.Cursor()
	a.lineAnchorX = cursor.X
	a.lineAnchorY = cursor.Y
	a.lineAnchored = true
}

// refreshPromptFromScreen is a fallback for terminals that render a shell
// prompt in chunks which do not independently match the output-tail prompt
// detector. This is particularly common with Win32-OpenSSH. By the time the
// user types, the virtual terminal has the complete prompt on its cursor row,
// so use that visible state before deciding whether Chinese input belongs to
// the inline AI.
func (a *App) refreshPromptFromScreen() {
	if a.atPrompt || a.virtual == nil || a.execution != nil || a.executionRecovery || a.monitorAwaitingPrompt {
		return
	}
	a.refreshGatewayShellPrompt()

	a.virtual.Lock()
	if !a.safePromptScreenLocked() {
		a.debugInput("screen_prompt unavailable alt_screen=%t cursor_visible=%t", a.virtual.Mode()&vt10x.ModeAltScreen != 0, a.virtual.CursorVisible())
		a.virtual.Unlock()
		return
	}
	cursor := a.virtual.Cursor()
	visibleLine, ok := a.terminalCursorLineLocked()
	a.virtual.Unlock()
	if !ok {
		return
	}
	trimmedLine := strings.TrimRightFunc(visibleLine, unicode.IsSpace)
	lastRune, _ := utf8.DecodeLastRuneInString(trimmedLine)
	matched := trigger.LooksLikePrompt(visibleLine)
	a.debugInput("screen_prompt match=%t cursor_x=%d cursor_y=%d chars=%d last=%U", matched, cursor.X, cursor.Y, len([]rune(trimmedLine)), lastRune)
	if !matched {
		return
	}
	a.atPrompt = true
	a.sensitive = false
	a.promptIdleSince = time.Now()
}

// The caller holds the virtual terminal lock.
func (a *App) terminalCursorLineLocked() (string, bool) {
	cursor := a.virtual.Cursor()
	cols, rows := a.virtual.Size()
	if cursor.Y < 0 || cursor.Y >= rows || cursor.X < 0 || cols <= 0 {
		return "", false
	}
	var line strings.Builder
	for x := 0; x < min(cursor.X, cols); x++ {
		switch char := a.virtual.Cell(x, cursor.Y).Char; char {
		case terminalview.WidePlaceholder:
		case 0:
			line.WriteByte(' ')
		default:
			line.WriteRune(char)
		}
	}
	return line.String(), true
}

func containsHanInput(data []byte) bool {
	for _, value := range string(data) {
		if unicode.Is(unicode.Han, value) {
			return true
		}
	}
	return false
}

func (a *App) handleAIInput(data []byte) {
	for len(data) > 0 {
		if data[0] == 0x1b {
			if consumed, handled := a.consumeAIEditorEscape(data); handled {
				data = data[consumed:]
				continue
			}
			a.focusTerminalWorkspace()
			a.aiPasteMode = false
			return
		}
		value := data[0]
		data = data[1:]
		switch value {
		case 0x14: // Ctrl+T
			a.focus = focusMonitor
			return
		case 0x0e: // Ctrl+N
			a.focus = focusPanelMenu
			return
		case 0x12: // Ctrl+R
			a.openAIHistory()
			return
		case '\r':
			if a.aiPasteMode {
				a.insertAIEditorRune('\n')
				if len(data) > 0 && data[0] == '\n' {
					data = data[1:]
				}
				continue
			}
			prompt := strings.TrimSpace(string(a.editor))
			if prompt != "" {
				if a.handleLocalSidebarCommand(prompt, false) || a.handleLocalWorkspaceCommand(prompt, false) {
					a.editor = nil
					a.aiEditorCursor = 0
					return
				}
			}
			if prompt != "" && !a.agentBusy {
				a.editor = nil
				a.aiEditorCursor = 0
				a.startAI(prompt, true)
			}
			return
		case '\n': // Ctrl+J; terminals that distinguish Shift+Enter use an escape sequence.
			a.insertAIEditorRune('\n')
			continue
		case 0x08, 0x7f:
			a.backspaceAIEditorRune()
			continue
		case 0x01: // Ctrl+A
			a.moveAIEditorHome()
			continue
		case 0x02: // Ctrl+B
			a.moveAIEditorCursor(-1)
			continue
		case 0x04: // Ctrl+D
			a.deleteAIEditorRune()
			continue
		case 0x05: // Ctrl+E
			a.moveAIEditorEnd()
			continue
		case 0x06: // Ctrl+F
			a.moveAIEditorCursor(1)
			continue
		case 0x15: // Ctrl+U
			a.editor = nil
			a.aiEditorCursor = 0
			continue
		}
		if value < 0x20 {
			continue
		}
		r, size := utf8.DecodeRune(append([]byte{value}, data...))
		if r == utf8.RuneError && size == 1 {
			continue
		}
		a.insertAIEditorRune(r)
		if size > 1 {
			data = data[size-1:]
		}
	}
}

func (a *App) handleAIHistoryInput(data []byte) {
	if bytes.HasPrefix(data, []byte("\x1b[A")) {
		a.moveHistorySelection(-1)
		return
	}
	if bytes.HasPrefix(data, []byte("\x1b[B")) {
		a.moveHistorySelection(1)
		return
	}
	if len(data) == 0 {
		return
	}
	switch data[0] {
	case 0x1b:
		a.focus = focusAI
	case '\r', '\n':
		a.selectHistoryConversation(a.historySelection)
	case 'j', 'J':
		a.moveHistorySelection(1)
	case 'k', 'K':
		a.moveHistorySelection(-1)
	case 'n', 'N':
		a.selectHistoryConversation(0)
	}
}

func (a *App) handleMonitorInput(data []byte) {
	if len(data) == 0 {
		return
	}
	switch data[0] {
	case 0x1b:
		a.focusTerminalWorkspace()
	case 0x14:
		a.panels.Next()
	case 0x0c: // Ctrl+L
		a.cycleMonitorLayout()
	case 0x0e:
		a.focus = focusPanelMenu
	}
}

func (a *App) handleMonitorSlotInput(data []byte) {
	if len(data) == 0 {
		return
	}
	switch data[0] {
	case 0x1b:
		a.monitorSlot = -1
		a.focus = focusMonitor
	case 0x14:
		a.monitorSlot = -1
		a.panels.Next()
		a.focus = focusMonitor
	case 0x0c:
		a.monitorSlot = -1
		a.cycleMonitorLayout()
		a.focus = focusMonitor
	case 0x0e:
		a.monitorSlot = -1
		a.focus = focusPanelMenu
	default:
		if data[0] >= '1' && data[0] <= '9' {
			a.assignMonitorSlot(int(data[0] - '1'))
		}
	}
}

func (a *App) handleApprovalInput(data []byte) {
	if a.proposal == nil {
		a.focus = focusTerminal
		return
	}
	if a.agentBusy {
		return
	}
	for len(data) > 0 {
		value := data[0]
		data = data[1:]
		switch value {
		case '\r', '\n':
			choice, ok := approvalChoice(string(a.approvalInput))
			if !ok {
				a.approvalStatus = a.t("请输入 yes 或 no 后回车")
				return
			}
			a.approvalInput = nil
			a.approvalStatus = ""
			if choice {
				a.approveProposal()
			} else {
				a.rejectProposal()
			}
			return
		case 0x08, 0x7f:
			a.approvalInput = appendRuneBackspace(a.approvalInput)
			a.approvalStatus = ""
			continue
		case 0x15:
			a.approvalInput = nil
			a.approvalStatus = ""
			continue
		case 0x1b:
			a.approvalStatus = a.t("请选择 yes（执行）或 no（拒绝）")
			continue
		}
		if value < 0x20 {
			continue
		}
		r, size := utf8.DecodeRune(append([]byte{value}, data...))
		if r == utf8.RuneError && size == 1 {
			continue
		}
		a.approvalInput = append(a.approvalInput, r)
		a.approvalStatus = ""
		if size > 1 {
			data = data[size-1:]
		}
	}
}

func approvalChoice(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "y", "yes", "是", "执行", "确认":
		return true, true
	case "n", "no", "否", "拒绝", "取消":
		return false, true
	default:
		return false, false
	}
}

func (a *App) handlePanelMenuInput(data []byte) {
	if len(data) == 0 {
		return
	}
	switch data[0] {
	case 0x1b:
		a.focusTerminalWorkspace()
	case '1':
		a.panels.Activate("system")
		a.focusTerminalWorkspace()
	case '2':
		a.openUserPanel(a.t("进程"), panel.KindTable, panel.BuiltinProcessCommand, 3*time.Second)
	case '3':
		a.openUserPanel(a.t("磁盘"), panel.KindMonitor, panel.BuiltinDiskCommand, 10*time.Second)
	case '4':
		a.openUserPanel(a.t("网络"), panel.KindMonitor, panel.BuiltinNetworkCommand, 3*time.Second)
	case '5':
		a.openUserPanel(a.t("服务"), panel.KindTable, panel.BuiltinServiceCommand, 10*time.Second)
	case '6', '7', 'c', 'C':
		a.focus = focusCustomPanel
		a.editor = nil
		a.menuStatus = a.t("输入只读监控命令")
	}
}

func (a *App) handleCustomPanelInput(data []byte) {
	for len(data) > 0 {
		value := data[0]
		data = data[1:]
		switch value {
		case 0x1b:
			a.focusTerminalWorkspace()
			a.editor = nil
			return
		case '\r', '\n':
			command := strings.TrimSpace(string(a.editor))
			if !policy.IsReadOnlyMonitorCommand(command) {
				a.menuStatus = a.t("命令不是只读命令，请修改后重试")
				return
			}
			a.openUserPanel(a.t("自定义"), panel.KindMonitor, command, 3*time.Second)
			a.editor = nil
			return
		case 0x08, 0x7f:
			a.editor = appendRuneBackspace(a.editor)
			continue
		case 0x15:
			a.editor = nil
			continue
		}
		if unicode.IsControl(rune(value)) {
			continue
		}
		r, size := utf8.DecodeRune(append([]byte{value}, data...))
		if r == utf8.RuneError && size == 1 {
			continue
		}
		a.editor = append(a.editor, r)
		if size > 1 {
			data = data[size-1:]
		}
	}
}

func (a *App) openUserPanel(title string, kind panel.Kind, command string, interval time.Duration) {
	item := a.panels.Open(panel.Panel{
		Kind:        kind,
		Origin:      panel.OriginUser,
		Title:       title,
		Command:     command,
		Interval:    interval,
		AllowAIRead: false,
		Target:      a.parsed.Target,
	})
	a.panels.Activate(item.ID)
	if a.compact {
		a.focus = focusMonitor
	} else {
		a.ensureSidebarVisible()
		a.focus = focusTerminal
	}
	a.monitorLast[item.ID] = time.Time{}
}

func removeLastUTF8Rune(value []byte) []byte {
	if len(value) == 0 {
		return value
	}
	_, size := utf8.DecodeLastRune(value)
	if size <= 0 {
		size = 1
	}
	return value[:len(value)-size]
}

func isOpenAIConversationCommand(line string) bool {
	return strings.EqualFold(strings.TrimSpace(line), "/ai")
}

func (a *App) openAIConversation() {
	a.ensureSidebarVisible()
	a.focus = focusAI
	a.aiExpanded = true
	a.clampAIEditorCursor()
}

func (a *App) inputStatus() string {
	if a.terminalTakeover {
		return a.t("终端已接管；可输入 Q 退出金库申请，自动采集已暂停")
	}
	if a.executionRecovery {
		return a.t("等待新的 Shell 提示符；Ctrl+C 重新同步")
	}
	if a.execution != nil && !a.execution.finished() {
		return a.t("正在等待命令结果；Ctrl+C 中断并恢复")
	}
	if a.monitorPrimaryPaused && a.monitorAwaitingPrompt {
		return a.t("监控已暂停，等待 Shell 提示符；可按 Ctrl+C 中断")
	}
	if a.focus == focusTerminal && a.terminalScroll > 0 {
		return a.t("SSH 历史  ↑ %d / %d 行  键盘输入返回实时终端", a.terminalScroll, len(a.terminalHistory))
	}
	if a.inlineAI != nil && a.focus == focusTerminal {
		if a.agentBusy {
			frame := inlineSpinnerFrames[a.inlineAI.Spinner%len(inlineSpinnerFrames)]
			return a.t("AI %s 本机 %s 正在处理", frame, a.agent.Name())
		}
		if a.proposal == nil {
			return a.t("AI 回答已显示在原输入下方；继续输入即可返回 SSH")
		}
	}
	switch a.focus {
	case focusApproval:
		return a.t("命令确认  yes 执行 · no 拒绝 · Enter 确认")
	case focusAI:
		return a.t("AI  Enter 发送 · Shift+Enter/Ctrl+J 换行 · Ctrl+R 历史 · Esc 返回 SSH")
	case focusAIHistory:
		return a.t("历史会话  ↑/↓ 选择 · Enter 继续 · N 新建 · Esc 返回")
	case focusMonitor:
		return a.t("监控  Ctrl+T 切换 · Ctrl+L 布局 · Ctrl+N 新增 · Esc 返回 SSH")
	case focusMonitorSlot:
		return a.t("选择分屏  1–9 选择 · Esc 取消")
	case focusPanelMenu:
		return a.t("新增监控  1–6 选择 · Esc 取消")
	case focusCustomPanel:
		return a.t("%s  Enter 创建 · Ctrl+U 清空 · Esc 取消", a.menuStatus)
	default:
		if a.atPrompt && !a.sensitive {
			return a.mouseModeHint() + " · " + a.t("SSH  /ai AI 对话 · Ctrl+T 切换监控 · Ctrl+N 新增监控")
		}
		return "SSH"
	}
}
