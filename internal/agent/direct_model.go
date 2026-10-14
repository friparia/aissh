package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/i18n"
)

type DirectModelProvider struct {
	Streaming          bool // Enabled only by the desktop client.
	Label              string
	BaseURL            string
	APIKey             string
	Model              string
	Language           string
	RequireToken       bool
	OutputContextChars int
	HTTP               *http.Client

	mu       sync.Mutex
	sessions map[string]*codexSession
}

type directChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type directChatRequest struct {
	Model          string              `json:"model"`
	Stream         bool                `json:"stream,omitempty"`
	Messages       []directChatMessage `json:"messages"`
	ResponseFormat map[string]string   `json:"response_format"`
	MaxTokens      int                 `json:"max_tokens"`
}

type directChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func NewDirectModelProvider(label, baseURL, apiKey, model string, requireToken bool) *DirectModelProvider {
	return &DirectModelProvider{
		Label:              firstNonEmpty(strings.TrimSpace(label), "Model"),
		BaseURL:            strings.TrimSpace(baseURL),
		APIKey:             strings.TrimSpace(apiKey),
		Model:              strings.TrimSpace(model),
		RequireToken:       requireToken,
		OutputContextChars: DefaultOutputContextChars,
		HTTP:               &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		sessions:           make(map[string]*codexSession),
	}
}

// SetModel keeps the existing conversation history and endpoint credentials.
func (p *DirectModelProvider) SetModel(model string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Model = model
}

func (p *DirectModelProvider) CurrentModel() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Model
}

func (p *DirectModelProvider) Name() string {
	if p == nil {
		return "Model"
	}
	return firstNonEmpty(p.Label, "Model")
}

func (p *DirectModelProvider) Configured() bool {
	return p != nil && strings.TrimSpace(p.BaseURL) != "" && strings.TrimSpace(p.CurrentModel()) != "" && (!p.RequireToken || strings.TrimSpace(p.APIKey) != "")
}

func (p *DirectModelProvider) localizer() i18n.Localizer { return i18n.New(p.Language) }

func (p *DirectModelProvider) Stream(ctx context.Context, request Request, callbacks Callbacks) (Turn, error) {
	if strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.CurrentModel()) == "" {
		return Turn{}, fmt.Errorf("%s", p.localizer().T("直连模型未配置；请设置模型地址和模型名称"))
	}
	if p.RequireToken && strings.TrimSpace(p.APIKey) == "" {
		return Turn{}, fmt.Errorf("%s", p.localizer().T("直连模型缺少 API Key"))
	}
	session, created, err := p.session(request)
	if err != nil {
		return Turn{}, err
	}
	input := p.requestText(session, request)
	request.SystemContext = cloneContext(request.SystemContext)

	p.mu.Lock()
	history := append([]codexHistory(nil), session.History...)
	responseLanguage := strings.TrimSpace(request.SystemContext["response_language"])
	if responseLanguage == "" {
		if request.Event == "result" && session.ResponseLanguage != "" {
			responseLanguage = session.ResponseLanguage
		} else {
			responseLanguage = i18n.PreferredResponseLanguage(request.Prompt, firstNonEmpty(p.Language, request.SystemContext["ui_language"]))
		}
	}
	responseLanguage = i18n.Resolve(responseLanguage)
	session.ResponseLanguage = responseLanguage
	p.mu.Unlock()
	request.SystemContext["response_language"] = responseLanguage

	if callbacks.OnStatus != nil {
		callbacks.OnStatus(p.localizer().T("正在调用直连模型 %s", p.Name()))
	}
	var streaming []Callbacks
	if p.Streaming {
		streaming = append(streaming, callbacks)
	}
	result, err := p.invoke(ctx, buildDirectModelPrompt(request, history, input), streaming...)
	if err != nil {
		if created {
			p.mu.Lock()
			delete(p.sessions, session.ID)
			p.mu.Unlock()
		}
		return Turn{}, err
	}
	if result.Type == "command_proposal" && (result.Proposal == nil || strings.TrimSpace(result.Proposal.Command) == "") {
		return Turn{}, fmt.Errorf("%s", p.localizer().T("直连模型返回了空命令建议"))
	}
	if result.Proposal != nil {
		result.Proposal.Command = strings.TrimSpace(result.Proposal.Command)
		if result.Proposal.ID == "" {
			result.Proposal.ID = newLocalID("proposal")
		}
	}
	for _, action := range result.PanelActions {
		if callbacks.OnPanelAction != nil {
			callbacks.OnPanelAction(action)
		}
	}

	p.mu.Lock()
	session.History = append(session.History,
		codexHistory{Role: "user", Content: input},
		codexHistory{Role: "assistant", Content: summarizeCodexResponse(result)},
	)
	if len(session.History) > 16 {
		session.History = append([]codexHistory(nil), session.History[len(session.History)-16:]...)
	}
	if result.Proposal != nil {
		session.Proposals[result.Proposal.ID] = *result.Proposal
	}
	p.mu.Unlock()

	status := "finished"
	if result.Proposal != nil {
		status = "waiting_approval"
	}
	return Turn{
		Type:     firstNonEmpty(result.Type, "finished"),
		Session:  Session{ID: session.ID, Status: status},
		Proposal: result.Proposal,
		Message:  result.Message,
	}, nil
}

