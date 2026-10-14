package desktop

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/catalog"
	"git.example.com/ai/wrd-ai-cli/internal/history"
	"git.example.com/ai/wrd-ai-cli/internal/redact"
	wails "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (s *Service) ListKnowledge(query string) ([]catalog.Knowledge, error) {
	items, err := s.store.ListKnowledge()
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return items, nil
	}
	data, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	result := []catalog.Knowledge{}
	for _, item := range items {
		host, _ := data.Host(item.HostID)
		resource, _ := data.Resource(item.ResourceID)
		parts := []string{item.Title, item.Content, item.FilePath, item.Source, item.SourceURL, host.Name, resource.Name}
		parts = append(parts, item.Tags...)
		parts = append(parts, item.Columns...)
		for _, row := range item.Rows {
			parts = append(parts, row...)
		}
		if strings.Contains(strings.ToLower(strings.Join(parts, " ")), query) {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *Service) SaveKnowledge(k catalog.Knowledge) (catalog.Knowledge, error) {
	return s.store.SaveKnowledge(k)
}
func (s *Service) DeleteKnowledge(id, updatedAt string) error {
	return s.store.DeleteKnowledge(id, updatedAt)
}

// ParseKnowledgeTable accepts CSV exports and tab-separated cells copied from
// Excel/online spreadsheets. It creates a local draft, never changes the source.
func (s *Service) ParseKnowledgeTable(text string) (catalog.Knowledge, error) {
	k := catalog.Knowledge{Kind: "data", Title: "导入的数据表", Tags: []string{}, Columns: []string{}, Rows: [][]string{}}
	if len(text) > 8*1024*1024 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return k, errors.New("请导入不超过 8 MB 的 UTF-8 CSV 或粘贴表格内容")
	}
	text = strings.TrimLeft(strings.TrimPrefix(text, "\ufeff"), "\r\n")
	reader := csv.NewReader(strings.NewReader(text))
	// A tab inside a quoted CSV header is content, not a delimiter.
	quoted := false
	for _, char := range text {
		if char == '"' {
			quoted = !quoted
		}
		if !quoted && (char == '\n' || char == '\r') {
			break
		}
		if !quoted && char == '\t' {
			reader.Comma = '\t'
			break
		}
	}
	columns, err := reader.Read()
	if err != nil {
		return k, errors.New("无法读取表头，请将列名放在第一行")
	}
	if len(columns) > 50 {
		return k, errors.New("数据表最多支持 50 列")
	}
	seen := map[string]bool{}
	for i, name := range columns {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return k, errors.New("表头不能为空或重复")
		}
		seen[name] = true
		columns[i] = name
	}
	k.Columns = columns
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return k, fmt.Errorf("表格格式无效，请检查引号和每行列数：%w", err)
		}
		if len(k.Rows) >= 5000 {
			return k, errors.New("数据表最多支持 5000 条记录")
		}
		k.Rows = append(k.Rows, row)
	}
	return k, nil
}

func (s *Service) PickKnowledgeTable() (*catalog.Knowledge, error) {
	path, err := wails.OpenFileDialog(s.ctx, wails.OpenDialogOptions{Title: "导入数据表", Filters: []wails.FileFilter{{DisplayName: "CSV / TSV 数据表", Pattern: "*.csv;*.tsv"}}})
	if err != nil || path == "" {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("请选择普通 CSV / TSV 文件")
	}
	content, err := io.ReadAll(io.LimitReader(f, 8*1024*1024+1))
	if err != nil {
		return nil, err
	}
	k, err := s.ParseKnowledgeTable(string(content))
	k.Title, k.Source = filepath.Base(path), "导入自："+path
	return &k, err
}

func (s *Service) OpenKnowledgeSource(id string) error {
	items, err := s.store.ListKnowledge()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == id && item.SourceURL != "" {
			if err := catalog.KnowledgeSourceURL(item.SourceURL); err != nil {
				return err
			}
			wails.BrowserOpenURL(s.ctx, item.SourceURL)
			return nil
		}
	}
	return errors.New("该条目未设置在线来源")
}

