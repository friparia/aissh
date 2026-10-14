package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hinshun/vt10x"
	"golang.org/x/term"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"git.example.com/ai/wrd-ai-cli/internal/history"
	app_i18n "git.example.com/ai/wrd-ai-cli/internal/i18n"
	"git.example.com/ai/wrd-ai-cli/internal/localworkspace"
	"git.example.com/ai/wrd-ai-cli/internal/monitor"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
	"git.example.com/ai/wrd-ai-cli/internal/redact"
	"git.example.com/ai/wrd-ai-cli/internal/resources"
	"git.example.com/ai/wrd-ai-cli/internal/sshauth"
	"git.example.com/ai/wrd-ai-cli/internal/sshwrap"
	"git.example.com/ai/wrd-ai-cli/internal/terminalview"
	"git.example.com/ai/wrd-ai-cli/internal/trigger"
)

type Config struct {
	DesktopUI            bool
	ProfileID            string
	AIProfileID          string
	HostLabel            string
	HistoryTarget        string
	ConversationID       string
	ConversationChanged  func(string)
	SSHEnvironment       []string
	SecretFilter         *redact.Filter
	Resources            *resources.Executor
	Version              string
	SSHPath              string
	SSHArgs              []string
	AgentProvider        string
	AgentURL             string
	AgentToken           string
	CodexPath            string
	CodexModel           string
	KeepCodexSession     bool
	KeepCodexSessionSet  bool
	ClaudePath           string
	ClaudeModel          string
	Language             string
	HistoryDir           string
	Workspace            string
	WorkspaceWritable    bool
	AlwaysYes            bool
	SystemCollection     bool
	BastionCompatibility bool
	InputDebug           bool
	ModelURL             string
	ModelAPIKey          string
	ModelName            string
	ModelOptions         []string
	ScrollbackLines      int
	AIContextChars       int
	AITimeout            time.Duration
	SSHKeepalive         time.Duration
	DisableSSHKeepalive  bool
	ShellTMOUT           time.Duration
}

const terminalFrameInterval = time.Second / 30
const DefaultScrollbackLines = 20000
const DefaultAIContextChars = agent.DefaultOutputContextChars
const DefaultAITimeout = 30 * time.Minute
const DefaultSSHKeepalive = 30 * time.Second
const AlwaysYesRiskAssessmentPasses = 3

