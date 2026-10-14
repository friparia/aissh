package app

import (
	"fmt"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/history"
)

func (a *App) resumeConversation(id string) error {
	if a.historyStore == nil {
		return fmt.Errorf("AI history is disabled")
	}
	// Fail explicitly instead of silently continuing with an unrelated context.
	conversations, err := a.historyStore.LoadConversations(a.historyTarget)
	if err != nil {
		return err
	}
	a.historyConversations = conversations
	for index, conversation := range conversations {
		if conversation.ID == id && a.selectHistoryConversation(index+1) {
			return nil
		}
	}
	return fmt.Errorf("AI conversation %q was not found for this server", id)
}

func (a *App) notifyConversationChanged() {
	if a.config.ConversationChanged != nil {
		a.config.ConversationChanged(a.historyConversationID)
	}
}

func (a *App) loadChatHistory() {
	if a.historyStore == nil {
		return
	}
	conversations, err := a.historyStore.LoadConversations(a.historyTarget)
	if err != nil {
		a.chat = append(a.chat, chatMessage{
			Role:    "system",
			Content: a.t("本机 AI 历史读取失败：%s", err.Error()),
			Time:    time.Now(),
		})
		return
	}
	a.historyConversations = conversations
	if len(conversations) > 0 {
		a.chat = append(a.chat, chatMessage{
			Role:    "system",
			Content: a.t("发现 %d 个本机 AI 历史会话 · 在 AI 对话中按 Ctrl+R 查看", len(conversations)),
			Time:    time.Now(),
		})
	}
}

func (a *App) appendAIReadyMessages() {
	if a.config.Workspace != "" {
		message := a.t("本机代码工作区（只读）：%s", a.config.Workspace)
		if a.config.WorkspaceWritable {
			message = a.t("本机代码工作区（可修改）：%s", a.config.Workspace)
		}
		a.chat = append(a.chat, chatMessage{
			Role:    "system",
			Content: message,
			Time:    time.Now(),
		})
	}
	mode := agentMode(a.config)
	if mode == "deepseek" || mode == "model" || mode == "openai-compatible" {
		a.chat = append(a.chat, chatMessage{
			Role:    "system",
			Content: a.t("直连模型模式：AI 提问和相关 SSH 输出会发送到 %s；本机代码工作区不会发送。", a.config.ModelURL),
			Time:    time.Now(),
		})
	}
	if a.config.AlwaysYes {
		a.chat = append(a.chat, chatMessage{
			Role:    "system",
			Content: a.t("always-yes 已开启：非高风险命令经 %d 次独立复核后自动执行；高风险、未知或复核失败仍需手动确认。", AlwaysYesRiskAssessmentPasses),
			Time:    time.Now(),
		})
	}
	a.chat = append(a.chat, chatMessage{
		Role:    "system",
		Content: a.t("AI 已就绪（%s）。中文开头或 /ai 会自动提问。", a.agent.Name()),
		Time:    time.Now(),
	})
}

func (a *App) refreshHistoryConversations() bool {
	if a.historyStore == nil {
		a.historyConversations = nil
		return true
	}
	conversations, err := a.historyStore.LoadConversations(a.historyTarget)
	if err != nil {
		a.chat = append(a.chat, chatMessage{Role: "system", Content: a.t("本机 AI 历史读取失败：%s", err.Error()), Time: time.Now()})
		return false
	}
	a.historyConversations = conversations
	return true
}

func (a *App) openAIHistory() bool {
	if a.agentBusy || a.proposal != nil {
		return false
	}
	if !a.refreshHistoryConversations() {
		return false
	}
	a.historySelection = 0
	for index, conversation := range a.historyConversations {
		if conversation.ID == a.historyConversationID {
			a.historySelection = index + 1
			break
		}
	}
	a.focus = focusAIHistory
	a.aiExpanded = true
	return true
}

