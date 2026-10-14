package history

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
)

const (
	DefaultMaxEntries    = 500
	DefaultMaxFileSize   = int64(8 * 1024 * 1024)
	LegacyConversationID = "legacy"
)

type Message struct {
	AIModel        string          `json:"aiModel,omitempty"`
	AIProfileID    string          `json:"aiProfileId,omitempty"`
	CodexThreadID  string          `json:"codexThreadId,omitempty"`
	Version        int             `json:"version"`
	Target         string          `json:"target"`
	ConversationID string          `json:"conversationId,omitempty"`
	Role           string          `json:"role"`
	Content        string          `json:"content"`
	Time           string          `json:"time"`
	Proposal       *agent.Proposal `json:"proposal,omitempty"`
	Workspace      *string         `json:"workspace,omitempty"`
}

type Conversation struct {
	AIModel       string
	AIProfileID   string
	CodexThreadID string
	ID            string
	Title         string
	StartedAt     time.Time
	UpdatedAt     time.Time
	Messages      []Message
	Workspace     *string // nil preserves the host default for older conversations
}

type Store struct {
	Dir         string
	MaxEntries  int
	MaxFileSize int64
}

func New(dir string) *Store {
	return &Store{
		Dir:         strings.TrimSpace(dir),
		MaxEntries:  DefaultMaxEntries,
		MaxFileSize: DefaultMaxFileSize,
	}
}

func DefaultDir() string {
	if value, err := os.UserConfigDir(); err == nil && strings.TrimSpace(value) != "" {
		return filepath.Join(value, "aissh", "history")
	}
	if value, err := os.UserHomeDir(); err == nil && strings.TrimSpace(value) != "" {
		return filepath.Join(value, ".aissh", "history")
	}
	return ""
}

func (s *Store) Load(target string) ([]Message, error) {
	if s == nil || strings.TrimSpace(s.Dir) == "" || strings.TrimSpace(target) == "" {
		return nil, nil
	}
	file, err := os.Open(s.path(target))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	limit := s.entryLimit()
	messages := make([]Message, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var message Message
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Target != target || strings.TrimSpace(message.Content) == "" {
			continue
		}
		messages = append(messages, message)
		if len(messages) > limit {
			copy(messages, messages[len(messages)-limit:])
			messages = messages[:limit]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) LoadConversations(target string) ([]Conversation, error) {
	messages, err := s.Load(target)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*Conversation)
	var conversations []*Conversation
	for _, message := range messages {
		conversationID := strings.TrimSpace(message.ConversationID)
		if conversationID == "" {
			conversationID = LegacyConversationID
		}
		conversation := byID[conversationID]
		if conversation == nil {
			conversation = &Conversation{ID: conversationID}
			byID[conversationID] = conversation
			conversations = append(conversations, conversation)
		}
		conversation.Messages = append(conversation.Messages, message)
		if message.AIModel != "" {
			conversation.AIModel = message.AIModel
		}
		if message.AIProfileID != "" {
			conversation.AIProfileID = message.AIProfileID
		}
		if message.CodexThreadID != "" {
			conversation.CodexThreadID = message.CodexThreadID
		}
		if message.Workspace != nil {
			conversation.Workspace = message.Workspace
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, message.Time)
		if !createdAt.IsZero() {
			if conversation.StartedAt.IsZero() || createdAt.Before(conversation.StartedAt) {
				conversation.StartedAt = createdAt
			}
			if conversation.UpdatedAt.IsZero() || createdAt.After(conversation.UpdatedAt) {
				conversation.UpdatedAt = createdAt
			}
		}
		if conversation.Title == "" && message.Role == "user" {
			conversation.Title = conversationTitle(message.Content)
		}
	}
	result := make([]Conversation, 0, len(conversations))
	for _, conversation := range conversations {
		if conversation.Title == "" {
			conversation.Title = conversationTitle(conversation.Messages[0].Content)
		}
		result = append(result, *conversation)
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].UpdatedAt.After(result[right].UpdatedAt)
	})
	return result, nil
}

func NewConversationID() string {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return time.Now().UTC().Format("20060102T150405.000000000") + "-" + hex.EncodeToString(random)
}

func (s *Store) Append(target string, message Message) error {
	if s == nil || strings.TrimSpace(s.Dir) == "" || strings.TrimSpace(target) == "" || strings.TrimSpace(message.Content) == "" {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(s.Dir, 0700); err != nil {
		return err
	}
	message.Version = 2
	message.Target = target
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.path(target)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Stat(path)
	if err == nil && info.Size() > s.fileSizeLimit() {
		return s.compact(target)
	}
	return nil
}

func (s *Store) Path(target string) string {
	if s == nil || strings.TrimSpace(s.Dir) == "" {
		return ""
	}
	return s.path(target)
}

func (s *Store) compact(target string) error {
	messages, err := s.Load(target)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.Dir, ".history-*.jsonl")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	for _, message := range messages {
		if err := encoder.Encode(message); err != nil {
			return err
		}
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, s.path(target)); err != nil {
		return err
	}
	keep = true
	return nil
}

func (s *Store) path(target string) string {
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(s.Dir, safeTargetName(target)+"-"+hex.EncodeToString(sum[:8])+".jsonl")
}

func safeTargetName(target string) string {
	var value strings.Builder
	for _, current := range strings.TrimSpace(target) {
		if unicode.IsLetter(current) || unicode.IsDigit(current) || current == '-' || current == '_' || current == '.' {
			value.WriteRune(current)
		} else {
			value.WriteByte('_')
		}
		if value.Len() >= 48 {
			break
		}
	}
	result := strings.Trim(value.String(), "_.-")
	if result == "" {
		return "target"
	}
	return result
}

func conversationTitle(content string) string {
	value := strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if value == "" {
		return "Untitled"
	}
	runes := []rune(value)
	if len(runes) > 36 {
		value = string(runes[:36]) + "…"
	}
	return value
}

func (s *Store) entryLimit() int {
	if s.MaxEntries > 0 {
		return s.MaxEntries
	}
	return DefaultMaxEntries
}

func (s *Store) fileSizeLimit() int64 {
	if s.MaxFileSize > 0 {
		return s.MaxFileSize
	}
	return DefaultMaxFileSize
}
