//go:build e2e

package sendspin

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/timesync"
)

func TestE2EReferenceNativeClockConvergesAfterStartupBursts(t *testing.T) {
	cred, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	key := Secret{86}
	ref := startReference(t, cred, key, false)
	cred.Records = []PairingRecord{{ServerID: ref.id, PSK: key}}
	store := &MemoryStore{}
	if err = store.Save(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	type row struct {
		Client, Received, Transmitted, Arrival, Offset, Uncertainty int64
		Valid                                                       bool
	}
	var mu sync.Mutex
	var rows []row
	valid, total := 0, 0
	var client *Client
	client, err = New(context.Background(), Config{Name: "Native clock startup", Store: store, Roles: []Role{Player}, Output: clockOutput{}, Formats: []codec.Format{{Codec: "pcm", SampleRate: 48000, Channels: 1, BitDepth: 16}}, Observe: func(m diagnostic.Message) {
		if m.Direction != diagnostic.Received || m.Type != "server/time" {
			return
		}
		var wire struct {
			Client      int64 `json:"client_transmitted"`
			Received    int64 `json:"server_received"`
			Transmitted int64 `json:"server_transmitted"`
		}
		if json.Unmarshal(m.Payload, &wire) != nil {
			return
		}
		client.mu.Lock()
		s := client.active
		client.mu.Unlock()
		if s == nil {
			return
		}
		arrival := m.At.Sub(s.epoch).Microseconds() + 1
		offset, uncertainty, ok := timesync.Sample(wire.Client, wire.Received, wire.Transmitted, arrival)
		mu.Lock()
		defer mu.Unlock()
		total++
		if ok {
			valid++
		}
		if len(rows) < 24 {
			rows = append(rows, row{wire.Client, wire.Received, wire.Transmitted, arrival, offset, uncertainty, ok})
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	connectReference(t, client, ref)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if client.Snapshot().Available {
				return
			}
		case <-deadline.C:
			mu.Lock()
			snapshot := append([]row(nil), rows...)
			v, n := valid, total
			mu.Unlock()
			client.mu.Lock()
			s := client.active
			client.mu.Unlock()
			if s != nil {
				t.Log("filter ready/error", s.filter.Ready(), s.filter.Error())
			}
			t.Fatalf("native clock did not converge: %d/%d valid samples; first observations=%+v", v, n, snapshot)
		}
	}
}
