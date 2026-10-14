package app

import (
	"fmt"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/resources"
)

func (a *App) safeText(value string) string { return a.config.SecretFilter.Text(value) }
func (a *App) prepareAgentRequest(request *agent.Request) {
	request.Prompt = a.safeText(request.Prompt)
	request.RecentOutput = a.safeText(request.RecentOutput)
	request.Output = a.safeText(request.Output)
	for key, value := range request.SystemContext {
		request.SystemContext[key] = a.safeText(value)
	}
	if a.config.Resources != nil {
		if request.SystemContext == nil {
			request.SystemContext = map[string]string{}
		}
		request.SystemContext["saved_systems"] = a.config.Resources.Descriptions()
	}
	if request.Target != nil && a.config.HostLabel != "" {
		target := *request.Target
		target.HostLabel = a.config.HostLabel
		request.Target = &target
	}
}

// resourceProposal intercepts the entire reserved operation before the SSH
// writer. Even malformed operations are rejected locally, never sent to a shell.
func (a *App) resourceProposal(command string) bool {
	return strings.HasPrefix(strings.TrimSpace(command), "@resource")
}
func (a *App) executeResource(command, proposalID, sessionID string, approved bool) {
	op, err := resources.Parse(strings.TrimSpace(command))
	if err != nil || a.config.Resources == nil {
		message := "当前连接未启用系统凭据"
		if err != nil {
			message = err.Error()
		}
		a.updateSSHActivity(proposalID, "failed", message)
		a.appendPersistentChat(chatMessage{Role: "error", Content: message, Time: time.Now()})
		a.agentBusy = false
		a.proposal = nil
		a.focus = focusTerminal
		a.dirty = true
		return
	}
	a.agentBusy = true
	a.agentStatus = "正在访问已绑定的系统"
	a.updateSSHActivity(proposalID, "running", "")
	a.proposal = nil
	a.approvalInput = nil
	a.approvalStatus = ""
	a.focus = focusTerminal
	a.dirty = true
	if a.inlineAI != nil {
		a.agentInline = a.inlineAI
		a.inlineAI.Status = a.agentStatus
		a.inlineAI.Done = false
	}
	language := a.agentLanguage
	panelContext := agent.Request{}
	a.addPanelContext(&panelContext)
	opScope := a.beginAgentOperation()
	go func() {
		if !approved {
			// Saved grants authorize this operation locally. Notify stateful
			// providers before submitting its result, just as manual approval does.
			if _, err := a.agent.Action(opScope.ctx, agent.Request{Event: "approve", SessionID: sessionID, ProposalID: proposalID}); err != nil {
				if a.config.DesktopUI {
					opScope.send(sshActivityEvent{proposalID, "failed", a.safeText(err.Error())})
				}
				opScope.send(executionResultEvent{err: fmt.Errorf("%s", a.safeText(err.Error()))})
				return
			}
		}
		result, err := a.config.Resources.Execute(opScope.ctx, op, approved)
		completion := "操作成功"
		if err != nil {
			result = err.Error()
			completion = "操作失败"
		}
		if a.config.DesktopUI {
			status := "completed"
			if err != nil {
				status = "failed"
			}
			opScope.send(sshActivityEvent{proposalID, status, a.safeText(result)})
		}
		request := agent.Request{Event: "result", SessionID: sessionID, ProposalID: proposalID, Output: result, Completion: completion, SystemContext: map[string]string{"response_language": language}}
		for key, value := range panelContext.SystemContext {
			request.SystemContext[key] = value
		}
		a.prepareAgentRequest(&request)
		turn, streamErr := a.agent.Stream(opScope.ctx, request, agent.Callbacks{
			OnStatus:   func(s string) { opScope.send(agentStatusEvent{status: a.safeText(s)}) },
			OnThought:  func(s string) { opScope.send(agentThoughtEvent{content: a.safeText(s)}) },
			OnAnswer:   func(s string) { opScope.send(agentAnswerEvent{content: s}) },
			OnActivity: func(value agent.Activity) { opScope.send(agentActivityEvent{activity: value}) },
		})
		if streamErr != nil {
			streamErr = fmt.Errorf("%s", a.safeText(streamErr.Error()))
		}
		opScope.send(executionResultEvent{turn: turn, err: streamErr})
	}()
}
