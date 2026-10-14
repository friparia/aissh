package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/app"
	"git.example.com/ai/wrd-ai-cli/internal/history"
	"git.example.com/ai/wrd-ai-cli/internal/i18n"
	"git.example.com/ai/wrd-ai-cli/internal/sshauth"
	"git.example.com/ai/wrd-ai-cli/internal/widgets"
)

var (
	version    = "dev"
	inputDebug = ""
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--widget-compile" {
		if err := widgets.RunCompiler(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if os.Getenv("AISSH_ASKPASS_MODE") == "1" {
		if len(os.Args) != 2 || sshauth.RunHelper(os.Args[1], os.Stdout) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--resource" {
		if err := resourceCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "aissh:", err)
			os.Exit(2)
		}
		return
	}
	config, showHelp, showVersion, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "aissh:", err)
		os.Exit(2)
	}
	if showHelp {
		printHelp(config.Language)
		return
	}
	if showVersion {
		fmt.Println("aissh", version)
		return
	}
	cleanup, err := prepareManaged(&config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aissh:", err)
		os.Exit(2)
	}
	defer cleanup()
	application, err := app.New(config)
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "aissh:", err)
		os.Exit(2)
	}
	if err := application.Run(); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, "aissh:", err)
		os.Exit(1)
	}
}

