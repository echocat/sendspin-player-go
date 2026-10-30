//go:build e2e

package sendspin

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
)

// This deliberately paused virtual output tests ownership/buffer lifecycle,
// not wall-clock playback or native DAC behavior. Its clock stops at the gate.
type lifecycleProbe struct {
	mu                               sync.Mutex
	entered, release                 chan struct{}
	once                             sync.Once
	accepted                         []float32
	opens, flushes, closes, canceled int
}
type lifecycleProbeDevice struct {
	owner  *lifecycleProbe
	next   time.Time
	format audio.Format
}

func (p *lifecycleProbe) Open(ctx context.Context, f audio.Format) (audio.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.opens++
	p.mu.Unlock()
	return &lifecycleProbeDevice{owner: p, format: f}, nil
}
func (d *lifecycleProbeDevice) Next(ctx context.Context) (time.Time, error) {
	if d.next.IsZero() {
		d.next = time.Now()
	}
	return d.next, ctx.Err()
}
func (d *lifecycleProbeDevice) Write(ctx context.Context, samples []float32) error {
	marker := float32(0)
	for _, s := range samples {
		if s != 0 {
			marker = s
			break
		}
	}
	if marker != 0 {
		block := false
		d.owner.once.Do(func() { block = true; close(d.owner.entered) })
		if block {
			select {
			case <-d.owner.release:
			case <-ctx.Done():
				d.owner.mu.Lock()
				d.owner.canceled++
				d.owner.mu.Unlock()
				return ctx.Err()
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if marker != 0 {
		d.owner.mu.Lock()
		d.owner.accepted = append(d.owner.accepted, marker)
		d.owner.mu.Unlock()
	}
	d.next = d.next.Add(time.Duration(len(samples)/d.format.Channels) * time.Second / time.Duration(d.format.SampleRate))
	return nil
}
func (d *lifecycleProbeDevice) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.next = time.Time{}
	d.owner.mu.Lock()
	d.owner.flushes++
	d.owner.mu.Unlock()
	return nil
}
func (d *lifecycleProbeDevice) Close() error {
	d.owner.mu.Lock()
	d.owner.closes++
	d.owner.mu.Unlock()
	return nil
}
func (p *lifecycleProbe) state() (accepted []float32, opens, flushes, canceled int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.accepted), p.opens, p.flushes, p.canceled
}

func injectLifecycleStart(t *testing.T, ref referenceServer) {
	t.Helper()
	sendPairingFixtureMessage(t, ref, "stream/start", map[string]any{"server_transmitted": 0, "player": map[string]any{"codec": "pcm", "sample_rate": 48000, "channels": 1, "bit_depth": 16}})
}
func injectLifecycleAudio(t *testing.T, ref referenceServer, timestamp int64, value int16) {
	t.Helper()
	frame := binary.BigEndian.AppendUint64([]byte{4}, uint64(timestamp))
	frame = binary.BigEndian.AppendUint32(frame, 0)
	for range 960 {
		frame = binary.LittleEndian.AppendUint16(frame, uint16(value))
	}
	ref.control(t, map[string]any{"command": "binary", "data": base64.RawURLEncoding.EncodeToString(frame)})
}
func lifecycleTimestamp(t *testing.T, ref referenceServer) int64 {
	t.Helper()
	return int64(ref.control(t, map[string]any{"command": "clock"})["now"].(float64)) + 100_000
}

func TestE2EReferenceClearAndRoleRemovalCancelBufferedPlayback(t *testing.T) {
	for _, mode := range []string{"clear", "role-removal"} {
		t.Run(mode, func(t *testing.T) {
			credentials, err := NewCredentials()
			if err != nil {
				t.Fatal(err)
			}
			psk := Secret{116}
			ref := startReference(t, credentials, psk, false)
			credentials.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
			store := &MemoryStore{}
			if err = store.Save(context.Background(), credentials); err != nil {
				t.Fatal(err)
			}
			output := &lifecycleProbe{entered: make(chan struct{}), release: make(chan struct{})}
			var decoded atomic.Int32
			client, err := New(context.Background(), Config{Name: "Playback lifecycle", Store: store, Roles: []Role{Player, Controller}, Output: output,
				Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}},
				Decoded: func(codec.Format, []float32) { decoded.Add(1) },
			})
			if err != nil {
				t.Fatal(err)
			}
			connectReference(t, client, ref)
			eventually(t, func() bool { return client.Snapshot().Available })
			injectLifecycleStart(t, ref)
			stamp := lifecycleTimestamp(t, ref)
			injectLifecycleAudio(t, ref, stamp, 8192)
			select {
			case <-output.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("output write never reached the gate")
			}
			injectLifecycleAudio(t, ref, stamp+20_000, 16384)
			pairingFixtureBarrier(t, client, ref, "old-audio-buffered")
			if decoded.Load() != 2 {
				t.Fatal("both old chunks must be decoded before teardown")
			}
			if mode == "clear" {
				sendPairingFixtureMessage(t, ref, "stream/clear", map[string]any{"server_transmitted": 0, "roles": []string{"player"}})
			} else {
				sendPairingFixtureMessage(t, ref, "stream/end", map[string]any{"roles": []string{"player"}})
				mediaCommand(t, client, ref, map[string]any{"command": "roles", "roles": []string{"controller@v1"}})
			}
			pairingFixtureBarrier(t, client, ref, "playback-teardown-processed")
			eventually(t, func() bool { _, _, flushes, canceled := output.state(); return flushes > 0 && canceled == 1 })
			if accepted, _, _, _ := output.state(); len(accepted) != 0 {
				t.Fatal("old gated/queued audio was submitted after teardown", accepted)
			}
			if mode == "role-removal" {
				if slices.Contains(client.Snapshot().ActiveRoles, Player) || slices.Contains(client.Snapshot().ActiveStreams, "player") {
					t.Fatal("player role or stream survived removal")
				}
				mediaCommand(t, client, ref, map[string]any{"command": "roles", "roles": []string{"player@v1", "controller@v1"}})
				injectLifecycleStart(t, ref)
			} else if !slices.Contains(client.Snapshot().ActiveStreams, "player") {
				t.Fatal("clear incorrectly ended the stream")
			}
			injectLifecycleAudio(t, ref, lifecycleTimestamp(t, ref), 24576)
			eventually(t, func() bool { accepted, _, _, _ := output.state(); return len(accepted) > 0 })
			accepted, opens, _, _ := output.state()
			if opens != 1 || len(accepted) != 1 || accepted[0] != .75 {
				t.Fatal("fresh playback replayed stale data or lost device ownership", accepted, opens)
			}
		})
	}
}

