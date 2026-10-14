package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/i18n"
)

var codexResponseSchema = withPipelineSchema(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "type": {
      "type": "string",
      "enum": [
        "finished",
        "command_proposal",
        "ask_user",
        "failed"
      ]
    },
    "message": {
      "type": "string"
    },
    "proposal": {
      "anyOf": [
        {
          "type": "object",
          "additionalProperties": false,
          "properties": {
            "id": {
              "type": "string"
            },
            "command": {
              "type": "string"
            },
            "explanation": {
              "type": "string"
            },
            "expectedEffect": {
              "type": "string"
            },
            "risk": {
              "type": "string",
              "enum": [
                "read_only",
                "low",
                "medium",
                "high",
                "critical"
              ]
            }
          },
          "required": [
            "id",
            "command",
            "explanation",
            "expectedEffect",
            "risk"
          ]
        },
        {
          "type": "null"
        }
      ]
    },
    "panelActions": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "action": {
            "type": "string",
            "enum": [
              "open",
              "create",
              "update",
              "append",
              "close"
            ]
          },
          "panel": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
              "id": {
                "type": "string"
              },
              "purpose": {
                "type": "string",
                "enum": [
                  "realtime",
                  "alert"
                ],
                "description": "realtime 为实时展示面板，alert 为独立告警任务；两者不可共用 ID"
              },
              "type": {
                "type": "string",
                "enum": [
                  "monitor",
                  "log"
                ]
              },
              "title": {
                "type": "string"
              },
              "collector": {
                "type": "string",
                "description": "内置采集器名称；使用自定义 command 时留空"
              },
              "command": {
                "type": "string",
                "description": "自定义只读采集命令，非空时优先于 collector；使用内置采集器时留空"
              },
              "intervalMs": {
                "type": "integer",
                "minimum": 0
              },
              "content": {
                "type": "string"
              },
              "allowAIRead": {
                "type": "boolean"
              },
              "alert": {
                "anyOf": [
                  {
                    "type": "null"
                  },
                  {
                    "type": "object",
                    "additionalProperties": false,
                    "properties": {
                      "enabled": {
                        "type": "boolean"
                      },
                      "kind": {
                        "type": "string",
                        "enum": [
                          "match",
                          "not_match",
                          "above",
                          "below"
                        ]
                      },
                      "pattern": {
                        "type": "string"
                      },
                      "threshold": {
                        "type": "number"
                      },
                      "recoveryThreshold": {
                        "type": "number"
                      },
                      "severity": {
                        "type": "string",
                        "enum": [
                          "warning",
                          "critical"
                        ]
                      },
                      "forSeconds": {
                        "type": "integer",
                        "minimum": 0,
                        "maximum": 3600
                      },
                      "recoverySeconds": {
                        "type": "integer",
                        "minimum": 0,
                        "maximum": 3600
                      }
                    },
                    "required": [
                      "enabled",
                      "kind",
                      "pattern",
                      "threshold",
                      "recoveryThreshold",
                      "severity",
                      "forSeconds",
                      "recoverySeconds"
                    ]
                  }
                ]
              },
              "data": {
                "anyOf": [
                  {
                    "type": "null"
                  },
                  {
                    "type": "object",
                    "additionalProperties": false,
                    "properties": {
                      "format": {
                        "type": "string",
                        "enum": [
                          "json",
                          "kv",
                          "text"
                        ]
                      },
                      "fields": {
                        "type": "array",
                        "items": {
                          "type": "object",
                          "additionalProperties": false,
                          "properties": {
                            "name": {
                              "type": "string"
                            },
                            "type": {
                              "type": "string",
                              "enum": [
                                "number",
                                "string",
                                "boolean",
                                "table"
                              ]
                            },
                            "unit": {
                              "type": "string"
                            },
                            "nullable": {
                              "type": "boolean"
                            },
                            "history": {
                              "type": "boolean"
                            }
                          },
                          "required": [
                            "name",
                            "type",
                            "unit",
                            "nullable",
                            "history"
                          ]
                        }
                      }
                    },
                    "required": [
                      "format",
                      "fields"
                    ]
                  }
                ]
              },
              "view": {
                "anyOf": [
                  {
                    "type": "null"
                  },
                  {
                    "type": "object",
                    "additionalProperties": false,
                    "properties": {
                      "kind": {
                        "type": "string",
                        "enum": [
                          "custom",
                          "text"
                        ]
                      },
                      "sdkVersion": {
                        "type": "integer"
                      },
                      "stateVersion": {
                        "type": "integer"
                      },
                      "source": {
                        "type": "string"
                      }
                    },
                    "required": [
                      "kind",
                      "sdkVersion",
                      "stateVersion",
                      "source"
                    ]
                  }
                ]
              },
              "expectedRevision": {
                "type": "integer",
                "minimum": 0
              }
            },
            "required": [
              "id",
              "purpose",
              "type",
              "title",
              "collector",
              "command",
              "intervalMs",
              "content",
              "allowAIRead",
              "alert",
              "data",
              "view",
              "expectedRevision"
            ]
          }
        },
        "required": [
          "action",
          "panel"
        ]
      }
    }
  },
  "required": [
    "type",
    "message",
    "proposal",
    "panelActions"
  ]
}`)

type codexResponse struct {
	Type         string        `json:"type"`
	Message      string        `json:"message"`
	Proposal     *Proposal     `json:"proposal"`
	PanelActions []PanelAction `json:"panelActions"`
}

type codexHistory struct {
	Role    string
	Content string
}

type codexSession struct {
	ID               string
	History          []codexHistory
	Pending          []string
	Proposals        map[string]Proposal
	ResponseLanguage string
}

type codexRunner func(context.Context, string, []string, string) ([]byte, []byte, error)

// CodexProvider invokes the locally installed Codex CLI. Each provider keeps a
// persistent Codex thread for its aissh SSH session and only returns structured
// advice. Local workspace writes are disabled by default and must be explicitly
// enabled; aissh remains solely responsible for executing approved commands
// over SSH.
type CodexProvider struct {
	Streaming          bool // Desktop clients opt into App Server events.
	KeepSession        bool // Keep persisted threads for local codex resume.
	Path               string
	Model              string
	Timeout            time.Duration
	Language           string
	Workspace          string
	WorkspaceWritable  bool
	OutputContextChars int

	mu          sync.Mutex
	active      sync.WaitGroup
	closed      bool
	sessions    map[string]*codexSession
	owned       map[string]struct{}
	streamMu    sync.Mutex
	server      *codexAppServer
	startServer func(string) (*codexAppServer, error)
	run         codexRunner
	lookPath    func(string) (string, error)
}

func NewCodexProvider(path, model string) *CodexProvider {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "codex"
	}
	return &CodexProvider{
		Path:               path,
		Model:              strings.TrimSpace(model),
		Timeout:            90 * time.Second,
		OutputContextChars: DefaultOutputContextChars,
		sessions:           make(map[string]*codexSession),
		owned:              make(map[string]struct{}),
		run:                runCodex,
		lookPath:           exec.LookPath,
	}
}

func (p *CodexProvider) Name() string { return "Codex" }

func (p *CodexProvider) localizer() i18n.Localizer { return i18n.New(p.Language) }

// SetLocalWorkspace changes the local directory used by subsequent Codex
// turns. An empty value disables local workspace access.
func (p *CodexProvider) SetLocalWorkspace(value string) {
	p.mu.Lock()
	workspace := strings.TrimSpace(value)
	if p.Workspace != workspace {
		p.sessions = make(map[string]*codexSession)
	}
	p.Workspace = workspace
	p.mu.Unlock()
}

// SetLocalWorkspaceWritable controls whether subsequent Codex sessions use the
// workspace-write sandbox. Changing the mode starts a fresh local AI session.
func (p *CodexProvider) SetLocalWorkspaceWritable(value bool) {
	p.mu.Lock()
	if p.WorkspaceWritable != value {
		p.sessions = make(map[string]*codexSession)
	}
	p.WorkspaceWritable = value
	p.mu.Unlock()
}

func (p *CodexProvider) LocalWorkspace() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(p.Workspace)
}

func (p *CodexProvider) localWorkspaceSettings() (string, bool) {
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(p.Workspace), p.WorkspaceWritable
}

func (p *CodexProvider) Configured() bool {
	if p == nil || strings.TrimSpace(p.Path) == "" {
		return false
	}
	_, err := p.lookPath(p.Path)
	return err == nil
}

func (p *CodexProvider) Stream(ctx context.Context, request Request, callbacks Callbacks) (Turn, error) {
	if !p.Configured() {
		return Turn{}, fmt.Errorf("%s", p.localizer().T("找不到本机 Codex CLI；请先安装 Codex 或设置 AISSH_CODEX_PATH"))
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return Turn{}, fmt.Errorf("%s", p.localizer().T("本机 Codex 会话已关闭"))
	}
	p.active.Add(1)
	p.mu.Unlock()
	defer p.active.Done()

	session, created, err := p.session(request)
	if err != nil {
		return Turn{}, err
	}
	input := p.requestText(session, request)
	request.SystemContext = cloneContext(request.SystemContext)
	workspace, workspaceWritable := p.localWorkspaceSettings()
	if workspace != "" {
		request.SystemContext["local_workspace"] = workspace
		if workspaceWritable {
			request.SystemContext["local_workspace_mode"] = "read-write"
		}
	}
	p.mu.Lock()
	history := append([]codexHistory(nil), session.History...)
	pending := append([]string(nil), session.Pending...)
	threadID := session.ID
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
	if !created && len(pending) > 0 {
		input = strings.Join(pending, "\n") + "\n\n" + input
	}

	if callbacks.OnStatus != nil {
		callbacks.OnStatus(p.localizer().T("正在调用本机 Codex"))
	}
	prompt := buildCodexPrompt(request, history, input)
	if !created {
		prompt = buildCodexContinuationPrompt(request, input)
	}
	result, resumedThreadID, err := p.invokeStreaming(ctx, prompt, workspace, workspaceWritable, threadID, callbacks)
	if err != nil && !created && codexSessionMissing(err) {
		p.mu.Lock()
		delete(p.sessions, threadID)
		delete(p.owned, threadID)
		p.mu.Unlock()
		prompt = buildCodexPrompt(request, history, input)
		result, resumedThreadID, err = p.invokeStreaming(ctx, prompt, workspace, workspaceWritable, "", callbacks)
		created = true
	}
	if err != nil {
		return Turn{}, err
	}
	if created {
		if strings.TrimSpace(resumedThreadID) == "" {
			return Turn{}, fmt.Errorf("%s", p.localizer().T("本机 Codex 没有返回会话 ID"))
		}
		var staleThreadIDs []string
		p.mu.Lock()
		for ownedThreadID := range p.owned {
			if ownedThreadID != resumedThreadID {
				staleThreadIDs = append(staleThreadIDs, ownedThreadID)
				delete(p.sessions, ownedThreadID)
			}
		}
		session.ID = strings.TrimSpace(resumedThreadID)
		p.sessions[session.ID] = session
		p.owned[session.ID] = struct{}{}
		p.mu.Unlock()
		for _, staleThreadID := range staleThreadIDs {
			if p.KeepSession {
				p.mu.Lock()
				delete(p.owned, staleThreadID)
				p.mu.Unlock()
				continue
			}
			if p.deleteThread(staleThreadID) == nil {
				p.mu.Lock()
				delete(p.owned, staleThreadID)
				p.mu.Unlock()
			}
		}
	} else if resumedThreadID != "" && resumedThreadID != session.ID {
		return Turn{}, fmt.Errorf("%s", p.localizer().T("本机 Codex 恢复了意外的会话：%s", resumedThreadID))
	}

	if result.Type == "command_proposal" && (result.Proposal == nil || strings.TrimSpace(result.Proposal.Command) == "") {
		return Turn{}, fmt.Errorf("%s", p.localizer().T("本机 Codex 返回了空命令建议"))
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
	if request.Event == "result" {
		delete(session.Proposals, request.ProposalID)
	}
	session.Pending = nil
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

// AssessRisk runs in an ephemeral Codex thread so repeated reviews are
// independent and cannot alter the main aissh conversation.
func (p *CodexProvider) AssessRisk(ctx context.Context, request RiskAssessmentRequest) (RiskAssessment, error) {
	if !p.Configured() {
		return RiskAssessment{}, fmt.Errorf("%s", p.localizer().T("找不到本机 Codex CLI；请先安装 Codex 或设置 AISSH_CODEX_PATH"))
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return RiskAssessment{}, fmt.Errorf("%s", p.localizer().T("本机 Codex 会话已关闭"))
	}
	p.active.Add(1)
	p.mu.Unlock()
	defer p.active.Done()

	result, _, err := p.invokeWithMode(ctx, buildRiskAssessmentPrompt(request), "", false, "", true)
	if err != nil {
		return RiskAssessment{}, err
	}
	return riskAssessmentFromResponse(result, request.Proposal.Command)
}

// Close removes persisted Codex threads unless KeepSession is enabled. One
// provider belongs to one aissh SSH process, so its Codex session lifetime
// follows the SSH session lifetime while aissh keeps its own text history.
func (p *CodexProvider) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	p.active.Wait()
	p.streamMu.Lock()
	if p.server != nil {
		p.server.close()
		p.server = nil
	}
	p.streamMu.Unlock()

	p.mu.Lock()
	threadIDs := make([]string, 0, len(p.owned))
	for threadID := range p.owned {
		threadIDs = append(threadIDs, threadID)
	}
	p.sessions = make(map[string]*codexSession)
	p.owned = make(map[string]struct{})
	p.mu.Unlock()

	if p.KeepSession {
		return nil
	}
	var closeErrors []error
	for _, threadID := range threadIDs {
		if err := p.deleteThread(threadID); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func (p *CodexProvider) deleteThread(threadID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stdout, stderr, err := p.run(ctx, p.Path, []string{"delete", "--force", strings.TrimSpace(threadID)}, "")
	if err != nil {
		return codexRunErrorLocalized(err, stdout, stderr, p.localizer())
	}
	return nil
}

func (p *CodexProvider) Action(_ context.Context, request Request) (Turn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return Turn{}, fmt.Errorf("%s", p.localizer().T("本机 Codex 会话已关闭"))
	}
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
		message := "用户批准执行命令：" + proposal.Command
		session.History = append(session.History, codexHistory{Role: "user", Content: message})
		session.Pending = append(session.Pending, message)
		return Turn{Type: "approved", Session: Session{ID: session.ID, Status: "running"}}, nil
	case "reject":
		delete(session.Proposals, request.ProposalID)
		message := "用户拒绝执行命令：" + proposal.Command
		session.History = append(session.History, codexHistory{Role: "user", Content: message})
		session.Pending = append(session.Pending, message)
		return Turn{
			Type:    "finished",
			Session: Session{ID: session.ID, Status: "finished"},
			Message: p.localizer().T("已拒绝该命令建议。你可以继续描述希望采用的处理方式。"),
		}, nil
	default:
		return Turn{}, fmt.Errorf("unsupported action: %s", request.Event)
	}
}

func (p *CodexProvider) session(request Request) (*codexSession, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.Event == "start" || request.SessionID == "" {
		return &codexSession{Proposals: make(map[string]Proposal)}, true, nil
	}
	value := p.sessions[request.SessionID]
	if value == nil {
		return nil, false, fmt.Errorf("session not found")
	}
	return value, false, nil
}

func (p *CodexProvider) requestText(session *codexSession, request Request) string {
	switch request.Event {
	case "result":
		command := ""
		p.mu.Lock()
		if proposal, ok := session.Proposals[request.ProposalID]; ok {
			command = proposal.Command
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

func (p *CodexProvider) invoke(parent context.Context, prompt, workspace string, workspaceWritable bool, threadID string) (codexResponse, string, error) {
	return p.invokeWithMode(parent, prompt, workspace, workspaceWritable, threadID, false)
}

func (p *CodexProvider) invokeWithMode(parent context.Context, prompt, workspace string, workspaceWritable bool, threadID string, ephemeral bool) (codexResponse, string, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	schema, err := os.CreateTemp("", "aissh-codex-schema-*.json")
	if err != nil {
		return codexResponse{}, "", err
	}
	schemaPath := schema.Name()
	defer os.Remove(schemaPath)
	if _, err := schema.WriteString(codexResponseSchema); err != nil {
		_ = schema.Close()
		return codexResponse{}, "", err
	}
	if err := schema.Close(); err != nil {
		return codexResponse{}, "", err
	}

	args := []string{"exec"}
	if strings.TrimSpace(threadID) != "" {
		args = append(args, "resume")
	} else if ephemeral {
		args = append(args, "--ephemeral")
	}
	args = append(args,
		"--json", "--skip-git-repo-check", "--ignore-rules",
		"--disable", "plugins", "--disable", "remote_plugin", "--disable", "apps",
		"--disable", "browser_use", "--disable", "computer_use", "--disable", "image_generation",
		"--disable", "multi_agent", "--disable", "hooks",
		"--output-schema", schemaPath,
	)
	if strings.TrimSpace(threadID) == "" {
		sandbox := "read-only"
		if strings.TrimSpace(workspace) != "" && workspaceWritable {
			sandbox = "workspace-write"
		}
		args = append(args, "--sandbox", sandbox, "-C", codexWorkingDirectory(workspace))
	}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	if strings.TrimSpace(threadID) != "" {
		args = append(args, strings.TrimSpace(threadID))
	}
	args = append(args, "-")
	stdout, stderr, runErr := p.run(ctx, p.Path, args, prompt)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return codexResponse{}, "", fmt.Errorf("%s", p.localizer().T("本机 Codex 调用超时（%s）", timeout))
	}
	response, returnedThreadID, parseErr := parseCodexInvocationJSONLLocalized(stdout, p.localizer())
	if runErr != nil {
		return codexResponse{}, returnedThreadID, codexRunErrorLocalized(runErr, stdout, stderr, p.localizer())
	}
	if parseErr != nil {
		return codexResponse{}, returnedThreadID, parseErr
	}
	return response, returnedThreadID, nil
}

func codexWorkingDirectory(workspace string) string {
	if value := strings.TrimSpace(workspace); value != "" {
		return value
	}
	return os.TempDir()
}

func runCodex(ctx context.Context, path string, args []string, prompt string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = localProviderEnvironment("codex", path)
	cmd.Stdin = strings.NewReader(prompt)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func parseCodexJSONL(data []byte) (codexResponse, error) {
	response, _, err := parseCodexInvocationJSONLLocalized(data, i18n.New(""))
	return response, err
}

func parseCodexInvocationJSONLLocalized(data []byte, localizer i18n.Localizer) (codexResponse, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var assistantText string
	var lastError string
	var threadID string
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			Message  string `json:"message"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Message string `json:"message"`
			} `json:"item"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type == "thread.started" && strings.TrimSpace(event.ThreadID) != "" {
			threadID = strings.TrimSpace(event.ThreadID)
		}
		if event.Type == "error" && event.Message != "" {
			lastError = event.Message
		}
		if event.Type == "item.completed" {
			switch event.Item.Type {
			case "agent_message":
				assistantText = event.Item.Text
			case "error":
				lastError = firstNonEmpty(event.Item.Message, event.Item.Text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return codexResponse{}, threadID, err
	}
	if strings.TrimSpace(assistantText) == "" {
		return codexResponse{}, threadID, fmt.Errorf("%s", localizer.T("本机 Codex 没有返回结果%s", optionalDetail(lastError)))
	}
	assistantText = stripJSONFence(assistantText)
	var response codexResponse
	if err := json.Unmarshal([]byte(assistantText), &response); err != nil {
		return codexResponse{}, threadID, fmt.Errorf("%s", localizer.T("无法解析本机 Codex 的结构化结果: %v", err))
	}
	return response, threadID, nil
}

func buildCodexPrompt(request Request, history []codexHistory, input string) string {
	if request.SystemContext["conversation_summary"] == "true" {
		// The ordinary chat input retains only the last 8,000 characters, which
		// can remove the summary instructions along with the start of its source.
		return request.Prompt
	}
	if request.SystemContext["catalog_assistant"] != "" {
		return buildCatalogPrompt(request, history, input)
	}
	var value strings.Builder
	value.WriteString(`你是 aissh 的本地运维助手。aissh 正在本机包装一个真实的 OpenSSH 会话。
