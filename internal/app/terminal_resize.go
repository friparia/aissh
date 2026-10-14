package app

import (
	"strings"

	"git.example.com/ai/wrd-ai-cli/internal/terminalview"
	"git.example.com/ai/wrd-ai-cli/internal/trigger"
)

type inlineResizeAnchor struct {
	rowOffset    int
	promptColumn bool
}

// ConPTY reflows the remote screen without the locally inserted AI rows.
// Keep all replies relative to the shell cursor, including archived replies
// and questions submitted while a resize repaint is still arriving.
func (a *App) captureInlineResizeAnchors() {
	if !a.terminalResizePending || a.virtual == nil {
		return
	}
	if !a.atPrompt && len(a.terminalResizeAnchors) == 0 {
		// A running command has no stable prompt to follow. Its output must
		// continue scrolling local replies normally after a window resize.
		return
	}
	if a.terminalResizeAnchors == nil {
		a.terminalResizeAnchors = make(map[*inlineAIState]inlineResizeAnchor)
	}
	cursor := a.virtual.Cursor()
	for _, state := range a.inlineBlocks() {
		if _, saved := a.terminalResizeAnchors[state]; saved {
			continue
		}
		a.terminalResizeAnchors[state] = inlineResizeAnchor{
			rowOffset:    state.AnchorY - cursor.Y,
			promptColumn: state.AnchorY == cursor.Y && state.AnchorX == cursor.X,
		}
	}
}

func (a *App) resolveInlineResizeAnchors() {
	if !a.terminalResizePending || a.virtual == nil || len(a.terminalResizeAnchors) == 0 {
		return
	}
	// Inspect the visible cursor row on every read: the final cursor restore
	// can arrive without text, and a repaint need not contain a clear command.
	// The output tail can still contain an old prompt while the cursor visits
	// other rows, so it cannot determine where the replies belong.
	a.virtual.Lock()
	cursor := a.virtual.Cursor()
	cols, rows := a.virtual.Size()
	if !a.safePromptScreenLocked() || cursor.Y < 0 || cursor.Y >= rows || cursor.X < 0 || cursor.X > cols {
		a.virtual.Unlock()
		return
	}
	var prefix strings.Builder
	for x := 0; x < cursor.X; x++ {
		char := a.virtual.Cell(x, cursor.Y).Char
		switch char {
		case terminalview.WidePlaceholder:
		case 0:
			prefix.WriteByte(' ')
		default:
			prefix.WriteRune(char)
		}
	}
	a.virtual.Unlock()
	if !trigger.LooksLikePrompt(prefix.String()) {
		return
	}
	for _, state := range a.inlineBlocks() {
		anchor, saved := a.terminalResizeAnchors[state]
		if !saved {
			continue
		}
		state.AnchorY = cursor.Y + anchor.rowOffset
		if anchor.promptColumn {
			state.AnchorX = cursor.X
		}
	}
	a.terminalRows = nil
}