type App struct {
	pipelineSequence      uint64
	pipelineSession       string
	pipelineGeneration    uint64
	pipelineDrafts        map[string]*pipelineDraft
	pipelineBatches       map[string]*pipelineBatch
	pipelineFeedback      []string
	pipelineContinuations int

	config Config
	parsed sshwrap.Parsed

	ctx           context.Context
	cancel        context.CancelFunc
	events        chan interface{}
	desktopStates chan credentialui.WorkbenchState

	sshCommand   *exec.Cmd
	sshPTY       io.ReadWriteCloser
	sshInput     *sshInputWriter
	controlDir   string
	controlPath  string
	monitor      monitor.Runner
	virtual      vt10x.Terminal
	transformer  terminalview.WideTransformer
	terminalDone bool
	terminalErr  error
	screenOut    io.Writer
	zmodem       *zmodemPassthrough
	zmodemTail   []byte

	cols             int
	rows             int
	mainCols         int
	contentRows      int
	compact          bool
	sidebarCollapsed bool

	widgetCandidates        map[string]*widgetCandidate
	panels                  *panel.Manager
	monitorViaPrimary       bool
	monitorPrimaryFallback  bool
	monitorPrimaryPaused    bool
	monitorAwaitingPrompt   bool
	inBandMonitor           *inBandMonitorState
	monitorPrompt           *monitorPromptState
	monitorDeferredInput    []byte
	monitorReview           *monitorAudit
	monitorDecisions        map[string]monitorDecision
	monitorSequence         uint64
	monitorLayout           monitorLayoutMode
	monitorLast             map[string]time.Time
	monitorActive           map[string]bool
	panelScroll             map[string]int
	monitorSlots            map[monitorLayoutMode]map[int]string
	monitorSlot             int
	monitorSlotMode         monitorLayoutMode
	focus                   focusMode
	menuStatus              string
	editor                  []rune
	aiEditorCursor          int
	aiPasteMode             bool
	approvalInput           []rune
	approvalStatus          string
	approvalCommandExpanded bool
	aiLineCapture           bool
	lineInput               []byte
	terminalInputAt         time.Time
	lineCursor              int
	lineAnchorX             int
	lineAnchorY             int
	lineAnchored            bool
	lineReliable            bool
	terminalKeyCarry        []byte
	terminalPasteMode       bool
	terminalPasteBuffer     []byte
	atPrompt                bool
	gatewayShellPrompt      bool
	sensitive               bool
	lastCommand             string
	outputTail              *rollingBuffer
	sshOutputTail           *rollingBuffer // raw PTY tail, independent of monitor/prompt filtering
	recentOutput            *contextBuffer
	errorCarry              string
	pendingError            *trigger.ErrorMatch

	agent                 agent.Provider
	agentBusy             bool
	agentCancel           context.CancelFunc
	agentGeneration       uint64
	terminalTakeover      bool
	agentInline           *inlineAIState
	agentSessionID        string
	codexThreadID         string
	agentStatus           string
	agentThought          string
	notifyAI              func(title, message string)
	aiNotice              string
	desktopNotices        []credentialui.WorkbenchNotice
	desktopNoticeSequence uint64
	agentLanguage         string
	agentSeedContext      string
	panelActionResults    []string
	promptIdleSince       time.Time
	chat                  []chatMessage
	proposal              *agent.Proposal
	aiExpanded            bool
	aiScroll              int
	aiDisplayCache        *aiConversationCache
	sidebarFrame          string
	inlineAI              *inlineAIState
	inlineHistory         []*inlineAIState
	execution             *executionState
	localizer             app_i18n.Localizer
	historyStore          *history.Store
	historyTarget         string
	historyWarned         bool
	historyConversations  []history.Conversation
	historyConversationID string
	historySelection      int
	mouseCarry            []byte
	mouseMode             bool
	mouseReporting        bool
	mouseScrollbarDrag    scrollbarDragState
	clearCarry            []byte
	terminalResizePending bool
	terminalResizeAnchors map[*inlineAIState]inlineResizeAnchor
	terminalRows          map[int]string
	terminalHistory       []terminalview.Line
	terminalScroll        int

	retiredExecutions       []*executionState
	executionRecovery       bool
	executionRecoveryOutput bool
	executionInterrupt      *sshWriteJob

	dirty bool

	inputDebug *inputDebugLogger
}

