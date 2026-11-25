package acp

import "github.com/zerlok/hermod/internal/session"

// asks is the part of a request's params that says the request is the agent's
// question to the client.
type asks struct{}

func (asks) Kind() session.Kind { return session.Question }

// ReadTextFileParams asks the client for a file's text.
type ReadTextFileParams struct {
	asks
	noDir
	SessionID SessionID `json:"sessionId"`
	Path      string    `json:"path" acp:"path"`
	Line      *int64    `json:"line,omitzero"`
	Limit     *int64    `json:"limit,omitzero"`
	Meta      Meta      `json:"_meta,omitzero"`
}

func (ReadTextFileParams) Method() string         { return "fs/read_text_file" }
func (p ReadTextFileParams) Chat() session.ChatID { return p.SessionID.chat() }

// ReadTextFileResult answers with the file's text.
type ReadTextFileResult struct {
	noChat
	Content string `json:"content"`
	Meta    Meta   `json:"_meta,omitzero"`
}

// WriteTextFileParams asks the client to write a file.
type WriteTextFileParams struct {
	asks
	noDir
	SessionID SessionID `json:"sessionId"`
	Path      string    `json:"path" acp:"path"`
	Content   string    `json:"content"`
	Meta      Meta      `json:"_meta,omitzero"`
}

func (WriteTextFileParams) Method() string         { return "fs/write_text_file" }
func (p WriteTextFileParams) Chat() session.ChatID { return p.SessionID.chat() }

// WriteTextFileResult answers that the file is written.
type WriteTextFileResult struct {
	noChat
	Meta Meta `json:"_meta,omitzero"`
}

