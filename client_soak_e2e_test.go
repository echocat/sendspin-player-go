//go:build e2e

package sendspin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/netfault"
	"github.com/echocat/sendspin-player-go/internal/specaudit"
	"github.com/echocat/slf4g/testing/recording"
)

type resourceOutput struct{ active, opens, closes, writes, slow atomic.Int64 }
type resourceDevice struct {
	clock clockDevice
	owner *resourceOutput
	once  sync.Once
}

func (o *resourceOutput) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o.opens.Add(1)
	o.active.Add(1)
	return &resourceDevice{clock: clockDevice{format: f}, owner: o}, nil
}
func (d *resourceDevice) Next(ctx context.Context) (time.Time, error) { return d.clock.Next(ctx) }
func (d *resourceDevice) Write(ctx context.Context, samples []float32) error {
	if wait := time.Duration(d.owner.slow.Load()); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.owner.writes.Add(1)
	return d.clock.Write(ctx, samples)
}
func (d *resourceDevice) Flush(ctx context.Context) error { return d.clock.Flush(ctx) }
func (d *resourceDevice) Close() error {
	d.once.Do(func() { d.owner.active.Add(-1); d.owner.closes.Add(1) })
	return nil
}

type soakReceipt struct {
	Schema                 int           `json:"schema"`
	Source                 string        `json:"source_sha256"`
	GOOS                   string        `json:"goos"`
	GOARCH                 string        `json:"goarch"`
	GoVersion              string        `json:"go_version"`
	Suite                  string        `json:"cipher_suite"`
	Status                 string        `json:"status"`
	Requested              time.Duration `json:"requested_duration_ns"`
	Elapsed                time.Duration `json:"elapsed_ns"`
	Rounds                 int64         `json:"rounds"`
	Rekeys                 int64         `json:"rekeys"`
	Connections            int64         `json:"connections"`
	SourceSamples          int64         `json:"source_samples"`
	DecodedSamples         int64         `json:"decoded_samples"`
	OutputWrites           int64         `json:"output_writes"`
	PeakQueuedWireBytes    int64         `json:"observed_peak_queued_wire_bytes"`
	PeakQueuedDecodedBytes int64         `json:"observed_peak_queued_decoded_bytes"`
	PeakQueuedChunks       int64         `json:"observed_peak_queued_chunks"`
	HeapBefore             uint64        `json:"heap_before_gc_bytes"`
	HeapAfter              uint64        `json:"heap_after_gc_bytes"`
	GoroutinesBefore       int           `json:"goroutines_before"`
	GoroutinesAfter        int           `json:"goroutines_after"`
	PhysicalConformance    bool          `json:"physical_conformance"`
	Scope                  string        `json:"scope"`
}

func raisePeak(peak *atomic.Int64, value int64) {
	for old := peak.Load(); value > old; old = peak.Load() {
		if peak.CompareAndSwap(old, value) {
			return
		}
	}
}

