package acp

import (
	"errors"
	"fmt"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/session"
)

// The parts of a type of params or result that say nothing to a session.
type (
	passes struct{}
	noChat struct{}
	noDir  struct{}
)

func (passes) Kind() session.Kind   { return session.Pass }
func (noChat) Chat() session.ChatID { return "" }
func (noDir) Dir() session.Path     { return "" }

// SessionID is ACP's ID of a session, which session calls a chat.
type SessionID string

func (id SessionID) chat() session.ChatID { return session.ChatID(id) }

// standardMethods are ACP's methods, by who sends them.
func standardMethods() []Method {
	return []Method{
		RequestOf[InitializeParams, InitializeResult](Client),
		RequestOf[AuthenticateParams, AuthenticateResult](Client),
		RequestOf[LogoutParams, LogoutResult](Client),
		RequestOf[NewSessionParams, NewSessionResult](Client),
		RequestOf[LoadSessionParams, LoadSessionResult](Client),
		RequestOf[ListSessionsParams, ListSessionsResult](Client),
		RequestOf[DeleteSessionParams, DeleteSessionResult](Client),
		RequestOf[ResumeSessionParams, ResumeSessionResult](Client),
		RequestOf[CloseSessionParams, CloseSessionResult](Client),
		RequestOf[SetSessionModeParams, SetSessionModeResult](Client),
		RequestOf[SetSessionConfigOptionParams, SetSessionConfigOptionResult](Client),
		RequestOf[PromptParams, PromptResult](Client),
		NotificationOf[CancelParams](Client),
		NotificationOf[CancelRequestParams](Client, Agent),

		RequestOf[ReadTextFileParams, ReadTextFileResult](Agent),
		RequestOf[WriteTextFileParams, WriteTextFileResult](Agent),
		RequestOf[RequestPermissionParams, RequestPermissionResult](Agent),
		RequestOf[CreateTerminalParams, CreateTerminalResult](Agent),
		RequestOf[TerminalOutputParams, TerminalOutputResult](Agent),
		RequestOf[ReleaseTerminalParams, ReleaseTerminalResult](Agent),
		RequestOf[WaitForTerminalExitParams, WaitForTerminalExitResult](Agent),
		RequestOf[KillTerminalParams, KillTerminalResult](Agent),
		RequestOf[CreateElicitationParams, CreateElicitationResult](Agent),
		NotificationOf[SessionNotification](Agent),
		NotificationOf[CompleteElicitationParams](Agent),
	}
}

// InitializeParams starts a client's connection.
type InitializeParams struct {
	noChat
	noDir
	ProtocolVersion    int                 `json:"protocolVersion"`
	ClientCapabilities *ClientCapabilities `json:"clientCapabilities,omitzero"`
	ClientInfo         *Implementation     `json:"clientInfo,omitzero"`
	Meta               Meta                `json:"_meta,omitzero"`
}

func (InitializeParams) Method() string     { return "initialize" }
func (InitializeParams) Kind() session.Kind { return session.Connect }

// withoutClientCapabilities is an initialize without the client capabilities
// that act on the client's own workstation, and any other message as it is.
func withoutClientCapabilities(msg session.Message) session.Message {
	init, ok := msg.(Request[InitializeParams, InitializeResult])
	if !ok || init.Params.ClientCapabilities == nil {
		return msg
	}
	caps := *init.Params.ClientCapabilities
	caps.FS, caps.Terminal = nil, nil
	init.Params.ClientCapabilities = &caps
	return init
}

// InitializeResult answers an initialize. Hermod asks for no authentication of
// its own: an adapter that needs one is started on the remote, where its
// credentials are.
type InitializeResult struct {
	noChat
	ProtocolVersion   int                `json:"protocolVersion"`
	AgentCapabilities *AgentCapabilities `json:"agentCapabilities,omitzero"`
	AuthMethods       []AuthMethod       `json:"authMethods,omitzero"`
	AgentInfo         *Implementation    `json:"agentInfo,omitzero"`
	Meta              Meta               `json:"_meta,omitzero"`
}

