package desktop

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/history"
	"git.example.com/ai/wrd-ai-cli/internal/localworkspace"
)

// ConversationInfo is independent of the lifetime of its SSH terminal.
type ConversationInfo struct {
	AIModel       string `json:"aiModel,omitempty"`
	AIProfileID   string `json:"aiProfileId,omitempty"`
	CodexThreadID string `json:"codexThreadId,omitempty"`
	ID            string `json:"id"`
	HostID        string `json:"hostId"`
	Title         string `json:"title"`
	Preview       string `json:"preview"`
	StartedAt     string `json:"startedAt"`
	UpdatedAt     string `json:"updatedAt"`
	MessageCount  int    `json:"messageCount"`
}

var errConversationNotFound = errors.New("这台服务器上未找到该 AI 会话，请刷新后重试")

func conversationInfo(hostID string, c history.Conversation) ConversationInfo {
	info := ConversationInfo{AIModel: c.AIModel, AIProfileID: c.AIProfileID, CodexThreadID: c.CodexThreadID, ID: c.ID, HostID: hostID, Title: c.Title,
		StartedAt: c.StartedAt.Format(time.RFC3339Nano), UpdatedAt: c.UpdatedAt.Format(time.RFC3339Nano)}
	for _, message := range c.Messages {
		if message.Role == "system" {
			continue
		}
		info.MessageCount++
		preview := []rune(strings.Join(strings.Fields(message.Content), " "))
		if len(preview) > 160 {
			preview = append(preview[:160], '…')
		}
		info.Preview = string(preview)
	}
	return info
}

func (s *Service) ListConversations() ([]ConversationInfo, error) {
	d, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	result := make([]ConversationInfo, 0)
	for _, host := range d.AllHosts() {
		conversations, err := s.history.LoadConversations("profile:" + host.ID)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 的 AI 会话：%w", host.Name, err)
		}
		for _, conversation := range conversations {
			result = append(result, conversationInfo(host.ID, conversation))
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, result[i].UpdatedAt)
		right, _ := time.Parse(time.RFC3339Nano, result[j].UpdatedAt)
		return left.After(right)
	})
	return result, nil
}

type DirectConversation struct {
	Host         catalog.Host     `json:"host"`
	Conversation ConversationInfo `json:"conversation"`
}

func (s *Service) CreateDirectConversation(host catalog.Host, saveHost bool) (DirectConversation, error) {
	return s.CreateDirectConversationWithAI(host, saveHost, "")
}

func (s *Service) CreateDirectConversationWithAI(host catalog.Host, saveHost bool, aiProfileID string) (DirectConversation, error) {
	if s.history == nil || s.history.Dir == "" {
		return DirectConversation{}, errors.New("AI 历史已禁用，请启用本机历史后新建对话")
	}
	data, err := s.store.Load()
	if err != nil {
		return DirectConversation{}, err
	}
	selected, provider, err := conversationAIProfile(data, host, aiProfileID)
	if err != nil {
		return DirectConversation{}, err
	}
	if provider != "codex" && provider != "claude" {
		host.Workspace = ""
	}
	workspace, err := localworkspace.Resolve(host.Workspace)
	if err != nil {
		return DirectConversation{}, fmt.Errorf("无法访问本机工作目录：%w", err)
	}
	host.Workspace = workspace
	host, err = s.store.CreateConversationHost(host, saveHost)
	if err != nil {
		return DirectConversation{}, err
	}
	conversation, err := s.CreateConversationWithAI(host.ID, workspace, selected)
	if err != nil {
		if cleanupErr := s.store.Delete("host", host.ID); cleanupErr != nil {
			return DirectConversation{}, fmt.Errorf("%w；清理连接信息失败：%v", err, cleanupErr)
		}
		return DirectConversation{}, err
	}
	return DirectConversation{Host: host, Conversation: conversation}, nil
}