// RequestPermissionParams asks the user to allow a tool call.
type RequestPermissionParams struct {
	asks
	noDir
	SessionID SessionID          `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
	Meta      Meta               `json:"_meta,omitzero"`
}

func (RequestPermissionParams) Method() string         { return "session/request_permission" }
func (p RequestPermissionParams) Chat() session.ChatID { return p.SessionID.chat() }

// PermissionOption is one answer the user may give.
type PermissionOption struct {
	OptionID string               `json:"optionId"`
	Name     string               `json:"name"`
	Kind     PermissionOptionKind `json:"kind"`
	Meta     Meta                 `json:"_meta,omitzero"`
}

// PermissionOptionKind is what an answer allows.
type PermissionOptionKind string

func (k *PermissionOptionKind) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, k, "allow_once", "allow_always", "reject_once", "reject_always")
}

// RequestPermissionResult is the user's answer.
type RequestPermissionResult struct {
	noChat
	Outcome RequestPermissionOutcome `json:"outcome"`
	Meta    Meta                     `json:"_meta,omitzero"`
}

// RequestPermissionOutcome is the answer: cancelled, or one option selected.
type RequestPermissionOutcome struct {
	Cancelled *Cancelled
	Selected  *SelectedPermissionOutcome
}

// Cancelled is the outcome of a request its prompt's cancel ended.
type Cancelled struct{}

// SelectedPermissionOutcome is the option the user selected.
type SelectedPermissionOutcome struct {
	OptionID string `json:"optionId"`
	Meta     Meta   `json:"_meta,omitzero"`
}

func (o *RequestPermissionOutcome) UnmarshalJSON(b []byte) error {
	*o = RequestPermissionOutcome{}
	return decodeTagged(b, "outcome", variants{"cancelled": &o.Cancelled, "selected": &o.Selected})
}

func (o RequestPermissionOutcome) MarshalJSON() ([]byte, error) {
	return encodeTagged("outcome", variants{"cancelled": o.Cancelled, "selected": o.Selected})
}

// CreateTerminalParams asks the client to run a command in a terminal.
type CreateTerminalParams struct {
	asks
	noDir
	SessionID       SessionID     `json:"sessionId"`
	Command         string        `json:"command"`
	Args            []string      `json:"args,omitzero"`
	Env             []EnvVariable `json:"env,omitzero"`
	Cwd             *string       `json:"cwd,omitzero" acp:"path"`
	OutputByteLimit *int64        `json:"outputByteLimit,omitzero"`
	Meta            Meta          `json:"_meta,omitzero"`
}

func (CreateTerminalParams) Method() string         { return "terminal/create" }
func (p CreateTerminalParams) Chat() session.ChatID { return p.SessionID.chat() }

// CreateTerminalResult names the terminal made.
type CreateTerminalResult struct {
	noChat
	TerminalID string `json:"terminalId"`
	Meta       Meta   `json:"_meta,omitzero"`
}

// TerminalOutputParams asks for a terminal's output.
type TerminalOutputParams struct {
	asks
	noDir
	SessionID  SessionID `json:"sessionId"`
	TerminalID string    `json:"terminalId"`
	Meta       Meta      `json:"_meta,omitzero"`
}

func (TerminalOutputParams) Method() string         { return "terminal/output" }
func (p TerminalOutputParams) Chat() session.ChatID { return p.SessionID.chat() }

// TerminalOutputResult is a terminal's output so far.
type TerminalOutputResult struct {
	noChat
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
	ExitStatus *TerminalExitStatus `json:"exitStatus,omitzero"`
	Meta       Meta                `json:"_meta,omitzero"`
}

// TerminalExitStatus is how a terminal's command ended.
type TerminalExitStatus struct {
	ExitCode *int64  `json:"exitCode,omitzero"`
	Signal   *string `json:"signal,omitzero"`
	Meta     Meta    `json:"_meta,omitzero"`
}

// ReleaseTerminalParams lets a terminal go.
type ReleaseTerminalParams struct {
	asks
	noDir
	SessionID  SessionID `json:"sessionId"`
	TerminalID string    `json:"terminalId"`
	Meta       Meta      `json:"_meta,omitzero"`
}

func (ReleaseTerminalParams) Method() string         { return "terminal/release" }
func (p ReleaseTerminalParams) Chat() session.ChatID { return p.SessionID.chat() }

// ReleaseTerminalResult answers a terminal/release.
type ReleaseTerminalResult struct {
	noChat
	Meta Meta `json:"_meta,omitzero"`
}

// WaitForTerminalExitParams waits for a terminal's command to end.
type WaitForTerminalExitParams struct {
	asks
	noDir
	SessionID  SessionID `json:"sessionId"`
	TerminalID string    `json:"terminalId"`
	Meta       Meta      `json:"_meta,omitzero"`
}

func (WaitForTerminalExitParams) Method() string         { return "terminal/wait_for_exit" }
func (p WaitForTerminalExitParams) Chat() session.ChatID { return p.SessionID.chat() }

// WaitForTerminalExitResult is how the command ended.
type WaitForTerminalExitResult struct {
	noChat
	ExitCode *int64  `json:"exitCode,omitzero"`
	Signal   *string `json:"signal,omitzero"`
	Meta     Meta    `json:"_meta,omitzero"`
}

// KillTerminalParams kills a terminal's command.
type KillTerminalParams struct {
	asks
	noDir
	SessionID  SessionID `json:"sessionId"`
	TerminalID string    `json:"terminalId"`
	Meta       Meta      `json:"_meta,omitzero"`
}

func (KillTerminalParams) Method() string         { return "terminal/kill" }
func (p KillTerminalParams) Chat() session.ChatID { return p.SessionID.chat() }

// KillTerminalResult answers a terminal/kill.
type KillTerminalResult struct {
	noChat
	Meta Meta `json:"_meta,omitzero"`
}

// SessionNotification is an update of a chat the agent reports.
type SessionNotification struct {
	passes
	noDir
	SessionID SessionID     `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
	Meta      Meta          `json:"_meta,omitzero"`
}

func (SessionNotification) Method() string         { return "session/update" }
func (p SessionNotification) Chat() session.ChatID { return p.SessionID.chat() }

