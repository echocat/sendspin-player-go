package oto

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/internal/monotime"
)

type fakePlayer struct {
	source                  io.Reader
	buffered, plays, pauses int
	err                     error
}

func (p *fakePlayer) Play()                { p.plays++ }
func (p *fakePlayer) PauseAndStopReading() { p.pauses++ }
func (p *fakePlayer) SetBufferSize(int)    {}
func (p *fakePlayer) BufferedSize() int    { return p.buffered }
func (p *fakePlayer) Seek(offset int64, whence int) (int64, error) {
	p.buffered = 0
	return p.source.(io.Seeker).Seek(offset, whence)
}
func (p *fakePlayer) Err() error { return p.err }

type fakeBackend struct {
	player  *fakePlayer
	failure error
}

func (b *fakeBackend) newPlayer(r io.Reader) player {
	b.player = &fakePlayer{source: r}
	return b.player
}
func (b *fakeBackend) err() error { return b.failure }

func readyPool(b *fakeBackend) *contextPool {
	ready := make(chan struct{})
	close(ready)
	return &contextPool{create: func(audio.Format, time.Duration) (backend, <-chan struct{}, error) { return b, ready, nil }}
}

func TestOutputRequiresExplicitApproximateOptInBeforeInitialization(t *testing.T) {
	pool := &contextPool{create: func(audio.Format, time.Duration) (backend, <-chan struct{}, error) {
		t.Fatal("unexpected native initialization")
		return nil, nil, nil
	}}
	format := audio.Format{SampleRate: 48000, Channels: 2}
	if _, err := (Output{}).open(context.Background(), format, pool); !errors.Is(err, ErrApproximateOnly) {
		t.Fatal(err)
	}
	for _, f := range []audio.Format{{}, {SampleRate: 48000, Channels: 3}, {SampleRate: 7999, Channels: 2}, {SampleRate: 192001, Channels: 2}} {
		if _, err := (Output{AllowApproximate: true}).open(context.Background(), f, pool); err == nil {
			t.Fatal("accepted invalid format")
		}
	}
	for _, o := range []Output{
		{AllowApproximate: true, BufferDuration: -1},
		{AllowApproximate: true, BufferDuration: 2 * time.Second},
		{AllowApproximate: true, EstimatedDeviceLatency: -1},
		{AllowApproximate: true, PlayerBufferFrames: 8193},
	} {
		if _, err := o.open(context.Background(), format, pool); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
}

func TestEstimatedTimelineSoftwareFlushAndClose(t *testing.T) {
	b := &fakeBackend{}
	pool := readyPool(b)
	format := audio.Format{SampleRate: 48000, Channels: 2}
	out, err := (Output{AllowApproximate: true, EstimatedDeviceLatency: 30 * time.Millisecond}).open(context.Background(), format, pool)
	if err != nil {
		t.Fatal(err)
	}
	d := out.(*device)
	samples := make([]float32, 20)
	if err = d.Write(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	b.player.buffered = 3 * 2 * 4
	before := monotime.Now()
	next, err := d.Next(context.Background())
	after := monotime.Now()
	want := 30*time.Millisecond + 13*time.Second/48000
	if err != nil || next.Before(before.Add(want)) || next.After(after.Add(want)) {
		t.Fatal("documented estimate differs", err)
	}
	if err = d.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.source.buffered() != 0 || b.player.buffered != 0 || b.player.pauses != 1 || b.player.plays != 2 {
		t.Fatal("software flush did not reset and resume")
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil || pool.inUse || b.player.pauses != 2 {
		t.Fatal("close not idempotent or did not release lease", err)
	}
	if _, err = d.Next(context.Background()); err == nil {
		t.Fatal("closed device returned a timestamp")
	}
	if err = d.Write(context.Background(), []float32{1, 2}); err == nil {
		t.Fatal("closed device accepted audio")
	}
}

func TestProcessContextIsExclusiveFixedAndReusable(t *testing.T) {
	b := &fakeBackend{}
	pool := readyPool(b)
	format := audio.Format{SampleRate: 48000, Channels: 2}
	o := Output{AllowApproximate: true}
	d, err := o.open(context.Background(), format, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.open(context.Background(), format, pool); !errors.Is(err, ErrInUse) {
		t.Fatal("simultaneous device accepted", err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = o.open(context.Background(), audio.Format{SampleRate: 44100, Channels: 2}, pool); !errors.Is(err, ErrContextFormat) {
		t.Fatal("changed process format", err)
	}
	if _, err = (Output{AllowApproximate: true, BufferDuration: 50 * time.Millisecond}).open(context.Background(), format, pool); !errors.Is(err, ErrContextFormat) {
		t.Fatal("changed process buffer", err)
	}
	d, err = o.open(context.Background(), format, pool)
	if err != nil {
		t.Fatal("failed to reopen unchanged format", err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationCancellationRetainsContextWithoutPinningLease(t *testing.T) {
	ready, created := make(chan struct{}), make(chan struct{})
	b := &fakeBackend{}
	calls := 0
	pool := &contextPool{create: func(audio.Format, time.Duration) (backend, <-chan struct{}, error) {
		calls++
		close(created)
		return b, ready, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	o := Output{AllowApproximate: true}
	format := audio.Format{SampleRate: 48000, Channels: 2}
	go func() { _, err := o.open(ctx, format, pool); done <- err }()
	<-created
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || pool.inUse {
		t.Fatal("initialization cancellation pinned the context", err)
	}
	close(ready)
	d, err := o.open(context.Background(), format, pool)
	if err != nil || calls != 1 {
		t.Fatal("created another process context after cancellation", err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationAndPlayerErrorsAreNotHidden(t *testing.T) {
	failure := errors.New("driver failed")
	b := &fakeBackend{failure: failure}
	pool := readyPool(b)
	o := Output{AllowApproximate: true}
	format := audio.Format{SampleRate: 48000, Channels: 2}
	if _, err := o.open(context.Background(), format, pool); !errors.Is(err, failure) || pool.inUse {
		t.Fatal("driver failure hidden or lease leaked", err)
	}
	b.failure = nil
	d, err := o.open(context.Background(), format, pool)
	if err != nil {
		t.Fatal(err)
	}
	b.player.err = failure
	if _, err = d.Next(context.Background()); !errors.Is(err, failure) {
		t.Fatal("player error hidden", err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
}
