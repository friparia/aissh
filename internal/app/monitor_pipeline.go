package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/monitor"
	"git.example.com/ai/wrd-ai-cli/internal/monitorplan"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
	"git.example.com/ai/wrd-ai-cli/internal/policy"
	"git.example.com/ai/wrd-ai-cli/internal/widgets"
)

type pipelineDraft struct {
	id, panelID, session string
	generation           uint64
	revision             uint64
	expires              time.Time
	sources              []monitorplan.Source
	captures             map[string]monitorplan.Capture
	plan                 *monitorplan.Plan
	mapID                string
	restore              *widgets.Definition
	title                string
	interval             time.Duration
	allowAIRead          bool
}
type pipelineBatch struct {
	id, panelID, draftID, candidateID, binding string
	revision                                   uint64
	generation                                 uint64
	sources                                    []monitorplan.Source
	captures                                   map[string]monitorplan.Capture
	index                                      int
	running                                    bool
	created                                    time.Time
	cancel                                     context.CancelFunc
}

func (a *App) pipelineContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}
func (a *App) initPipelines() {
	if a.pipelineSession == "" {
		a.pipelineSession = catalog.NewID()
		a.pipelineGeneration++
	}
	if a.pipelineDrafts == nil {
		a.pipelineDrafts = map[string]*pipelineDraft{}
	}
	if a.pipelineBatches == nil {
		a.pipelineBatches = map[string]*pipelineBatch{}
	}
}
func (a *App) cancelPipeline(id string) {
	delete(a.pipelineDrafts, id)
	for key, b := range a.pipelineBatches {
		if b.panelID == id {
			if b.cancel != nil {
				b.cancel()
			}
			delete(a.pipelineBatches, key)
		}
	}
}
func (a *App) invalidatePipelines() {
	a.monitorDecisions = nil
	if a.monitorReview != nil {
		a.monitorReview, a.proposal = nil, nil
	}
	a.pipelineGeneration++
	for id := range a.pipelineDrafts {
		a.cancelPipeline(id)
	}
	for key, b := range a.pipelineBatches {
		if b.cancel != nil {
			b.cancel()
		}
		delete(a.pipelineBatches, key)
	}
	for id, c := range a.widgetCandidates {
		if c.panel.Pipeline != nil {
			a.cancelWidgetCandidate(id)
		}
	}
	a.pipelineFeedback = nil
}
func (a *App) pipelineReply(message string) {
	message = a.safeText(message)
	a.pipelineFeedback = append(a.pipelineFeedback, message)
	if len(a.pipelineFeedback) > 8 {
		a.pipelineFeedback = a.pipelineFeedback[len(a.pipelineFeedback)-8:]
	}
	a.dirty = true
}

