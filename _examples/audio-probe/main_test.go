package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

type probeOutput struct{ opens, writes, flushes, closes int }
type probeDevice struct {
	p      *probeOutput
	closed bool
}

func (p *probeOutput) Open(_ context.Context, f audio.Format) (audio.Device, error) {
	if f != (audio.Format{SampleRate: 48000, Channels: 2}) {
		return nil, errors.New("unexpected probe format")
	}
	p.opens++
	return &probeDevice{p: p}, nil
}
func (d *probeDevice) Next(ctx context.Context) (time.Time, error) { return time.Now(), ctx.Err() }
func (d *probeDevice) Write(ctx context.Context, x []float32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, v := range x {
		if v != 0 {
			return errors.New("probe attempted non-silent output")
		}
	}
	d.p.writes++
	return nil
}
func (d *probeDevice) Flush(context.Context) error { d.p.flushes++; return nil }
func (d *probeDevice) Close() error {
	if !d.closed {
		d.closed = true
		d.p.closes++
	}
	return nil
}

func TestProbeEmitsOnlySilenceAndClosesEveryCycle(t *testing.T) {
	p := &probeOutput{}
	r := report{}
	if err := smoke(context.Background(), p, 3, &r); err != nil {
		t.Fatal(err)
	}
	if p.opens != 3 || p.closes != 3 || p.flushes != 3 || p.writes != 60 || r.CaptureOpened || r.PhysicalConformance {
		t.Fatal("probe lifecycle/privacy/scope mismatch", p, r)
	}
	if _, _, _, err := output("unknown"); err == nil {
		t.Fatal("unknown backend accepted")
	}
}