func TestReferenceSustainedPlaybackResourceSoak(t *testing.T) {
	duration := 12 * time.Second
	if value := os.Getenv("SENDSPIN_SOAK_DURATION"); value != "" {
		var err error
		duration, err = time.ParseDuration(value)
		if err != nil || duration < 12*time.Second || duration > 10*time.Minute {
			t.Fatal("soak duration must be in [12s,10m]")
		}
	}
	project, err := specaudit.Load(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, suite := range []string{"25519_ChaChaPoly_SHA256", "25519_AESGCM_SHA256"} {
		t.Run(suite, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), duration+time.Minute)
			defer cancel()
			receipt := soakReceipt{Schema: 1, Source: project.SourceDigest, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), Suite: suite, Status: "running", Requested: duration, Scope: "finite software resource soak; virtual output only; no physical speed, latency, audibility or universal leak guarantee"}
			if err := os.MkdirAll("var/soak", 0700); err != nil {
				t.Fatal(err)
			}
			file, err := os.CreateTemp("var/soak", "resource-"+runtime.GOOS+"-*.json")
			if err != nil {
				t.Fatal(err)
			}
			path := file.Name()
			if err = file.Close(); err != nil {
				t.Fatal(err)
			}
			write := func() {
				data, e := json.MarshalIndent(receipt, "", "  ")
				if e != nil {
					t.Error(e)
					return
				}
				if e = os.WriteFile(path, append(data, '\n'), 0600); e != nil {
					t.Error(e)
				}
			}
			write()
			defer func() {
				if t.Failed() {
					receipt.Status = "fail"
				}
				write()
				t.Log("resource receipt:", filepath.ToSlash(path))
			}()
			cred, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			psk := Secret{161}
			ref := startReferenceWithBudget(t, cred, psk, false, map[string]any{"device_pairing_psk": base64.RawURLEncoding.EncodeToString(cred.PairingPSK[:])}, duration+time.Minute)
			cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
			store := &MemoryStore{}
			if err = store.Save(ctx, cred); err != nil {
				t.Fatal(err)
			}
			output := &resourceOutput{}
			logs := recording.NewLogger()
			var samples, rekeys, replies, replyBase, peakWire, peakDecoded, peakChunks atomic.Int64
			var client *Client
			client, err = New(ctx, Config{Name: "Resource soak", Store: store, Roles: []Role{Player, Controller}, Output: output, DisableDiscovery: true, CipherSuite: suite, Logger: logs, BufferCapacity: 65535, MinBuffer: 150 * time.Millisecond, RequiredLeadTime: 200 * time.Millisecond, Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}},
				Observe: func(m diagnostic.Message) {
					if m.Type == "noise/handshake" && m.Direction == diagnostic.Sent {
						if m.Encrypted {
							rekeys.Add(1)
						}
						replyBase.Store(replies.Load())
					}
					if m.Type == "server/time" && m.Direction == diagnostic.Received {
						replies.Add(1)
					}
				},
				Decoded: func(_ codec.Format, x []float32) {
					samples.Add(int64(len(x)))
					// This hook executes on the session actor, where engine is owned.
					// Never call a public Client method or retain borrowed samples here.
					client.mu.Lock()
					s := client.active
					client.mu.Unlock()
					if s != nil && s.engine != nil {
						stats := s.engine.Stats()
						raisePeak(&peakWire, int64(stats.PeakWireBytes))
						raisePeak(&peakDecoded, int64(stats.PeakDecodedBytes))
						raisePeak(&peakChunks, int64(stats.PeakChunks))
					}
				}})
			if err != nil {
				t.Fatal(err)
			}
			// Transport impairments have their own campaign. This test isolates
			// sustained resource ownership and cooperative output backpressure.
			proxy, err := netfault.New(strings.TrimPrefix(ref.url, "http://"), netfault.Profile{ChunkBytes: 2048})
			if err != nil {
				t.Fatal(err)
			}
			defer proxy.Close()
			done := make(chan error, 1)
			go func() { done <- client.Run(ctx, "ws://"+proxy.Address()+"/sendspin") }()
			var once sync.Once
			join := func() {
				once.Do(func() {
					cancel()
					proxy.Close()
					select {
					case e := <-done:
						if !errors.Is(e, context.Canceled) {
							t.Error(e)
						}
					case <-time.After(5 * time.Second):
						t.Error("soak session leaked")
					}
				})
			}
			t.Cleanup(join)
			ready := func() {
				eventually(t, func() bool {
					return replies.Load() >= replyBase.Load()+8 && client.Snapshot().Available && ref.control(t, map[string]any{"command": "status"})["available"] == true
				})
				mediaCommand(t, client, ref, map[string]any{"command": "barrier"})
			}
			ready()
			if err = client.SetVolume(ctx, 37); err != nil {
				t.Fatal(err)
			}
			ref.control(t, map[string]any{"command": "pump_start"})
			eventually(t, func() bool { return samples.Load() > 0 })
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			receipt.HeapBefore = before.HeapAlloc
			receipt.GoroutinesBefore = runtime.NumGoroutine()
			started := time.Now()
			connectedAt := started
			deadline := started.Add(duration)
			for round := 0; time.Now().Before(deadline) || round < 4; round++ {
				switch round % 4 {
				case 0:
					// The independent server may stop its PushStream while its
					// pairing policy changes credentials. Join the producer first;
					// this is sustained segmented streaming, not gapless rekey proof.
					ref.control(t, map[string]any{"command": "pump_stop"})
					old := rekeys.Load()
					ref.control(t, map[string]any{"command": "pair"})
					eventually(t, func() bool { return rekeys.Load() >= old+2 && client.Snapshot().Available })
					ready()
					ref.control(t, map[string]any{"command": "pump_start"})
				case 1:
					output.slow.Store(int64(150 * time.Millisecond))
				case 2:
					output.slow.Store(0)
				case 3:
					// Run intentionally grows retry backoff for short sessions.
					// Keep each connection past its documented reset window while
					// still pumping audio, rather than mislabel a later 16s retry
					// as failure of a ten-second fixture wait.
					if wait := 11*time.Second - time.Since(connectedAt); wait > 0 {
						timer := time.NewTimer(wait)
						select {
						case <-timer.C:
						case <-ctx.Done():
							timer.Stop()
							t.Fatal("soak lifetime expired during sustained streaming")
						}
					}
					ref.control(t, map[string]any{"command": "pump_stop"})
					output.slow.Store(0)
					before := proxy.Accepted()
					proxy.DropConnections()
					eventually(t, func() bool { return proxy.Accepted() > before && client.Snapshot().Available })
					ready()
					connectedAt = time.Now()
					if client.Snapshot().Volume != 37 {
						t.Fatal("reconnect lost controls")
					}
					ref.control(t, map[string]any{"command": "pump_start"})
				}
				if peakWire.Load() > 65535 || peakDecoded.Load() > 32<<20 || output.active.Load() > 1 {
					t.Fatal("unbounded queue/device ownership")
				}
				stored, e := store.Load(ctx)
				if e != nil || len(stored.Records) != 1 || stored.Records[0].ServerID != ref.id || stored.PrivateKey != cred.PrivateKey || stored.PairingPSK != cred.PairingPSK {
					t.Fatal("trust/identity state grew or changed", e)
				}
				receipt.Rounds++
				logs.Reset()
				timer := time.NewTimer(time.Second)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					t.Fatal("soak exceeded bounded lifetime")
				}
			}
			result := ref.control(t, map[string]any{"command": "pump_stop"})
			receipt.SourceSamples = int64(result["source_samples"].(float64))
			mediaCommand(t, client, ref, map[string]any{"command": "barrier"})
			eventually(t, func() bool { return samples.Load() == receipt.SourceSamples })
			receipt.Elapsed = time.Since(started)
			receipt.DecodedSamples = samples.Load()
			receipt.Rekeys = rekeys.Load()
			receipt.Connections = proxy.Accepted()
			receipt.OutputWrites = output.writes.Load()
			if receipt.SourceSamples < int64(duration.Seconds()*48000/2) {
				t.Fatal("soak spent too little time streaming actual source samples")
			}
			receipt.PeakQueuedWireBytes = peakWire.Load()
			receipt.PeakQueuedDecodedBytes = peakDecoded.Load()
			receipt.PeakQueuedChunks = peakChunks.Load()
			join()
			client.mu.Lock()
			released := client.active == nil && len(client.sessions) == 0
			client.mu.Unlock()
			if !released || proxy.Active() != 0 || output.active.Load() != 0 || output.opens.Load() != output.closes.Load() || receipt.Rekeys < 2 || receipt.Connections < 2 || receipt.OutputWrites == 0 {
				t.Fatal("resource ownership was not released")
			}
			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			receipt.HeapAfter = after.HeapAlloc
			receipt.GoroutinesAfter = runtime.NumGoroutine()
			if after.HeapAlloc > before.HeapAlloc+32<<20 {
				t.Fatal("retained process heap exceeded the finite 32-MiB allowance")
			}
			receipt.Status = "pass"
		})
	}
}