// SessionUpdate is what changed in a chat.
type SessionUpdate struct {
	UserMessageChunk        *ContentChunk
	AgentMessageChunk       *ContentChunk
	AgentThoughtChunk       *ContentChunk
	ToolCall                *ToolCall
	ToolCallUpdate          *ToolCallUpdate
	Plan                    *Plan
	PlanUpdate              *PlanUpdate
	PlanRemoved             *PlanRemoved
	AvailableCommandsUpdate *AvailableCommandsUpdate
	CurrentModeUpdate       *CurrentModeUpdate
	ConfigOptionUpdate      *ConfigOptionUpdate
	SessionInfoUpdate       *SessionInfoUpdate
	UsageUpdate             *UsageUpdate
	Notice                  *Notice
	CompactionUpdate        *CompactionUpdate
	CompactionSummaryChunk  *CompactionSummaryChunk
	SubagentUpdate          *SubagentUpdate
	SessionMessage          *SessionMessage
	SessionMessageChunk     *SessionMessageChunk
}

func (u *SessionUpdate) UnmarshalJSON(b []byte) error {
	*u = SessionUpdate{}
	return decodeTagged(b, "sessionUpdate", variants{
		"user_message_chunk":        &u.UserMessageChunk,
		"agent_message_chunk":       &u.AgentMessageChunk,
		"agent_thought_chunk":       &u.AgentThoughtChunk,
		"tool_call":                 &u.ToolCall,
		"tool_call_update":          &u.ToolCallUpdate,
		"plan":                      &u.Plan,
		"plan_update":               &u.PlanUpdate,
		"plan_removed":              &u.PlanRemoved,
		"available_commands_update": &u.AvailableCommandsUpdate,
		"current_mode_update":       &u.CurrentModeUpdate,
		"config_option_update":      &u.ConfigOptionUpdate,
		"session_info_update":       &u.SessionInfoUpdate,
		"usage_update":              &u.UsageUpdate,
		"notice":                    &u.Notice,
		"compaction_update":         &u.CompactionUpdate,
		"compaction_summary_chunk":  &u.CompactionSummaryChunk,
		"subagent_update":           &u.SubagentUpdate,
		"session_message":           &u.SessionMessage,
		"session_message_chunk":     &u.SessionMessageChunk,
	})
}

func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	return encodeTagged("sessionUpdate", variants{
		"user_message_chunk":        u.UserMessageChunk,
		"agent_message_chunk":       u.AgentMessageChunk,
		"agent_thought_chunk":       u.AgentThoughtChunk,
		"tool_call":                 u.ToolCall,
		"tool_call_update":          u.ToolCallUpdate,
		"plan":                      u.Plan,
		"plan_update":               u.PlanUpdate,
		"plan_removed":              u.PlanRemoved,
		"available_commands_update": u.AvailableCommandsUpdate,
		"current_mode_update":       u.CurrentModeUpdate,
		"config_option_update":      u.ConfigOptionUpdate,
		"session_info_update":       u.SessionInfoUpdate,
		"usage_update":              u.UsageUpdate,
		"notice":                    u.Notice,
		"compaction_update":         u.CompactionUpdate,
		"compaction_summary_chunk":  u.CompactionSummaryChunk,
		"subagent_update":           u.SubagentUpdate,
		"session_message":           u.SessionMessage,
		"session_message_chunk":     u.SessionMessageChunk,
	})
}

// ContentChunk is a part of a message of the user's or the agent's.
type ContentChunk struct {
	Content   ContentBlock `json:"content"`
	MessageID *string      `json:"messageId,omitzero"`
	Meta      Meta         `json:"_meta,omitzero"`
}

// Plan is the agent's plan, every entry of it.
type Plan struct {
	Entries []PlanEntry `json:"entries"`
	Meta    Meta        `json:"_meta,omitzero"`
}

// PlanEntry is one step of a plan.
type PlanEntry struct {
	Content  string            `json:"content"`
	Priority PlanEntryPriority `json:"priority"`
	Status   PlanEntryStatus   `json:"status"`
	Meta     Meta              `json:"_meta,omitzero"`
}

