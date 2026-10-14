package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/hinshun/vt10x"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
)

type terminalInputTestProvider struct{}

func (terminalInputTestProvider) Name() string     { return "test" }
func (terminalInputTestProvider) Configured() bool { return true }
func (terminalInputTestProvider) Stream(context.Context, agent.Request, agent.Callbacks) (agent.Turn, error) {
	return agent.Turn{}, nil
}
func (terminalInputTestProvider) Action(context.Context, agent.Request) (agent.Turn, error) {
	return agent.Turn{}, nil
}

func TestCtrlCClearsTrackedLineBeforeNextChineseInput(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	application.sshPTY = writer
	application.atPrompt = true

	application.handleTerminalInput([]byte("unfinished"))
	application.handleTerminalInput([]byte{0x03})
	application.handleTerminalInput([]byte("这"))

	if got := string(application.lineInput); got != "这" || !application.aiLineCapture {
		t.Fatalf("next Chinese input was not captured: input=%q capture=%v", got, application.aiLineCapture)
	}
	if application.lineCursor != len(application.lineInput) {
		t.Fatalf("line cursor=%d, want %d", application.lineCursor, len(application.lineInput))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(written), "unfinished\x03"; got != want {
		t.Fatalf("SSH input=%q, want %q", got, want)
	}
}

func TestCapturedChineseArrowEditingDoesNotLeakIntoSSH(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	application.sshPTY = writer
	application.agent = terminalInputTestProvider{}
	application.atPrompt = true

	application.handleTerminalInput([]byte("机器"))
	application.handleTerminalInput([]byte("\x1b[D"))
	application.handleTerminalInput([]byte("好"))
	application.handleTerminalInput([]byte{0x1b})
	application.handleTerminalInput([]byte("[C"))
	application.handleTerminalInput([]byte("吗"))

	if got, want := string(application.lineInput), "机好器吗"; got != want {
		t.Fatalf("edited captured line=%q, want %q", got, want)
	}
	application.handleTerminalInput([]byte{'\r'})
	if application.inlineAI == nil || application.inlineAI.Prompt != "机好器吗" {
		t.Fatalf("inline AI prompt=%#v", application.inlineAI)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("captured edit leaked into SSH: %q", written)
	}
}

func TestUTF8ChineseQuestionInSingleInputChunkStartsInlineAI(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	application.sshPTY = writer
	application.agent = terminalInputTestProvider{}
	application.atPrompt = true

	application.handleTerminalInput([]byte("帮我检查 nginx\r"))
	if application.inlineAI == nil || application.inlineAI.Prompt != "帮我检查 nginx" {
		t.Fatalf("inline AI prompt=%#v", application.inlineAI)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("Chinese AI question leaked into SSH: %q", written)
	}
}

func TestVisiblePromptStartsInlineAIWhenOutputPromptWasMissed(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	application.sshPTY = writer
	application.agent = terminalInputTestProvider{}
	application.mainCols = 80
	application.contentRows = 24
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("PS C:\\Users\\ops> "))

	application.handleTerminalInput([]byte("帮我检查 nginx\r"))
	if !application.atPrompt {
		t.Fatal("visible shell prompt was not detected")
	}
	if application.inlineAI == nil || application.inlineAI.Prompt != "帮我检查 nginx" {
		t.Fatalf("inline AI prompt=%#v", application.inlineAI)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("Chinese AI question leaked into SSH: %q", written)
	}
}

func TestVisiblePromptFallbackIsDisabledOnAlternateScreen(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	application.sshPTY = writer
	application.agent = terminalInputTestProvider{}
	application.mainCols = 80
	application.contentRows = 24
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("\x1b[?1049h编辑器> "))

	application.handleTerminalInput([]byte("中文\r"))
	if application.atPrompt || application.inlineAI != nil {
		t.Fatalf("alternate-screen input was treated as AI: atPrompt=%v inlineAI=%#v", application.atPrompt, application.inlineAI)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(written), "中文\r"; got != want {
		t.Fatalf("SSH input=%q, want %q", got, want)
	}
}

