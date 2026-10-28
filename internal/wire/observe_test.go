package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/diagnostic"
)

func TestObservationProjectsOnlyKnownFields(t *testing.T) {
	for _, test := range []struct{ body, kind, want string }{
		{`{"type":"client/init","payload":{"client_id":"public","version":1,"suite":"public","psk":"SECRET","extension":{"key":"SECRET"}}}`, "client/init", `{"client_id":"public","suite":"public","version":1}`},
		{`{"type":"noise/handshake","payload":{"data":"SECRET","future":"SECRET"}}`, "noise/handshake", ""},
		{`{"type":"client/pair-psk","payload":{"psk":"SECRET"}}`, "redacted/control", ""},
		{`{"type":"server/pair-future","payload":{"secret":"SECRET"}}`, "redacted/control", ""},
		{`{"type":"pair/abort","payload":{"reason":"SECRET"}}`, "redacted/control", ""},
		{`{"type":"unknown-SECRET","payload":{"data":"SECRET"}}`, "unknown", ""},
		{`{"type":"client/state","payload":{"available":true,"player":{"volume":50,"format":{"codec":"pcm","psk":"SECRET"},"key":"SECRET"},"PLAYER":{"volume":"SECRET"}}}`, "client/state", `{"available":true,"player":{"format":{"codec":"pcm"},"volume":50}}`},
		{`{"type":"client/hello","payload":{"supported_roles":["player@v1"],"player@v1_support":{"supported_formats":[{"codec":"pcm","secret":"SECRET"}],"secret":"SECRET"},"pairing_methods":[{"secret":"SECRET"}]}}`, "client/hello", `{"player@v1_support":{"supported_formats":[{"codec":"pcm"}]},"supported_roles":["player@v1"]}`},
		{`{"type":"server/state","payload":{"metadata":{"timestamp":42,"title":"SECRET"},"color":null}}`, "server/state", `{"color":null,"metadata":{"timestamp":42}}`},
		{`{"type":"client/init","payload":{"version":"SECRET","client_id":{"secret":"SECRET"},"suite":[]}}`, "client/init", `{"client_id":null,"suite":null,"version":null}`},
		{`SECRET not JSON`, "malformed", ""},
	} {
		t.Run(test.kind+test.want, func(t *testing.T) {
			var got diagnostic.Message
			c := Channel{observer: func(m diagnostic.Message) { got = m }}
			raw := []byte(test.body)
			at := time.Now()
			c.observeJSON(raw, diagnostic.Received, "binary", true, false, at)
			if got.Type != test.kind || string(got.Payload) != test.want || got.PlaintextBytes != len(raw)+1 || !got.HasMessageID || got.MessageID != 0 || got.At != at || !got.Encrypted {
				t.Fatalf("unexpected observation: %+v", got)
			}
			if strings.Contains(string(got.Payload), "SECRET") || strings.Contains(got.Type, "SECRET") {
				t.Fatal("secret leaked")
			}
			before := bytes.Clone(got.Payload)
			clear(raw)
			if !bytes.Equal(before, got.Payload) {
				t.Fatal("observation aliases raw input")
			}
		})
	}
}

func TestObservationBoundsAndBinaryOwnership(t *testing.T) {
	var events []diagnostic.Message
	c := Channel{observer: func(m diagnostic.Message) { events = append(events, m) }}
	long, _ := json.Marshal(strings.Repeat("x", 4097))
	c.observeJSON(append(append([]byte(`{"type":"server/hello","payload":{"name":`), long...), []byte(`}}`)...), diagnostic.Received, "binary", true, false, time.Now())
	if !events[0].Truncated || bytes.Contains(events[0].Payload, long) {
		t.Fatal("long field was not bounded")
	}
	roles := make([]string, 257)
	payload, _ := json.Marshal(map[string]any{"type": "server/activate", "payload": map[string]any{"active_roles": roles}})
	c.observeJSON(payload, diagnostic.Received, "binary", true, false, time.Now())
	if !events[1].Truncated {
		t.Fatal("long array was not bounded")
	}
	for _, id := range []byte{1, 4, 8, 16, 255} {
		frame := append([]byte{id}, bytes.Repeat([]byte{0xaa}, 100)...)
		binary.BigEndian.PutUint64(frame[1:9], 42)
		c.observeFrame(frame, diagnostic.Received, false, time.Now())
		got := events[len(events)-1]
		want := 1
		if id == 4 {
			want = 13
		}
		if len(got.Prefix) != want || got.PlaintextBytes != len(frame) || !got.HasMessageID || got.MessageID != id || len(got.Payload) != 0 {
			t.Fatalf("binary leaked: %+v", got)
		}
		got.Prefix[0] ^= 0xff
		if frame[0] != id {
			t.Fatal("observer could mutate transport buffer")
		}
	}
	for size := 0; size < 14; size++ {
		c.observeFrame(bytes.Repeat([]byte{4}, size), diagnostic.Received, false, time.Now())
	}
}

func TestDisabledObserverDoesNotAllocate(t *testing.T) {
	c := Channel{}
	frame := []byte(`{"type":"server/hello","payload":{"name":"name"}}`)
	if got := testing.AllocsPerRun(100, func() {
		c.observeJSON(frame, diagnostic.Received, "binary", true, false, time.Time{})
		c.observeFrame(frame, diagnostic.Received, false, time.Time{})
	}); got != 0 {
		t.Fatalf("disabled observer allocated %v", got)
	}
}
