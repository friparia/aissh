package credentialui

import (
	"context"
	"git.example.com/ai/wrd-ai-cli/internal/panel"
	"git.example.com/ai/wrd-ai-cli/internal/widgets"

	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Workbench data travels over the per-session authenticated loopback channel,
// never through terminal escape sequences or the remote SSH environment.
type WorkbenchState struct {
	CanCollectOutput     bool                `json:"canCollectOutput"`
	TerminalTakeover     bool                `json:"terminalTakeover,omitempty"`
	SystemCollection     bool                `json:"systemCollection"`
	BastionCompatibility bool                `json:"bastionCompatibility"`
	Model                string              `json:"model,omitempty"`
	ModelOptions         []string            `json:"modelOptions,omitempty"`
	Notices              []WorkbenchNotice   `json:"notices,omitempty"`
	WidgetCandidates     []widgets.Candidate `json:"widgetCandidates,omitempty"`
	WidgetTransfer       widgets.Transfer    `json:"-"`

	CodexThreadID      string             `json:"codexThreadId,omitempty"`
	ConversationID     string             `json:"conversationId"`
	Workspace          string             `json:"workspace"`
	WorkspaceSupported bool               `json:"workspaceSupported"`
	WorkspaceWritable  bool               `json:"workspaceWritable"`
	Messages           []WorkbenchMessage `json:"messages"`
	Busy               bool               `json:"busy"`
	Status             string             `json:"status"`
	Proposal           *WorkbenchProposal `json:"proposal,omitempty"`
	CanApprove         bool               `json:"canApprove"`
	ApprovalStatus     string             `json:"approvalStatus"`
	Executing          bool               `json:"executing"`
	Panels             []MonitorPanel     `json:"panels"`
	AlertMonitors      []MonitorPanel     `json:"alertMonitors"`
	UpdatedAt          string             `json:"updatedAt"`
}

// Notices describe final AI outcomes without carrying prompts, commands or output.
// A bounded history survives coalesced snapshots and temporary IPC failures.
type WorkbenchNotice struct {
	Sequence uint64 `json:"sequence"`
	Kind     string `json:"kind"` // completed, failed, approval, reply
	Time     string `json:"time"`
}

type WorkbenchMessage struct {
	Activity *WorkbenchActivity `json:"activity,omitempty"`
	Role     string             `json:"role"`
	Content  string             `json:"content"`
	Time     string             `json:"time"`
	Proposal *WorkbenchProposal `json:"proposal,omitempty"`
}

type WorkbenchProposal struct {
	ID             string `json:"id"`
	Command        string `json:"command"`
	Explanation    string `json:"explanation"`
	ExpectedEffect string `json:"expectedEffect"`
	Risk           string `json:"risk"`
}

type MonitorPanel struct {
	Widget *widgets.Meta `json:"widget,omitempty"`

	Command        string                `json:"command"`
	Origin         string                `json:"origin"`
	Collecting     bool                  `json:"collecting"`
	Alert          *panel.AlertCondition `json:"alert,omitempty"`
	SampleContent  string                `json:"sampleContent"`
	SourceRevision uint64                `json:"sourceRevision"`
	ID             string                `json:"id"`
	Title          string                `json:"title"`
	Kind           string                `json:"kind"`
	Content        string                `json:"content"`
	Status         string                `json:"status"`
	Error          string                `json:"error"`
	UpdatedAt      string                `json:"updatedAt"`
	IntervalMS     int64                 `json:"intervalMs"`
	Pinned         bool                  `json:"pinned"`
}

type WorkbenchCommand struct {
	CandidateID      string              `json:"candidateId,omitempty"`
	WidgetDefinition *widgets.Definition `json:"widgetDefinition,omitempty"`

	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	ProposalID string `json:"proposalId"`
	PanelID    string `json:"panelId"`
	Deadline   int64  `json:"deadline"`
}

type CommandResult struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

type WorkbenchExchange struct {
	Widgets widgets.Transfer `json:"widgets,omitempty"`

	State   WorkbenchState  `json:"state"`
	Results []CommandResult `json:"results"`
}

func ExchangeWorkbench(ctx context.Context, state WorkbenchState, results []CommandResult) ([]WorkbenchCommand, error) {
	address, token := os.Getenv(AddressEnv), os.Getenv(CapabilityEnv)
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || len(token) != 32 {
		return nil, errors.New("invalid desktop channel")
	}
	body, err := json.Marshal(WorkbenchExchange{State: state, Results: results, Widgets: state.WidgetTransfer})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/workbench", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("desktop channel unavailable")
	}
	var commands []WorkbenchCommand
	err = json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&commands)
	return commands, err
}

type WorkbenchActivity struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
