package main

import (
	"context"
	"errors"
	"testing"

	sendspin "github.com/echocat/sendspin-player-go"
)

func TestFakeVolumeDoesNotUnmuteAndCanceledWritesDoNothing(t *testing.T) {
	d := &fakeDevice{volume: 35, muted: true}
	if err := d.setVolume(context.Background(), 50); err != nil || d.volume != 50 || !d.muted {
		t.Fatal("volume changed mute or failed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.setVolume(ctx, 100); !errors.Is(err, context.Canceled) || d.volume != 50 {
		t.Fatal("canceled volume reached fake hardware", err)
	}
	d.failure = errors.New("fake device unavailable")
	if err := d.setMuted(context.Background(), false); !errors.Is(err, d.failure) || !d.muted {
		t.Fatal("failed mute changed observed state", err)
	}
}

func TestFakeHostReadbackCapabilityLossAndTargetReplacement(t *testing.T) {
	c, err := sendspin.New(context.Background(), sendspin.Config{
		Name: "Example regression", Store: &sendspin.MemoryStore{},
		Output: unopenedOutput{}, Roles: []sendspin.Role{sendspin.Player},
		DisableDiscovery: true, PlayerControl: &sendspin.PlayerControlConfig{},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &host{client: c}
	d := &fakeDevice{volume: 35, muted: true}
	if err = h.Select(d, true); err != nil {
		t.Fatal(err)
	}
	if s := c.Snapshot(); !s.VolumeKnown || s.Volume != 35 || !s.MutedKnown || !s.Muted || !s.PlayerControl.Capabilities.Volume {
		t.Fatal("initial observation/capabilities lost", s)
	}
	d.mu.Lock()
	d.volume = 42 // Physical knob/readback, not an SDK request.
	d.mu.Unlock()
	if err = h.Readback(false, nil); err != nil || c.Snapshot().Volume != 42 {
		t.Fatal("readback did not publish independent observation", err)
	}
	if err = h.Select(d, false); err != nil {
		t.Fatal(err)
	}
	if s := c.Snapshot(); !s.VolumeKnown || s.Volume != 42 || s.PlayerControl.Capabilities.Volume || s.PlayerControl.Capabilities.Mute {
		t.Fatal("read-only observation became writable", s)
	}
	if err = h.Readback(true, errors.New("fake nonterminal failure")); err != nil {
		t.Fatal(err)
	}
	if s := c.Snapshot(); s.VolumeKnown || s.MutedKnown || s.PlayerControl.Error == "" {
		t.Fatal("unavailable state reused stale observations", s)
	}
	if err = h.Select(&fakeDevice{volume: 80}, true); err != nil || c.Snapshot().Volume != 80 {
		t.Fatal("replacement target reused previous device state", err)
	}
}
