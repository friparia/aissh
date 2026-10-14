package app

import (
	"fmt"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	app_i18n "git.example.com/ai/wrd-ai-cli/internal/i18n"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
	"git.example.com/ai/wrd-ai-cli/internal/resources"
)

func (a *App) startInlineAI(prompt, sourceLine string, locallyCaptured bool) {
	if a.config.DesktopUI {
		a.startAI(prompt, false)
		return
	}
	if a.inlineAI != nil {
		a.archiveInlineAI()
	}
	anchorX, anchorY := 0, 0
	if a.lineAnchored {
		anchorX, anchorY = a.lineAnchorX, a.lineAnchorY
	} else if a.virtual != nil {
		cursor := a.virtual.Cursor()
		anchorX, anchorY = cursor.X, cursor.Y
		if !locallyCaptured {
			anchorX -= textWidth(sourceLine)
			if anchorX < 0 {
				anchorX = 0
			}
		}
	}
	a.lineAnchored = false
	a.inlineAI = &inlineAIState{
		Prompt:  strings.TrimSpace(prompt),
		AnchorX: anchorX,
		AnchorY: anchorY,
		Status:  a.t("正在连接本机 %s", a.agent.Name()),
		Started: time.Now(),
	}
	a.focus = focusTerminal
	if a.agentBusy {
		a.inlineAI.Response = a.t("AI 正在处理上一个请求，请稍后重试。")
		a.inlineAI.Done = true
		a.inlineAI.Status = ""
		a.dirty = true
		return
	}
	a.startAI(prompt, false)
}

func (a *App) startAI(prompt string, foreground bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	if a.terminalTakeover {
		a.appendPersistentChat(chatMessage{Role: "system", Content: "请先在终端完成或退出审批，回到 Shell 提示符后再向 AI 提问。", Time: time.Now()})
		return
	}
	if a.monitorReview != nil || a.config.DesktopUI && (a.proposal != nil || a.execution != nil || a.executionRecovery) {
		a.chat = append(a.chat, chatMessage{Role: "system", Content: "请先处理当前命令建议或等待执行完成。", Time: time.Now()})
		return
	}
	if a.agentBusy {
		a.chat = append(a.chat, chatMessage{Role: "system", Content: a.t("AI 正在处理上一个请求。"), Time: time.Now()})
		return
	}
	if foreground && !a.config.DesktopUI && a.sidebarCollapsed && !a.compact {
		a.startInlineAI(prompt, prompt, true)
		return
	}
	if foreground && a.inlineAI != nil {
		a.archiveInlineAI()
	}
	if foreground {
		a.focus = focusAI
		a.aiExpanded = true
		a.agentInline = nil
	} else {
		a.agentInline = a.inlineAI
	}
	a.pipelineContinuations = 0
	a.pipelineFeedback = nil
	a.appendPersistentChat(chatMessage{Role: "user", Content: prompt, Time: time.Now()})
	a.aiScroll = 0
	a.agentBusy = true
	a.aiNotice = ""
	a.agentStatus = a.t("正在连接 AI")
	a.agentThought = ""
	a.agentLanguage = app_i18n.PreferredResponseLanguage(prompt, a.localizer.Language())
	a.proposal = nil
	a.dirty = true

	event := "start"
	if a.agentSessionID != "" {
		event = "user_message"
	}
	request := agent.Request{
		Event:        event,
		SessionID:    a.agentSessionID,
		Prompt:       prompt,
		RecentOutput: agent.LimitOutputContext(a.recentOutput.ContextString(), a.config.AIContextChars),
		SystemContext: map[string]string{
			"ui_language":       a.localizer.Language(),
			"response_language": a.agentLanguage,
		},
		Target: &agent.Target{
			Type:      "linux",
			HostLabel: a.parsed.Target,
			Protocol:  "ssh",
		},
	}
	if a.agentSessionID == "" && strings.TrimSpace(a.agentSeedContext) != "" {
		request.SystemContext["conversation_history"] = a.agentSeedContext
	}
	a.addPanelContext(&request)
	a.launchAgentStream(request, true)
}

