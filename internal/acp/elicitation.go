package acp

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zerlok/hermod/internal/session"
)

// CreateElicitationParams asks the user for input: by a form the client
// shows, or at a URL. It is tied to a chat, and then perhaps to one of its
// tool calls, or to a request.
type CreateElicitationParams struct {
	Message string          `json:"message"`
	Mode    ElicitationMode `json:"mode"`
	// RequestedSchema is the form's, for the form mode.
	RequestedSchema *ElicitationSchema `json:"requestedSchema,omitzero"`
	// ElicitationID and URL are the URL mode's.
	ElicitationID *string    `json:"elicitationId,omitzero"`
	URL           *string    `json:"url,omitzero"`
	SessionID     *SessionID `json:"sessionId,omitzero"`
	ToolCallID    *string    `json:"toolCallId,omitzero"`
	RequestID     Value      `json:"requestId,omitzero"`
	Extensible
}

func (p CreateElicitationParams) Chat() session.ChatID {
	if p.SessionID == nil {
		return ""
	}
	return p.SessionID.chat()
}

// ElicitationMode is how the user is asked.
type ElicitationMode string

func (m *ElicitationMode) UnmarshalJSON(b []byte) error { return decodeEnum(b, m, "form", "url") }

// Validate checks that the params hold what their mode and their scope need,
// which ACP writes in one object.
func (p CreateElicitationParams) Validate() error {
	switch {
	case p.Message == "" || p.Mode == "":
		return errors.New("an elicitation has a message and a mode")
	case p.Mode == "form" && p.RequestedSchema == nil:
		return errors.New("a form elicitation has a requestedSchema")
	case p.Mode == "url" && (p.ElicitationID == nil || p.URL == nil):
		return errors.New("a URL elicitation has an elicitationId and a url")
	case (p.SessionID == nil) == (p.RequestID.IsZero() || p.RequestID.value == nil):
		return errors.New("an elicitation is tied to a sessionId or to a requestId")
	case p.ToolCallID != nil && p.SessionID == nil:
		return errors.New("an elicitation's toolCallId is in a sessionId")
	}
	return opt("requestedSchema", p.RequestedSchema)
}

func (p *CreateElicitationParams) mapPaths(m mapper) {
	mapOpt(m, &p.RequestedSchema)
	m.meta(&p.Meta)
}

// ElicitationSchema is a form: the properties it asks for.
type ElicitationSchema struct {
	Type        *string                              `json:"type,omitzero"`
	Title       *string                              `json:"title,omitzero"`
	Properties  map[string]ElicitationPropertySchema `json:"properties,omitzero"`
	Required    []string                             `json:"required,omitzero"`
	Description *string                              `json:"description,omitzero"`
	Extensible
}

// Validate accepts only an object schema, the one ACP declares.
func (s ElicitationSchema) Validate() error {
	if s.Type != nil && *s.Type != "object" {
		return fmt.Errorf("an elicitation's schema is an object, not %q", *s.Type)
	}
	return nil
}

func (s *ElicitationSchema) mapPaths(m mapper) {
	if s.Properties != nil {
		properties := make(map[string]ElicitationPropertySchema, len(s.Properties))
		for name, p := range s.Properties {
			p.mapPaths(m)
			properties[name] = p
		}
		s.Properties = properties
	}
	m.meta(&s.Meta)
}

// ElicitationPropertySchema is one property of a form.
type ElicitationPropertySchema struct {
	String  *StringPropertySchema
	Number  *NumberPropertySchema
	Integer *IntegerPropertySchema
	Boolean *BooleanPropertySchema
	Array   *MultiSelectPropertySchema
}

func (u *ElicitationPropertySchema) arms() []arm {
	return []arm{
		armOf("string", &u.String),
		armOf("number", &u.Number),
		armOf("integer", &u.Integer),
		armOf("boolean", &u.Boolean),
		armOf("array", &u.Array),
	}
}

func (u *ElicitationPropertySchema) UnmarshalJSON(b []byte) error {
	*u = ElicitationPropertySchema{}
	return decodeUnion(b, "type", u.arms())
}

func (u ElicitationPropertySchema) MarshalJSON() ([]byte, error) {
	return encodeUnion("type", u.arms())
}

func (u ElicitationPropertySchema) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *ElicitationPropertySchema) mapPaths(m mapper) {
	mapOpt(m, &u.String)
	mapOpt(m, &u.Number)
	mapOpt(m, &u.Integer)
	mapOpt(m, &u.Boolean)
	mapOpt(m, &u.Array)
}

// StringPropertySchema is a text property, or a choice of one of its values.
type StringPropertySchema struct {
	Title       *string       `json:"title,omitzero"`
	Description *string       `json:"description,omitzero"`
	MinLength   *int64        `json:"minLength,omitzero"`
	MaxLength   *int64        `json:"maxLength,omitzero"`
	Pattern     *string       `json:"pattern,omitzero"`
	Format      *StringFormat `json:"format,omitzero"`
	Default     *string       `json:"default,omitzero"`
	Enum        []string      `json:"enum,omitzero"`
	OneOf       []EnumOption  `json:"oneOf,omitzero"`
	Extensible
}

func (s StringPropertySchema) Validate() error { return each("oneOf", s.OneOf) }

func (s *StringPropertySchema) mapPaths(m mapper) {
	mapEach(m, &s.OneOf)
	m.meta(&s.Meta)
}

// StringFormat is the format a text property's value takes.
type StringFormat string

func (f *StringFormat) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, f, "email", "uri", "date", "date-time")
}