// FromSession writes a HandshakeResult in ACP's terms.
func (InitializeResult) FromSession(r session.Result) (InitializeResult, error) {
	h, ok := r.(session.HandshakeResult)
	if !ok {
		return InitializeResult{}, fmt.Errorf("hermod: initialize is not answered with a %T", r)
	}
	var caps AgentCapabilities
	if err := decode(h.Handshake.Capabilities(), &caps); err != nil {
		return InitializeResult{}, fmt.Errorf("hermod: the agent's capabilities: %w", err)
	}
	return InitializeResult{ProtocolVersion: h.Handshake.Version(), AgentCapabilities: &caps, AuthMethods: []AuthMethod{}}, nil
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
	passes
	noChat
	noDir
	MethodID string `json:"methodId"`
	Meta     Meta   `json:"_meta,omitzero"`
}

func (AuthenticateParams) Method() string { return "authenticate" }

// AuthenticateResult answers an authenticate.
type AuthenticateResult struct {
	noChat
	Meta Meta `json:"_meta,omitzero"`
}

// LogoutParams ends the client's authentication.
type LogoutParams struct {
	passes
	noChat
	noDir
	Meta Meta `json:"_meta,omitzero"`
}

func (LogoutParams) Method() string { return "logout" }

// LogoutResult answers a logout.
type LogoutResult struct {
	noChat
	Meta Meta `json:"_meta,omitzero"`
}

// NewSessionParams opens a new chat in a directory.
type NewSessionParams struct {
	noChat
	Cwd                   string        `json:"cwd" acp:"path"`
	AdditionalDirectories []string      `json:"additionalDirectories,omitzero" acp:"path"`
	MCPServers            []MCPServer   `json:"mcpServers"`
	SystemPrompt          *SystemPrompt `json:"systemPrompt,omitzero"`
	Meta                  Meta          `json:"_meta,omitzero"`
}

func (NewSessionParams) Method() string      { return "session/new" }
func (NewSessionParams) Kind() session.Kind  { return session.Open }
func (p NewSessionParams) Dir() session.Path { return session.Path(p.Cwd) }

// NewSessionResult answers a session/new with the chat it made.
type NewSessionResult struct {
	SessionID     SessionID             `json:"sessionId"`
	Modes         *SessionModeState     `json:"modes,omitzero"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitzero"`
	Meta          Meta                  `json:"_meta,omitzero"`
}

func (r NewSessionResult) Chat() session.ChatID { return r.SessionID.chat() }

// LoadSessionParams opens a chat again, its history replayed.
type LoadSessionParams struct {
	SessionID             SessionID     `json:"sessionId"`
	Cwd                   string        `json:"cwd" acp:"path"`
	AdditionalDirectories []string      `json:"additionalDirectories,omitzero" acp:"path"`
	MCPServers            []MCPServer   `json:"mcpServers"`
	SystemPrompt          *SystemPrompt `json:"systemPrompt,omitzero"`
	Meta                  Meta          `json:"_meta,omitzero"`
}

func (LoadSessionParams) Method() string         { return "session/load" }
func (LoadSessionParams) Kind() session.Kind     { return session.Open }
func (p LoadSessionParams) Chat() session.ChatID { return p.SessionID.chat() }
func (p LoadSessionParams) Dir() session.Path    { return session.Path(p.Cwd) }

// LoadSessionResult answers a session/load.
type LoadSessionResult struct {
	noChat
	Modes         *SessionModeState     `json:"modes,omitzero"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitzero"`
	Meta          Meta                  `json:"_meta,omitzero"`
}

// ResumeSessionParams opens a chat again without replaying its history.
type ResumeSessionParams struct {
	SessionID             SessionID     `json:"sessionId"`
	Cwd                   string        `json:"cwd" acp:"path"`
	AdditionalDirectories []string      `json:"additionalDirectories,omitzero" acp:"path"`
	MCPServers            []MCPServer   `json:"mcpServers,omitzero"`
	SystemPrompt          *SystemPrompt `json:"systemPrompt,omitzero"`
	Meta                  Meta          `json:"_meta,omitzero"`
}

func (ResumeSessionParams) Method() string         { return "session/resume" }
func (ResumeSessionParams) Kind() session.Kind     { return session.Open }
func (p ResumeSessionParams) Chat() session.ChatID { return p.SessionID.chat() }
func (p ResumeSessionParams) Dir() session.Path    { return session.Path(p.Cwd) }

