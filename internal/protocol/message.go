package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"
)

var ErrProtocol = errors.New("sendspin: protocol violation")

// Message preserves payload bytes, including unknown informational fields.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func Encode(kind string, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Message{Type: kind, Payload: p})
}

func Parse(data []byte) (Message, error) {
	var m Message
	var kind *string
	var fields map[string]json.RawMessage
	if !utf8.Valid(data) || !Object(data) || json.Unmarshal(data, &fields) != nil || json.Unmarshal(fields["type"], &kind) != nil || kind == nil || !Object(fields["payload"]) {
		return m, fmt.Errorf("%w: invalid message envelope", ErrProtocol)
	}
	// An empty or unnamespaced string is still an unrecognized type, not a
	// malformed envelope. The actor applies the initial/re-handshake gates.
	m.Type = *kind
	m.Payload = fields["payload"]
	return m, nil
}

func Object(data []byte) bool {
	b := bytes.TrimSpace(data)
	return len(b) >= 2 && b[0] == '{' && b[len(b)-1] == '}' && json.Valid(b)
}

// Decode enforces the typed fields of a wire model without rejecting unknown
// informational keys. Known fields cannot be null, including optional fields:
// omission and null are different in RC1. Matching is case-sensitive, unlike
// encoding/json's default field matching. RawMessage fields/maps defer validation
// to the handler, including fields with an explicit conditional-ignore rule.
func Decode(data []byte, target any, required ...string) error {
	var fields map[string]json.RawMessage
	if !utf8.Valid(data) || !Object(data) || json.Unmarshal(data, &fields) != nil {
		return ErrProtocol
	}
	for _, name := range required {
		v, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fmt.Errorf("%w: missing %s", ErrProtocol, name)
		}
	}
	normalized, err := normalizeJSON(data, reflect.TypeOf(target))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(normalized, target); err != nil {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return nil
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()
var modelFields sync.Map // reflect.Type -> immutable map[string]reflect.Type

func indirect(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func wireFields(t reflect.Type) map[string]reflect.Type {
	t = indirect(t)
	if cached, ok := modelFields.Load(t); ok {
		return cached.(map[string]reflect.Type)
	}
	fields := make(map[string]reflect.Type)
	collectFields(t, fields, map[reflect.Type]bool{})
	actual, _ := modelFields.LoadOrStore(t, fields)
	return actual.(map[string]reflect.Type)
}

func collectFields(t reflect.Type, fields map[string]reflect.Type, seen map[reflect.Type]bool) {
	t = indirect(t)
	if t == nil || t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	// Flatten untagged embedded wire models first. Explicit outer fields win.
	for i := range t.NumField() {
		field := t.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.Anonymous && tag == "" {
			collectFields(field.Type, fields, seen)
		}
	}
	for i := range t.NumField() {
		field := t.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.PkgPath != "" || tag == "-" || (field.Anonymous && tag == "") {
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		fields[tag] = field.Type
	}
}

func normalizeJSON(data []byte, t reflect.Type) ([]byte, error) {
	t = indirect(t)
	if t == nil {
		return nil, fmt.Errorf("%w: missing decode target", ErrProtocol)
	}
	if t == rawMessageType {
		return data, nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("%w: null is not a typed field value", ErrProtocol)
	}
	switch t.Kind() {
	case reflect.Struct:
		var values map[string]json.RawMessage
		if !Object(data) || json.Unmarshal(data, &values) != nil {
			return nil, ErrProtocol
		}
		fields := wireFields(t)
		for name, value := range values {
			field, known := fields[name]
			if !known {
				delete(values, name)
				continue
			}
			clean, err := normalizeJSON(value, field)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			values[name] = clean
		}
		return json.Marshal(values)
	case reflect.Slice, reflect.Array:
		// encoding/json represents byte slices as base64 strings, not arrays.
		if t.Elem().Kind() == reflect.Uint8 && t.Kind() == reflect.Slice {
			return data, nil
		}
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return nil, ErrProtocol
		}
		for i, value := range values {
			clean, err := normalizeJSON(value, t.Elem())
			if err != nil {
				return nil, err
			}
			values[i] = clean
		}
		return json.Marshal(values)
	default:
		// Primitives and dynamic maps retain JSON's standard type validation.
		return data, nil
	}
}
