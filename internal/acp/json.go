package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// decode parses b fully into v: every member's type is checked by v's type,
// and every member ACP requires must be there and not null. encoding/json
// reads a missing member as its zero value, which would let a message that
// lacks, say, its cwd pass as one naming the empty directory.
func decode(b []byte, v any) error {
	if isNull(b) {
		return errors.New("null where a value is required")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return err
	}
	return required(b, reflect.TypeOf(v).Elem())
}

var unmarshaler = reflect.TypeFor[json.Unmarshaler]()

// required checks b, a document v's type t parsed, for the members t requires:
// a struct's field whose json tag has neither omitempty nor omitzero. A type
// with its own UnmarshalJSON checks its own members.
func required(b []byte, t reflect.Type) error {
	if reflect.PointerTo(t).Implements(unmarshaler) || isNull(b) {
		return nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return required(b, t.Elem())
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(b, &items); err != nil {
			return err
		}
		for i, item := range items {
			if err := required(item, t.Elem()); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
	case reflect.Map:
		var members map[string]json.RawMessage
		if err := json.Unmarshal(b, &members); err != nil {
			return err
		}
		for key, member := range members {
			if err := required(member, t.Elem()); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	case reflect.Struct:
		var members map[string]json.RawMessage
		if err := json.Unmarshal(b, &members); err != nil {
			return err
		}
		if err := exactNames(members, t); err != nil {
			return err
		}
		return requiredFields(members, t)
	}
	return nil
}

// requiredFields checks the members of an object of struct type t for the
// fields t requires, and each member's own.
func requiredFields(members map[string]json.RawMessage, t reflect.Type) error {
	for i := range t.NumField() {
		f := t.Field(i)
		if embedded(f) {
			if err := requiredFields(members, f.Type); err != nil {
				return err
			}
			continue
		}
		name, optional, ok := jsonName(f)
		if !ok {
			continue
		}
		member, present := members[name]
		if !present || isNull(member) {
			if optional {
				continue
			}
			return fmt.Errorf("%s is required", name)
		}
		if err := required(member, f.Type); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// embedded reports whether f is an embedded struct whose members encoding/json
// writes as its container's own.
func embedded(f reflect.StructField) bool {
	return f.Anonymous && f.IsExported() && f.Tag.Get("json") == "" && f.Type.Kind() == reflect.Struct
}

// exactMembers rejects a member of the object b whose name differs from one
// struct type t declares only by case.
func exactMembers(b []byte, t reflect.Type) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(b, &members); err != nil {
		return err
	}
	return exactNames(members, t)
}

// exactNames rejects a member whose name differs from one t declares only by
// case. encoding/json matches names regardless of case, so such a member,
// which no type declares, would otherwise overwrite the declared one.
func exactNames(members map[string]json.RawMessage, t reflect.Type) error {
	declared := declaredNames(t)
	for key := range members {
		if slices.Contains(declared, key) {
			continue
		}
		for _, name := range declared {
			if strings.EqualFold(key, name) {
				return fmt.Errorf("%s is not %s", key, name)
			}
		}
	}
	return nil
}

// declaredNames are the members struct type t is written with.
func declaredNames(t reflect.Type) []string {
	var names []string
	for i := range t.NumField() {
		f := t.Field(i)
		if embedded(f) {
			names = append(names, declaredNames(f.Type)...)
			continue
		}
		if name, _, ok := jsonName(f); ok {
			names = append(names, name)
		}
	}
	return names
}

// jsonName is the member a field is written as, and whether it may be left
// out; ok is false for a field that is not written.
func jsonName(f reflect.StructField) (name string, optional, ok bool) {
	if !f.IsExported() {
		return "", false, false
	}
	tag, hasTag := f.Tag.Lookup("json")
	if !hasTag || tag == "-" {
		return "", false, false
	}
	name, options, _ := strings.Cut(tag, ",")
	for option := range strings.SplitSeq(options, ",") {
		if option == "omitempty" || option == "omitzero" {
			optional = true
		}
	}
	return name, optional, true
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

// variants are the members of a union, by the value of the member that names
// each: the union's field for that variant to write it, and a pointer to the
// field to read it.
type variants map[string]any

// decodeTagged parses b into the variant that its member key names, and
// rejects a variant the union does not have.
func decodeTagged(b []byte, key string, union variants) error {
	tag, ok, err := tagOf(b, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s is required", key)
	}
	field, known := union[tag]
	if !known {
		return unknownVariant(key, tag)
	}
	return decodeInto(b, field)
}

// unknownVariant rejects a variant of a union that Hermod does not have: ACP
// reserves other values for its future variants and for extensions, whose
// members no type here declares.
func unknownVariant(key, tag string) error {
	return fmt.Errorf("%s %q is not one Hermod reads", key, tag)
}

// decodeInto parses b into a new value for field, a pointer to a pointer
// field of a union.
func decodeInto(b []byte, field any) error {
	target := reflect.ValueOf(field).Elem()
	value := reflect.New(target.Type().Elem())
	if err := decode(b, value.Interface()); err != nil {
		return err
	}
	target.Set(value)
	return nil
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

// has reports whether the object b has member key.
func has(b []byte, key string) (bool, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(b, &members); err != nil {
		return false, err
	}
	_, ok := members[key]
	return ok, nil
}

// encodeTagged writes the one variant a union holds, with its member key
// naming it. union maps each name to the union's field for it.
func encodeTagged(key string, union variants) ([]byte, error) {
	tag, value, err := heldVariant(union)
	if err != nil {
		return nil, err
	}
	return withTag(key, tag, value)
}

var errNoVariant = errors.New("a union holds no variant, or more than one")

// heldVariant is the one variant of union that is set.
func heldVariant(union variants) (tag string, value any, err error) {
	found := 0
	for name, field := range union {
		if v := reflect.ValueOf(field); !v.IsNil() {
			tag, value = name, field
			found++
		}
	}
	if found != 1 {
		return "", nil, errNoVariant
	}
	return tag, value, nil
}

// withTag writes v, an object, with member key set to tag first.
func withTag(key, tag string, v any) ([]byte, error) {
	body, err := marshal(v)
	if err != nil {
		return nil, err
	}
	head, err := marshal(map[string]string{key: tag})
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

// one writes the one variant of union that is set, with no member naming it.
func one(union variants) ([]byte, error) {
	_, value, err := heldVariant(union)
	if err != nil {
		return nil, err
	}
	return marshal(value)
}

// Meta is ACP's _meta, which every type of ACP's declares: an object whose
// members ACP leaves to the two sides, such as Air's jetbrains.air. It is
// carried whole, every string in it mapped, since an extension may name a
// path in any member.
type Meta map[string]any

func (m *Meta) UnmarshalJSON(b []byte) error {
	var members map[string]any
	if err := decodeNumbers(b, &members); err != nil {
		return err
	}
	*m = members
	return nil
}

func (m Meta) mapStrings(mapString func(string) string) any {
	if m == nil {
		return m
	}
	return Meta(mapAny(map[string]any(m), mapString).(map[string]any))
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

func (v Value) mapStrings(mapString func(string) string) any {
	if !v.set {
		return v
	}
	return Value{value: mapAny(v.value, mapString), set: true}
}

// decodeNumbers parses b into v keeping each number's digits: a float64 would
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

// mapAny is v, a value encoding/json reads into an interface, with every
// string in it taken through mapString; objects and arrays are new.
func mapAny(v any, mapString func(string) string) any {
	switch v := v.(type) {
	case string:
		return mapString(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = mapAny(item, mapString)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, member := range v {
			out[key] = mapAny(member, mapString)
		}
		return out
	}
	return v
}

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

// Given is a Nullable holding v.
func Given[T any](v T) Nullable[T] { return Nullable[T]{value: v, state: given} }

// Null is a Nullable written as null.
func Null[T any]() Nullable[T] { return Nullable[T]{state: null} }

// Get is the value, when one was given.
func (n Nullable[T]) Get() (T, bool) { return n.value, n.state == given }

// IsNull reports whether the member was written as null.
func (n Nullable[T]) IsNull() bool { return n.state == null }

// IsZero reports whether the member was left out, which omitzero writes as no
// member at all.
func (n Nullable[T]) IsZero() bool { return n.state == leftOut }

func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	if isNull(b) {
		*n = Null[T]()
		return nil
	}
	var v T
	if err := decode(b, &v); err != nil {
		return err
	}
	*n = Given(v)
	return nil
}

func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.state != given {
		return []byte("null"), nil
	}
	return marshal(n.value)
}

func (n Nullable[T]) mapWith(w walker) any {
	if n.state != given {
		return n
	}
	// NOTE: walk returns a value of the type it was given.
	n.value = w.walk(reflect.ValueOf(&n.value).Elem(), w.mode).Interface().(T)
	return n
}
