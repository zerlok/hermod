package acp

import (
	"fmt"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/session"
)

// SessionID is ACP's ID of a session, which session calls a chat.
type SessionID string

func (id SessionID) chat() session.ChatID { return session.ChatID(id) }

// Standard is a registry of ACP's own methods.
func Standard() *Registry {
	r := NewRegistry()
	Request[InitializeParams, InitializeResult](r, "initialize", Client)
	Request[AuthenticateParams, AuthenticateResult](r, "authenticate", Client)
	Request[LogoutParams, LogoutResult](r, "logout", Client)
	Request[NewSessionParams, NewSessionResult](r, "session/new", Client)
	Request[LoadSessionParams, LoadSessionResult](r, "session/load", Client)
	Request[ResumeSessionParams, ResumeSessionResult](r, "session/resume", Client)
	Request[ListSessionsParams, ListSessionsResult](r, "session/list", Client)
	Request[DeleteSessionParams, DeleteSessionResult](r, "session/delete", Client)
	Request[CloseSessionParams, CloseSessionResult](r, "session/close", Client)
	Request[SetSessionModeParams, SetSessionModeResult](r, "session/set_mode", Client)
	Request[SetSessionConfigOptionParams, SetSessionConfigOptionResult](r, "session/set_config_option", Client)
	Request[PromptParams, PromptResult](r, "session/prompt", Client)
	Notification[CancelParams](r, "session/cancel", Client)

	Request[ReadTextFileParams, ReadTextFileResult](r, "fs/read_text_file", Agent)
	Request[WriteTextFileParams, WriteTextFileResult](r, "fs/write_text_file", Agent)
	Request[RequestPermissionParams, RequestPermissionResult](r, "session/request_permission", Agent)
	Request[CreateTerminalParams, CreateTerminalResult](r, "terminal/create", Agent)
	Request[TerminalOutputParams, TerminalOutputResult](r, "terminal/output", Agent)
	Request[ReleaseTerminalParams, ReleaseTerminalResult](r, "terminal/release", Agent)
	Request[WaitForTerminalExitParams, WaitForTerminalExitResult](r, "terminal/wait_for_exit", Agent)
	Request[KillTerminalParams, KillTerminalResult](r, "terminal/kill", Agent)
	Request[CreateElicitationParams, CreateElicitationResult](r, "elicitation/create", Agent)
	Notification[SessionNotification](r, "session/update", Agent)
	Notification[CompleteElicitationParams](r, "elicitation/complete", Agent)
	return r
}

// fromSession is a Result of session's in ACP's terms: what a request session
// answers itself is answered with.
func fromSession(r session.Result) (any, error) {
	switch r := r.(type) {
	case session.EmptyResult:
		return struct{}{}, nil
	case session.HandshakeResult:
		var caps AgentCapabilities
		if err := decode(r.Handshake.Capabilities(), &caps); err != nil {
			return nil, fmt.Errorf("hermod: the agent's capabilities: %w", err)
		}
		return InitializeResult{ProtocolVersion: r.Handshake.Version(), AgentCapabilities: &caps, AuthMethods: []AuthMethod{}}, nil
	case session.ListResult:
		// A conversation's state is the agent program's own and has no place
		// in ACP's list.
		sessions := make([]SessionInfo, 0, len(r.Conversations))
		for _, c := range r.Conversations {
			info := SessionInfo{SessionID: SessionID(c.ID), Cwd: c.Dir}
			if c.Title != "" {
				info.Title = &c.Title
			}
			if !c.Updated.IsZero() {
				updated := c.Updated.UTC().Format(time.RFC3339)
				info.UpdatedAt = &updated
			}
			sessions = append(sessions, info)
		}
		return ListSessionsResult{Sessions: sessions}, nil
	}
	return nil, fmt.Errorf("hermod: no answer of ACP's is a %T", r)
}

// InitializeParams starts a client's connection.
type InitializeParams struct {
	ProtocolVersion    int                 `json:"protocolVersion"`
	ClientCapabilities *ClientCapabilities `json:"clientCapabilities,omitzero"`
	ClientInfo         *Implementation     `json:"clientInfo,omitzero"`
	Extensible
}

func (InitializeParams) Kind() session.Kind { return session.Connect }

// Validate requires a protocol version, which ACP counts from 1.
func (p InitializeParams) Validate() error {
	return check(need(p.ProtocolVersion > 0, "protocolVersion"), opt("clientInfo", p.ClientInfo))
}