func New(config Config) (*App, error) {
	if strings.TrimSpace(config.SSHPath) == "" {
		config.SSHPath = "ssh"
	}
	if config.ScrollbackLines <= 0 {
		config.ScrollbackLines = DefaultScrollbackLines
	}
	if config.AIContextChars <= 0 {
		config.AIContextChars = DefaultAIContextChars
	}
	if config.AITimeout <= 0 {
		config.AITimeout = DefaultAITimeout
	}
	if config.DisableSSHKeepalive {
		config.SSHKeepalive = 0
	} else if config.SSHKeepalive <= 0 {
		config.SSHKeepalive = DefaultSSHKeepalive
	}
	parsed, err := sshwrap.Parse(config.SSHArgs)
	if err != nil {
		return nil, err
	}
	if len(parsed.RemoteCommand) > 0 {
		return nil, fmt.Errorf("aissh interactive workspace does not accept a remote command yet")
	}
	localizer := app_i18n.New(config.Language)
	config.Language = localizer.Language()
	mode := agentMode(config)
	if mode == "deepseek" {
		config.ModelURL = firstText(config.ModelURL, "https://api.deepseek.com")
		config.ModelName = firstText(config.ModelName, "deepseek-v4-pro")
	}
	// Restore the conversation directory before validating the host default.
	if config.ConversationID != "" && config.HistoryDir != "" {
		target := config.HistoryTarget
		if target == "" {
			target = parsed.Target
		}
		conversations, err := history.New(config.HistoryDir).LoadConversations(target)
		if err != nil {
			return nil, err
		}
		for _, conversation := range conversations {
			if conversation.ID == config.ConversationID {
				if conversation.Workspace != nil {
					config.Workspace = *conversation.Workspace
				}
				for _, model := range config.ModelOptions {
					if mode == "model" && model == conversation.AIModel {
						config.ModelName = model
						break
					}
				}
				break
			}
		}
	}
	if mode != "codex" && mode != "local" && mode != "claude" {
		config.Workspace = ""
		config.WorkspaceWritable = false
	}
	workspace, err := resolveLocalWorkspace(config.Workspace)
	if err != nil {
		return nil, fmt.Errorf("%s", localizer.T("无法访问本机代码工作区：%s", err.Error()))
	}
	config.Workspace = workspace
	ctx, cancel := context.WithCancel(context.Background())
	provider, err := newAgentProvider(config)
	if err != nil {
		cancel()
		return nil, err
	}
	panels := panel.NewManager(parsed.Target)
	panels.ByID("system").Title = localizer.T("系统")
	value := &App{
		config:                config,
		parsed:                parsed,
		ctx:                   ctx,
		cancel:                cancel,
		events:                make(chan interface{}, 256),
		sidebarCollapsed:      true,
		panels:                panels,
		monitorLast:           make(map[string]time.Time),
		monitorActive:         make(map[string]bool),
		panelScroll:           make(map[string]int),
		monitorSlots:          make(map[monitorLayoutMode]map[int]string),
		monitorSlot:           -1,
		focus:                 focusTerminal,
		aiExpanded:            true,
		lineReliable:          true,
		outputTail:            newRollingBuffer(16 * 1024),
		sshOutputTail:         newRollingBuffer(16 * 1024),
		recentOutput:          newContextBuffer(config.AIContextChars),
		agent:                 provider,
		localizer:             localizer,
		historyConversationID: history.NewConversationID(),
		dirty:                 true,
	}
	if strings.TrimSpace(config.HistoryDir) != "" {
		value.historyStore = history.New(config.HistoryDir)
		value.historyTarget = parsed.Target
		if config.HistoryTarget != "" {
			value.historyTarget = config.HistoryTarget
		}
		value.loadChatHistory()
	}
	value.appendAIReadyMessages()
	if config.ConversationID != "" {
		if err := value.resumeConversation(config.ConversationID); err != nil {
			cancel()
			return nil, err
		}
		value.sidebarCollapsed = false
	} else {
		value.notifyConversationChanged()
	}
	return value, nil
}

func (a *App) t(message string, args ...any) string {
	return a.localizer.T(message, args...)
}

func newAgentProvider(config Config) (agent.Provider, error) {
	mode := agentMode(config)
	switch mode {
	case "codex", "local":
		provider := agent.NewCodexProvider(config.CodexPath, config.CodexModel)
		provider.KeepSession = config.KeepCodexSession
		provider.Streaming = config.DesktopUI
		provider.Language = config.Language
		provider.SetLocalWorkspaceWritable(config.WorkspaceWritable)
		provider.SetLocalWorkspace(config.Workspace)
		provider.Timeout = config.AITimeout
		provider.OutputContextChars = config.AIContextChars
		return provider, nil
	case "claude":
		provider := agent.NewClaudeProvider(config.ClaudePath, config.ClaudeModel)
		provider.Streaming = config.DesktopUI
		provider.Language = config.Language
		provider.SetLocalWorkspaceWritable(config.WorkspaceWritable)
		provider.SetLocalWorkspace(config.Workspace)
		provider.Timeout = config.AITimeout
		provider.OutputContextChars = config.AIContextChars
		return provider, nil
	case "http", "remote":
		provider := agent.NewClient(config.AgentURL, config.AgentToken)
		provider.HTTP.Timeout = config.AITimeout
		return provider, nil
	case "deepseek":
		provider := agent.NewDirectModelProvider("DeepSeek", config.ModelURL, config.ModelAPIKey, config.ModelName, true)
		provider.Streaming = config.DesktopUI
		provider.Language = config.Language
		provider.HTTP.Timeout = config.AITimeout
		provider.OutputContextChars = config.AIContextChars
		return provider, nil
	case "model", "openai-compatible":
		provider := agent.NewDirectModelProvider("Model", config.ModelURL, config.ModelAPIKey, config.ModelName, false)
		provider.Streaming = config.DesktopUI
		provider.Language = config.Language
		provider.HTTP.Timeout = config.AITimeout
		provider.OutputContextChars = config.AIContextChars
		return provider, nil
	default:
		return nil, fmt.Errorf("unsupported AI provider %q (use codex, claude, deepseek, model, or http)", mode)
	}
}

