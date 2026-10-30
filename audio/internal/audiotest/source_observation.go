package audiotest

import (
	"encoding/json"
	"errors"
	"strconv"
)

type sourceWireFormat struct {
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	BitDepth   int    `json:"bit_depth"`
}

type sourceObservedEvent struct {
	Type      string          `json:"type"`
	Available bool            `json:"available"`
	Source    json.RawMessage `json:"source"`
	Support   json.RawMessage `json:"support"`
	Timestamp string          `json:"timestamp"`
	Bytes     int64           `json:"bytes"`
	Payload   struct {
		Source sourceWireFormat `json:"source"`
	} `json:"payload"`
}

// Hash/sample content from the Python observer is deliberately not retained.
type sourceObservedStream struct {
	SampleRate     int    `json:"sample_rate"`
	Channels       int    `json:"channels"`
	BitDepth       int    `json:"bit_depth"`
	Bytes          int64  `json:"bytes"`
	Chunks         int64  `json:"chunks"`
	Ended          bool   `json:"ended"`
	FirstTimestamp string `json:"first_timestamp"`
}

type sourceObservation struct {
	Events    []sourceObservedEvent  `json:"events"`
	Streams   []sourceObservedStream `json:"streams"`
	ServerNow string                 `json:"server_now"`
}

func sourceObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil && len(object) == 0
}

// validateSourceObservation verifies independent-decoder geometry, FIFO ordering
// and broad server-clock-domain sanity. There is no signal-content criterion.
func validateSourceObservation(s sourceObservation, count int, startUS int64) (int64, error) {
	now, err := strconv.ParseInt(s.ServerNow, 10, 64)
	if err != nil || now <= 0 || startUS <= 0 || now < startUS {
		return 0, errors.New("invalid independent server clock observation")
	}
	if len(s.Streams) != count {
		return 0, errors.New("unexpected independent source stream count")
	}
	validTimestamp := func(raw string) (int64, bool) {
		n, err := strconv.ParseInt(raw, 10, 64)
		// Intentionally broad domain checks, never a physical latency assertion.
		return n, err == nil && n > 0 && n >= startUS-10_000_000 && n <= now+1_000_000
	}
	bytes := make([]int64, count)
	first := make([]int64, count)
	stream := -1
	open, hello, ready := false, false, false
	ends := 0
	var last int64
	for _, event := range s.Events {
		switch event.Type {
		case "client/hello":
			hello = sourceObject(event.Support)
		case "client/state":
			ready = event.Available && sourceObject(event.Source)
			if !ready && open {
				return 0, errors.New("source readiness withdrawn before stream end")
			}
		case "client-stream/start":
			if open || !hello || !ready || event.Payload.Source != (sourceWireFormat{"pcm", 48000, 2, 16}) {
				return 0, errors.New("source start outside ready state or wrong wire format")
			}
			stream++
			if stream >= count {
				return 0, errors.New("extra source start")
			}
			open, last = true, 0
		case "audio":
			if !open || event.Bytes <= 0 || event.Bytes > 3840 || event.Bytes%4 != 0 {
				return 0, errors.New("source audio outside lifecycle or invalid PCM16 frame geometry")
			}
			stamp, valid := validTimestamp(event.Timestamp)
			if !valid || stamp <= last {
				return 0, errors.New("invalid/non-increasing source server-domain timestamp")
			}
			if bytes[stream] == 0 {
				first[stream] = stamp
			}
			last = stamp
			bytes[stream] += event.Bytes
		case "client-stream/end":
			if !open {
				return 0, errors.New("source end without an open stream")
			}
			open = false
			ends++
		}
	}
	if open || stream+1 != count || ends != count {
		return 0, errors.New("source FIFO lifecycle incomplete")
	}
	var frames int64
	for i, decoded := range s.Streams {
		stamp, valid := validTimestamp(decoded.FirstTimestamp)
		if !decoded.Ended || decoded.SampleRate != 48000 || decoded.Channels != 2 || decoded.BitDepth != 16 || decoded.Chunks <= 0 || decoded.Bytes != bytes[i] || decoded.Bytes < 48000*4 || !valid || stamp != first[i] {
			return 0, errors.New("independent decoder format/count/timestamp/drain mismatch or insufficient frames")
		}
		frames += decoded.Bytes / 4
	}
	return frames, nil
}
