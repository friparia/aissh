package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hinshun/vt10x"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/monitor"
)

func TestInBandMonitorCommandKeepsMarkersOutOfShellEcho(t *testing.T) {
	marker := "__AISSH_MONITOR_7_"
	command := inBandMonitorCommand(marker, "printf '%s' \"it's safe\"")
	if strings.Contains(command, marker+"BEGIN__") || strings.Contains(command, marker+"END__:") {
		t.Fatalf("complete monitor marker leaked into echoed command: %q", command)
	}
	if !strings.HasSuffix(command, "\r") {
		t.Fatalf("monitor command does not submit the shell line: %q", command)
	}
}

func TestInBandMonitorStateCollectsChunkedOutputAndRestoresPrompt(t *testing.T) {
	state := &inBandMonitorState{marker: "__AISSH_MONITOR_9_"}
	chunks := [][]byte{
		[]byte("echoed command containing __AISSH_MONITOR_9_ but no complete marker\r\n__AISSH_MON"),
		[]byte("ITOR_9_BEGIN__\r\nHOST=server\r\nUPTIME=1 day\r\n"),
		[]byte("\r\n__AISSH_MONITOR_9_END__:0_"),
		[]byte("_\r\nuser@server$ "),
	}
	for index, chunk := range chunks {
		visible, complete, content, err := state.consume(chunk)
		if index < len(chunks)-1 {
			if complete || len(visible) != 0 || content != "" || err != nil {
				t.Fatalf("chunk %d completed early: visible=%q complete=%v content=%q err=%v", index, visible, complete, content, err)
			}
			continue
		}
		if err != nil || !complete {
			t.Fatalf("final chunk complete=%v err=%v", complete, err)
		}
		if got, want := content, "HOST=server\r\nUPTIME=1 day\r\n"; got != want {
			t.Fatalf("content=%q want=%q", got, want)
		}
		if got, want := string(visible), "\x1b[2K\r\r\nuser@server$ "; got != want {
			t.Fatalf("visible=%q want=%q", got, want)
		}
	}
}

func TestInBandMonitorStateReportsRemoteFailure(t *testing.T) {
	state := &inBandMonitorState{marker: "__AISSH_MONITOR_11_"}
	data := []byte("__AISSH_MONITOR_11_BEGIN__\r\npermission denied\r\n\r\n__AISSH_MONITOR_11_END__:1__\r\nhost$ ")
	visible, complete, content, err := state.consume(data)
	if !complete || err == nil || content != "" {
		t.Fatalf("complete=%v content=%q err=%v", complete, content, err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error=%q", err)
	}
	if !bytes.HasPrefix(visible, inBandMonitorScreenRestore) {
		t.Fatalf("prompt restore missing: %q", visible)
	}
}

func TestScheduleInBandMonitorUsesPrimaryPTY(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}, SystemCollection: true})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	application.sshPTY = writer
	application.monitorViaPrimary = true
	application.ensureSidebarVisible()
	application.atPrompt = true
	application.lineReliable = true

	approveMonitorFixture(t, application)
	application.scheduleMonitors()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if application.inBandMonitor == nil || !application.monitorActive["system"] || application.atPrompt {
		t.Fatalf("state=%#v active=%v atPrompt=%v", application.inBandMonitor, application.monitorActive["system"], application.atPrompt)
	}
	if !bytes.Contains(written, []byte("sh -c")) || !bytes.HasSuffix(written, []byte{'\r'}) {
		t.Fatalf("primary PTY command=%q", written)
	}
}

func TestInBandMonitorDoesNotStartWhileAIRuns(t *testing.T) {
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
	application.monitorViaPrimary = true
	application.ensureSidebarVisible()
	application.atPrompt = true
	application.lineReliable = true
	application.agentBusy = true

	application.scheduleMonitors()
	if application.inBandMonitor != nil || application.monitorActive["system"] {
		t.Fatalf("monitor started while AI was running: state=%#v active=%v", application.inBandMonitor, application.monitorActive["system"])
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("monitor wrote to primary SSH while AI was running: %q", written)
	}
}

func TestInputWaitsForActiveInBandMonitor(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.inBandMonitor = &inBandMonitorState{startedAt: time.Now()}
	application.handleInput([]byte("whoami\r"))
	if got, want := string(application.inBandMonitor.pendingInput), "whoami\r"; got != want {
		t.Fatalf("pending input=%q want=%q", got, want)
	}
}