// AssessRisk makes a stateless model request and leaves the main conversation
// session unchanged.
func (p *DirectModelProvider) AssessRisk(ctx context.Context, request RiskAssessmentRequest) (RiskAssessment, error) {
	if strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.CurrentModel()) == "" {
		return RiskAssessment{}, fmt.Errorf("%s", p.localizer().T("直连模型未配置；请设置模型地址和模型名称"))
	}
	if p.RequireToken && strings.TrimSpace(p.APIKey) == "" {
		return RiskAssessment{}, fmt.Errorf("%s", p.localizer().T("直连模型缺少 API Key"))
	}
	result, err := p.invoke(ctx, buildRiskAssessmentPrompt(request))
	if err != nil {
		return RiskAssessment{}, err
	}
	return riskAssessmentFromResponse(result, request.Proposal.Command)
}

func (p *DirectModelProvider) Action(_ context.Context, request Request) (Turn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.sessions[request.SessionID]
	if session == nil {
		return Turn{}, fmt.Errorf("session not found")
	}
	proposal, ok := session.Proposals[request.ProposalID]
	if !ok {
		return Turn{}, fmt.Errorf("proposal not found")
	}
	switch request.Event {
	case "approve":
		session.History = append(session.History, codexHistory{Role: "user", Content: "用户批准执行命令：" + proposal.Command})
		return Turn{Type: "approved", Session: Session{ID: session.ID, Status: "running"}}, nil
	case "reject":
		delete(session.Proposals, request.ProposalID)
		session.History = append(session.History, codexHistory{Role: "user", Content: "用户拒绝执行命令：" + proposal.Command})
		return Turn{
			Type:    "finished",
			Session: Session{ID: session.ID, Status: "finished"},
			Message: p.localizer().T("已拒绝该命令建议。你可以继续描述希望采用的处理方式。"),
		}, nil
	default:
		return Turn{}, fmt.Errorf("unsupported action: %s", request.Event)
	}
}

func (p *DirectModelProvider) session(request Request) (*codexSession, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.Event == "start" || request.SessionID == "" {
		value := &codexSession{ID: newLocalID("model"), Proposals: make(map[string]Proposal)}
		p.sessions[value.ID] = value
		return value, true, nil
	}
	value := p.sessions[request.SessionID]
	if value == nil {
		return nil, false, fmt.Errorf("session not found")
	}
	return value, false, nil
}

func (p *DirectModelProvider) requestText(session *codexSession, request Request) string {
	switch request.Event {
	case "result":
		command := ""
		p.mu.Lock()
		if proposal, ok := session.Proposals[request.ProposalID]; ok {
			command = proposal.Command
			delete(session.Proposals, request.ProposalID)
		}
		p.mu.Unlock()
		return fmt.Sprintf("远端命令执行结果\n命令：%s\n完成原因：%s\n输出：\n%s", firstNonEmpty(command, "未知"), request.Completion, LimitOutputContext(request.Output, p.OutputContextChars))
	default:
		parts := []string{"用户请求：" + trimText(request.Prompt, 8000)}
		if strings.TrimSpace(request.RecentOutput) != "" {
			parts = append(parts, "SSH 最近输出：\n"+LimitOutputContext(request.RecentOutput, p.OutputContextChars))
		}
		return strings.Join(parts, "\n\n")
	}
}

