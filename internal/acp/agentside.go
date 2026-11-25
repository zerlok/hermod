package acp

import "github.com/zerlok/hermod/internal/session"

// The agent's requests to the client, and its updates of a chat. A required
// text that may be empty, such as a file's content, is not checked: the empty
// text and a text left out read alike.

// ReadTextFileParams asks the client for a file's text.
type ReadTextFileParams struct {
	SessionID SessionID `json:"sessionId"`
	Path      string    `json:"path"`
	Line      *int64    `json:"line,omitzero"`
	Limit     *int64    `json:"limit,omitzero"`
	Extensible
}

func (p ReadTextFileParams) Chat() session.ChatID { return p.SessionID.chat() }

func (p ReadTextFileParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), need(p.Path != "", "path"))
}

func (p *ReadTextFileParams) mapPaths(m mapper) {
	m.path(&p.Path)
	m.meta(&p.Meta)
}

// ReadTextFileResult answers with the file's text.
type ReadTextFileResult struct {
	Content string `json:"content"`
	Extensible
}

// WriteTextFileParams asks the client to write a file.
type WriteTextFileParams struct {
	SessionID SessionID `json:"sessionId"`
	Path      string    `json:"path"`
	Content   string    `json:"content"`
	Extensible
}

func (p WriteTextFileParams) Chat() session.ChatID { return p.SessionID.chat() }

func (p WriteTextFileParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), need(p.Path != "", "path"))
}

func (p *WriteTextFileParams) mapPaths(m mapper) {
	m.path(&p.Path)
	m.meta(&p.Meta)
}

// WriteTextFileResult answers that the file is written.
type WriteTextFileResult struct{ EmptyResult }

