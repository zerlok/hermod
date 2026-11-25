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
	asks
	noDir
	Message string          `json:"message"`
	Mode    ElicitationMode `json:"mode"`
	// RequestedSchema is the form's, for the form mode.
	RequestedSchema *ElicitationSchema `json:"requestedSchema,omitzero"`
	// ElicitationID and URL are the URL mode's.
	ElicitationID *string    `json:"elicitationId,omitzero"`
	URL           *string    `json:"url,omitzero"`
	SessionID     *SessionID `json:"sessionId,omitzero"`
	ToolCallID    *string    `json:"toolCallId,omitzero"`
	RequestID     *requestID `json:"requestId,omitzero"`
	Meta          Meta       `json:"_meta,omitzero"`
}

func (CreateElicitationParams) Method() string { return "elicitation/create" }

func (p CreateElicitationParams) Chat() session.ChatID {
	if p.SessionID == nil {
		return ""
	}
	return p.SessionID.chat()
}

// ElicitationMode is how the user is asked.
type ElicitationMode string

func (m *ElicitationMode) UnmarshalJSON(b []byte) error { return decodeEnum(b, m, "form", "url") }

// UnmarshalJSON checks that the params hold what their mode and their scope
// need, which ACP writes in one object.
func (p *CreateElicitationParams) UnmarshalJSON(b []byte) error {
	type plain CreateElicitationParams
	if err := decode(b, (*plain)(p)); err != nil {
		return err
	}
	switch {
	case p.Mode == "form" && p.RequestedSchema == nil:
		return errors.New("a form elicitation has a requestedSchema")
	case p.Mode == "url" && (p.ElicitationID == nil || p.URL == nil):
		return errors.New("a URL elicitation has an elicitationId and a url")
	case (p.SessionID == nil) == (p.RequestID == nil):
		return errors.New("an elicitation is tied to a sessionId or to a requestId")
	case p.ToolCallID != nil && p.SessionID == nil:
		return errors.New("an elicitation's toolCallId is in a sessionId")
	}
	return nil
}

// ElicitationSchema is a form: the properties it asks for.
type ElicitationSchema struct {
	Type        *string                              `json:"type,omitzero"`
	Title       *string                              `json:"title,omitzero"`
	Properties  map[string]ElicitationPropertySchema `json:"properties,omitzero"`
	Required    []string                             `json:"required,omitzero"`
	Description *string                              `json:"description,omitzero"`
	Meta        Meta                                 `json:"_meta,omitzero"`
}

// UnmarshalJSON accepts only an object schema, the one ACP declares.
func (s *ElicitationSchema) UnmarshalJSON(b []byte) error {
	type plain ElicitationSchema
	if err := decode(b, (*plain)(s)); err != nil {
		return err
	}
	if s.Type != nil && *s.Type != "object" {
		return fmt.Errorf("an elicitation's schema is an object, not %q", *s.Type)
	}
	return nil
}

// ElicitationPropertySchema is one property of a form.
type ElicitationPropertySchema struct {
	String  *StringPropertySchema
	Number  *NumberPropertySchema
	Integer *IntegerPropertySchema
	Boolean *BooleanPropertySchema
	Array   *MultiSelectPropertySchema
}

func (s *ElicitationPropertySchema) UnmarshalJSON(b []byte) error {
	*s = ElicitationPropertySchema{}
	return decodeTagged(b, "type", variants{"string": &s.String, "number": &s.Number, "integer": &s.Integer, "boolean": &s.Boolean, "array": &s.Array})
}

func (s ElicitationPropertySchema) MarshalJSON() ([]byte, error) {
	return encodeTagged("type", variants{"string": s.String, "number": s.Number, "integer": s.Integer, "boolean": s.Boolean, "array": s.Array})
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
	Meta        Meta          `json:"_meta,omitzero"`
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
	Meta        Meta    `json:"_meta,omitzero"`
}

