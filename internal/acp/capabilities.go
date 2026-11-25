package acp

// An empty capability is ACP's way to say a side supports something: {} offers
// it, and leaving it out, or null, does not.

// Capability is a capability ACP offers by an object of no members of its own.
type Capability struct {
	Meta Meta `json:"_meta,omitzero"`
}

// ClientCapabilities are what a client supports.
type ClientCapabilities struct {
	FS          *FileSystemCapabilities    `json:"fs,omitzero"`
	Terminal    *bool                      `json:"terminal,omitzero"`
	Session     *ClientSessionCapabilities `json:"session,omitzero"`
	Subagents   *Capability                `json:"subagents,omitzero"`
	Plan        *Capability                `json:"plan,omitzero"`
	Auth        *AuthCapabilities          `json:"auth,omitzero"`
	Elicitation *ElicitationCapabilities   `json:"elicitation,omitzero"`
	Meta        Meta                       `json:"_meta,omitzero"`
}

// FileSystemCapabilities are the client's files the agent may ask for.
type FileSystemCapabilities struct {
	ReadTextFile  *bool `json:"readTextFile,omitzero"`
	WriteTextFile *bool `json:"writeTextFile,omitzero"`
	Meta          Meta  `json:"_meta,omitzero"`
}

// ClientSessionCapabilities are the session extensions a client supports.
type ClientSessionCapabilities struct {
	Compaction    *Capability                       `json:"compaction,omitzero"`
	ConfigOptions *SessionConfigOptionsCapabilities `json:"configOptions,omitzero"`
	Notices       *Capability                       `json:"notices,omitzero"`
	Meta          Meta                              `json:"_meta,omitzero"`
}

// SessionConfigOptionsCapabilities are the kinds of config option a client
// supports beyond selects.
type SessionConfigOptionsCapabilities struct {
	Boolean *Capability `json:"boolean,omitzero"`
	Meta    Meta        `json:"_meta,omitzero"`
}

// AuthCapabilities are the ways of authenticating a client supports.
type AuthCapabilities struct {
	Terminal *bool `json:"terminal,omitzero"`
	Meta     Meta  `json:"_meta,omitzero"`
}

// ElicitationCapabilities are the modes of elicitation a client supports.
type ElicitationCapabilities struct {
	Form *Capability `json:"form,omitzero"`
	URL  *Capability `json:"url,omitzero"`
	Meta Meta        `json:"_meta,omitzero"`
}

// AgentCapabilities are what an agent supports.
type AgentCapabilities struct {
	LoadSession         *bool                  `json:"loadSession,omitzero"`
	PromptCapabilities  *PromptCapabilities    `json:"promptCapabilities,omitzero"`
	MCPCapabilities     *MCPCapabilities       `json:"mcpCapabilities,omitzero"`
	SessionCapabilities *SessionCapabilities   `json:"sessionCapabilities,omitzero"`
	Auth                *AgentAuthCapabilities `json:"auth,omitzero"`
	Meta                Meta                   `json:"_meta,omitzero"`
}

// PromptCapabilities are the content a prompt may hold beyond text.
type PromptCapabilities struct {
	Image           *bool `json:"image,omitzero"`
	Audio           *bool `json:"audio,omitzero"`
	EmbeddedContext *bool `json:"embeddedContext,omitzero"`
	Meta            Meta  `json:"_meta,omitzero"`
}

// MCPCapabilities are the MCP transports an agent connects over beyond stdio.
type MCPCapabilities struct {
	HTTP *bool `json:"http,omitzero"`
	SSE  *bool `json:"sse,omitzero"`
	Meta Meta  `json:"_meta,omitzero"`
}

// SessionCapabilities are the session methods an agent supports.
type SessionCapabilities struct {
	List                  *Capability                      `json:"list,omitzero"`
	Delete                *Capability                      `json:"delete,omitzero"`
	AdditionalDirectories *Capability                      `json:"additionalDirectories,omitzero"`
	SystemPrompt          *SessionSystemPromptCapabilities `json:"systemPrompt,omitzero"`
	Resume                *Capability                      `json:"resume,omitzero"`
	Close                 *Capability                      `json:"close,omitzero"`
	Meta                  Meta                             `json:"_meta,omitzero"`
}

// SessionSystemPromptCapabilities are the ways an agent takes a client's
// system prompt beyond appending it.
type SessionSystemPromptCapabilities struct {
	Mode *SessionSystemPromptModeCapabilities `json:"mode,omitzero"`
	Meta Meta                                 `json:"_meta,omitzero"`
}

