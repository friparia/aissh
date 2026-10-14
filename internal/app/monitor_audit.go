package app

import (
	"encoding/json"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/monitorplan"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
)

// Approvals belong to this connection and the exact collection configuration.
// They are never loaded from saved widgets or inferred from the read-only policy.
type monitorAudit struct {
	PanelID, SourceID, Command, Title string
	Interval, Timeout                 time.Duration
	AllowAIRead                       bool
	PipelineID                        string
	Binding                           string
}

type monitorDecision struct {
	key, proposalID string
	approved        bool
	shown           bool
}

func (r monitorAudit) scope() string { return r.PanelID + "/" + r.SourceID }
func (r monitorAudit) key() string {
	r.PipelineID, r.Title = "", ""
	b, _ := json.Marshal(r)
	return monitorplan.Hash(string(b))
}

func (a *App) panelMonitorAudit(p *panel.Panel) monitorAudit {
	return monitorAudit{PanelID: p.ID, Command: p.Command, Title: p.Title, Binding: p.BindingID,
		Interval: max(time.Second, p.Interval), Timeout: a.monitor.Timeout, AllowAIRead: p.AllowAIRead}
}

func (a *App) pipelineMonitorAudit(b *pipelineBatch) monitorAudit {
	s := b.sources[b.index]
	r := monitorAudit{PanelID: b.panelID, SourceID: s.ID, Command: s.Command, Title: b.panelID,
		Interval: 5 * time.Second, Timeout: time.Duration(s.TimeoutMS) * time.Millisecond, PipelineID: b.id}
	if d := a.pipelineDrafts[b.panelID]; d != nil && d.id == b.draftID {
		r.Interval, r.Title, r.AllowAIRead = d.interval, d.title, d.allowAIRead
	} else if c := a.widgetCandidates[b.panelID]; c != nil && c.id == b.candidateID {
		r.Interval, r.Title, r.AllowAIRead = c.panel.Interval, c.panel.Title, c.panel.AllowAIRead
	} else if p := a.panels.ByID(b.panelID); p != nil {
		r.Interval, r.Title, r.AllowAIRead = p.Interval, p.Title, p.AllowAIRead
	}
	return r
}

func (a *App) ensureMonitorAudit(r monitorAudit) bool {
	if isBuiltinMonitorCommand(r.Command) {
		return true
	}
	if d, ok := a.monitorDecisions[r.scope()]; ok && d.key == r.key() {
		return d.approved
	}
	if a.proposal != nil || a.agentBusy || a.execution != nil || a.executionRecovery || a.inBandMonitor != nil || a.terminalDone {
		return false
	}
	a.monitorReview = &r
	explanation := a.t("监控采集：%s。批准后在当前 SSH 终端执行，并允许此配置每 %g 秒重复采集；终端忙时延后。首次运行显示命令、输出及退出状态，后续刷新仅更新监控面板。修改命令或周期后重新审计。", firstText(r.Title, r.PanelID), r.Interval.Seconds())
	if r.SourceID != "" {
		explanation += a.t(" 数据源：%s。首次试采也需此次批准。", r.SourceID)
	}
	if r.AllowAIRead || r.PipelineID != "" {
		explanation += a.t(" 采集结果会用于 AI 分析或监控配置。")
	}
	a.proposal = &agent.Proposal{ID: "monitor-" + catalog.NewID(), Command: r.Command,
		Explanation: explanation, ExpectedEffect: a.t("采集监控数据；仅通过只读策略校验，仍需你审阅完整脚本。"), Risk: "medium"}
	a.approvalInput = nil
	a.approvalCommandExpanded = false
	a.approvalStatus = a.t("监控脚本等待审计，批准前不会执行；拒绝后可修改配置或点击刷新重新审计。")
	a.appendPersistentChat(chatMessage{Role: "proposal", Content: a.proposalConversationContent(*a.proposal), Time: time.Now(), Proposal: a.proposal,
		Activity: &agent.Activity{ID: "ssh:" + a.proposal.ID, Kind: "ssh", Title: r.Command, Status: "pending"}})
	a.focus, a.aiExpanded, a.dirty = focusApproval, true, true
	a.notifyAgentTurn(agent.Turn{Proposal: a.proposal}, nil)
	return false
}