func TestE2EReferenceRekeyPreservesBlockedAndQueuedPlayback(t *testing.T) {
	credentials, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ref := startReferenceWithOptions(t, credentials, credentials.PairingPSK, false, map[string]any{"no_record": true, "observe_pairing": true})
	store := &MemoryStore{}
	if err = store.Save(context.Background(), credentials); err != nil {
		t.Fatal(err)
	}
	output := &lifecycleProbe{entered: make(chan struct{}), release: make(chan struct{})}
	var rekeys, decoded atomic.Int32
	client, err := New(context.Background(), Config{Name: "Buffered rekey", Store: store, Roles: []Role{Player, Controller}, Output: output, AllowUnpaired: true,
		Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}},
		Decoded: func(codec.Format, []float32) { decoded.Add(1) },
		Observe: func(message diagnostic.Message) {
			if message.Type == "noise/handshake" && message.Direction == diagnostic.Sent && message.Encrypted {
				rekeys.Add(1)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	connectReference(t, client, ref)
	eventually(t, func() bool { return client.Snapshot().ServerID == ref.id })
	ref.control(t, map[string]any{"command": "trust_unpaired"})
	eventually(t, func() bool {
		return client.Snapshot().Available && slices.Contains(client.Snapshot().ActiveRoles, Player)
	})
	injectLifecycleStart(t, ref)
	stamp := lifecycleTimestamp(t, ref)
	injectLifecycleAudio(t, ref, stamp, 8192)
	select {
	case <-output.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("output never blocked")
	}
	injectLifecycleAudio(t, ref, stamp+20_000, 16384)
	pairingFixtureBarrier(t, client, ref, "before-buffered-rekey")
	if decoded.Load() != 2 {
		t.Fatal("old-key audio was not queued")
	}
	result := ref.control(t, map[string]any{"command": "pair"})
	if result["ok"] != true {
		t.Fatal("native pairing failed", result)
	}
	pairingFixtureBarrier(t, client, ref, "after-buffered-rekey")
	if rekeys.Load() != 2 {
		t.Fatal("expected Sentinel-to-pairing and pairing-to-long-term native rekeys", rekeys.Load())
	}
	accepted, opens, flushes, canceled := output.state()
	if len(accepted) != 0 || opens != 1 || flushes != 0 || canceled != 0 || !slices.Contains(client.Snapshot().ActiveStreams, "player") {
		t.Fatal("rekey reset or displaced buffered playback", accepted, opens, flushes, canceled)
	}
	close(output.release)
	eventually(t, func() bool { accepted, _, _, _ := output.state(); return len(accepted) >= 2 })
	accepted, opens, flushes, canceled = output.state()
	if len(accepted) != 2 || accepted[0] != .25 || accepted[1] != .5 || opens != 1 || flushes != 0 || canceled != 0 || decoded.Load() != 2 {
		t.Fatal("old-key buffered audio was replayed, lost or reordered", accepted, opens, flushes, canceled)
	}
}
