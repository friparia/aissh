//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"github.com/creack/pty"
)

// Exercise the real CLI event loop, PTY, monitor runner and desktop transport
// together, using a local shell and a deterministic AI endpoint (no real host).
func TestDesktopCLIStreamsChatAndMonitoringOutsideTerminal(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	const prompt = "native-question-not-a-shell-command"
	const answer = "native-answer-not-in-terminal"
	var mu sync.Mutex
	sent, acknowledged, sawPrompt, sawMonitor, sawAnswer := false, false, false, false, false
	sawApproval := false
	complete := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/agent/stream":
			var request agent.Request
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.Prompt != prompt {
				http.Error(w, "bad prompt", 400)
				return
			}
			sawPrompt = true
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: answer_delta\ndata: {\"content\":%q}\n\nevent: turn\ndata: {\"type\":\"finished\",\"session\":{\"id\":\"native-test\"},\"message\":%q}\n\n", answer, answer)
		case "/conversation":
			w.WriteHeader(http.StatusNoContent)
		case "/workbench":
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "denied", 403)
				return
			}
			var input credentialui.WorkbenchExchange
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				http.Error(w, "bad snapshot", 400)
				return
			}
			sawApproval = sawApproval || input.State.Proposal != nil
			for _, result := range input.Results {
				if result.ID == "test-prompt" && result.Error == "" {
					acknowledged = true
				}
			}
			for _, message := range input.State.Messages {
				if message.Role == "assistant" && message.Content == answer {
					sawAnswer = true
				}
			}
			for _, p := range input.State.Panels {
				if strings.Contains(p.Content, "HOST=native-test") {
					sawMonitor = true
				}
			}
			if sawPrompt && acknowledged && sawAnswer && sawMonitor {
				select {
				case complete <- struct{}{}:
				default:
				}
			}
			commands := []credentialui.WorkbenchCommand{}
			if !sent {
				sent = true
				commands = append(commands, credentialui.WorkbenchCommand{ID: "test-prompt", Kind: "prompt", Text: prompt, Deadline: time.Now().Add(5 * time.Second).UnixMilli()})
			}
			_ = json.NewEncoder(w).Encode(commands)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fixtureDir := t.TempDir()
	script := filepath.Join(fixtureDir, "ssh")
	// The built-in now collects through the interactive shell. Supply a stable
	// hostname there as well as in the standalone SSH-exec fixture below.
	if err := os.WriteFile(filepath.Join(fixtureDir, "hostname"), []byte("#!/bin/sh\nprintf 'native-test\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nfor arg do\n case \"$arg\" in *HOST=*) printf 'HOST=native-test\\nLOAD=0.1 0.2 0.3\\nMEM=1/4 MB (25%%)\\n'; exit 0;; esac\ndone\nPS1='ops@test:~$ '; export PS1\nexec /bin/sh -i\n"), 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDesktopCLIHelper$")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "AISSH_") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"), "AISSH_TEST_NATIVE_SSH="+script, "AISSH_TEST_NATIVE_AI="+server.URL+"/agent/stream", "AISSH_CONFIG_DIR="+t.TempDir(), credentialui.AddressEnv+"="+strings.TrimPrefix(server.URL, "http://"), credentialui.CapabilityEnv+"="+token)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 90})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	output := make(chan string, 1)
	go func() { data, _ := io.ReadAll(terminal); output <- string(data) }()
	select {
	case <-complete:
	case <-ctx.Done():
		_ = cmd.Wait()
		<-output
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("desktop roundtrip timed out: sent=%v ack=%v prompt=%v monitor=%v answer=%v", sent, acknowledged, sawPrompt, sawMonitor, sawAnswer)
	}
	_, _ = terminal.Write([]byte("exit\r"))
	if err := cmd.Wait(); err != nil {
		t.Fatalf("CLI exit: %v", err)
	}
	rendered := <-output
	if strings.Contains(rendered, prompt) || strings.Contains(rendered, answer) || strings.Contains(rendered, "/sidebar") {
		t.Fatal("native chat/sidebar rendered inside SSH terminal")
	}
	if strings.Contains(rendered, "HOST=native-test") || strings.Contains(rendered, "[监控") || strings.Contains(rendered, "__AISSH_MONITOR_") {
		t.Fatal("built-in monitor leaked collection output into SSH terminal")
	}
	mu.Lock()
	defer mu.Unlock()
	if sawApproval {
		t.Fatal("built-in monitor requested approval")
	}
}

func TestDesktopCLIHelper(t *testing.T) {
	script := os.Getenv("AISSH_TEST_NATIVE_SSH")
	if script == "" {
		return
	}
	os.Args = []string{"aissh", "--agent", "http", "--agent-url", os.Getenv("AISSH_TEST_NATIVE_AI"), "--no-history", "--no-workspace", "--system-collection", "--ssh-path", script, "server"}
	main()
	os.Exit(0)
}
