//go:build !windows

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/creack/pty"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"git.example.com/ai/wrd-ai-cli/internal/monitorplan"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
	"git.example.com/ai/wrd-ai-cli/internal/widgets"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--widget-compile" {
		if err := widgets.RunCompiler(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func pipelineTestApp(t *testing.T) *App {
	a := desktopTestApp(t)
	a.config.SystemCollection = true
	a.agent = nil
	dir := t.TempDir()
	if err := os.Symlink("/bin/sh", filepath.Join(dir, "sh")); err != nil {
		t.Fatal(err)
	}
	// Real interactive PTY, with no Python/jq/ps in the collector PATH.
	cmd := exec.CommandContext(a.ctx, "/bin/bash", "--noprofile", "--norc", "-i")
	cmd.Env = append(os.Environ(), "PATH="+dir, "PS1=root@server:~# ", "PS2=> ", "PROMPT_COMMAND=", "HISTFILE=/dev/null", "TERM=xterm")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(a.mainCols), Rows: uint16(a.contentRows)})
	if err != nil {
		t.Fatal(err)
	}
	a.sshPTY, a.sshCommand = terminal, cmd
	a.startOutputReader()
	t.Cleanup(func() { a.cancel(); terminal.Close() })
	deadline := time.After(5 * time.Second)
	for !a.atPrompt {
		select {
		case e := <-a.events:
			a.handleEvent(e)
		case <-deadline:
			t.Fatal("no shell prompt")
		}
	}

	return a
}
func pipelineSource() monitorplan.Source {
	return monitorplan.Source{ID: "ps", Command: "printf '1 init 1024\n1823 nginx 20480\n1824 nginx 10240\n2000 other process 4096\n'", Required: true, TimeoutMS: 8000}
}
func pipelinePlan() monitorplan.Plan {
	return monitorplan.Plan{Parsers: []monitorplan.Parser{{ID: "rows", SourceID: "ps", Kind: "regex_rows", Pattern: `^\s*(?P<pid>[0-9]+)\s+(?P<name>.+?)\s+(?P<rss>[0-9]+)\s*$`, Columns: []monitorplan.Column{{ID: "pid", Type: "string"}, {ID: "name", Type: "string"}, {ID: "rss", Type: "number", Unit: "KiB"}}, SkipBlankLines: true}}, Steps: []monitorplan.Step{{ID: "nginx", Op: "filter", Input: "rows", Field: "name", Test: "equals", Values: []string{"nginx"}}, {ID: "count", Op: "count", Input: "nginx"}, {ID: "rss", Op: "sum", Input: "nginx", Field: "rss"}, {ID: "memory", Op: "convert_unit", Input: "rss", From: "KiB", To: "MiB"}}, Outputs: []monitorplan.Output{{ID: "count", Kind: "metric", Input: "count", History: true}, {ID: "memory", Kind: "metric", Input: "memory", Unit: "MiB", History: true}, {ID: "processes", Kind: "table", Input: "nginx"}}}
}
func pipelineBindings() []monitorplan.Binding {
	return []monitorplan.Binding{{Prop: "count", Kind: "metric", OutputID: "count"}, {Prop: "memory", Kind: "metric", OutputID: "memory"}, {Prop: "processes", Kind: "table", OutputID: "processes"}}
}
func pipelineView() widgets.View {
	return widgets.View{Kind: "custom", SDKVersion: 2, StateVersion: 1, Source: `import {defineWidget,MetricCard,LineChart,DataTable} from '@wrd/monitor-ui'; export default defineWidget({render({data,history}){return <section><h3>Nginx 原始文本映射</h3><MetricCard label="进程数" value={data?.count?.value??null}/><MetricCard label="RSS 合计" value={data?.memory?.value??null} unit="MiB"/><LineChart label="内存历史" series={history.series('memory')} unit="MiB"/><DataTable columns={['pid','name','rss']} rows={data?.processes?.rows??[]}/><small>RSS 中共享页可能重复计数</small></section>}})`}
}
func pipelinePump(t *testing.T, a *App, done func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !done() {
		a.schedulePipelines(time.Now())
		if a.monitorReview != nil {
			a.approveProposal()
		}
		select {
		case e := <-a.events:
			a.handleEvent(e)
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatalf("pipeline timeout batches=%+v candidates=%+v chat=%+v", a.pipelineBatches, a.widgetCandidates, a.chat)
		}
	}
}
func probeAndMap(t *testing.T, a *App, id string, source monitorplan.Source) string {
	t.Helper()
	_, _, err := a.executePanelAction(agent.PanelAction{Action: "probe", Panel: agent.PanelSpec{ID: id, Pipeline: &monitorplan.Request{ProtocolVersion: 2, Sources: []monitorplan.Source{source}}}})
	if err != nil {
		t.Fatal(err)
	}
	pipelinePump(t, a, func() bool { return len(a.pipelineBatches) == 0 })
	d := a.pipelineDrafts[id]
	if d.captures[source.ID].ID == "" {
		t.Fatal("no native receipt")
	}
	plan := pipelinePlan()
	refs := []monitorplan.SourceRef{{SourceID: source.ID, CaptureID: d.captures[source.ID].ID}}
	_, _, err = a.executePanelAction(agent.PanelAction{Action: "map", Panel: agent.PanelSpec{ID: id, Pipeline: &monitorplan.Request{ProtocolVersion: 2, Refs: refs, DataPlan: &plan}}})
	if err != nil {
		t.Fatal(err)
	}
	return d.mapID
}
func createPipeline(t *testing.T, a *App, id, mapID string) *widgetCandidate {
	t.Helper()
	view := pipelineView()
	_, _, err := a.executePanelAction(agent.PanelAction{Action: "create", Panel: agent.PanelSpec{ID: id, Title: "Nginx 首采看板", IntervalMS: 5000, AllowAIRead: true, View: &view, Pipeline: &monitorplan.Request{ProtocolVersion: 2, MapID: mapID, Bindings: pipelineBindings()}}})
	if err != nil {
		t.Fatal(err)
	}
	pipelinePump(t, a, func() bool { c := a.widgetCandidates[id]; return c == nil || c.artifact != nil })
	c := a.widgetCandidates[id]
	if c == nil {
		t.Fatalf("compile failed %+v", a.chat)
	}
	return c
}
func TestPipelineActualSSHFirstCaptureMapCompileAndActivateWithoutPython(t *testing.T) {
	a := pipelineTestApp(t)
	id := "live-nginx"
	mapID := probeAndMap(t, a, id, pipelineSource())
	if a.panels.ByID(id) != nil {
		t.Fatal("activated before view")
	}
	c := createPipeline(t, a, id, mapID)
	if a.panels.ByID(id) != nil {
		t.Fatal("activated before real-data render receipt")
	}
	state := a.desktopState()
	if len(state.WidgetCandidates) != 1 || state.WidgetCandidates[0].Sample == nil || state.WidgetCandidates[0].Sample.Data["memory"].(map[string]any)["value"] != float64(30) {
		t.Fatal("real sample not supplied to preflight")
	}
	if out := os.Getenv("AISSH_PIPELINE_FIXTURE"); out != "" {
		data, _ := json.Marshal(map[string]any{"candidate": state.WidgetCandidates[0], "artifact": c.artifact, "sample": c.panel.WidgetData})
		if err := os.WriteFile(out, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.finishWidget(id, c.id, ""); err != nil {
		t.Fatal(err)
	}
	p := a.panels.ByID(id)
	if p == nil || p.WidgetData.Sequence != 1 || len(p.WidgetData.History) != 1 {
		t.Fatal("first real sample not committed")
	}
	before := a.monitorLast[id]
	_, _, err := a.executePanelAction(agent.PanelAction{Action: "update", Panel: agent.PanelSpec{ID: id, Title: "新布局", ExpectedRevision: p.DefinitionRevision}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := a.widgetCandidates[id]
	if err = a.finishWidget(id, candidate.id, ""); err != nil {
		t.Fatal(err)
	}
	if p.Title != "新布局" || p.WidgetData.Sequence != 1 || len(p.WidgetData.History) != 1 || !before.Equal(a.monitorLast[id]) {
		t.Fatal("layout change reset source/history/schedule")
	}
	a.monitorLast[id] = time.Time{}
	pipelinePump(t, a, func() bool { return p.WidgetData.Sequence == 2 })
	if len(p.WidgetData.History) != 2 || p.PipelineCaptures["ps"].Sequence < 2 {
		t.Fatal("periodic collection not using pipeline")
	}
	definition := a.desktopState().WidgetTransfer.Definitions[id]
	raw, _ := json.Marshal(definition)
	if strings.Contains(string(raw), "captureId") || strings.Contains(string(raw), "stdout") {
		t.Fatal("capture leaked into persisted definition")
	}
	if definition.ValidateData() != nil || definition.Pipeline == nil {
		t.Fatal("pipeline not persisted")
	}
}
func TestPipelineRejectsInventedMissingExpiredAndCrossPanelReceipts(t *testing.T) {
	a := pipelineTestApp(t)
	v := pipelineView()
	if _, _, err := a.executePanelAction(agent.PanelAction{Action: "create", Panel: agent.PanelSpec{ID: "direct", Command: "printf ok", View: &v}}); err == nil {
		t.Fatal("direct generation accepted")
	}
	id := "receipt-panel"
	probeAndMap(t, a, id, pipelineSource())
	d := a.pipelineDrafts[id]
	refs := []monitorplan.SourceRef{{SourceID: "ps", CaptureID: d.captures["ps"].ID}}
	if err := a.validatePipelineReceipts(d, refs); err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"forged", "command", "expired", "generation", "missing"} {
		t.Run(test, func(t *testing.T) {
			copy := *d
			copy.captures = map[string]monitorplan.Capture{}
			for k, v := range d.captures {
				copy.captures[k] = v
			}
			r := append([]monitorplan.SourceRef(nil), refs...)
			switch test {
			case "forged":
				r[0].CaptureID = "AI-verified"
			case "command":
				v := copy.captures["ps"]
				v.CommandHash = "different"
				copy.captures["ps"] = v
			case "expired":
				copy.expires = time.Now().Add(-time.Second)
			case "generation":
				copy.generation++
			case "missing":
				copy.captures = nil
			}
			if a.validatePipelineReceipts(&copy, r) == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
	if _, _, err := a.executePanelAction(agent.PanelAction{Action: "create", Panel: agent.PanelSpec{ID: "other-panel", View: &v, Pipeline: &monitorplan.Request{ProtocolVersion: 2, MapID: d.mapID, Bindings: pipelineBindings()}}}); err == nil {
		t.Fatal("cross-panel map receipt accepted")
	}
	a.invalidatePipelines()
	if a.validatePipelineReceipts(d, refs) == nil {
		t.Fatal("disconnected capture accepted")
	}
}
func TestPipelineFailedFirstSourceAndBadMappingNeverActivate(t *testing.T) {
	for _, command := range []string{"python3 --version", "printf 'not a process row'"} {
		t.Run(command, func(t *testing.T) {
			a := pipelineTestApp(t)
			s := pipelineSource()
			s.Command = command
			_, _, err := a.executePanelAction(agent.PanelAction{Action: "probe", Panel: agent.PanelSpec{ID: "failed", Pipeline: &monitorplan.Request{ProtocolVersion: 2, Sources: []monitorplan.Source{s}}}})
			if err != nil {
				t.Fatal(err)
			}
			pipelinePump(t, a, func() bool { return len(a.pipelineBatches) == 0 })
			d := a.pipelineDrafts["failed"]
			plan := pipelinePlan()
			_, _, err = a.executePanelAction(agent.PanelAction{Action: "map", Panel: agent.PanelSpec{ID: "failed", Pipeline: &monitorplan.Request{ProtocolVersion: 2, Refs: []monitorplan.SourceRef{{SourceID: "ps", CaptureID: d.captures["ps"].ID}}, DataPlan: &plan}}})
			if err == nil || d.mapID != "" || a.panels.ByID("failed") != nil {
				t.Fatal("invalid first capture activated")
			}
		})
	}
}
func TestPipelinePrimaryChannelUsesMergedRealExitReceipt(t *testing.T) {
	a := pipelineTestApp(t)
	a.monitorViaPrimary = true
	source := pipelineSource()
	_, _, err := a.executePanelAction(agent.PanelAction{Action: "probe", Panel: agent.PanelSpec{ID: "primary", Pipeline: &monitorplan.Request{ProtocolVersion: 2, Sources: []monitorplan.Source{source}}}})
	if err != nil {
		t.Fatal(err)
	}
	var batch *pipelineBatch
	for _, b := range a.pipelineBatches {
		batch = b
	}
	state := &inBandMonitorState{pipelineID: batch.id, marker: "__TEST__", startedAt: time.Now()}
	_, complete, _, err := state.consume([]byte("__TEST__BEGIN__\r\n1823 nginx 20480\r\n__TEST__END__:0__\r\n"))
	if !complete || err != nil {
		t.Fatal("primary framing failed", err)
	}
	a.applyMonitorResult(monitorResultEvent{pipelineID: batch.id, capture: state.capture(err), err: err})
	capture := a.pipelineDrafts["primary"].captures["ps"]
	if !capture.Good() || capture.Streams != "merged" || capture.Stderr != nil || capture.Transport != "ssh-primary" || capture.ID == "" {
		t.Fatalf("invalid primary receipt %+v", capture)
	}
	state = &inBandMonitorState{marker: "__ERR__", startedAt: time.Now()}
	_, _, _, err = state.consume([]byte("__ERR__BEGIN__\npermission denied\n__ERR__END__:7__\n"))
	c := state.capture(err)
	if c.Good() || c.ExitCode == nil || *c.ExitCode != 7 || !strings.Contains(c.Stdout, "permission denied") {
		t.Fatalf("lost merged failure %+v", c)
	}
}
func TestPipelineStaleCandidateRefreshesAndRequiresNewPreflight(t *testing.T) {
	a := pipelineTestApp(t)
	id := "stale"
	mapID := probeAndMap(t, a, id, pipelineSource())
	c := createPipeline(t, a, id, mapID)
	oldID := c.id
	capture := c.panel.PipelineCaptures["ps"]
	capture.FinishedAt = time.Now().Add(-time.Minute)
	c.panel.PipelineCaptures["ps"] = capture
	if err := a.finishWidget(id, oldID, ""); err != nil {
		t.Fatal(err)
	}
	if !c.pipelineRefreshing || len(a.desktopState().WidgetCandidates) != 0 {
		t.Fatal("stale candidate offered for preflight")
	}
	pipelinePump(t, a, func() bool { return !c.pipelineRefreshing })
	if c.id == oldID {
		t.Fatal("old render receipt still valid")
	}
	if err := a.finishWidget(id, oldID, ""); err == nil {
		t.Fatal("stale preview accepted")
	}
	if err := a.finishWidget(id, c.id, ""); err != nil {
		t.Fatal(err)
	}
}
func TestPipelineRestoreRequiresFreshCaptureAndClosedDraftCannotResurrect(t *testing.T) {
	a := pipelineTestApp(t)
	def := widgets.Definition{Title: "历史面板", IntervalMS: 5000, View: pipelineView(), Pipeline: &monitorplan.Definition{ProtocolVersion: 2, Sources: []monitorplan.Source{pipelineSource()}, DataPlan: pipelinePlan(), Bindings: pipelineBindings()}}
	if err := a.restorePipeline(def); err != nil {
		t.Fatal(err)
	}
	if len(a.widgetCandidates) != 0 {
		t.Fatal("restored before capture")
	}
	var id string
	for key := range a.pipelineDrafts {
		id = key
	}
	pipelinePump(t, a, func() bool { return a.widgetCandidates[id] != nil && a.widgetCandidates[id].artifact != nil })
	c := a.widgetCandidates[id]
	if c.panel.WidgetData.Sequence != 1 {
		t.Fatal("missing restored sample")
	}
	if err := a.finishWidget(id, c.id, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.handleDesktopCommand(credentialui.WorkbenchCommand{Kind: "monitor-close", PanelID: id, Deadline: time.Now().Add(time.Second).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if a.panels.ByID(id) != nil {
		t.Fatal("close failed")
	}
	if err := a.finishWidget(id, c.id, ""); err == nil {
		t.Fatal("closed candidate revived")
	}
}
func TestLegacyHistoryAlsoCollectsBeforePreview(t *testing.T) {
	a := pipelineTestApp(t)
	d := widgets.Definition{ID: "legacy-restore", Command: "printf 'n=7'", Title: "旧面板", IntervalMS: 1000, Contract: widgets.Contract{Format: "kv", Fields: []widgets.Field{{Name: "n", Type: "number", History: true}}}, View: widgets.View{Kind: "custom", SDKVersion: 1, StateVersion: 1, Source: `export default function Widget({data}){return <p>{data?.n??'—'}</p>}`}}
	if err := a.restorePipeline(d); err != nil {
		t.Fatal(err)
	}
	pipelinePump(t, a, func() bool { return a.widgetCandidates[d.ID] != nil && a.widgetCandidates[d.ID].artifact != nil })
	c := a.widgetCandidates[d.ID]
	if c.panel.WidgetData.Data["n"] != float64(7) || a.desktopState().WidgetCandidates[0].Sample == nil {
		t.Fatal("legacy restore missing real sample")
	}
	if err := a.finishWidget(d.ID, c.id, ""); err != nil {
		t.Fatal(err)
	}
	if a.panels.ByID(d.ID).Origin != panel.OriginAI {
		t.Fatal("bad restored ownership")
	}
}

func TestPipelineRenderFailureKeepsActiveVersionAndReturnsDiagnostic(t *testing.T) {
	a := pipelineTestApp(t)
	id := "atomic"
	mapID := probeAndMap(t, a, id, pipelineSource())
	c := createPipeline(t, a, id, mapID)
	if err := a.finishWidget(id, c.id, ""); err != nil {
		t.Fatal(err)
	}
	old := a.panels.ByID(id)
	revision := old.DefinitionRevision
	_, _, err := a.executePanelAction(agent.PanelAction{Action: "update", Panel: agent.PanelSpec{ID: id, Title: "broken", ExpectedRevision: revision}})
	if err != nil {
		t.Fatal(err)
	}
	c = a.widgetCandidates[id]
	if err = a.finishWidget(id, c.id, "真实数据渲染失败"); err != nil {
		t.Fatal(err)
	}
	if old.DefinitionRevision != revision || old.Title == "broken" || len(old.WidgetData.History) != 1 {
		t.Fatal("failed update changed live source")
	}
	if !strings.Contains(a.pipelineFeedback[len(a.pipelineFeedback)-1], "真实数据渲染") {
		t.Fatal("AI did not receive preflight failure")
	}
}

type pipelinePhaseProvider struct {
	approvalTestProvider
	phase int
}

func (p *pipelinePhaseProvider) Stream(_ context.Context, request agent.Request, callbacks agent.Callbacks) (agent.Turn, error) {
	p.phase++
	spec := agent.PanelSpec{ID: "auto-pipeline", Title: "自动管线", Pipeline: &monitorplan.Request{ProtocolVersion: 2}}
	switch p.phase {
	case 1:
		spec.Pipeline.Sources = []monitorplan.Source{pipelineSource()}
		callbacks.OnPanelAction(agent.PanelAction{Action: "probe", Panel: spec})
	case 2:
		receipt := request.SystemContext["monitor_pipeline_receipt"]
		index := strings.Index(receipt, "[{\"captureId\"")
		if index < 0 {
			return agent.Turn{}, fmt.Errorf("missing actual receipt: %s", receipt)
		}
		var captures []monitorplan.Capture
		if err := json.Unmarshal([]byte(receipt[index:]), &captures); err != nil {
			return agent.Turn{}, err
		}
		if len(captures) != 1 || !captures[0].Good() {
			return agent.Turn{}, fmt.Errorf("invalid receipt")
		}
		plan := pipelinePlan()
		spec.Pipeline.Refs = []monitorplan.SourceRef{{SourceID: "ps", CaptureID: captures[0].ID}}
		spec.Pipeline.DataPlan = &plan
		callbacks.OnPanelAction(agent.PanelAction{Action: "map", Panel: spec})
	case 3:
		receipt := request.SystemContext["monitor_pipeline_receipt"]
		_, rest, ok := strings.Cut(receipt, "mapId=")
		if !ok {
			return agent.Turn{}, fmt.Errorf("missing map receipt")
		}
		spec.Pipeline.MapID = strings.Split(rest, "\n")[0]
		spec.Pipeline.Bindings = pipelineBindings()
		view := pipelineView()
		spec.View = &view
		callbacks.OnPanelAction(agent.PanelAction{Action: "create", Panel: spec})
	default:
		return agent.Turn{}, fmt.Errorf("unexpected phase %d", p.phase)
	}
	return agent.Turn{Session: agent.Session{ID: "pipeline-session"}, Type: "finished", Message: "等待客户端回执"}, nil
}
func TestPipelineAutomaticallyResumesAIAtActualCaptureAndMappingReceipts(t *testing.T) {
	a := pipelineTestApp(t)
	provider := &pipelinePhaseProvider{}
	a.agent = provider
	a.startAI("创建实时面板", false)
	deadline := time.After(10 * time.Second)
	for a.widgetCandidates["auto-pipeline"] == nil || a.widgetCandidates["auto-pipeline"].artifact == nil || a.agentBusy {
		a.schedulePipelines(time.Now())
		if a.monitorReview != nil {
			a.approveProposal()
		}
		a.continuePipelineAI()
		select {
		case e := <-a.events:
			a.handleEvent(e)
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatalf("continuation stalled: %+v", a.chat)
		}
	}
	if provider.phase != 3 || a.pipelineContinuations != 2 {
		t.Fatalf("phases=%d continuations=%d", provider.phase, a.pipelineContinuations)
	}
	userMessages := 0
	for _, message := range a.chat {
		if message.Role == "user" {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatal("native receipts inserted fake user turns")
	}
}

func TestPipelineAICloseAcceptsExistingPipelineMetadata(t *testing.T) {
	a := pipelineTestApp(t)
	id := "ai-close"
	mapID := probeAndMap(t, a, id, pipelineSource())
	c := createPipeline(t, a, id, mapID)
	if err := a.finishWidget(id, c.id, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.executePanelAction(agent.PanelAction{Action: "close", Panel: agent.PanelSpec{ID: id, Pipeline: &monitorplan.Request{ProtocolVersion: 2}}}); err != nil {
		t.Fatal(err)
	}
	if a.panels.ByID(id) != nil || a.pipelineDrafts[id] != nil {
		t.Fatal("AI close retained live pipeline")
	}
}

func TestExplicitProbeAuditsAndCollectsWithAutomaticCollectionDisabled(t *testing.T) {
	a := pipelineTestApp(t)
	a.config.SystemCollection = false
	_, _, err := a.executePanelAction(agent.PanelAction{Action: "probe", Panel: agent.PanelSpec{ID: "explicit", Pipeline: &monitorplan.Request{ProtocolVersion: 2, Sources: []monitorplan.Source{pipelineSource()}}}})
	if err != nil {
		t.Fatal(err)
	}
	a.scheduleMonitors()
	state := a.desktopState()
	if a.monitorReview == nil || state.Proposal == nil || !state.CanApprove {
		t.Fatal("explicit first sample did not publish an approvable audit")
	}
	a.approveProposal()
	pipelinePump(t, a, func() bool { return len(a.pipelineDrafts["explicit"].captures) > 0 })
	if a.config.SystemCollection {
		t.Fatal("explicit sampling enabled background collection")
	}
	a.scheduleMonitors()
	if a.inBandMonitor != nil || len(a.pipelineBatches) != 0 {
		t.Fatal("automatic collection started after the requested first sample")
	}
}