func (s *Service) conversation(hostID, id string) (history.Conversation, error) {
	d, err := s.store.Load()
	if err != nil {
		return history.Conversation{}, err
	}
	if _, err := d.Host(hostID); err != nil {
		return history.Conversation{}, err
	}
	conversations, err := s.history.LoadConversations("profile:" + hostID)
	if err != nil {
		return history.Conversation{}, err
	}
	for _, c := range conversations {
		if c.ID == id {
			return c, nil
		}
	}
	return history.Conversation{}, errConversationNotFound
}

func (s *Service) GetConversation(hostID, id string) ([]history.Message, error) {
	c, err := s.conversation(hostID, id)
	return c.Messages, err
}

func (s *Service) CreateConversation(hostID string) (ConversationInfo, error) {
	d, err := s.store.Load()
	if err != nil {
		return ConversationInfo{}, err
	}
	host, err := d.Host(hostID)
	if err != nil {
		return ConversationInfo{}, err
	}
	return s.CreateConversationWithWorkspace(hostID, host.Workspace)
}

// A conversation's directory is independent of the saved host and other chats.
func (s *Service) CreateConversationWithWorkspace(hostID, directory string) (ConversationInfo, error) {
	return s.CreateConversationWithAI(hostID, directory, "")
}

func (s *Service) CreateConversationWithAI(hostID, directory, aiProfileID string) (ConversationInfo, error) {
	if s.history == nil || s.history.Dir == "" {
		return ConversationInfo{}, errors.New("AI 历史已禁用，请启用本机历史后新建对话")
	}
	d, err := s.store.Load()
	if err != nil {
		return ConversationInfo{}, err
	}
	host, err := d.Host(hostID)
	if err != nil {
		return ConversationInfo{}, err
	}
	selected, provider, err := conversationAIProfile(d, host, aiProfileID)
	if err != nil {
		return ConversationInfo{}, err
	}
	if provider != "codex" && provider != "claude" {
		directory = ""
	}
	workspace, err := localworkspace.Resolve(directory)
	if err != nil {
		return ConversationInfo{}, fmt.Errorf("无法访问本机工作目录：%w", err)
	}
	now := time.Now().UTC()
	id := history.NewConversationID()
	// Persist an empty conversation without inserting a synthetic user prompt.
	if err := s.history.Append("profile:"+hostID, history.Message{
		AIProfileID: selected, ConversationID: id, Role: "system", Content: "新对话", Time: now.Format(time.RFC3339Nano), Workspace: &workspace,
	}); err != nil {
		return ConversationInfo{}, err
	}
	return conversationInfo(hostID, history.Conversation{AIProfileID: selected, ID: id, Title: "新对话", StartedAt: now, UpdatedAt: now}), nil
}

func (s *Service) OpenConversationSession(hostID, id string) (SessionInfo, error) {
	if _, err := s.conversation(hostID, id); err != nil {
		return SessionInfo{}, err
	}
	return s.createSession(hostID, id)
}

func (s *Service) ReconnectSession(id string) (SessionInfo, error) {
	session, err := s.session(id)
	if err != nil {
		return SessionInfo{}, err
	}
	session.mu.Lock()
	info := session.info
	session.mu.Unlock()
	if info.ConversationID != "" {
		if _, err := s.conversation(info.HostID, info.ConversationID); err == nil {
			return s.createSession(info.HostID, info.ConversationID)
		} else if !errors.Is(err, errConversationNotFound) {
			return SessionInfo{}, err
		}
	}
	// A plain SSH terminal may never have sent an AI message. It has no
	// persistent context to restore and can reconnect as a fresh terminal.
	return s.CreateSession(info.HostID)
}

func conversationAIProfile(data catalog.Data, host catalog.Host, selected string) (string, string, error) {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		selected = host.AIProfileID
	}
	if selected == "" {
		selected = data.DefaultAIProfileID
	}
	if selected == "" || selected == catalog.BuiltinCodexProfileID {
		return catalog.BuiltinCodexProfileID, "codex", nil
	}
	profile, err := data.AIProfile(selected)
	if err != nil {
		return "", "", err
	}
	return profile.ID, profile.Provider, nil
}