func agentMode(config Config) string {
	mode := strings.ToLower(strings.TrimSpace(config.AgentProvider))
	if mode == "" {
		if strings.TrimSpace(config.AgentURL) != "" {
			mode = "http"
		} else {
			mode = "codex"
		}
	}
	return mode
}

func resolveLocalWorkspace(value string) (string, error) {
	return localworkspace.Resolve(value)
}

func (a *App) Run() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("aissh must run in an interactive terminal")
	}
	if a.config.InputDebug {
		debug, err := newInputDebugLogger(a.config.Version)
		if err != nil {
			return fmt.Errorf("start input diagnostics: %w", err)
		}
		a.inputDebug = debug
		defer a.closeInputDebug()
	}
	if err := a.updateSize(); err != nil {
		return err
	}
	if err := a.startSSH(); err != nil {
		return err
	}
	oldState, err := makeTerminalRaw(int(os.Stdin.Fd()))
	if err != nil {
		a.stopSSH()
		return err
	}
	defer func() {
		a.cancel()
		_ = restoreTerminal(int(os.Stdin.Fd()), oldState)
		_, _ = io.WriteString(os.Stdout, "\x1b[?2026l"+bracketedPasteDisable+mouseReportingDisable+"\x1b[0m\x1b[?25h\x1b[?1049l")
		a.stopSSH()
		if closer, ok := a.agent.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		if !a.config.DesktopUI && a.config.KeepCodexSession && a.codexThreadID != "" {
			_, _ = fmt.Fprintf(os.Stdout, "\n%s\n", a.t("本地恢复 Codex：codex resume %s", a.codexThreadID))
		}
		if a.controlDir != "" {
			_ = os.RemoveAll(a.controlDir)
		}
	}()
	if !a.config.DesktopUI {
		_, _ = io.WriteString(os.Stdout, "\x1b[?1049h\x1b[2J\x1b[H"+bracketedPasteEnable+mouseReportingDisable)
	}
	a.notifyAI = newAINotifier(a.ctx)

	a.startInputReader()
	a.startSignalReader()
	if a.config.DesktopUI {
		a.desktopStates = make(chan credentialui.WorkbenchState, 1)
		go a.runDesktopBridge()
	}

	maintenanceTicker := time.NewTicker(120 * time.Millisecond)
	desktopTicker := time.NewTicker(80 * time.Millisecond)
	defer desktopTicker.Stop()
	frameTicker := time.NewTicker(terminalFrameInterval)
	defer maintenanceTicker.Stop()
	defer frameTicker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return nil
		case event := <-a.events:
			a.handleEvent(event)
		case <-desktopTicker.C:
			a.publishDesktopState()
		case <-maintenanceTicker.C:
			now := time.Now()
			a.checkZmodemCompletion(now)
			a.checkExecutionWait(now)
			a.maintainAIKeepalive(now)
			a.checkInBandMonitorTimeout(now)
			a.submitExecutionResult()
			a.scheduleMonitors()
			if a.inlineAI != nil && a.agentBusy {
				a.inlineAI.Spinner++
				a.dirty = true
			}
		case <-frameTicker.C:
			if a.dirty && a.zmodem == nil {
				a.render()
				a.dirty = false
			}
		}
		if a.terminalDone {
			if a.dirty {
				a.render()
				a.dirty = false
			}
			return a.sshSessionError()
		}
	}
}