func TestInBandMonitorDoesNotBlockLocalEditor(t *testing.T) {
	for _, timedOut := range []bool{false, true} {
		t.Run(fmt.Sprint(timedOut), func(t *testing.T) {
			a, terminal := newMonitorTestApp(t)
			a.scheduleMonitors()
			terminal.Reset()
			if timedOut {
				a.checkInBandMonitorTimeout(time.Now().Add(time.Hour))
			}
			a.focus = focusAI
			a.handleInput([]byte("检查服务器"))
			a.handleInput([]byte("\x1b[D\x7f"))
			if got := string(a.editor); got != "检查服器" {
				t.Fatalf("local editor blocked by monitoring: %q", got)
			}
			if terminal.Len() != 0 || len(a.monitorDeferredInput) != 0 {
				t.Fatal("local editing leaked into remote input")
			}
		})
	}
}

func TestInBandMonitorAllowsMouseFocusAndKeepsQueuedInputInTerminal(t *testing.T) {
	a, terminal := newMonitorTestApp(t)
	a.scheduleMonitors()
	marker := a.inBandMonitor.marker
	terminal.Reset()
	a.handleInput([]byte("date\r"))
	top, left, _, height := a.aiRegion()
	a.handleInput([]byte(fmt.Sprintf("\x1b[<0;%d;%dM", left+3, top+height-1)))
	a.handleInput([]byte("正在编辑"))
	if a.focus != focusAI || string(a.editor) != "正在编辑" {
		t.Fatal("monitor blocked clicking or typing in the AI editor")
	}
	a.processSSHOutput([]byte(marker + "BEGIN__\r\nHOST=server\r\n" + marker + "END__:0__\r\nroot@server:~# "))
	if terminal.String() != "date\r" || string(a.editor) != "正在编辑" || a.focus != focusAI {
		t.Fatalf("queued terminal input changed destinations: remote=%q editor=%q focus=%v", terminal.String(), string(a.editor), a.focus)
	}
}

func TestInBandMonitorTimeoutDoesNotInterruptOrRetryAndPreservesInput(t *testing.T) {
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
	application.monitorViaPrimary = true
	application.ensureSidebarVisible()
	application.monitor.Timeout = time.Second
	application.monitorActive["system"] = true
	application.inBandMonitor = &inBandMonitorState{
		panelID:      "system",
		startedAt:    time.Now().Add(-2 * time.Second),
		pendingInput: []byte("date\r"),
	}

	application.checkInBandMonitorTimeout(time.Now())
	if got, want := string(application.monitorDeferredInput), "date\r"; got != want {
		t.Fatalf("deferred input=%q want=%q", got, want)
	}
	// Even after the prompt returns, maintenance ticks must not restart a
	// broken query and send another interrupt every timeout interval.
	application.monitorDeferredInput = nil
	for tick := 0; tick < 3; tick++ {
		application.handleOutput([]byte("\r\nroot@server:~# "))
		later := time.Now().Add(time.Duration(tick+1) * time.Hour)
		application.scheduleInBandMonitor(later)
		application.checkInBandMonitorTimeout(later.Add(time.Minute))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Fatalf("timed-out monitor wrote to the interactive shell: %q", written)
	}
	if application.inBandMonitor != nil || application.monitorActive["system"] {
		t.Fatalf("monitor was not released: state=%#v active=%v", application.inBandMonitor, application.monitorActive["system"])
	}
}

func TestInBandMonitorCompletionDoesNotDependOnTerminalEraseSequence(t *testing.T) {
	marker := "__AISSH_MONITOR_12_"
	response := marker + "BEGIN__\r\nHOST=server\r\n" + marker + "END__:0__"
	// ConPTY can replace the remote newline/erase sequence with cursor moves.
	prompt := "\x1b[12;1Hroot@server:~# "
	for split := 0; split < len(response); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			state := &inBandMonitorState{marker: marker}
			if _, complete, _, _ := state.consume([]byte(response[:split])); complete {
				t.Fatal("partial response completed early")
			}
			visible, complete, content, err := state.consume([]byte(response[split:] + prompt))
			if !complete || err != nil || content != "HOST=server" || string(visible) != string(inBandMonitorScreenRestore)+prompt {
				t.Fatalf("complete=%v content=%q visible=%q err=%v", complete, content, visible, err)
			}
		})
	}
}

