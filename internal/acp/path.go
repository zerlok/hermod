package acp

import (
	"net/url"
	"reflect"
	"strings"

	"github.com/zerlok/hermod/internal/session"
)

// A type declares which of its fields are paths with the struct tag acp:
//
//   - acp:"path" is an absolute path, or a list of them;
//   - acp:"uri" is a URI that names a file by a file URI.
//
// mapped takes each through the path map, and every string inside a Meta or a
// Value, whose shape ACP leaves open, as one or the other. Nothing else is
// mapped: a text that only mentions a path, such as a prompt or a title, says
// what its writer wrote.
const pathTag = "acp"

type mode uint8

const (
	keep mode = iota
	asPath
	asURI
)

// mapped is v with every path its type declares taken through path. v is left
// as it was: every struct, slice, map and pointer on the way is new.
func mapped[T any](v T, path func(session.Path) session.Path) T {
	w := walker{path: path}
	// NOTE: walk returns a value of the type it was given.
	return w.walk(reflect.ValueOf(&v).Elem(), keep).Interface().(T)
}

type walker struct {
	path func(session.Path) session.Path
	mode mode // the mode of the field being walked, for mapWith
}

// stringsMapper is a type whose strings are all mapped, such as Meta.
type stringsMapper interface {
	mapStrings(mapString func(string) string) any
}

// fieldMapper is a type that maps its value by the mode of its field, such as
// Nullable.
type fieldMapper interface {
	mapWith(w walker) any
}

func (w walker) walk(v reflect.Value, m mode) reflect.Value {
	// A pointer has its value's methods too, and is walked to its value first.
	if v.Kind() != reflect.Pointer && v.CanInterface() {
		switch x := v.Interface().(type) {
		case stringsMapper:
			return reflect.ValueOf(x.mapStrings(w.anyString))
		case fieldMapper:
			w.mode = m
			return reflect.ValueOf(x.mapWith(w))
		}
	}
	switch v.Kind() {
	case reflect.String:
		if m == keep {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.SetString(w.mapString(v.String(), m))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(w.walk(v.Elem(), m))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(w.walk(v.Index(i), m))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), w.walk(it.Value(), m))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			out.Field(i).Set(w.walk(v.Field(i), fieldMode(f)))
		}
		return out
	}
	return v
}

func fieldMode(f reflect.StructField) mode {
	switch f.Tag.Get(pathTag) {
	case "path":
		return asPath
	case "uri":
		return asURI
	}
	return keep
}

func (w walker) mapString(s string, m mode) string {
	if m == asURI {
		return mapFileURI(s, w.path)
	}
	return string(w.path(session.Path(s)))
}

// anyString maps a string of an open value: a file URI as one, and any other
// string as a path, which the path map leaves as it is unless it lies under a
// root.
func (w walker) anyString(s string) string {
	if strings.HasPrefix(s, fileScheme+"://") {
		return mapFileURI(s, w.path)
	}
	return string(w.path(session.Path(s)))
}

const fileScheme = "file"

// mapFileURI maps the path of a file URI on this workstation; a URI of another
// scheme, one that does not parse, or one naming another host is left as
// written.
func mapFileURI(s string, path func(session.Path) session.Path) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != fileScheme || (u.Host != "" && u.Host != "localhost") {
		return s
	}
	mapped := string(path(session.Path(u.Path)))
	if mapped == u.Path {
		return s
	}
	u.Path, u.RawPath = mapped, ""
	return u.String()
}