// Native tool receipts resume the existing AI conversation after a phase, without
// requiring the user to send another message or inserting a fabricated user turn.
func (a *App) continuePipelineAI() {
	if len(a.pipelineFeedback) == 0 || a.agentBusy || a.agent == nil || a.proposal != nil || a.execution != nil || a.terminalDone {
		return
	}
	for _, b := range a.pipelineBatches {
		if b.draftID != "" {
			return
		}
	}
	if a.pipelineContinuations >= 8 {
		a.pipelineFeedback = nil
		a.widgetNotice("监控配置已达到自动修正次数上限，请根据采集或映射诊断调整")
		return
	}
	a.pipelineContinuations++
	feedback := strings.Join(a.pipelineFeedback, "\n")
	a.pipelineFeedback = nil
	request := agent.Request{Event: "user_message", SessionID: a.agentSessionID, Prompt: "客户端已返回 monitor_pipeline_receipt，请继续原监控任务的下一阶段。不可省略首采与 map，也不可伪造回执。", SystemContext: map[string]string{"ui_language": a.localizer.Language(), "response_language": a.agentLanguage}, Target: &agent.Target{Type: "linux", HostLabel: a.parsed.Target, Protocol: "ssh"}}
	if request.SessionID == "" {
		request.Event = "start"
	}
	request.SystemContext["monitor_pipeline_receipt"] = feedback
	a.addPanelContext(&request)
	a.agentBusy = true
	a.agentStatus = "正在配置监控数据映射"
	a.dirty = true
	a.launchAgentStream(request, true)
}
func (a *App) executePipelineAction(operation string, spec agent.PanelSpec) (*panel.Panel, string, error) {
	a.initPipelines()
	if !a.config.DesktopUI || spec.Purpose == "alert" {
		return nil, "", fmt.Errorf("数据管线只用于桌面实时面板")
	}
	if a.terminalDone {
		return nil, "", fmt.Errorf("SSH 连接已结束，需要重新连接并首采")
	}
	if !monitorplan.ValidID(spec.ID) {
		return nil, "", fmt.Errorf("管线必须有稳定的 panel.id（字母/数字/下划线/短横线）")
	}
	old := a.panels.ByID(spec.ID)
	if old != nil && spec.ExpectedRevision != old.DefinitionRevision {
		return nil, "", fmt.Errorf("组件版本冲突，当前 definitionRevision=%d", old.DefinitionRevision)
	}
	req := spec.Pipeline
	if req != nil && req.ProtocolVersion != 2 {
		return nil, "", fmt.Errorf("pipeline.protocolVersion 必须为 2")
	}
	switch operation {
	case "probe":
		if req == nil {
			return nil, "", fmt.Errorf("probe 需要 pipeline.sources")
		}
		if err := monitorplan.ValidateSources(req.Sources); err != nil {
			return nil, "", err
		}
		for _, s := range req.Sources {
			if err := policy.ValidateReadOnlyMonitorCommand(s.Command); err != nil {
				return nil, "", fmt.Errorf("源 %s 被只读策略拒绝：%w", s.ID, err)
			}
		}
		if a.pipelineDrafts[spec.ID] == nil && len(a.pipelineDrafts) >= 4 {
			return nil, "", fmt.Errorf("最多同时配置 4 个监控面板")
		}
		a.cancelPipeline(spec.ID)
		a.cancelWidgetCandidate(spec.ID)
		revision := uint64(1)
		if old != nil {
			revision = old.SourceRevision + 1
		}
		draft := &pipelineDraft{id: catalog.NewID(), panelID: spec.ID, session: a.pipelineSession, generation: a.pipelineGeneration, revision: revision, expires: time.Now().Add(5 * time.Minute), sources: append([]monitorplan.Source(nil), req.Sources...)}
		draft.title, draft.interval, draft.allowAIRead = spec.Title, 5*time.Second, spec.AllowAIRead
		if spec.IntervalMS > 0 {
			draft.interval = time.Duration(max(1000, min(spec.IntervalMS, 300000))) * time.Millisecond
		}
		a.pipelineDrafts[spec.ID] = draft
		a.newPipelineBatch(spec.ID, draft.sources, draft.id, "", revision, "")
		return nil, "等待监控脚本审计后在当前 SSH 终端首采：" + spec.ID + "；采集回执到达后才可提交 map", nil
	case "map":
		draft := a.pipelineDrafts[spec.ID]
		if req == nil || req.DataPlan == nil || len(req.Refs) == 0 {
			return nil, "", fmt.Errorf("map 需要 refs 和 dataPlan")
		}
		if err := a.validatePipelineReceipts(draft, req.Refs); err != nil {
			return nil, "", err
		}
		if err := req.DataPlan.Validate(draft.sources); err != nil {
			return nil, "", err
		}
		engine := &monitorplan.Engine{}
		dataset, err := engine.Evaluate(a.pipelineContext(), *req.DataPlan, draft.sources, draft.captures, spec.ExpectedRevision+1)
		if err != nil {
			return nil, "", err
		}
		if err = initialDataset(draft.sources, draft.captures, dataset); err != nil {
			return nil, "", err
		}
		copy := *req.DataPlan
		draft.plan = &copy
		draft.mapID = catalog.NewID()
		data, _ := json.Marshal(dataset)
		a.pipelineReply("map 成功 panel=" + spec.ID + " mapId=" + draft.mapID + "\n真实 Dataset=" + string(data))
		return nil, "真实采样映射通过：" + spec.ID + "；下一步生成 bindings 和 sdkVersion=2 组件", nil
	case "create", "open", "update":
		if spec.View == nil && old == nil {
			return nil, "", fmt.Errorf("需要 sdkVersion=2 组件")
		}
		if spec.View != nil && (spec.View.Kind != "custom" || spec.View.SDKVersion != 2) {
			return nil, "", fmt.Errorf("管线组件必须使用 custom / sdkVersion=2")
		}
		if spec.View != nil {
			if err := spec.View.Validate(); err != nil {
				return nil, "", err
			}
			if a.safeText(spec.View.Source) != spec.View.Source {
				return nil, "", fmt.Errorf("组件源码不能包含凭据")
			}
		}
		if spec.Command != "" || spec.Collector != "" || spec.Data != nil {
			return nil, "", fmt.Errorf("v2 采集和数据格式由首采管线确定，command/collector/data 应为空")
		}
		var definition monitorplan.Definition
		var captures map[string]monitorplan.Capture
		if req == nil || req.MapID == "" {
			if old == nil || old.Pipeline == nil {
				return nil, "", fmt.Errorf("需要首采：先 probe，再 map，使用客户端 mapId 创建")
			}
			if req != nil && (len(req.Sources) > 0 || req.DataPlan != nil || len(req.Refs) > 0) {
				return nil, "", fmt.Errorf("改变数据源或映射需要重新 probe 和 map")
			}
			definition = *old.Pipeline
			captures = old.PipelineCaptures
			if req != nil && len(req.Bindings) > 0 {
				definition.Bindings = req.Bindings
			}
		} else {
			draft := a.pipelineDrafts[spec.ID]
			if draft == nil || draft.mapID != req.MapID || draft.plan == nil {
				return nil, "", fmt.Errorf("mapId 无效或不属于该面板；需要首采和 map")
			}
			if err := a.validatePipelineReceipts(draft, nil); err != nil {
				return nil, "", err
			}
			definition = monitorplan.Definition{ProtocolVersion: 2, Sources: draft.sources, DataPlan: *draft.plan, Bindings: req.Bindings}
			captures = draft.captures
		}
		if err := definition.Validate(); err != nil {
			return nil, "", err
		}
		candidate := panel.Panel{ID: spec.ID, Kind: panel.KindMonitor, Origin: panel.OriginAI, Purpose: "realtime", Title: spec.Title, Target: a.parsed.Target, Interval: 5 * time.Second, BindingID: catalog.NewID()}
		if old != nil {
			candidate = *old
		}
		if spec.Title != "" {
			candidate.Title = spec.Title
		}
		if spec.IntervalMS > 0 {
			candidate.Interval = time.Duration(max(1000, min(spec.IntervalMS, 300000))) * time.Millisecond
		}
		candidate.AllowAIRead = spec.AllowAIRead
		same := old != nil && old.Pipeline != nil && reflect.DeepEqual(old.Pipeline.Sources, definition.Sources) && reflect.DeepEqual(old.Pipeline.DataPlan, definition.DataPlan)
		candidate.Pipeline = &definition
		candidate.PipelineCaptures = captures
		candidate.Command = ""
		candidate.DataContract = nil
		candidate.DefinitionRevision++
		if !same {
			candidate.SourceRevision++
			candidate.WidgetData = widgets.Data{}
			candidate.PipelineEngine = &monitorplan.Engine{MaxGap: max(15*time.Second, candidate.Interval*3)}
			if err := a.evaluatePipeline(&candidate, captures, true); err != nil {
				return nil, "", err
			}
		} else {
			candidate.PipelineEngine = old.PipelineEngine.Clone()
			if !reflect.DeepEqual(old.Pipeline.Bindings, definition.Bindings) {
				candidate.SourceRevision++
				candidate.WidgetData = widgets.Data{}
				candidate.PipelineEngine = &monitorplan.Engine{MaxGap: max(15*time.Second, candidate.Interval*3)}
				if err := a.evaluatePipeline(&candidate, captures, true); err != nil {
					return nil, "", err
				}
			}
		}
		item, message, err := a.prepareWidget(candidate, spec.View)
		if err == nil {
			c := a.widgetCandidates[spec.ID]
			if pipelineStale(candidate, time.Now()) {
				a.refreshPipelineCandidate(c)
			}
		}
		return item, message, err
	default:
		return nil, "", fmt.Errorf("不支持管线操作 %s", operation)
	}
}
func (a *App) validatePipelineReceipts(d *pipelineDraft, refs []monitorplan.SourceRef) error {
	if d == nil || d.session != a.pipelineSession || d.generation != a.pipelineGeneration || time.Now().After(d.expires) || len(d.captures) != len(d.sources) {
		return fmt.Errorf("首采凭据缺失、过期或连接已变化，请重新 probe")
	}
	supplied := map[string]string{}
	for _, r := range refs {
		if supplied[r.SourceID] != "" {
			return fmt.Errorf("重复首采引用")
		}
		supplied[r.SourceID] = r.CaptureID
	}
	if refs != nil && len(refs) != len(d.sources) {
		return fmt.Errorf("refs 必须包含全部首采源")
	}
	for _, s := range d.sources {
		c, ok := d.captures[s.ID]
		if !ok || c.SessionID != d.session || c.Generation != d.generation || c.CommandHash != monitorplan.Hash(s.Command) || c.SourceRevision != d.revision || refs != nil && supplied[s.ID] != c.ID {
			return fmt.Errorf("源 %s 的首采凭据不匹配", s.ID)
		}
		if s.Required && !c.Good() {
			return fmt.Errorf("必需源 %s 首采失败（%s），请修改命令重新 probe", s.ID, c.Status)
		}
	}
	return nil
}
func initialDataset(sources []monitorplan.Source, captures map[string]monitorplan.Capture, d monitorplan.Dataset) error {
	required := map[string]bool{}
	for _, s := range sources {
		required[s.ID] = s.Required
		if s.Required && !captures[s.ID].Good() {
			return fmt.Errorf("必需源 %s 首采失败", s.ID)
		}
	}
	for _, diag := range d.Diagnostics {
		if diag.Stage == "parse" && required[diag.SourceID] {
			return fmt.Errorf("映射节点 %s：%s", diag.NodeID, diag.Message)
		}
	}
	for _, m := range d.Metrics {
		if m.Quality == "unknown" && !strings.HasPrefix(m.Reason, "等待") && !strings.Contains(m.Reason, "重新建立基线") {
			allRequired := true
			for _, id := range m.SourceIDs {
				allRequired = allRequired && required[id]
			}
			if allRequired {
				return fmt.Errorf("映射结果无效：%s", m.Reason)
			}
		}
	}
	for _, table := range d.Tables {
		if table.Quality == "unknown" {
			allRequired := true
			for _, id := range table.SourceIDs {
				allRequired = allRequired && required[id]
			}
			if allRequired {
				return fmt.Errorf("映射表无效：%s", table.Reason)
			}
		}
	}
	return nil
}
func (a *App) newPipelineBatch(panelID string, sources []monitorplan.Source, draftID, candidateID string, revision uint64, binding string) *pipelineBatch {
	b := &pipelineBatch{id: catalog.NewID(), panelID: panelID, draftID: draftID, candidateID: candidateID, revision: revision, binding: binding, generation: a.pipelineGeneration, sources: sources, captures: map[string]monitorplan.Capture{}, created: time.Now()}
	a.pipelineBatches[b.id] = b
	return b
}
func pipelineStale(p panel.Panel, now time.Time) bool {
	if p.Pipeline == nil {
		return false
	}
	for _, s := range p.Pipeline.Sources {
		c := p.PipelineCaptures[s.ID]
		if s.Required && (!c.Good() || now.Sub(c.FinishedAt) > max(30*time.Second, p.Interval*2)) {
			return true
		}
	}
	return false
}
func (a *App) refreshPipelineCandidate(c *widgetCandidate) {
	for _, b := range a.pipelineBatches {
		if b.candidateID == c.id {
			return
		}
	}
	c.pipelineRefreshes++
	if c.pipelineRefreshes > 2 {
		a.widgetNotice("组件未生效：采集总耗时使首采持续过期，请减少数据源或提高采样间隔")
		a.cancelWidgetCandidate(c.panel.ID)
		return
	}
	c.expires = time.Now().Add(90 * time.Second)
	c.pipelineRefreshing = true
	a.newPipelineBatch(c.panel.ID, c.panel.Pipeline.Sources, "", c.id, c.panel.SourceRevision, c.panel.BindingID)
	a.widgetNotice("首采已过期，正在按原命令刷新并重新验证：" + c.panel.Title)
}
func (a *App) schedulePipelines(now time.Time) {
	if a.terminalDone {
		return
	}
	a.initPipelines()
	for id, d := range a.pipelineDrafts {
		if a.monitorReview != nil && a.monitorReview.PanelID == id {
			d.expires = now.Add(5 * time.Minute)
		}
		if now.After(d.expires) {
			a.cancelPipeline(id)
		}
	}
	if a.panels != nil {
		for _, p := range a.panels.Panels() {
			if p.Pipeline == nil {
				continue
			}
			if !p.LastUpdated.IsZero() && now.Sub(p.LastUpdated) > max(30*time.Second, p.Interval*2) && p.WidgetData.Error != "采样已过期，正在等待 SSH 采集" {
				p.WidgetData = widgets.Stale(p.WidgetData, "采样已过期，正在等待 SSH 采集")
				p.LastError = p.WidgetData.Error
				a.dirty = true
			}
			busy := false
			for _, b := range a.pipelineBatches {
				busy = busy || b.panelID == p.ID && b.draftID == "" && b.candidateID == ""
			}
			// Waiting for an occupied/paused shell is not a failed SSH sample.
			// Keep the last data stale and defer recurring work until it can run;
			// otherwise each 90-second wait creates another failure and chat entry.
			if !busy && a.canStartInBandMonitor() && now.Sub(a.monitorLast[p.ID]) >= p.Interval {
				a.newPipelineBatch(p.ID, p.Pipeline.Sources, "", "", p.SourceRevision, p.BindingID)
			}
		}
	}
	ids := []string{}
	for id := range a.pipelineBatches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b := a.pipelineBatches[id]
		if b == nil || b.running || (!a.config.SystemCollection && b.draftID == "" && b.candidateID == "") {
			continue
		}

		if b.index >= len(b.sources) {
			continue
		}
		source := b.sources[b.index]
		if err := policy.ValidateReadOnlyMonitorCommand(source.Command); err != nil {
			a.failPipelineBatch(b, "只读策略拒绝："+err.Error())
			continue
		}
		if !a.ensureMonitorAudit(a.pipelineMonitorAudit(b)) {
			// Human review time is not an SSH collection timeout.
			b.created = now
			continue
		}
		if now.Sub(b.created) > 90*time.Second {
			a.failPipelineBatch(b, "采集等待超时，请确认 SSH 位于空闲提示符")
			continue
		}
		if !a.canStartInBandCollection() || now.Sub(a.terminalInputAt) < 750*time.Millisecond {
			continue
		}
		b.running = true
		item := &panel.Panel{ID: "pipeline-" + b.id, Title: b.panelID + "/" + source.ID, Command: source.Command}
		a.startInBandCollection(item, now, b.id, time.Duration(source.TimeoutMS)*time.Millisecond)
		return
	}
}
func (a *App) failPipelineBatch(b *pipelineBatch, message string) {
	if b.cancel != nil {
		b.cancel()
	}
	delete(a.pipelineBatches, b.id)
	if b.candidateID != "" {
		a.cancelWidgetCandidate(b.panelID)
	}
	if b.draftID != "" {
		a.pipelineReply("probe 失败 panel=" + b.panelID + ": " + message)
	}
	if b.draftID == "" && b.candidateID == "" {
		if p := a.panels.ByID(b.panelID); p != nil && p.SourceRevision == b.revision {
			_ = a.evaluatePipeline(p, map[string]monitorplan.Capture{}, false)
		}
	}
	a.widgetNotice("采集未完成：" + b.panelID + "：" + message)
	a.monitorLast[b.panelID] = time.Now()
}
func (a *App) applyPipelineResult(v monitorResultEvent) {
	b := a.pipelineBatches[v.pipelineID]
	if b == nil || b.generation != a.pipelineGeneration || b.index >= len(b.sources) {
		return
	}
	b.running = false
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	if v.err != nil && a.monitorPrimaryFallback && !a.monitorPrimaryPaused && monitor.CanRetryViaPrimary(v.err) {
		a.monitorViaPrimary = true
		return
	}
	source := b.sources[b.index]
	c := monitorplan.Capture{Status: "disconnected", FinishedAt: time.Now()}
	if v.capture != nil {
		c = *v.capture
	}
	if v.err != nil && c.Stderr == nil && c.Streams != "merged" {
		message := v.err.Error()
		c.Stderr = &message
	}
	c.ID = catalog.NewID()
	c.SourceID = source.ID
	c.SessionID = a.pipelineSession
	c.Generation = a.pipelineGeneration
	c.CommandHash = monitorplan.Hash(source.Command)
	c.SourceRevision = b.revision
	a.pipelineSequence++
	c.Sequence = a.pipelineSequence
	b.captures[source.ID] = c
	b.index++
	if b.index < len(b.sources) {
		return
	}
	delete(a.pipelineBatches, b.id)
	if b.draftID != "" {
		d := a.pipelineDrafts[b.panelID]
		if d == nil || d.id != b.draftID {
			return
		}
		d.captures = b.captures
		previews := []monitorplan.Capture{}
		for _, s := range b.sources {
			p := b.captures[s.ID]
			if len(p.Stdout) > 6000 {
				p.Stdout = p.Stdout[:6000] + "\n[预览截短；完整采样留在本地]"
			}
			if p.Stderr != nil {
				x := *p.Stderr
				if len(x) > 1000 {
					x = x[:1000]
				}
				p.Stderr = &x
			}
			previews = append(previews, p)
		}
		data, _ := json.Marshal(previews)
		if d.restore != nil {
			a.finishPipelineRestore(d)
			return
		}
		a.pipelineReply("probe 完成 panel=" + b.panelID + "；以下为真实 SSH 回执（预览脱敏，map 使用本地完整原文）：\n" + string(data))
		a.widgetNotice("SSH 首采完成：" + b.panelID + "；等待本地映射验证")
		return
	}
	if b.candidateID != "" {
		c := a.widgetCandidates[b.panelID]
		if c == nil || c.id != b.candidateID {
			return
		}
		if err := a.evaluatePipeline(&c.panel, b.captures, true); err != nil {
			a.widgetNotice("组件未生效：" + err.Error())
			a.cancelWidgetCandidate(b.panelID)
			return
		}
		c.pipelineRefreshing = false
		c.id = catalog.NewID()
		c.expires = time.Now().Add(30 * time.Second)
		a.dirty = true
		return
	}
	p := a.panels.ByID(b.panelID)
	if p == nil || p.Pipeline == nil || p.BindingID != b.binding || p.SourceRevision != b.revision {
		return
	}
	if err := a.evaluatePipeline(p, b.captures, false); err != nil {
		p.WidgetData.Error = err.Error()
		p.WidgetData.Data = nil
		p.WidgetData.Sequence++
		p.LastError = err.Error()
	}
	a.monitorLast[p.ID] = time.Now()
	a.dirty = true
}
func (a *App) evaluatePipeline(p *panel.Panel, captures map[string]monitorplan.Capture, initial bool) error {
	if p.PipelineEngine == nil {
		p.PipelineEngine = &monitorplan.Engine{MaxGap: max(15*time.Second, p.Interval*3)}
	}
	engine := p.PipelineEngine.Clone()
	dataset, err := engine.Evaluate(a.pipelineContext(), p.Pipeline.DataPlan, p.Pipeline.Sources, captures, p.DefinitionRevision)
	if err != nil {
		if initial {
			return err
		}
		dataset = engine.Failure(p.Pipeline.DataPlan, p.Pipeline.Sources, p.DefinitionRevision, err.Error())
	}
	if initial {
		if err = initialDataset(p.Pipeline.Sources, captures, dataset); err != nil {
			return err
		}
	}
	p.PipelineEngine = engine
	p.PipelineCaptures = captures
	p.Content = pipelineCapturePreview(p.Pipeline.Sources, captures)
	p.WidgetData.BindingID = p.BindingID
	p.WidgetData.SourceRevision = p.SourceRevision
	p.WidgetData.IntervalMS = p.Interval.Milliseconds()
	sampleTime := time.Time{}
	for _, capture := range captures {
		if capture.FinishedAt.After(sampleTime) {
			sampleTime = capture.FinishedAt
		}
	}
	if sampleTime.IsZero() {
		sampleTime = time.Now()
	}
	p.WidgetData = widgets.RecordDataset(p.WidgetData, dataset, *p.Pipeline, sampleTime)
	p.LastUpdated = sampleTime
	p.LastError = ""
	p.Status = "实时"
	p.RefreshCount++
	if len(dataset.Diagnostics) > 0 {
		p.LastError = dataset.Diagnostics[0].Message
	}
	return nil
}
func (a *App) restorePipeline(d widgets.Definition) error {
	if err := d.ValidateData(); err != nil {
		return err
	}
	if d.ID == "" {
		d.ID = "widget-" + catalog.NewID()
	}
	a.resetMonitorAudit(d.ID, false)
	spec := agent.PanelSpec{ID: d.ID, Title: d.Title, IntervalMS: int(d.IntervalMS), AllowAIRead: d.AllowAIRead, ExpectedRevision: d.Revision, Purpose: "realtime", Pipeline: &monitorplan.Request{ProtocolVersion: 2, Sources: savedPipelineSources(d)}}
	_, message, err := a.executePanelAction(agent.PanelAction{Action: "probe", Panel: spec})
	if err != nil {
		return err
	}
	a.pipelineDrafts[d.ID].restore = &d
	a.widgetNotice(message)
	return nil
}
func (a *App) finishPipelineRestore(d *pipelineDraft) {
	saved := d.restore
	if saved.Pipeline == nil {
		a.finishLegacyRestore(d)
		return
	}
	refs := []monitorplan.SourceRef{}
	for _, s := range d.sources {
		refs = append(refs, monitorplan.SourceRef{SourceID: s.ID, CaptureID: d.captures[s.ID].ID})
	}
	if err := a.validatePipelineReceipts(d, refs); err != nil {
		a.widgetNotice("历史组件未恢复：" + err.Error())
		return
	}
	engine := &monitorplan.Engine{}
	data, err := engine.Evaluate(a.pipelineContext(), saved.Pipeline.DataPlan, d.sources, d.captures, saved.Revision+1)
	if err == nil {
		err = initialDataset(d.sources, d.captures, data)
	}
	if err != nil {
		a.widgetNotice("历史组件未恢复：" + err.Error())
		return
	}
	d.plan = &saved.Pipeline.DataPlan
	d.mapID = catalog.NewID()
	spec := agent.PanelSpec{ID: d.panelID, Title: saved.Title, ExpectedRevision: saved.Revision, IntervalMS: int(saved.IntervalMS), AllowAIRead: saved.AllowAIRead, View: &saved.View, Pipeline: &monitorplan.Request{ProtocolVersion: 2, MapID: d.mapID, Bindings: saved.Pipeline.Bindings}}
	_, message, err := a.executePanelAction(agent.PanelAction{Action: "create", Panel: spec})
	if err != nil {
		message = "历史组件未恢复：" + err.Error()
	}
	a.widgetNotice(message)
}