func (p *InitializeParams) mapPaths(m mapper) {
	mapOpt(m, &p.ClientCapabilities)
	mapOpt(m, &p.ClientInfo)
	m.meta(&p.Meta)
}

// withoutClientCapabilities is an initialize without the client capabilities
// that act on the client's own workstation.
func withoutClientCapabilities(m message[InitializeParams]) message[InitializeParams] {
	if m.value.ClientCapabilities == nil {
		return m
	}
	caps := *m.value.ClientCapabilities
	caps.FS, caps.Terminal = nil, nil
	m.value.ClientCapabilities = &caps
	return m
}

// InitializeResult answers an initialize. Hermod asks for no authentication of
// its own: an adapter that needs one is started on the remote, where its
// credentials are.
type InitializeResult struct {
	ProtocolVersion   int                `json:"protocolVersion"`
	AgentCapabilities *AgentCapabilities `json:"agentCapabilities,omitzero"`
	AuthMethods       []AuthMethod       `json:"authMethods,omitzero"`
	AgentInfo         *Implementation    `json:"agentInfo,omitzero"`
	Extensible
}

func (r InitializeResult) Validate() error {
	return check(need(r.ProtocolVersion > 0, "protocolVersion"), opt("agentInfo", r.AgentInfo))
}

func (r *InitializeResult) mapPaths(m mapper) {
	mapOpt(m, &r.AgentCapabilities)
	mapEach(m, &r.AuthMethods)
	mapOpt(m, &r.AgentInfo)
	m.meta(&r.Meta)
}

// toSession reads the answer as a handshake. ACP lets an agent leave out its
// capabilities, which then offer nothing.
func (r InitializeResult) toSession() (session.Result, bool) {
	caps := []byte("{}")
	if r.AgentCapabilities != nil {
		b, err := marshal(r.AgentCapabilities)
		if err != nil {
			return nil, false
		}
		caps = b
	}
	h, err := agent.NewHandshake(r.ProtocolVersion, caps)
	if err != nil {
		return nil, false
	}
	return session.HandshakeResult{Handshake: h}, true
}

// AuthenticateParams authenticates the client with one of the agent's methods.
type AuthenticateParams struct {
	MethodID string `json:"methodId"`
	Extensible
}

func (p AuthenticateParams) Validate() error { return need(p.MethodID != "", "methodId") }

// AuthenticateResult answers an authenticate.
type AuthenticateResult struct{ EmptyResult }

// LogoutParams ends the client's authentication.
type LogoutParams struct {
	Extensible
}

// LogoutResult answers a logout.
type LogoutResult struct{ EmptyResult }

// NewSessionParams opens a new chat in a directory.
type NewSessionParams struct {
	Cwd                   string        `json:"cwd"`
	AdditionalDirectories []string      `json:"additionalDirectories,omitzero"`
	MCPServers            []MCPServer   `json:"mcpServers"`
	SystemPrompt          *SystemPrompt `json:"systemPrompt,omitzero"`
	Extensible
}

func (NewSessionParams) Kind() session.Kind  { return session.Open }
func (p NewSessionParams) Dir() session.Path { return session.Path(p.Cwd) }

func (p NewSessionParams) Validate() error {
	return check(need(p.Cwd != "", "cwd"), need(p.MCPServers != nil, "mcpServers"), opt("systemPrompt", p.SystemPrompt))
}

func (p *NewSessionParams) mapPaths(m mapper) {
	m.path(&p.Cwd)
	m.paths(&p.AdditionalDirectories)
	mapEach(m, &p.MCPServers)
	mapOpt(m, &p.SystemPrompt)
	m.meta(&p.Meta)
}

// NewSessionResult answers a session/new with the chat it made.
type NewSessionResult struct {
	SessionID     SessionID             `json:"sessionId"`
	Modes         *SessionModeState     `json:"modes,omitzero"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitzero"`
	Extensible
}

func (r NewSessionResult) Chat() session.ChatID { return r.SessionID.chat() }

func (r NewSessionResult) Validate() error {
	return check(need(r.SessionID != "", "sessionId"), opt("modes", r.Modes), each("configOptions", r.ConfigOptions))
}

func (r *NewSessionResult) mapPaths(m mapper) {
	mapOpt(m, &r.Modes)
	mapEach(m, &r.ConfigOptions)
	m.meta(&r.Meta)
}