func TestInBandMonitorParsesConPTYFormattedMarkers(t *testing.T) {
	marker := "__AISSH_MONITOR_22_"
	for _, separator := range []string{"\x1b[0m", "\x1b[K", "\r\n", "\x1b[12;1H"} {
		for _, chunkSize := range []int{1, 7, 4096} {
			t.Run(fmt.Sprintf("separator=%q/chunk=%d", separator, chunkSize), func(t *testing.T) {
				decorate := func(text string) string { return strings.Join(strings.Split(text, ""), separator) }
				// The OSC title contains decoy marker text and must be ignored.
				data := []byte("\x1b]0;" + marker + "BEGIN__ decoy " + marker + "END__:1__\a" +
					decorate(marker+"BEGIN__") + "\x1b[13;1HHOST=server" +
					"\x1b[14;1HLOAD=0.1 0.2 0.3\x1b[15;1H" + decorate(marker+"END__:0__"))
				state := &inBandMonitorState{marker: marker}
				for offset := 0; offset < len(data); offset += chunkSize {
					end := min(offset+chunkSize, len(data))
					_, complete, content, err := state.consume(data[offset:end])
					if end < len(data) {
						if complete {
							t.Fatalf("completed before real marker: content=%q err=%v", content, err)
						}
					} else if !complete || err != nil || content != "HOST=server\nLOAD=0.1 0.2 0.3" {
						t.Fatalf("complete=%v content=%q err=%v", complete, content, err)
					}
				}
			})
		}
	}
}

func TestInBandMonitorWrapperEmitsPersistentPrintableCompletion(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	marker := "__AISSH_MONITOR_13_"
	command := inBandMonitorCommand(marker, "printf 'HOST=server\\n'")
	output, err := exec.Command(shell, "-c", strings.TrimSuffix(command, "\r")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output, []byte(marker+"END__:0__\n")) || bytes.Contains(output, []byte("\x1b[2K")) {
		t.Fatalf("completion can be erased before ConPTY delivers it: %q", output)
	}
	state := &inBandMonitorState{marker: marker}
	_, complete, content, err := state.consume(output)
	if !complete || err != nil || content != "HOST=server\n" {
		t.Fatalf("complete=%v content=%q err=%v", complete, content, err)
	}
}

type monitorTestPTY struct{ bytes.Buffer }

func (*monitorTestPTY) Read([]byte) (int, error) { return 0, io.EOF }
func (*monitorTestPTY) Close() error             { return nil }

