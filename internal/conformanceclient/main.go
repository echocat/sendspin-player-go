// Command conformanceclient adapts the public SDK to Sendspin/conformance.
// Deterministic credentials are for isolated tests only, never SDK defaults.
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
)

const implementation = "sendspin-player-go"
const assertionRevision = "8c9577ea8719ad082d051ec13cc73ef15ed68948"

type options struct {
	name, id, serverID, serverName, summary, ready, registry, scenario, initiator, preferred, path string
	port                                                                                           int
	timeout                                                                                        float64
	controller, artFormat                                                                          string
	artWidth, artHeight                                                                            int
}

func parse(args []string) (options, error) {
	var o options
	f := flag.NewFlagSet("conformanceclient", flag.ContinueOnError)
	for _, entry := range []struct {
		target      *string
		name, value string
	}{
		{&o.name, "client-name", ""}, {&o.id, "client-id", ""}, {&o.serverID, "server-id", ""}, {&o.serverName, "server-name", "Sendspin Conformance Server"},
		{&o.summary, "summary", ""}, {&o.ready, "ready", ""}, {&o.registry, "registry", ""}, {&o.scenario, "scenario-id", "client-initiated-pcm"},
		{&o.initiator, "initiator-role", "client"}, {&o.preferred, "preferred-codec", "pcm"}, {&o.path, "path", "/sendspin"},
		{&o.controller, "controller-command", "next"}, {&o.artFormat, "artwork-format", "jpeg"},
	} {
		f.StringVar(entry.target, entry.name, entry.value, entry.name)
	}
	f.IntVar(&o.port, "port", 8928, "Listener port (loopback only)")
	f.Float64Var(&o.timeout, "timeout-seconds", 30, "Case timeout")
	f.IntVar(&o.artWidth, "artwork-width", 256, "Artwork width")
	f.IntVar(&o.artHeight, "artwork-height", 256, "Artwork height")
	// Harness stimulus flags are accepted, but never used to fabricate received
	// metadata/controller state. Summaries contain only SDK-observed values.
	for _, name := range []string{"metadata-title", "metadata-artist", "metadata-album-artist", "metadata-album", "metadata-artwork-url", "metadata-year", "metadata-track", "metadata-track-progress", "metadata-track-duration", "metadata-playback-speed", "controller-repeat", "controller-shuffle"} {
		f.String(name, "", "Server stimulus (not an expected client value)")
	}
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || o.name == "" || o.id == "" || o.serverID == "" || o.summary == "" || o.ready == "" || o.registry == "" || o.path != "/sendspin" || math.IsNaN(o.timeout) || math.IsInf(o.timeout, 0) || o.timeout <= 0 || o.timeout > 300 || o.port < 0 || o.port > 65535 || (o.initiator != "server" && o.initiator != "client") {
		return o, errors.New("invalid/incomplete adapter arguments")
	}
	return o, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".conformance-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func waitEndpoint(ctx context.Context, path, name string) (string, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			var registry map[string]struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(data, &registry) == nil && registry[name].URL != "" {
				endpoint := registry[name].URL
				u, err := url.Parse(endpoint)
				if err != nil || u.Scheme != "ws" || u.User != nil || !net.ParseIP(u.Hostname()).IsLoopback() {
					return "", errors.New("adapter refuses non-loopback registry endpoint")
				}
				return endpoint, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
func registerEndpoint(path, name, endpoint string) error {
	registry := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(data, &registry); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	registry[name] = map[string]string{"url": endpoint}
	return writeJSON(path, registry)
}

func credentials(o options) (sendspin.Credentials, error) {
	credentials, err := sendspin.NewCredentials()
	if err != nil {
		return credentials, err
	}
	private := sha256.Sum256([]byte("sendspin-conformance-identity:client:" + o.id))
	credentials.PrivateKey = sendspin.Secret(private)
	serverPrivate := sha256.Sum256([]byte("sendspin-conformance-identity:server:" + o.serverID))
	key, err := ecdh.X25519().NewPrivateKey(serverPrivate[:])
	if err != nil {
		return credentials, err
	}
	psk := sha256.Sum256([]byte("sendspin-conformance-psk:" + o.serverID + ":" + o.id))
	credentials.Records = []sendspin.PairingRecord{{ServerID: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), PSK: sendspin.Secret(psk)}}
	return credentials, nil
}