// NumberPropertySchema is a number property.
type NumberPropertySchema struct {
	Title       *string  `json:"title,omitzero"`
	Description *string  `json:"description,omitzero"`
	Minimum     *float64 `json:"minimum,omitzero"`
	Maximum     *float64 `json:"maximum,omitzero"`
	Default     *float64 `json:"default,omitzero"`
	Meta        Meta     `json:"_meta,omitzero"`
}

// IntegerPropertySchema is an integer property.
type IntegerPropertySchema struct {
	Title       *string `json:"title,omitzero"`
	Description *string `json:"description,omitzero"`
	Minimum     *int64  `json:"minimum,omitzero"`
	Maximum     *int64  `json:"maximum,omitzero"`
	Default     *int64  `json:"default,omitzero"`
	Meta        Meta    `json:"_meta,omitzero"`
}

// BooleanPropertySchema is a boolean property.
type BooleanPropertySchema struct {
	Title       *string `json:"title,omitzero"`
	Description *string `json:"description,omitzero"`
	Default     *bool   `json:"default,omitzero"`
	Meta        Meta    `json:"_meta,omitzero"`
}

// MultiSelectPropertySchema is a choice of several values.
type MultiSelectPropertySchema struct {
	Title       *string          `json:"title,omitzero"`
	Description *string          `json:"description,omitzero"`
	MinItems    *int64           `json:"minItems,omitzero"`
	MaxItems    *int64           `json:"maxItems,omitzero"`
	Items       MultiSelectItems `json:"items"`
	Default     []string         `json:"default,omitzero"`
	Meta        Meta             `json:"_meta,omitzero"`
}

// MultiSelectItems are the values of a choice of several: plain, or titled,
// which ACP writes with no type, as MCP does.
type MultiSelectItems struct {
	Plain  *StringMultiSelectItems
	Titled *TitledMultiSelectItems
}

// StringMultiSelectItems are plain values.
type StringMultiSelectItems struct {
	Enum []string `json:"enum"`
	Meta Meta     `json:"_meta,omitzero"`
}

// TitledMultiSelectItems are titled values.
type TitledMultiSelectItems struct {
	AnyOf []EnumOption `json:"anyOf"`
	Meta  Meta         `json:"_meta,omitzero"`
}

func (i *MultiSelectItems) UnmarshalJSON(b []byte) error {
	*i = MultiSelectItems{}
	typed, err := has(b, "type")
	if err != nil {
		return err
	}
	if !typed {
		return decodeInto(b, &i.Titled)
	}
	return decodeTagged(b, "type", variants{"string": &i.Plain})
}

func (i MultiSelectItems) MarshalJSON() ([]byte, error) {
	if i.Titled != nil && i.Plain == nil {
		return marshal(i.Titled)
	}
	return encodeTagged("type", variants{"string": i.Plain})
}

// CreateElicitationResult is the user's answer: the form's content when they
// accepted, or that they declined or cancelled.
type CreateElicitationResult struct {
	noChat
	Action  ElicitationAction           `json:"action"`
	Content map[string]ElicitationValue `json:"content,omitzero"`
	Meta    Meta                        `json:"_meta,omitzero"`
}

// ElicitationAction is what the user did.
type ElicitationAction string

func (a *ElicitationAction) UnmarshalJSON(b []byte) error {
	return decodeEnum(b, a, "accept", "decline", "cancel")
}

// UnmarshalJSON accepts content only with an accept.
func (r *CreateElicitationResult) UnmarshalJSON(b []byte) error {
	type plain CreateElicitationResult
	if err := decode(b, (*plain)(r)); err != nil {
		return err
	}
	if r.Content != nil && r.Action != "accept" {
		return fmt.Errorf("an elicitation the user did not accept has no content, and %q does", r.Action)
	}
	return nil
}

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
	passes
	noChat
	noDir
	ElicitationID string `json:"elicitationId"`
	Meta          Meta   `json:"_meta,omitzero"`
}

func (CompleteElicitationParams) Method() string { return "elicitation/complete" }
