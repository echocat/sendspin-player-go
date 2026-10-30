package alsa

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/echocat/sendspin-player-go/audio"
)

// All capture orchestration is test-only. Neither importing the SDK/backend nor
// ordinary tests can start arecord. The live entry point is separately gated.
type duplexRecorder interface {
	Start() error
	Wait() error
	Stop()
}
type duplexResult struct {
	Schema              int    `json:"schema"`
	Scope               string `json:"scope"`
	Device              string `json:"device"`
	GOOS                string `json:"goos"`
	Seconds             int    `json:"requested_capture_seconds"`
	Rate                int    `json:"capture_sample_rate"`
	Channels            int    `json:"capture_channels"`
	Format              string `json:"requested_capture_format"`
	Output              Info   `json:"negotiated_output"`
	ZeroFrames          int64  `json:"submitted_output_zero_frames"`
	CaptureFrames       int64  `json:"capture_frames"`
	CaptureBytes        int64  `json:"capture_file_bytes"`
	PhysicalConformance bool   `json:"physical_conformance"`
	CaptureOpened       bool   `json:"capture_process_started"`
	Status              string `json:"status"`
	Error               string `json:"error,omitempty"`
}

func duplexLoop(ctx context.Context, open func(context.Context, audio.Format) (audio.Device, error), recorder duplexRecorder) (frames int64, info Info, err error) {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	if err = work.Err(); err != nil {
		return 0, info, err
	}
	d, err := open(work, audio.Format{SampleRate: 48000, Channels: 2})
	if err != nil {
		return 0, info, err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	if native, ok := d.(Device); ok {
		info = native.Info()
	}
	if _, err = d.Next(work); err != nil {
		return 0, info, err
	}
	writerCtx, stopWriter := context.WithCancel(work)
	defer stopWriter()
	type written struct {
		frames int64
		err    error
	}
	writes := make(chan written, 1)
	go func() {
		var total int64
		var writeErr error
		defer func() { writes <- written{total, writeErr} }()
		zeros := make([]float32, 960) // 10ms at 48kHz/stereo; never nonzero media.
		for {
			if writeErr = writerCtx.Err(); writeErr != nil {
				return
			}
			if _, writeErr = d.Next(writerCtx); writeErr != nil {
				return
			}
			if writeErr = d.Write(writerCtx, zeros); writeErr != nil {
				return
			}
			total += 480
		}
	}()
	if err = recorder.Start(); err != nil {
		stopWriter()
		w := <-writes
		return w.frames, info, err
	}
	defer recorder.Stop()
	recorded := make(chan error, 1)
	go func() { recorded <- recorder.Wait() }()
	var w written
	select {
	case err = <-recorded:
		stopWriter()
		w = <-writes
		if w.err != nil && !errors.Is(w.err, context.Canceled) {
			err = errors.Join(err, w.err)
		}
	case w = <-writes:
		err = w.err
		recorder.Stop()
		cancel()
		err = errors.Join(err, <-recorded)
	case <-ctx.Done():
		err = ctx.Err()
		recorder.Stop()
		cancel()
		stopWriter()
		w = <-writes
		err = errors.Join(err, <-recorded)
	}
	return w.frames, info, err
}

func createDuplexArtifacts(root string) (dir string, capture, diagnostics *os.File, err error) {
	if !filepath.IsAbs(root) {
		return "", nil, nil, errors.New("duplex artifact directory must be absolute")
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", nil, nil, err
	}
	dir, err = os.MkdirTemp(root, "capture-")
	if err != nil {
		return dir, nil, nil, err
	}
	capture, err = os.OpenFile(filepath.Join(dir, "capture.wav"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return dir, nil, nil, err
	}
	diagnostics, err = os.OpenFile(filepath.Join(dir, "arecord.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = capture.Close()
		return dir, nil, nil, err
	}
	return dir, capture, diagnostics, nil
}
func writeDuplexReport(dir string, r duplexResult) (err error) {
	f, err := os.OpenFile(filepath.Join(dir, "report.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	e := json.NewEncoder(f)
	e.SetIndent("", "  ")
	return e.Encode(r)
}

// Check container geometry only. Never print, decode or upload recorded samples.
func captureGeometry(f *os.File) (frames, bytes int64, err error) {
	stat, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	bytes = stat.Size()
	if bytes < 44 || bytes > 4<<20 {
		return 0, bytes, errors.New("capture size outside ten-second stereo PCM bounds")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return 0, bytes, err
	}
	var head [12]byte
	if _, err = io.ReadFull(f, head[:]); err != nil {
		return 0, bytes, err
	}
	if string(head[:4]) != "RIFF" || string(head[8:]) != "WAVE" || int64(binary.LittleEndian.Uint32(head[4:8]))+8 != bytes {
		return 0, bytes, errors.New("capture did not finalize a complete RIFF/WAVE file")
	}
	remaining := bytes - 12
	format, data := false, false
	for remaining > 0 {
		var chunk [8]byte
		if remaining < 8 {
			return 0, bytes, io.ErrUnexpectedEOF
		}
		if _, err = io.ReadFull(f, chunk[:]); err != nil {
			return 0, bytes, err
		}
		remaining -= 8
		n := int64(binary.LittleEndian.Uint32(chunk[4:]))
		if n+n%2 > remaining {
			return 0, bytes, errors.New("capture RIFF extent is incomplete")
		}
		switch string(chunk[:4]) {
		case "fmt ":
			if format || data || n < 16 || n > 4096 {
				return 0, bytes, errors.New("invalid capture fmt chunk")
			}
			b := make([]byte, int(n))
			if _, err = io.ReadFull(f, b); err != nil {
				return 0, bytes, err
			}
			pcm := binary.LittleEndian.Uint16(b) == 1
			if binary.LittleEndian.Uint16(b) == 0xfffe && len(b) >= 40 && binary.LittleEndian.Uint16(b[16:]) >= 22 && binary.LittleEndian.Uint16(b[18:]) == 24 {
				pcm = string(b[24:40]) == string([]byte{1, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71})
			}
			if !pcm || binary.LittleEndian.Uint16(b[2:]) != 2 || binary.LittleEndian.Uint32(b[4:]) != 48000 || binary.LittleEndian.Uint32(b[8:]) != 288000 || binary.LittleEndian.Uint16(b[12:]) != 6 || binary.LittleEndian.Uint16(b[14:]) != 24 {
				return 0, bytes, errors.New("capture is not exact 48kHz/stereo/packed PCM24")
			}
			format = true
		case "data":
			if !format || data || n%6 != 0 {
				return 0, bytes, errors.New("invalid capture data chunk")
			}
			frames = n / 6
			data = true
			if _, err = f.Seek(n, io.SeekCurrent); err != nil {
				return 0, bytes, err
			}
		default:
			if _, err = f.Seek(n, io.SeekCurrent); err != nil {
				return 0, bytes, err
			}
		}
		if n%2 != 0 {
			if _, err = f.Seek(1, io.SeekCurrent); err != nil {
				return 0, bytes, err
			}
		}
		remaining -= n + n%2
	}
	if !data || frames != 480000 {
		return frames, bytes, fmt.Errorf("capture has %d frames, expected 480000", frames)
	}
	return frames, bytes, nil
}