func (a *App) selectHistoryConversation(index int) bool {
	if index < 0 || index > len(a.historyConversations) || a.agentBusy || a.proposal != nil {
		return false
	}
	if index == 0 {
		a.startNewAIConversation()
		return true
	}
	conversation := a.historyConversations[index-1]
	if provider, ok := a.agent.(localWorkspaceAgent); ok && conversation.Workspace != nil {
		workspace, err := resolveLocalWorkspace(*conversation.Workspace)
		if err != nil {
			a.chat = append(a.chat, chatMessage{Role: "error", Content: a.t("无法访问本机代码工作区：%s", err.Error()), Time: time.Now()})
			return false
		}
		a.setLocalWorkspace(provider, workspace)
	}
	keepRuntimeSession := conversation.ID == a.historyConversationID && a.agentSessionID != ""
	messages := make([]chatMessage, 0, len(conversation.Messages)+1)
	for _, message := range conversation.Messages {
		createdAt, _ := time.Parse(time.RFC3339Nano, message.Time)
		messages = append(messages, chatMessage{
			Role:       message.Role,
			Content:    message.Content,
			Time:       createdAt,
			Proposal:   message.Proposal,
			Historical: true,
		})
	}
	a.chat = messages
	a.historyConversationID = conversation.ID
	a.codexThreadID = conversation.CodexThreadID
	if keepRuntimeSession {
		a.agentSeedContext = ""
	} else {
		a.agentSessionID = ""
		a.panelActionResults = nil
		a.agentSeedContext = historyConversationContext(conversation.Messages)
	}
	a.proposal = nil
	a.approvalCommandExpanded = false
	a.execution = nil
	a.editor = nil
	a.aiEditorCursor = 0
	a.aiPasteMode = false
	a.aiScroll = 0
	a.focus = focusAI
	a.aiExpanded = true
	a.chat = append(a.chat, chatMessage{
		Role:    "system",
		Content: a.t("已切换到历史会话：%s；后续提问会延续该会话上下文。", conversation.Title),
		Time:    time.Now(),
	})
	a.notifyConversationChanged()
	return true
}

func (a *App) startNewAIConversation() {
	a.historyConversationID = history.NewConversationID()
	a.codexThreadID = ""
	a.agentSessionID = ""
	a.agentSeedContext = ""
	a.panelActionResults = nil
	a.proposal = nil
	a.approvalCommandExpanded = false
	a.execution = nil
	a.editor = nil
	a.aiEditorCursor = 0
	a.aiPasteMode = false
	a.aiScroll = 0
	a.chat = nil
	a.appendAIReadyMessages()
	a.chat = append(a.chat, chatMessage{Role: "system", Content: a.t("已新建 AI 会话。"), Time: time.Now()})
	a.focus = focusAI
	a.aiExpanded = true
	a.notifyConversationChanged()
}

func historyConversationContext(messages []history.Message) string {
	const maximum = 24000
	parts := make([]string, 0, len(messages))
	total := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		content := strings.TrimSpace(message.Content)
		if content == "" || (message.Role != "user" && message.Role != "assistant" && message.Role != "proposal") {
			continue
		}
		label := map[string]string{"user": "用户", "assistant": "助手", "proposal": "命令建议"}[message.Role]
		part := fmt.Sprintf("%s：%s", label, content)
		if total+len(part) > maximum && len(parts) > 0 {
			break
		}
		parts = append(parts, part)
		total += len(part)
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return strings.Join(parts, "\n\n")
}

func (a *App) appendPersistentChat(message chatMessage) {
	a.chat = append(a.chat, message)
	a.persistChatMessage(message)
}

func (a *App) persistChatMessage(message chatMessage) {
	message.Content = a.safeText(message.Content)
	if message.Proposal != nil {
		copy := *message.Proposal
		copy.Command = a.safeText(copy.Command)
		copy.Explanation = a.safeText(copy.Explanation)
		copy.ExpectedEffect = a.safeText(copy.ExpectedEffect)
		message.Proposal = &copy
	}
	if a.historyStore == nil || message.Historical || message.Role == "assistant_stream" || strings.TrimSpace(message.Content) == "" {
		return
	}
	if a.historyConversationID == "" {
		a.historyConversationID = history.NewConversationID()
	}
	var workspace *string
	if provider, ok := a.agent.(localWorkspaceAgent); ok {
		value := provider.LocalWorkspace()
		workspace = &value
	}
	err := a.historyStore.Append(a.historyTarget, history.Message{
		AIModel:        a.config.ModelName,
		AIProfileID:    a.config.AIProfileID,
		ConversationID: a.historyConversationID,
		CodexThreadID:  a.codexThreadID,
		Workspace:      workspace,
		Role:           message.Role,
		Content:        message.Content,
		Time:           message.Time.Format(time.RFC3339Nano),
		Proposal:       message.Proposal,
	})
	if err == nil || a.historyWarned {
		return
	}
	a.historyWarned = true
	a.chat = append(a.chat, chatMessage{
		Role:    "system",
		Content: a.t("本机 AI 历史写入失败：%s", err.Error()),
		Time:    time.Now(),
	})
}

// Save the recovery command even for proposal-only responses with no answer text.
func (a *App) rememberCodexThread(id string) {
	if provider, ok := a.agent.(*agent.CodexProvider); !ok || !provider.KeepSession || id == "" || id == a.codexThreadID {
		return
	}
	a.codexThreadID = id
	a.appendPersistentChat(chatMessage{Role: "system", Content: "本地恢复 Codex：codex resume " + id, Time: time.Now()})
}
