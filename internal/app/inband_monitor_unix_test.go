//go:build !windows

package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

func TestRepeatedApprovedMonitorsRunInInteractiveTerminal(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := New(Config{SSHArgs: []string{"server"}, SystemCollection: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.cancel()
	a.setViewportSize(140, 30)
	a.ensureSidebarVisible()
	a.virtual = vt10x.New(vt10x.WithSize(a.mainCols, a.contentRows))
	a.monitorViaPrimary = true
	a.panels.ByID("system").Command = "printf 'HOST=server\\n'"
	cmd := exec.CommandContext(ctx, shell, "--noprofile", "--norc", "-i")
	cmd.Env = append(os.Environ(), "PS1=root@server:~# ", "PS2=> ", "PROMPT_COMMAND=", "HISTFILE=/dev/null", "TERM=xterm")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(a.mainCols), Rows: uint16(a.contentRows)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = terminal.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	a.sshPTY = terminal
	chunks := make(chan []byte, 32)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 4096)
		for {
			count, err := terminal.Read(buffer)
			if count > 0 {
				select {
				case chunks <- append([]byte(nil), buffer[:count]...):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	read := func() {
		t.Helper()
		select {
		case data, ok := <-chunks:
			if !ok {
				t.Fatal("interactive shell exited early")
			}
			a.processSSHOutput(data)
		case <-ctx.Done():
			t.Fatalf("shell timed out: monitor=%v awaitingPrompt=%v", a.inBandMonitor != nil, a.monitorAwaitingPrompt)
		}
	}
	for !a.atPrompt {
		read()
	}
	var transcript bytes.Buffer
	a.config.DesktopUI = true
	a.screenOut = &transcript
	a.scheduleMonitors()
	if a.inBandMonitor != nil || a.monitorReview == nil {
		t.Fatal("real shell was used before review")
	}
	a.approveProposal()
	before := monitorScreenText(a)
	var firstScreen string
	for tick := 0; tick < 20; tick++ {
		a.scheduleInBandMonitor(time.Now().Add(time.Duration(tick+1) * time.Minute))
		if a.inBandMonitor == nil {
			t.Fatalf("monitor did not start at tick %d", tick)
		}
		for a.inBandMonitor != nil || a.monitorAwaitingPrompt {
			read()
		}
		if screen := monitorScreenText(a); screen == before || !strings.Contains(screen, "HOST=server") || !strings.Contains(screen, "退出码：0") {
			t.Fatalf("tick %d missing visible command/result: %s", tick, screen)
		}
		if tick == 0 {
			firstScreen = monitorScreenText(a)
			t.Logf("Visible output from the real Shell/PTY:\n%s", transcript.String())
		} else if monitorScreenText(a) != firstScreen {
			t.Fatalf("recurring monitor changed the terminal at tick %d", tick)
		}
	}
	if strings.Count(transcript.String(), "[监控完成 · 退出码：0]") != 1 || a.panels.ByID("system").RefreshCount != 20 {
		t.Fatal("monitor did not collect silently after the first visible execution")
	}
	if target := os.Getenv("AISSH_MONITOR_AUDIT_TRANSCRIPT"); target != "" {
		if err := os.WriteFile(target, transcript.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
