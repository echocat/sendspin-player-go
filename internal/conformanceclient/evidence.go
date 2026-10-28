package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
)

func (r *recorder) summary() map[string]any {
	var stream any
	if len(r.starts) > 0 {
		stream = r.starts[len(r.starts)-1]
	}
	summary := map[string]any{"hello": r.hello, "peer_hello": r.peer, "stream": stream, "protocol": r.protocolEvidence(), "audio": map[string]any{"audio_chunk_count": r.chunks, "received_pcm_sha256": fmt.Sprintf("%x", r.pcm.Sum(nil)), "received_sample_count": r.samples, "received_encoded_sha256": fmt.Sprintf("%x", r.encoded.Sum(nil)), "received_encoded_byte_count": r.encodedBytes, "received_chunk_frames": r.frames}}
	if r.metadata != nil {
		m := r.metadata
		received := map[string]any{"title": m.Title, "artist": m.Artist, "album_artist": m.AlbumArtist, "album": m.Album, "artwork_url": m.ArtworkURL, "year": m.Year, "track": m.Track}
		if m.Progress != nil {
			received["progress"] = map[string]any{"track_progress": m.Progress.PositionMS, "track_duration": m.Progress.DurationMS, "playback_speed": m.Progress.Speed}
		}
		summary["metadata"] = map[string]any{"update_count": r.metadataUpdates, "received": received, "first_object_state": r.firstMetadata}
	}
	if r.controller != nil {
		summary["controller"] = map[string]any{"received_state": r.controller, "sent_command": r.sentCommand}
	}
	if r.artwork != nil {
		hash := hashBytes(r.artwork.Data)
		summary["artwork"] = map[string]any{"channel": r.artwork.Channel, "received_count": 1, "received_sha256": hash, "byte_count": len(r.artwork.Data)}
	}
	if r.requested != nil {
		var initial any
		if len(r.starts) > 0 {
			initial = r.starts[0]
		}
		summary["renegotiation"] = map[string]any{"requested": r.requested, "initial_format": initial, "final_format": stream, "stream_start_count": len(r.starts)}
	}
	return summary
}

func assertion(events []map[string]any, defect string) map[string]any {
	if defect != "" {
		return map[string]any{"status": "failed", "detail": defect, "events": events}
	}
	if len(events) == 0 {
		return map[string]any{"status": "failed", "detail": "no transport evidence", "events": events}
	}
	return map[string]any{"status": "passed", "events": events}
}

