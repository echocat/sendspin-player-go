package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/diagnostic"
)

func validTrace() *recorder {
	r := newRecorder()
	for _, entry := range []struct {
		kind, payload string
		sent          bool
	}{
		{"client/init", `{}`, true}, {"server/init", `{}`, false}, {"noise/handshake", `{}`, false}, {"noise/handshake", `{}`, true},
		{"server/hello", `{}`, false}, {"client/hello", `{"supported_roles":["player@v1"],"player@v1_support":{"supported_formats":[{"codec":"pcm","sample_rate":48000,"channels":2,"bit_depth":16}]}}`, true},
		{"server/activate", `{"active_roles":["player@v1"],"activities":["playback"]}`, false}, {"client/state", `{"available":true}`, true},
		{"stream/start", `{"player":{"codec":"pcm","sample_rate":48000,"channels":2,"bit_depth":16}}`, false},
	} {
		message := diagnostic.Message{Type: entry.kind, Payload: json.RawMessage(entry.payload), Direction: diagnostic.Received, Transport: "text"}
		if entry.sent {
			message.Direction = diagnostic.Sent
		}
		if len(r.messages) >= 4 {
			message.Transport = "binary"
			message.Encrypted = true
			message.HasMessageID = true
		}
		r.observe(message)
	}
	header := make([]byte, 13)
	header[0] = 4
	binary.BigEndian.PutUint64(header[1:9], 42)
	r.observe(diagnostic.Message{Type: "binary", Direction: diagnostic.Received, Transport: "binary", Encrypted: true, HasMessageID: true, MessageID: 4, Prefix: header, PlaintextBytes: 17})
	r.audio(diagnostic.AudioChunk{Timestamp: 42, Payload: []byte{1, 2, 3, 4}})
	return r
}
func verdict(r *recorder, id string) string {
	return r.protocolEvidence()["assertions"].(map[string]any)[id].(map[string]any)["status"].(string)
}

func TestEvidenceRequiresActualObservations(t *testing.T) {
	ids := []string{"CORE-001", "CORE-002", "CORE-003", "CORE-004", "PLAYER-001"}
	for _, id := range ids {
		if verdict(newRecorder(), id) != "failed" {
			t.Fatalf("empty trace passed %s", id)
		}
		if verdict(validTrace(), id) != "passed" {
			t.Fatalf("valid trace failed %s", id)
		}
	}
	for _, test := range []struct {
		name, id   string
		breakTrace func(*recorder)
	}{
		{"order", "CORE-001", func(r *recorder) { r.messages[4], r.messages[5] = r.messages[5], r.messages[4] }},
		{"unencrypted", "CORE-001", func(r *recorder) { r.messages[4].Encrypted = false }},
		{"direction", "CORE-001", func(r *recorder) { r.messages[0].Direction = diagnostic.Received }},
		{"early application", "CORE-002", func(r *recorder) {
			r.messages = append(r.messages[:6], append([]diagnostic.Message{{Type: "client/time"}}, r.messages[6:]...)...)
		}},
		{"unadvertised role", "CORE-003", func(r *recorder) { r.messages[6].Payload = json.RawMessage(`{"active_roles":["source@v1"]}`) }},
		{"duplicate family", "CORE-003", func(r *recorder) {
			r.messages[6].Payload = json.RawMessage(`{"active_roles":["player@v1","player@v1"]}`)
		}},
		{"null support", "CORE-003", func(r *recorder) {
			r.messages[5].Payload = json.RawMessage(`{"supported_roles":["player@v1"],"player@v1_support":null}`)
		}},
		{"no initial state", "CORE-004", func(r *recorder) { r.messages = append(r.messages[:7], r.messages[8:]...) }},
		{"reactivation needs state", "CORE-004", func(r *recorder) {
			r.messages = append(r.messages[:9], append([]diagnostic.Message{r.messages[6]}, r.messages[9:]...)...)
		}},
		{"no stream", "PLAYER-001", func(r *recorder) { r.messages = append(r.messages[:8], r.messages[9:]...) }},
		{"unadvertised format", "PLAYER-001", func(r *recorder) {
			r.messages[8].Payload = json.RawMessage(`{"player":{"codec":"pcm","sample_rate":1,"channels":2,"bit_depth":16}}`)
		}},
		{"short header", "PLAYER-001", func(r *recorder) { r.messages[9].Prefix = r.messages[9].Prefix[:9] }},
		{"negative timestamp", "PLAYER-001", func(r *recorder) { r.messages[9].Prefix[1] = 0xff }},
		{"backwards timestamp", "PLAYER-001", func(r *recorder) {
			message := r.messages[9]
			message.Prefix = append([]byte(nil), message.Prefix...)
			binary.BigEndian.PutUint64(message.Prefix[1:9], 41)
			r.messages = append(r.messages, message)
		}},
		{"no payload observation", "PLAYER-001", func(r *recorder) { r.chunks = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := validTrace()
			test.breakTrace(r)
			if verdict(r, test.id) != "failed" {
				t.Fatalf("bad trace passed %s", test.id)
			}
		})
	}
	for _, test := range []struct {
		name       string
		breakTrace func(*recorder)
	}{
		{"truncated", func(r *recorder) { r.messages[5].Truncated = true }},
		{"fragment", func(r *recorder) { r.messages[5].Reassembled = true }},
		{"record loss", func(r *recorder) { r.failure = "lost observation" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := validTrace()
			test.breakTrace(r)
			for _, id := range ids {
				if verdict(r, id) != "failed" {
					t.Fatalf("incomplete trace passed %s", id)
				}
			}
		})
	}
}

