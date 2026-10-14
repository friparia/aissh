package app

import (
	"errors"
	"slices"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
)

func (a *App) switchConversationModel(model string) error {
	if a.agentBusy || a.proposal != nil || a.execution != nil || a.executionRecovery {
		return errors.New("请等待当前任务完成，或先处理命令建议")
	}
	provider, ok := a.agent.(*agent.DirectModelProvider)
	if !ok || agentMode(a.config) != "model" {
		return errors.New("当前 AI 配置不支持切换兼容 OpenAI API 模型")
	}
	if model == "" || !slices.Contains(a.config.ModelOptions, model) {
		return errors.New("请选择当前 AI 配置中已保存的模型")
	}
	if provider.CurrentModel() == model {
		return nil
	}
	provider.SetModel(model)
	a.config.ModelName = model
	a.appendPersistentChat(chatMessage{Role: "system", Content: "已切换 AI 模型：" + model, Time: time.Now()})
	a.dirty = true
	return nil
}
