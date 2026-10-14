package main

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestParseArgsPreservesSSHOptions(t *testing.T) {
	config, help, version, err := parseArgs([]string{
		"--agent", "http",
		"--agent-url", "http://agent/stream",
		"-p", "2222", "-J", "jump", "root@server",
	})
	if err != nil {
		t.Fatal(err)
	}
	if help || version {
		t.Fatal("unexpected help/version")
	}
	if config.AgentURL != "http://agent/stream" {
		t.Fatalf("agent URL = %q", config.AgentURL)
	}
	if config.AgentProvider != "http" {
		t.Fatalf("agent provider = %q", config.AgentProvider)
	}
	want := []string{"-p", "2222", "-J", "jump", "root@server"}
	if !reflect.DeepEqual(config.SSHArgs, want) {
		t.Fatalf("ssh args = %#v", config.SSHArgs)
	}
}

func TestParseArgsDefaultsToLocalCodex(t *testing.T) {
	t.Setenv("AISSH_AGENT_PROVIDER", "")
	t.Setenv("AISSH_AGENT", "")
	t.Setenv("AISSH_AGENT_URL", "")
	config, help, version, err := parseArgs([]string{"server"})
	if err != nil || help || version {
		t.Fatalf("help=%v version=%v err=%v", help, version, err)
	}
	if config.AgentProvider != "codex" || config.CodexPath != "codex" {
		t.Fatalf("provider=%q codex=%q", config.AgentProvider, config.CodexPath)
	}
}

func TestParseArgsSupportsClaudeProvider(t *testing.T) {
	t.Setenv("AISSH_CLAUDE_PATH", "")
	t.Setenv("AISSH_CLAUDE_MODEL", "")
	config, help, version, err := parseArgs([]string{
		"--agent", "claude",
		"--claude-path", "/opt/bin/claude",
		"--claude-model=opus",
		"server",
	})
	if err != nil || help || version {
		t.Fatalf("help=%v version=%v err=%v", help, version, err)
	}
	if config.AgentProvider != "claude" || config.ClaudePath != "/opt/bin/claude" || config.ClaudeModel != "opus" {
		t.Fatalf("provider=%q path=%q model=%q", config.AgentProvider, config.ClaudePath, config.ClaudeModel)
	}
	if !reflect.DeepEqual(config.SSHArgs, []string{"server"}) {
		t.Fatalf("ssh args = %#v", config.SSHArgs)
	}
}

func TestParseArgsSupportsLanguage(t *testing.T) {
	config, help, version, err := parseArgs([]string{"--lang", "en-US", "server"})
	if err != nil || help || version {
		t.Fatalf("help=%v version=%v err=%v", help, version, err)
	}
	if config.Language != "en" {
		t.Fatalf("language=%q", config.Language)
	}
}

func TestParseArgsSupportsHistoryConfiguration(t *testing.T) {
	t.Setenv("AISSH_HISTORY", "")
	t.Setenv("AISSH_HISTORY_DIR", "")
	config, help, version, err := parseArgs([]string{"--history-dir", "/tmp/aissh-history-test", "server"})
	if err != nil || help || version || config.HistoryDir != "/tmp/aissh-history-test" {
		t.Fatalf("history=%q help=%v version=%v err=%v", config.HistoryDir, help, version, err)
	}
	config, _, _, err = parseArgs([]string{"--no-history", "server"})
	if err != nil || config.HistoryDir != "" {
		t.Fatalf("disabled history=%q err=%v", config.HistoryDir, err)
	}
}

