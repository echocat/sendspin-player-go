package main

import (
	"context"
	"errors"
	"math"
)

var errWorkBudget = errors.New("correlation work budget exceeded; narrow the initial search window")

const maxProducts = 250_000_000

type options struct {
	SearchMS, TrackMS, MinCorrelation, MinSeparation, MaxResidualMS float64
	Source                                                          string
}

func (o options) validate() error {
	for _, v := range []float64{o.SearchMS, o.TrackMS, o.MinCorrelation, o.MinSeparation, o.MaxResidualMS} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("nonfinite analysis option")
		}
	}
	if o.SearchMS <= 0 || o.SearchMS > 5000 || o.TrackMS < 1 || o.TrackMS > o.SearchMS || o.MinCorrelation < .5 || o.MinCorrelation > 1 || o.MinSeparation < 0 || o.MinSeparation > .5 || o.MaxResidualMS <= 0 || o.MaxResidualMS > 100 {
		return errors.New("invalid search/correlation/linearity limits")
	}
	if o.Source != "unknown" && o.Source != "synthetic" && o.Source != "recording" {
		return errors.New("source must be unknown, synthetic or recording")
	}
	return nil
}
func defaultOptions() options {
	return options{SearchMS: 2000, TrackMS: 50, MinCorrelation: .7, MinSeparation: .08, MaxResidualMS: .5, Source: "unknown"}
}

type detection struct {
	ID             int     `json:"marker_id"`
	ReferenceFrame float64 `json:"reference_marker_center_frame"`
	CaptureFrame   float64 `json:"capture_marker_center_frame"`
	Correlation    float64 `json:"correlation"`
	RunnerUp       float64 `json:"distinct_runner_up_absolute_correlation"`
	Status         string  `json:"status"`
}
type fit struct {
	FileOffsetFrames  float64 `json:"relative_file_offset_frames"`
	FileOffsetMS      float64 `json:"relative_file_offset_ms"`
	RelativePPM       float64 `json:"capture_reference_ratio_ppm"`
	MaxResidualMS     float64 `json:"max_linear_residual_ms"`
	RMSResidualFrames float64 `json:"rms_linear_residual_frames"`
	SpanSeconds       float64 `json:"marker_span_seconds"`
}
type driftWindow struct {
	First int     `json:"first_marker"`
	Last  int     `json:"last_marker"`
	PPM   float64 `json:"capture_reference_ratio_ppm"`
}
type level struct {
	Frames int     `json:"frames"`
	RMS    float64 `json:"rms"`
	ACRMS  float64 `json:"ac_rms"`
	DC     float64 `json:"dc"`
	Peak   float64 `json:"peak"`
}
type report struct {
	Version                 int           `json:"version"`
	Status                  string        `json:"status"`
	Scope                   string        `json:"scope"`
	Source                  string        `json:"input_provenance_claim"`
	PhysicalConformance     bool          `json:"physical_conformance"`
	HardwareOpened          bool          `json:"hardware_opened"`
	AbsoluteOutputLatencyMS *float64      `json:"absolute_output_latency_ms"`
	Reference               wave          `json:"reference"`
	Capture                 wave          `json:"capture"`
	Detections              []detection   `json:"markers"`
	Fit                     *fit          `json:"linear_timing_estimate"`
	DriftWindows            []driftWindow `json:"five_marker_windows"`
	Tail                    *level        `json:"post_last_burst_capture_level"`
	Warnings                []string      `json:"warnings"`
	Products                int64         `json:"correlation_sample_products"`
	Error                   string        `json:"error,omitempty"`
}