func TestPayloadPairingRejectsAmbiguousTimestamps(t *testing.T) {
	r := validTrace()
	if r.frames[0].Payload == nil || *r.frames[0].Payload != 4 {
		t.Fatal("actual slice length was not paired")
	}
	r.observe(r.messages[len(r.messages)-1])
	r.audio(diagnostic.AudioChunk{Timestamp: 42, Payload: []byte{5, 6}})
	if r.failure == "" {
		t.Fatal("ambiguous timestamps accepted")
	}
	for _, frame := range r.frames {
		if frame.Payload != nil {
			t.Fatal("ambiguous payload size reported")
		}
	}
	r = newRecorder()
	r.audio(diagnostic.AudioChunk{Timestamp: 42, Payload: []byte{1}})
	if r.failure == "" {
		t.Fatal("unobserved header accepted")
	}
}

func TestRegistryIsLoopbackOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	for _, endpoint := range []string{"ws://127.0.0.1:123/sendspin", "ws://[::1]:123/sendspin", "ws://192.0.2.1/sendspin", "ws://localhost/sendspin", "wss://127.0.0.1/sendspin", "ws://user@127.0.0.1/sendspin", "broken"} {
		if err := registerEndpoint(path, "server", endpoint); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		got, err := waitEndpoint(ctx, path, "server")
		cancel()
		allowed := endpoint == "ws://127.0.0.1:123/sendspin" || endpoint == "ws://[::1]:123/sendspin"
		if allowed != (err == nil) || (allowed && got != endpoint) {
			t.Fatalf("endpoint %s: %s, %v", endpoint, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitEndpoint(ctx, path, "absent"); err == nil {
		t.Fatal("lookup ignored cancellation")
	}
}

func TestDeterministicHarnessCredentials(t *testing.T) {
	o := options{id: "client", serverID: "server"}
	first, err := credentials(o)
	if err != nil {
		t.Fatal(err)
	}
	second, err := credentials(o)
	if err != nil {
		t.Fatal(err)
	}
	if first.ClientID() != second.ClientID() || first.Records[0].ServerID != second.Records[0].ServerID || first.Records[0].PSK != second.Records[0].PSK {
		t.Fatal("harness credentials differ")
	}
	// Frozen independent Python hashlib/X25519 results, not SDK predictions.
	if first.ClientID() != "gr-xeag6-CkXy-28MZgTAJo3OmOPr9vrPKTS_L2tQF4" || first.Records[0].ServerID != "q9vLOJrQSaCEnZ5hFOxGuF-Gom_unbx8MV2jj2uvt2w" || fmt.Sprintf("%x", first.Records[0].PSK[:]) != "9c500f81d7a75f1652b804c3dc87e5c263c3f8df6e379c22117fd973f2e24e39" {
		t.Fatalf("client identity drift: %s", first.ClientID())
	}
}

func TestUnknownScenarioDoesNotSucceed(t *testing.T) {
	for _, scenario := range []string{"unknown", "unknown-metadata", "legacy-server-initiated-pcm"} {
		if _, _, err := scenarioConfig(options{scenario: scenario}); err == nil {
			t.Fatalf("unsupported %s accepted", scenario)
		}
	}
	directory := t.TempDir()
	o := options{scenario: "unknown", summary: filepath.Join(directory, "summary.json"), ready: filepath.Join(directory, "ready.json"), timeout: 1}
	if err := run(o); err == nil {
		t.Fatal("unknown scenario succeeded")
	}
	for _, timeout := range []string{"NaN", "Inf", "-1", "301"} {
		args := strings.Fields("--client-name a --client-id a --server-id b --summary s --ready r --registry e --timeout-seconds " + timeout)
		if _, err := parse(args); err == nil {
			t.Fatalf("invalid timeout %s accepted", timeout)
		}
	}
}