func TestParseArgsConversation(t *testing.T) {
	for _, args := range [][]string{{"--conversation", "saved-id", "server"}, {"--conversation=saved-id", "server"}} {
		config, _, _, err := parseArgs(args)
		if err != nil || config.ConversationID != "saved-id" || !reflect.DeepEqual(config.SSHArgs, []string{"server"}) {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	}
	for _, args := range [][]string{{"--conversation"}, {"--conversation=", "server"}, {"--conversation", "", "server"}} {
		if _, _, _, err := parseArgs(args); err == nil {
			t.Fatalf("accepted empty conversation: %q", args)
		}
	}
}

func TestParseArgsSupportsLocalWorkspace(t *testing.T) {
	config, help, version, err := parseArgs([]string{"--workspace", "/tmp/project", "server"})
	if err != nil || help || version || config.Workspace != "/tmp/project" {
		t.Fatalf("workspace=%q help=%v version=%v err=%v", config.Workspace, help, version, err)
	}
}

func TestParseArgsSupportsWritableLocalWorkspace(t *testing.T) {
	t.Setenv("AISSH_WORKSPACE_WRITE", "")
	config, help, version, err := parseArgs([]string{"--workspace-write", "server"})
	if err != nil || help || version || !config.WorkspaceWritable {
		t.Fatalf("writable=%v help=%v version=%v err=%v", config.WorkspaceWritable, help, version, err)
	}

	t.Setenv("AISSH_WORKSPACE_WRITE", "1")
	config, _, _, err = parseArgs([]string{"server"})
	if err != nil || !config.WorkspaceWritable {
		t.Fatalf("env writable=%v err=%v", config.WorkspaceWritable, err)
	}
	config, _, _, err = parseArgs([]string{"--no-workspace-write", "server"})
	if err != nil || config.WorkspaceWritable {
		t.Fatalf("disabled writable=%v err=%v", config.WorkspaceWritable, err)
	}
}

func TestParseArgsSupportsAlwaysYes(t *testing.T) {
	t.Setenv("AISSH_ALWAYS_YES", "")
	config, help, version, err := parseArgs([]string{"--always-yes", "server"})
	if err != nil || help || version || !config.AlwaysYes {
		t.Fatalf("alwaysYes=%v help=%v version=%v err=%v", config.AlwaysYes, help, version, err)
	}

	t.Setenv("AISSH_ALWAYS_YES", "1")
	config, _, _, err = parseArgs([]string{"server"})
	if err != nil || !config.AlwaysYes {
		t.Fatalf("env alwaysYes=%v err=%v", config.AlwaysYes, err)
	}
	config, _, _, err = parseArgs([]string{"--no-always-yes", "server"})
	if err != nil || config.AlwaysYes {
		t.Fatalf("disabled alwaysYes=%v err=%v", config.AlwaysYes, err)
	}
}

func TestParseArgsDefaultsWorkspaceToCurrentDirectory(t *testing.T) {
	t.Setenv("AISSH_WORKSPACE", "")
	config, help, version, err := parseArgs([]string{"server"})
	current, currentErr := os.Getwd()
	if err != nil || currentErr != nil || help || version || config.Workspace != current {
		t.Fatalf("workspace=%q current=%q help=%v version=%v err=%v currentErr=%v", config.Workspace, current, help, version, err, currentErr)
	}
	config, _, _, err = parseArgs([]string{"--no-workspace", "server"})
	if err != nil || config.Workspace != "" {
		t.Fatalf("disabled workspace=%q err=%v", config.Workspace, err)
	}
}

func TestParseArgsSupportsDirectModel(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	config, help, version, err := parseArgs([]string{
		"--agent", "deepseek", "--model-url", "https://model.example/v1", "--model-name", "deepseek-test", "server",
	})
	if err != nil || help || version {
		t.Fatalf("help=%v version=%v err=%v", help, version, err)
	}
	if config.AgentProvider != "deepseek" || config.ModelURL != "https://model.example/v1" || config.ModelName != "deepseek-test" || config.ModelAPIKey != "deepseek-secret" {
		t.Fatalf("config=%#v", config)
	}
}

func TestParseArgsSupportsScrollbackLines(t *testing.T) {
	t.Setenv("AISSH_SCROLLBACK_LINES", "")
	config, help, version, err := parseArgs([]string{"--scrollback-lines", "40000", "server"})
	if err != nil || help || version || config.ScrollbackLines != 40000 {
		t.Fatalf("scrollback=%d help=%v version=%v err=%v", config.ScrollbackLines, help, version, err)
	}
	if _, _, _, err := parseArgs([]string{"--scrollback-lines", "0", "server"}); err == nil {
		t.Fatal("invalid scrollback line count was accepted")
	}
}

func TestParseArgsSupportsAITimeout(t *testing.T) {
	t.Setenv("AISSH_AI_TIMEOUT", "")
	config, help, version, err := parseArgs([]string{"--ai-timeout", "10m", "server"})
	if err != nil || help || version || config.AITimeout != 10*time.Minute {
		t.Fatalf("timeout=%s help=%v version=%v err=%v", config.AITimeout, help, version, err)
	}
	config, _, _, err = parseArgs([]string{"--ai-timeout=180", "server"})
	if err != nil || config.AITimeout != 3*time.Minute {
		t.Fatalf("numeric timeout=%s err=%v", config.AITimeout, err)
	}
	config, _, _, err = parseArgs([]string{"--ai-timeout", "1h", "server"})
	if err != nil || config.AITimeout != time.Hour {
		t.Fatalf("hour timeout=%s err=%v", config.AITimeout, err)
	}
	if _, _, _, err := parseArgs([]string{"--ai-timeout", "0", "server"}); err == nil {
		t.Fatal("invalid AI timeout was accepted")
	}
}

func TestParseArgsSupportsSSHKeepalive(t *testing.T) {
	t.Setenv("AISSH_AI_KEEPALIVE", "")
	t.Setenv("AISSH_SSH_KEEPALIVE", "")
	config, help, version, err := parseArgs([]string{"--ssh-keepalive", "45s", "server"})
	if err != nil || help || version || config.SSHKeepalive != 45*time.Second || config.DisableSSHKeepalive {
		t.Fatalf("keepalive=%s disabled=%v help=%v version=%v err=%v", config.SSHKeepalive, config.DisableSSHKeepalive, help, version, err)
	}
	config, _, _, err = parseArgs([]string{"--ssh-keepalive=off", "server"})
	if err != nil || config.SSHKeepalive != 0 || !config.DisableSSHKeepalive {
		t.Fatalf("disabled keepalive=%s disabled=%v err=%v", config.SSHKeepalive, config.DisableSSHKeepalive, err)
	}
	config, _, _, err = parseArgs([]string{"--no-ssh-keepalive", "server"})
	if err != nil || config.SSHKeepalive != 0 || !config.DisableSSHKeepalive {
		t.Fatalf("flag keepalive=%s disabled=%v err=%v", config.SSHKeepalive, config.DisableSSHKeepalive, err)
	}
	if _, _, _, err := parseArgs([]string{"--ssh-keepalive", "later", "server"}); err == nil {
		t.Fatal("invalid SSH keepalive was accepted")
	}
}

func TestParseArgsKeepsAIKeepaliveAsLegacySSHAlias(t *testing.T) {
	t.Setenv("AISSH_AI_KEEPALIVE", "")
	t.Setenv("AISSH_SSH_KEEPALIVE", "")
	config, help, version, err := parseArgs([]string{"--ai-keepalive", "45s", "server"})
	if err != nil || help || version || config.SSHKeepalive != 45*time.Second || config.ShellTMOUT != 0 {
		t.Fatalf("ssh keepalive=%s shell tmout=%s help=%v version=%v err=%v", config.SSHKeepalive, config.ShellTMOUT, help, version, err)
	}
}

func TestParseArgsSupportsShellTMOUT(t *testing.T) {
	t.Setenv("AISSH_SHELL_TMOUT", "")
	config, help, version, err := parseArgs([]string{"--shell-tmout", "10m", "server"})
	if err != nil || help || version || config.ShellTMOUT != 10*time.Minute {
		t.Fatalf("shell tmout=%s help=%v version=%v err=%v", config.ShellTMOUT, help, version, err)
	}
	config, _, _, err = parseArgs([]string{"--shell-tmout=off", "server"})
	if err != nil || config.ShellTMOUT != 0 {
		t.Fatalf("disabled shell tmout=%s err=%v", config.ShellTMOUT, err)
	}
	if _, _, _, err := parseArgs([]string{"--shell-tmout", "later", "server"}); err == nil {
		t.Fatal("invalid shell TMOUT was accepted")
	}
}

func TestParseArgsSupportsAIContextChars(t *testing.T) {
	t.Setenv("AISSH_AI_CONTEXT_CHARS", "")
	config, help, version, err := parseArgs([]string{"--ai-context-chars", "300000", "server"})
	if err != nil || help || version || config.AIContextChars != 300000 {
		t.Fatalf("context chars=%d help=%v version=%v err=%v", config.AIContextChars, help, version, err)
	}
	config, _, _, err = parseArgs([]string{"--ai-context-chars=240000", "server"})
	if err != nil || config.AIContextChars != 240000 {
		t.Fatalf("inline context chars=%d err=%v", config.AIContextChars, err)
	}
	if _, _, _, err := parseArgs([]string{"--ai-context-chars", "0", "server"}); err == nil {
		t.Fatal("invalid AI context character count was accepted")
	}
}

func TestParseArgsCodexRetention(t *testing.T) {
	for _, tc := range []struct {
		env      string
		flags    []string
		want     bool
		explicit bool
	}{
		{"", nil, false, false},
		{"", []string{"--keep-codex-session"}, true, true},
		{"1", []string{"--no-keep-codex-session"}, false, true},
		{"0", []string{"--keep-codex-session"}, true, true},
		{"1", nil, true, true},
	} {
		t.Setenv("AISSH_KEEP_CODEX_SESSION", tc.env)
		config, _, _, err := parseArgs(append(tc.flags, "server"))
		if err != nil {
			t.Fatal(err)
		}
		if config.KeepCodexSession != tc.want || config.KeepCodexSessionSet != tc.explicit {
			t.Fatalf("env=%q flags=%v keep=%v explicit=%v", tc.env, tc.flags, config.KeepCodexSession, config.KeepCodexSessionSet)
		}
	}
}
