// Command loopback generates reference WAV files and analyzes recordings offline.
// It contains no audio, capture, mixer, network or native-library integration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
)

func writeJSON(path string, value any, stdout io.Writer) (err error) {
	w := stdout
	if path != "" {
		f, err := createPrivate(path)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, f.Close()) }()
		w = f
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
func loadManifest(path string) (manifest, error) {
	var m manifest
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil {
		return m, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 128<<10 {
		return m, errors.New("manifest must be a regular file <=128 KiB")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 128<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&m); err != nil {
		return m, err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return m, errors.New("manifest contains trailing JSON")
	}
	return m, nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: loopback generate -out reference.wav | analyze -reference reference.wav -manifest reference.wav.json -capture capture.wav [-out report.json]")
	}
	switch args[0] {
	case "generate":
		fs := flag.NewFlagSet("generate", flag.ContinueOnError)
		fs.SetOutput(stderr)
		out := fs.String("out", "", "Required new WAV path; existing files are never overwritten")
		meta := fs.String("manifest", "", "New manifest path; default <out>.json")
		rate := fs.Int("rate", 48000, "Nominal WAV sample rate (8..192kHz)")
		seconds := fs.Int("seconds", 20, "Reference duration (3..120 seconds)")
		channels := fs.Int("channels", 2, "WAV channels (1..8); only the selected channel contains a signal")
		channel := fs.Int("channel", 0, "Zero-based signal output channel; other channels stay silent")
		amplitude := fs.Float64("amplitude", .05, "Peak digital amplitude in (0,0.1]; NOT a safe microphone wiring/level guarantee")
		seed := fs.Uint64("seed", 1, "Deterministic reference seed, not cryptographic randomness")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 0 || *out == "" {
			return errors.New("generate requires -out and no positional arguments")
		}
		if *meta == "" {
			*meta = *out + ".json"
		}
		a, _ := filepath.Abs(*out)
		b, _ := filepath.Abs(*meta)
		if a == b {
			return errors.New("WAV and manifest paths must differ")
		}
		samples, m, err := referenceSignal(*rate, *seconds, *channels, *channel, *amplitude, *seed)
		if err != nil {
			return err
		}
		m.SHA256, err = writeWave(ctx, *out, *rate, *channels, *channel, samples)
		if err != nil {
			return err
		}
		if err = writeJSON(*meta, m, stdout); err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, "Reference WAV and manifest created. No audio device opened; no playback or recording performed.")
		return err
	case "analyze":
		fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
		fs.SetOutput(stderr)
		o := defaultOptions()
		referencePath := fs.String("reference", "", "Required reference WAV")
		manifestPath := fs.String("manifest", "", "Required reference manifest")
		capturePath := fs.String("capture", "", "Required recording/synthetic WAV; no recording is started")
		channel := fs.Int("capture-channel", 0, "Zero-based input channel, selected explicitly without downmixing")
		out := fs.String("out", "", "New JSON report path; empty writes JSON to stdout")
		fs.Float64Var(&o.SearchMS, "search-ms", o.SearchMS, "Initial positive/negative file-offset search bound (max 5000ms)")
		fs.Float64Var(&o.TrackMS, "track-ms", o.TrackMS, "Subsequent marker search radius around the fitted model")
		fs.Float64Var(&o.MinCorrelation, "min-correlation", o.MinCorrelation, "Minimum absolute normalized marker correlation")
		fs.Float64Var(&o.MinSeparation, "min-separation", o.MinSeparation, "Minimum separation from a distinct correlation peak")
		fs.Float64Var(&o.MaxResidualMS, "max-residual-ms", o.MaxResidualMS, "Linearity diagnostic limit; NOT an RC1 timing acceptance threshold")
		fs.StringVar(&o.Source, "source", o.Source, "Input provenance claim: unknown, synthetic, recording; not authenticated by WAV")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 0 || *referencePath == "" || *manifestPath == "" || *capturePath == "" {
			return errors.New("analyze requires reference, manifest and capture paths")
		}
		if err := o.validate(); err != nil {
			return err
		}
		m, err := loadManifest(*manifestPath)
		if err != nil {
			return err
		}
		reference, err := readWave(ctx, *referencePath, m.Channel)
		if err != nil {
			return err
		}
		capture, err := readWave(ctx, *capturePath, *channel)
		if err != nil {
			return err
		}
		r, analysisErr := analyze(ctx, m, reference, capture, o)
		if err = writeJSON(*out, r, stdout); err != nil {
			return err
		}
		if analysisErr != nil {
			return analysisErr
		}
		if r.Status != "aligned" {
			return errors.New("offline alignment is inconclusive; inspect report warnings/marker results")
		}
		return nil
	case "-h", "--help", "help":
		_, err := fmt.Fprintln(stdout, "Offline loopback WAV tools: generate -h or analyze -h. No device access, playback or capture.")
		return err
	default:
		return errors.New("unknown command; use generate or analyze")
	}
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