func TestAICommandOpensConversationAfterNodeSelection(t *testing.T) {
	for _, input := range []string{"/ai", "  /AI  ", "\x1b[200~/ai\x1b[201~"} {
		t.Run(input, func(t *testing.T) {
			application, err := New(Config{SSHArgs: []string{"sdp"}})
			if err != nil {
				t.Fatal(err)
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			application.sshPTY = writer
			application.agent = terminalInputTestProvider{}
			application.mainCols = 80
			application.contentRows = 24
			application.virtual = vt10x.New(vt10x.WithSize(80, 24))
			application.handleTerminalInput([]byte("1\r"))
			prompt := []byte("\r\nroot@node:~# ")
			_, _ = application.virtual.Write(prompt)
			application.handleOutput(prompt)

			application.handleInput([]byte(input))
			if application.focus != focusTerminal {
				t.Fatal("AI conversation opened before Enter")
			}
			application.handleInput([]byte{'\r'})
			if application.focus != focusAI || !application.aiExpanded || application.sidebarCollapsed {
				t.Fatalf("AI conversation did not open: focus=%v expanded=%v collapsed=%v", application.focus, application.aiExpanded, application.sidebarCollapsed)
			}
			if application.agentBusy || application.inlineAI != nil || len(application.lineInput) != 0 {
				t.Fatal("opening the conversation submitted an AI request or retained terminal input")
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			written, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			want := "1\r" + input + "\x15"
			if strings.HasPrefix(input, "\x1b[200~") {
				want = "1\r"
			}
			if string(written) != want {
				t.Fatalf("SSH input=%q, want %q", written, want)
			}
		})
	}
}

func TestGatewayAlternateScreenShellStartsAI(t *testing.T) {
	for _, outputFallback := range []bool{false, true} {
		t.Run(fmt.Sprint(outputFallback), func(t *testing.T) {
			a, err := New(Config{SSHArgs: []string{"sdp"}})
			if err != nil {
				t.Fatal(err)
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			a.sshPTY = writer
			a.agent = terminalInputTestProvider{}
			a.mainCols, a.contentRows = 100, 24
			a.virtual = vt10x.New(vt10x.WithSize(100, 24))
			a.processSSHOutput([]byte("\x1b[?1049h\x1b[2J\x1b[H请输入 : "))
			if a.atPrompt || a.safePromptScreen() {
				t.Fatal("gateway menu was treated as a shell")
			}
			a.handleInput([]byte("1\r"))
			prompt := []byte("\r\n[deployer@SZJD-YFQ-PM-OS01-BCONEST-BEAZ-OBSERVER-01 ~]$ ")
			if outputFallback {
				_, _ = a.virtual.Write(prompt)
			} else {
				for _, b := range prompt {
					a.processSSHOutput([]byte{b})
				}
			}
			a.handleInput([]byte("/ai "))
			// Remote echo must not revoke a recognised prompt while typing.
			a.processSSHOutput([]byte("/ai "))
			a.handleInput([]byte("当前top 10 cpu 进程\r"))
			if a.inlineAI == nil || a.inlineAI.Prompt != "当前top 10 cpu 进程" || !a.agentBusy {
				t.Fatalf("gateway shell did not start AI: prompt=%t inline=%#v", a.atPrompt, a.inlineAI)
			}
			// Ctrl+U erases the remotely echoed question without a newline or
			// a fresh prompt in the byte tail. The visible shell is still ready.
			a.processSSHOutput([]byte("\b \b\b \b\b \b\b \b"))
			if !a.atPrompt || !a.safePromptScreen() {
				t.Fatal("clearing the AI question lost the visible gateway shell prompt")
			}
			writer.Close()
			written, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if want := "1\r/ai 当前top 10 cpu 进程\x15"; string(written) != want {
				t.Fatalf("SSH input=%q, want %q", written, want)
			}
			// Logging out to the gateway must revoke the shell prompt again.
			a.processSSHOutput([]byte("\x1b[2J\x1b[H请输入 : "))
			if a.atPrompt || a.safePromptScreen() {
				t.Fatal("returning to the gateway retained shell prompt state")
			}
		})
	}
}

func TestCapturedLineWrapsAcrossTerminalRows(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 12
	application.contentRows = 4
	application.rows = 6
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("prompt> "))
	application.aiLineCapture = true
	application.lineInput = []byte("中文测试")
	application.lineCursor = len(application.lineInput)

	output, cursorRow, cursorCol := application.renderTerminalWorkspace()
	if !strings.Contains(output, positionedLine(2, 9, "中文", 4, "\x1b[38;5;213m")) {
		t.Fatalf("first captured row was not rendered: %q", output)
	}
	if !strings.Contains(output, positionedLine(3, 1, "测试", 12, "\x1b[38;5;213m")) {
		t.Fatalf("wrapped captured row was not rendered: %q", output)
	}
	if cursorRow != 3 || cursorCol != 5 {
		t.Fatalf("captured cursor=(%d,%d), want (3,5)", cursorRow, cursorCol)
	}
}

func TestCapturedLineWrapKeepsCursorVisibleAtTerminalBottom(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.mainCols = 10
	application.contentRows = 3
	application.rows = 5
	application.virtual = vt10x.New(vt10x.WithSize(application.mainCols, application.contentRows))
	_, _ = application.virtual.Write([]byte("old\r\nolder\r\n> "))
	application.aiLineCapture = true
	application.lineInput = []byte("一二三四五六七八九十")
	application.lineCursor = len(application.lineInput)

	output, cursorRow, cursorCol := application.renderTerminalWorkspace()
	if cursorRow != 4 || cursorCol != 3 {
		t.Fatalf("captured cursor=(%d,%d), want bottom row (4,3)", cursorRow, cursorCol)
	}
	if !strings.Contains(output, positionedLine(2, 3, "一二三四", 8, "\x1b[38;5;213m")) ||
		!strings.Contains(output, positionedLine(4, 1, "十", 10, "\x1b[38;5;213m")) {
		t.Fatalf("wrapped input did not stay visible after viewport shift: %q", output)
	}
}