// EnumOption is one titled value of a choice.
type EnumOption struct {
	Const       string  `json:"const"`
	Title       string  `json:"title"`
	Description *string `json:"description,omitzero"`
	Extensible
}

func (o EnumOption) Validate() error {
	return check(need(o.Const != "", "const"), need(o.Title != "", "title"))
}

// NumberPropertySchema is a number property.
type NumberPropertySchema struct {
	Title       *string  `json:"title,omitzero"`
	Description *string  `json:"description,omitzero"`
	Minimum     *float64 `json:"minimum,omitzero"`
	Maximum     *float64 `json:"maximum,omitzero"`
	Default     *float64 `json:"default,omitzero"`
	Extensible
}

// IntegerPropertySchema is an integer property.
type IntegerPropertySchema struct {
	Title       *string `json:"title,omitzero"`
	Description *string `json:"description,omitzero"`
	Minimum     *int64  `json:"minimum,omitzero"`
	Maximum     *int64  `json:"maximum,omitzero"`
	Default     *int64  `json:"default,omitzero"`
	Extensible
}

// BooleanPropertySchema is a boolean property.
type BooleanPropertySchema struct {
	Title       *string `json:"title,omitzero"`
	Description *string `json:"description,omitzero"`
	Default     *bool   `json:"default,omitzero"`
	Extensible
}

// MultiSelectPropertySchema is a choice of several values.
type MultiSelectPropertySchema struct {
	Title       *string          `json:"title,omitzero"`
	Description *string          `json:"description,omitzero"`
	MinItems    *int64           `json:"minItems,omitzero"`
	MaxItems    *int64           `json:"maxItems,omitzero"`
	Items       MultiSelectItems `json:"items"`
	Default     []string         `json:"default,omitzero"`
	Extensible
}

func (s MultiSelectPropertySchema) Validate() error {
	return in("items", s.Items.Validate())
}

func (s *MultiSelectPropertySchema) mapPaths(m mapper) {
	s.Items.mapPaths(m)
	m.meta(&s.Meta)
}

// MultiSelectItems are the values of a choice of several: plain, or titled,
// which ACP writes with no type, as MCP does.
type MultiSelectItems struct {
	Plain  *StringMultiSelectItems
	Titled *TitledMultiSelectItems
}

func (u *MultiSelectItems) arms() []arm {
	return []arm{
		armOf("string", &u.Plain),
		armOf("", &u.Titled),
	}
}

func (u *MultiSelectItems) UnmarshalJSON(b []byte) error {
	*u = MultiSelectItems{}
	return decodeUnion(b, "type", u.arms())
}

func (u MultiSelectItems) MarshalJSON() ([]byte, error) { return encodeUnion("type", u.arms()) }

func (u MultiSelectItems) Validate() error {
	_, err := heldArm(u.arms())
	return err
}

func (u *MultiSelectItems) mapPaths(m mapper) {
	mapOpt(m, &u.Plain)
	mapOpt(m, &u.Titled)
}

// StringMultiSelectItems are plain values.
type StringMultiSelectItems struct {
	Enum []string `json:"enum"`
	Extensible
}

func (i StringMultiSelectItems) Validate() error { return need(i.Enum != nil, "enum") }

// TitledMultiSelectItems are titled values.
type TitledMultiSelectItems struct {
	AnyOf []EnumOption `json:"anyOf"`
	Extensible
}

func (i TitledMultiSelectItems) Validate() error {
	return check(need(i.AnyOf != nil, "anyOf"), each("anyOf", i.AnyOf))
}

func (i *TitledMultiSelectItems) mapPaths(m mapper) {
	mapEach(m, &i.AnyOf)
	m.meta(&i.Meta)
}

// CreateElicitationResult is the user's answer: the form's content when they
// accepted, or that they declined or cancelled.
type CreateElicitationResult struct {
	Action  ElicitationAction           `json:"action"`
	Content map[string]ElicitationValue `json:"content,omitzero"`
	Extensible
}

// ElicitationAction is what the user did.
type ElicitationAction string

func (a *ElicitationAction) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, a, "accept", "decline", "cancel")
}

// Validate accepts content only with an accept.
func (r CreateElicitationResult) Validate() error {
	if r.Action == "" {
		return errors.New("action is required")
	}
	if r.Content != nil && r.Action != "accept" {
		return fmt.Errorf("an elicitation the user did not accept has no content, and %q does", r.Action)
	}
	return nil
}

// mapPaths maps the result's _meta; the form's content is what the user
// typed.
// ElicitationValue is one value of a form's content: a string, a number, a
// boolean, or a list of strings.
type ElicitationValue struct{ value any }

func (v *ElicitationValue) UnmarshalJSON(b []byte) error {
	var value any
	if err := decodeNumbers(b, &value); err != nil {
		return err
	}
	switch x := value.(type) {
	case string, json.Number, bool:
	case []any:
		for _, item := range x {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("a form's list value holds strings only, not %s", b)
			}
		}
	default:
		return fmt.Errorf("a form's value is a string, a number, a boolean or a list of strings, not %s", b)
	}
	v.value = value
	return nil
}

func (v ElicitationValue) MarshalJSON() ([]byte, error) { return marshal(v.value) }

// CompleteElicitationParams says a URL elicitation is done.
type CompleteElicitationParams struct {
	ElicitationID string `json:"elicitationId"`
	Extensible
}

func (p CompleteElicitationParams) Validate() error {
	return need(p.ElicitationID != "", "elicitationId")
}
