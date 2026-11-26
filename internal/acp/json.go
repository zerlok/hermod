package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/zerlok/hermod/internal/pathmap"
)

// validator is a type that checks the members ACP requires of it. encoding/json
// reads a missing member as its zero value, which would let a message that
// lacks, say, its cwd pass as one naming the empty directory.
type validator interface{ Validate() error }

// decode reads b into v and checks it with v's own Validate, when it has one.
func decode(b []byte, v any) error {
	if isNull(b) {
		return errors.New("null where a value is required")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return err
	}
	if x, ok := v.(validator); ok {
		return x.Validate()
	}
	return nil
}

// check is the first of errs that is not nil.
func check(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// need rejects a required member, named member, that is missing.
func need(present bool, member string) error {
	if present {
		return nil
	}
	return fmt.Errorf("%s is required", member)
}

// in is err, from checking member, prefixed with its name.
func in(member string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", member, err)
	}
	return nil
}

// valid checks v, a value of one of ACP's types, when the type has a Validate.
func valid(v any) error {
	if x, ok := v.(validator); ok {
		return x.Validate()
	}
	return nil
}

// opt checks the member at p, when it is given.
func opt[T any](member string, p *T) error {
	if p == nil {
		return nil
	}
	return in(member, valid(*p))
}

// each checks every item of the list member.
func each[T any](member string, items []T) error {
	for i, item := range items {
		if err := valid(item); err != nil {
			return fmt.Errorf("%s[%d]: %w", member, i, err)
		}
	}
	return nil
}

func isNull(b []byte) bool { return bytes.Equal(bytes.TrimSpace(b), []byte("null")) }

// marshal writes v as JSON as it was given: paths and prose may hold '&', '<'
// or '>', which json.Marshal escapes for HTML, and the stream is not HTML.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// decodeNumbers reads b into v keeping each number's digits: a float64 would
// round a large integer, and the other side gets back what it wrote.
func decodeNumbers(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("more than one JSON value")
	}
	return nil
}

// decodeEnum reads b as one of the values ACP declares for T.
func decodeEnum[T ~string](b []byte, v *T, known ...T) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if !slices.Contains(known, T(s)) {
		return fmt.Errorf("%q is not one of %q", s, known)
	}
	*v = T(s)
	return nil
}

// A union of ACP's is an object one member of which, its tag, names the
// variant it is; its Go type has a pointer field for each variant, one of them
// set, and lists its arms once for reading, writing and checking it. A
// variant is checked as it is read, so a list of a union needs no check of
// its items.

// arm is one variant of a union: the tag that names it, "" for the variant
// ACP writes with no tag, how to read it into its field, and the field's
// value when the union holds it.
type arm struct {
	tag   string
	read  func(b []byte) error
	value any
}

// armOf is the arm tag of the variant held in field.
func armOf[T any](tag string, field **T) arm {
	a := arm{tag: tag, read: func(b []byte) error {
		v := new(T)
		if err := decode(b, v); err != nil {
			return err
		}
		*field = v
		return nil
	}}
	if *field != nil {
		a.value = *field
	}
	return a
}

// decodeUnion reads b into the arm its member key names, or the arm of no tag
// when it has none, and rejects a variant the union does not have: ACP
// reserves other values for its future variants and for extensions, whose
// members no type here declares.
func decodeUnion(b []byte, key string, arms []arm) error {
	tag, ok, err := tagOf(b, key)
	if err != nil {
		return err
	}
	for _, a := range arms {
		if a.tag == tag {
			return a.read(b)
		}
	}
	if !ok {
		return fmt.Errorf("%s is required", key)
	}
	return fmt.Errorf("%s %q is not one Hermod reads", key, tag)
}

// tagOf is the string member key of the object b; ok is false when b has none.
func tagOf(b []byte, key string) (tag string, ok bool, err error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(b, &members); err != nil {
		return "", false, err
	}
	raw, ok := members[key]
	if !ok {
		return "", false, nil
	}
	if err := json.Unmarshal(raw, &tag); err != nil {
		return "", false, fmt.Errorf("%s: %w", key, err)
	}
	return tag, true, nil
}