// RequestPermissionParams asks the user to allow a tool call.
type RequestPermissionParams struct {
	SessionID SessionID          `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
	Extensible
}

func (p RequestPermissionParams) Chat() session.ChatID { return p.SessionID.chat() }

func (p RequestPermissionParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), in("toolCall", p.ToolCall.Validate()),
		need(p.Options != nil, "options"), each("options", p.Options))
}

func (p *RequestPermissionParams) mapPaths(m mapper) {
	p.ToolCall.mapPaths(m)
	mapEach(m, &p.Options)
	m.meta(&p.Meta)
}

// PermissionOption is one answer the user may give.
type PermissionOption struct {
	OptionID string               `json:"optionId"`
	Name     string               `json:"name"`
	Kind     PermissionOptionKind `json:"kind"`
	Extensible
}

func (o PermissionOption) Validate() error {
	return check(need(o.OptionID != "", "optionId"), need(o.Name != "", "name"), need(o.Kind != "", "kind"))
}

// PermissionOptionKind is what an answer allows.
type PermissionOptionKind string

func (k *PermissionOptionKind) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, k, "allow_once", "allow_always", "reject_once", "reject_always")
}

// RequestPermissionResult is the user's answer.
type RequestPermissionResult struct {
	Outcome RequestPermissionOutcome `json:"outcome"`
	Extensible
}

func (r RequestPermissionResult) Validate() error { return in("outcome", r.Outcome.Validate()) }

func (r *RequestPermissionResult) mapPaths(m mapper) {
	r.Outcome.mapPaths(m)
	m.meta(&r.Meta)
}

// RequestPermissionOutcome is the answer: cancelled, or one option selected.
type RequestPermissionOutcome struct {
	Cancelled *Cancelled
	Selected  *SelectedPermissionOutcome
}

func (u *RequestPermissionOutcome) arms() []arm {
	return []arm{
		armOf("cancelled", &u.Cancelled),
		armOf("selected", &u.Selected),
	}
}

func (u *RequestPermissionOutcome) UnmarshalJSON(b []byte) error {
	*u = RequestPermissionOutcome{}
	return decodeUnion(b, "outcome", u.arms())
}

func (u RequestPermissionOutcome) MarshalJSON() ([]byte, error) {
	return encodeUnion("outcome", u.arms())
}

func (u RequestPermissionOutcome) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

// mapPaths maps a selected option; a cancelled outcome holds nothing.
func (u *RequestPermissionOutcome) mapPaths(m mapper) { mapOpt(m, &u.Selected) }

// Cancelled is the outcome of a request its prompt's cancel ended.
type Cancelled struct{}

// SelectedPermissionOutcome is the option the user selected.
type SelectedPermissionOutcome struct {
	OptionID string `json:"optionId"`
	Extensible
}

func (o SelectedPermissionOutcome) Validate() error { return need(o.OptionID != "", "optionId") }

// CreateTerminalParams asks the client to run a command in a terminal.
type CreateTerminalParams struct {
	SessionID       SessionID     `json:"sessionId"`
	Command         string        `json:"command"`
	Args            []string      `json:"args,omitzero"`
	Env             []EnvVariable `json:"env,omitzero"`
	Cwd             *string       `json:"cwd,omitzero"`
	OutputByteLimit *int64        `json:"outputByteLimit,omitzero"`
	Extensible
}

func (p CreateTerminalParams) Chat() session.ChatID { return p.SessionID.chat() }

func (p CreateTerminalParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), need(p.Command != "", "command"), each("env", p.Env))
}

// mapPaths maps the terminal's directory. Its command is left as written: a
// program name is looked up on the PATH of the workstation that runs it.
func (p *CreateTerminalParams) mapPaths(m mapper) {
	m.optPath(&p.Cwd)
	mapEach(m, &p.Env)
	m.meta(&p.Meta)
}

// CreateTerminalResult names the terminal made.
type CreateTerminalResult struct {
	TerminalID string `json:"terminalId"`
	Extensible
}

func (r CreateTerminalResult) Validate() error { return need(r.TerminalID != "", "terminalId") }

// TerminalParams are what the params of a message that names a terminal of a
// chat hold, such as terminal/output's.
type TerminalParams struct {
	SessionID  SessionID `json:"sessionId"`
	TerminalID string    `json:"terminalId"`
	Extensible
}

func (p TerminalParams) Chat() session.ChatID { return p.SessionID.chat() }

func (p TerminalParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), need(p.TerminalID != "", "terminalId"))
}

// TerminalOutputParams asks for a terminal's output.
type TerminalOutputParams struct{ TerminalParams }

// ReleaseTerminalParams lets a terminal go.
type ReleaseTerminalParams struct{ TerminalParams }

// ReleaseTerminalResult answers a terminal/release.
type ReleaseTerminalResult struct{ EmptyResult }

// WaitForTerminalExitParams waits for a terminal's command to end.
type WaitForTerminalExitParams struct{ TerminalParams }

// KillTerminalParams kills a terminal's command.
type KillTerminalParams struct{ TerminalParams }

// KillTerminalResult answers a terminal/kill.
type KillTerminalResult struct{ EmptyResult }

// TerminalOutputResult is a terminal's output so far.
type TerminalOutputResult struct {
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
	ExitStatus *TerminalExitStatus `json:"exitStatus,omitzero"`
	Extensible
}

func (r *TerminalOutputResult) mapPaths(m mapper) {
	mapOpt(m, &r.ExitStatus)
	m.meta(&r.Meta)
}

// TerminalExitStatus is how a terminal's command ended.
type TerminalExitStatus struct {
	ExitCode *int64  `json:"exitCode,omitzero"`
	Signal   *string `json:"signal,omitzero"`
	Extensible
}

// WaitForTerminalExitResult is how the command ended.
type WaitForTerminalExitResult struct{ TerminalExitStatus }

// SessionNotification is an update of a chat the agent reports.
type SessionNotification struct {
	SessionID SessionID     `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
	Extensible
}

func (p SessionNotification) Chat() session.ChatID { return p.SessionID.chat() }

func (p SessionNotification) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), in("update", p.Update.Validate()))
}

func (p *SessionNotification) mapPaths(m mapper) {
	p.Update.mapPaths(m)
	m.meta(&p.Meta)
}

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

func (u *SessionUpdate) arms() []arm {
	return []arm{
		armOf("user_message_chunk", &u.UserMessageChunk),
		armOf("agent_message_chunk", &u.AgentMessageChunk),
		armOf("agent_thought_chunk", &u.AgentThoughtChunk),
		armOf("tool_call", &u.ToolCall),
		armOf("tool_call_update", &u.ToolCallUpdate),
		armOf("plan", &u.Plan),
		armOf("plan_update", &u.PlanUpdate),
		armOf("plan_removed", &u.PlanRemoved),
		armOf("available_commands_update", &u.AvailableCommandsUpdate),
		armOf("current_mode_update", &u.CurrentModeUpdate),
		armOf("config_option_update", &u.ConfigOptionUpdate),
		armOf("session_info_update", &u.SessionInfoUpdate),
		armOf("usage_update", &u.UsageUpdate),
		armOf("notice", &u.Notice),
		armOf("compaction_update", &u.CompactionUpdate),
		armOf("compaction_summary_chunk", &u.CompactionSummaryChunk),
		armOf("subagent_update", &u.SubagentUpdate),
		armOf("session_message", &u.SessionMessage),
		armOf("session_message_chunk", &u.SessionMessageChunk),
	}
}