`)
	if workspace := strings.TrimSpace(request.SystemContext["local_workspace"]); workspace != "" {
		if request.SystemContext["local_workspace_mode"] == "read-write" {
			value.WriteString(`
本机代码上下文：
1. 用户已显式开启本机工作区写入；你可以使用提供的本机工具检查、编辑、创建或删除工作区内的文件。
2. 修改必须限制在配置的工作区内，保留用户已有的无关改动，不访问工作区外文件或网络；完成后在 message 中简要说明改动和验证结果。
3. 不要把本机工作区命令作为 command_proposal 返回；command_proposal 永远表示将在目标服务器当前 Shell 中执行的远端命令。
`)
		} else {
			value.WriteString(`
本机代码上下文：
1. 你可以使用只读本机工具检查工作区中的源码、配置、Git 状态和差异，以便把本地实现与线上现象关联起来。
2. 本机工作区严格只读：不得编辑、创建或删除文件，不得运行可能写文件的构建、测试或项目程序，不得访问网络。
3. 不要把本机工作区命令作为 command_proposal 返回；command_proposal 永远表示将在目标服务器当前 Shell 中执行的远端命令。
`)
		}
	} else {
		value.WriteString(`
本机代码上下文：不要调用任何本机工具、Shell、网络、文件或外部服务；只分析下面提供的文本。
`)
	}
	value.WriteString(`
