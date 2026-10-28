// Package diagnostic defines opt-in SDK observations. It is not a packet logger:
// JSON is projected to known diagnostic fields, credential/Noise payloads are
// excluded, and binary observations never contain media beyond the audio header.
package diagnostic

import (
	"encoding/json"
	"time"

	"github.com/echocat/sendspin-player-go/codec"
)

type Direction string

const (
	Sent     Direction = "sent"
	Received Direction = "received"
)

// Message is application-owned. Payload is a projection, not raw JSON: unknown
// fields and credential-bearing message bodies are omitted. Display names may
// still be personal data. Prefix is at most the 13-byte audio header (otherwise
// the type byte only). Reassembled distinguishes a logical fragmented message
// from individual authenticated transport fragments. Truncated invalidates any
// claim requiring the complete projected payload.
type Message struct {
	At             time.Time
	Direction      Direction
	Transport      string // "text" or "binary", referring to complete WebSocket messages.
	Encrypted      bool
	Type           string
	Payload        json.RawMessage
	PlaintextBytes int
	HasMessageID   bool
	MessageID      byte
	Prefix         []byte
	Reassembled    bool
	Truncated      bool
}

// Observer may be called concurrently by receive/send paths. It must return
// promptly, must not call Client methods, and must not panic. Nil disables all
// observation work. Message fields can be retained or changed by the caller.
type Observer func(Message)

// AudioChunk observes the SDK's actual encoded payload slice, before decoding
// or playback correction. Payload is borrowed read-only for the callback; copy
// to retain it. It contains private media, not credentials, and is separate from
// the safe-by-projection message observer. Calls are serialized by the actor.
type AudioChunk struct {
	Format      codec.Format
	Timestamp   int64
	SendAheadUS uint32
	Payload     []byte
}