func (u *SessionUpdate) UnmarshalJSON(b []byte) error {
	*u = SessionUpdate{}
	return decodeUnion(b, "sessionUpdate", u.arms())
}

func (u SessionUpdate) MarshalJSON() ([]byte, error) { return encodeUnion("sessionUpdate", u.arms()) }

func (u SessionUpdate) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *SessionUpdate) mapPaths(m mapper) {
	mapOpt(m, &u.UserMessageChunk)
	mapOpt(m, &u.AgentMessageChunk)
	mapOpt(m, &u.AgentThoughtChunk)
	mapOpt(m, &u.ToolCall)
	mapOpt(m, &u.ToolCallUpdate)
	mapOpt(m, &u.Plan)
	mapOpt(m, &u.PlanUpdate)
	mapOpt(m, &u.PlanRemoved)
	mapOpt(m, &u.AvailableCommandsUpdate)
	mapOpt(m, &u.CurrentModeUpdate)
	mapOpt(m, &u.ConfigOptionUpdate)
	mapOpt(m, &u.SessionInfoUpdate)
	mapOpt(m, &u.UsageUpdate)
	mapOpt(m, &u.Notice)
	mapOpt(m, &u.CompactionUpdate)
	mapOpt(m, &u.CompactionSummaryChunk)
	mapOpt(m, &u.SubagentUpdate)
	mapOpt(m, &u.SessionMessage)
	mapOpt(m, &u.SessionMessageChunk)
}

// ContentChunk is a part of a message of the user's or the agent's.
type ContentChunk struct {
	Content   ContentBlock `json:"content"`
	MessageID *string      `json:"messageId,omitzero"`
	Extensible
}

func (c ContentChunk) Validate() error { return in("content", c.Content.Validate()) }

func (c *ContentChunk) mapPaths(m mapper) {
	c.Content.mapPaths(m)
	m.meta(&c.Meta)
}

// Plan is the agent's plan, every entry of it.
type Plan struct {
	Entries []PlanEntry `json:"entries"`
	Extensible
}

func (p Plan) Validate() error {
	return check(need(p.Entries != nil, "entries"), each("entries", p.Entries))
}

func (p *Plan) mapPaths(m mapper) {
	mapEach(m, &p.Entries)
	m.meta(&p.Meta)
}

// PlanEntry is one step of a plan.
type PlanEntry struct {
	Content  string            `json:"content"`
	Priority PlanEntryPriority `json:"priority"`
	Status   PlanEntryStatus   `json:"status"`
	Extensible
}

func (e PlanEntry) Validate() error {
	return check(need(e.Priority != "", "priority"), need(e.Status != "", "status"))
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
	Extensible
}

func (u PlanUpdate) Validate() error { return in("plan", u.Plan.Validate()) }

func (u *PlanUpdate) mapPaths(m mapper) {
	u.Plan.mapPaths(m)
	m.meta(&u.Meta)
}

// PlanUpdateContent is a plan as entries or as markdown.
type PlanUpdateContent struct {
	Items    *PlanItems
	Markdown *PlanMarkdown
}

func (u *PlanUpdateContent) arms() []arm {
	return []arm{
		armOf("items", &u.Items),
		armOf("markdown", &u.Markdown),
	}
}

func (u *PlanUpdateContent) UnmarshalJSON(b []byte) error {
	*u = PlanUpdateContent{}
	return decodeUnion(b, "type", u.arms())
}

func (u PlanUpdateContent) MarshalJSON() ([]byte, error) { return encodeUnion("type", u.arms()) }

func (u PlanUpdateContent) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *PlanUpdateContent) mapPaths(m mapper) {
	mapOpt(m, &u.Items)
	mapOpt(m, &u.Markdown)
}

// PlanItems is a plan as entries.
type PlanItems struct {
	PlanID  string      `json:"planId"`
	Entries []PlanEntry `json:"entries"`
	Extensible
}