func knowledgeDocument(path string) (catalog.Knowledge, error) {
	k := catalog.Knowledge{Kind: "document", Title: filepath.Base(path), FilePath: path, Tags: []string{}, Columns: []string{}, Rows: [][]string{}}
	if !filepath.IsAbs(path) {
		return k, errors.New("请选择本机文件的绝对路径")
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".txt", ".md", ".csv", ".tsv", ".json", ".yaml", ".yml", ".log", ".pdf", ".docx", ".xlsx", ".pptx", ".doc", ".xls", ".ppt", ".rtf":
	default:
		return k, errors.New("请选择文本、PDF 或 Office 文档")
	}
	f, err := os.Open(path)
	if err != nil {
		return k, errors.New("文档不存在或不可读，请重新选择")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return k, errors.New("请选择普通文档文件")
	}
	switch ext {
	case ".txt", ".md", ".csv", ".tsv", ".json", ".yaml", ".yml", ".log":
		if info.Size() <= 256*1024 {
			content, err := io.ReadAll(io.LimitReader(f, 256*1024+1))
			if err != nil {
				return k, err
			}
			if len(content) <= 256*1024 && utf8.Valid(content) && !strings.ContainsRune(string(content), 0) {
				k.Content = string(content)
			}
		}
	}
	return k, nil
}

// Documents remain at their original location. Text can be indexed locally;
// binary documents are referenced and opened with the system's default app.
func (s *Service) PickKnowledgeDocument() (*catalog.Knowledge, error) {
	path, err := wails.OpenFileDialog(s.ctx, wails.OpenDialogOptions{Title: "添加本地文档", Filters: []wails.FileFilter{{DisplayName: "文档与数据文件", Pattern: "*.txt;*.md;*.csv;*.tsv;*.json;*.yaml;*.yml;*.log;*.pdf;*.docx;*.xlsx;*.pptx;*.doc;*.xls;*.ppt;*.rtf"}}})
	if err != nil || path == "" {
		return nil, err
	}
	k, err := knowledgeDocument(path)
	return &k, err
}

func (s *Service) OpenKnowledgeDocument(id string) error {
	items, err := s.store.ListKnowledge()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID != id || item.Kind != "document" {
			continue
		}
		if _, err := knowledgeDocument(item.FilePath); err != nil {
			return err
		}
		var command *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			command = exec.Command("open", item.FilePath)
		case "windows":
			command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", item.FilePath)
		default:
			command = exec.Command("xdg-open", item.FilePath)
		}
		return command.Run()
	}
	return errors.New("未找到本地文档")
}

func summaryHistory(messages []history.Message) (string, bool) {
	parts := []string{}
	size := 0
	truncated := false
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == "system" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		// Encode records as data so embedded role labels cannot forge boundaries.
		raw, _ := json.Marshal(struct {
			Role     string          `json:"role"`
			Content  string          `json:"content"`
			Proposal *agent.Proposal `json:"proposal,omitempty"`
		}{m.Role, m.Content, m.Proposal})
		if size+len(raw) > 128*1024 {
			truncated = true
			break
		}
		parts = append([]string{string(raw)}, parts...)
		size += len(raw)
	}
	return strings.Join(parts, "\n"), truncated
}

func summaryDraft(ctx context.Context, provider agent.Provider, conversation history.Conversation, hostID string, filter *redact.Filter) (catalog.Knowledge, error) {
	input, truncated := summaryHistory(conversation.Messages)
	if input == "" {
		return catalog.Knowledge{}, errors.New("该会话没有可总结的内容，或单条消息过长")
	}
	request := agent.NewSummaryRequest(filter.Text(input))
	var turn agent.Turn
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return catalog.Knowledge{}, fmt.Errorf("生成总结失败：%s", filter.Text(err.Error()))
		}
		panelRequested := false
		var err error
		turn, err = provider.Stream(ctx, request, agent.Callbacks{
			OnPanelAction: func(agent.PanelAction) { panelRequested = true },
		})
		if err != nil {
			return catalog.Knowledge{}, fmt.Errorf("生成总结失败：%s", filter.Text(err.Error()))
		}
		if turn.Error != "" {
			return catalog.Knowledge{}, fmt.Errorf("生成总结失败：%s", filter.Text(turn.Error))
		}
		reason := ""
		switch {
		case turn.Proposal != nil || turn.Type == "command_proposal" || panelRequested:
			reason = "AI 返回了操作请求，未生成文字总结"
		case turn.Type == "ask_user":
			reason = "AI 返回了追问，未生成文字总结"
		case turn.Type != "finished":
			reason = "AI 未按要求返回总结"
		case strings.TrimSpace(turn.Message) == "":
			reason = "AI 返回的总结内容为空"
		}
		if reason == "" {
			break
		}
		if attempt == 1 {
			return catalog.Knowledge{}, errors.New(reason + "，自动重试后仍失败，请重试")
		}
		// Retry in a fresh session with the same source, without feeding the
		// rejected response back as instructions or approving any action.
		request.Prompt = "上次生成总结未符合要求。请直接完成文字总结；缺失信息列为待确认，不追问、不请求任何操作。\n" + request.Prompt
	}
	source := "会话自动总结：" + conversation.Title
	if truncated {
		source += "（仅最近 128 KB 的完整消息）"
	}
	return catalog.Knowledge{Title: conversation.Title + " · 经验总结", Kind: "experience", Content: filter.Text(turn.Message), HostID: hostID, ConversationID: conversation.ID, Source: source, Tags: []string{"会话总结"}, Columns: []string{}, Rows: [][]string{}}, nil
}