远端操作硬性边界：
1. 你不能直接操作目标服务器。需要操作时，只能返回一个 command_proposal，命令由用户明确批准后才会经现有 SSH PTY 执行。
2. 命令在目标服务器当前 SSH Shell 中执行，单行命令按原文直接输入；客户端按提示符状态处理多行命令和完成回执。命令必须自包含，优先使用简洁的单行工具命令和管道。不要添加用于完成检测的 eval、trap、子 Shell 或 printf 标记包装；不要改变当前 Shell 的目录、环境、别名、函数或选项。Shell 提示符返回只表示本次交互结束，退出码未知时必须依据实际输出判断结果，不能自动视为执行成功。
3. 不得使用 exit、logout，也不得用 exec 替换 Shell；前置条件失败时应输出诊断并用 false 或自然返回结束命令，绝不能主动结束 SSH 登录 Shell。
4. 命令必须适合在目标服务器的当前交互式 Shell 中执行，不得包含交互式全屏程序。
5. 有破坏性、重启、删除、覆盖、权限或网络策略变更风险时，risk 必须如实标为 high 或 critical，并在 explanation 中说明。
6. 可用 panelActions 创建 monitor 或 log Tab，command 必须严格只读；自定义 command 与内置 collector 二选一，另一项留空，非空 command 优先。update 会更新命令、标题和刷新间隔；目标不存在时，只有提供完整采集配置才能创建。面板操作是否成功以 monitor_panel_result / monitor_panel_state 上下文为准；未确认前不要声称已经成功，创建成功不代表采集成功。分析结论和报告内容统一写入 message，不创建报告面板；不需要监控面板时返回空数组。
7. 如果信息不足，返回 ask_user 并在 message 中提出一个简短问题；能直接回答时返回 finished。
8. 只返回符合 JSON Schema 的对象，不要 Markdown 代码围栏。