func (a *App) startSSH() error {
	args := append([]string(nil), a.config.SSHArgs...)
	if a.config.SSHKeepalive > 0 {
		args = append(sshServerAliveOptions(a.config.SSHKeepalive), args...)
	}
	if supportsSSHControlPath() && !sshwrap.HasControlPath(args) {
		directory, err := os.MkdirTemp("", "aissh-")
		if err != nil {
			return err
		}
		if err := os.Chmod(directory, 0700); err != nil {
			return err
		}
		a.controlDir = directory
		a.controlPath = filepath.Join(directory, "master")
		args = append(sshwrap.ControlOptions(a.controlPath), args...)
	}
	args = interactiveSSHArgs(args)
	cmd := exec.CommandContext(a.ctx, a.config.SSHPath, args...)
	cmd.Env = append(sshauth.CleanEnvironment(os.Environ()), "TERM=xterm-256color")
	if a.config.SSHEnvironment != nil {
		cmd.Env = append(append([]string(nil), a.config.SSHEnvironment...), "TERM=xterm-256color")
	}
	ptmx, err := startSSHSession(cmd, a.contentRows, a.mainCols)
	if err != nil {
		return err
	}
	a.sshCommand = cmd
	a.sshPTY = ptmx
	a.sshInput = newSSHInputWriter(a.ctx, ptmx, a.events)
	var terminalReplyWriter io.Writer = a.sshInput
	if a.config.DesktopUI {
		// xterm answers device/cursor queries for the actual displayed terminal.
		// The observation-only emulator must not send a second, conflicting reply.
		terminalReplyWriter = io.Discard
	}
	a.virtual = vt10x.New(vt10x.WithWriter(terminalReplyWriter), vt10x.WithSize(a.mainCols, a.contentRows))
	a.monitor = monitor.Runner{
		SSHPath:     a.config.SSHPath,
		Parsed:      a.parsed,
		ControlPath: a.controlPath,
		Timeout:     8 * time.Second,
		Language:    a.localizer.Language(),
		RawOutput:   a.config.DesktopUI,
	}
	// Keep background monitoring isolated from the interactive PTY initially.
	// Win32-OpenSSH cannot reuse ControlMaster, so password-only sessions may
	// reject the separate BatchMode query. In that case applyMonitorResult
	// switches monitoring to the already authenticated primary shell.
	a.monitorViaPrimary = false
	a.monitorPrimaryFallback = !supportsSSHControlPath()
	a.startOutputReader()
	return nil
}

func (a *App) stopSSH() {
	if a.sshCommand != nil && a.sshCommand.Process != nil {
		stopSSHProcess(a.sshCommand.Process)
	}
	if a.sshPTY != nil {
		_ = a.sshPTY.Close()
	}
}

func (a *App) writeTerminalOutput(tokens []terminalview.OutputToken) []terminalview.Line {
	wasAlternate := a.virtual.Mode()&vt10x.ModeAltScreen != 0
	defer func() {
		if wasAlternate != (a.virtual.Mode()&vt10x.ModeAltScreen != 0) {
			a.gatewayShellPrompt = false
		}
	}()
	var history []terminalview.Line
	for index := 0; index < len(tokens); {
		token := tokens[index]
		switch token.Kind {
		case terminalview.OutputText:
			cells := terminalview.CellsUntilAutoWrap(a.virtual, a.mainCols)
			if cells == 0 {
				if line, ok := terminalview.CaptureTopLineBeforeToken(a.virtual, a.contentRows-1, a.mainCols, token); ok {
					history = append(history, line)
				}
				// The first token consumes the pending wrap, then the rest can fill
				// the newly entered row without causing another wrap.
				cells = a.mainCols
			}
			end := index
			for end < len(tokens) && end-index < cells && tokens[end].Kind == terminalview.OutputText {
				end++
			}
			_, _ = a.virtual.Write(joinOutputTokens(tokens[index:end]))
			index = end
		case terminalview.OutputLineFeed:
			if line, ok := terminalview.CaptureTopLineBeforeToken(a.virtual, a.contentRows-1, a.mainCols, token); ok {
				history = append(history, line)
			}
			_, _ = a.virtual.Write(token.Data)
			index++
		default:
			end := index + 1
			for end < len(tokens) && tokens[end].Kind == terminalview.OutputControl {
				end++
			}
			_, _ = a.virtual.Write(joinOutputTokens(tokens[index:end]))
			index = end
		}
	}
	return history
}