func (p PlanItems) Validate() error {
	return check(need(p.PlanID != "", "planId"), need(p.Entries != nil, "entries"), each("entries", p.Entries))
}

func (p *PlanItems) mapPaths(m mapper) {
	mapEach(m, &p.Entries)
	m.meta(&p.Meta)
}

// PlanMarkdown is a plan as markdown, and the file that holds it, if any.
type PlanMarkdown struct {
	PlanID  string  `json:"planId"`
	Content string  `json:"content"`
	Path    *string `json:"path,omitzero"`
	Extensible
}

func (p PlanMarkdown) Validate() error { return need(p.PlanID != "", "planId") }

func (p *PlanMarkdown) mapPaths(m mapper) {
	m.optPath(&p.Path)
	m.meta(&p.Meta)
}

// PlanRemoved removes a plan.
type PlanRemoved struct {
	PlanID string `json:"planId"`
	Extensible
}

func (p PlanRemoved) Validate() error { return need(p.PlanID != "", "planId") }

// AvailableCommandsUpdate is the chat's commands, every one of them.
type AvailableCommandsUpdate struct {
	AvailableCommands []AvailableCommand `json:"availableCommands"`
	Extensible
}

func (u AvailableCommandsUpdate) Validate() error {
	return check(need(u.AvailableCommands != nil, "availableCommands"), each("availableCommands", u.AvailableCommands))
}

func (u *AvailableCommandsUpdate) mapPaths(m mapper) {
	mapEach(m, &u.AvailableCommands)
	m.meta(&u.Meta)
}

// AvailableCommand is a command the user may run in a chat.
type AvailableCommand struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Input       *UnstructuredCommandInput `json:"input,omitzero"`
	Extensible
}

func (c AvailableCommand) Validate() error { return need(c.Name != "", "name") }

func (c *AvailableCommand) mapPaths(m mapper) {
	mapOpt(m, &c.Input)
	m.meta(&c.Meta)
}

// UnstructuredCommandInput is a command's input, the text after its name.
type UnstructuredCommandInput struct {
	Hint string `json:"hint"`
	Extensible
}

// CurrentModeUpdate is the chat's mode now.
type CurrentModeUpdate struct {
	CurrentModeID string `json:"currentModeId"`
	Extensible
}

func (u CurrentModeUpdate) Validate() error { return need(u.CurrentModeID != "", "currentModeId") }

// ConfigOptionUpdate is the chat's options, every one of them.
type ConfigOptionUpdate struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
	Extensible
}

func (u ConfigOptionUpdate) Validate() error {
	return check(need(u.ConfigOptions != nil, "configOptions"), each("configOptions", u.ConfigOptions))
}

func (u *ConfigOptionUpdate) mapPaths(m mapper) {
	mapEach(m, &u.ConfigOptions)
	m.meta(&u.Meta)
}

// SessionInfoUpdate changes what the chat's list entry says. A member left
// out is unchanged, and null clears it.
type SessionInfoUpdate struct {
	Title     Nullable[string] `json:"title,omitzero"`
	UpdatedAt Nullable[string] `json:"updatedAt,omitzero"`
	Extensible
}

// UsageUpdate is how much of its context a chat uses, and its cost.
type UsageUpdate struct {
	Used uint64 `json:"used"`
	Size uint64 `json:"size"`
	Cost *Cost  `json:"cost,omitzero"`
	Extensible
}

func (u UsageUpdate) Validate() error { return opt("cost", u.Cost) }

func (u *UsageUpdate) mapPaths(m mapper) {
	mapOpt(m, &u.Cost)
	m.meta(&u.Meta)
}

// Cost is an amount of money.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Extensible
}

func (c Cost) Validate() error { return need(c.Currency != "", "currency") }

// Notice is a notice for the user. Its severity is open: ACP names info,
// warning and error, and lets an agent write others.
type Notice struct {
	Severity    string  `json:"severity"`
	Title       string  `json:"title"`
	Description *string `json:"description,omitzero"`
	Extensible
}

func (n Notice) Validate() error { return need(n.Severity != "", "severity") }

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

func (u CompactionUpdate) Validate() error {
	return check(need(u.CompactionID != "", "compactionId"), need(u.Status != "", "status"))
}