func parseArgs(args []string) (app.Config, bool, bool, error) {
	config := app.Config{
		Version:              version,
		SSHPath:              firstNonEmpty(os.Getenv("AISSH_SSH_PATH"), "ssh"),
		AgentProvider:        firstNonEmpty(os.Getenv("AISSH_AGENT_PROVIDER"), os.Getenv("AISSH_AGENT"), "codex"),
		AgentURL:             os.Getenv("AISSH_AGENT_URL"),
		AgentToken:           os.Getenv("AISSH_AGENT_TOKEN"),
		CodexPath:            firstNonEmpty(os.Getenv("AISSH_CODEX_PATH"), "codex"),
		CodexModel:           os.Getenv("AISSH_CODEX_MODEL"),
		KeepCodexSession:     enabledValue(os.Getenv("AISSH_KEEP_CODEX_SESSION")),
		KeepCodexSessionSet:  os.Getenv("AISSH_KEEP_CODEX_SESSION") != "",
		ClaudePath:           firstNonEmpty(os.Getenv("AISSH_CLAUDE_PATH"), "claude"),
		ClaudeModel:          os.Getenv("AISSH_CLAUDE_MODEL"),
		Language:             i18n.Resolve(firstNonEmpty(os.Getenv("AISSH_LANG"), "auto")),
		HistoryDir:           firstNonEmpty(os.Getenv("AISSH_HISTORY_DIR"), history.DefaultDir()),
		Workspace:            currentDirectory(),
		WorkspaceWritable:    enabledValue(os.Getenv("AISSH_WORKSPACE_WRITE")),
		AlwaysYes:            enabledValue(os.Getenv("AISSH_ALWAYS_YES")),
		SystemCollection:     enabledValue(os.Getenv("AISSH_SYSTEM_COLLECTION")),
		BastionCompatibility: enabledValue(os.Getenv("AISSH_BASTION_COMPATIBILITY")),
		InputDebug:           enabledValue(firstNonEmpty(os.Getenv("AISSH_INPUT_DEBUG"), inputDebug)),
		ModelURL:             os.Getenv("AISSH_MODEL_URL"),
		ModelAPIKey:          firstNonEmpty(os.Getenv("AISSH_MODEL_API_KEY"), os.Getenv("DEEPSEEK_API_KEY")),
		ModelName:            os.Getenv("AISSH_MODEL_NAME"),
		ScrollbackLines:      app.DefaultScrollbackLines,
		AIContextChars:       app.DefaultAIContextChars,
		AITimeout:            app.DefaultAITimeout,
		SSHKeepalive:         app.DefaultSSHKeepalive,
	}
	if value := strings.TrimSpace(os.Getenv("AISSH_SCROLLBACK_LINES")); value != "" {
		lines, err := positiveLineCount(value)
		if err != nil {
			return config, false, false, fmt.Errorf("AISSH_SCROLLBACK_LINES: %w", err)
		}
		config.ScrollbackLines = lines
	}
	if value := strings.TrimSpace(os.Getenv("AISSH_AI_CONTEXT_CHARS")); value != "" {
		characters, err := positiveLineCount(value)
		if err != nil {
			return config, false, false, fmt.Errorf("AISSH_AI_CONTEXT_CHARS: %w", err)
		}
		config.AIContextChars = characters
	}
	if value := strings.TrimSpace(os.Getenv("AISSH_AI_TIMEOUT")); value != "" {
		timeout, err := positiveDuration(value)
		if err != nil {
			return config, false, false, fmt.Errorf("AISSH_AI_TIMEOUT: %w", err)
		}
		config.AITimeout = timeout
	}
	if value := strings.TrimSpace(os.Getenv("AISSH_AI_KEEPALIVE")); value != "" {
		keepalive, disabled, err := durationOrDisabled(value)
		if err != nil {
			return config, false, false, fmt.Errorf("AISSH_AI_KEEPALIVE: %w", err)
		}
		config.SSHKeepalive = keepalive
		config.DisableSSHKeepalive = disabled
	}
	if value := strings.TrimSpace(os.Getenv("AISSH_SSH_KEEPALIVE")); value != "" {
		keepalive, disabled, err := durationOrDisabled(value)
		if err != nil {
			return config, false, false, fmt.Errorf("AISSH_SSH_KEEPALIVE: %w", err)
		}
		config.SSHKeepalive = keepalive
		config.DisableSSHKeepalive = disabled
	}
	if value := strings.TrimSpace(os.Getenv("AISSH_SHELL_TMOUT")); value != "" {
		timeout, disabled, err := durationOrDisabled(value)
		if err != nil {
			return config, false, false, fmt.Errorf("AISSH_SHELL_TMOUT: %w", err)
		}
		if disabled {
			timeout = 0
		}
		config.ShellTMOUT = timeout
	}
	if disabledValue(os.Getenv("AISSH_HISTORY")) {
		config.HistoryDir = ""
	}
	if value, exists := os.LookupEnv("AISSH_WORKSPACE"); exists && strings.TrimSpace(value) != "" {
		if disabledValue(value) {
			config.Workspace = ""
		} else {
			config.Workspace = value
		}
	}
	var sshArgs []string
	for index := 0; index < len(args); index++ {
		value := args[index]
		switch {
		case value == "--help" || value == "-h":
			return config, true, false, nil
		case value == "--profile" || value == "--ai-profile":
			if index+1 >= len(args) {
				return config, false, false, fmt.Errorf("%s requires a value", value)
			}
			index++
			if value == "--profile" {
				config.ProfileID = args[index]
			} else {
				config.AIProfileID = args[index]
			}
		case strings.HasPrefix(value, "--profile="):
			config.ProfileID = strings.TrimPrefix(value, "--profile=")
		case strings.HasPrefix(value, "--ai-profile="):
			config.AIProfileID = strings.TrimPrefix(value, "--ai-profile=")
		case value == "--version":
			return config, false, true, nil
		case value == "--no-history":
			config.HistoryDir = ""
		case value == "--bastion-compatibility" || value == "--ai-keystroke-input":
			config.BastionCompatibility = true
		case value == "--conversation":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return config, false, false, fmt.Errorf("--conversation requires an ID")
			}
			index++
			config.ConversationID = args[index]
		case strings.HasPrefix(value, "--conversation="):
			config.ConversationID = strings.TrimPrefix(value, "--conversation=")
			if strings.TrimSpace(config.ConversationID) == "" {
				return config, false, false, fmt.Errorf("--conversation requires an ID")
			}
		case value == "--no-workspace":
			config.Workspace = ""
		case value == "--workspace-write":
			config.WorkspaceWritable = true
		case value == "--no-workspace-write":
			config.WorkspaceWritable = false
		case value == "--keep-codex-session":
			config.KeepCodexSession, config.KeepCodexSessionSet = true, true
		case value == "--no-keep-codex-session":
			config.KeepCodexSession, config.KeepCodexSessionSet = false, true
		case value == "--always-yes":
			config.AlwaysYes = true
		case value == "--no-always-yes":
			config.AlwaysYes = false
		case value == "--system-collection":
			config.SystemCollection = true
		case value == "--no-system-collection":
			config.SystemCollection = false
		case value == "--no-ai-keepalive" || value == "--no-ssh-keepalive":
			config.SSHKeepalive = 0
			config.DisableSSHKeepalive = true
		case value == "--agent" || value == "--agent-url" || value == "--agent-token" || value == "--codex-path" || value == "--codex-model" || value == "--claude-path" || value == "--claude-model" || value == "--ssh-path" || value == "--lang" || value == "--history-dir" || value == "--workspace" || value == "--model-url" || value == "--model-name" || value == "--scrollback-lines" || value == "--ai-context-chars" || value == "--ai-timeout" || value == "--ai-keepalive" || value == "--ssh-keepalive" || value == "--shell-tmout":
			if index+1 >= len(args) {
				return config, false, false, fmt.Errorf("%s requires a value", value)
			}
			index++
			switch value {
			case "--agent":
				config.AgentProvider = args[index]
			case "--agent-url":
				config.AgentURL = args[index]
			case "--agent-token":
				config.AgentToken = args[index]
			case "--codex-path":
				config.CodexPath = args[index]
			case "--codex-model":
				config.CodexModel = args[index]
			case "--claude-path":
				config.ClaudePath = args[index]
			case "--claude-model":
				config.ClaudeModel = args[index]
			case "--ssh-path":
				config.SSHPath = args[index]
			case "--lang":
				config.Language = i18n.Resolve(args[index])
			case "--history-dir":
				config.HistoryDir = args[index]
			case "--workspace":
				config.Workspace = args[index]
			case "--model-url":
				config.ModelURL = args[index]
			case "--model-name":
				config.ModelName = args[index]
			case "--scrollback-lines":
				lines, err := positiveLineCount(args[index])
				if err != nil {
					return config, false, false, fmt.Errorf("--scrollback-lines: %w", err)
				}
				config.ScrollbackLines = lines
			case "--ai-context-chars":
				characters, err := positiveLineCount(args[index])
				if err != nil {
					return config, false, false, fmt.Errorf("--ai-context-chars: %w", err)
				}
				config.AIContextChars = characters
			case "--ai-timeout":
				timeout, err := positiveDuration(args[index])
				if err != nil {
					return config, false, false, fmt.Errorf("--ai-timeout: %w", err)
				}
				config.AITimeout = timeout
			case "--ai-keepalive", "--ssh-keepalive":
				keepalive, disabled, err := durationOrDisabled(args[index])
				if err != nil {
					return config, false, false, fmt.Errorf("%s: %w", value, err)
				}
				config.SSHKeepalive = keepalive
				config.DisableSSHKeepalive = disabled
			case "--shell-tmout":
				timeout, disabled, err := durationOrDisabled(args[index])
				if err != nil {
					return config, false, false, fmt.Errorf("--shell-tmout: %w", err)
				}
				if disabled {
					timeout = 0
				}
				config.ShellTMOUT = timeout
			}
		case strings.HasPrefix(value, "--agent="):
			config.AgentProvider = strings.TrimPrefix(value, "--agent=")
		case strings.HasPrefix(value, "--agent-url="):
			config.AgentURL = strings.TrimPrefix(value, "--agent-url=")
		case strings.HasPrefix(value, "--agent-token="):
			config.AgentToken = strings.TrimPrefix(value, "--agent-token=")
		case strings.HasPrefix(value, "--codex-path="):
			config.CodexPath = strings.TrimPrefix(value, "--codex-path=")
		case strings.HasPrefix(value, "--codex-model="):
			config.CodexModel = strings.TrimPrefix(value, "--codex-model=")
		case strings.HasPrefix(value, "--claude-path="):
			config.ClaudePath = strings.TrimPrefix(value, "--claude-path=")
		case strings.HasPrefix(value, "--claude-model="):
			config.ClaudeModel = strings.TrimPrefix(value, "--claude-model=")
		case strings.HasPrefix(value, "--ssh-path="):
			config.SSHPath = strings.TrimPrefix(value, "--ssh-path=")
		case strings.HasPrefix(value, "--lang="):
			config.Language = i18n.Resolve(strings.TrimPrefix(value, "--lang="))
		case strings.HasPrefix(value, "--history-dir="):
			config.HistoryDir = strings.TrimPrefix(value, "--history-dir=")
		case strings.HasPrefix(value, "--workspace="):
			config.Workspace = strings.TrimPrefix(value, "--workspace=")
		case strings.HasPrefix(value, "--model-url="):
			config.ModelURL = strings.TrimPrefix(value, "--model-url=")
		case strings.HasPrefix(value, "--model-name="):
			config.ModelName = strings.TrimPrefix(value, "--model-name=")
		case strings.HasPrefix(value, "--scrollback-lines="):
			lines, err := positiveLineCount(strings.TrimPrefix(value, "--scrollback-lines="))
			if err != nil {
				return config, false, false, fmt.Errorf("--scrollback-lines: %w", err)
			}
			config.ScrollbackLines = lines
		case strings.HasPrefix(value, "--ai-context-chars="):
			characters, err := positiveLineCount(strings.TrimPrefix(value, "--ai-context-chars="))
			if err != nil {
				return config, false, false, fmt.Errorf("--ai-context-chars: %w", err)
			}
			config.AIContextChars = characters
		case strings.HasPrefix(value, "--ai-timeout="):
			timeout, err := positiveDuration(strings.TrimPrefix(value, "--ai-timeout="))
			if err != nil {
				return config, false, false, fmt.Errorf("--ai-timeout: %w", err)
			}
			config.AITimeout = timeout
		case strings.HasPrefix(value, "--ai-keepalive="):
			keepalive, disabled, err := durationOrDisabled(strings.TrimPrefix(value, "--ai-keepalive="))
			if err != nil {
				return config, false, false, fmt.Errorf("--ai-keepalive: %w", err)
			}
			config.SSHKeepalive = keepalive
			config.DisableSSHKeepalive = disabled
		case strings.HasPrefix(value, "--ssh-keepalive="):
			keepalive, disabled, err := durationOrDisabled(strings.TrimPrefix(value, "--ssh-keepalive="))
			if err != nil {
				return config, false, false, fmt.Errorf("--ssh-keepalive: %w", err)
			}
			config.SSHKeepalive = keepalive
			config.DisableSSHKeepalive = disabled
		case strings.HasPrefix(value, "--shell-tmout="):
			timeout, disabled, err := durationOrDisabled(strings.TrimPrefix(value, "--shell-tmout="))
			if err != nil {
				return config, false, false, fmt.Errorf("--shell-tmout: %w", err)
			}
			if disabled {
				timeout = 0
			}
			config.ShellTMOUT = timeout
		default:
			sshArgs = append(sshArgs, value)
		}
	}
	if len(sshArgs) == 0 && config.ProfileID == "" {
		return config, true, false, nil
	}
	config.SSHArgs = sshArgs
	return config, false, false, nil
}