// Keep an in-flight sidebar conversation visible when its pane is hidden.
// Reuse the existing request and streamed text so subsequent events update
// both surfaces without starting another AI turn.
func (a *App) mirrorActiveAIInline() {
	if !a.agentBusy || a.agentInline != nil || a.proposal != nil || a.execution != nil {
		return
	}
	var prompt, response string
	var started time.Time
	for index := len(a.chat) - 1; index >= 0; index-- {
		message := a.chat[index]
		if message.Role == "user" {
			prompt, started = message.Content, message.Time
			break
		}
		if message.Role == "assistant_stream" {
			response = message.Content + response
		}
	}
	if prompt == "" {
		return
	}
	if started.IsZero() {
		started = time.Now()
	}
	a.archiveInlineAI()
	state := &inlineAIState{Prompt: prompt, Response: response, Status: a.agentStatus, Started: started}
	if a.virtual != nil {
		cursor := a.virtual.Cursor()
		state.AnchorX, state.AnchorY = cursor.X, cursor.Y
	}
	a.inlineAI, a.agentInline = state, state
	a.terminalScroll = 0
	a.terminalRows = nil
}

func (a *App) launchAgentStream(request agent.Request, retrySession bool) {
	a.prepareAgentRequest(&request)
	op := a.beginAgentOperation()
	go a.runAgentStream(op, request, retrySession)
}

func (a *App) runAgentStream(op agentOperation, request agent.Request, retrySession bool) {
	callbacks := agent.Callbacks{
		OnStatus:   func(value string) { op.send(agentStatusEvent{status: value}) },
		OnThought:  func(value string) { op.send(agentThoughtEvent{content: value}) },
		OnAnswer:   func(value string) { op.send(agentAnswerEvent{content: value}) },
		OnActivity: func(value agent.Activity) { op.send(agentActivityEvent{activity: value}) },
		OnPanelAction: func(value agent.PanelAction) {
			op.send(agentPanelEvent{action: value})
		},
	}
	turn, err := a.agent.Stream(op.ctx, request, callbacks)
	if err != nil {
		err = fmt.Errorf("%s", a.safeText(err.Error()))
	}
	if err != nil && retrySession && request.SessionID != "" && strings.Contains(strings.ToLower(err.Error()), "session not found") {
		request.Event = "start"
		request.SessionID = ""
		turn, err = a.agent.Stream(op.ctx, request, callbacks)
	}
	op.send(agentTurnEvent{turn: turn, err: err})
}

func (a *App) appendAssistantDelta(content string) {
	index := a.assistantStreamIndex()
	if index < 0 {
		a.chat = append(a.chat, chatMessage{Role: "assistant_stream", Time: time.Now()})
		index = len(a.chat) - 1
	}
	a.chat[index].Content += content
}