// ResumeSessionResult answers a session/resume.
type ResumeSessionResult struct {
	noChat
	Modes         *SessionModeState     `json:"modes,omitzero"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitzero"`
	Meta          Meta                  `json:"_meta,omitzero"`
}

// ListSessionsParams asks for the chats of a directory.
type ListSessionsParams struct {
	noChat
	Cwd    *string `json:"cwd,omitzero" acp:"path"`
	Cursor *string `json:"cursor,omitzero"`
	Meta   Meta    `json:"_meta,omitzero"`
}

func (ListSessionsParams) Method() string     { return "session/list" }
func (ListSessionsParams) Kind() session.Kind { return session.List }

func (p ListSessionsParams) Dir() session.Path {
	if p.Cwd == nil {
		return ""
	}
	return session.Path(*p.Cwd)
}

// ListSessionsResult answers a session/list.
type ListSessionsResult struct {
	noChat
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor *string       `json:"nextCursor,omitzero"`
	Meta       Meta          `json:"_meta,omitzero"`
}

// FromSession writes a ListResult in ACP's terms. A conversation's state is
// the agent program's own and has no place in it.
func (ListSessionsResult) FromSession(r session.Result) (ListSessionsResult, error) {
	l, ok := r.(session.ListResult)
	if !ok {
		return ListSessionsResult{}, fmt.Errorf("hermod: session/list is not answered with a %T", r)
	}
	sessions := make([]SessionInfo, 0, len(l.Conversations))
	for _, c := range l.Conversations {
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

// SessionInfo is one chat a session/list answers with.
type SessionInfo struct {
	SessionID             SessionID `json:"sessionId"`
	Cwd                   string    `json:"cwd" acp:"path"`
	AdditionalDirectories []string  `json:"additionalDirectories,omitzero" acp:"path"`
	Title                 *string   `json:"title,omitzero"`
	UpdatedAt             *string   `json:"updatedAt,omitzero"`
	Meta                  Meta      `json:"_meta,omitzero"`
}

// DeleteSessionParams deletes a chat.
type DeleteSessionParams struct {
	passes
	noDir
	SessionID SessionID `json:"sessionId"`
	Meta      Meta      `json:"_meta,omitzero"`
}

func (DeleteSessionParams) Method() string         { return "session/delete" }
func (p DeleteSessionParams) Chat() session.ChatID { return p.SessionID.chat() }

// DeleteSessionResult answers a session/delete.
type DeleteSessionResult struct {
	noChat
	Meta Meta `json:"_meta,omitzero"`
}

// CloseSessionParams closes a chat: ACP's agent then ends it.
type CloseSessionParams struct {
	passes
	noDir
	SessionID SessionID `json:"sessionId"`
	Meta      Meta      `json:"_meta,omitzero"`
}

func (CloseSessionParams) Method() string         { return "session/close" }
func (p CloseSessionParams) Chat() session.ChatID { return p.SessionID.chat() }

// CloseSessionResult answers a session/close.
type CloseSessionResult struct {
	noChat
	Meta Meta `json:"_meta,omitzero"`
}

// SetSessionModeParams sets a chat's mode.
type SetSessionModeParams struct {
	passes
	noDir
	SessionID SessionID `json:"sessionId"`
	ModeID    string    `json:"modeId"`
	Meta      Meta      `json:"_meta,omitzero"`
}

func (SetSessionModeParams) Method() string         { return "session/set_mode" }
func (p SetSessionModeParams) Chat() session.ChatID { return p.SessionID.chat() }

// SetSessionModeResult answers a session/set_mode.
type SetSessionModeResult struct {
	noChat
	Meta Meta `json:"_meta,omitzero"`
}

// SetSessionConfigOptionParams sets one of a chat's options: a boolean, or the
// ID of one of the option's values.
type SetSessionConfigOptionParams struct {
	passes
	noDir
	SessionID SessionID
	ConfigID  string
	Boolean   *bool
	ValueID   *string
	Meta      Meta
}

func (SetSessionConfigOptionParams) Method() string         { return "session/set_config_option" }
func (p SetSessionConfigOptionParams) Chat() session.ChatID { return p.SessionID.chat() }

type setBoolean struct {
	SessionID SessionID `json:"sessionId"`
	ConfigID  string    `json:"configId"`
	Type      string    `json:"type"`
	Value     bool      `json:"value"`
	Meta      Meta      `json:"_meta,omitzero"`
}

type setValueID struct {
	SessionID SessionID `json:"sessionId"`
	ConfigID  string    `json:"configId"`
	Value     string    `json:"value"`
	Meta      Meta      `json:"_meta,omitzero"`
}

// UnmarshalJSON reads a boolean when the params say so by their type, and a
// value ID otherwise: ACP reads any other type as a value ID too.
func (p *SetSessionConfigOptionParams) UnmarshalJSON(b []byte) error {
	tag, _, err := tagOf(b, "type")
	if err != nil {
		return err
	}
	if tag == "boolean" {
		var v setBoolean
		if err := decode(b, &v); err != nil {
			return err
		}
		*p = SetSessionConfigOptionParams{SessionID: v.SessionID, ConfigID: v.ConfigID, Boolean: &v.Value, Meta: v.Meta}
		return nil
	}
	var v setValueID
	if err := decode(b, &v); err != nil {
		return err
	}
	*p = SetSessionConfigOptionParams{SessionID: v.SessionID, ConfigID: v.ConfigID, ValueID: &v.Value, Meta: v.Meta}
	return nil
}

func (p SetSessionConfigOptionParams) MarshalJSON() ([]byte, error) {
	switch {
	case p.Boolean != nil && p.ValueID == nil:
		return marshal(setBoolean{SessionID: p.SessionID, ConfigID: p.ConfigID, Type: "boolean", Value: *p.Boolean, Meta: p.Meta})
	case p.ValueID != nil && p.Boolean == nil:
		return marshal(setValueID{SessionID: p.SessionID, ConfigID: p.ConfigID, Value: *p.ValueID, Meta: p.Meta})
	}
	return nil, errNoVariant
}

// SetSessionConfigOptionResult answers a session/set_config_option with every
// option of the chat.
type SetSessionConfigOptionResult struct {
	noChat
	ConfigOptions []SessionConfigOption `json:"configOptions"`
	Meta          Meta                  `json:"_meta,omitzero"`
}

// PromptParams is the client's prompt in a chat.
type PromptParams struct {
	passes
	noDir
	SessionID SessionID      `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
	Meta      Meta           `json:"_meta,omitzero"`
}

func (PromptParams) Method() string         { return "session/prompt" }
func (p PromptParams) Chat() session.ChatID { return p.SessionID.chat() }

// PromptResult answers a prompt once its turn ends.
type PromptResult struct {
	noChat
	StopReason StopReason `json:"stopReason"`
	Meta       Meta       `json:"_meta,omitzero"`
}

// StopReason is why a prompt's turn ended.
type StopReason string

func (r *StopReason) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, r, "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled")
}

// CancelParams cancels the turn under way in a chat.
type CancelParams struct {
	passes
	noDir
	SessionID SessionID `json:"sessionId"`
	Meta      Meta      `json:"_meta,omitzero"`
}

func (CancelParams) Method() string         { return "session/cancel" }
func (p CancelParams) Chat() session.ChatID { return p.SessionID.chat() }

// CancelRequestParams cancels one of its sender's requests, which either side
// may send.
type CancelRequestParams struct {
	noChat
	noDir
	RequestID requestID `json:"requestId"`
	Meta      Meta      `json:"_meta,omitzero"`
}

func (CancelRequestParams) Method() string     { return "$/cancel_request" }
func (CancelRequestParams) Kind() session.Kind { return session.Cancel }

func (p CancelRequestParams) cancels() session.MessageID { return session.MessageID(p.RequestID) }

func (p CancelRequestParams) cancelling(id session.MessageID) CancelRequestParams {
	p.RequestID = requestID(id)
	return p
}

// UnmarshalJSON rejects a cancel of no request: JSON-RPC's null ID names none.
func (p *CancelRequestParams) UnmarshalJSON(b []byte) error {
	type plain CancelRequestParams
	if err := decode(b, (*plain)(p)); err != nil {
		return err
	}
	if session.MessageID(p.RequestID).IsZero() {
		return errors.New("requestId: null names no request")
	}
	return nil
}
