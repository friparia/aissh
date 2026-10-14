package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
	"git.example.com/ai/wrd-ai-cli/internal/policy"
	"git.example.com/ai/wrd-ai-cli/internal/widgets"
)

type desktopCommandEvent struct {
	command credentialui.WorkbenchCommand
	reply   chan error
}

// All app state is copied on the event loop. Slow desktop IPC cannot stall SSH.
func (a *App) publishDesktopState() {
	if a.desktopStates == nil {
		return
	}
	state := a.desktopState()
	select {
	case <-a.desktopStates:
	default:
	}
	a.desktopStates <- state
}

func (a *App) desktopState() credentialui.WorkbenchState {
	state := credentialui.WorkbenchState{
		TerminalTakeover:     a.terminalTakeover,
		CanCollectOutput:     a.canCollectExecutionOutput(),
		SystemCollection:     a.config.SystemCollection,
		BastionCompatibility: a.config.BastionCompatibility,
		Notices:              append([]credentialui.WorkbenchNotice(nil), a.desktopNotices...),
		CodexThreadID:        a.codexThreadID,
		ConversationID:       a.historyConversationID, Busy: a.agentBusy,
		Status: a.safeText(a.agentStatus), Executing: a.execution != nil || a.executionRecovery,
		CanApprove: a.desktopCanApprove(), ApprovalStatus: a.safeText(a.approvalStatus),
		Messages: []credentialui.WorkbenchMessage{}, Panels: []credentialui.MonitorPanel{},
		AlertMonitors: []credentialui.MonitorPanel{},
	}
	if provider, ok := a.agent.(*agent.DirectModelProvider); ok && agentMode(a.config) == "model" {
		state.Model = provider.CurrentModel()
		state.ModelOptions = append([]string(nil), a.config.ModelOptions...)
	}
	if provider, ok := a.agent.(localWorkspaceAgent); ok {
		state.WorkspaceSupported = true
		state.Workspace = provider.LocalWorkspace()
		state.WorkspaceWritable = a.config.WorkspaceWritable
	}
	if a.proposal != nil {
		p := *a.proposal
		p.Command, p.Explanation, p.ExpectedEffect = a.safeText(p.Command), a.safeText(p.Explanation), a.safeText(p.ExpectedEffect)
		state.Proposal = &credentialui.WorkbenchProposal{ID: p.ID, Command: p.Command, Explanation: p.Explanation, ExpectedEffect: p.ExpectedEffect, Risk: p.Risk}
	}
	start := max(0, len(a.chat)-100)
	for _, m := range a.chat[start:] {
		var proposal *credentialui.WorkbenchProposal
		if m.Proposal != nil {
			p := m.Proposal
			proposal = &credentialui.WorkbenchProposal{ID: p.ID, Command: a.safeText(p.Command), Explanation: a.safeText(p.Explanation), ExpectedEffect: a.safeText(p.ExpectedEffect), Risk: p.Risk}
		}
		var activity *credentialui.WorkbenchActivity
		if m.Activity != nil {
			copy := *m.Activity
			if a.execution != nil && copy.ID == "ssh:"+a.execution.ProposalID {
				copy.Status = "running"
				if a.executionRecovery {
					copy.Status = "recovering"
				}
				if a.execution.Output != nil {
					copy.Detail = a.execution.Output.ContextString()
				}
			}
			copy.Title, copy.Detail = a.safeText(copy.Title), desktopText(a.safeText(copy.Detail))
			activity = &credentialui.WorkbenchActivity{ID: copy.ID, Kind: copy.Kind, Title: copy.Title, Status: copy.Status, Detail: copy.Detail}
		}
		state.Messages = append(state.Messages, credentialui.WorkbenchMessage{
			Proposal: proposal, Activity: activity,
			Role: m.Role, Content: desktopText(a.safeText(m.Content)), Time: m.Time.UTC().Format(time.RFC3339Nano),
		})
	}
	if a.panels != nil {
		for _, p := range a.panels.Panels() {
			var alert *panel.AlertCondition
			if p.Alert != nil {
				copy := *p.Alert
				copy.Pattern = a.safeText(copy.Pattern)
				alert = &copy
			}
			entry := credentialui.MonitorPanel{
				Command: a.safeText(p.Command), Origin: string(p.Origin), Collecting: a.monitorActive[p.ID],
				Alert: alert, SampleContent: desktopText(a.safeText(p.SampleContent)), SourceRevision: p.SourceRevision,
				ID: p.ID, Title: a.safeText(p.Title), Kind: string(p.Kind), Content: desktopText(a.safeText(p.Content)),
				Status: a.safeText(p.Status), Error: a.safeText(p.LastError), UpdatedAt: p.LastUpdated.UTC().Format(time.RFC3339Nano),
				IntervalMS: p.Interval.Milliseconds(), Pinned: p.Pinned,
			}
			if p.Pipeline != nil {
				entry.Command = desktopText(a.safeText(pipelineCommandPreview(p.Pipeline.Sources)))
			}
			if p.Widget != nil {
				entry.Widget = &widgets.Meta{Hash: p.Widget.Hash, Revision: p.DefinitionRevision, BindingID: p.BindingID, Sequence: p.WidgetData.Sequence}
				if !a.pipelineSaveSafe(p) {
					entry.Widget.SaveError = "采集命令包含凭据，本次会话运行但不保存该定义"
				}
			}
			if p.IsAlert() {
				state.AlertMonitors = append(state.AlertMonitors, entry)
			} else {
				state.Panels = append(state.Panels, entry)
			}
		}
	}

	state.WidgetTransfer = widgets.Transfer{Data: map[string]widgets.Data{}, Definitions: map[string]widgets.Definition{}}
	if a.panels != nil {
		for _, p := range a.panels.Panels() {
			if p.Widget != nil && (p.DataContract != nil || p.Pipeline != nil) {
				state.WidgetTransfer.Artifacts = append(state.WidgetTransfer.Artifacts, *p.Widget)
				state.WidgetTransfer.Data[p.ID] = widgets.Redact(p.WidgetData, a.safeText)
				if a.pipelineSaveSafe(p) {
					contract := widgets.Contract{}
					if p.DataContract != nil {
						contract = *p.DataContract
					}
					state.WidgetTransfer.Definitions[p.ID] = widgets.Definition{Pipeline: p.Pipeline, ID: p.ID, Title: p.Title, Command: p.Command, IntervalMS: p.Interval.Milliseconds(), Contract: contract, View: p.Widget.View, Revision: p.DefinitionRevision, AllowAIRead: p.AllowAIRead}
				}
			}
		}
	}
	for _, c := range a.widgetCandidates {
		if c.artifact != nil && !c.pipelineRefreshing {
			sample := (*widgets.Data)(nil)
			version := 0
			if c.panel.WidgetData.Sequence > 0 || c.requiresSample {
				data := widgets.Redact(c.panel.WidgetData, a.safeText)
				sample = &data
				version = c.artifact.View.SDKVersion
			}
			state.WidgetCandidates = append(state.WidgetCandidates, widgets.Candidate{Sample: sample, ProtocolVersion: version, Contract: c.panel.DataContract, ID: c.id, PanelID: c.panel.ID, Title: c.panel.Title, Hash: c.artifact.Hash, ExpiresAt: c.expires.UTC().Format(time.RFC3339Nano)})
			state.WidgetTransfer.Artifacts = append(state.WidgetTransfer.Artifacts, *c.artifact)
		}
	}
	return state
}