func joinOutputTokens(tokens []terminalview.OutputToken) []byte {
	if len(tokens) == 1 {
		return tokens[0].Data
	}
	size := 0
	for _, token := range tokens {
		size += len(token.Data)
	}
	data := make([]byte, 0, size)
	for _, token := range tokens {
		data = append(data, token.Data...)
	}
	return data
}

func (a *App) updateSize() error {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return err
	}
	if cols < 40 || rows < 10 {
		return fmt.Errorf("terminal is too small; need at least 40x10")
	}
	a.setViewportSize(cols, rows)
	return nil
}

func (a *App) setViewportSize(cols, rows int) {
	a.cols, a.rows = cols, rows
	a.contentRows = rows - 2
	if a.config.DesktopUI {
		a.contentRows = rows
	}
	a.compact = cols < 92
	if !a.config.DesktopUI && !a.compact && a.sidebarCollapsed && a.sidebarWorkspaceFocused() {
		a.sidebarCollapsed = false
	}
	a.applyPaneLayout()
}

func (a *App) applyPaneLayout() {
	if a.cols <= 0 || a.contentRows <= 0 {
		return
	}
	previousMainCols := a.mainCols
	// mainCols is the SSH content width; the column right of it (mainCols+1) is
	// reserved as the scrollbar gutter so the scrollbar never covers pane text.
	if a.config.DesktopUI {
		// The desktop owns the caption, scrollbar and status outside the PTY.
		a.mainCols = a.cols
	} else if a.compact || a.sidebarCollapsed {
		a.mainCols = a.cols - 1
	} else {
		a.mainCols = a.cols*64/100 - 1
		if a.mainCols < 48 {
			a.mainCols = 48
		}
		if a.cols-a.mainCols-2 < 32 {
			a.mainCols = a.cols - 34
		}
	}
	resized := previousMainCols != a.mainCols
	if a.virtual != nil {
		cols, rows := a.virtual.Size()
		resized = cols != a.mainCols || rows != a.contentRows
		if resized {
			// ConPTY can clear and repaint after a resize. That repaint must
			// not erase AI output owned by the local UI.
			a.terminalResizePending = true
			a.captureInlineResizeAnchors()
			a.virtual.Resize(a.mainCols, a.contentRows)
		}
	}
	if resized {
		a.terminalRows = nil
	}
	if resized && a.sshPTY != nil {
		resizeSSHSession(a.sshPTY, a.contentRows, a.mainCols)
	}
}

func (a *App) sidebarVisible() bool {
	return !a.config.DesktopUI && !a.compact && !a.sidebarCollapsed
}

func (a *App) sidebarWorkspaceFocused() bool {
	switch a.focus {
	case focusAI, focusAIHistory, focusMonitor, focusMonitorSlot, focusPanelMenu, focusCustomPanel:
		return true
	default:
		return false
	}
}

func (a *App) toggleSidebar() {
	if a.config.DesktopUI || a.compact {
		return
	}
	a.sidebarCollapsed = !a.sidebarCollapsed
	if a.sidebarCollapsed {
		a.mirrorActiveAIInline()
		if a.proposal == nil {
			a.focus = focusTerminal
		} else {
			a.focus = focusApproval
		}
	}
	a.applyPaneLayout()
	a.dirty = true
}

func (a *App) ensureSidebarVisible() {
	if a.config.DesktopUI || a.compact || !a.sidebarCollapsed {
		return
	}
	a.sidebarCollapsed = false
	a.applyPaneLayout()
	a.dirty = true
}