func analyze(ctx context.Context, m manifest, reference, capture wave, o options) (report, error) {
	r := report{Version: 1, Status: "inconclusive", Source: o.Source, Scope: "Offline WAV sample-coordinate alignment only. File origins and combined DAC/ADC latency are not separated. Tail is post-reference-burst energy, not a flush/stop measurement.", Reference: reference, Capture: capture}
	r.Reference.Frames = len(reference.Samples)
	r.Capture.Frames = len(capture.Samples)
	fail := func(err error) (report, error) { r.Error = err.Error(); return r, err }
	if err := o.validate(); err != nil {
		return fail(err)
	}
	if err := m.validate(reference); err != nil {
		return fail(err)
	}
	if capture.Rate != reference.Rate {
		return fail(errors.New("nominal WAV rates must match; no implicit resampling is performed"))
	}
	if capture.Channels < 1 || capture.Channels > 8 || capture.Channel < 0 || capture.Channel >= capture.Channels {
		return fail(errors.New("invalid selected capture channel"))
	}
	if len(capture.Samples) == 0 || len(capture.Samples) > maxFrames {
		return fail(errors.New("invalid capture sample bounds"))
	}
	for _, w := range []wave{reference, capture} {
		for i, v := range w.Samples {
			if i%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return fail(err)
				}
			}
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < -1 || v > 1 {
				return fail(errors.New("invalid normalized input sample"))
			}
		}
	}
	if capture.FullScale > 0 {
		r.Warnings = append(r.Warnings, "capture_contains_digital_full_scale_samples; analog clipping below full scale cannot be excluded")
	}
	var matched []detection
	budget := int64(maxProducts)
	for _, p := range m.Markers {
		center := float64(p.Frames-1) / 2
		prediction := float64(p.Start)
		radius := o.SearchMS * float64(m.Rate) / 1000
		if len(matched) > 0 {
			prediction += matched[0].CaptureFrame - matched[0].ReferenceFrame
			radius = o.TrackMS * float64(m.Rate) / 1000
		}
		if len(matched) >= 2 {
			f := fitMarkers(matched, m.Rate)
			prediction = (float64(p.Start)+center)*(1+f.RelativePPM/1e6) + f.FileOffsetFrames - center
		}
		low := max(0, int(math.Floor(prediction-radius)))
		high := min(len(capture.Samples)-p.Frames, int(math.Ceil(prediction+radius)))
		at, score, runner, err := locate(ctx, capture.Samples, reference.Samples[p.Start:p.Start+p.Frames], low, high, &budget)
		r.Products = maxProducts - budget
		if err != nil {
			return fail(err)
		}
		// Correlation under a clock stretch aligns the marker's interior, not
		// its leading edge. Fit matching centers in both coordinate systems so
		// relative rate does not bias the extrapolated file-origin offset.
		d := detection{ID: p.ID, ReferenceFrame: float64(p.Start) + center, CaptureFrame: at + center, Correlation: score, RunnerUp: runner, Status: "not_found"}
		if math.Abs(score) >= o.MinCorrelation {
			d.Status = "matched"
			if math.Abs(score)-runner < o.MinSeparation {
				d.Status = "ambiguous"
			}
		}
		r.Detections = append(r.Detections, d)
		if d.Status == "matched" {
			matched = append(matched, d)
		}
	}
	if len(matched) < 3 {
		r.Warnings = append(r.Warnings, "fewer_than_three_unambiguous_markers")
		return r, nil
	}
	f := fitMarkers(matched, m.Rate)
	r.Fit = &f
	if len(matched) != len(m.Markers) {
		r.Warnings = append(r.Warnings, "missing_or_ambiguous_markers")
	}
	if f.SpanSeconds < 2 {
		r.Warnings = append(r.Warnings, "drift_baseline_shorter_than_two_seconds")
	}
	if f.MaxResidualMS > o.MaxResidualMS {
		r.Warnings = append(r.Warnings, "timing_is_not_consistent_with_one_affine_clock_model")
	}
	polarity := matched[0].Correlation < 0
	for _, d := range matched[1:] {
		if (d.Correlation < 0) != polarity {
			r.Warnings = append(r.Warnings, "marker_polarity_changed")
			break
		}
	}
	for i := 0; i+5 <= len(matched); i++ {
		window := matched[i : i+5]
		wf := fitMarkers(window, m.Rate)
		r.DriftWindows = append(r.DriftWindows, driftWindow{First: window[0].ID, Last: window[4].ID, PPM: wf.RelativePPM})
	}
	last := m.Markers[len(m.Markers)-1]
	tailStart := int(math.Ceil(f.FileOffsetFrames+(1+f.RelativePPM/1e6)*float64(last.Start+last.Frames))) + m.Rate/200
	if tailStart >= 0 && tailStart < len(capture.Samples) {
		v := levels(capture.Samples[tailStart:])
		r.Tail = &v
	}
	if len(r.Warnings) == 0 {
		r.Status = "aligned"
	}
	return r, nil
}

type needle struct {
	values []float64
	energy float64
	step   int
}

func makeNeedle(samples []float32, step int) (needle, error) {
	n := needle{step: step}
	var mean float64
	for i := 0; i < len(samples); i += step {
		n.values = append(n.values, float64(samples[i]))
		mean += float64(samples[i])
	}
	mean /= float64(len(n.values))
	for i := range n.values {
		n.values[i] -= mean
		n.energy += n.values[i] * n.values[i]
	}
	if n.energy < 1e-15 {
		return n, errors.New("reference marker has no usable AC energy")
	}
	return n, nil
}
func correlation(c []float32, at int, n needle, budget *int64) (float64, error) {
	if *budget < int64(len(n.values)) {
		return 0, errWorkBudget
	}
	*budget -= int64(len(n.values))
	var sum, squares, dot float64
	for i, v := range n.values {
		sample := float64(c[at+i*n.step])
		sum += sample
		squares += sample * sample
		dot += v * sample
	}
	energy := squares - sum*sum/float64(len(n.values))
	if energy <= 1e-18 {
		return 0, nil
	}
	return max(-1, min(1, dot/math.Sqrt(energy*n.energy))), nil
}

