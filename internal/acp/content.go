package acp

// ContentBlock is one block of a prompt or of an agent's output.
type ContentBlock struct {
	Text         *TextContent
	Image        *ImageContent
	Audio        *AudioContent
	ResourceLink *ResourceLink
	Resource     *EmbeddedResource
}

func (c *ContentBlock) UnmarshalJSON(b []byte) error {
	*c = ContentBlock{}
	return decodeTagged(b, "type", variants{
		"text": &c.Text, "image": &c.Image, "audio": &c.Audio, "resource_link": &c.ResourceLink, "resource": &c.Resource,
	})
}

func (c ContentBlock) MarshalJSON() ([]byte, error) {
	return encodeTagged("type", variants{
		"text": c.Text, "image": c.Image, "audio": c.Audio, "resource_link": c.ResourceLink, "resource": c.Resource,
	})
}

// Annotations say whom content is for and how it matters.
type Annotations struct {
	Audience     []Role   `json:"audience,omitzero"`
	LastModified *string  `json:"lastModified,omitzero"`
	Priority     *float64 `json:"priority,omitzero"`
	Meta         Meta     `json:"_meta,omitzero"`
}

// Role is whom content is for.
type Role string

func (r *Role) UnmarshalJSON(b []byte) error { return decodeEnum(b, r, "assistant", "user") }

// TextContent is text.
type TextContent struct {
	Annotations *Annotations `json:"annotations,omitzero"`
	Text        string       `json:"text"`
	Meta        Meta         `json:"_meta,omitzero"`
}

// ImageContent is an image, its data in base64.
type ImageContent struct {
	Annotations *Annotations `json:"annotations,omitzero"`
	Data        string       `json:"data"`
	MimeType    string       `json:"mimeType"`
	URI         *string      `json:"uri,omitzero" acp:"uri"`
	Meta        Meta         `json:"_meta,omitzero"`
}

// AudioContent is audio, its data in base64.
type AudioContent struct {
	Annotations *Annotations `json:"annotations,omitzero"`
	Data        string       `json:"data"`
	MimeType    string       `json:"mimeType"`
	Meta        Meta         `json:"_meta,omitzero"`
}

// ResourceLink names a resource, such as a file, by its URI.
type ResourceLink struct {
	Annotations *Annotations `json:"annotations,omitzero"`
	Description *string      `json:"description,omitzero"`
	MimeType    *string      `json:"mimeType,omitzero"`
	Name        string       `json:"name"`
	Size        *int64       `json:"size,omitzero"`
	Title       *string      `json:"title,omitzero"`
	URI         string       `json:"uri" acp:"uri"`
	Meta        Meta         `json:"_meta,omitzero"`
}

// EmbeddedResource is a resource with its contents.
type EmbeddedResource struct {
	Annotations *Annotations             `json:"annotations,omitzero"`
	Resource    EmbeddedResourceContents `json:"resource"`
	Meta        Meta                     `json:"_meta,omitzero"`
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
	URI      string  `json:"uri" acp:"uri"`
	Meta     Meta    `json:"_meta,omitzero"`
}

// BlobResourceContents are a resource's contents in base64.
type BlobResourceContents struct {
	Blob     string  `json:"blob"`
	MimeType *string `json:"mimeType,omitzero"`
	URI      string  `json:"uri" acp:"uri"`
	Meta     Meta    `json:"_meta,omitzero"`
}

// UnmarshalJSON tells text from a blob by the member each has: ACP names
// neither.
func (c *EmbeddedResourceContents) UnmarshalJSON(b []byte) error {
	*c = EmbeddedResourceContents{}
	blob, err := has(b, "blob")
	if err != nil {
		return err
	}
	if blob {
		return decodeInto(b, &c.Blob)
	}
	return decodeInto(b, &c.Text)
}

func (c EmbeddedResourceContents) MarshalJSON() ([]byte, error) {
	return one(variants{"text": c.Text, "blob": c.Blob})
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
	Meta       Meta               `json:"_meta,omitzero"`
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
	Meta       Meta               `json:"_meta,omitzero"`
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

func (c *ToolCallContent) UnmarshalJSON(b []byte) error {
	*c = ToolCallContent{}
	return decodeTagged(b, "type", variants{"content": &c.Content, "diff": &c.Diff, "terminal": &c.Terminal, "session": &c.Session})
}

func (c ToolCallContent) MarshalJSON() ([]byte, error) {
	return encodeTagged("type", variants{"content": c.Content, "diff": c.Diff, "terminal": c.Terminal, "session": c.Session})
}

// ToolCallContentBlock is a block of content a tool call produced.
type ToolCallContentBlock struct {
	Content ContentBlock `json:"content"`
	Meta    Meta         `json:"_meta,omitzero"`
}

// Diff is a change to a file.
type Diff struct {
	Path    string  `json:"path" acp:"path"`
	OldText *string `json:"oldText,omitzero"`
	NewText string  `json:"newText"`
	Meta    Meta    `json:"_meta,omitzero"`
}

// TerminalRef names a terminal a tool call runs in.
type TerminalRef struct {
	TerminalID string `json:"terminalId"`
	Meta       Meta   `json:"_meta,omitzero"`
}

// SessionReference names another session.
type SessionReference struct {
	SessionID SessionID `json:"sessionId"`
	Meta      Meta      `json:"_meta,omitzero"`
}

// ToolCallLocation is a file a tool call reads or changes.
type ToolCallLocation struct {
	Path string `json:"path" acp:"path"`
	Line *int64 `json:"line,omitzero"`
	Meta Meta   `json:"_meta,omitzero"`
}