func (a *App) handleEvent(event interface{}) {
	if scoped, ok := event.(agentOperationEvent); ok {
		if scoped.generation != a.agentGeneration {
			return
		}
		event = scoped.event
	}
	switch value := event.(type) {
	case widgetCompiledEvent:
		a.widgetCompiled(value)
	case desktopCommandEvent:
		value.reply <- a.handleDesktopCommand(value.command)
	case sshWriteEvent:
		a.finishSSHWrite(value)
	case inputEvent:
		a.handleInput(value.data)
	case outputEvent:
		if a.sshOutputTail != nil {
			a.sshOutputTail.Append(string(value.data))
		}
		a.processSSHOutput(value.data)
	case resizeEvent:
		_ = a.updateSize()
		a.dirty = true
	case sshExitEvent:
		a.invalidatePipelines()
		a.terminalErr = value.err
		if !a.terminalDone && value.err != nil {
			a.appendPersistentChat(chatMessage{Role: "system", Content: a.safeText(a.sshSessionError().Error()), Time: time.Now()})
		}
		a.terminalDone = true
		a.dirty = true
	case monitorResultEvent:
		a.applyMonitorResult(value)
	case agentStatusEvent:
		a.agentStatus = a.t(value.status)
		if a.agentInline != nil {
			if a.agentInline.StatusPrefix != "" {
				a.agentInline.Status = a.agentInline.StatusPrefix + " · " + a.t(value.status)
			} else {
				a.agentInline.Status = a.t(value.status)
			}
		}
		a.dirty = true
	case agentThoughtEvent:
		a.agentThought += value.content
		if len(a.agentThought) > 12000 {
			a.agentThought = a.agentThought[len(a.agentThought)-12000:]
		}
		a.dirty = true
	case sshActivityEvent:
		a.updateSSHActivity(value.proposalID, value.status, value.detail)
		a.dirty = true
	case agentActivityEvent:
		a.updateAgentActivity(value.activity)
		a.dirty = true
	case agentAnswerEvent:
		a.agentThought = ""
		a.appendAssistantDelta(value.content)
		if a.agentInline != nil {
			a.agentInline.Response += value.content
		}
		a.dirty = true
	case agentTurnEvent:
		a.finishAgentTurn(value.turn, value.err)
	case agentPanelEvent:
		a.applyPanelAction(value.action)
	case alwaysYesRiskProgressEvent:
		status := a.t("always-yes 正在进行风险复核（%d/%d）", value.pass, value.total)
		a.agentStatus = status
		a.approvalStatus = status
		a.dirty = true
	case alwaysYesRiskEvent:
		a.finishAlwaysYesRiskAssessment(value)
	case approvalEvent:
		a.finishApproval(value)
	case rejectionEvent:
		a.finishRejection(value)
	case executionResultEvent:
		a.finishExecutionResult(value)
	case delayedErrorTriggerEvent:
		if a.pendingError != nil && a.pendingError.Kind == value.match.Kind {
			a.pendingError = nil
			a.startAI(a.t("刚才命令 %q 执行失败：%s。请分析原因并给出安全的下一步。", value.command, value.match.Message), true)
		}
	}
}

func (a *App) monitorWorkspaceVisible() bool {
	if a.config.DesktopUI {
		return true
	}
	if a.sidebarVisible() {
		return true
	}
	if !a.compact {
		return false
	}
	return a.focus == focusMonitor || a.focus == focusMonitorSlot || a.focus == focusPanelMenu || a.focus == focusCustomPanel
}