func printHelp(language string) {
	localizer := i18n.New(language)
	if localizer.IsEnglish() {
		fmt.Print(`aissh - AI monitor workspace for SSH

Usage:
  aissh [aissh options] [OpenSSH options] user@host

Examples:
  aissh root@192.0.2.1
  aissh -p 2222 production
  aissh -J bastion internal-server

aissh options:
  --profile ID        Connect using a saved desktop host profile
  --ai-profile ID     Use a saved AI profile and its bound credential
  --resource JSON     Run an authorised local read-only system operation
  --agent PROVIDER    AI backend: codex, claude, deepseek, model, or http
  --codex-path PATH   Local Codex CLI path; defaults to codex
  --keep-codex-session Keep Codex threads after exit for local resume (default: delete)
  --no-keep-codex-session Delete Codex threads after exit
  --codex-model MODEL Codex model; defaults to the local configuration
  --claude-path PATH  Local Claude Code CLI path; defaults to claude
  --claude-model MODEL Claude model; defaults to the local configuration
  --agent-url URL     HTTP Agent SSE endpoint
  --agent-token TOKEN HTTP Agent Bearer token
  --ssh-path PATH     OpenSSH binary; defaults to ssh from PATH
  --lang LANG         UI language: zh-CN or en (auto-detected by default)
  --history-dir DIR   Local per-target AI history directory
  --conversation ID   Continue a saved AI conversation for this server
  --no-history        Disable loading and saving AI history
  --workspace DIR     Local code workspace (default: current directory)
  --workspace-write   Allow Codex/Claude to modify files in the local workspace
  --always-yes        Auto-approve non-high-risk commands after 3 independent risk reviews
  --system-collection Enable automatic remote monitor collection (default: off)
  --no-system-collection Disable automatic remote monitor collection
  --bastion-compatibility Send approved AI commands verbatim; Ctrl+Y collects output without shell integration
  --no-workspace      Disable local workspace access for Codex/Claude
  --model-url URL     OpenAI-compatible model API base URL
  --model-name MODEL  Direct model name
  --scrollback-lines N SSH scrollback lines kept locally (default: 20000)
  --ai-context-chars N Maximum SSH output characters sent to AI (default: 120000)
  --ai-timeout DURATION AI request timeout; e.g. 30s, 5m, 1h; bare number = seconds (default: 30m)
  --ssh-keepalive DURATION OpenSSH transport keepalive interval (default: 30s; use off to disable)
  --shell-tmout DURATION Remote shell TMOUT; send an empty line near expiry only while AI runs (default: off)
  --ai-keepalive DURATION Legacy alias for --ssh-keepalive
  --no-ssh-keepalive Disable OpenSSH transport keepalive
  --version           Show version

Shortcuts:
  Ctrl+O  Expand the currently folded command in place
  Ctrl+R  Browse sessions while the AI conversation is focused
  Ctrl+T  Switch monitor tabs
  Ctrl+N  Add a monitor tab
  Esc     Return to the SSH terminal

Local commands (available after SSH connects):
  /sidebar               Toggle the monitoring and AI sidebar (hidden by default)
  /sidebar show|hide     Show or hide the sidebar (at least 92 columns)
  /workspace             Show the current local AI workspace
  /workspace LOCAL_PATH  Change the workspace for subsequent AI requests
  /workspace off         Disable local workspace access

AI command proposals appear in the left SSH pane; enter yes or no and press Enter.

All unrecognized options are passed through to OpenSSH.
`)
		return
	}
	fmt.Print(`aissh - AI 监控分屏 SSH

用法:
  aissh [aissh选项] [原生ssh选项] user@host

示例:
  aissh root@192.0.2.1
  aissh -p 2222 production
  aissh -J bastion internal-server

aissh选项:
  --profile ID        使用桌面端保存的主机及登录凭据
  --ai-profile ID     使用保存的 AI 配置及绑定凭据
  --resource JSON     执行已授权本机使用的只读系统操作
  --agent PROVIDER    AI 后端：codex、claude、deepseek、model 或 http
  --codex-path PATH   本机 Codex CLI 路径，默认 codex
  --keep-codex-session 退出后保留 Codex 会话，供本地恢复（默认删除）
  --no-keep-codex-session 退出后删除 Codex 会话
  --codex-model MODEL 指定 Codex 模型；默认沿用本机配置
  --claude-path PATH  本机 Claude Code CLI 路径，默认 claude
  --claude-model MODEL 指定 Claude 模型；默认沿用本机配置
  --agent-url URL     HTTP Agent SSE 地址；设置后兼容旧模式
  --agent-token TOKEN HTTP Agent Bearer Token
  --ssh-path PATH     系统 ssh 路径，默认使用 PATH 中的 ssh
  --lang LANG         界面语言：zh-CN 或 en，默认根据系统环境自动选择
  --history-dir DIR   按目标保存 AI 历史的本机目录
  --conversation ID   继续当前服务器上保存的 AI 对话
  --no-history        禁用 AI 历史的读取和写入
  --workspace DIR     供 Codex/Claude 使用的本机代码工作区，默认当前目录
  --workspace-write   允许 Codex/Claude 修改本机工作区文件（默认只读）
  --always-yes        经 3 次独立风险复核后自动批准非高风险命令
  --system-collection 开启自动远端监控采集（默认关闭）
  --no-system-collection 关闭自动远端监控采集
  --bastion-compatibility 堡垒机兼容模式：原样发送批准的命令，Ctrl+Y 手动收集输出
  --no-workspace      禁止 Codex/Claude 访问本机工作区
  --model-url URL     OpenAI-compatible 模型 API 地址
  --model-name MODEL  直连模型名称
  --scrollback-lines N 本机保留的 SSH 回滚行数，默认 20000
  --ai-context-chars N 每次交给 AI 的 SSH 输出字符上限，默认 120000
  --ai-timeout DURATION AI 请求超时；如 30s、5m、1h；纯数字按秒（默认 30m）
  --ssh-keepalive DURATION OpenSSH 传输层保活间隔（默认 30s；off 可关闭）
  --shell-tmout DURATION 远端 Shell 的 TMOUT；仅在 AI 执行且临近超时时发送空行（默认关闭）
  --ai-keepalive DURATION --ssh-keepalive 的兼容别名
  --no-ssh-keepalive 关闭 OpenSSH 传输层保活
  --version           显示版本

快捷键:
  Ctrl+O  就地展开当前折叠的完整命令
  Ctrl+R  聚焦 AI 对话时浏览当前目标的历史会话
  Ctrl+T  切换右上监控 Tab
  Ctrl+N  新增监控 Tab
  Esc     返回 SSH 主终端

本地命令（SSH 连接后可在左侧提示符或右下 AI 输入框使用）:
  /sidebar               收起或展开右侧监控与 AI 栏目（默认收起）
  /sidebar show|hide     展开或收起右栏（终端至少 92 列）
  /workspace             查看当前本机 AI 工作区
  /workspace 本机路径     切换后续 AI 请求使用的工作区
  /workspace off         关闭本机工作区访问

AI 建议命令时，会在左侧 SSH 窗口显示确认卡片；输入 yes 或 no 后回车。

所有未识别参数都会原样传给系统 OpenSSH。
`)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func disabledValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "off", "no", "none", "disabled":
		return true
	default:
		return false
	}
}

func enabledValue(value string) bool {
	return strings.TrimSpace(value) != "" && !disabledValue(value)
}

func currentDirectory() string {
	value, err := os.Getwd()
	if err != nil {
		return ""
	}
	return value
}

func positiveLineCount(value string) (int, error) {
	lines, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || lines <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return lines, nil
}

func positiveDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("must be a positive duration")
	}
	if _, err := strconv.Atoi(value); err == nil {
		value += "s"
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("must be a positive duration such as 300s, 5m, or 10m")
	}
	return duration, nil
}

func durationOrDisabled(value string) (time.Duration, bool, error) {
	if disabledValue(value) {
		return 0, true, nil
	}
	duration, err := positiveDuration(value)
	return duration, false, err
}
