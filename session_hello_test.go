package sendspin

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func TestServerHelloKnownFieldsAreValidatedBeforeReply(t *testing.T) {
	for _, payload := range []string{
		`{"name":"server","languages":null}`,
		`{"name":"server","languages":[]}`,
		`{"name":"server","languages":"de"}`,
		`{"name":"server","languages":[null]}`,
		`{"name":"server","languages":[4]}`,
		`{"name":"server","languages":[""]}`,
		`{"name":"server","languages":["de_DE"]}`,
		`{"name":"server","languages":["en","bad--tag"]}`,
		`{"name":"server","source@v1_support":null}`,
		`{"name":"server","source@v1_support":[]}`,
		`{"name":"server","source@v1_support":"unused"}`,
		`{"name":null}`,
		`{"name":4}`,
		`{}`,
	} {
		t.Run(payload, func(t *testing.T) {
			s := mediaSession(t)
			s.hello, s.activated, s.admitted = false, false, false
			// No transport keys/socket exist: validation must return before a
			// client/hello reply or any state is accepted from the malformed peer.
			frame := append([]byte{0}, []byte(`{"type":"server/hello","payload":`+payload+`}`)...)
			if err := s.dispatch(frame); !errors.Is(err, protocol.ErrProtocol) {
				t.Fatal("malformed hello accepted", err)
			}
			if s.hello || s.state.ServerID != "" || s.state.ServerName != "" {
				t.Fatal("invalid hello changed session state")
			}
		})
	}
}

func TestServerHelloPreservesLanguageHintsAndIgnoresUnknownFields(t *testing.T) {
	for _, payload := range []string{
		`{"name":"server"}`,
		`{"name":"","languages":["DE-de","i-klingon","x-custom","zz-Zzzz-999-future1","DE-de"]}`,
		`{"name":"server","languages":["de"],"LANGUAGES":null,"NAME":[],"future_support":false}`,
		`{"name":"server","source@v1_support":{},"future":null}`,
		`{"name":"server","source@v1_support":{"supported_formats":null,"future":{"anything":false}}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			hello, err := decodeServerHello([]byte(payload))
			if err != nil {
				t.Fatal("valid hello rejected", err)
			}
			var original map[string]json.RawMessage
			if err = json.Unmarshal([]byte(payload), &original); err != nil {
				t.Fatal(err)
			}
			var name string
			var languages []string
			_ = json.Unmarshal(original["name"], &name)
			if raw, ok := original["languages"]; ok {
				_ = json.Unmarshal(raw, &languages)
			}
			if hello.Name != name || !slices.Equal(hello.Languages, languages) {
				t.Fatal("hello values were rewritten or unknown case variants took effect")
			}
			if hello.SourceSupport != nil {
				t.Fatal("unused source body retained")
			}
		})
	}
}

func TestServerLanguagesSnapshotDoesNotAliasAndSurvivesRoleChanges(t *testing.T) {
	s := mediaSession(t)
	s.client.active = s
	s.state.ServerLanguages = []string{"DE-de", "i-klingon", "DE-de"}
	s.publish()
	first := s.client.Snapshot()
	if !slices.Equal(first.ServerLanguages, s.state.ServerLanguages) {
		t.Fatal("snapshot lost language preference order")
	}
	first.ServerLanguages[0] = "modified"
	s.clearRole("metadata")
	s.restartTimeExchange()
	s.publish()
	if !slices.Equal(s.client.Snapshot().ServerLanguages, []string{"DE-de", "i-klingon", "DE-de"}) {
		t.Fatal("consumer/role/clock operation changed immutable hello preferences")
	}
	s.client.active = nil
	if s.client.Snapshot().ServerLanguages != nil {
		t.Fatal("disconnected read model retained the old server's preferences")
	}
}