func (a *App) handleOutput(data []byte) {
	if a.execution != nil && a.execution.Direct && !a.execution.finished() {
		a.execution.LastOutputAt = time.Now()
	}
	a.observeTerminalClear(data)
	plain := trigger.StripANSI(string(data))
	a.outputTail.Append(plain)
	a.recentOutput.Append(plain)
	if a.execution != nil && !a.execution.finished() && a.execution.Output != nil {
		a.execution.Output.Append(plain)
	}
	a.sensitive = trigger.LooksSensitive(a.outputTail.String())
	a.refreshGatewayShellPrompt()
	if a.terminalTakeover {
		plain := trigger.StripANSI(a.outputTail.String())
		if trigger.LooksLikePrompt(plain) && a.safePromptScreen() && !a.sensitive {
			a.terminalTakeover = false
		} else {
			a.atPrompt = false
		}
	}
	// Opening a saved AI conversation can focus its editor before SSH has
	// authenticated. A password prompt must hand input back to the terminal.
	if a.sensitive && a.focus == focusAI {
		a.focus = focusTerminal
	}
	errorWindow := a.errorCarry + plain
	if match := trigger.MatchError(errorWindow); match != nil {
		a.pendingError = match
	}
	if len(errorWindow) > 512 {
		errorWindow = errorWindow[len(errorWindow)-512:]
		for len(errorWindow) > 0 && (errorWindow[0]&0xc0) == 0x80 {
			errorWindow = errorWindow[1:]
		}
	}
	a.errorCarry = errorWindow
	if a.executionRecovery {
		if a.executionRecoveryCanObserveOutput() {
			a.executionRecoveryOutput = a.executionRecoveryOutput || plain != ""
			a.tryRecoverExecutionPrompt()
		} else {
			// Discard output received before the interrupt starts. Keep output
			// received during that write until its acknowledgement arrives.
			a.outputTail.value = ""
		}
	}
	promptScreenSafe := a.safePromptScreen()
	if !promptScreenSafe && len(a.lineInput) == 0 && !a.aiLineCapture {
		a.atPrompt = false
	}
	if !a.terminalTakeover && a.inBandMonitor == nil && !a.executionRecovery && (a.execution == nil || a.execution.finished()) && promptScreenSafe && (trigger.LooksLikePrompt(a.outputTail.String()) || a.gatewayShellPrompt) {
		if !a.atPrompt {
			a.promptIdleSince = time.Now()
		}
		a.atPrompt = true
		a.monitorAwaitingPrompt = false
		a.sensitive = false
		a.resolveInlineExecutionAnchor()
		if a.execution == nil && a.pendingError != nil && a.pendingError.Kind == "command_not_found" && !a.agentBusy {
			match := *a.pendingError
			command := a.lastCommand
			go func() {
				time.Sleep(120 * time.Millisecond)
				a.events <- delayedErrorTriggerEvent{match: match, command: command, createdAt: time.Now()}
			}()
		}
	}
	a.dirty = true
}

func (a *App) scheduleMonitors() {
	now := time.Now()
	a.expireWidgetCandidates(now)
	a.schedulePipelines(now)
	if a.config.SystemCollection {
		a.scheduleInBandMonitor(now)
	}
	a.continuePipelineAI()
}

func (a *App) applyMonitorResult(value monitorResultEvent) {
	if value.pipelineID != "" {
		a.applyPipelineResult(value)
		return
	}
	item := a.panels.ByID(value.panelID)
	if value.binding != "" && (item == nil || item.BindingID != value.binding || item.SourceRevision != value.revision || item.RunID != value.runID) {
		if item != nil && item.BindingID == value.binding && item.RunID == value.runID {
			a.monitorActive[value.panelID] = false
		}
		return
	}
	a.monitorActive[value.panelID] = false
	if item == nil || (value.command != "" && item.Command != value.command) {
		// A panel may be reconfigured while its previous query is still running.
		// Release the running slot without publishing the old query's result.
		delete(a.monitorLast, value.panelID)
		if a.monitorWorkspaceVisible() {
			a.dirty = true
		}
		return
	}
	if value.err != nil && a.monitorPrimaryFallback && !a.monitorPrimaryPaused && monitor.CanRetryViaPrimary(value.err) {
		a.monitorViaPrimary = true
		// Retry connection/authentication failures through the authenticated shell,
		// instead of displaying and repeating an error that cannot succeed.
		a.monitorLast[value.panelID] = time.Time{}
		if a.monitorWorkspaceVisible() {
			a.dirty = true
		}
		return
	}
	a.monitorLast[value.panelID] = time.Now()
	if value.err != nil {
		a.panels.SetError(value.panelID, value.err)
	} else {
		a.panels.UpdateContent(value.panelID, value.content, a.t("实时"), false)
	}
	if a.monitorWorkspaceVisible() {
		a.dirty = true
	}
}

func bytesToRunes(data []byte) []rune {
	if utf8.Valid(data) {
		return []rune(string(data))
	}
	return []rune(string([]byte{data[0]}))
}