func (a *App) finishAgentTurn(turn agent.Turn, err error) {
	defer a.notifyAgentTurn(turn, err)
	a.finishAgentActivities()
	inline := a.agentInline
	a.agentInline = nil
	a.agentBusy = false
	a.agentStatus = ""
	a.agentThought = ""
	streamedMessage := a.assistantStreamIndex()
	if streamedMessage >= 0 {
		a.chat[streamedMessage].Role = "assistant"
	}
	if err != nil {
		if streamedMessage >= 0 {
			a.persistChatMessage(a.chat[streamedMessage])
		}
		a.appendPersistentChat(chatMessage{Role: "error", Content: err.Error(), Time: time.Now()})
		if inline != nil {
			inline.Error = err.Error()
			inline.Response = err.Error()
			inline.Done = true
			inline.Status = ""
			a.focus = focusTerminal
		} else {
			a.aiExpanded = true
		}
		a.dirty = true
		return
	}
	if turn.Session.ID != "" {
		a.agentSessionID = turn.Session.ID
		a.rememberCodexThread(turn.Session.ID)
		a.agentSeedContext = ""
	}
	if turn.Message != "" {
		if streamedMessage >= 0 {
			a.chat[streamedMessage].Content = turn.Message
		} else {
			a.appendPersistentChat(chatMessage{Role: "assistant", Content: turn.Message, Time: time.Now()})
		}
		if inline != nil {
			inline.Response = turn.Message
		}
	}
	if streamedMessage >= 0 {
		a.persistChatMessage(a.chat[streamedMessage])
	}
	if turn.Error != "" {
		a.appendPersistentChat(chatMessage{Role: "error", Content: turn.Error, Time: time.Now()})
		if inline != nil {
			inline.Error = turn.Error
			if strings.TrimSpace(inline.Response) == "" {
				inline.Response = turn.Error
			}
		}
	}
	a.proposal = turn.Proposal
	a.approvalCommandExpanded = false
	if turn.Proposal != nil {
		if inline != nil {
			a.reanchorInlineForReview(inline)
			proposalCopy := *turn.Proposal
			inline.Proposal = &proposalCopy
			inline.CommandExpanded = false
		}
		a.appendPersistentChat(chatMessage{
			Role:     "proposal",
			Content:  a.proposalConversationContent(*turn.Proposal),
			Time:     time.Now(),
			Proposal: turn.Proposal,
			Activity: &agent.Activity{ID: "ssh:" + turn.Proposal.ID, Kind: "ssh", Title: turn.Proposal.Command, Status: "pending"},
		})
		a.approvalInput = nil
		a.approvalStatus = ""
		a.terminalScroll = 0
		a.terminalRows = nil
		if !a.atPrompt || a.sensitive || a.execution != nil || a.executionRecovery {
			a.approvalStatus = a.t("当前不在安全的 Shell 提示符；可输入 no 拒绝，或稍后再确认")
		}
		a.focus = focusApproval
		if isReviewRisk(turn.Proposal.Risk) {
			a.aiExpanded = true
		}
		if inline != nil {
			inline.Done = true
			inline.Status = ""
		}
		if a.resourceProposal(turn.Proposal.Command) {
			a.dirty = true
			if a.config.Resources != nil {
				op, err := resources.Parse(strings.TrimSpace(turn.Proposal.Command))
				if err == nil {
					needs, checkErr := a.config.Resources.RequiresApproval(op)
					if summary := a.config.Resources.ApprovalSummary(op); summary != "" {
						a.approvalStatus = summary
					}
					if checkErr != nil {
						a.approvalStatus = checkErr.Error()
					}
					if checkErr == nil && !needs && a.atPrompt && !a.sensitive && !isReviewRisk(turn.Proposal.Risk) {
						a.executeResource(turn.Proposal.Command, turn.Proposal.ID, a.agentSessionID, false)
						return
					}
				}
			}
			return // Credential grants are independent of --always-yes.
		}
		if a.config.AlwaysYes && a.atPrompt && !a.sensitive {
			if risk, known := agent.NormalizeRisk(turn.Proposal.Risk); !known {
				a.approvalStatus = a.t("风险等级未知，always-yes 未自动批准；请手动确认")
			} else if isReviewRisk(risk) {
				a.approvalStatus = a.t("高风险命令不会由 always-yes 自动批准；请手动确认")
			} else {
				a.startAlwaysYesRiskAssessment(*turn.Proposal)
			}
		}
	} else if turn.Type == "ask_user" || turn.Type == "failed" || turn.Error != "" {
		if inline == nil {
			a.aiExpanded = true
		}
	}
	if inline != nil && turn.Proposal == nil {
		inline.Done = true
		inline.Status = ""
		a.focus = focusTerminal
	}
	a.dirty = true
}

func (a *App) startAlwaysYesRiskAssessment(proposal agent.Proposal) {
	assessor, ok := a.agent.(agent.RiskAssessor)
	if !ok {
		a.approvalStatus = a.t("当前 AI 后端不支持独立风险复核，always-yes 未自动批准；请手动确认")
		return
	}
	a.agentBusy = true
	a.agentStatus = a.t("always-yes 正在进行风险复核（%d/%d）", 1, AlwaysYesRiskAssessmentPasses)
	a.approvalStatus = a.agentStatus
	request := agent.RiskAssessmentRequest{
		Proposal:     proposal,
		RecentOutput: a.safeText(agent.LimitOutputContext(a.recentOutput.ContextString(), a.config.AIContextChars)),
		Target: &agent.Target{
			Type:      "linux",
			HostLabel: a.parsed.Target,
			Protocol:  "ssh",
		},
	}
	if a.safeText(proposal.Command) != proposal.Command {
		a.agentBusy = false
		a.approvalStatus = "命令包含已保存的凭据，已阻止自动执行；请改用系统连接操作"
		return
	}
	request.Proposal.Explanation = a.safeText(request.Proposal.Explanation)
	request.Proposal.ExpectedEffect = a.safeText(request.Proposal.ExpectedEffect)
	op := a.beginAgentOperation()
	go func() {
		assessments := make([]agent.RiskAssessment, 0, AlwaysYesRiskAssessmentPasses)
		for pass := 1; pass <= AlwaysYesRiskAssessmentPasses; pass++ {
			if pass > 1 {
				op.send(alwaysYesRiskProgressEvent{pass: pass, total: AlwaysYesRiskAssessmentPasses})
			}
			assessment, err := assessor.AssessRisk(op.ctx, request)
			if err != nil {
				op.send(alwaysYesRiskEvent{proposalID: proposal.ID, assessments: assessments, err: err})
				return
			}
			assessments = append(assessments, assessment)
		}
		op.send(alwaysYesRiskEvent{proposalID: proposal.ID, assessments: assessments})
	}()
}