func newMonitorTestApp(t *testing.T) (*App, *monitorTestPTY) {
	t.Helper()
	a, err := New(Config{SSHArgs: []string{"server"}, SystemCollection: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.cancel)
	a.setViewportSize(140, 30)
	a.ensureSidebarVisible()
	a.virtual = vt10x.New(vt10x.WithSize(a.mainCols, a.contentRows))
	a.processSSHOutput([]byte("root@server:~# "))
	terminal := &monitorTestPTY{}
	a.sshPTY = terminal
	a.monitorViaPrimary = true
	a.panels.ByID("system").Command = "printf 'test sample\\n'"
	approveMonitorFixture(t, a)
	return a, terminal
}

func TestInBandMonitorDoesNotUseStalePromptToReplayInput(t *testing.T) {
	a, terminal := newMonitorTestApp(t)
	a.scheduleMonitors()
	terminal.Reset()
	a.handleInput([]byte("date\r"))
	a.checkInBandMonitorTimeout(time.Now().Add(time.Hour))
	a.refreshPromptFromScreen()
	a.flushDeferredMonitorInput()
	if a.atPrompt || terminal.Len() != 0 {
		t.Fatal("timeout trusted the old visible prompt or wrote queued input early")
	}
	a.handleInput([]byte("whoami\r"))
	if terminal.Len() != 0 {
		t.Fatal("new input bypassed the queued input while waiting for the shell")
	}
	a.processSSHOutput([]byte("\r\nroot@server:~# "))
	if got := terminal.String(); got != "date\rwhoami\r" {
		t.Fatalf("queued input not restored in order: %q", got)
	}
}

func TestSuccessfulInBandMonitorWaitsForFreshPromptAndCanRunAgain(t *testing.T) {
	a, terminal := newMonitorTestApp(t)
	a.scheduleMonitors()
	marker := a.inBandMonitor.marker
	terminal.Reset()
	a.processSSHOutput([]byte(marker + "BEGIN__\r\nHOST=server\r\n" + marker + "END__:0__"))
	if a.inBandMonitor != nil || a.monitorActive["system"] || a.atPrompt {
		t.Fatal("monitor did not finish independently of the next shell prompt")
	}
	a.refreshPromptFromScreen()
	a.handleInput([]byte("date\r"))
	if a.atPrompt || terminal.Len() != 0 {
		t.Fatal("monitor reused a stale prompt before the new one arrived")
	}
	a.processSSHOutput([]byte("\x1b[12;1Hroot@server:~# "))
	if terminal.String() != "date\r" {
		t.Fatalf("successful query did not restore input: %q", terminal.String())
	}
	if content := a.panels.ByID("system").Content; !strings.Contains(content, "server") {
		t.Fatalf("monitor result missing: %q", content)
	}
	a.processSSHOutput([]byte("\r\nroot@server:~# "))
	a.scheduleInBandMonitor(time.Now().Add(time.Minute))
	if a.inBandMonitor == nil || bytes.Contains(terminal.Bytes(), []byte{0x03}) {
		t.Fatal("healthy monitoring did not resume normally")
	}
}

func TestUserCanInterruptInBandMonitorWithoutAutomaticRetries(t *testing.T) {
	a, terminal := newMonitorTestApp(t)
	a.scheduleMonitors()
	terminal.Reset()
	a.handleInput([]byte("unfinished"))
	a.handleInput([]byte{0x03})
	if terminal.String() != "\x03" || a.inBandMonitor != nil {
		t.Fatalf("user Ctrl+C was not forwarded once: %q", terminal.String())
	}
	a.processSSHOutput([]byte("\r\nroot@server:~# "))
	a.scheduleMonitors()
	a.checkInBandMonitorTimeout(time.Now().Add(time.Hour))
	if terminal.String() != "\x03" {
		t.Fatalf("monitor restarted or replayed cancelled input: %q", terminal.String())
	}
}

func TestInBandMonitorDoesNotStartInHiddenOrBusyWorkspace(t *testing.T) {
	for _, mode := range []string{"hidden", "execution", "approval"} {
		t.Run(mode, func(t *testing.T) {
			a, terminal := newMonitorTestApp(t)
			switch mode {
			case "hidden":
				a.toggleSidebar()
			case "execution":
				a.execution = &executionState{Command: "sleep 60"}
			case "approval":
				a.proposal = &agent.Proposal{Command: "uptime"}
			}
			a.scheduleMonitors()
			if terminal.Len() != 0 || a.inBandMonitor != nil {
				t.Fatalf("monitor took over the primary session in %s mode", mode)
			}
		})
	}
}

func TestMonitorAuthenticationFailureFallsBackToPrimarySession(t *testing.T) {
	application, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	application.monitorPrimaryFallback = true
	application.monitorActive["system"] = true
	application.monitorLast["system"] = time.Now()

	application.applyMonitorResult(monitorResultEvent{
		panelID: "system",
		err: &monitor.RunError{
			Message:  "user@server: Permission denied (publickey,password).",
			ExitCode: 255,
		},
	})

	if !application.monitorViaPrimary {
		t.Fatal("monitor did not switch to the authenticated primary session")
	}
	if application.monitorActive["system"] {
		t.Fatal("failed background monitor remained active")
	}
	if !application.monitorLast["system"].IsZero() {
		t.Fatalf("monitor retry was delayed: last=%v", application.monitorLast["system"])
	}
	if got := application.panels.ByID("system").LastError; got != "" {
		t.Fatalf("transient authentication error was shown: %q", got)
	}
}

func TestMonitorTimeoutFallbackDoesNotRetrySlowCommands(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			a, terminal := newMonitorTestApp(t)
			a.monitorViaPrimary = false
			a.monitorPrimaryFallback = true
			a.applyMonitorResult(monitorResultEvent{panelID: "system", err: &monitor.RunError{
				Message: "monitor query timed out", CommandStarted: started, ConnectionTimeout: !started,
			}})
			if a.monitorViaPrimary == started {
				t.Fatalf("command_started=%v fallback=%v", started, a.monitorViaPrimary)
			}
			if !started {
				a.scheduleMonitors()
				if a.inBandMonitor == nil || terminal.Len() == 0 {
					t.Fatal("connection timeout did not retry through the authenticated session")
				}
			} else if a.panels.ByID("system").LastError == "" {
				t.Fatal("slow command error was hidden")
			}
		})
	}
}

func TestInBandMonitorWaitsUntilTerminalInputIsIdle(t *testing.T) {
	a, terminal := newMonitorTestApp(t)
	a.handleInput([]byte("date\r"))
	a.processSSHOutput([]byte("\r\nroot@server:~# "))
	terminal.Reset()
	a.scheduleInBandMonitor(a.terminalInputAt.Add(100 * time.Millisecond))
	if a.inBandMonitor != nil || terminal.Len() != 0 {
		t.Fatal("monitor interrupted the user's next keystroke immediately after a command")
	}
	a.scheduleInBandMonitor(a.terminalInputAt.Add(time.Second))
	if a.inBandMonitor == nil {
		t.Fatal("monitor did not resume after input became idle")
	}
}

// Transport tests can use either a built-in or an explicitly reviewed script.
func approveMonitorFixture(t *testing.T, a *App) {
	t.Helper()
	if a.ensureMonitorAudit(a.panelMonitorAudit(a.panels.ByID("system"))) {
		return
	}
	if a.monitorReview == nil {
		t.Fatal("fixture did not request audit")
	}
	a.approveProposal()
	if a.proposal != nil {
		t.Fatalf("fixture approval failed: %s", a.approvalStatus)
	}
}
