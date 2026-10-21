package httpkit

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
)

// A Go slice or map that was never filled is nil, and encoding/json writes a nil one as null: an answer that was meant to say "no rows"
// says "no value" instead, and a page that reads rows.length stops. Every handler used to guard against it with a line of its own
// (`if rows == nil { rows = []T{} }`), and the ones that forgot are the ones that failed in a corner nobody tried.
//
// The TypeScript written from the Go types (internal/wire) says what the server promises: a list is a list and a map is a map. This is
// the promise. WriteJSON looks at what it is about to send and, only when the text holds a null at all, writes it again from a copy in
// which a nil slice is an empty one, a nil map is an empty one, and nil bytes are an empty string. What is left null is what a pointer, an
// interface or a value with an encoding of its own (a time, a raw message) says it is, and a field marked omitzero, which is omitted
// when nil and so must stay nil.

var (
	jsonMarshaler = reflect.TypeFor[json.Marshaler]()
	textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
	walkCache     sync.Map // reflect.Type -> bool
)

// ListsAsLists is v with every nil slice and nil map in it made empty, as WriteJSON writes it. v itself is not touched: it may be shared
// (a cache's entry, a document held in memory), and a copy is made of what has to change.
func ListsAsLists(v any) any {
	if v == nil {
		return nil
	}
	out := fixer{}.value(reflect.ValueOf(v), 0)
	return out.Interface()
}

type fixer struct{}

const maxDepth = 64 // a value that loops back on itself is encoded by encoding/json with an error of its own; this only stops the walk

// ownEncoding says t is written by code of its own, whatever its kind: it is left exactly as it is.
func ownEncoding(t reflect.Type) bool {
	return t.Implements(jsonMarshaler) || t.Implements(textMarshaler) ||
		reflect.PointerTo(t).Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(textMarshaler)
}

// canBeNil says whether anything inside a value of type t could be a nil list, so that a value of it has to be walked at all.
func canBeNil(t reflect.Type) bool {
	if c, ok := walkCache.Load(t); ok {
		return c.(bool)
	}
	r := computeCanBeNil(t, map[reflect.Type]bool{})
	walkCache.Store(t, r)
	return r
}

func computeCanBeNil(t reflect.Type, visiting map[reflect.Type]bool) bool {
	if ownEncoding(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Map, reflect.Interface:
		return true
	case reflect.Pointer, reflect.Array:
		if visiting[t] {
			return true
		}
		visiting[t] = true
		return computeCanBeNil(t.Elem(), visiting)
	case reflect.Struct:
		if visiting[t] {
			return true
		}
		visiting[t] = true
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath == "" && computeCanBeNil(t.Field(i).Type, visiting) {
				return true
			}
		}
	}
	return false
}

func (f fixer) value(v reflect.Value, depth int) reflect.Value {
	t := v.Type()
	if depth > maxDepth || !canBeNil(t) {
		return v
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(t).Elem()
		out.Set(f.value(v.Elem(), depth+1))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(f.value(v.Elem(), depth+1))
		return p
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 { // bytes are a string
			if v.IsNil() {
				return reflect.MakeSlice(t, 0, 0)
			}
			return v
		}
		if v.IsNil() {
			return reflect.MakeSlice(t, 0, 0)
		}
		if !canBeNil(t.Elem()) {
			return v
		}
		out := reflect.MakeSlice(t, v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(f.value(v.Index(i), depth+1))
		}
		return out
	case reflect.Array:
		out := reflect.New(t).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(f.value(v.Index(i), depth+1))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.MakeMap(t)
		}
		if !canBeNil(t.Elem()) {
			return v
		}
		out := reflect.MakeMapWithSize(t, v.Len())
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(it.Key(), f.value(it.Value(), depth+1))
		}
		return out
	case reflect.Struct:
		out := reflect.New(t).Elem()
		out.Set(v)
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if sf.PkgPath != "" || !canBeNil(sf.Type) { // unexported (an embedded unexported struct's fields are the one thing not reached) or nothing to fix
				continue
			}
			tag := sf.Tag.Get("json")
			if tag == "-" {
				continue
			}
			fv := v.Field(i)
			if (fv.Kind() == reflect.Slice || fv.Kind() == reflect.Map) && fv.IsNil() && omitted(tag) {
				continue // omitted when nil, so nil stays
			}
			out.Field(i).Set(f.value(fv, depth+1))
		}
		return out
	}
	return v
}

// omitted says the tag leaves a nil slice or map out: omitzero does, and omitempty does for nil as for empty (either way an empty one is
// left out as well, so nothing is lost by leaving it nil).
func omitted(tag string) bool {
	_, opts, _ := strings.Cut(tag, ",")
	for _, o := range strings.Split(opts, ",") {
		if o == "omitzero" || o == "omitempty" {
			return true
		}
	}
	return false
}
