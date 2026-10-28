package wire

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

// A small explicit allowlist avoids accidentally exposing credentials from new
// roles/fields. This must not become a generic "redact a few secret names" logger.
type projection struct {
	kind    byte
	fields  map[string]*projection
	element *projection
}

var textProjection = &projection{kind: 's'}
var numberProjection = &projection{kind: 'n'}
var boolProjection = &projection{kind: 'b'}

func objectProjection(fields map[string]*projection) *projection {
	return &projection{kind: 'o', fields: fields}
}
func arrayProjection(element *projection) *projection {
	return &projection{kind: 'a', element: element}
}

var formatProjection = objectProjection(map[string]*projection{"codec": textProjection, "sample_rate": numberProjection, "channels": numberProjection, "bit_depth": numberProjection})
var playerProjection = objectProjection(map[string]*projection{"codec": textProjection, "sample_rate": numberProjection, "channels": numberProjection, "bit_depth": numberProjection, "codec_header": textProjection})
var messageProjections = map[string]*projection{
	"client/init":     objectProjection(map[string]*projection{"client_id": textProjection, "version": numberProjection, "suite": textProjection}),
	"server/init":     objectProjection(map[string]*projection{"server_id": textProjection, "version": numberProjection}),
	"server/error":    objectProjection(map[string]*projection{"reason": textProjection}),
	"server/hello":    objectProjection(map[string]*projection{"name": textProjection, "languages": arrayProjection(textProjection)}),
	"client/hello":    objectProjection(map[string]*projection{"name": textProjection, "supported_roles": arrayProjection(textProjection), "player@v1_support": objectProjection(map[string]*projection{"supported_formats": arrayProjection(formatProjection), "buffer_capacity": numberProjection}), "visualizer@v1_support": objectProjection(map[string]*projection{"buffer_capacity": numberProjection})}),
	"server/activate": objectProjection(map[string]*projection{"activities": arrayProjection(textProjection), "active_roles": arrayProjection(textProjection)}),
	"client/state":    objectProjection(map[string]*projection{"available": boolProjection, "player": objectProjection(map[string]*projection{"volume": numberProjection, "muted": boolProjection, "output_delay_ms": numberProjection, "required_lead_time_ms": numberProjection, "min_buffer_ms": numberProjection, "supported_commands": arrayProjection(textProjection), "format": formatProjection})}),
	"client/time":     objectProjection(map[string]*projection{"client_transmitted": numberProjection}),
	"server/time":     objectProjection(map[string]*projection{"client_transmitted": numberProjection, "server_received": numberProjection, "server_transmitted": numberProjection}),
	"stream/start":    objectProjection(map[string]*projection{"server_transmitted": numberProjection, "player": playerProjection}),
	"stream/clear":    objectProjection(map[string]*projection{"server_transmitted": numberProjection, "roles": arrayProjection(textProjection)}),
	"stream/end":      objectProjection(map[string]*projection{"roles": arrayProjection(textProjection)}),
	"group/update":    objectProjection(map[string]*projection{"playback_state": textProjection, "group_id": textProjection, "group_name": textProjection}),
	"server/state":    objectProjection(map[string]*projection{"metadata": objectProjection(map[string]*projection{"timestamp": numberProjection}), "color": objectProjection(map[string]*projection{"timestamp": numberProjection})}),
	"client/command":  objectProjection(map[string]*projection{"controller": objectProjection(map[string]*projection{"command": textProjection, "volume": numberProjection, "mute": boolProjection, "position_ms": numberProjection, "offset_ms": numberProjection})}),
	"client/leave":    objectProjection(nil), "server/unpair": objectProjection(nil),
	"client/goodbye": objectProjection(map[string]*projection{"reason": textProjection}),
}

func project(data json.RawMessage, schema *projection, limited *bool) json.RawMessage {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return json.RawMessage("null")
	}
	switch schema.kind {
	case 's':
		var value string
		if json.Unmarshal(data, &value) != nil {
			return json.RawMessage("null")
		}
		if len(value) > 4096 {
			*limited = true
			return json.RawMessage(`"[TRUNCATED]"`)
		}
		out, _ := json.Marshal(value)
		return out
	case 'n':
		value := bytes.TrimSpace(data)
		if len(value) > 0 && (value[0] == '-' || (value[0] >= '0' && value[0] <= '9')) && json.Valid(value) {
			return bytes.Clone(value)
		}
	case 'b':
		value := bytes.TrimSpace(data)
		if bytes.Equal(value, []byte("true")) || bytes.Equal(value, []byte("false")) {
			return bytes.Clone(value)
		}
	case 'o':
		var input map[string]json.RawMessage
		if json.Unmarshal(data, &input) != nil {
			return json.RawMessage("null")
		}
		output := map[string]json.RawMessage{}
		for key, child := range schema.fields {
			if value, ok := input[key]; ok {
				output[key] = project(value, child, limited)
			}
		}
		out, _ := json.Marshal(output)
		return out
	case 'a':
		var input []json.RawMessage
		if json.Unmarshal(data, &input) != nil {
			return json.RawMessage("null")
		}
		if len(input) > 256 {
			*limited = true
			input = input[:256]
		}
		output := make([]json.RawMessage, len(input))
		for i, value := range input {
			output[i] = project(value, schema.element, limited)
		}
		out, _ := json.Marshal(output)
		return out
	}
	return json.RawMessage("null")
}

func (c *Channel) observeJSON(data []byte, direction diagnostic.Direction, transport string, encrypted, reassembled bool, at time.Time) {
	if c.observer == nil {
		return
	}
	event := diagnostic.Message{At: at, Direction: direction, Transport: transport, Encrypted: encrypted, PlaintextBytes: len(data), Type: "malformed", Reassembled: reassembled}
	if encrypted {
		event.HasMessageID = true
		event.MessageID = 0
		event.PlaintextBytes++
	}
	if message, err := protocol.Parse(data); err == nil {
		if schema, known := messageProjections[message.Type]; known {
			event.Type = message.Type
			event.Payload = project(message.Payload, schema, &event.Truncated)
		} else if message.Type == "noise/handshake" || strings.HasPrefix(message.Type, "client/pair-") || strings.HasPrefix(message.Type, "server/pair-") || message.Type == "pair/abort" {
			// Never expose pairing or Noise bodies, even future fields.
			event.Type = "redacted/control"
			if message.Type == "noise/handshake" {
				event.Type = message.Type
			}
		} else {
			event.Type = "unknown"
		}
	}
	if len(event.Payload) > 65536 {
		event.Payload = nil
		event.Truncated = true
	}
	c.observer(event)
}

func (c *Channel) observeFrame(frame []byte, direction diagnostic.Direction, reassembled bool, at time.Time) {
	if c.observer == nil {
		return
	}
	if len(frame) > 0 && frame[0] == 0 {
		c.observeJSON(frame[1:], direction, "binary", true, reassembled, at)
		return
	}
	event := diagnostic.Message{At: at, Direction: direction, Transport: "binary", Encrypted: true, PlaintextBytes: len(frame), Type: "binary", Reassembled: reassembled}
	if len(frame) > 0 {
		event.HasMessageID = true
		event.MessageID = frame[0]
		size := 1
		if frame[0] == 4 {
			size = min(13, len(frame))
		}
		event.Prefix = bytes.Clone(frame[:size])
	}
	c.observer(event)
}
