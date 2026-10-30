//go:build e2e

package sendspin

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/codec"
)

// These are software-only producer/validation regressions, not measurements of
// physical output level. The independent binding reads and validates every WAV.
func TestE2EReferenceWeakProducerLifecycleAndLimits(t *testing.T) {
	path := weakReferenceWAV(t, 320, 0)
	var workerPID float64
	t.Run("start stop restart and close while producing", func(t *testing.T) {
		var decoded atomic.Int64
		ref, client := connectWeakReference(t, path, &decoded)
		initial := weakControl(t, ref, "weak_status", http.StatusOK)
		workerPID = initial["worker_pid"].(float64)
		if workerPID <= 0 || initial["producer_active"] != false || initial["producer_tasks"] != float64(0) || initial["source_frames"] != float64(0) {
			t.Fatal("weak producer was not initially idle")
		}
		weakControl(t, ref, "weak_start", http.StatusOK)
		eventually(t, func() bool {
			state := weakControl(t, ref, "weak_status", http.StatusOK)
			return state["producer_active"] == true && state["producer_tasks"] == float64(1) && state["source_frames"].(float64) >= 4800 && decoded.Load() >= 4800
		})
		weakControl(t, ref, "weak_start", http.StatusConflict)
		stopped := weakControl(t, ref, "weak_stop", http.StatusOK)["source_frames"].(float64)
		state := weakControl(t, ref, "weak_status", http.StatusOK)
		if stopped < 4800 || state["source_frames"] != stopped || state["producer_active"] != false || state["producer_tasks"] != float64(0) {
			t.Fatal("weak_stop did not join the producer")
		}
		weakControl(t, ref, "weak_stop", http.StatusConflict)
		if !client.Snapshot().Available || state["codec"] != "pcm" {
			t.Fatal("weak producer lost its native PCM session")
		}
		beforeRestart := decoded.Load()
		weakControl(t, ref, "weak_start", http.StatusOK)
		eventually(t, func() bool {
			state := weakControl(t, ref, "weak_status", http.StatusOK)
			return state["producer_active"] == true && state["producer_tasks"] == float64(1) && state["source_frames"].(float64) >= stopped+4800 && decoded.Load() >= beforeRestart+4800
		})
		// Reference's cleanup now closes this peer while the producer and Go
		// client are still alive. The earlier client cleanup then joins Connect.
	})
	t.Run("peer cleanup joined producer without restarting worker", func(t *testing.T) {
		cred, err := NewCredentials()
		if err != nil {
			t.Fatal(err)
		}
		ref := startReferenceWithOptions(t, cred, Secret{181}, false, map[string]any{"weak_reference_path": path})
		state := weakControl(t, ref, "weak_status", http.StatusOK)
		if state["worker_pid"] != workerPID || state["producer_tasks"] != float64(0) || state["producer_active"] != false || state["source_frames"] != float64(0) {
			t.Fatal("peer cleanup leaked a producer or restarted the shared worker")
		}
	})
}

func TestE2EReferenceWeakProducerRejectsInvalidSources(t *testing.T) {
	for _, test := range []struct {
		name        string
		left, right int16
		mutate      func([]byte) []byte
	}{
		{name: "positive level exceeds limit", left: 329},
		{name: "negative level exceeds limit", left: -329},
		{name: "right channel signal", left: 320, right: 1},
		{name: "wrong sample rate", mutate: func(wav []byte) []byte { binary.LittleEndian.PutUint32(wav[24:], 44100); return wav }},
		{name: "wrong channel count", mutate: func(wav []byte) []byte { binary.LittleEndian.PutUint16(wav[22:], 1); return wav }},
		{name: "wrong sample width", mutate: func(wav []byte) []byte { binary.LittleEndian.PutUint16(wav[34:], 8); return wav }},
		{name: "truncated file", mutate: func(wav []byte) []byte { return wav[:len(wav)-1] }},
		{name: "oversized file", mutate: func(wav []byte) []byte { return append(wav, 0) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := weakReferenceWAV(t, test.left, test.right)
			if test.mutate != nil {
				wav, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, test.mutate(wav), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var decoded atomic.Int64
			ref, client := connectWeakReference(t, path, &decoded)
			weakControl(t, ref, "weak_start", http.StatusInternalServerError)
			state := weakControl(t, ref, "weak_status", http.StatusOK)
			if state["producer_active"] != false || state["producer_tasks"] != float64(0) || state["source_frames"] != float64(0) || decoded.Load() != 0 || !client.Snapshot().Available {
				t.Fatal("rejected WAV started a producer or damaged its peer session")
			}
			weakControl(t, ref, "weak_stop", http.StatusConflict)
		})
	}
}

func weakReferenceWAV(t *testing.T, left, right int16) string {
	t.Helper()
	const frames = 3 * 48000
	wav := make([]byte, 44+frames*4)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 2)
	binary.LittleEndian.PutUint32(wav[24:], 48000)
	binary.LittleEndian.PutUint32(wav[28:], 48000*4)
	binary.LittleEndian.PutUint16(wav[32:], 4)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], frames*4)
	// Preserve the old regression's nonzero point at one second; a second
	// negative point also verifies acceptance of both signs at the valid level.
	binary.LittleEndian.PutUint16(wav[44+48000*4:], uint16(left))
	binary.LittleEndian.PutUint16(wav[44+48000*4+2:], uint16(right))
	binary.LittleEndian.PutUint16(wav[44+48001*4:], uint16(1<<16-320))
	path := filepath.Join(t.TempDir(), "synthetic-only.wav")
	if err := os.WriteFile(path, wav, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func connectWeakReference(t *testing.T, path string, decoded *atomic.Int64) (referenceServer, *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	var done chan error
	// Register before Reference so the peer closes its active producer first.
	t.Cleanup(func() {
		cancel()
		if done != nil {
			select {
			case err := <-done:
				// The peer is deliberately closed before the client is joined.
				// Its transport/session termination result is not a test failure.
				_ = err
			case <-time.After(5 * time.Second):
				t.Error("weak reference Go client did not stop")
			}
		}
	})
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	psk := Secret{182}
	ref := startReferenceWithOptions(t, cred, psk, false, map[string]any{"weak_reference_path": path})
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: psk}}
	store := &MemoryStore{}
	if err := store.Save(ctx, cred); err != nil {
		t.Fatal(err)
	}
	client, err := New(ctx, Config{
		Name: "Weak producer software regression", Store: store, Roles: []Role{Player}, Output: clockOutput{}, DisableDiscovery: true,
		Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 16}},
		Decoded: func(format codec.Format, samples []float32) { decoded.Add(int64(len(samples) / format.Channels)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	done = make(chan error, 1)
	go func() { done <- client.Connect(ctx, strings.Replace(ref.url, "http://", "ws://", 1)+"/sendspin") }()
	eventually(t, func() bool { return client.Snapshot().Available })
	return ref, client
}

func weakControl(t *testing.T, ref referenceServer, command string, wantStatus int) map[string]any {
	t.Helper()
	data, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal("cannot encode weak control command")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Post(ref.url+"/control", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal("weak control request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != wantStatus {
		t.Fatalf("weak control %s: status=%d want=%d", command, response.StatusCode, wantStatus)
	}
	if wantStatus != http.StatusOK {
		// Exception responses may contain private local paths; never log them.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		t.Fatal("invalid weak control response")
	}
	return result
}