func savedPipelineSources(d widgets.Definition) []monitorplan.Source {
	if d.Pipeline != nil {
		return d.Pipeline.Sources
	}
	return []monitorplan.Source{{ID: "legacy", Command: d.Command, TimeoutMS: 8000, Required: true}}
}
func (a *App) finishLegacyRestore(d *pipelineDraft) {
	saved := d.restore
	if err := a.validatePipelineReceipts(d, nil); err != nil {
		a.widgetNotice("历史组件未恢复：" + err.Error())
		return
	}
	c := d.captures["legacy"]
	if _, _, err := widgets.Parse(saved.Contract, c.Stdout); err != nil {
		a.widgetNotice("历史组件数据解析失败：" + err.Error())
		return
	}
	candidate := panel.Panel{ID: d.panelID, Origin: panel.OriginAI, Kind: panel.KindMonitor, Purpose: "realtime", BindingID: catalog.NewID(), SourceRevision: d.revision, DefinitionRevision: saved.Revision + 1, Command: saved.Command, Title: saved.Title, Interval: time.Duration(max(1000, saved.IntervalMS)) * time.Millisecond, DataContract: &saved.Contract, AllowAIRead: saved.AllowAIRead, LastUpdated: c.FinishedAt, RefreshCount: 1, Target: a.parsed.Target, SampleContent: c.Stdout, Content: c.Stdout}
	if old := a.panels.ByID(d.panelID); old != nil {
		if old.DefinitionRevision != saved.Revision {
			a.widgetNotice("历史组件未恢复：定义版本已变化")
			return
		}
		candidate.BindingID = old.BindingID
	}
	candidate.WidgetData = widgets.Record(widgets.Data{BindingID: candidate.BindingID, SourceRevision: candidate.SourceRevision, IntervalMS: candidate.Interval.Milliseconds()}, saved.Contract, c.Stdout, nil, c.FinishedAt)
	_, message, err := a.prepareWidget(candidate, &saved.View)
	if err != nil {
		a.widgetNotice("历史组件未恢复：" + err.Error())
		return
	}
	a.widgetCandidates[candidate.ID].requiresSample = true
	a.widgetNotice(message)
}

