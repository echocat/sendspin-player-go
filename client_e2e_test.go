//go:build e2e

package sendspin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/devpath"
)

// clockOutput is an instrumented fake device, not proof of physical sync.
// It simulates a real bounded device clock, allowing the shared renderer to run.
type clockOutput struct{}
type clockDevice struct {
	format audio.Format
	next   time.Time
}

func (clockOutput) Open(_ context.Context, f audio.Format) (audio.Device, error) {
	return &clockDevice{format: f}, nil
}
func (d *clockDevice) Next(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if d.next.Before(time.Now()) {
		d.next = time.Now()
	}
	return d.next, nil
}
func (d *clockDevice) Write(ctx context.Context, samples []float32) error {
	d.next = d.next.Add(time.Duration(len(samples)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
	if wait := time.Until(d.next) - 40*time.Millisecond; wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
func (d *clockDevice) Flush(context.Context) error { d.next = time.Time{}; return nil }
func (d *clockDevice) Close() error                { return nil }

type referenceServer struct{ url, id string }

var referenceSetup sync.Once
var referenceSetupErr error

func (r referenceServer) control(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Post(r.url+"/control", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("reference server: %s %s", response.Status, body)
	}
	var out map[string]any
	if err = json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
func startReference(t *testing.T, cred Credentials, psk Secret, pairing bool) referenceServer {
	t.Helper()
	return startReferenceWithOptions(t, cred, psk, pairing, nil)
}

func startReferenceWithOptions(t *testing.T, cred Credentials, psk Secret, pairing bool, options map[string]any) referenceServer {
	return startReferenceWithBudget(t, cred, psk, pairing, options, 90*time.Second)
}

func startReferenceWithBudget(t *testing.T, cred Credentials, psk Secret, pairing bool, options map[string]any, lifetime time.Duration) referenceServer {
	t.Helper()
	if lifetime <= 0 || lifetime > 31*time.Minute {
		t.Fatal("invalid bounded fixture lifetime")
	}
	config := filepath.Join(t.TempDir(), "config.json")
	settings := map[string]any{"client_id": cred.ClientID(), "psk": base64.RawURLEncoding.EncodeToString(psk[:]), "pairing": pairing}
	for key, value := range options {
		settings[key] = value
	}
	b, _ := json.Marshal(settings)
	if err := os.WriteFile(config, b, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifetime)
	root, err := os.Getwd()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err = devpath.Prepare(root); err != nil {
		cancel()
		t.Fatal(err)
	}
	referenceSetup.Do(func() {
		setup := exec.CommandContext(ctx, "uv", "sync", "--locked", "--project", "internal/e2e")
		setup.Env = devpath.PythonEnv(root, "e2e")
		out, err := setup.CombinedOutput()
		if err != nil {
			referenceSetupErr = fmt.Errorf("uv sync: %w\n%s", err, out)
		}
	})
	if referenceSetupErr != nil {
		cancel()
		t.Fatal(referenceSetupErr)
	}
	python := devpath.Python(root, "e2e")
	cmd := exec.CommandContext(ctx, python, "internal/e2e/server.py", "--config", config)
	cmd.Env = devpath.PythonEnv(root, "e2e")
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatalf("uv is required for E2E (mise install): %v", err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	t.Cleanup(func() {
		cancel()
		err := <-finished
		if t.Failed() {
			t.Logf("reference server exit: %v\n%s", err, stderr.String())
		}
	})
	ready := make(chan []byte, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- append([]byte(nil), scanner.Bytes()...)
		} else {
			ready <- nil
		}
	}()
	select {
	case <-ctx.Done():
		t.Fatal("reference server startup timeout")
	case line := <-ready:
		var msg struct {
			URL string `json:"url"`
			ID  string `json:"server_id"`
		}
		if err = json.Unmarshal(line, &msg); err != nil || msg.URL == "" {
			t.Fatalf("reference server failed to start: %v (see cleanup log)", err)
		}
		return referenceServer{msg.URL, msg.ID}
	}
	return referenceServer{}
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestReferenceServer(t *testing.T) {
	for _, codecName := range []string{"pcm", "flac", "opus"} {
		for _, suite := range []string{"25519_ChaChaPoly_SHA256", "25519_AESGCM_SHA256"} {
			t.Run(codecName+"/"+suite, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cred, err := NewCredentials()
				if err != nil {
					t.Fatal(err)
				}
				psk := Secret{1, 2, 3}
				ref := startReference(t, cred, psk, false)
				cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
				store := &MemoryStore{}
				if err = store.Save(ctx, cred); err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				var decoded []byte
				negotiated := ""
				negotiatedDepth := 0
				formats := []codec.Format{{Codec: codecName, SampleRate: 48000, Channels: 1, BitDepth: 16}}
				if codecName == "opus" {
					formats = append(formats, codec.Format{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16})
				}
				if codecName == "pcm" {
					formats = append(formats, codec.Format{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 24})
				}
				c, err := New(ctx, Config{Name: "Go RC1 E2E", Store: store, Roles: []Role{Player, Controller, MetadataRole, ColorRole, ArtworkRole, VisualizerRole}, Artwork: []ArtworkChannel{{Source: "album", Format: "png", Width: 32, Height: 32}}, Output: clockOutput{}, CipherSuite: suite, Formats: formats, MinBuffer: 100 * time.Millisecond, Decoded: func(format codec.Format, samples []float32) {
					mu.Lock()
					defer mu.Unlock()
					negotiated = format.Codec
					negotiatedDepth = format.BitDepth
					for _, sample := range samples {
						decoded = binary.LittleEndian.AppendUint16(decoded, uint16(int16(sample*32768)))
					}
				}})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- c.Connect(ctx, strings.Replace(ref.url, "http://", "ws://", 1)+"/sendspin") }()
				t.Cleanup(func() {
					cancel()
					select {
					case err := <-done:
						t.Logf("client exit: %v", err)
					case <-time.After(5 * time.Second):
						t.Error("client did not stop")
					}
				})
				eventually(t, func() bool { return c.Snapshot().Available })
				result := ref.control(t, map[string]any{"command": "stream"})
				mu.Lock()
				original := min(len(decoded), 14400*2)
				sum := sha256.Sum256(decoded[:original])
				count := len(decoded) / 2
				padding := append([]byte(nil), decoded[original:]...)
				gotCodec := negotiated
				mu.Unlock()
				if gotCodec != codecName {
					t.Fatalf("server negotiated %s instead of %s", gotCodec, codecName)
				}
				if count != int(result["decoded_samples"].(float64)) || original != 14400*2 || (codecName != "opus" && hex.EncodeToString(sum[:]) != result["pcm_sha256"]) {
					t.Fatalf("decoded audio differs: samples=%d want=%v hash=%x want=%s", count, result["decoded_samples"], sum, result["pcm_sha256"])
				}
				if codecName != "opus" && !bytes.Equal(padding, make([]byte, len(padding))) {
					t.Fatal("reference encoder tail is not silence")
				}
				ref.control(t, map[string]any{"command": "volume"})
				eventually(t, func() bool { state := c.Snapshot(); return state.Volume == 37 && state.Muted })
				volume := 55
				if err := c.Command(ctx, Command{Command: "volume", Volume: &volume}); err != nil {
					t.Fatal(err)
				}
				eventually(t, func() bool { state := c.Snapshot(); return state.Volume == 55 && state.Muted })
				if codecName == "pcm" && suite == "25519_ChaChaPoly_SHA256" {
					ref.control(t, map[string]any{"command": "clear"})
					ref.control(t, map[string]any{"command": "more", "frames": 144000})
					if err := c.PreferFormat(ctx, formats[1]); err != nil {
						t.Fatal(err)
					}
					ref.control(t, map[string]any{"command": "more"})
					eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return negotiatedDepth == 24 })
				}
				ref.control(t, map[string]any{"command": "clear"})
				ref.control(t, map[string]any{"command": "stop"})
			})
		}
	}
}

func TestReferencePSKPairingServerInitiated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := FileStore{Path: filepath.Join(t.TempDir(), "device.json")}
	config := Config{Name: "Pairing E2E", Store: store, Roles: []Role{Player, Controller}, Output: clockOutput{}, DisableDiscovery: true}
	c, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref := startReference(t, cred, cred.PairingPSK, true)
	endpoint, stop := serveTestClient(t, c)
	ref.control(t, map[string]any{"command": "connect", "url": endpoint})
	eventually(t, func() bool {
		persisted, err := store.Load(ctx)
		return err == nil && len(persisted.Records) == 1 && c.Snapshot().Available
	})
	persisted, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Records[0].ServerID != ref.id {
		t.Fatal("pairing was not bound to reference server")
	}
	if persisted.PrivateKey != cred.PrivateKey || persisted.PairingPSK != cred.PairingPSK {
		t.Fatal("pairing replaced device identity")
	}
	if err = c.SetOutputDelay(ctx, 123*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	stop()
	restarted, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.ClientID() != c.ClientID() || restarted.Snapshot().OutputDelayMS != 123 {
		t.Fatal("restart lost device settings")
	}
	endpoint, _ = serveTestClient(t, restarted)
	ref.control(t, map[string]any{"command": "connect", "url": endpoint, "pairing": false})
	eventually(t, func() bool { return restarted.Snapshot().Available })
	after, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Records[0].PSK != persisted.Records[0].PSK {
		t.Fatal("paired reconnect unexpectedly repeated pairing")
	}
	// Keep a snapshot open across the server-driven credential update. On
	// Windows neither a reader nor the rename may invalidate the other.
	reader, err := openCredentialFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	ref.control(t, map[string]any{"command": "unpair"})
	eventually(t, func() bool { v, err := store.Load(ctx); return err == nil && len(v.Records) == 0 })
}

func serveTestClient(t *testing.T, c *Client) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Serve(ctx, listener) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done: // Cleanup: listener cancellation is expected.
			case <-time.After(5 * time.Second):
				t.Error("Serve did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return fmt.Sprintf("ws://%s/sendspin", listener.Addr()), stop
}