// PlanEntryPriority is how much a step matters.
type PlanEntryPriority string

func (p *PlanEntryPriority) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, p, "high", "medium", "low")
}

// PlanEntryStatus is how far a step is.
type PlanEntryStatus string

func (s *PlanEntryStatus) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, s, "pending", "in_progress", "completed")
}

// PlanUpdate is a plan the agent made or changed, by its ID.
type PlanUpdate struct {
	Plan PlanUpdateContent `json:"plan"`
	Meta Meta              `json:"_meta,omitzero"`
}

// PlanUpdateContent is a plan as entries or as markdown.
type PlanUpdateContent struct {
	Items    *PlanItems
	Markdown *PlanMarkdown
}

func (c *PlanUpdateContent) UnmarshalJSON(b []byte) error {
	*c = PlanUpdateContent{}
	return decodeTagged(b, "type", variants{"items": &c.Items, "markdown": &c.Markdown})
}

func (c PlanUpdateContent) MarshalJSON() ([]byte, error) {
	return encodeTagged("type", variants{"items": c.Items, "markdown": c.Markdown})
}

// PlanItems is a plan as entries.
type PlanItems struct {
	PlanID  string      `json:"planId"`
	Entries []PlanEntry `json:"entries"`
	Meta    Meta        `json:"_meta,omitzero"`
}

// PlanMarkdown is a plan as markdown, and the file that holds it, if any.
type PlanMarkdown struct {
	PlanID  string  `json:"planId"`
	Content string  `json:"content"`
	Path    *string `json:"path,omitzero" acp:"path"`
	Meta    Meta    `json:"_meta,omitzero"`
}

// PlanRemoved removes a plan.
type PlanRemoved struct {
	PlanID string `json:"planId"`
	Meta   Meta   `json:"_meta,omitzero"`
}

// AvailableCommandsUpdate is the chat's commands, every one of them.
type AvailableCommandsUpdate struct {
	AvailableCommands []AvailableCommand `json:"availableCommands"`
	Meta              Meta               `json:"_meta,omitzero"`
}

// AvailableCommand is a command the user may run in a chat.
type AvailableCommand struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Input       *UnstructuredCommandInput `json:"input,omitzero"`
	Meta        Meta                      `json:"_meta,omitzero"`
}

// UnstructuredCommandInput is a command's input, the text after its name.
type UnstructuredCommandInput struct {
	Hint string `json:"hint"`
	Meta Meta   `json:"_meta,omitzero"`
}

// CurrentModeUpdate is the chat's mode now.
type CurrentModeUpdate struct {
	CurrentModeID string `json:"currentModeId"`
	Meta          Meta   `json:"_meta,omitzero"`
}

// ConfigOptionUpdate is the chat's options, every one of them.
type ConfigOptionUpdate struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
	Meta          Meta                  `json:"_meta,omitzero"`
}

// SessionInfoUpdate changes what the chat's list entry says. A member left
// out is unchanged, and null clears it.
type SessionInfoUpdate struct {
	Title     Nullable[string] `json:"title,omitzero"`
	UpdatedAt Nullable[string] `json:"updatedAt,omitzero"`
	Meta      Meta             `json:"_meta,omitzero"`
}

// UsageUpdate is how much of its context a chat uses, and its cost.
type UsageUpdate struct {
	Used uint64 `json:"used"`
	Size uint64 `json:"size"`
	Cost *Cost  `json:"cost,omitzero"`
	Meta Meta   `json:"_meta,omitzero"`
}

// Cost is an amount of money.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Meta     Meta    `json:"_meta,omitzero"`
}

// Notice is a notice for the user. Its severity is open: ACP names info,
// warning and error, and lets an agent write others.
type Notice struct {
	Severity    string  `json:"severity"`
	Title       string  `json:"title"`
	Description *string `json:"description,omitzero"`
	Meta        Meta    `json:"_meta,omitzero"`
}

