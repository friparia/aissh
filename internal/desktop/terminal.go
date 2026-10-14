package desktop

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os/exec"
	"sort"
	"sync"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/credentialui"
	"git.example.com/ai/wrd-ai-cli/internal/widgets"
)

type terminalSession struct {
	widgetArtifacts   map[string]widgets.Artifact
	widgetData        map[string]widgets.Data
	widgetDefinitions map[string]widgets.Definition
	widgetSaveErrors  map[string]string

	mu                sync.Mutex
	id, hostID        string
	aiProfileID       string
	cmd               *exec.Cmd
	pty               io.ReadWriteCloser
	cancel            context.CancelFunc
	closeChannel      func()
	started           bool
	closed            bool
	info              SessionInfo
	workbench         *credentialui.WorkbenchState
	workbenchCommands []credentialui.WorkbenchCommand
	workbenchReplies  map[string]chan string
	lastTaskNotice    uint64 // guarded by mu; snapshots may resend the same notices
}
type SessionInfo struct {
	ID             string `json:"id"`
	HostID         string `json:"hostId"`
	ConversationID string `json:"conversationId"`
	Name           string `json:"name"`
	Address        string `json:"address"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	CreatedAt      string `json:"createdAt"`
	EndedAt        string `json:"endedAt"`
	Status         string `json:"status"` // starting, running (local workbench), ended, failed
}

func (s *Service) ListSessions() []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]SessionInfo, 0, len(s.sessions))
	for _, session := range s.sessions {
		session.mu.Lock()
		result = append(result, session.info)
		session.mu.Unlock()
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt < result[j].CreatedAt })
	return result
}

func (s *Service) CreateSession(hostID string) (SessionInfo, error) {
	return s.createSession(hostID, "")
}

func (s *Service) createSession(hostID, conversationID string) (SessionInfo, error) {
	d, err := s.store.Load()
	if err != nil {
		return SessionInfo{}, err
	}
	h, err := d.Host(hostID)
	if err != nil {
		return SessionInfo{}, err
	}
	selected := ""
	if conversationID != "" {
		conversation, err := s.conversation(hostID, conversationID)
		if err != nil {
			return SessionInfo{}, err
		}
		selected = conversation.AIProfileID
	}
	selected, _, err = conversationAIProfile(d, h, selected)
	if err != nil {
		return SessionInfo{}, err
	}
	if _, err := cliPath(); err != nil {
		return SessionInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	live := 0
	for _, session := range s.sessions {
		session.mu.Lock()
		if conversationID != "" && !session.closed && session.hostID == hostID && session.info.ConversationID == conversationID {
			info := session.info
			session.mu.Unlock()
			return info, nil
		}
		if !session.closed {
			live++
		}
		session.mu.Unlock()
	}
	if live >= 12 {
		return SessionInfo{}, errors.New("最多同时运行 12 个会话，请先断开不用的连接")
	}
	if len(s.sessions) >= 50 {
		return SessionInfo{}, errors.New("最多保留 50 个终端标签，请先清理已结束的会话")
	}
	if err := s.store.RecordConnection(hostID); err != nil {
		return SessionInfo{}, err
	}
	id := catalog.NewID()
	info := SessionInfo{ID: id, HostID: hostID, ConversationID: conversationID, Name: h.Name, Address: h.Address, Port: h.Port, User: h.User, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Status: "starting"}
	s.sessions[id] = &terminalSession{id: id, hostID: hostID, aiProfileID: selected, info: info}
	return info, nil
}
func (s *Service) session(id string) (*terminalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[id]
	if session == nil {
		return nil, errors.New("终端会话不存在")
	}
	return session, nil
}
func dimensions(cols, rows int) (int, int) {
	if cols < 20 {
		cols = 20
	}
	if cols > 500 {
		cols = 500
	}
	if rows < 5 {
		rows = 5
	}
	if rows > 200 {
		rows = 200
	}
	return cols, rows
}
func (s *Service) StartSession(id string, cols, rows int) (startErr error) {
	session, err := s.session(id)
	if err != nil {
		return err
	}
	session.mu.Lock()
	defer func() {
		if startErr != nil && !session.closed {
			session.closed = true
			session.info.Status = "failed"
			session.info.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		info := session.info
		s.emit("session-updated", info)
		session.mu.Unlock()
	}()
	if session.closed {
		return errors.New("会话已关闭")
	}
	if session.started {
		return nil
	}
	helper, err := cliPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	args := []string{"--no-workspace", "--profile", session.hostID, "--ai-profile", session.aiProfileID}
	if session.info.ConversationID != "" {
		args = append(args, "--history-dir", s.history.Dir, "--conversation", session.info.ConversationID)
	}
	cmd := exec.CommandContext(ctx, helper, args...)
	cmd.Env = append(desktopEnvironment(), "TERM=xterm-256color", "AISSH_CONFIG_DIR="+s.store.Dir)
	channelEnv, closeChannel, err := s.credentialChannel(session)
	if err != nil {
		cancel()
		return err
	}
	cmd.Env = append(cmd.Env, channelEnv...)
	cols, rows = dimensions(cols, rows)
	terminal, err := startTerminal(cmd, cols, rows)
	if err != nil {
		cancel()
		closeChannel()
		return err
	}
	session.closeChannel = closeChannel
	session.started = true
	session.info.Status = "running"
	session.cmd = cmd
	session.cancel = cancel
	session.pty = terminal
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buffer := make([]byte, 32*1024)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				s.emit("terminal:"+id, base64.StdEncoding.EncodeToString(buffer[:n]))
			}
			if err != nil {
				break
			}
		}
	}()
	go func() {
		err := waitTerminal(cmd)
		// ConPTY may keep its output pipe open after process exit.
		timer := time.NewTimer(time.Second)
		select {
		case <-outputDone:
		case <-timer.C:
		}
		timer.Stop()
		message := "连接已关闭"
		if err != nil {
			message = "连接已结束，请查看上方终端信息"
		}
		status := "ended"
		if err != nil {
			status = "failed"
		}
		session.finish(status)
		session.mu.Lock()
		info := session.info
		session.mu.Unlock()
		s.emit("session-updated", info)
		s.emit("terminal-exit:"+id, message)
	}()
	return nil
}
func (s *Service) WriteTerminal(id, data string) error {
	if len(data) > 512*1024 {
		return errors.New("单次终端输入过大")
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	session, err := s.session(id)
	if err != nil {
		return err
	}
	session.mu.Lock()
	terminal := session.pty
	closed := session.closed
	session.mu.Unlock()
	if terminal == nil || closed {
		return errors.New("终端连接已关闭")
	}
	_, err = terminal.Write(decoded)
	return err
}
func (s *Service) ResizeTerminal(id string, cols, rows int) error {
	session, err := s.session(id)
	if err != nil {
		return err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.pty == nil || session.closed {
		return nil
	}
	cols, rows = dimensions(cols, rows)
	return resizeTerminal(session.pty, cols, rows)
}
func (s *Service) CloseSession(id string) error {
	s.mu.Lock()
	session := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if session != nil {
		session.close()
	}
	return nil
}
func (s *terminalSession) close() {
	s.finish("ended")
}
func (s *terminalSession) finish(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for id, reply := range s.workbenchReplies {
		select {
		case reply <- "连接已结束":
		default:
		}
		delete(s.workbenchReplies, id)
	}
	s.workbenchCommands = nil
	s.info.Status = status
	s.info.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if s.cmd != nil && s.cmd.Process != nil {
		stopTerminal(s.cmd)
	}
	if s.pty != nil {
		_ = s.pty.Close()
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.closeChannel != nil {
		s.closeChannel()
	}
}
