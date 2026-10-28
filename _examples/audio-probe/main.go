// Command audio-probe is a standalone silence-only native output validation example.
// It never opens an input/monitor or claims physical synchronization evidence.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/oto"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	log "github.com/echocat/slf4g"
	_ "github.com/echocat/slf4g/native"
)

type nativeDevice struct {
	Name           string `json:"name"`
	HostAPI        string `json:"host_api"`
	OutputChannels int    `json:"output_channels"`
}
type operation struct {
	Cycle      int    `json:"cycle"`
	Name       string `json:"name"`
	Error      string `json:"error,omitempty"`
	DurationNS int64  `json:"duration_ns"`
}
type report struct {
	Schema              int            `json:"schema"`
	GOOS                string         `json:"goos"`
	GOARCH              string         `json:"goarch"`
	GoVersion           string         `json:"go_version"`
	Backend             string         `json:"backend"`
	Timing              string         `json:"timing"`
	Clock               string         `json:"clock_source"`
	Status              string         `json:"status"`
	Scope               string         `json:"scope"`
	PhysicalConformance bool           `json:"physical_conformance"`
	CaptureOpened       bool           `json:"capture_opened"`
	Started             time.Time      `json:"started"`
	Finished            time.Time      `json:"finished"`
	Devices             []nativeDevice `json:"output_devices,omitempty"`
	Operations          []operation    `json:"operations,omitempty"`
	Error               string         `json:"error,omitempty"`
}

func output(backend string) (audio.Output, []nativeDevice, string, error) {
	if backend == "oto" {
		return oto.Output{AllowApproximate: true}, nil, "estimated; software-only flush", nil
	}
	if backend == "portaudio" {
		return portaudioOutput()
	}
	return nil, nil, "", fmt.Errorf("unknown backend %q", backend)
}

func smoke(ctx context.Context, o audio.Output, cycles int, r *report) error {
	format := audio.Format{SampleRate: 48000, Channels: 2}
	for cycle := 1; cycle <= cycles; cycle++ {
		at := monotime.Now()
		d, err := o.Open(ctx, format)
		r.Operations = append(r.Operations, operation{Cycle: cycle, Name: "open", DurationNS: int64(monotime.Since(at))})
		if err != nil {
			return err
		}
		err = func() error {
			record := func(name string, fn func() error) error {
				at := monotime.Now()
				err := fn()
				op := operation{Cycle: cycle, Name: name, DurationNS: int64(monotime.Since(at))}
				if err != nil {
					op.Error = err.Error()
				}
				r.Operations = append(r.Operations, op)
				return err
			}
			if err := record("first-next", func() error { _, err := d.Next(ctx); return err }); err != nil {
				return err
			}
			// Twenty 10-ms zero blocks. No tones, input capture, volume changes,
			// default-device changes or global audio-server configuration.
			if err := record("write-silence", func() error {
				for range 20 {
					if err := d.Write(ctx, make([]float32, 960)); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
			if err := record("wait-reported-queue", func() error {
				next, err := d.Next(ctx)
				if err != nil {
					return err
				}
				wait := monotime.Until(next)
				if wait <= 0 {
					return nil
				}
				if wait > 3*time.Second {
					return errors.New("reported output queue exceeds smoke-test bound")
				}
				timer := time.NewTimer(wait)
				defer timer.Stop()
				select {
				case <-timer.C:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}); err != nil {
				return err
			}
			if err := record("flush", func() error { return d.Flush(ctx) }); err != nil {
				return err
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := record("canceled-write", func() error {
				if err := d.Write(canceled, make([]float32, 960)); !errors.Is(err, context.Canceled) {
					return fmt.Errorf("cancellation not preserved: %v", err)
				}
				return nil
			}); err != nil {
				return err
			}
			return nil
		}()
		at = monotime.Now()
		closeErr := d.Close()
		r.Operations = append(r.Operations, operation{Cycle: cycle, Name: "close", DurationNS: int64(monotime.Since(at))})
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		if err = d.Close(); err != nil {
			return fmt.Errorf("idempotent close: %w", err)
		}
	}
	return nil
}

func writeReport(path string, r report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func main() {
	backend := flag.String("backend", "oto", "oto or portaudio")
	live := flag.Bool("run-silence", false, "Explicitly open the default output and write only silence")
	cycles := flag.Int("cycles", 3, "Open/flush/close/reopen cycles (1..20)")
	path := flag.String("output", "", "JSON result path; empty writes stdout")
	flag.Parse()
	r := report{Schema: 1, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), Backend: *backend, Clock: monotime.Source(), Started: time.Now().UTC(), Status: "not-run", Scope: "silence-only native output lifecycle; no input capture, physical latency, sync, speed, audibility or downstream-silence proof"}
	if *path != "" {
		initial := r
		initial.Status = "running"
		if err := writeReport(*path, initial); err != nil {
			log.WithError(err).Error("Cannot initialize probe report")
			os.Exit(1)
		}
	}
	if *backend != "oto" && *backend != "portaudio" {
		r.Status, r.Error = "fail", "backend must be oto or portaudio"
	} else if *cycles < 1 || *cycles > 20 {
		r.Status, r.Error = "fail", "cycles must be in 1..20"
	} else {
		o, devices, timing, err := output(*backend)
		r.Devices, r.Timing = devices, timing
		if err != nil {
			r.Status, r.Error = "blocked", err.Error()
		} else if *live {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err = smoke(ctx, o, *cycles, &r)
			cancel()
			if err != nil {
				r.Status, r.Error = "fail", err.Error()
			} else {
				r.Status = "pass"
			}
		}
	}
	r.Finished = time.Now().UTC()
	if err := writeReport(*path, r); err != nil {
		log.WithError(err).Error("Cannot write probe report")
		os.Exit(1)
	}
	if r.Status == "fail" {
		os.Exit(1)
	}
	if r.Status == "blocked" {
		os.Exit(2)
	}
}