// Match the shipped scripts exactly; a built-in name or panel ID must not
// exempt a modified or custom command from review.
func isBuiltinMonitorCommand(command string) bool {
	switch command {
	case panel.BuiltinOverviewCommand, panel.BuiltinProcessCommand, panel.BuiltinDiskCommand,
		panel.BuiltinNetworkCommand, panel.BuiltinServiceCommand:
		return true
	default:
		return false
	}
}

func (a *App) currentMonitorAudit(r monitorAudit) (monitorAudit, bool) {
	if r.PipelineID != "" {
		b := a.pipelineBatches[r.PipelineID]
		if b == nil || b.index >= len(b.sources) {
			return monitorAudit{}, false
		}
		return a.pipelineMonitorAudit(b), true
	}
	p := a.panels.ByID(r.PanelID)
	if p == nil || p.Pipeline != nil {
		return monitorAudit{}, false
	}
	return a.panelMonitorAudit(p), true
}

func (a *App) decideMonitorAudit(approved bool) {
	r := *a.monitorReview
	current, exists := a.currentMonitorAudit(r)
	status, message := "rejected", a.t("已拒绝监控采集：%s", r.Command)
	if !exists || current.key() != r.key() {
		status, message = "unknown", a.t("监控配置已变化，本次审计已失效：%s", r.Command)
	} else {
		if a.monitorDecisions == nil {
			a.monitorDecisions = map[string]monitorDecision{}
		}
		a.monitorDecisions[r.scope()] = monitorDecision{key: r.key(), proposalID: a.proposal.ID, approved: approved}
		if approved {
			status, message = "approved", a.t("已批准监控采集及按周期重复执行：%s", r.Command)
			if b := a.pipelineBatches[r.PipelineID]; b != nil {
				b.created = time.Now()
				if d := a.pipelineDrafts[b.panelID]; d != nil {
					d.expires = time.Now().Add(5 * time.Minute)
				}
			}
		} else if b := a.pipelineBatches[r.PipelineID]; b != nil {
			a.failPipelineBatch(b, a.t("用户拒绝监控脚本，未执行"))
		} else if p := a.panels.ByID(r.PanelID); p != nil {
			p.LastError = a.t("监控脚本已拒绝，未执行；点击刷新可重新审计")
		}
	}
	a.updateSSHActivity(a.proposal.ID, status, message)
	a.appendCommandDecision(chatMessage{Role: "user", Content: message, Time: time.Now()})
	a.monitorReview, a.proposal = nil, nil
	a.approvalInput, a.approvalStatus = nil, ""
	if status == "approved" && a.config.BastionCompatibility {
		// Manual collection pauses recurring work. A new explicit approval can
		// resume the approved collector once the terminal is ready again.
		_ = a.resumePausedMonitoring()
	}
	a.focus, a.dirty = focusTerminal, true
}

func (a *App) resetMonitorAudit(panelID string, rejectedOnly bool) {
	if !rejectedOnly && a.monitorReview != nil && a.monitorReview.PanelID == panelID {
		a.updateSSHActivity(a.proposal.ID, "unknown", a.t("监控配置已关闭或重新加载，本次审计已失效"))
		a.monitorReview, a.proposal = nil, nil
		a.approvalInput, a.approvalStatus = nil, ""
		a.focus, a.dirty = focusTerminal, true
	}
	for scope, d := range a.monitorDecisions {
		if strings.HasPrefix(scope, panelID+"/") && (!rejectedOnly || !d.approved) {
			delete(a.monitorDecisions, scope)
		}
	}
}
