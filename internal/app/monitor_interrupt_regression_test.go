package app

import (
	"bytes"
	"io"
	"testing"
	"time"
)

type interruptCapturePTY struct{ bytes.Buffer }

func (*interruptCapturePTY) Read([]byte) (int, error) { return 0, io.EOF }
func (*interruptCapturePTY) Close() error             { return nil }

// No keyboard input is delivered in this test. A missing completion marker
// followed by a fresh shell prompt must never make maintenance send Ctrl+C
// or repeatedly inject another hidden command into the interactive session.
func TestMonitorTimeoutNeverInjectsInterruptAfterPromptReturns(t *testing.T) {
	a, err := New(Config{SSHArgs: []string{"server"}, SystemCollection: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.cancel()
	a.ensureSidebarVisible()
	terminal := &interruptCapturePTY{}
	a.sshPTY = terminal
	a.monitorViaPrimary = true
	a.atPrompt = true
	a.lineReliable = true
	approveMonitorFixture(t, a)
	now := time.Now()
	for tick := 0; tick < 5; tick++ {
		a.scheduleInBandMonitor(now.Add(time.Duration(tick+1) * time.Hour))
		if a.inBandMonitor != nil {
			a.inBandMonitor.startedAt = now.Add(-time.Hour)
		}
		a.checkInBandMonitorTimeout(now)
		a.handleOutput([]byte("\r\nroot@server:~# "))
	}
	if count := bytes.Count(terminal.Bytes(), []byte{0x03}); count != 0 {
		t.Fatalf("monitor generated %d Ctrl+C bytes without any keyboard input", count)
	}
	if commands := bytes.Count(terminal.Bytes(), []byte("(__aissh_monitor_marker=")); commands != 1 {
		t.Fatalf("timed-out monitor submitted %d hidden commands, want 1", commands)
	}
}
