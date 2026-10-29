// Package jsonx implements the structural JSON operations claudesync uses for
// settings files: layered merging, change detection, path edits and a
// three-way merge for git.
//
// Values are the generic encoding/json types (map[string]any, []any, string,
// bool, nil) with numbers decoded as json.Number so they round-trip exactly.
package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Object is a decoded JSON object.
type Object = map[string]any

// Parse decodes b. Empty or whitespace-only input decodes to an empty object.
func Parse(b []byte) (any, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return Object{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("unexpected data after JSON value")
	}
	return v, nil
}

// ParseObject decodes b and requires a JSON object.
func ParseObject(b []byte) (Object, error) {
	v, err := Parse(b)
	if err != nil {
		return nil, err
	}
	o, ok := v.(Object)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return o, nil
}

// Marshal encodes v with sorted keys, two-space indentation, no HTML escaping
// and a trailing newline.
func Marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err) // only decoded JSON values are passed in
	}
	return buf.Bytes()
}

func compact(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.String()
}

// Equal reports whether a and b encode to the same JSON.
func Equal(a, b any) bool {
	if a == missing || b == missing {
		return a == b
	}
	return compact(a) == compact(b)
}

// Clone returns a deep copy of v.
func Clone(v any) any {
	switch t := v.(type) {
	case Object:
		o := make(Object, len(t))
		for k, val := range t {
			o[k] = Clone(val)
		}
		return o
	case []any:
		a := make([]any, len(t))
		for i, val := range t {
			a[i] = Clone(val)
		}
		return a
	default:
		return v
	}
}

func contains(list []any, item any) bool {
	for _, x := range list {
		if Equal(x, item) {
			return true
		}
	}
	return false
}

// Merge overlays over onto base: objects merge recursively, arrays are
// unioned (base items first), anything else is replaced by over.
func Merge(base, over any) any {
	bo, bok := base.(Object)
	oo, ook := over.(Object)
	if bok && ook {
		out := Clone(bo).(Object)
		for k, v := range oo {
			if cur, ok := out[k]; ok {
				out[k] = Merge(cur, v)
			} else {
				out[k] = Clone(v)
			}
		}
		return out
	}
	ba, bok := base.([]any)
	oa, ook := over.([]any)
	if bok && ook {
		out := Clone(ba).([]any)
		for _, v := range oa {
			if !contains(out, v) {
				out = append(out, Clone(v))
			}
		}
		return out
	}
	return Clone(over)
}

// OpKind identifies a change between two settings documents.
type OpKind int

const (
	OpSet    OpKind = iota // set Path to Value
	OpDelete               // remove Path
	OpAdd                  // add Value to the array at Path
	OpRemove               // remove Value from the array at Path
)

func (k OpKind) String() string {
	return [...]string{"set", "delete", "add", "remove"}[k]
}

// Op is one change produced by Diff.
type Op struct {
	Kind  OpKind
	Path  []string
	Value any
}

func (o Op) String() string {
	p := strings.Join(o.Path, ".")
	if o.Kind == OpDelete {
		return o.Kind.String() + " " + p
	}
	return fmt.Sprintf("%s %s %s", o.Kind, p, strings.TrimSpace(compact(o.Value)))
}

// Diff returns the operations that turn the object from into the object to.
// Objects are compared key by key, arrays item by item (as sets).
func Diff(from, to Object) []Op {
	var ops []Op
	diff(nil, from, to, &ops)
	return ops
}

func diff(prefix []string, from, to Object, ops *[]Op) {
	path := func(k string) []string {
		p := make([]string, len(prefix)+1)
		copy(p, prefix)
		p[len(prefix)] = k
		return p
	}
	for _, k := range SortedKeys(from) {
		if _, ok := to[k]; !ok {
			*ops = append(*ops, Op{Kind: OpDelete, Path: path(k)})
		}
	}
	for _, k := range SortedKeys(to) {
		tv := to[k]
		fv, ok := from[k]
		if !ok {
			*ops = append(*ops, Op{Kind: OpSet, Path: path(k), Value: Clone(tv)})
			continue
		}
		fo, fok := fv.(Object)
		tob, tok := tv.(Object)
		if fok && tok {
			diff(path(k), fo, tob, ops)
			continue
		}
		fa, fok := fv.([]any)
		ta, tok := tv.([]any)
		if fok && tok {
			for _, item := range fa {
				if !contains(ta, item) {
					*ops = append(*ops, Op{Kind: OpRemove, Path: path(k), Value: Clone(item)})
				}
			}
			for _, item := range ta {
				if !contains(fa, item) {
					*ops = append(*ops, Op{Kind: OpAdd, Path: path(k), Value: Clone(item)})
				}
			}
			continue
		}
		if !Equal(fv, tv) {
			*ops = append(*ops, Op{Kind: OpSet, Path: path(k), Value: Clone(tv)})
		}
	}
}