// Human-readable inspection lives in the host's existing raw-output disclosure,
// never in the data bound to an authored component or in saved definitions.
func pipelineCapturePreview(sources []monitorplan.Source, captures map[string]monitorplan.Capture) string {
	var out strings.Builder
	preview := func(value string) string {
		r := []rune(value)
		if len(r) > 2000 {
			return string(r[:2000]) + "\n[原始输出预览已截短；映射使用本地完整采样]"
		}
		return value
	}
	for _, source := range sources {
		c, ok := captures[source.ID]
		if !ok {
			fmt.Fprintf(&out, "[%s] 未取得采样\n", source.ID)
			continue
		}
		fmt.Fprintf(&out, "[%s] %s · %s / %s · %s\n", source.ID, c.Status, c.Transport, c.Streams, c.FinishedAt.Local().Format("15:04:05"))
		out.WriteString(preview(c.Stdout))
		if c.Stderr != nil && *c.Stderr != "" {
			out.WriteString("\nstderr:\n" + preview(*c.Stderr))
		}
		out.WriteString("\n\n")
	}
	return out.String()
}
func pipelineCommandPreview(sources []monitorplan.Source) string {
	parts := []string{}
	for _, s := range sources {
		parts = append(parts, "# "+s.ID+"\n"+s.Command)
	}
	return strings.Join(parts, "\n\n")
}