func (a *App) finishAlwaysYesRiskAssessment(event alwaysYesRiskEvent) {
	defer func() {
		if a.proposal != nil && a.proposal.ID == event.proposalID {
			a.notifyAgentTurn(agent.Turn{Proposal: a.proposal}, nil)
		}
	}()
	a.agentBusy = false
	a.agentStatus = ""
	if a.proposal == nil || a.proposal.ID != event.proposalID {
		return
	}
	if event.err != nil {
		a.approvalStatus = a.t("always-yes 风险复核失败，未自动批准；请手动确认：%s", event.err.Error())
		a.focus = focusApproval
		a.aiExpanded = true
		a.dirty = true
		return
	}

	highest, known := agent.NormalizeRisk(a.proposal.Risk)
	for _, assessment := range event.assessments {
		risk, valid := agent.NormalizeRisk(assessment.Risk)
		if !valid {
			known = false
			break
		}
		if riskRank(risk) > riskRank(highest) {
			highest = risk
		}
	}
	if !known || len(event.assessments) != AlwaysYesRiskAssessmentPasses {
		a.approvalStatus = a.t("always-yes 风险复核结果不完整，未自动批准；请手动确认")
		a.focus = focusApproval
		a.aiExpanded = true
		a.dirty = true
		return
	}
	a.proposal.Risk = highest
	if a.inlineAI != nil && a.inlineAI.Proposal != nil && a.inlineAI.Proposal.ID == event.proposalID {
		a.inlineAI.Proposal.Risk = highest
	}
	if isReviewRisk(highest) {
		a.approvalStatus = a.t("多次风险复核判定为高风险，always-yes 未自动批准；请手动确认")
		a.focus = focusApproval
		a.aiExpanded = true
		a.dirty = true
		return
	}

	a.approvalStatus = ""
	a.appendPersistentChat(chatMessage{
		Role:    "system",
		Content: a.t("always-yes 已通过 %d 次独立风险复核，正在自动批准命令。", len(event.assessments)),
		Time:    time.Now(),
	})
	a.approveProposal()
}

func riskRank(value string) int {
	risk, ok := agent.NormalizeRisk(value)
	if !ok {
		return -1
	}
	switch risk {
	case "read_only":
		return 0
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	default:
		return -1
	}
}

func (a *App) reanchorInlineForReview(state *inlineAIState) {
	if state == nil || a.virtual == nil || (state.AnchorY >= 0 && state.AnchorY < a.contentRows) {
		return
	}
	cursor := a.virtual.Cursor()
	state.AnchorX = cursor.X
	state.AnchorY = cursor.Y
}

func (a *App) approveProposal() {
	if a.proposal == nil || a.agentBusy {
		return
	}
	if !a.atPrompt || a.sensitive || a.execution != nil || a.executionRecovery || (a.config.DesktopUI && !a.desktopCanApprove()) {
		a.approvalStatus = a.t("当前不在安全的 Shell 提示符，暂时不能执行；请输入 no 拒绝")
		return
	}
	if a.monitorReview != nil {
		a.decideMonitorAudit(true)
		return
	}
	proposal := *a.proposal
	a.agentBusy = true
	a.agentStatus = a.t("正在批准命令")
	a.approvalStatus = a.t("正在确认并准备写入 SSH……")
	op := a.beginAgentOperation()
	sessionID := a.agentSessionID
	go func() {
		turn, err := a.agent.Action(op.ctx, agent.Request{
			Event:      "approve",
			SessionID:  sessionID,
			ProposalID: proposal.ID,
		})
		op.send(approvalEvent{turn: turn, command: proposal.Command, err: err})
	}()
}

