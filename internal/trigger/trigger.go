package trigger

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ErrorMatch struct {
	Kind    string
	Message string
}

var (
	ansiPattern   = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)
	promptPattern = regexp.MustCompile(`(?:^|[\r\n])[^\r\n]{0,240}(?:[$#>%])\s*$`)
	// A gateway can leave its alternate screen active while forwarding a shell.
	// Require a user@host primary prompt there, rather than generic TUI text.
	shellPromptPattern = regexp.MustCompile(`^(?:\[[^\s@\[\]]+@[^\s\[\]]+ [^\r\n\[\]]*\]|[^\s@]+@[^\s:]+:[^\r\n]*)[$#]\s*$`)
	sensitivePattern   = regexp.MustCompile(`(?i)(?:password|passphrase|密码)[^\r\n]{0,80}[:：]\s*$`)
	errorPatterns      = []struct {
		kind    string
		pattern *regexp.Regexp
	}{
		{"command_not_found", regexp.MustCompile(`(?i)(?:command not found(?::\s*[^\r\n]+)?|(?:^|[\r\n])(?:sh:\s*)?[^\r\n:]+:\s*not found|未找到命令)`)},
		{"permission_denied", regexp.MustCompile(`(?i)(?:permission denied|权限不够|权限被拒绝)`)},
		{"no_such_file", regexp.MustCompile(`(?i)(?:no such file or directory|没有那个文件或目录)`)},
		{"connection_refused", regexp.MustCompile(`(?i)(?:connection refused|连接被拒绝)`)},
	}
)

func StripANSI(value string) string {
	return ansiPattern.ReplaceAllString(value, "")
}

func LooksLikePrompt(value string) bool {
	plain := StripANSI(value)
	// Only the current line can be a prompt. In particular, a line feed after
	// an echoed command must not revive an earlier prompt-looking line.
	if index := strings.LastIndexAny(plain, "\r\n"); index >= 0 {
		plain = plain[index+1:]
	}
	trimmed := strings.TrimSpace(plain)
	// POSIX shells use PS2 while accepting multiline commands. This includes
	// partial command echo (e.g. "> printf ... >"), not just the bare prompt.
	if trimmed == ">" || strings.HasPrefix(trimmed, "> ") || strings.HasPrefix(trimmed, ">\t") {
		return false
	}
	return promptPattern.MatchString(plain) && !sensitivePattern.MatchString(plain)
}

func LooksSensitive(value string) bool {
	return sensitivePattern.MatchString(StripANSI(value))
}

func LooksLikeShellPrompt(value string) bool {
	plain := StripANSI(value)
	return LooksLikePrompt(plain) && shellPromptPattern.MatchString(plain)
}

func MatchError(value string) *ErrorMatch {
	plain := StripANSI(value)
	for _, candidate := range errorPatterns {
		lines := strings.Split(strings.ReplaceAll(plain, "\r", ""), "\n")
		for index := len(lines) - 1; index >= 0; index-- {
			line := strings.TrimSpace(lines[index])
			if line != "" && candidate.pattern.MatchString(line) {
				return &ErrorMatch{Kind: candidate.kind, Message: line}
			}
		}
	}
	return nil
}

func IsAIInput(value string) (prompt string, ok bool) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), "/ai ") {
		return strings.TrimSpace(trimmed[4:]), true
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	if first != utf8.RuneError && unicode.Is(unicode.Han, first) {
		return trimmed, true
	}
	return "", false
}

func ForceShellInput(value string) (command string, ok bool) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), ":shell ") {
		return strings.TrimSpace(trimmed[7:]), true
	}
	return "", false
}