// SessionSystemPromptModeCapabilities say whether an agent lets a client's
// system prompt replace its own.
type SessionSystemPromptModeCapabilities struct {
	Override *Capability `json:"override,omitzero"`
	Meta     Meta        `json:"_meta,omitzero"`
}

// AgentAuthCapabilities are the authentication methods an agent supports
// beyond authenticate.
type AgentAuthCapabilities struct {
	Logout *Capability `json:"logout,omitzero"`
	Meta   Meta        `json:"_meta,omitzero"`
}

// Implementation names a client's or an agent's program.
type Implementation struct {
	Name    string  `json:"name"`
	Title   *string `json:"title,omitzero"`
	Version string  `json:"version"`
	Meta    Meta    `json:"_meta,omitzero"`
}

// AuthMethod is a way an agent authenticates: in a terminal, or by the agent
// itself.
type AuthMethod struct {
	Terminal *AuthMethodTerminal
	Agent    *AuthMethodAgent
}

// AuthMethodTerminal authenticates by running the agent in a terminal.
type AuthMethodTerminal struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description *string           `json:"description,omitzero"`
	Args        []string          `json:"args,omitzero"`
	Env         map[string]string `json:"env,omitzero"`
	Meta        Meta              `json:"_meta,omitzero"`
}

// AuthMethodAgent authenticates by the agent itself.
type AuthMethodAgent struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	Meta        Meta    `json:"_meta,omitzero"`
}

// UnmarshalJSON reads a terminal method by its type, and the agent's own
// method, which ACP writes with none.
func (m *AuthMethod) UnmarshalJSON(b []byte) error {
	ok, err := has(b, "type")
	if err != nil {
		return err
	}
	*m = AuthMethod{}
	if !ok {
		return decodeInto(b, &m.Agent)
	}
	return decodeTagged(b, "type", variants{"terminal": &m.Terminal})
}

func (m AuthMethod) MarshalJSON() ([]byte, error) {
	if m.Agent != nil && m.Terminal == nil {
		return marshal(m.Agent)
	}
	return encodeTagged("type", variants{"terminal": m.Terminal})
}

// MCPServer is an MCP server the agent connects to: over HTTP or SSE, or a
// program it starts, which ACP writes with no type.
type MCPServer struct {
	HTTP  *MCPServerHTTP
	SSE   *MCPServerHTTP
	Stdio *MCPServerStdio
}

// MCPServerHTTP is an MCP server reached at a URL.
type MCPServerHTTP struct {
	Name    string       `json:"name"`
	URL     string       `json:"url"`
	Headers []HTTPHeader `json:"headers"`
	Meta    Meta         `json:"_meta,omitzero"`
}

// HTTPHeader is one header of a request to an MCP server.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Meta  Meta   `json:"_meta,omitzero"`
}

// MCPServerStdio is an MCP server the agent starts.
type MCPServerStdio struct {
	Name    string        `json:"name"`
	Command string        `json:"command" acp:"path"`
	Args    []string      `json:"args"`
	Env     []EnvVariable `json:"env"`
	Meta    Meta          `json:"_meta,omitzero"`
}

// EnvVariable is one variable of a program's environment.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Meta  Meta   `json:"_meta,omitzero"`
}

func (s *MCPServer) UnmarshalJSON(b []byte) error {
	ok, err := has(b, "type")
	if err != nil {
		return err
	}
	*s = MCPServer{}
	if !ok {
		return decodeInto(b, &s.Stdio)
	}
	return decodeTagged(b, "type", variants{"http": &s.HTTP, "sse": &s.SSE})
}

func (s MCPServer) MarshalJSON() ([]byte, error) {
	if s.Stdio != nil && s.HTTP == nil && s.SSE == nil {
		return marshal(s.Stdio)
	}
	return encodeTagged("type", variants{"http": s.HTTP, "sse": s.SSE})
}

// SystemPrompt is a client's instructions for a chat.
type SystemPrompt struct {
	Mode    string         `json:"mode"`
	Content []ContentBlock `json:"content"`
	Meta    Meta           `json:"_meta,omitzero"`
}

// SessionModeState is a chat's mode and the modes it may take.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
	Meta           Meta          `json:"_meta,omitzero"`
}

