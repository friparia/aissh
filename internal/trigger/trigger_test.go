package trigger

import "testing"

func TestAIInput(t *testing.T) {
	for _, input := range []string{"帮我检查 nginx", "  /ai explain this"} {
		if _, ok := IsAIInput(input); !ok {
			t.Fatalf("expected AI input: %q", input)
		}
	}
	if _, ok := IsAIInput("echo 中文"); ok {
		t.Fatal("shell command should not be classified as AI input")
	}
}

func TestPromptAndError(t *testing.T) {
	if !LooksLikePrompt("root@host:~# ") {
		t.Fatal("expected prompt")
	}
	if LooksLikePrompt("Password: ") {
		t.Fatal("password prompt must not be treated as shell prompt")
	}
	match := MatchError("bash: docker-compose: command not found\r\n")
	if match == nil || match.Kind != "command_not_found" {
		t.Fatalf("match = %#v", match)
	}
	if match.Message != "bash: docker-compose: command not found" {
		t.Fatalf("message = %q", match.Message)
	}
}

func TestPromptRequiresCurrentPrimaryPrompt(t *testing.T) {
	for _, value := range []string{
		"> ", "(\r\n> ", "> printf 'ok' > ", "> printf '$", ">\tprintf '%",
		"root@host:~# \r\n", "root@host:~# \r\n\x1b[?2004l", "Password: ",
	} {
		if LooksLikePrompt(value) {
			t.Errorf("continuation or old line treated as primary prompt: %q", value)
		}
	}
	for _, value := range []string{
		"root@host:~# ", "output\r\nuser@host:~$ ", "\x1b[32mhost% \x1b[0m",
		"PS C:\\Users\\user> ", "C:\\Users\\user> ", "prompt> ",
	} {
		if !LooksLikePrompt(value) {
			t.Errorf("primary prompt not detected: %q", value)
		}
	}
}

func TestShellPromptRequiresUserAndHost(t *testing.T) {
	for _, value := range []string{
		"[deployer@SZJD-YFQ-PM-OS01-BCONEST-BEAZ-OBSERVER-01 ~]$ ",
		"[root@node /var/log]# ", "ops@node:~$ ",
		"\x1b[32mroot@node:/tmp# \x1b[0m",
	} {
		if !LooksLikeShellPrompt(value) {
			t.Errorf("shell prompt not detected: %q", value)
		}
	}
	for _, value := range []string{"请输入 : ", "编辑器> ", "prompt> ", "$ ", "Password: ", "> root@node:~$ ", "ops@node:~$ \r\n"} {
		if LooksLikeShellPrompt(value) {
			t.Errorf("non-shell prompt accepted: %q", value)
		}
	}
}