func (p *DirectModelProvider) invoke(ctx context.Context, prompt string, streaming ...Callbacks) (codexResponse, error) {
	body, err := json.Marshal(directChatRequest{
		Model:  p.CurrentModel(),
		Stream: len(streaming) > 0,
		Messages: []directChatMessage{{
			Role: "system", Content: prompt,
		}},
		ResponseFormat: map[string]string{"type": "json_object"},
		MaxTokens:      4096,
	})
	if err != nil {
		return codexResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return codexResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if len(streaming) > 0 {
		request.Header.Set("Accept", "text/event-stream")
	}
	if p.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return codexResponse{}, fmt.Errorf("%s", p.localizer().T("直连模型调用失败：%s", err.Error()))
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 && strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		var cb Callbacks
		if len(streaming) > 0 {
			cb = streaming[0]
		}
		return p.readChatStream(response.Body, cb)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return codexResponse{}, err
	}
	var envelope directChatResponse
	if err := json.Unmarshal(data, &envelope); err != nil {
		return codexResponse{}, fmt.Errorf("%s", p.localizer().T("无法解析直连模型响应：%s", trimText(string(data), 800)))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := firstNonEmpty(envelope.Error.Message, response.Status)
		if p.APIKey != "" {
			detail = strings.ReplaceAll(detail, p.APIKey, "[redacted]")
		}
		return codexResponse{}, fmt.Errorf("%s", p.localizer().T("直连模型返回错误：%s", trimText(detail, 800)))
	}
	if len(envelope.Choices) == 0 || strings.TrimSpace(envelope.Choices[0].Message.Content) == "" {
		return codexResponse{}, fmt.Errorf("%s", p.localizer().T("直连模型没有返回内容"))
	}
	content := stripJSONFence(envelope.Choices[0].Message.Content)
	var result codexResponse
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return codexResponse{}, fmt.Errorf("%s", p.localizer().T("无法解析直连模型的结构化结果：%s", err.Error()))
	}
	return result, nil
}

func (p *DirectModelProvider) endpoint() string {
	value := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if strings.HasSuffix(value, "/chat/completions") {
		return value
	}
	return value + "/chat/completions"
}

func (p *DirectModelProvider) readChatStream(reader io.Reader, cb Callbacks) (codexResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var content strings.Builder
	stream := messageStream{emit: cb.OnAnswer}
	var lines []string
	finished, done := false, false
	eventBytes := 0
	flush := func() error {
		raw := strings.Join(lines, "\n")
		lines = nil
		eventBytes = 0
		if raw == "[DONE]" {
			done = true
			return nil
		}
		if raw == "" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return fmt.Errorf("无法解析模型流式响应：%w", err)
		}
		if chunk.Error != nil {
			detail := chunk.Error.Message
			if p.APIKey != "" {
				detail = strings.ReplaceAll(detail, p.APIKey, "[redacted]")
			}
			return fmt.Errorf("模型流式响应失败：%s", trimText(detail, 800))
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if content.Len()+len(choice.Delta.Content) > 4*1024*1024 {
				return fmt.Errorf("模型响应过长")
			}
			content.WriteString(choice.Delta.Content)
			stream.Write(choice.Delta.Content)
			if choice.FinishReason != nil {
				if *choice.FinishReason != "stop" {
					return fmt.Errorf("模型回复未完成：%s", *choice.FinishReason)
				}
				finished = true
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return codexResponse{}, err
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			eventBytes += len(line)
			if eventBytes > 4*1024*1024 {
				return codexResponse{}, fmt.Errorf("模型响应事件过长")
			}
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return codexResponse{}, err
	}
	if err := flush(); err != nil {
		return codexResponse{}, err
	}
	if !finished {
		return codexResponse{}, fmt.Errorf("模型连接中断，回复尚未完成")
	}
	var result codexResponse
	if err := json.Unmarshal([]byte(stripJSONFence(content.String())), &result); err != nil {
		return result, fmt.Errorf("无法解析模型的结构化结果：%w", err)
	}
	return result, nil
}

func buildDirectModelPrompt(request Request, history []codexHistory, input string) string {
	return buildCodexPrompt(request, history, input) + `

你必须输出一个 JSON 对象，不要输出 JSON 之外的文字。对象必须符合下面的 JSON Schema：
` + codexResponseSchema + `

最小 JSON 示例：
{"type":"finished","message":"结论","proposal":null,"panelActions":[]}
`
}