func desktopText(s string) string {
	runes := []rune(s)
	if len(runes) > 16000 {
		return string(runes[:16000]) + "\n…（内容较长，已截断显示）"
	}
	return s
}

func (a *App) runDesktopBridge() {
	var results []credentialui.CommandResult
	sentArtifacts := map[string]bool{}
	sentData := map[string]string{}
	sentDefinitions := map[string]string{}
	for {
		var state credentialui.WorkbenchState
		select {
		case <-a.ctx.Done():
			return
		case state = <-a.desktopStates:
		}
		transfer := state.WidgetTransfer
		filtered := widgets.Transfer{Data: map[string]widgets.Data{}, Definitions: map[string]widgets.Definition{}}
		for _, v := range transfer.Artifacts {
			if !sentArtifacts[v.Hash] {
				filtered.Artifacts = append(filtered.Artifacts, v)
			}
		}
		for id, v := range transfer.Data {
			key := fmt.Sprintf("%s:%d:%d", v.BindingID, v.SourceRevision, v.Sequence)
			if sentData[id] != key {
				filtered.Data[id] = v
			}
		}
		for id, v := range transfer.Definitions {
			if sentDefinitions[id] != fmt.Sprintf("%s:%d", transfer.Data[id].BindingID, v.Revision) {
				filtered.Definitions[id] = v
			}
		}
		state.WidgetTransfer = filtered
		commands, err := credentialui.ExchangeWorkbench(a.ctx, state, results)
		if err != nil {
			continue
		}

		for _, v := range filtered.Artifacts {
			sentArtifacts[v.Hash] = true
		}
		for id, v := range filtered.Data {
			sentData[id] = fmt.Sprintf("%s:%d:%d", v.BindingID, v.SourceRevision, v.Sequence)
		}
		for id, v := range filtered.Definitions {
			sentDefinitions[id] = fmt.Sprintf("%s:%d", transfer.Data[id].BindingID, v.Revision)
		}
		// Keep de-duplication bounded to this snapshot's live/candidate references.
		current := map[string]bool{}
		for _, v := range transfer.Artifacts {
			current[v.Hash] = true
		}
		for hash := range sentArtifacts {
			if !current[hash] {
				delete(sentArtifacts, hash)
			}
		}
		for id := range sentDefinitions {
			if _, ok := transfer.Definitions[id]; !ok {
				delete(sentDefinitions, id)
				delete(sentData, id)
			}
		}
		results = nil
		for _, command := range commands {
			reply := make(chan error, 1)
			select {
			case <-a.ctx.Done():
				return
			case a.events <- desktopCommandEvent{command, reply}:
			}
			select {
			case <-a.ctx.Done():
				return
			case err := <-reply:
				result := credentialui.CommandResult{ID: command.ID}
				if err != nil {
					result.Error = err.Error()
				}
				results = append(results, result)
			}
		}
	}
}

