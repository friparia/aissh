package app

import (
	"git.example.com/ai/wrd-ai-cli/internal/monitorplan"
	"time"

	"git.example.com/ai/wrd-ai-cli/internal/agent"
	"git.example.com/ai/wrd-ai-cli/internal/trigger"
)

type outputEvent struct{ data []byte }
type inputEvent struct{ data []byte }
type resizeEvent struct{}
type sshExitEvent struct{ err error }
type monitorResultEvent struct {
	pipelineID      string
	capture         *monitorplan.Capture
	binding         string
	revision, runID uint64

	panelID string
	command string
	content string
	err     error
}
type agentStatusEvent struct{ status string }
type agentThoughtEvent struct{ content string }
type agentAnswerEvent struct{ content string }
type agentActivityEvent struct{ activity agent.Activity }
type sshActivityEvent struct{ proposalID, status, detail string }
type agentTurnEvent struct {
	turn agent.Turn
	err  error
}
type agentPanelEvent struct{ action agent.PanelAction }
type alwaysYesRiskProgressEvent struct {
	pass  int
	total int
}
type alwaysYesRiskEvent struct {
	proposalID  string
	assessments []agent.RiskAssessment
	err         error
}
type approvalEvent struct {
	turn    agent.Turn
	command string
	err     error
}
type rejectionEvent struct {
	turn    agent.Turn
	command string
	err     error
}
type executionResultEvent struct {
	turn agent.Turn
	err  error
}
type delayedErrorTriggerEvent struct {
	match     trigger.ErrorMatch
	command   string
	createdAt time.Time
}

type focusMode uint8

const (
	focusTerminal focusMode = iota
	focusApproval
	focusAI
	focusAIHistory
	focusMonitor
	focusMonitorSlot
	focusPanelMenu
	focusCustomPanel
)

type monitorLayoutMode uint8

const (
	monitorLayoutAuto monitorLayoutMode = iota
	monitorLayoutSingle
	monitorLayoutSplit
	monitorLayout1x3
	monitorLayout2x2
	monitorLayoutGrid
)

type chatMessage struct {
	Role            string
	Activity        *agent.Activity
	Content         string
	Time            time.Time
	Proposal        *agent.Proposal
	Historical      bool
	CommandExpanded bool
}

type executionState struct {
	BastionCompatibility bool
	Multiline            bool
	IntegrationCarry     []byte
	IntegrationStarted   bool
	IntegrationPrompt    bool
	IntegrationExitCode  *int
	SessionID            string
	ProposalID           string
	Command              string
	Output               *contextBuffer
	StartedAt            time.Time
	Inline               bool
	Marker               string
	TextMarker           bool
	Pending              []byte
	ExitCode             *int
	Completion           string
	WriteJob             *sshWriteJob
	WaitWarned           bool
	Direct               bool
	EchoMarker           bool
	ReceiptReceived      bool
	ShellPrompt          string
	LastOutputAt         time.Time
}

type inlineAIState struct {
	answerCache     *inlineAnswerCache
	Prompt          string
	AnchorX         int
	AnchorY         int
	AwaitingPrompt  bool
	Status          string
	StatusPrefix    string
	Response        string
	Error           string
	Done            bool
	Spinner         int
	Started         time.Time
	Proposal        *agent.Proposal
	ApprovalResult  string
	CommandExpanded bool
}
