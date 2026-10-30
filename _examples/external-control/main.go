// This example is a fake host, not an audio/hardware implementation. No CEC,
// mixer, device discovery, CLI or logging provider is installed here.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/sendspin-player-go/audio"
)

// fakeDevice models an independently observable host target. Writes do not
// report state; Readback below is a separate operation. A real adapter must keep
// mute independent of volume and honor the context in every backend operation.
type fakeDevice struct {
	mu      sync.Mutex
	volume  int
	muted   bool
	failure error
}

func (d *fakeDevice) setVolume(ctx context.Context, v int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.failure != nil {
		return d.failure
	}
	d.volume = v // Explicitly does NOT clear muted.
	return nil
}

func (d *fakeDevice) setMuted(ctx context.Context, v bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.failure != nil {
		return d.failure
	}
	d.muted = v
	return nil
}

// host serializes target selection and readback token replacement. The backend
// setter captures its device directly, never a mutable "current device" pointer.
type host struct {
	mu           sync.Mutex
	client       *sendspin.Client
	binding      sendspin.PlayerControlBinding
	device       *fakeDevice
	capabilities sendspin.PlayerControlCapabilities
}

func (h *host) Select(d *fakeDevice, writable bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	caps := sendspin.PlayerControlCapabilities{Volume: writable, Mute: writable}
	target := sendspin.PlayerControlTarget{}
	if d != nil {
		d.mu.Lock()
		v, m := d.volume, d.muted
		d.mu.Unlock()
		target.Initial = sendspin.PlayerControlReport{Capabilities: caps, Volume: &v, Muted: &m}
		if writable {
			target.SetVolume = d.setVolume
			target.SetMuted = d.setMuted
		}
	}
	b, err := h.client.BindPlayerControl(target)
	if err != nil {
		return err
	}
	h.binding, h.device, h.capabilities = b, d, caps
	return nil
}

// Readback is host-initiated, contains actual observed values, and never writes.
// unavailable=true removes knowledge/controls without making the stream unavailable.
// A subsequent successful readback restores controls using a new generation.
func (h *host) Readback(unavailable bool, diagnostic error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := sendspin.PlayerControlReport{Err: diagnostic}
	if h.device != nil && !unavailable {
		h.device.mu.Lock()
		v, m := h.device.volume, h.device.muted
		h.device.mu.Unlock()
		r.Volume, r.Muted, r.Capabilities = &v, &m, h.capabilities
	}
	b, err := h.client.ReportPlayerControl(h.binding, r)
	if err == nil {
		h.binding = b
	}
	return err
}

// Nothing in this demonstration opens audio. Production supplies an independent
// audio.Output; that output always receives unity-gain samples in external mode.
type unopenedOutput struct{}

func (unopenedOutput) Open(context.Context, audio.Format) (audio.Device, error) {
	return nil, errors.New("example has no physical audio output")
}

func main() {
	c, err := sendspin.New(context.Background(), sendspin.Config{
		Name: "Fake host-managed player", Store: &sendspin.MemoryStore{},
		Output: unopenedOutput{}, Roles: []sendspin.Role{sendspin.Player},
		PlayerControl: &sendspin.PlayerControlConfig{},
	})
	if err != nil {
		panic(err)
	}
	h := &host{client: c}
	d := &fakeDevice{volume: 35, muted: true}
	must(h.Select(d, true)) // Writable, observed, no startup write.
	show(c, "writable")
	must(h.Readback(true, errors.New("fake host temporarily unavailable")))
	show(c, "unavailable (stream/identity remain intact)")
	must(h.Readback(false, nil))
	show(c, "restored")
	must(h.Select(d, false)) // Same physical target, intentionally read-only.
	show(c, "read-only")
	must(h.Select(&fakeDevice{volume: 80}, true)) // New immutable target binding.
	show(c, "replacement target")
	must(h.Select(nil, false)) // Fixed/unknown, still unity gain.
	show(c, "fixed/unknown")
	// During a real session, c.SetVolume(ctx,50) and server player commands
	// enqueue the same host writes. c.Command(...) remains a GROUP command.
	// Poll c.Snapshot().PlayerControl for request results; then call Readback
	// using independently observed values. Events are optional notifications only.
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func show(c *sendspin.Client, label string) {
	s := c.Snapshot()
	fmt.Printf("%s: volume=%d known=%v muted=%v known=%v capabilities=%+v diagnostic=%q\n", label, s.Volume, s.VolumeKnown, s.Muted, s.MutedKnown, s.PlayerControl.Capabilities, s.PlayerControl.Error)
}