type sink struct{}
type sinkDevice struct {
	format audio.Format
	next   time.Time
}

func (sink) Open(_ context.Context, format audio.Format) (audio.Device, error) {
	return &sinkDevice{format: format}, nil
}
func (d *sinkDevice) Next(ctx context.Context) (time.Time, error) {
	if d.next.Before(time.Now()) {
		d.next = time.Now()
	}
	return d.next, ctx.Err()
}
func (d *sinkDevice) Write(ctx context.Context, data []float32) error {
	d.next = d.next.Add(time.Duration(len(data)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
	if delay := time.Until(d.next) - 40*time.Millisecond; delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
func (d *sinkDevice) Flush(context.Context) error { d.next = time.Time{}; return nil }
func (d *sinkDevice) Close() error                { return nil }

type frameRecord struct {
	Bytes     int    `json:"byte_count"`
	Leading   string `json:"leading_hex"`
	Payload   *int   `json:"payload_byte_count"`
	timestamp int64
	audio     bool
}
type recorder struct {
	mu                            sync.Mutex
	messages                      []diagnostic.Message
	frames                        []*frameRecord
	encoded, pcm                  hash.Hash
	encodedBytes, samples, chunks int
	starts                        []map[string]any
	hello, peer                   map[string]any
	metadataUpdates               int
	firstMetadata                 map[string]any
	metadata                      *sendspin.Metadata
	controller                    *sendspin.ControllerState
	sentCommand                   *sendspin.Command
	artwork                       *sendspin.Artwork
	requested                     *codec.Format
	failure                       string
}

func newRecorder() *recorder { return &recorder{encoded: sha256.New(), pcm: sha256.New()} }
func messageMap(event diagnostic.Message) map[string]any {
	var payload any
	_ = json.Unmarshal(event.Payload, &payload)
	return map[string]any{"type": event.Type, "payload": payload}
}
func (r *recorder) observe(event diagnostic.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) >= 4096 {
		r.failure = "observer record limit exceeded"
		return
	}
	if event.Truncated {
		r.failure = "diagnostic projection was truncated"
	}
	r.messages = append(r.messages, event)
	if event.Type == "client/hello" {
		r.hello = messageMap(event)
	}
	if event.Type == "server/hello" {
		r.peer = messageMap(event)
	}
	if event.Direction == diagnostic.Received && event.Encrypted && event.HasMessageID && event.MessageID != 0 {
		frame := &frameRecord{Bytes: event.PlaintextBytes, Leading: hex.EncodeToString(event.Prefix)}
		if event.MessageID == 4 && len(event.Prefix) >= 9 {
			frame.timestamp = int64(binary.BigEndian.Uint64(event.Prefix[1:9]))
			frame.audio = len(event.Prefix) == 13
		}
		if event.Reassembled || event.MessageID == 1 {
			r.failure = "framing evidence cannot equate transport fragments with logical audio chunks"
		}
		r.frames = append(r.frames, frame)
	}
	if event.Type == "stream/start" && event.Direction == diagnostic.Received {
		var payload struct {
			Player map[string]any `json:"player"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Player != nil {
			r.starts = append(r.starts, payload.Player)
		}
	}
	if event.Type == "server/state" && event.Direction == diagnostic.Received {
		var payload map[string]json.RawMessage
		_ = json.Unmarshal(event.Payload, &payload)
		if data, ok := payload["metadata"]; ok {
			r.metadataUpdates++
			if string(data) != "null" && r.firstMetadata == nil {
				var m map[string]any
				_ = json.Unmarshal(data, &m)
				r.firstMetadata = map[string]any{"update_index": r.metadataUpdates, "timestamp_us": m["timestamp"]}
			}
		}
	}
}
func (r *recorder) audio(chunk diagnostic.AudioChunk) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunks++
	r.encodedBytes += len(chunk.Payload)
	_, _ = r.encoded.Write(chunk.Payload)
	matched := false
	var matches []*frameRecord
	for _, frame := range r.frames {
		if frame.audio && frame.timestamp == chunk.Timestamp {
			matches = append(matches, frame)
		}
	}
	if len(matches) > 1 {
		for _, frame := range matches {
			frame.Payload = nil
		}
		r.failure = "duplicate audio timestamp cannot pair payloads unambiguously"
		return
	}
	for _, frame := range matches {
		if frame.Payload == nil {
			size := len(chunk.Payload)
			frame.Payload = &size
			matched = true
			break
		}
	}
	if !matched {
		r.failure = "encoded payload had no matching observed transport frame"
	}
}
func (r *recorder) decoded(_ codec.Format, data []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples += len(data)
	var bytes [4]byte
	for _, sample := range data {
		binary.LittleEndian.PutUint32(bytes[:], math.Float32bits(sample))
		_, _ = r.pcm.Write(bytes[:])
	}
}

func scenarioConfig(o options) ([]sendspin.Role, []codec.Format, error) {
	if strings.Contains(o.scenario, "legacy") {
		return nil, nil, errors.New("legacy unencrypted scenarios are unsupported")
	}
	if o.scenario == "client-initiated-metadata" || o.scenario == "server-initiated-metadata" {
		return []sendspin.Role{sendspin.MetadataRole}, nil, nil
	}
	if o.scenario == "client-initiated-controller" || o.scenario == "server-initiated-controller" {
		if o.controller != "next" {
			return nil, nil, errors.New("adapter only drives the next controller command")
		}
		return []sendspin.Role{sendspin.Controller}, nil, nil
	}
	if o.scenario == "client-initiated-artwork" || o.scenario == "server-initiated-artwork" {
		return []sendspin.Role{sendspin.ArtworkRole}, nil, nil
	}
	var formats []codec.Format
	if strings.Contains(o.scenario, "state-format") {
		formats = []codec.Format{{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 24}, {Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 16}}
		if strings.HasSuffix(o.scenario, "flac") {
			formats = []codec.Format{{Codec: "pcm", SampleRate: 8000, Channels: 1, BitDepth: 16}, {Codec: "flac", SampleRate: 8000, Channels: 1, BitDepth: 16}}
		}
	} else if o.preferred == "flac" || o.preferred == "opus" {
		formats = []codec.Format{{Codec: o.preferred, SampleRate: 48000, Channels: 2, BitDepth: 16}}
		if o.preferred == "opus" {
			formats = append(formats, codec.Format{Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 16})
		}
	} else {
		depth := 16
		if strings.HasSuffix(o.scenario, "24bit") {
			depth = 24
		}
		formats = []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: depth}}
	}
	known := []string{"client-initiated-pcm", "server-initiated-pcm", "server-initiated-pcm-24bit", "server-initiated-flac", "server-initiated-opus", "server-initiated-protocol-baseline-v1", "server-initiated-audio-chunk-framing", "client-initiated-state-format-pcm", "client-initiated-state-format-flac"}
	if !slices.Contains(known, o.scenario) {
		return nil, nil, errors.New("unsupported player scenario")
	}
	return []sendspin.Role{sendspin.Player}, formats, nil
}

func run(o options) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(o.timeout*float64(time.Second)))
	defer cancel()
	record := newRecorder()
	summary := map[string]any{"implementation": implementation, "role": "client", "scenario_id": o.scenario, "initiator_role": o.initiator, "specification": sendspin.Specification}
	finish := func(err error) error {
		record.mu.Lock()
		defer record.mu.Unlock()
		for key, value := range record.summary() {
			summary[key] = value
		}
		if err != nil {
			summary["status"] = "error"
			summary["reason"] = err.Error()
		} else {
			summary["status"] = "ok"
		}
		if writeErr := writeJSON(o.summary, summary); writeErr != nil {
			return writeErr
		}
		return err
	}
	roles, formats, err := scenarioConfig(o)
	if err != nil {
		_ = writeJSON(o.ready, map[string]string{"status": "error"})
		return finish(err)
	}
	creds, err := credentials(o)
	if err != nil {
		return finish(err)
	}
	store := &sendspin.MemoryStore{}
	if err = store.Save(ctx, creds); err != nil {
		return finish(err)
	}
	config := sendspin.Config{Name: o.name, Store: store, Roles: roles, Formats: formats, Output: sink{}, DisableDiscovery: true, Observe: record.observe, Encoded: record.audio, Decoded: record.decoded, RequiredLeadTime: 250 * time.Millisecond, MinBuffer: 100 * time.Millisecond}
	if roles[0] == sendspin.ArtworkRole {
		config.Artwork = []sendspin.ArtworkChannel{{Source: "album", Format: o.artFormat, Width: o.artWidth, Height: o.artHeight}}
	}
	client, err := sendspin.New(ctx, config)
	if err != nil {
		return finish(err)
	}
	done := make(chan error, 1)
	if o.initiator == "server" {
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(o.port)))
		if err != nil {
			return finish(err)
		}
		if err = registerEndpoint(o.registry, o.name, "ws://"+listener.Addr().String()+o.path); err != nil {
			_ = listener.Close()
			return finish(err)
		}
		go func() { done <- client.Serve(ctx, listener) }()
		if err = writeJSON(o.ready, map[string]any{"status": "ready", "scenario_id": o.scenario, "initiator_role": o.initiator, "url": "ws://" + listener.Addr().String() + o.path}); err != nil {
			cancel()
			<-done // Cleanup: preserve the ready-file error.
			return finish(err)
		}
	} else {
		if err = writeJSON(o.ready, map[string]any{"status": "ready", "scenario_id": o.scenario, "initiator_role": o.initiator}); err != nil {
			return finish(err)
		}
		endpoint, err := waitEndpoint(ctx, o.registry, o.serverName)
		if err != nil {
			return finish(err)
		}
		go func() { done <- client.Connect(ctx, endpoint) }()
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	poll := func() error {
		state := client.Snapshot()
		record.mu.Lock()
		if state.Metadata != nil {
			record.metadata = state.Metadata
		}
		if state.Controller != nil {
			record.controller = state.Controller
		}
		needCommand := state.Controller != nil && record.sentCommand == nil && state.Controller.Repeat == "all" && !state.Controller.Shuffle && slices.Contains(state.Controller.SupportedCommands, o.controller)
		needFormat := strings.Contains(o.scenario, "state-format") && len(record.starts) > 0 && record.requested == nil
		record.mu.Unlock()
		if image, ok := client.CurrentArtwork(0); ok {
			record.mu.Lock()
			record.artwork = &image
			record.mu.Unlock()
		}
		if needCommand {
			command := sendspin.Command{Command: o.controller}
			if err := client.Command(ctx, command); err != nil {
				return err
			}
			record.mu.Lock()
			record.sentCommand = &command
			record.mu.Unlock()
		}
		if needFormat {
			if err := client.PreferFormat(ctx, formats[1]); err != nil {
				return err
			}
			target := formats[1]
			record.mu.Lock()
			record.requested = &target
			record.mu.Unlock()
		}
		return nil
	}
	var sessionErr error
loop:
	for {
		select {
		case <-ctx.Done():
			sessionErr = ctx.Err()
			cancel()
			<-done // Cleanup: preserve cancellation/timeout.
			break loop
		case err := <-done:
			sessionErr = err
			break loop
		case event := <-client.Events():
			if event.Kind == "disconnected" {
				sessionErr = event.Err
				cancel()
				<-done // Cleanup: retain the disconnected event's error.
				break loop
			}
		case <-ticker.C:
			if err = poll(); err != nil {
				sessionErr = err
				cancel()
				<-done // Cleanup: retain the polling error.
				break loop
			}
		}
	}
	cancel()
	if status := websocket.CloseStatus(sessionErr); status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
		sessionErr = nil
	}
	if sessionErr == nil {
		record.mu.Lock()
		failure := record.failure
		chunks := record.chunks
		metadata := record.metadata
		controller := record.sentCommand
		image := record.artwork
		record.mu.Unlock()
		if failure != "" {
			sessionErr = errors.New(failure)
		} else if roles[0] == sendspin.Player && chunks == 0 {
			sessionErr = errors.New("no audio chunks observed")
		} else if roles[0] == sendspin.MetadataRole && metadata == nil {
			sessionErr = errors.New("no applied metadata observed")
		} else if roles[0] == sendspin.Controller && controller == nil {
			sessionErr = errors.New("no controller command sent")
		} else if roles[0] == sendspin.ArtworkRole && image == nil {
			sessionErr = errors.New("no complete artwork observed")
		}
	}
	return finish(sessionErr)
}

func main() {
	o, err := parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err = run(o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