// LoadSessionParams opens a chat again, its history replayed.
type LoadSessionParams struct {
	SessionID             SessionID     `json:"sessionId"`
	Cwd                   string        `json:"cwd"`
	AdditionalDirectories []string      `json:"additionalDirectories,omitzero"`
	MCPServers            []MCPServer   `json:"mcpServers"`
	SystemPrompt          *SystemPrompt `json:"systemPrompt,omitzero"`
	Extensible
}

func (LoadSessionParams) Kind() session.Kind     { return session.Open }
func (p LoadSessionParams) Chat() session.ChatID { return p.SessionID.chat() }
func (p LoadSessionParams) Dir() session.Path    { return session.Path(p.Cwd) }

func (p LoadSessionParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), need(p.Cwd != "", "cwd"), need(p.MCPServers != nil, "mcpServers"), opt("systemPrompt", p.SystemPrompt))
}

func (p *LoadSessionParams) mapPaths(m mapper) {
	m.path(&p.Cwd)
	m.paths(&p.AdditionalDirectories)
	mapEach(m, &p.MCPServers)
	mapOpt(m, &p.SystemPrompt)
	m.meta(&p.Meta)
}

// OpenedSession is what the answer to a session/load or a session/resume
// holds: the chat's modes and options.
type OpenedSession struct {
	Modes         *SessionModeState     `json:"modes,omitzero"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitzero"`
	Extensible
}

func (r OpenedSession) Validate() error {
	return check(opt("modes", r.Modes), each("configOptions", r.ConfigOptions))
}

func (r *OpenedSession) mapPaths(m mapper) {
	mapOpt(m, &r.Modes)
	mapEach(m, &r.ConfigOptions)
	m.meta(&r.Meta)
}

// LoadSessionResult answers a session/load.
type LoadSessionResult struct{ OpenedSession }

// ResumeSessionParams opens a chat again without replaying its history.
type ResumeSessionParams struct {
	SessionID             SessionID     `json:"sessionId"`
	Cwd                   string        `json:"cwd"`
	AdditionalDirectories []string      `json:"additionalDirectories,omitzero"`
	MCPServers            []MCPServer   `json:"mcpServers,omitzero"`
	SystemPrompt          *SystemPrompt `json:"systemPrompt,omitzero"`
	Extensible
}

func (ResumeSessionParams) Kind() session.Kind     { return session.Open }
func (p ResumeSessionParams) Chat() session.ChatID { return p.SessionID.chat() }
func (p ResumeSessionParams) Dir() session.Path    { return session.Path(p.Cwd) }

func (p ResumeSessionParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), need(p.Cwd != "", "cwd"), opt("systemPrompt", p.SystemPrompt))
}

func (p *ResumeSessionParams) mapPaths(m mapper) {
	m.path(&p.Cwd)
	m.paths(&p.AdditionalDirectories)
	mapEach(m, &p.MCPServers)
	mapOpt(m, &p.SystemPrompt)
	m.meta(&p.Meta)
}

// ResumeSessionResult answers a session/resume.
type ResumeSessionResult struct{ OpenedSession }

// ListSessionsParams asks for the chats of a directory.
type ListSessionsParams struct {
	Cwd    *string `json:"cwd,omitzero"`
	Cursor *string `json:"cursor,omitzero"`
	Extensible
}

func (ListSessionsParams) Kind() session.Kind { return session.List }

func (p ListSessionsParams) Dir() session.Path {
	if p.Cwd == nil {
		return ""
	}
	return session.Path(*p.Cwd)
}

func (p *ListSessionsParams) mapPaths(m mapper) {
	m.optPath(&p.Cwd)
	m.meta(&p.Meta)
}

// ListSessionsResult answers a session/list.
type ListSessionsResult struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor *string       `json:"nextCursor,omitzero"`
	Extensible
}

func (r ListSessionsResult) Validate() error {
	return check(need(r.Sessions != nil, "sessions"), each("sessions", r.Sessions))
}

func (r *ListSessionsResult) mapPaths(m mapper) {
	mapEach(m, &r.Sessions)
	m.meta(&r.Meta)
}

// SessionInfo is one chat a session/list answers with.
type SessionInfo struct {
	SessionID             SessionID `json:"sessionId"`
	Cwd                   string    `json:"cwd"`
	AdditionalDirectories []string  `json:"additionalDirectories,omitzero"`
	Title                 *string   `json:"title,omitzero"`
	UpdatedAt             *string   `json:"updatedAt,omitzero"`
	Extensible
}

