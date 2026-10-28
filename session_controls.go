package sendspin

import (
	"slices"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

func decodeControllerState(data []byte) (ControllerState, error) {
	var state ControllerState
	if err := protocol.Decode(data, &state, "supported_commands", "volume", "muted", "repeat", "shuffle"); err != nil {
		return ControllerState{}, err
	}
	if state.SupportedCommands == nil || state.Volume < 0 || state.Volume > 100 || !slices.Contains([]string{"off", "one", "all"}, state.Repeat) {
		return ControllerState{}, protocol.ErrProtocol
	}
	for _, command := range state.SupportedCommands {
		if !slices.Contains(controllerCommands, command) {
			return ControllerState{}, protocol.ErrProtocol
		}
	}
	if state.SeekMaxMS != nil && *state.SeekMaxMS < 0 {
		return ControllerState{}, protocol.ErrProtocol
	}
	if slices.Contains(state.SupportedCommands, "seek") && state.SeekMaxMS == nil {
		return ControllerState{}, protocol.ErrProtocol
	}
	return state, nil
}

// decodePlayerCommand first validates the discriminator, then applies its own
// ignore rule. Unoffered commands cannot make their argument bodies actionable.
// Known command arguments remain strictly typed and conditionally required.
func decodePlayerCommand(data []byte) (string, any, error) {
	var discriminator struct {
		Command string `json:"command"`
	}
	if err := protocol.Decode(data, &discriminator, "command"); err != nil {
		return "", nil, err
	}
	switch discriminator.Command {
	case "volume", "mute", "set_output_delay":
	default:
		return "", nil, nil
	}
	var command struct {
		Command string `json:"command"`
		Volume  *int   `json:"volume"`
		Mute    *bool  `json:"mute"`
		Delay   *int64 `json:"output_delay_ms"`
	}
	if err := protocol.Decode(data, &command, "command"); err != nil {
		return "", nil, err
	}
	switch command.Command {
	case "volume":
		if command.Volume == nil || *command.Volume < 0 || *command.Volume > 100 || command.Mute != nil || command.Delay != nil {
			return "", nil, protocol.ErrProtocol
		}
		return "volume", *command.Volume, nil
	case "mute":
		if command.Mute == nil || command.Volume != nil || command.Delay != nil {
			return "", nil, protocol.ErrProtocol
		}
		return "muted", *command.Mute, nil
	default: // set_output_delay, selected above
		if command.Delay == nil || command.Volume != nil || command.Mute != nil {
			return "", nil, protocol.ErrProtocol
		}
		// Clamp before converting to machine int, including on 32-bit hosts.
		return "delay", int(max(int64(0), min(int64(5000), *command.Delay))), nil
	}
}
