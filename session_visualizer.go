package sendspin

import (
	"encoding/json"
	"slices"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func decodeVisualizerStream(data []byte, requested VisualizerConfig) (VisualizerConfig, bool, error) {
	var wire struct {
		VisualizerConfig
		TracksDownbeats *bool `json:"tracks_downbeats"`
	}
	if err := protocol.Decode(data, &wire, "types", "rate_max"); err != nil {
		return VisualizerConfig{}, false, err
	}
	if wire.Types == nil || wire.RateMax < 0 || wire.RateMax > requested.RateMax {
		return VisualizerConfig{}, false, protocol.ErrProtocol
	}
	seen := map[string]bool{}
	types := make([]string, 0, len(requested.Types))
	for _, typ := range wire.Types {
		if !slices.Contains(requested.Types, typ) {
			return VisualizerConfig{}, false, protocol.ErrProtocol
		}
		// Accept duplicate list entries, but retain only the effective set so
		// per-frame type lookups cannot grow with redundant peer input.
		if !seen[typ] {
			types = append(types, typ)
			seen[typ] = true
		}
	}
	wire.Types = types
	if seen["beat"] != (wire.TracksDownbeats != nil) || seen["spectrum"] != (wire.Spectrum != nil) {
		return VisualizerConfig{}, false, protocol.ErrProtocol
	}
	if seen["spectrum"] {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		if err := protocol.Decode(fields["spectrum"], wire.Spectrum, "n_disp_bins", "scale", "f_min", "f_max"); err != nil {
			return VisualizerConfig{}, false, err
		}
		if requested.Spectrum == nil || *wire.Spectrum != *requested.Spectrum {
			return VisualizerConfig{}, false, protocol.ErrProtocol
		}
	}
	return wire.VisualizerConfig, wire.TracksDownbeats != nil && *wire.TracksDownbeats, nil
}