// CompactionUpdate is how a compaction of the chat's context goes. Its status
// is open, as a Notice's severity is. Its summary, error and _meta patch what
// the client holds: a member left out is unchanged, and null clears it.
type CompactionUpdate struct {
	CompactionID string                   `json:"compactionId"`
	Status       string                   `json:"status"`
	Summary      Nullable[[]ContentBlock] `json:"summary,omitzero"`
	Error        Nullable[string]         `json:"error,omitzero"`
	Meta         Nullable[Meta]           `json:"_meta,omitzero"`
}

// CompactionSummaryChunk is a part of a compaction's summary.
type CompactionSummaryChunk struct {
	CompactionID string       `json:"compactionId"`
	Content      ContentBlock `json:"content"`
	Meta         Meta         `json:"_meta,omitzero"`
}

// SubagentUpdate changes what the chat says of a child session. A member
// left out is unchanged, and null clears it.
type SubagentUpdate struct {
	SessionID             SessionID                             `json:"sessionId"`
	Title                 Nullable[string]                      `json:"title,omitzero"`
	Description           Nullable[string]                      `json:"description,omitzero"`
	Capabilities          Nullable[SubagentSessionCapabilities] `json:"capabilities,omitzero"`
	State                 Nullable[StateUpdate]                 `json:"state,omitzero"`
	Cwd                   Nullable[string]                      `json:"cwd,omitzero" acp:"path"`
	AdditionalDirectories Nullable[[]string]                    `json:"additionalDirectories,omitzero" acp:"path"`
	Meta                  Meta                                  `json:"_meta,omitzero"`
}

// SubagentSessionCapabilities are what the client may do to a child session.
type SubagentSessionCapabilities struct {
	Cancel *Capability `json:"cancel,omitzero"`
	Meta   Meta        `json:"_meta,omitzero"`
}

// StateUpdate is a child session's state.
type StateUpdate struct {
	Running        *Capability
	Idle           *IdleStateUpdate
	RequiresAction *Capability
	Unknown        *Capability
}

func (s *StateUpdate) UnmarshalJSON(b []byte) error {
	*s = StateUpdate{}
	return decodeTagged(b, "state", variants{"running": &s.Running, "idle": &s.Idle, "requires_action": &s.RequiresAction, "unknown": &s.Unknown})
}

func (s StateUpdate) MarshalJSON() ([]byte, error) {
	return encodeTagged("state", variants{"running": s.Running, "idle": s.Idle, "requires_action": s.RequiresAction, "unknown": s.Unknown})
}

// IdleStateUpdate is a child session that does no work, and why it stopped,
// if it says.
type IdleStateUpdate struct {
	StopReason *IdleStopReason `json:"stopReason,omitzero"`
	Error      *Error          `json:"error,omitzero"`
	Meta       Meta            `json:"_meta,omitzero"`
}

// IdleStopReason is why a child session stopped: a prompt's stop reason, or
// an error.
type IdleStopReason string

func (r *IdleStopReason) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, r, "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled", "error")
}

// SessionMessage is a message between two sessions. A member left out is
// unchanged, and null clears it.
type SessionMessage struct {
	MessageID          string                   `json:"messageId"`
	SenderSessionID    Nullable[SessionID]      `json:"senderSessionId,omitzero"`
	RecipientSessionID Nullable[SessionID]      `json:"recipientSessionId,omitzero"`
	SenderMessageID    Nullable[string]         `json:"senderMessageId,omitzero"`
	RecipientMessageID Nullable[string]         `json:"recipientMessageId,omitzero"`
	Content            Nullable[[]ContentBlock] `json:"content,omitzero"`
	Meta               Nullable[Meta]           `json:"_meta,omitzero"`
}

// SessionMessageChunk is a part of a message between two sessions.
type SessionMessageChunk struct {
	MessageID string       `json:"messageId"`
	Content   ContentBlock `json:"content"`
	Meta      Meta         `json:"_meta,omitzero"`
}