// Get returns the value at path.
func Get(obj Object, path []string) (any, bool) {
	var cur any = obj
	for _, k := range path {
		o, ok := cur.(Object)
		if !ok {
			return nil, false
		}
		cur, ok = o[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Has reports whether path exists in obj.
func Has(obj Object, path []string) bool {
	_, ok := Get(obj, path)
	return ok
}

// Set stores v at path, creating (or replacing non-object) parents.
func Set(obj Object, path []string, v any) {
	cur := obj
	for _, k := range path[:len(path)-1] {
		next, ok := cur[k].(Object)
		if !ok {
			next = Object{}
			cur[k] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = v
}

// Delete removes path from obj and prunes parents left empty by the removal.
// It reports whether anything was removed.
func Delete(obj Object, path []string) bool {
	if len(path) == 0 {
		return false
	}
	if len(path) == 1 {
		_, ok := obj[path[0]]
		delete(obj, path[0])
		return ok
	}
	child, ok := obj[path[0]].(Object)
	if !ok {
		return false
	}
	removed := Delete(child, path[1:])
	if removed && len(child) == 0 {
		delete(obj, path[0])
	}
	return removed
}

// AddItem adds item to the array at path (creating it) unless already present.
func AddItem(obj Object, path []string, item any) {
	cur, _ := Get(obj, path)
	arr, _ := cur.([]any)
	if contains(arr, item) {
		return
	}
	Set(obj, path, append(Clone(arr).([]any), item))
}

// RemoveItem removes item from the array at path. An array left empty is
// removed as well.
func RemoveItem(obj Object, path []string, item any) {
	cur, _ := Get(obj, path)
	arr, ok := cur.([]any)
	if !ok {
		return
	}
	out := make([]any, 0, len(arr))
	for _, x := range arr {
		if !Equal(x, item) {
			out = append(out, x)
		}
	}
	if len(out) == len(arr) {
		return
	}
	if len(out) == 0 {
		Delete(obj, path)
		return
	}
	Set(obj, path, out)
}

// IsArrayAt reports whether obj holds an array at path.
func IsArrayAt(obj Object, path []string) bool {
	v, ok := Get(obj, path)
	if !ok {
		return false
	}
	_, ok = v.([]any)
	return ok
}

// Restrict returns the parts of src whose paths also exist in other: object
// keys present in both (recursively) and array items present in both. Values
// come from src.
func Restrict(src, other any) any {
	so, sok := src.(Object)
	oo, ook := other.(Object)
	if sok && ook {
		out := Object{}
		for k, v := range so {
			if ov, ok := oo[k]; ok {
				out[k] = Restrict(v, ov)
			}
		}
		return out
	}
	sa, sok := src.([]any)
	oa, ook := other.([]any)
	if sok && ook {
		out := []any{}
		for _, v := range sa {
			if contains(oa, v) {
				out = append(out, Clone(v))
			}
		}
		return out
	}
	return Clone(src)
}

// missing marks an absent key during a three-way merge.
var missing any = &struct{ name string }{"missing"}

// Merge3 merges the changes from base to a and from base to b. Objects merge
// key by key and arrays item by item; when both sides change the same scalar
// differently, b wins.
func Merge3(base, a, b any) any {
	return merge3(base, a, b)
}

func merge3(o, a, b any) any {
	if Equal(a, b) {
		return a
	}
	if Equal(o, a) {
		return b
	}
	if Equal(o, b) {
		return a
	}
	ao, aok := a.(Object)
	bo, bok := b.(Object)
	if aok && bok {
		oo, _ := o.(Object)
		out := Object{}
		keys := map[string]bool{}
		for k := range oo {
			keys[k] = true
		}
		for k := range ao {
			keys[k] = true
		}
		for k := range bo {
			keys[k] = true
		}
		get := func(m Object, k string) any {
			if v, ok := m[k]; ok {
				return v
			}
			return missing
		}
		for k := range keys {
			v := merge3(get(oo, k), get(ao, k), get(bo, k))
			if v != missing {
				out[k] = v
			}
		}
		return out
	}
	aa, aok := a.([]any)
	ba, bok := b.([]any)
	if aok && bok {
		oa, _ := o.([]any)
		out := []any{}
		for _, v := range aa {
			if contains(oa, v) && !contains(ba, v) {
				continue // removed on b's side
			}
			out = append(out, v)
		}
		for _, v := range ba {
			if !contains(oa, v) && !contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	return b
}

// MapStrings returns a copy of v with f applied to every string value
// (object keys are left alone).
func MapStrings(v any, f func(string) string) any {
	switch t := v.(type) {
	case Object:
		o := make(Object, len(t))
		for k, val := range t {
			o[k] = MapStrings(val, f)
		}
		return o
	case []any:
		a := make([]any, len(t))
		for i, val := range t {
			a[i] = MapStrings(val, f)
		}
		return a
	case string:
		return f(t)
	default:
		return v
	}
}

// SortedKeys returns the keys of o in sorted order.
func SortedKeys(o Object) []string {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