func (a *App) finishApproval(event approvalEvent) {
	a.agentBusy = false
	a.agentStatus = ""
	marker := ""
	if event.err == nil && (!a.atPrompt || a.sensitive || a.execution != nil || a.executionRecovery || (a.config.DesktopUI && !a.desktopCanApprove())) {
		event.err = fmt.Errorf("%s", a.t("当前不在安全的 Shell 提示符，暂时不能执行；请输入 no 拒绝"))
	}
	if event.err == nil && a.resourceProposal(event.command) {
		proposalID := ""
		if a.proposal != nil {
			proposalID = a.proposal.ID
		}
		a.appendCommandDecision(chatMessage{Role: "user", Content: "已批准系统操作：" + event.command, Time: time.Now()})
		a.executeResource(event.command, proposalID, a.agentSessionID, true)
		return
	}
	if event.err == nil {
		marker, event.err = newExecutionMarker()
	}
	if event.err != nil {
		a.appendPersistentChat(chatMessage{Role: "error", Content: a.t("批准失败：%s", event.err.Error()), Time: time.Now()})
		a.approvalStatus = a.t("批准失败：%s", event.err.Error())
		a.focus = focusApproval
		a.aiExpanded = true
		a.dirty = true
		return
	}
	if event.turn.Session.ID != "" {
		a.agentSessionID = event.turn.Session.ID
	}
	a.appendCommandDecision(chatMessage{Role: "user", Content: a.t("已批准执行命令：%s", event.command), Time: time.Now()})
	inlineExecution := a.inlineAI != nil
	if a.inlineAI != nil {
		a.inlineAI.ApprovalResult = a.t("已批准执行：%s", event.command)
		a.inlineAI.Done = true
		a.inlineAI.Status = ""
		a.archiveInlineAI()
	}
	proposalID := ""
	if a.proposal != nil {
		proposalID = a.proposal.ID
	}
	a.updateSSHActivity(proposalID, "running", "")
	a.execution = &executionState{
		SessionID:  a.agentSessionID,
		ProposalID: proposalID,
		Command:    event.command,
		Output:     newContextBuffer(a.config.AIContextChars),
		StartedAt:  time.Now(),
		Inline:     inlineExecution,
		Marker:     marker,
	}
	a.beginCommandOutput(event.command)
	a.proposal = nil
	a.approvalInput = nil
	a.approvalStatus = ""
	a.lastCommand = event.command
	a.atPrompt = false
	a.pendingError = nil
	a.focus = focusTerminal
	a.sendApprovedCommand(a.execution)
	a.dirty = true
}

func (a *App) rejectProposal() {
	if a.proposal == nil || a.agentBusy {
		return
	}
	if a.monitorReview != nil {
		a.decideMonitorAudit(false)
		return
	}
	proposal := *a.proposal
	a.agentBusy = true
	a.agentStatus = a.t("正在拒绝命令")
	a.approvalStatus = a.t("正在拒绝命令建议……")
	op := a.beginAgentOperation()
	sessionID := a.agentSessionID
	go func() {
		turn, err := a.agent.Action(op.ctx, agent.Request{
			Event:      "reject",
			SessionID:  sessionID,
			ProposalID: proposal.ID,
		})
		op.send(rejectionEvent{turn: turn, command: proposal.Command, err: err})
	}()
}

func (a *App) finishRejection(event rejectionEvent) {
	if event.err == nil && a.proposal != nil {
		a.updateSSHActivity(a.proposal.ID, "rejected", "")
	}
	if a.inlineAI != nil && event.err == nil {
		a.inlineAI.ApprovalResult = a.t("已拒绝命令建议：%s", event.command)
		a.inlineAI.Done = true
		a.inlineAI.Status = ""
	}
	a.proposal = nil
	a.approvalInput = nil
	a.approvalStatus = ""
	a.agentBusy = false
	a.agentStatus = ""
	a.focus = focusTerminal
	if event.err != nil {
		if a.inlineAI != nil {
			a.inlineAI.ApprovalResult = a.t("拒绝失败：%s", event.err.Error())
			a.inlineAI.Done = true
			a.inlineAI.Status = ""
		}
		a.appendPersistentChat(chatMessage{Role: "error", Content: a.t("拒绝失败：%s", event.err.Error()), Time: time.Now()})
	} else {
		a.appendCommandDecision(chatMessage{Role: "user", Content: a.t("已拒绝命令建议：%s", event.command), Time: time.Now()})
		a.finishAgentTurn(event.turn, nil)
	}
	a.dirty = true
}

func (a *App) archiveInlineAI() {
	if a.inlineAI == nil {
		return
	}
	a.inlineHistory = append(a.inlineHistory, a.inlineAI)
	if len(a.inlineHistory) > 32 {
		a.inlineHistory = append([]*inlineAIState(nil), a.inlineHistory[len(a.inlineHistory)-32:]...)
	}
	a.inlineAI = nil
}