func (u *CompactionUpdate) mapPaths(m mapper) {
	mapNullable(&u.Summary, func(p *[]ContentBlock) { mapEach(m, p) })
	mapNullable(&u.Meta, m.meta)
}

// CompactionSummaryChunk is a part of a compaction's summary.
type CompactionSummaryChunk struct {
	CompactionID string       `json:"compactionId"`
	Content      ContentBlock `json:"content"`
	Extensible
}

func (c CompactionSummaryChunk) Validate() error {
	return check(need(c.CompactionID != "", "compactionId"), in("content", c.Content.Validate()))
}

func (c *CompactionSummaryChunk) mapPaths(m mapper) {
	c.Content.mapPaths(m)
	m.meta(&c.Meta)
}

// SubagentUpdate changes what the chat says of a child session. A member
// left out is unchanged, and null clears it.
type SubagentUpdate struct {
	SessionID             SessionID                             `json:"sessionId"`
	Title                 Nullable[string]                      `json:"title,omitzero"`
	Description           Nullable[string]                      `json:"description,omitzero"`
	Capabilities          Nullable[SubagentSessionCapabilities] `json:"capabilities,omitzero"`
	State                 Nullable[StateUpdate]                 `json:"state,omitzero"`
	Cwd                   Nullable[string]                      `json:"cwd,omitzero"`
	AdditionalDirectories Nullable[[]string]                    `json:"additionalDirectories,omitzero"`
	Extensible
}

func (u SubagentUpdate) Validate() error { return need(u.SessionID != "", "sessionId") }

func (u *SubagentUpdate) mapPaths(m mapper) {
	mapNullable(&u.Capabilities, func(c *SubagentSessionCapabilities) { c.mapPaths(m) })
	mapNullable(&u.State, func(s *StateUpdate) { s.mapPaths(m) })
	mapNullable(&u.Cwd, m.path)
	mapNullable(&u.AdditionalDirectories, m.paths)
	m.meta(&u.Meta)
}

// SubagentSessionCapabilities are what the client may do to a child session.
type SubagentSessionCapabilities struct {
	Cancel *Capability `json:"cancel,omitzero"`
	Extensible
}

func (c *SubagentSessionCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.Cancel)
	m.meta(&c.Meta)
}

// StateUpdate is a child session's state.
type StateUpdate struct {
	Running        *Capability
	Idle           *IdleStateUpdate
	RequiresAction *Capability
	Unknown        *Capability
}

func (u *StateUpdate) arms() []arm {
	return []arm{
		armOf("running", &u.Running),
		armOf("idle", &u.Idle),
		armOf("requires_action", &u.RequiresAction),
		armOf("unknown", &u.Unknown),
	}
}

func (u *StateUpdate) UnmarshalJSON(b []byte) error {
	*u = StateUpdate{}
	return decodeUnion(b, "state", u.arms())
}

func (u StateUpdate) MarshalJSON() ([]byte, error) { return encodeUnion("state", u.arms()) }

func (u StateUpdate) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *StateUpdate) mapPaths(m mapper) {
	mapOpt(m, &u.Running)
	mapOpt(m, &u.Idle)
	mapOpt(m, &u.RequiresAction)
	mapOpt(m, &u.Unknown)
}

// IdleStateUpdate is a child session that does no work, and why it stopped,
// if it says.
type IdleStateUpdate struct {
	StopReason *IdleStopReason `json:"stopReason,omitzero"`
	Error      *Error          `json:"error,omitzero"`
	Extensible
}

func (u *IdleStateUpdate) mapPaths(m mapper) {
	mapOpt(m, &u.Error)
	m.meta(&u.Meta)
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

func (s SessionMessage) Validate() error { return need(s.MessageID != "", "messageId") }

func (s *SessionMessage) mapPaths(m mapper) {
	mapNullable(&s.Content, func(p *[]ContentBlock) { mapEach(m, p) })
	mapNullable(&s.Meta, m.meta)
}

// SessionMessageChunk is a part of a message between two sessions.
type SessionMessageChunk struct {
	MessageID string       `json:"messageId"`
	Content   ContentBlock `json:"content"`
	Extensible
}

func (c SessionMessageChunk) Validate() error {
	return check(need(c.MessageID != "", "messageId"), in("content", c.Content.Validate()))
}

func (c *SessionMessageChunk) mapPaths(m mapper) {
	c.Content.mapPaths(m)
	m.meta(&c.Meta)
}
