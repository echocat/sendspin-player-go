//go:build e2e

package sendspin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/netfault"
	"github.com/echocat/slf4g/level"
	"github.com/echocat/slf4g/testing/recording"
)

// These are real encrypted loopback sessions through a bounded userspace TCP
// proxy, not a LAN simulation, TCP packet-loss model or physical output proof.
func TestE2EReferenceImpairedTransportRecoveryAndCancellation(t *testing.T) {
	for _, inbound := range []bool{false, true} {
		for _, suite := range []string{"25519_ChaChaPoly_SHA256", "25519_AESGCM_SHA256"} {
			for i, codecName := range []string{"pcm", "flac", "opus"} {
				t.Run(fmt.Sprintf("inbound=%v/%s/%s", inbound, suite, codecName), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					t.Cleanup(cancel)
					cred, err := NewCredentials()
					if err != nil {
						t.Fatal(err)
					}
					psk := Secret{152}
					ref := startReference(t, cred, psk, false)
					cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
					store := &MemoryStore{}
					if err = store.Save(ctx, cred); err != nil {
						t.Fatal(err)
					}
					l := recording.NewLogger()
					l.SetLevel(level.Debug)
					var samples, encoded, clockReplies atomic.Int64
					c, err := New(ctx, Config{Name: "Private network campaign", Store: store, Roles: []Role{Player, Controller}, Output: clockOutput{}, DisableDiscovery: true, CipherSuite: suite,
						// This tests delivery/recovery, not a 200-ms latency target.
						// Leave lead for encoding and paced delivery of the short clip
						// on a busy host, without changing the impairment profile.
						Logger: l, MinBuffer: 150 * time.Millisecond, RequiredLeadTime: time.Second,
						Formats: []codec.Format{{Codec: codecName, SampleRate: 48000, Channels: 1, BitDepth: 16}, {Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 24}},
						Encoded: func(diagnostic.AudioChunk) { encoded.Add(1) },
						Observe: func(message diagnostic.Message) {
							if message.Direction == diagnostic.Received && message.Type == "server/time" {
								clockReplies.Add(1)
							}
						},
						Decoded: func(_ codec.Format, x []float32) { samples.Add(int64(len(x))) }})
					if err != nil {
						t.Fatal(err)
					}
					target, _ := url.Parse(ref.url)
					stop := func() {}
					if inbound {
						var endpoint string
						endpoint, stop = serveTestClient(t, c)
						target, _ = url.Parse(endpoint)
					}
					proxy, err := netfault.New(target.Host, netfault.Profile{Delay: time.Duration(i+1) * time.Millisecond, Jitter: 2 * time.Millisecond, BytesPerSecond: 64_000 * (i + 1), ChunkBytes: 1024})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(proxy.Close)
					stage := "startup"
					endpoint := "ws://" + proxy.Address() + "/sendspin"
					done := make(chan error, 1)
					var joinOnce sync.Once
					join := func() {
						joinOnce.Do(func() {
							cancel()
							proxy.Close()
							select {
							case err := <-done:
								if err != nil && !errors.Is(err, context.Canceled) {
									t.Errorf("outbound worker stopped unexpectedly: %v", err)
								}
							case <-time.After(5 * time.Second):
								t.Error("outbound worker leaked")
							}
						})
					}
					if inbound {
						ref.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
					} else {
						go func() { done <- c.Run(ctx, endpoint) }()
						t.Cleanup(join)
					}
					// Register after join/Close so failures are captured while the
					// session is still live, not after cleanup erased the evidence.
					t.Cleanup(func() {
						if t.Failed() {
							state := c.Snapshot()
							t.Logf("campaign stage=%s accepted=%d active=%d clock_replies=%d encoded_chunks=%d samples=%d available=%t streams=%v", stage, proxy.Accepted(), proxy.Active(), clockReplies.Load(), encoded.Load(), samples.Load(), state.Available, state.ActiveStreams)
							for {
								select {
								case event := <-c.Events():
									t.Logf("campaign event: kind=%s error_class=%s", event.Kind, logErrorClass(event.Err))
								default:
									return
								}
							}
						}
					})
					// Readiness may first converge partway through a later startup
					// burst. Fence a complete eight-exchange burst, and retry the
					// marker if clock replies advanced while it crossed the link.
					// The actor must apply that whole model update before the clip.
					var startupReplies int64
					var startupMarker string
					eventually(t, func() bool {
						replies, state := clockReplies.Load(), c.Snapshot()
						if startupMarker != "" && replies == startupReplies && state.Available && state.Group.Name == startupMarker {
							return true
						}
						if replies >= 8 && replies%8 == 0 && replies != startupReplies && state.Available {
							startupReplies = replies
							startupMarker = ref.control(t, map[string]any{"command": "barrier"})["marker"].(string)
						}
						return false
					})
					if err = c.SetVolume(ctx, 37); err != nil {
						t.Fatal(err)
					}
					// Local publication precedes delivery through the paced link.
					// The out-of-band HTTP stream command must not overtake the
					// independent server's receipt of available=true client/state.
					eventually(t, func() bool { return ref.control(t, map[string]any{"command": "status"})["available"] == true })
					result := ref.control(t, map[string]any{"command": "stream"})
					wantSamples := int64(result["decoded_samples"].(float64))
					if wantSamples < 14400 {
						t.Fatal("reference encoded too few samples", wantSamples)
					}
					stage = "media"
					eventually(t, func() bool { return samples.Load() >= wantSamples })
					proxy.DropConnections()
					stage = "recovery"
					if inbound {
						ref.control(t, map[string]any{"command": "disconnect", "url": endpoint})
						eventually(t, func() bool {
							return proxy.Active() == 0 && ref.control(t, map[string]any{"command": "status"})["connected"] == false
						})
						ref.control(t, map[string]any{"command": "connect", "url": endpoint, "playback": true})
					}
					eventually(t, func() bool { return proxy.Accepted() >= 2 && c.Snapshot().Available })
					if c.Snapshot().Volume != 37 {
						t.Fatal("recovery lost persistent controls")
					}
					persisted, err := store.Load(ctx)
					if err != nil || len(persisted.Records) != 1 || persisted.Records[0].PSK != psk {
						t.Fatal("transport recovery changed trust", err)
					}
					proxy.Stall()
					// Make actual actor traffic enter the stalled transport before
					// cancellation. The fixture does not alter system network state.
					if err = c.SetVolume(ctx, 38); err != nil {
						t.Fatal(err)
					}
					select {
					case <-proxy.Stalled():
					case <-time.After(2 * time.Second):
						t.Fatal("fixture never stalled actual actor traffic")
					}
					cancel()
					proxy.Close()
					stop()
					if !inbound {
						join()
					}
					if proxy.Active() != 0 || proxy.Bytes() == 0 {
						t.Fatal("transport workers/data accounting", proxy.Active(), proxy.Bytes())
					}
					for _, event := range l.GetAll() {
						if err = event.ForEach(func(key string, v any) error {
							if key == "error" {
								t.Fatal("raw error in SDK log")
							}
							value, ok := v.(string)
							if !ok {
								return nil
							}
							for _, secret := range []string{cred.PairingToken(), base64.RawURLEncoding.EncodeToString(psk[:]), "Private network campaign", endpoint} {
								if strings.Contains(value, secret) {
									t.Fatal("SDK log disclosed private data")
								}
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}