func (i SessionInfo) Validate() error {
	return check(need(i.SessionID != "", "sessionId"), need(i.Cwd != "", "cwd"))
}

func (i *SessionInfo) mapPaths(m mapper) {
	m.path(&i.Cwd)
	m.paths(&i.AdditionalDirectories)
	m.meta(&i.Meta)
}

// ChatParams are what the params of a message that names a chat and nothing
// else hold, such as session/delete's.
type ChatParams struct {
	SessionID SessionID `json:"sessionId"`
	Extensible
}

func (p ChatParams) Chat() session.ChatID { return p.SessionID.chat() }
func (p ChatParams) Validate() error      { return need(p.SessionID != "", "sessionId") }
func (p *ChatParams) mapPaths(m mapper)   { m.meta(&p.Meta) }

// EmptyResult is what the result of an answer that returns nothing but its
// _meta holds.
type EmptyResult struct {
	Extensible
}

// DeleteSessionParams deletes a chat.
type DeleteSessionParams struct{ ChatParams }

// DeleteSessionResult answers a session/delete.
type DeleteSessionResult struct{ EmptyResult }

// CloseSessionParams closes a chat: ACP's agent then ends it.
type CloseSessionParams struct{ ChatParams }

// Kind is Suspend: a client such as Air sends session/close for every tab it
// closes and at an IDE quit, so it only hides the chat, and an adapter never
// receives it.
func (CloseSessionParams) Kind() session.Kind { return session.Suspend }

// CloseSessionResult answers a session/close.
type CloseSessionResult struct{ EmptyResult }

// CancelParams cancels the turn under way in a chat.
type CancelParams struct{ ChatParams }

// SetSessionModeParams sets a chat's mode.
type SetSessionModeParams struct {
	SessionID SessionID `json:"sessionId"`
	ModeID    string    `json:"modeId"`
	Extensible
}

func (p SetSessionModeParams) Chat() session.ChatID { return p.SessionID.chat() }

func (p SetSessionModeParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), need(p.ModeID != "", "modeId"))
}

// SetSessionModeResult answers a session/set_mode.
type SetSessionModeResult struct{ EmptyResult }

// SetSessionConfigOptionParams sets one of a chat's options to a value its
// type allows. ACP leaves out the type of a select's.
type SetSessionConfigOptionParams struct {
	SessionID SessionID         `json:"sessionId"`
	ConfigID  string            `json:"configId"`
	Type      *ConfigOptionType `json:"type,omitzero"`
	Value     Value             `json:"value"`
	Extensible
}

func (p SetSessionConfigOptionParams) Chat() session.ChatID { return p.SessionID.chat() }

func (p SetSessionConfigOptionParams) Validate() error {
	t := ConfigSelect
	if p.Type != nil {
		t = *p.Type
	}
	return check(need(p.SessionID != "", "sessionId"), need(p.ConfigID != "", "configId"), t.check("value", p.Value))
}

// SetSessionConfigOptionResult answers a session/set_config_option with every
// option of the chat.
type SetSessionConfigOptionResult struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
	Extensible
}

func (r SetSessionConfigOptionResult) Validate() error {
	return check(need(r.ConfigOptions != nil, "configOptions"), each("configOptions", r.ConfigOptions))
}

func (r *SetSessionConfigOptionResult) mapPaths(m mapper) {
	mapEach(m, &r.ConfigOptions)
	m.meta(&r.Meta)
}

// PromptParams is the client's prompt in a chat.
type PromptParams struct {
	SessionID SessionID      `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
	Extensible
}

func (p PromptParams) Chat() session.ChatID { return p.SessionID.chat() }

func (p PromptParams) Validate() error {
	return check(need(p.SessionID != "", "sessionId"), need(p.Prompt != nil, "prompt"))
}

func (p *PromptParams) mapPaths(m mapper) {
	mapEach(m, &p.Prompt)
	m.meta(&p.Meta)
}

// PromptResult answers a prompt once its turn ends.
type PromptResult struct {
	StopReason StopReason `json:"stopReason"`
	Extensible
}

func (r PromptResult) Validate() error { return need(r.StopReason != "", "stopReason") }

// StopReason is why a prompt's turn ended.
type StopReason string

func (r *StopReason) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, r, "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled")
}