// SessionMode is one mode a chat may take.
type SessionMode struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	Meta        Meta    `json:"_meta,omitzero"`
}

// SessionConfigOption is one of a chat's options: a select or a boolean.
type SessionConfigOption struct {
	ID          string
	Name        string
	Description *string
	// Category is the option's kind, such as its mode or model; ACP leaves the
	// set of them open.
	Category *string
	Select   *SessionConfigSelect
	Boolean  *SessionConfigBoolean
	Meta     Meta
}

type configOptionSelect struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	Category    *string `json:"category,omitzero"`
	Type        string  `json:"type"`
	SessionConfigSelect
	Meta Meta `json:"_meta,omitzero"`
}

type configOptionBoolean struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	Category    *string `json:"category,omitzero"`
	Type        string  `json:"type"`
	SessionConfigBoolean
	Meta Meta `json:"_meta,omitzero"`
}

// SessionConfigSelect is a select option's value and its choices.
type SessionConfigSelect struct {
	CurrentValue string                     `json:"currentValue"`
	Options      SessionConfigSelectOptions `json:"options"`
}

// SessionConfigBoolean is a boolean option's value.
type SessionConfigBoolean struct {
	CurrentValue bool `json:"currentValue"`
}

func (o *SessionConfigOption) UnmarshalJSON(b []byte) error {
	tag, _, err := tagOf(b, "type")
	if err != nil {
		return err
	}
	switch tag {
	case "select":
		var v configOptionSelect
		if err := decode(b, &v); err != nil {
			return err
		}
		*o = SessionConfigOption{ID: v.ID, Name: v.Name, Description: v.Description, Category: v.Category, Select: &v.SessionConfigSelect, Meta: v.Meta}
	case "boolean":
		var v configOptionBoolean
		if err := decode(b, &v); err != nil {
			return err
		}
		*o = SessionConfigOption{ID: v.ID, Name: v.Name, Description: v.Description, Category: v.Category, Boolean: &v.SessionConfigBoolean, Meta: v.Meta}
	default:
		return unknownVariant("type", tag)
	}
	return nil
}

func (o SessionConfigOption) MarshalJSON() ([]byte, error) {
	switch {
	case o.Select != nil && o.Boolean == nil:
		return marshal(configOptionSelect{ID: o.ID, Name: o.Name, Description: o.Description, Category: o.Category, Type: "select", SessionConfigSelect: *o.Select, Meta: o.Meta})
	case o.Boolean != nil && o.Select == nil:
		return marshal(configOptionBoolean{ID: o.ID, Name: o.Name, Description: o.Description, Category: o.Category, Type: "boolean", SessionConfigBoolean: *o.Boolean, Meta: o.Meta})
	}
	return nil, errNoVariant
}

// SessionConfigSelectOptions are a select's choices, in groups or not.
type SessionConfigSelectOptions struct {
	Ungrouped []SessionConfigSelectOption
	Grouped   []SessionConfigSelectGroup
}

// SessionConfigSelectOption is one choice of a select.
type SessionConfigSelectOption struct {
	Value       string  `json:"value"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	Meta        Meta    `json:"_meta,omitzero"`
}

// SessionConfigSelectGroup is a group of a select's choices.
type SessionConfigSelectGroup struct {
	Group   string                      `json:"group"`
	Name    string                      `json:"name"`
	Options []SessionConfigSelectOption `json:"options"`
	Meta    Meta                        `json:"_meta,omitzero"`
}

// UnmarshalJSON reads groups when the first choice is one: ACP writes the two
// lists alike, and only a group has a group ID.
func (o *SessionConfigSelectOptions) UnmarshalJSON(b []byte) error {
	var items []map[string]any
	if err := decodeNumbers(b, &items); err != nil {
		return err
	}
	*o = SessionConfigSelectOptions{}
	if len(items) > 0 {
		if _, grouped := items[0]["group"]; grouped {
			return decode(b, &o.Grouped)
		}
	}
	if err := decode(b, &o.Ungrouped); err != nil {
		return err
	}
	if o.Ungrouped == nil {
		o.Ungrouped = []SessionConfigSelectOption{}
	}
	return nil
}

func (o SessionConfigSelectOptions) MarshalJSON() ([]byte, error) {
	if o.Grouped != nil {
		return marshal(o.Grouped)
	}
	if o.Ungrouped == nil {
		return []byte("[]"), nil
	}
	return marshal(o.Ungrouped)
}
