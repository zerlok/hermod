package acp

import "fmt"

// An empty capability is ACP's way to say a side supports something: {} offers
// it, and leaving it out, or null, does not.

// Capability is a capability ACP offers by an object of no members of its own.
type Capability struct {
	Extensible
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
	Extensible
}

func (c *ClientCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.FS)
	mapOpt(m, &c.Session)
	mapOpt(m, &c.Subagents)
	mapOpt(m, &c.Plan)
	mapOpt(m, &c.Auth)
	mapOpt(m, &c.Elicitation)
	m.meta(&c.Meta)
}

// FileSystemCapabilities are the client's files the agent may ask for.
type FileSystemCapabilities struct {
	ReadTextFile  *bool `json:"readTextFile,omitzero"`
	WriteTextFile *bool `json:"writeTextFile,omitzero"`
	Extensible
}

// ClientSessionCapabilities are the session extensions a client supports.
type ClientSessionCapabilities struct {
	Compaction    *Capability                       `json:"compaction,omitzero"`
	ConfigOptions *SessionConfigOptionsCapabilities `json:"configOptions,omitzero"`
	Notices       *Capability                       `json:"notices,omitzero"`
	Extensible
}

func (c *ClientSessionCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.Compaction)
	mapOpt(m, &c.ConfigOptions)
	mapOpt(m, &c.Notices)
	m.meta(&c.Meta)
}

// SessionConfigOptionsCapabilities are the kinds of config option a client
// supports beyond selects.
type SessionConfigOptionsCapabilities struct {
	Boolean *Capability `json:"boolean,omitzero"`
	Extensible
}

func (c *SessionConfigOptionsCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.Boolean)
	m.meta(&c.Meta)
}

// AuthCapabilities are the ways of authenticating a client supports.
type AuthCapabilities struct {
	Terminal *bool `json:"terminal,omitzero"`
	Extensible
}

// ElicitationCapabilities are the modes of elicitation a client supports.
type ElicitationCapabilities struct {
	Form *Capability `json:"form,omitzero"`
	URL  *Capability `json:"url,omitzero"`
	Extensible
}

func (c *ElicitationCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.Form)
	mapOpt(m, &c.URL)
	m.meta(&c.Meta)
}

// AgentCapabilities are what an agent supports.
type AgentCapabilities struct {
	LoadSession         *bool                  `json:"loadSession,omitzero"`
	PromptCapabilities  *PromptCapabilities    `json:"promptCapabilities,omitzero"`
	MCPCapabilities     *MCPCapabilities       `json:"mcpCapabilities,omitzero"`
	SessionCapabilities *SessionCapabilities   `json:"sessionCapabilities,omitzero"`
	Auth                *AgentAuthCapabilities `json:"auth,omitzero"`
	Extensible
}

func (c *AgentCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.PromptCapabilities)
	mapOpt(m, &c.MCPCapabilities)
	mapOpt(m, &c.SessionCapabilities)
	mapOpt(m, &c.Auth)
	m.meta(&c.Meta)
}

// PromptCapabilities are the content a prompt may hold beyond text.
type PromptCapabilities struct {
	Image           *bool `json:"image,omitzero"`
	Audio           *bool `json:"audio,omitzero"`
	EmbeddedContext *bool `json:"embeddedContext,omitzero"`
	Extensible
}

// MCPCapabilities are the MCP transports an agent connects over beyond stdio.
type MCPCapabilities struct {
	HTTP *bool `json:"http,omitzero"`
	SSE  *bool `json:"sse,omitzero"`
	Extensible
}

// SessionCapabilities are the session methods an agent supports.
type SessionCapabilities struct {
	List                  *Capability                      `json:"list,omitzero"`
	Delete                *Capability                      `json:"delete,omitzero"`
	AdditionalDirectories *Capability                      `json:"additionalDirectories,omitzero"`
	SystemPrompt          *SessionSystemPromptCapabilities `json:"systemPrompt,omitzero"`
	Resume                *Capability                      `json:"resume,omitzero"`
	Close                 *Capability                      `json:"close,omitzero"`
	Extensible
}

func (c *SessionCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.List)
	mapOpt(m, &c.Delete)
	mapOpt(m, &c.AdditionalDirectories)
	mapOpt(m, &c.SystemPrompt)
	mapOpt(m, &c.Resume)
	mapOpt(m, &c.Close)
	m.meta(&c.Meta)
}

// SessionSystemPromptCapabilities are the ways an agent takes a client's
// system prompt beyond appending it.
type SessionSystemPromptCapabilities struct {
	Mode *SessionSystemPromptModeCapabilities `json:"mode,omitzero"`
	Extensible
}

