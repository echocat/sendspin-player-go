package capture

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

type fakeDriver struct {
	d                     *Device
	samples               []float32
	started               chan struct{}
	starts, stops, closes int
}

func (f *fakeDriver) Start() error {
	f.starts++
	if f.started != nil {
		close(f.started)
	}
	if len(f.samples) > 0 {
		f.d.Process(f.samples, 990000000)
	}
	return nil
}
func (f *fakeDriver) Stop() error  { f.stops++; return nil }
func (f *fakeDriver) Close() error { f.closes++; return nil }
func (f *fakeDriver) Check() error { return nil }
func (f *fakeDriver) Clock() int64 { return 1000000000 }
func openFake(t *testing.T, f *fakeDriver) *Device {
	t.Helper()
	d, err := Open(context.Background(), Config{Backend: "capture fake"}, audio.Format{SampleRate: 48000, Channels: 2}, func(d *Device) (Driver, error) { f.d = d; return f, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d.(*Device)
}
func TestCaptureIsLazyAndPreservesSamplesAndPartialReadTimes(t *testing.T) {
	f := &fakeDriver{samples: []float32{.5, -.5, .25, -.25}}
	d := openFake(t, f)
	if f.starts != 0 {
		t.Fatal("Open implicitly started recording")
	}
	first := make([]float32, 2)
	second := make([]float32, 2)
	n, at, err := d.Read(context.Background(), first)
	if err != nil || n != 2 || !slices.Equal(first, []float32{.5, -.5}) {
		t.Fatal(n, err, first)
	}
	n, next, err := d.Read(context.Background(), second)
	if err != nil || n != 2 || !slices.Equal(second, []float32{.25, -.25}) || next.Sub(at) != time.Second/48000 {
		t.Fatal(n, err, second, next.Sub(at))
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil || f.starts != 1 || f.stops != 1 || f.closes != 1 {
		t.Fatal("capture lifecycle", err, f)
	}
}
func TestCaptureOverrunIsExplicitAndBounded(t *testing.T) {
	f := &fakeDriver{samples: make([]float32, 9602)}
	d := openFake(t, f)
	_, _, err := d.Read(context.Background(), make([]float32, 960))
	var discontinuity audio.InputDiscontinuityError
	if !errors.Is(err, ErrOverrun) || !errors.As(err, &discontinuity) || !discontinuity.AudioInputDiscontinuity() {
		t.Fatal("capture silently hid lost samples", err)
	}
}
func TestCaptureReadCancelsAndCloseJoinsWithoutAnotherSample(t *testing.T) {
	f := &fakeDriver{started: make(chan struct{})}
	d := openFake(t, f)
	done := make(chan error, 1)
	go func() { _, _, err := d.Read(context.Background(), make([]float32, 960)); done <- err }()
	<-f.started
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join pending input")
	}
	if f.closes != 1 {
		t.Fatal("input still owned")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := d.Read(ctx, make([]float32, 960)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
