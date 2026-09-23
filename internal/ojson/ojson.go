// Package ojson is JSON with insertion-ordered objects, so resolved stubs print in the same
// key order their authors wrote them (jq's behaviour, which test.sh relied on).
package ojson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Object is an ordered JSON object. Values are nil, bool, json.Number, string, []any or *Object.
type Object struct {
	keys []string
	vals map[string]any
}

func NewObject() *Object { return &Object{vals: map[string]any{}} }

func (o *Object) Keys() []string { return o.keys }
func (o *Object) Len() int       { return len(o.keys) }

func (o *Object) Get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *Object) Has(k string) bool { _, ok := o.vals[k]; return ok }

func (o *Object) Set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *Object) String(k string) string {
	if v, ok := o.vals[k].(string); ok {
		return v
	}
	return ""
}

func (o *Object) Object(k string) *Object {
	if v, ok := o.vals[k].(*Object); ok {
		return v
	}
	return nil
}

func (o *Object) Clone() *Object { return Clone(o).(*Object) }

func Clone(v any) any {
	switch t := v.(type) {
	case *Object:
		c := NewObject()
		for _, k := range t.keys {
			c.Set(k, Clone(t.vals[k]))
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i := range t {
			c[i] = Clone(t[i])
		}
		return c
	default:
		return v
	}
}

// Merge is jq's `base * own`: objects merge recursively, everything else is replaced.
func Merge(base, own any) any {
	bo, bok := base.(*Object)
	oo, ook := own.(*Object)
	if !bok || !ook {
		return Clone(own)
	}
	out := bo.Clone()
	for _, k := range oo.keys {
		if existing, ok := out.vals[k]; ok {
			out.Set(k, Merge(existing, oo.vals[k]))
		} else {
			out.Set(k, Clone(oo.vals[k]))
		}
	}
	return out
}

func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decode(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

func ParseFile(path string) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func ParseObjectFile(path string) (*Object, error) {
	v, err := ParseFile(path)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, fmt.Errorf("top-level JSON is not an object")
	}
	return o, nil
}

func decode(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := decode(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decode(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return tok, nil
	}
}

func (o *Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := marshalValue(o.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshalValue(v any) ([]byte, error) {
	enc := &bytes.Buffer{}
	e := json.NewEncoder(enc)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(enc.Bytes(), "\n"), nil
}

// Marshal is compact, order-preserving and does not HTML-escape.
func Marshal(v any) ([]byte, error) { return marshalValue(v) }

func MarshalIndent(v any) ([]byte, error) {
	b, err := marshalValue(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Number helpers.
func AsFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case int:
		return float64(t), true
	}
	return 0, false
}

func IsInt(v any) bool {
	f, ok := AsFloat(v)
	return ok && f == float64(int64(f))
}