func (a *App) desktopCanApprove() bool {
	return a.proposal != nil && !a.agentBusy && a.atPrompt && !a.sensitive && a.execution == nil && !a.executionRecovery &&
		a.inBandMonitor == nil && a.zmodem == nil && len(a.lineInput) == 0 && a.lineReliable && !a.terminalPasteMode
}

func (a *App) handleDesktopCommand(command credentialui.WorkbenchCommand) error {
	if !a.config.DesktopUI || a.terminalDone || command.Deadline <= time.Now().UnixMilli() {
		return errors.New("操作已过期或连接已结束")
	}
	switch command.Kind {
	case "terminal-takeover":
		a.takeOverTerminal("已取消本次 AI 交互")
		return nil
	case "execution-collect":
		return a.collectExecutionOutput()
	case "bastion-compatibility":
		switch command.Text {
		case "true":
			a.config.BastionCompatibility = true
		case "false":
			a.config.BastionCompatibility = false
		default:
			return errors.New("无效的堡垒机兼容模式设置")
		}
		a.dirty = true
		return nil
	case "system-collection":
		switch command.Text {
		case "true":
			a.config.SystemCollection = true
		case "false":
			a.config.SystemCollection = false
		default:
			return errors.New("无效的自动采集设置")
		}
		a.dirty = true
		return nil
	case "model":
		return a.switchConversationModel(strings.TrimSpace(command.Text))
	case "widget-cancel":
		c := a.widgetCandidates[command.PanelID]
		if c == nil || c.id != command.CandidateID {
			return errors.New("组件候选已过期")
		}
		a.cancelWidgetCandidate(command.PanelID)
		a.cancelPipeline(command.PanelID)
		a.widgetNotice("已取消组件修改，原版本保持运行")
		return nil
	case "widget-ready", "widget-failed":
		if len(command.Text) > 1000 {
			return errors.New("组件诊断过长")
		}
		return a.finishWidget(command.PanelID, command.CandidateID, command.Text)
	case "widget-restore":
		d := command.WidgetDefinition
		if d == nil {
			return errors.New("缺少已保存的组件")
		}
		return a.restorePipeline(*d)

	case "workspace":
		if a.agentBusy || a.proposal != nil || a.execution != nil || a.executionRecovery {
			return errors.New("请等待当前任务完成，或先处理命令建议")
		}
		provider, ok := a.agent.(localWorkspaceAgent)
		if !ok {
			return errors.New(a.t("当前 AI 后端不支持本机代码工作区"))
		}
		workspace, err := resolveLocalWorkspace(command.Text)
		if err != nil {
			return fmt.Errorf("无法访问本机工作目录：%w", err)
		}
		a.setLocalWorkspace(provider, workspace)
		message, _ := a.applyLocalWorkspaceCommand("")
		a.appendPersistentChat(chatMessage{Role: "system", Content: message, Time: time.Now()})
		a.dirty = true
	case "prompt":
		if a.terminalTakeover {
			return errors.New("请先在终端完成或退出审批，回到 Shell 提示符后再向 AI 提问")
		}
		prompt := strings.TrimSpace(command.Text)
		if prompt == "" || len(prompt) > 64*1024 {
			return errors.New("请输入有效的问题（最多 64 KB）")
		}
		if a.agentBusy || a.proposal != nil || a.execution != nil || a.executionRecovery {
			return errors.New("请等待当前任务完成，或先处理命令建议")
		}
		if !a.handleLocalWorkspaceCommand(prompt, false) {
			a.startAI(prompt, false)
		}
	case "approve", "reject":
		if a.proposal == nil || a.proposal.ID != command.ProposalID || a.agentBusy {
			return errors.New("命令建议已变化，请查看最新状态")
		}
		if command.Kind == "approve" {
			if !a.desktopCanApprove() {
				return errors.New("请等待 SSH 回到空闲 Shell 提示符，并清空未提交的输入")
			}
			a.approveProposal()
		} else {
			a.rejectProposal()
		}
	case "monitor-open":
		if a.panels.CountPurpose(false) >= 16 {
			return errors.New("最多打开 16 个监控，请先关闭不用的监控")
		}
		var title, collector string
		interval := 3 * time.Second
		switch command.PanelID {
		case "system":
			return nil
		case "processes":
			title, collector = "进程", panel.BuiltinProcessCommand
		case "disk":
			title, collector, interval = "磁盘", panel.BuiltinDiskCommand, 10*time.Second
		case "network":
			title, collector = "网络", panel.BuiltinNetworkCommand
		case "services":
			title, collector, interval = "服务", panel.BuiltinServiceCommand, 10*time.Second
		case "custom":
			title, collector, interval = "自定义监控", strings.TrimSpace(command.Text), 5*time.Second
			if err := policy.ValidateReadOnlyMonitorCommand(collector); err != nil {
				return fmt.Errorf("监控命令被只读策略拒绝：%w", err)
			}
		default:
			return errors.New("未知的监控类型")
		}
		id := "desktop-" + command.PanelID
		if command.PanelID == "custom" {
			id = ""
		}
		a.panels.Open(panel.Panel{ID: id, Kind: panel.KindMonitor, Origin: panel.OriginUser, Title: title, Command: collector, Interval: interval, Target: a.parsed.Target, AllowAIRead: true})
	case "monitor-close", "alert-close", "monitor-refresh", "alert-refresh":
		p := a.panels.ByID(command.PanelID)
		if p == nil || p.IsAlert() != strings.HasPrefix(command.Kind, "alert-") {
			return errors.New("对应的实时面板或告警任务不存在")
		}
		if strings.HasSuffix(command.Kind, "-close") {
			if err := a.panels.Close(command.PanelID, panel.OriginUser); err != nil {
				return err
			}
			a.resetMonitorAudit(command.PanelID, false)
			a.cancelWidgetCandidate(command.PanelID)
			a.cancelPipeline(command.PanelID)
			delete(a.monitorActive, command.PanelID)
			delete(a.monitorLast, command.PanelID)
		} else {
			if err := a.resumePausedMonitoring(); err != nil {
				return err
			}
			a.resetMonitorAudit(command.PanelID, true)
			a.monitorLast[command.PanelID] = time.Time{}
		}
	default:
		return errors.New("未知的工作台操作")
	}
	a.dirty = true
	return nil
}

func (a *App) pipelineSaveSafe(p *panel.Panel) bool {
	if a.safeText(p.Command) != p.Command {
		return false
	}
	if p.Pipeline != nil {
		data, _ := json.Marshal(p.Pipeline)
		return a.safeText(string(data)) == string(data)
	}
	return true
}
