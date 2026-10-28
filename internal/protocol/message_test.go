package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestEnvelope(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"payload":{}}`, `{"type":null,"payload":{}}`, `{"type":"x","payload":null}`, `{"type":4,"payload":{}}`, `{"type":"x","payload":[]} trailing`, `{"TYPE":"x","PAYLOAD":{}}`} {
		if _, err := Parse([]byte(raw)); !errors.Is(err, ErrProtocol) {
			t.Errorf("accepted %q: %v", raw, err)
		}
	}
	if _, err := Parse([]byte(`{"type":"future/message","payload":{"future":true}}`)); err != nil {
		t.Fatal(err)
	}
	// RC1 imposes a string type, not a non-empty or slash-separated grammar.
	for _, kind := range []string{"", "future", "  ", "future/message"} {
		data, _ := json.Marshal(map[string]any{"type": kind, "payload": map[string]any{}})
		message, err := Parse(data)
		if err != nil || message.Type != kind {
			t.Errorf("valid unknown type %q rejected: %v", kind, err)
		}
	}
	message, err := Parse([]byte(`{"type":"x","payload":{},"TYPE":"wrong","PAYLOAD":null}`))
	if err != nil || message.Type != "x" || !Object(message.Payload) {
		t.Fatal("unknown envelope keys changed known fields", message, err)
	}
}

func TestTypedDecodeIgnoresOnlyUnknownExactKeys(t *testing.T) {
	type nested struct {
		Count *int `json:"count"`
	}
	type model struct {
		Name     string  `json:"name"`
		Enabled  bool    `json:"enabled"`
		Optional *nested `json:"optional,omitempty"`
		Values   []int   `json:"values,omitempty"`
	}
	for _, data := range []string{
		`{"name":"x","enabled":false,"optional":null}`,
		`{"name":"x","enabled":false,"optional":{"count":null}}`,
		`{"name":"x","enabled":false,"values":[1,null]}`,
		`{"name":"x","enabled":null}`,
		`{"name":"x","enabled":false,"values":null}`,
	} {
		var got model
		if err := Decode([]byte(data), &got, "name", "enabled"); !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted known null field: %s (%v)", data, err)
		}
	}
	var got model
	data := []byte(`{"name":"correct","NAME":"wrong","enabled":false,"Enabled":true,"future":null,"optional":{"count":0,"COUNT":42}}`)
	if err := Decode(data, &got, "name", "enabled"); err != nil {
		t.Fatal(err)
	}
	if got.Name != "correct" || got.Enabled || got.Optional == nil || got.Optional.Count == nil || *got.Optional.Count != 0 {
		t.Fatal("unknown case variant changed typed fields", got)
	}
}

func TestTypedDecodeEmbeddedModelAndRawRoleMap(t *testing.T) {
	type Format struct {
		Codec string `json:"codec"`
		Rate  int    `json:"sample_rate"`
	}
	var embedded struct {
		Format
		Header string `json:"codec_header"`
	}
	if err := Decode([]byte(`{"codec":"pcm","sample_rate":48000,"codec_header":"","Codec":"bad","new_support":null}`), &embedded, "codec", "sample_rate"); err != nil {
		t.Fatal(err)
	}
	if embedded.Codec != "pcm" || embedded.Rate != 48000 {
		t.Fatal(embedded)
	}
	var roles map[string]json.RawMessage
	if err := Decode([]byte(`{"future":null,"metadata":{"title":"x"}}`), &roles); err != nil {
		t.Fatal(err)
	}
	if string(roles["future"]) != "null" {
		t.Fatal("dynamic map was not preserved")
	}
}

func TestDecodeRejectsInvalidUTF8EvenInUnknownFields(t *testing.T) {
	data := append([]byte(`{"name":"valid","unknown":"`), 0xff)
	data = append(data, []byte(`"}`)...)
	var got struct {
		Name string `json:"name"`
	}
	if err := Decode(data, &got, "name"); !errors.Is(err, ErrProtocol) {
		t.Fatal("invalid UTF-8 was accepted", err)
	}
}

func FuzzTypedDecode(f *testing.F) {
	f.Add([]byte(`{"name":"x","optional":{"enabled":true},"values":[1,2]}`))
	f.Add([]byte(`{"name":"x","optional":null}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var model struct {
			Name     string `json:"name"`
			Optional *struct {
				Enabled bool `json:"enabled"`
			} `json:"optional"`
			Values []int `json:"values"`
		}
		if err := Decode(data, &model, "name"); err != nil {
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			t.Fatal("accepted invalid JSON")
		}
		if raw, ok := fields["optional"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			t.Fatal("accepted null optional field")
		}
	})
}
func TestFragmentRoundTrip(t *testing.T) {
	for _, n := range []int{1, MaxPlaintext, MaxPlaintext + 1, 2 * MaxPlaintext, 200000} {
		in := bytes.Repeat([]byte{42}, n)
		in[0] = 0
		var r Reassembler
		r.Limit = 300000
		var out []byte
		err := Fragment(in, func(frame []byte) error {
			var err error
			out, err = r.Push(frame, func(byte) bool { return true })
			return err
		})
		if err != nil || !bytes.Equal(out, in) {
			t.Fatalf("size %d: %v", n, err)
		}
	}
}
func TestMalformedFragments(t *testing.T) {
	for _, seq := range [][][]byte{
		{{1}}, {{1, 0}}, {{1, 2}}, {{1, 3, 1}}, {{1, 4}}, {{1, 2, 0}, {4}}, {{1, 2, 0}, {1, 2, 0}}, {{1, 2, 0}, {1, 0, 1, 2, 3, 4}},
	} {
		r := Reassembler{Limit: 3}
		bad := false
		for _, frame := range seq {
			if _, err := r.Push(frame, func(byte) bool { return true }); err != nil {
				bad = true
				break
			}
		}
		if !bad {
			t.Errorf("accepted %v", seq)
		}
	}
}
func TestUnknownFragmentStillTracksSequence(t *testing.T) {
	r := Reassembler{Limit: 100}
	if out, err := r.Push([]byte{1, 2, 240, 1, 2}, func(byte) bool { return false }); err != nil || out != nil {
		t.Fatal(out, err)
	}
	out, err := r.Push([]byte{1, 1, 3}, func(byte) bool { return false })
	if err != nil || !bytes.Equal(out, []byte{240}) {
		t.Fatal(out, err)
	}
}
func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"type":"x","payload":{}}`))
	f.Add([]byte{255})
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := Parse(b)
		if err == nil {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(b, &fields)
			var kind *string
			if !Object(m.Payload) || json.Unmarshal(fields["type"], &kind) != nil || kind == nil || *kind != m.Type {
				t.Fatal("invalid accepted envelope")
			}
		}
	})
}
func FuzzFragments(f *testing.F) {
	f.Add([]byte{1, 3, 0, 1})
	f.Fuzz(func(t *testing.T, b []byte) {
		r := Reassembler{Limit: 4096}
		for i := 0; i < len(b); {
			n := min(int(b[i])+1, len(b)-i)
			_, err := r.Push(b[i:i+n], func(byte) bool { return true })
			if err != nil {
				return
			}
			i += n
		}
	})
}
