package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hinshun/vt10x"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
)

type executionCaptureProvider struct {
	approvalTestProvider
	requests chan agent.Request
}

func TestExecutionMarkerSurvivesZmodemPassthrough(t *testing.T) {
	a, requests := executionTestApp(t)
	var screen bytes.Buffer
	a.screenOut = &screen
	a.processSSHOutput(testZmodemHandshake)
	if a.zmodem == nil {
		t.Fatal("transfer did not enter passthrough")
	}
	a.zmodem.clientStarted = true
	marker := executionMarkerPrefix("test") + "0\a"
	a.processSSHOutput(append(append([]byte(nil), zmodemFinishMarker...), []byte(marker[:5])...))
	a.processSSHOutput([]byte(marker[5:] + "root@server:~# "))
	if a.zmodem != nil || a.execution != nil || !a.atPrompt {
		t.Fatalf("completion did not restore shell: zmodem=%v execution=%v prompt=%v", a.zmodem, a.execution, a.atPrompt)
	}
	if !bytes.Contains(screen.Bytes(), zmodemFinishMarker) || strings.Contains(screen.String(), "aissh-test") {
		t.Fatalf("binary protocol or completion marker was corrupted: %q", screen.String())
	}
	select {
	case request := <-requests:
		if request.Completion != "exit_status=0" || request.Output != "" {
			t.Fatalf("binary output leaked into result: %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("transfer completion was not submitted")
	}
}

func (p executionCaptureProvider) Stream(_ context.Context, request agent.Request, _ agent.Callbacks) (agent.Turn, error) {
	p.requests <- request
	return agent.Turn{Type: "finished"}, nil
}

func executionTestApp(t *testing.T) (*App, chan agent.Request) {
	t.Helper()
	a, err := New(Config{SSHArgs: []string{"server"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.cancel)
	a.mainCols = 80
	a.contentRows = 24
	a.rows = 26
	a.virtual = vt10x.New(vt10x.WithSize(80, 24))
	requests := make(chan agent.Request, 4)
	a.agent = executionCaptureProvider{requests: requests}
	a.execution = &executionState{SessionID: "s1", ProposalID: "p1", Command: "deploy", Marker: "test", Output: newContextBuffer(a.config.AIContextChars)}
	return a, requests
}

func TestGatewayTextMarkerSurvivesSplitReadsAndRejectsEcho(t *testing.T) {
	marker := "9a27842c5da7404caf89607667293212"
	for _, ending := range []string{"\n", "\r\n"} {
		data := "RESULT\r\n" + executionTextMarkerPrefix(marker) + "7" + ending + "[root@node ~]# "
		for split := 0; split <= len(data); split++ {
			state := &executionState{Marker: marker, TextMarker: true}
			var output, after []byte
			var code *int
			for _, chunk := range []string{data[:split], data[split:]} {
				if code != nil {
					after = append(after, chunk...)
					continue
				}
				before, rest, status := state.consume([]byte(chunk))
				output = append(output, before...)
				after = append(after, rest...)
				code = status
			}
			if code == nil || *code != 7 || string(output) != "RESULT\r\n" || string(after) != "[root@node ~]# " {
				t.Fatalf("split=%d ending=%q output=%q after=%q status=%v", split, ending, output, after, code)
			}
		}
	}
	state := &executionState{Marker: marker, TextMarker: true}
	echo := isolateGatewayApprovedCommand("printf OK", marker)
	if _, _, code := state.consume([]byte(echo)); code != nil {
		t.Fatal("gateway command echo impersonated completion")
	}
	for _, suffix := range []string{"999\n", "x\n", strings.Repeat("1", 1000), "7\a"} {
		state := &executionState{Marker: marker, TextMarker: true}
		if _, _, code := state.consume([]byte(executionTextMarkerPrefix(marker) + suffix)); code != nil || len(state.Pending) > len(executionTextMarkerPrefix(marker))+4 {
			t.Fatalf("malformed marker accepted or retained: %q", suffix)
		}
	}
}

func TestExecutionWaitsForMarkerAcrossPTYReads(t *testing.T) {
	marker := executionMarkerPrefix("test") + "7\a"
	for split := 0; split <= len(marker); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			a, requests := executionTestApp(t)
			for _, chunk := range []string{"(\r\n> ", "command echo\r\n> ", "looks like a prompt # ", "\r\n", "checksum: OK\r\n", "FINAL RESULT\r\n"} {
				a.processSSHOutput([]byte(chunk))
				if a.execution == nil || a.agentBusy || a.atPrompt {
					t.Fatalf("completed on ordinary output %q", chunk)
				}
			}
			a.processSSHOutput([]byte(marker[:split]))
			if split < len(marker) && (a.execution == nil || a.agentBusy) {
				t.Fatal("completed on partial marker")
			}
			a.processSSHOutput([]byte(marker[split:] + "root@server:~# "))
			select {
			case request := <-requests:
				if request.Event != "result" || request.Completion != "exit_status=7" || request.ProposalID != "p1" {
					t.Fatalf("result=%#v", request)
				}
				for _, want := range []string{"checksum: OK", "FINAL RESULT"} {
					if !strings.Contains(request.Output, want) {
						t.Fatalf("missing %q in %q", want, request.Output)
					}
				}
				if strings.Contains(request.Output, "aissh-") || strings.Contains(request.Output, "root@server") {
					t.Fatalf("completion metadata or next prompt leaked into result: %q", request.Output)
				}
			case <-time.After(time.Second):
				t.Fatal("completed result was not submitted")
			}
			if a.execution != nil || !a.atPrompt || strings.Contains(a.recentOutput.ContextString(), "aissh-") {
				t.Fatalf("execution=%#v prompt=%v context=%q", a.execution, a.atPrompt, a.recentOutput.ContextString())
			}
		})
	}
}

func TestExecutionMarkerDoesNotTrustEchoOrMalformedMarkers(t *testing.T) {
	a, requests := executionTestApp(t)
	echo := isolateApprovedCommand("printf 'OK'", "test")
	a.processSSHOutput([]byte(echo))
	for _, value := range []string{executionMarkerPrefix("other") + "0\a", executionMarkerPrefix("test") + "999\a", executionMarkerPrefix("test") + "x\a", executionMarkerPrefix("test") + strings.Repeat("1", 1000)} {
		a.processSSHOutput([]byte(value))
		if a.execution == nil || a.agentBusy || len(a.execution.Pending) > len(executionMarkerPrefix("test"))+3 {
			t.Fatal("invalid marker completed execution or retained unbounded data")
		}
	}
	// Single-byte reads also exercise split OSC escape sequences.
	for _, b := range []byte("OK\r\n" + executionMarkerPrefix("test") + "0\a") {
		a.processSSHOutput([]byte{b})
	}
	select {
	case request := <-requests:
		if request.Completion != "exit_status=0" || !strings.Contains(request.Output, "OK") {
			t.Fatalf("result=%#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("valid completion after malformed markers was lost")
	}
	if a.atPrompt {
		t.Fatal("completion marker alone must not make the shell ready for input")
	}
}

func TestCompletedExecutionWaitsForBusyAgentWithoutCollectingNextCommand(t *testing.T) {
	a, requests := executionTestApp(t)
	a.agentBusy = true
	a.processSSHOutput([]byte("FINAL\r\n" + executionMarkerPrefix("test") + "0\aroot@server:~# "))
	a.processSSHOutput([]byte("unrelated later output\r\n"))
	if a.execution == nil || a.execution.ExitCode == nil {
		t.Fatal("busy agent discarded completed execution")
	}
	a.agentBusy = false
	a.submitExecutionResult()
	a.submitExecutionResult()
	select {
	case request := <-requests:
		if request.Output != "FINAL" {
			t.Fatalf("result=%q", request.Output)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred result was not submitted")
	}
	if a.execution != nil {
		t.Fatal("execution was not consumed exactly once")
	}
}