func (a *App) proposalConversationContent(proposal agent.Proposal) string {
	lines := []string{firstText(proposal.Explanation, proposal.ExpectedEffect, a.t("已生成命令建议，请在左侧 SSH 窗口确认。"))}
	if strings.TrimSpace(proposal.Command) != "" {
		lines = append(lines, a.t("命令：%s", proposal.Command))
	}
	if strings.TrimSpace(proposal.Risk) != "" {
		lines = append(lines, a.t("风险：%s", a.riskDisplay(proposal.Risk)))
	}
	return strings.Join(lines, "\n")
}

func (a *App) submitExecutionResult() {
	execution := a.execution
	if execution == nil || !execution.finished() || execution.sending() || a.executionRecovery || a.agentBusy {
		return
	}
	a.finishSSHActivity(execution)
	a.execution = nil
	a.retireExecution(execution)
	if !a.config.DesktopUI && (execution.Inline || !a.sidebarVisible()) {
		a.startInlineExecutionAnalysis(execution.Command)
	}
	a.agentBusy = true
	a.aiNotice = ""
	a.agentStatus = a.t("AI 正在分析执行结果")
	output := ""
	if execution.Output != nil {
		output = agent.LimitOutputContext(execution.Output.ContextString(), a.config.AIContextChars)
	}
	request := agent.Request{
		Event:      "result",
		SessionID:  execution.SessionID,
		ProposalID: execution.ProposalID,
		Output:     output,
		Completion: execution.completionDescription(),
		SystemContext: map[string]string{
			"ui_language":       a.localizer.Language(),
			"response_language": firstText(a.agentLanguage, a.localizer.Language()),
		},
	}
	a.addPanelContext(&request)
	a.prepareAgentRequest(&request)
	op := a.beginAgentOperation()
	go func() {
		callbacks := agent.Callbacks{
			OnStatus:   func(value string) { op.send(agentStatusEvent{status: value}) },
			OnThought:  func(value string) { op.send(agentThoughtEvent{content: value}) },
			OnAnswer:   func(value string) { op.send(agentAnswerEvent{content: value}) },
			OnActivity: func(value agent.Activity) { op.send(agentActivityEvent{activity: value}) },
			OnPanelAction: func(value agent.PanelAction) {
				op.send(agentPanelEvent{action: value})
			},
		}
		turn, err := a.agent.Stream(op.ctx, request, callbacks)
		op.send(executionResultEvent{turn: turn, err: err})
	}()
}

func (a *App) startInlineExecutionAnalysis(command string) {
	if a.inlineAI != nil {
		a.archiveInlineAI()
	}
	anchorX, anchorY := 0, 0
	if a.virtual != nil {
		cursor := a.virtual.Cursor()
		anchorX, anchorY = cursor.X, cursor.Y
	}
	a.inlineAI = &inlineAIState{
		Prompt:         a.t("AI 复核命令：%s", strings.TrimSpace(command)),
		AnchorX:        anchorX,
		AnchorY:        anchorY,
		AwaitingPrompt: !a.atPrompt,
		Status:         a.t("正在分析命令执行结果"),
		StatusPrefix:   a.t("分析命令结果"),
		Started:        time.Now(),
	}
	a.agentInline = a.inlineAI
	a.focus = focusTerminal
}

func (a *App) finishExecutionResult(event executionResultEvent) {
	a.finishAgentTurn(event.turn, event.err)
}

func panelKind(value string) panel.Kind {
	switch strings.ToLower(value) {
	case "log":
		return panel.KindLog
	case "table":
		return panel.KindTable
	case "report":
		return panel.KindReport
	case "shell":
		return panel.KindShell
	default:
		return panel.KindMonitor
	}
}

func collectorCommand(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "system", "system.overview", "system.cpu", "system.memory", "system.load":
		return panel.BuiltinOverviewCommand
	case "process", "process.top":
		return panel.BuiltinProcessCommand
	case "disk", "disk.usage", "disk.io":
		return panel.BuiltinDiskCommand
	case "network", "network.traffic", "network.connections", "network.sockets":
		return panel.BuiltinNetworkCommand
	case "service", "service.status":
		return panel.BuiltinServiceCommand
	case "nginx.errors":
		return `tail -n 80 /var/log/nginx/error.log 2>/dev/null`
	default:
		return ""
	}
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isReviewRisk(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high", "critical", "destructive", "dangerous":
		return true
	default:
		return false
	}
}