func (r *recorder) protocolEvidence() map[string]any {
	assertions := map[string]any{}
	var handshake, ordering, activation, stateEvents, player []map[string]any
	var first []diagnostic.Message
	activated, stateSent, helloSeen, streamSeen := false, false, false, false
	var advertised []string
	support := map[string]json.RawMessage{}
	var formats []codec.Format
	var lastTimestamp int64
	hasTimestamp := false
	defects := map[string]string{}
	messages := slices.Clone(r.messages)
	slices.SortStableFunc(messages, func(a, b diagnostic.Message) int { return a.At.Compare(b.At) })
	for _, message := range messages {
		direction := "inbound"
		if message.Direction == diagnostic.Sent {
			direction = "outbound"
		}
		event := map[string]any{"direction": direction, "message_type": message.Type, "websocket_type": message.Transport, "encrypted": message.Encrypted, "byte_count": message.PlaintextBytes, "observed_at": message.At}
		if len(message.Payload) > 0 {
			event["payload"] = json.RawMessage(message.Payload)
		}
		if len(message.Prefix) > 0 {
			event["leading_hex"] = fmt.Sprintf("%x", message.Prefix)
		}
		if message.Truncated || message.Reassembled || (message.HasMessageID && message.MessageID == 1) {
			for _, id := range []string{"CORE-001", "CORE-002", "CORE-003", "CORE-004", "PLAYER-001"} {
				defects[id] = "incomplete, fragmented or truncated transport evidence"
			}
		}
		if message.HasMessageID {
			event["message_id"] = message.MessageID
		}
		if !activated {
			first = append(first, message)
			ordering = append(ordering, event)
			if message.Type == "client/init" || message.Type == "server/init" || message.Type == "noise/handshake" || message.Type == "server/hello" || message.Type == "client/hello" || message.Type == "server/activate" {
				handshake = append(handshake, event)
			} else {
				defects["CORE-002"] = "application/unknown message before first activation"
			}
		}
		switch message.Type {
		case "client/hello":
			if message.Direction != diagnostic.Sent {
				defects["CORE-003"] = "client capabilities observed in wrong direction"
			}
			helloSeen = true
			_ = json.Unmarshal(message.Payload, &support)
			_ = json.Unmarshal(support["supported_roles"], &advertised)
			var playerSupport struct {
				Formats []codec.Format `json:"supported_formats"`
			}
			_ = json.Unmarshal(support["player@v1_support"], &playerSupport)
			formats = playerSupport.Formats
		case "server/activate":
			if message.Direction != diagnostic.Received {
				defects["CORE-003"] = "activation observed in wrong direction"
			}
			if activated && !stateSent {
				defects["CORE-004"] = "activation was superseded without an observed initial client/state"
			}
			stateSent = false
			var payload struct {
				Roles []string `json:"active_roles"`
			}
			_ = json.Unmarshal(message.Payload, &payload)
			families := map[string]bool{}
			if !helloSeen {
				defects["CORE-003"] = "activation before observed capabilities"
			}
			for _, role := range payload.Roles {
				family, _, _ := strings.Cut(role, "@")
				if !slices.Contains(advertised, role) || families[family] {
					defects["CORE-003"] = "unadvertised or duplicate active role family"
				}
				families[family] = true
				if (family == "player" || family == "visualizer") && (support[role+"_support"] == nil || string(support[role+"_support"]) == "null") {
					defects["CORE-003"] = "required role support object was not observed"
				}
			}
			activation = append(activation, event)
			activated = true
		case "client/state":
			if message.Direction != diagnostic.Sent {
				defects["CORE-004"] = "client/state observed in wrong direction"
			}
			stateSent = true
			stateEvents = append(stateEvents, event)
		case "stream/start":
			if message.Direction != diagnostic.Received {
				defects["PLAYER-001"] = "stream/start observed in wrong direction"
			}
			var payload struct {
				Player *codec.Format `json:"player"`
			}
			_ = json.Unmarshal(message.Payload, &payload)
			if payload.Player != nil {
				hasTimestamp = false
				streamSeen = true
				if !slices.ContainsFunc(formats, func(format codec.Format) bool { return format.Matches(*payload.Player) }) {
					defects["PLAYER-001"] = "negotiated player format was not observed in capabilities"
				}
				player = append(player, event)
			}
		case "stream/end":
			var payload struct {
				Roles []string `json:"roles"`
			}
			_ = json.Unmarshal(message.Payload, &payload)
			if slices.Contains(payload.Roles, "player") {
				streamSeen = false
			}
		}
		if message.Direction == diagnostic.Received && message.HasMessageID && (message.MessageID == 4 || (message.MessageID >= 8 && message.MessageID <= 11) || (message.MessageID >= 16 && message.MessageID <= 20)) {
			if !stateSent {
				defects["CORE-004"] = "binary role data before observed initial client/state"
			}
			stateEvents = append(stateEvents, event)
		}
		if message.Direction == diagnostic.Received && message.HasMessageID && message.MessageID == 4 {
			if !streamSeen || message.PlaintextBytes < 14 || len(message.Prefix) < 13 || int64(binary.BigEndian.Uint64(message.Prefix[1:9])) < 0 {
				defects["PLAYER-001"] = "audio chunk without observed stream or complete 13-byte header"
			}
			if len(message.Prefix) >= 13 {
				timestamp := int64(binary.BigEndian.Uint64(message.Prefix[1:9]))
				if hasTimestamp && timestamp < lastTimestamp {
					defects["PLAYER-001"] = "audio timestamps moved backwards within the stream"
				}
				lastTimestamp = timestamp
				hasTimestamp = true
			}
			player = append(player, event)
		}
	}
	expected := []string{"client/init", "server/init", "noise/handshake", "noise/handshake", "server/hello", "client/hello", "server/activate"}
	if len(first) != len(expected) {
		defects["CORE-001"] = "initial message count/order differs from required handshake"
	} else {
		for i, message := range first {
			wantDirection := diagnostic.Received
			if i == 0 || i == 3 || i == 5 {
				wantDirection = diagnostic.Sent
			}
			if message.Type != expected[i] || message.Direction != wantDirection || message.Encrypted != (i >= 4) || message.Transport != map[bool]string{true: "binary", false: "text"}[i >= 4] {
				defects["CORE-001"] = "observed handshake direction/order/frame type is invalid"
			}
		}
	}
	if !activated {
		defects["CORE-002"] = "no activation observed"
		defects["CORE-003"] = "no activation observed"
	}
	if !stateSent {
		defects["CORE-004"] = "no initial state observed"
	}
	if len(player) < 2 || r.chunks == 0 {
		defects["PLAYER-001"] = "no observed stream plus audio chunk"
	}
	if r.failure != "" {
		for _, id := range []string{"CORE-001", "CORE-002", "CORE-003", "CORE-004", "PLAYER-001"} {
			defects[id] = r.failure
		}
	}
	assertions["CORE-001"] = assertion(handshake, defects["CORE-001"])
	assertions["CORE-002"] = assertion(ordering, defects["CORE-002"])
	assertions["CORE-003"] = assertion(activation, defects["CORE-003"])
	assertions["CORE-004"] = assertion(stateEvents, defects["CORE-004"])
	assertions["PLAYER-001"] = assertion(player, defects["PLAYER-001"])
	return map[string]any{"spec_revision": assertionRevision, "assertions": assertions}
}

func hashBytes(data []byte) string { sum := sha256.Sum256(data); return fmt.Sprintf("%x", sum) }