func (c *SessionSystemPromptCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.Mode)
	m.meta(&c.Meta)
}

// SessionSystemPromptModeCapabilities say whether an agent lets a client's
// system prompt replace its own.
type SessionSystemPromptModeCapabilities struct {
	Override *Capability `json:"override,omitzero"`
	Extensible
}

func (c *SessionSystemPromptModeCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.Override)
	m.meta(&c.Meta)
}

// AgentAuthCapabilities are the authentication methods an agent supports
// beyond authenticate.
type AgentAuthCapabilities struct {
	Logout *Capability `json:"logout,omitzero"`
	Extensible
}

func (c *AgentAuthCapabilities) mapPaths(m mapper) {
	mapOpt(m, &c.Logout)
	m.meta(&c.Meta)
}

// Implementation names a client's or an agent's program.
type Implementation struct {
	Name    string  `json:"name"`
	Title   *string `json:"title,omitzero"`
	Version string  `json:"version"`
	Extensible
}

func (i Implementation) Validate() error {
	return check(need(i.Name != "", "name"), need(i.Version != "", "version"))
}

// AuthMethod is a way an agent authenticates: in a terminal, or by the agent
// itself, which ACP writes with no type.
type AuthMethod struct {
	Terminal *AuthMethodTerminal
	Agent    *AuthMethodAgent
}

func (u *AuthMethod) arms() []arm {
	return []arm{
		armOf("terminal", &u.Terminal),
		armOf("", &u.Agent),
	}
}

func (u *AuthMethod) UnmarshalJSON(b []byte) error {
	*u = AuthMethod{}
	return decodeUnion(b, "type", u.arms())
}

func (u AuthMethod) MarshalJSON() ([]byte, error) { return encodeUnion("type", u.arms()) }

func (u AuthMethod) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *AuthMethod) mapPaths(m mapper) {
	mapOpt(m, &u.Terminal)
	mapOpt(m, &u.Agent)
}

// AuthMethodTerminal authenticates by running the agent in a terminal.
type AuthMethodTerminal struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description *string           `json:"description,omitzero"`
	Args        []string          `json:"args,omitzero"`
	Env         map[string]string `json:"env,omitzero"`
	Extensible
}

func (m AuthMethodTerminal) Validate() error {
	return check(need(m.ID != "", "id"), need(m.Name != "", "name"))
}

// AuthMethodAgent authenticates by the agent itself.
type AuthMethodAgent struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	Extensible
}

func (m AuthMethodAgent) Validate() error {
	return check(need(m.ID != "", "id"), need(m.Name != "", "name"))
}

// MCPServer is an MCP server the agent connects to: over HTTP or SSE, or a
// program it starts, which ACP writes with no type.
type MCPServer struct {
	HTTP  *MCPServerHTTP
	SSE   *MCPServerHTTP
	Stdio *MCPServerStdio
}

func (u *MCPServer) arms() []arm {
	return []arm{
		armOf("http", &u.HTTP),
		armOf("sse", &u.SSE),
		armOf("", &u.Stdio),
	}
}

func (u *MCPServer) UnmarshalJSON(b []byte) error {
	*u = MCPServer{}
	return decodeUnion(b, "type", u.arms())
}

func (u MCPServer) MarshalJSON() ([]byte, error) { return encodeUnion("type", u.arms()) }

func (u MCPServer) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *MCPServer) mapPaths(m mapper) {
	mapOpt(m, &u.HTTP)
	mapOpt(m, &u.SSE)
	mapOpt(m, &u.Stdio)
}

// MCPServerHTTP is an MCP server reached at a URL.
type MCPServerHTTP struct {
	Name    string       `json:"name"`
	URL     string       `json:"url"`
	Headers []HTTPHeader `json:"headers"`
	Extensible
}

func (s *MCPServerHTTP) mapPaths(m mapper) {
	mapEach(m, &s.Headers)
	m.meta(&s.Meta)
}

func (s MCPServerHTTP) Validate() error {
	return check(need(s.Name != "", "name"), need(s.URL != "", "url"), need(s.Headers != nil, "headers"), each("headers", s.Headers))
}

// HTTPHeader is one header of a request to an MCP server.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Extensible
}

func (h HTTPHeader) Validate() error { return need(h.Name != "", "name") }

// MCPServerStdio is an MCP server the agent starts.
type MCPServerStdio struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args"`
	Env     []EnvVariable `json:"env"`
	Extensible
}

