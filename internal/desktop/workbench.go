package desktop

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
)

func (s *Service) exchangeWorkbench(session *terminalSession, w http.ResponseWriter, r *http.Request) {
	var input credentialui.WorkbenchExchange
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024*1024)).Decode(&input) != nil {
		http.Error(w, "invalid workbench data", http.StatusBadRequest)
		return
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		http.Error(w, "session closed", http.StatusGone)
		return
	}
	if err := acceptWidgets(session, input); err != nil {
		session.mu.Unlock()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for i := range input.State.Panels {
		p := &input.State.Panels[i]
		if p.Widget != nil {
			if p.Widget.SaveError == "" {
				p.Widget.SaveError = session.widgetSaveErrors[p.ID]
			}
		}
	}
	input.State.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.acceptTaskNotices(session, input.State.Notices, time.Now())
	session.workbench = &input.State
	changed := input.State.ConversationID != "" && session.info.ConversationID != input.State.ConversationID
	if changed {
		session.info.ConversationID = input.State.ConversationID
	}
	info := session.info
	for _, result := range input.Results {
		if reply := session.workbenchReplies[result.ID]; reply != nil {
			select {
			case reply <- result.Error:
			default:
			}
			delete(session.workbenchReplies, result.ID)
		}
	}
	commands := make([]credentialui.WorkbenchCommand, 0, len(session.workbenchCommands))
	for _, command := range session.workbenchCommands {
		if session.workbenchReplies[command.ID] != nil && command.Deadline > time.Now().UnixMilli() {
			commands = append(commands, command)
		}
	}
	session.workbenchCommands = nil
	session.mu.Unlock()
	if len(input.Widgets.Definitions) > 0 {
		s.saveWidgetDefinitions(session, input.Widgets.Definitions)
	}
	if changed {
		s.emit("session-updated", info)
	}
	s.emit("workbench:"+session.id, input.State)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(commands)
}

func (s *Service) GetWorkbench(id string) (*credentialui.WorkbenchState, error) {
	session, err := s.session(id)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.workbench, nil
}

// Commands use their own structured channel. No AI text is pasted into a shell.
func (s *Service) WorkbenchAction(id string, command credentialui.WorkbenchCommand) error {
	switch command.Kind {
	case "execution-collect", "terminal-takeover", "bastion-compatibility", "system-collection", "model", "workspace", "widget-cancel", "widget-ready", "widget-failed", "widget-restore", "prompt", "approve", "reject", "monitor-open", "monitor-close", "monitor-refresh", "alert-close", "alert-refresh":
	default:
		return errors.New("不支持的工作台操作")
	}
	if command.Kind == "widget-failed" && command.Text == "" {
		return errors.New("缺少组件错误信息")
	}
	if command.Kind == "widget-ready" && command.Text != "" {
		return errors.New("无效组件预检回执")
	}
	if len(command.Text) > 64*1024 {
		return errors.New("输入内容过长")
	}
	session, err := s.session(id)
	if err != nil {
		return err
	}
	reply := make(chan string, 1)
	session.mu.Lock()
	if session.closed || session.workbench == nil {
		session.mu.Unlock()
		return errors.New("工作台尚未就绪或连接已结束")
	}
	if len(session.workbenchReplies) >= 8 {
		session.mu.Unlock()
		return errors.New("操作正在处理，请稍后重试")
	}
	command.ID = catalog.NewID()
	command.Deadline = time.Now().Add(5 * time.Second).UnixMilli()
	if session.workbenchReplies == nil {
		session.workbenchReplies = make(map[string]chan string)
	}
	session.workbenchReplies[command.ID] = reply
	session.workbenchCommands = append(session.workbenchCommands, command)
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		delete(session.workbenchReplies, command.ID)
		session.mu.Unlock()
	}()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	select {
	case message := <-reply:
		if message != "" {
			return errors.New(message)
		}
		return nil
	case <-timer.C:
		return errors.New("工作台响应超时，请核对当前状态后重试")
	case <-s.ctx.Done():
		return errors.New("客户端已关闭")
	}
}
