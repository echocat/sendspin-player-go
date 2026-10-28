package sendspin

import (
	"encoding/json"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func decodeMetadataState(data []byte) (Metadata, error) {
	var wire struct {
		Metadata
		Track *int `json:"track"`
	}
	if err := protocol.Decode(data, &wire, "timestamp"); err != nil {
		return Metadata{}, err
	}
	if wire.Track != nil {
		if *wire.Track < 1 {
			return Metadata{}, protocol.ErrProtocol
		}
		wire.Metadata.Track = *wire.Track
	}
	metadata := wire.Metadata
	if metadata.Progress != nil {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		if err := protocol.Decode(fields["progress"], metadata.Progress, "track_progress", "track_duration", "playback_speed"); err != nil {
			return Metadata{}, err
		}
		if metadata.Progress.PositionMS < 0 || metadata.Progress.DurationMS < 0 || metadata.Progress.Speed < 0 {
			return Metadata{}, protocol.ErrProtocol
		}
	}
	return metadata, nil
}

func decodeColorState(data []byte) (Colors, error) {
	var colors Colors
	if err := protocol.Decode(data, &colors, "timestamp"); err != nil {
		return Colors{}, err
	}
	for _, rgb := range [][]int{colors.BackgroundDark, colors.BackgroundLight, colors.Primary, colors.Accent, colors.OnDark, colors.OnLight} {
		if rgb == nil {
			continue
		}
		if len(rgb) != 3 {
			return Colors{}, protocol.ErrProtocol
		}
		for _, value := range rgb {
			if value < 0 || value > 255 {
				return Colors{}, protocol.ErrProtocol
			}
		}
	}
	return colors, nil
}