// heldArm is the one arm whose variant the union holds; a union that holds
// none, such as a required union member left out, or more than one is
// invalid.
func heldArm(arms []arm) (arm, error) {
	var found []arm
	for _, a := range arms {
		if a.value != nil {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		return arm{}, errors.New("a union holds no variant, or more than one")
	}
	return found[0], nil
}

// encodeUnion writes the variant the union holds, with its member key naming
// it first, unless it is the variant of no tag.
func encodeUnion(key string, arms []arm) ([]byte, error) {
	a, err := heldArm(arms)
	if err != nil {
		return nil, err
	}
	body, err := marshal(a.value)
	if err != nil || a.tag == "" {
		return body, err
	}
	head, err := marshal(map[string]string{key: a.tag})
	if err != nil {
		return nil, err
	}
	if bytes.Equal(body, []byte("{}")) {
		return head, nil
	}
	// NOTE: both are JSON objects, head with one member and body with at
	// least one, so joining them inside one pair of braces is one object.
	return slices.Concat(head[:len(head)-1], []byte(","), body[1:]), nil
}

// Meta is ACP's _meta, which every type of ACP's declares: an object whose
// members ACP leaves to the two sides, such as Air's jetbrains.air. It is
// carried whole, every string in it mapped, since an extension may name a
// path in any member.
type Meta map[string]any

// Extensible is embedded by every type of ACP's, to declare its _meta: as an
// untagged embedded field, its _meta is a member of the type's own object, and
// its mapPaths maps it for a type that declares no path of its own.
type Extensible struct {
	Meta Meta `json:"_meta,omitzero"`
}

func (e *Extensible) mapPaths(m mapper) { m.meta(&e.Meta) }

func (m *Meta) UnmarshalJSON(b []byte) error {
	var members map[string]any
	if err := decodeNumbers(b, &members); err != nil {
		return err
	}
	*m = members
	return nil
}

// Value is a JSON value whose shape ACP leaves open, such as a tool's raw
// input or an error's data. It is carried whole, every string in it mapped as
// in a Meta: a tool names its files in its input, as Air's plan file is.
type Value struct {
	value any
	set   bool
}

// IsZero reports whether the value was left out, which omitzero writes as no
// member at all, apart from a value written as null.
func (v Value) IsZero() bool { return !v.set }

func (v *Value) UnmarshalJSON(b []byte) error {
	v.set = true
	return decodeNumbers(b, &v.value)
}

func (v Value) MarshalJSON() ([]byte, error) { return marshal(v.value) }

// Nullable is a member ACP reads three ways: left out keeps what the other
// side holds, null clears it, and a value replaces it.
type Nullable[T any] struct {
	value T
	state nullState
}

type nullState uint8

const (
	leftOut nullState = iota
	null
	given
)

// IsZero reports whether the member was left out, which omitzero writes as no
// member at all.
func (n Nullable[T]) IsZero() bool { return n.state == leftOut }

func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	if isNull(b) {
		*n = Nullable[T]{state: null}
		return nil
	}
	var v T
	if err := decode(b, &v); err != nil {
		return err
	}
	*n = Nullable[T]{value: v, state: given}
	return nil
}

func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.state != given {
		return []byte("null"), nil
	}
	return marshal(n.value)
}

// pather is a type of ACP's that maps the fields it declares as paths, and
// its _meta and any other field ACP leaves free-form. Mapped hands it a copy of
// a message, which it maps in place; each field it maps gets a new value, so
// the message it was copied from keeps its own. A text that only mentions a
// path, such as a prompt or a title, says what its writer wrote and is not
// mapped.
type pather interface{ mapPaths(m mapper) }

// mapper is the path function Mapped is handed.
type mapper func(string) string

// path maps a field that holds an absolute path.
func (m mapper) path(p *string) { *p = m(*p) }

func (m mapper) optPath(p **string) {
	if *p != nil {
		s := m(**p)
		*p = &s
	}
}

func (m mapper) paths(p *[]string) {
	if *p == nil {
		return
	}
	out := make([]string, len(*p))
	for i, s := range *p {
		out[i] = m(s)
	}
	*p = out
}

// uri maps a field that holds a URI, which names a file by a file URI.
func (m mapper) uri(p *string) { *p = mapFileURI(*p, m) }

func (m mapper) optURI(p **string) {
	if *p != nil {
		s := mapFileURI(**p, m)
		*p = &s
	}
}

// meta maps every string of a _meta.
func (m mapper) meta(p *Meta) {
	if *p == nil {
		return
	}
	// NOTE: pathmap.Value returns a map[string]any for one, so the assertion
	// holds.
	*p = pathmap.Value(map[string]any(*p), m.anyString).(map[string]any)
}

// value maps every string of a free-form value.
func (m mapper) value(p *Value) { p.value = pathmap.Value(p.value, m.anyString) }

// anyString maps a string of a free-form value: a file URI as one, and any
// other string as a path, which the path map leaves as it is unless it lies
// under a root.
func (m mapper) anyString(s string) string {
	if strings.HasPrefix(s, fileScheme+"://") {
		return mapFileURI(s, m)
	}
	return m(s)
}

// mapOpt maps an optional field into a new value.
func mapOpt[T any, P interface {
	*T
	pather
}](m mapper, p **T) {
	if *p == nil {
		return
	}
	v := **p
	P(&v).mapPaths(m)
	*p = &v
}

// mapEach maps every item of a list into a new list.
func mapEach[T any, P interface {
	*T
	pather
}](m mapper, items *[]T) {
	if *items == nil {
		return
	}
	out := slices.Clone(*items)
	for i := range out {
		P(&out[i]).mapPaths(m)
	}
	*items = out
}

// mapNullable maps a Nullable's value when it is given.
func mapNullable[T any](n *Nullable[T], mapValue func(*T)) {
	if n.state == given {
		mapValue(&n.value)
	}
}

const fileScheme = "file"

// mapFileURI maps the path of a file URI on this workstation; a URI of another
// scheme, one that does not parse, or one naming another host is left as
// written.
func mapFileURI(s string, path func(string) string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != fileScheme || (u.Host != "" && u.Host != "localhost") {
		return s
	}
	mapped := path(u.Path)
	if mapped == u.Path {
		return s
	}
	u.Path, u.RawPath = mapped, ""
	return u.String()
}