// mapPaths maps the program the agent starts, which lies on the agent's
// workstation; its arguments are left as written, as any text is.
func (s *MCPServerStdio) mapPaths(m mapper) {
	m.path(&s.Command)
	mapEach(m, &s.Env)
	m.meta(&s.Meta)
}

func (s MCPServerStdio) Validate() error {
	return check(need(s.Name != "", "name"), need(s.Command != "", "command"), need(s.Args != nil, "args"),
		need(s.Env != nil, "env"), each("env", s.Env))
}

// EnvVariable is one variable of a program's environment.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Extensible
}

func (v EnvVariable) Validate() error { return need(v.Name != "", "name") }

// SystemPrompt is a client's instructions for a chat.
type SystemPrompt struct {
	Mode    string         `json:"mode"`
	Content []ContentBlock `json:"content"`
	Extensible
}

func (p *SystemPrompt) mapPaths(m mapper) {
	mapEach(m, &p.Content)
	m.meta(&p.Meta)
}

func (p SystemPrompt) Validate() error {
	return check(need(p.Mode != "", "mode"), need(p.Content != nil, "content"))
}

// SessionModeState is a chat's mode and the modes it may take.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
	Extensible
}

func (s *SessionModeState) mapPaths(m mapper) {
	mapEach(m, &s.AvailableModes)
	m.meta(&s.Meta)
}

func (s SessionModeState) Validate() error {
	return check(need(s.CurrentModeID != "", "currentModeId"), need(s.AvailableModes != nil, "availableModes"),
		each("availableModes", s.AvailableModes))
}

// SessionMode is one mode a chat may take.
type SessionMode struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	Extensible
}

func (m SessionMode) Validate() error {
	return check(need(m.ID != "", "id"), need(m.Name != "", "name"))
}

// SessionConfigOption is one of a chat's options: a select, whose value is
// one of its options' values, or a boolean.
type SessionConfigOption struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	// Category is the option's kind, such as its mode or model; ACP leaves the
	// set of them open.
	Category     *string                     `json:"category,omitzero"`
	Type         ConfigOptionType            `json:"type"`
	CurrentValue Value                       `json:"currentValue"`
	Options      *SessionConfigSelectOptions `json:"options,omitzero"`
	Extensible
}

func (o SessionConfigOption) Validate() error {
	return check(need(o.ID != "", "id"), need(o.Name != "", "name"), o.Type.check("currentValue", o.CurrentValue),
		need((o.Type == ConfigSelect) == (o.Options != nil), "a select's options, and only a select's,"), opt("options", o.Options))
}

func (o *SessionConfigOption) mapPaths(m mapper) {
	mapOpt(m, &o.Options)
	m.meta(&o.Meta)
}

// ConfigOptionType is the type of a config option, which says what its values
// are.
type ConfigOptionType string

const (
	ConfigSelect  ConfigOptionType = "select"  // the ID of one of its options' values
	ConfigBoolean ConfigOptionType = "boolean" // a boolean
)

func (t *ConfigOptionType) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, t, ConfigSelect, ConfigBoolean)
}

// check rejects a value of member that is not of the type's values.
func (t ConfigOptionType) check(member string, v Value) error {
	var ok bool
	switch t {
	case ConfigSelect:
		_, ok = v.value.(string)
	case ConfigBoolean:
		_, ok = v.value.(bool)
	default:
		return fmt.Errorf("type %q is not one Hermod reads", t)
	}
	return need(ok, fmt.Sprintf("%s, a %s's value,", member, t))
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
	Extensible
}

func (o SessionConfigSelectOption) Validate() error {
	return check(need(o.Value != "", "value"), need(o.Name != "", "name"))
}

// SessionConfigSelectGroup is a group of a select's choices.
type SessionConfigSelectGroup struct {
	Group   string                      `json:"group"`
	Name    string                      `json:"name"`
	Options []SessionConfigSelectOption `json:"options"`
	Extensible
}

func (g *SessionConfigSelectGroup) mapPaths(m mapper) {
	mapEach(m, &g.Options)
	m.meta(&g.Meta)
}

func (g SessionConfigSelectGroup) Validate() error {
	return check(need(g.Group != "", "group"), need(g.Name != "", "name"), need(g.Options != nil, "options"), each("options", g.Options))
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

func (o SessionConfigSelectOptions) Validate() error {
	return check(need(o.Grouped != nil || o.Ungrouped != nil, "options"), each("options", o.Grouped), each("options", o.Ungrouped))
}

func (o *SessionConfigSelectOptions) mapPaths(m mapper) {
	mapEach(m, &o.Ungrouped)
	mapEach(m, &o.Grouped)
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