func locate(ctx context.Context, capture, template []float32, low, high int, budget *int64) (float64, float64, float64, error) {
	if high < low {
		return 0, 0, 0, nil
	}
	coarse, err := makeNeedle(template, 4)
	if err != nil {
		return 0, 0, 0, err
	}
	full, err := makeNeedle(template, 1)
	if err != nil {
		return 0, 0, 0, err
	}
	scores := make([]float64, (high-low)/4+1)
	for i := range scores {
		if i%128 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, 0, 0, err
			}
		}
		score, err := correlation(capture, low+i*4, coarse, budget)
		if err != nil {
			return 0, 0, 0, err
		}
		scores[i] = score
	}
	type peak struct {
		at    int
		score float64
	}
	peaks := make([]peak, 0, 4)
	for range 4 {
		best := -1
		for i, v := range scores {
			if math.IsNaN(v) {
				continue
			}
			if best < 0 || math.Abs(v) > math.Abs(scores[best]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		center := low + best*4
		p := peak{at: center}
		for at := max(low, center-4); at <= min(high, center+4); at++ {
			score, err := correlation(capture, at, full, budget)
			if err != nil {
				return 0, 0, 0, err
			}
			if math.Abs(score) > math.Abs(p.score) {
				p = peak{at: at, score: score}
			}
		}
		peaks = append(peaks, p)
		for i := max(0, best-4*chipFrames/4); i <= min(len(scores)-1, best+4*chipFrames/4); i++ {
			scores[i] = math.NaN()
		}
	}
	if len(peaks) == 0 {
		return 0, 0, 0, nil
	}
	best := 0
	for i := range peaks {
		if math.Abs(peaks[i].score) > math.Abs(peaks[best].score) {
			best = i
		}
	}
	p := peaks[best]
	runner := 0.0
	for i, other := range peaks {
		if i != best && absInt(other.at-p.at) > 2*chipFrames {
			runner = max(runner, math.Abs(other.score))
		}
	}
	position := float64(p.at)
	if p.at > 0 && p.at < len(capture)-len(template) {
		left, err := correlation(capture, p.at-1, full, budget)
		if err != nil {
			return 0, 0, 0, err
		}
		right, err := correlation(capture, p.at+1, full, budget)
		if err != nil {
			return 0, 0, 0, err
		}
		l, m, r := math.Abs(left), math.Abs(p.score), math.Abs(right)
		denom := l - 2*m + r
		if denom < -1e-12 {
			delta := .5 * (l - r) / denom
			if math.Abs(delta) <= 1 {
				position += delta
			}
		}
	}
	return position, p.score, runner, nil
}
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func fitMarkers(points []detection, rate int) fit {
	var meanX, meanY float64
	for _, p := range points {
		meanX += float64(p.ReferenceFrame)
		meanY += p.CaptureFrame
	}
	meanX /= float64(len(points))
	meanY /= float64(len(points))
	var xx, xy float64
	for _, p := range points {
		x := float64(p.ReferenceFrame) - meanX
		xx += x * x
		xy += x * (p.CaptureFrame - meanY)
	}
	slope := xy / xx
	offset := meanY - slope*meanX
	f := fit{FileOffsetFrames: offset, FileOffsetMS: offset * 1000 / float64(rate), RelativePPM: (slope - 1) * 1e6, SpanSeconds: float64(points[len(points)-1].ReferenceFrame-points[0].ReferenceFrame) / float64(rate)}
	var squares float64
	for _, p := range points {
		residual := p.CaptureFrame - offset - slope*float64(p.ReferenceFrame)
		squares += residual * residual
		f.MaxResidualMS = max(f.MaxResidualMS, math.Abs(residual)*1000/float64(rate))
	}
	f.RMSResidualFrames = math.Sqrt(squares / float64(len(points)))
	return f
}
func levels(samples []float32) level {
	v := level{Frames: len(samples)}
	if len(samples) == 0 {
		return v
	}
	var sum, squares float64
	for _, s := range samples {
		f := float64(s)
		sum += f
		squares += f * f
		v.Peak = max(v.Peak, math.Abs(f))
	}
	v.DC = sum / float64(len(samples))
	v.RMS = math.Sqrt(squares / float64(len(samples)))
	v.ACRMS = math.Sqrt(max(0, squares/float64(len(samples))-v.DC*v.DC))
	return v
}