常用只读 collector：system.overview、process.top、disk.usage、network.sockets、service.status。
实时展示和告警监控是两个独立用途。panel.purpose=realtime 仅创建工作台实时面板，alert 必须为 null，不触发桌宠；panel.purpose=alert 仅创建告警任务，必须配置有效且启用的 panel.alert，不创建实时面板。用户只要实时数据时不要创建告警任务；只要异常提醒时不要创建实时面板；用途不明确时先询问。用户同时需要两者时，使用不同 ID（建议 live- / alert- 前缀）创建两个任务，各自设置 command 和 intervalMs，不能复用或覆盖另一个用途的 ID。关闭、刷新或修改其中一个不会影响另一个。
用户可以用自然语言创建任意只读监控，不限于内置 collector。用户明确要求异常提醒时，在 panelActions 的 panel.alert 配置该项目专属条件（仅该会话生效）。alert=null 表示不修改原规则；enabled=false 关闭此项目提醒。kind=match 在 RE2 正则命中时异常，not_match 在未命中时异常（只对成功且非空采集结果判断）；above/below 用 pattern 的唯一捕获组提取数值，达到 threshold 时异常，返回 recoveryThreshold 后确认恢复。数值提取失败是未知，不是正常。severity 为 warning/critical，forSeconds/recoverySeconds 为异常/恢复需持续秒数；未指定时可用 10 秒，说明所用阈值。全局静音、确认和重复间隔仍适用。关闭项目会停止它的提醒，修改命令会清空旧数据。规则和监控仅在当前连接生效，重连后需重新创建，不要声称已永久保存。
例如用户要求 nginx 停止时提醒，可以创建 purpose="alert"、id="alert-nginx"、command="systemctl is-active nginx 2>/dev/null || true"、intervalMs=5000，alert={"enabled":true,"kind":"not_match","pattern":"(?m)^active\\s*$","threshold":0,"recoveryThreshold":0,"severity":"critical","forSeconds":10,"recoverySeconds":10}。采集错误、权限不足、缺失工具或空输出不能当作服务已正常运行。日志用有界只读命令（如 tail -n 100，不能 tail -f）；关键词使用 match。请求“最近一分钟出现 5 次”必须让采集命令真正按时间筛选并计数，不能把静态历史关键词命中说成发生了新事件。接口延迟、进程数、连接数、业务计数等可用 above/below；尽量输出单行 VALUE=数值再匹配。不要把整段敏感日志写入提醒，message 中清楚说明采集频率、异常条件和生效范围。只展示不提醒时 alert=null。
`)
	value.WriteString(monitorOutputGuidance)
	if strings.HasPrefix(strings.ToLower(request.SystemContext["response_language"]), "en") {
		value.WriteString("\n输出语言：本轮用户使用英文提问，因此 message、proposal explanation、expectedEffect 和 panel 标题使用英文。\n")
	} else {
		value.WriteString("\n输出语言：本轮用户使用中文提问，因此 message、proposal explanation、expectedEffect 和 panel 标题使用简体中文。\n")
	}
	if request.Target != nil {
		value.WriteString("\n目标：" + request.Target.HostLabel + "（" + request.Target.Protocol + "）\n")
	}
	if conversation := strings.TrimSpace(request.SystemContext["conversation_history"]); conversation != "" {
		value.WriteString("\n用户选择继续一个本机历史会话。以下是该会话此前的上下文；请在此基础上继续回答，不要声称仍连接着旧的运行时 Agent Session：\n")
		value.WriteString(trimText(conversation, 24000) + "\n")
	}
	if len(request.SystemContext) > 0 {
		value.WriteString("环境上下文：\n")
		for key, item := range request.SystemContext {
			if key == "conversation_history" {
				continue
			}
			value.WriteString(key + "=" + promptContextValue(key, item) + "\n")
		}
	}
	if len(history) > 0 {
		value.WriteString("\n此前对话：\n")
		for _, item := range history {
			value.WriteString(item.Role + ": " + trimText(item.Content, 8000) + "\n")
		}
	}
	value.WriteString("\n本轮事件：" + request.Event + "\n" + input)
	return value.String()
}

func buildCodexContinuationPrompt(request Request, input string) string {
	if request.SystemContext["conversation_summary"] == "true" {
		return request.Prompt
	}
	if request.SystemContext["catalog_assistant"] != "" {
		return buildCatalogPrompt(request, nil, input)
	}
	var value strings.Builder
	value.WriteString(monitorOutputGuidance)
	value.WriteString("\n通过 panelActions 创建任意只读采集：purpose=realtime 只展示，alert=null；purpose=alert 为独立告警任务，必须配置有效的 panel.alert；两类使用不同 ID，各自命令、周期、关闭操作独立，不得互相覆盖。只要展示就不要创建告警；只要告警就不要创建面板。panel.alert 项目专属提醒：kind=match/not_match 使用 RE2 pattern 匹配或检查未命中；above/below 用唯一捕获组提取数值，与 threshold/recoveryThreshold 比较。enabled、severity、forSeconds、recoverySeconds 也需填写。null 保留规则，enabled=false 关闭。数值提取失败、空输出和过期数据为未知；只对真实新采集确认异常或恢复。项目和规则仅当前连接有效。是否创建成功以 monitor_panel_result 为准。\n")
	if len(request.SystemContext) > 0 {
		value.WriteString("环境上下文更新：\n")
		for key, item := range request.SystemContext {
			if key == "conversation_history" || key == "local_workspace" {
				continue
			}
			value.WriteString(key + "=" + promptContextValue(key, item) + "\n")
		}
	}
	value.WriteString("\n本轮事件：" + request.Event + "\n" + input)
	return value.String()
}

// Capability protocols are instructions, not log tails. Trimming this one
// removed the imports and entry point while leaving only the UI restrictions.
func promptContextValue(key, value string) string {
	if key == "monitorWidgetV1" || key == "monitorWidgetV2" || key == "monitor_pipeline_receipt" || strings.HasPrefix(key, "monitor_panel_state_") {
		return value
	}
	return trimText(value, 1000)
}

func cloneContext(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func summarizeCodexResponse(response codexResponse) string {
	parts := []string{firstNonEmpty(response.Message, response.Type)}
	if response.Proposal != nil {
		parts = append(parts, "建议命令："+response.Proposal.Command)
	}
	if len(response.PanelActions) > 0 {
		parts = append(parts, fmt.Sprintf("面板操作：%d 个", len(response.PanelActions)))
	}
	return strings.Join(parts, "\n")
}

func codexRunError(runErr error, stdout, stderr []byte) error {
	return codexRunErrorLocalized(runErr, stdout, stderr, i18n.New(""))
}

func codexRunErrorLocalized(runErr error, stdout, stderr []byte, localizer i18n.Localizer) error {
	detail := string(append(append([]byte(nil), stderr...), stdout...))
	lower := strings.ToLower(detail)
	if strings.Contains(lower, "401 unauthorized") || strings.Contains(lower, "authentication required") || strings.Contains(lower, "incorrect api key") {
		return fmt.Errorf("%s", localizer.T("本机 Codex 认证失败；请先确认 `codex exec` 可用，并检查 codex login 或 ~/.codex/config.toml"))
	}
	if strings.Contains(lower, "session not found") || strings.Contains(lower, "no session found") || strings.Contains(lower, "unknown session") || strings.Contains(lower, "rollout not found") {
		return fmt.Errorf("session not found: %s", localizer.T("本机 Codex 会话已失效"))
	}
	clean := strings.TrimSpace(detail)
	if clean == "" {
		clean = runErr.Error()
	}
	return fmt.Errorf("%s", localizer.T("本机 Codex 调用失败: %s", trimText(clean, 800)))
}

func codexSessionMissing(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "session not found")
}

func stripJSONFence(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		lines := strings.Split(value, "\n")
		if len(lines) >= 3 {
			lines = lines[1 : len(lines)-1]
			value = strings.Join(lines, "\n")
		}
	}
	return strings.TrimSpace(value)
}

func trimText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return "…" + string(runes[len(runes)-limit:])
}

func optionalDetail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return "：" + trimText(value, 300)
}

func newLocalID(prefix string) string {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err == nil {
		return prefix + "-" + hex.EncodeToString(data)
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func (p *CodexProvider) invocationTimeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 90 * time.Second
}
