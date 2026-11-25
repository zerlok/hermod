package acp

import "encoding/json"

// ContentBlock is one block of a prompt or of an agent's output.
type ContentBlock struct {
	Text         *TextContent
	Image        *ImageContent
	Audio        *AudioContent
	ResourceLink *ResourceLink
	Resource     *EmbeddedResource
}

func (u *ContentBlock) arms() []arm {
	return []arm{
		armOf("text", &u.Text),
		armOf("image", &u.Image),
		armOf("audio", &u.Audio),
		armOf("resource_link", &u.ResourceLink),
		armOf("resource", &u.Resource),
	}
}

func (u *ContentBlock) UnmarshalJSON(b []byte) error {
	*u = ContentBlock{}
	return decodeUnion(b, "type", u.arms())
}

func (u ContentBlock) MarshalJSON() ([]byte, error) { return encodeUnion("type", u.arms()) }

func (u ContentBlock) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *ContentBlock) mapPaths(m mapper) {
	mapOpt(m, &u.Text)
	mapOpt(m, &u.Image)
	mapOpt(m, &u.Audio)
	mapOpt(m, &u.ResourceLink)
	mapOpt(m, &u.Resource)
}

// Annotations say whom content is for and how it matters.
type Annotations struct {
	Audience     []Role   `json:"audience,omitzero"`
	LastModified *string  `json:"lastModified,omitzero"`
	Priority     *float64 `json:"priority,omitzero"`
	Extensible
}

// Role is whom content is for.
type Role string

func (r *Role) UnmarshalJSON(b []byte) error { return decodeEnum(b, r, "assistant", "user") }

// TextContent is text.
type TextContent struct {
	Annotations *Annotations `json:"annotations,omitzero"`
	Text        string       `json:"text"`
	Extensible
}

func (c *TextContent) mapPaths(m mapper) {
	mapOpt(m, &c.Annotations)
	m.meta(&c.Meta)
}

// ImageContent is an image, its data in base64.
type ImageContent struct {
	Annotations *Annotations `json:"annotations,omitzero"`
	Data        string       `json:"data"`
	MimeType    string       `json:"mimeType"`
	URI         *string      `json:"uri,omitzero"`
	Extensible
}

func (c ImageContent) Validate() error { return need(c.MimeType != "", "mimeType") }

func (c *ImageContent) mapPaths(m mapper) {
	mapOpt(m, &c.Annotations)
	m.optURI(&c.URI)
	m.meta(&c.Meta)
}

// AudioContent is audio, its data in base64.
type AudioContent struct {
	Annotations *Annotations `json:"annotations,omitzero"`
	Data        string       `json:"data"`
	MimeType    string       `json:"mimeType"`
	Extensible
}

func (c AudioContent) Validate() error { return need(c.MimeType != "", "mimeType") }

func (c *AudioContent) mapPaths(m mapper) {
	mapOpt(m, &c.Annotations)
	m.meta(&c.Meta)
}

// ResourceLink names a resource, such as a file, by its URI.
type ResourceLink struct {
	Annotations *Annotations `json:"annotations,omitzero"`
	Description *string      `json:"description,omitzero"`
	MimeType    *string      `json:"mimeType,omitzero"`
	Name        string       `json:"name"`
	Size        *int64       `json:"size,omitzero"`
	Title       *string      `json:"title,omitzero"`
	URI         string       `json:"uri"`
	Extensible
}

func (l ResourceLink) Validate() error {
	return check(need(l.Name != "", "name"), need(l.URI != "", "uri"))
}

func (l *ResourceLink) mapPaths(m mapper) {
	mapOpt(m, &l.Annotations)
	m.uri(&l.URI)
	m.meta(&l.Meta)
}

// EmbeddedResource is a resource with its contents.
type EmbeddedResource struct {
	Annotations *Annotations             `json:"annotations,omitzero"`
	Resource    EmbeddedResourceContents `json:"resource"`
	Extensible
}

func (r EmbeddedResource) Validate() error { return in("resource", r.Resource.Validate()) }

func (r *EmbeddedResource) mapPaths(m mapper) {
	mapOpt(m, &r.Annotations)
	r.Resource.mapPaths(m)
	m.meta(&r.Meta)
}

// EmbeddedResourceContents are a resource's contents, as text or as a blob.
type EmbeddedResourceContents struct {
	Text *TextResourceContents
	Blob *BlobResourceContents
}

// TextResourceContents are a resource's contents as text.
type TextResourceContents struct {
	MimeType *string `json:"mimeType,omitzero"`
	Text     string  `json:"text"`
	URI      string  `json:"uri"`
	Extensible
}

func (c TextResourceContents) Validate() error { return need(c.URI != "", "uri") }

func (c *TextResourceContents) mapPaths(m mapper) {
	m.uri(&c.URI)
	m.meta(&c.Meta)
}

// BlobResourceContents are a resource's contents in base64.
type BlobResourceContents struct {
	Blob     string  `json:"blob"`
	MimeType *string `json:"mimeType,omitzero"`
	URI      string  `json:"uri"`
	Extensible
}

func (c BlobResourceContents) Validate() error { return need(c.URI != "", "uri") }

func (c *BlobResourceContents) mapPaths(m mapper) {
	m.uri(&c.URI)
	m.meta(&c.Meta)
}

func (c *EmbeddedResourceContents) arms() []arm {
	return []arm{armOf("", &c.Text), armOf("", &c.Blob)}
}