func localAIPath(path, name string) string {
	if path != "" {
		return path
	}
	if found, err := exec.LookPath(name); err == nil {
		return found
	}
	for _, entry := range desktopEnvironment() {
		if !strings.HasPrefix(entry, "PATH=") {
			continue
		}
		for _, dir := range filepath.SplitList(strings.TrimPrefix(entry, "PATH=")) {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
				return candidate
			}
		}
	}
	return name
}

func (s *Service) SummarizeConversation(hostID, id string) (catalog.Knowledge, error) {
	c, err := s.conversation(hostID, id)
	if err != nil {
		return catalog.Knowledge{}, err
	}
	provider, filter, err := s.knowledgeProviderWithModel(hostID, c.AIProfileID, c.AIModel)
	if err != nil {
		return catalog.Knowledge{}, err
	}
	if closer, ok := provider.(io.Closer); ok {
		defer closer.Close()
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	// A separate provider with no workspace never interferes with a live SSH
	// conversation or executes a returned proposal. Saving is a separate action.
	return summaryDraft(ctx, provider, c, hostID, filter)
}

func (s *Service) knowledgeProvider(hostID string) (agent.Provider, *redact.Filter, error) {
	return s.knowledgeProviderWithAI(hostID, "")
}

func (s *Service) knowledgeProviderWithAI(hostID, selected string) (agent.Provider, *redact.Filter, error) {
	return s.knowledgeProviderWithModel(hostID, selected, "")
}

func (s *Service) knowledgeProviderWithModel(hostID, selected, model string) (agent.Provider, *redact.Filter, error) {
	d, err := s.store.Load()
	if err != nil {
		return nil, nil, err
	}
	profileID := selected
	if hostID != "" {
		h, err := d.Host(hostID)
		if err != nil {
			return nil, nil, err
		}
		if profileID == "" {
			profileID = h.AIProfileID
		}
	}
	if profileID == "" {
		profileID = d.DefaultAIProfileID
	}
	p := catalog.AIProfile{Provider: "codex"}
	if profileID != "" && profileID != catalog.BuiltinCodexProfileID {
		p, err = d.AIProfile(profileID)
		if err != nil {
			return nil, nil, err
		}
	}
	filter := redact.New()
	if p.Provider == "model" {
		for _, choice := range p.ModelChoices() {
			if choice == model {
				p.Model = model
				break
			}
		}
	}
	token := ""
	if p.CredentialID != "" {
		credential, err := d.Credential(p.CredentialID)
		if err != nil {
			return nil, nil, err
		}
		token, err = s.store.Secret(credential)
		if err != nil {
			return nil, nil, err
		}
		filter.Add(token)
	}
	var provider agent.Provider
	switch p.Provider {
	case "codex":
		provider = agent.NewCodexProvider(localAIPath(p.CLIPath, "codex"), p.Model)
	case "claude":
		provider = agent.NewClaudeProvider(localAIPath(p.CLIPath, "claude"), p.Model)
	case "model", "deepseek":
		provider = agent.NewDirectModelProvider(p.Provider, p.URL, token, p.Model, p.CredentialID != "")
	case "http":
		provider = agent.NewClient(p.URL, token)
	default:
		return nil, nil, errors.New("不支持此 AI 配置")
	}

	return provider, filter, nil
}