// UnmarshalJSON tells text from a blob by the member each has: ACP names
// neither.
func (c *EmbeddedResourceContents) UnmarshalJSON(b []byte) error {
	*c = EmbeddedResourceContents{}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(b, &members); err != nil {
		return err
	}
	arms := c.arms()
	if _, blob := members["blob"]; blob {
		return arms[1].read(b)
	}
	return arms[0].read(b)
}

func (c EmbeddedResourceContents) MarshalJSON() ([]byte, error) { return encodeUnion("", c.arms()) }

func (c EmbeddedResourceContents) Validate() error {
	_, err := heldArm(c.arms())
	return err
}

func (c *EmbeddedResourceContents) mapPaths(m mapper) {
	mapOpt(m, &c.Text)
	mapOpt(m, &c.Blob)
}

// ToolCall is a tool call the agent reports.
type ToolCall struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title"`
	Name       *string            `json:"name,omitzero"`
	Kind       *ToolKind          `json:"kind,omitzero"`
	Status     *ToolCallStatus    `json:"status,omitzero"`
	Content    []ToolCallContent  `json:"content,omitzero"`
	Locations  []ToolCallLocation `json:"locations,omitzero"`
	RawInput   Value              `json:"rawInput,omitzero"`
	RawOutput  Value              `json:"rawOutput,omitzero"`
	Extensible
}

func (c ToolCall) Validate() error {
	return check(need(c.ToolCallID != "", "toolCallId"), each("locations", c.Locations))
}

func (c *ToolCall) mapPaths(m mapper) {
	mapEach(m, &c.Content)
	mapEach(m, &c.Locations)
	m.value(&c.RawInput)
	m.value(&c.RawOutput)
	m.meta(&c.Meta)
}

// ToolCallUpdate changes the members of a tool call it gives.
type ToolCallUpdate struct {
	ToolCallID string             `json:"toolCallId"`
	Kind       *ToolKind          `json:"kind,omitzero"`
	Status     *ToolCallStatus    `json:"status,omitzero"`
	Title      *string            `json:"title,omitzero"`
	Name       *string            `json:"name,omitzero"`
	Content    []ToolCallContent  `json:"content,omitzero"`
	Locations  []ToolCallLocation `json:"locations,omitzero"`
	RawInput   Value              `json:"rawInput,omitzero"`
	RawOutput  Value              `json:"rawOutput,omitzero"`
	Extensible
}

func (c ToolCallUpdate) Validate() error {
	return check(need(c.ToolCallID != "", "toolCallId"), each("locations", c.Locations))
}

func (c *ToolCallUpdate) mapPaths(m mapper) {
	mapEach(m, &c.Content)
	mapEach(m, &c.Locations)
	m.value(&c.RawInput)
	m.value(&c.RawOutput)
	m.meta(&c.Meta)
}

// ToolKind is what a tool does.
type ToolKind string

func (k *ToolKind) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, k, "read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other")
}

// ToolCallStatus is how far a tool call is.
type ToolCallStatus string

func (s *ToolCallStatus) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, s, "pending", "in_progress", "completed", "failed")
}

// ToolCallContent is what a tool call produced: content, a diff of a file, a
// terminal, or another session.
type ToolCallContent struct {
	Content  *ToolCallContentBlock
	Diff     *Diff
	Terminal *TerminalRef
	Session  *SessionReference
}

func (u *ToolCallContent) arms() []arm {
	return []arm{
		armOf("content", &u.Content),
		armOf("diff", &u.Diff),
		armOf("terminal", &u.Terminal),
		armOf("session", &u.Session),
	}
}

func (u *ToolCallContent) UnmarshalJSON(b []byte) error {
	*u = ToolCallContent{}
	return decodeUnion(b, "type", u.arms())
}

func (u ToolCallContent) MarshalJSON() ([]byte, error) { return encodeUnion("type", u.arms()) }

func (u ToolCallContent) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *ToolCallContent) mapPaths(m mapper) {
	mapOpt(m, &u.Content)
	mapOpt(m, &u.Diff)
	mapOpt(m, &u.Terminal)
	mapOpt(m, &u.Session)
}

// ToolCallContentBlock is a block of content a tool call produced.
type ToolCallContentBlock struct {
	Content ContentBlock `json:"content"`
	Extensible
}

func (b ToolCallContentBlock) Validate() error { return in("content", b.Content.Validate()) }

func (b *ToolCallContentBlock) mapPaths(m mapper) {
	b.Content.mapPaths(m)
	m.meta(&b.Meta)
}

// Diff is a change to a file.
type Diff struct {
	Path    string  `json:"path"`
	OldText *string `json:"oldText,omitzero"`
	NewText string  `json:"newText"`
	Extensible
}

func (d Diff) Validate() error { return need(d.Path != "", "path") }

func (d *Diff) mapPaths(m mapper) {
	m.path(&d.Path)
	m.meta(&d.Meta)
}

// TerminalRef names a terminal a tool call runs in.
type TerminalRef struct {
	TerminalID string `json:"terminalId"`
	Extensible
}

func (r TerminalRef) Validate() error { return need(r.TerminalID != "", "terminalId") }

// SessionReference names another session.
type SessionReference struct {
	SessionID SessionID `json:"sessionId"`
	Extensible
}

func (r SessionReference) Validate() error { return need(r.SessionID != "", "sessionId") }

// ToolCallLocation is a file a tool call reads or changes.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int64 `json:"line,omitzero"`
	Extensible
}

func (l ToolCallLocation) Validate() error { return need(l.Path != "", "path") }

func (l *ToolCallLocation) mapPaths(m mapper) {
	m.path(&l.Path)
	m.meta(&l.Meta)
}
