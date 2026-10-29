package sendspin

import (
	"encoding/json"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

type serverHello struct {
	Name          string          `json:"name"`
	Languages     []string        `json:"languages"`
	SourceSupport json.RawMessage `json:"source@v1_support"`
}

func decodeServerHello(data []byte) (serverHello, error) {
	var hello serverHello
	if err := protocol.Decode(data, &hello, "name"); err != nil {
		return serverHello{}, err
	}
	if hello.Languages != nil {
		if len(hello.Languages) == 0 {
			return serverHello{}, protocol.ErrProtocol
		}
		for _, tag := range hello.Languages {
			if !protocol.WellFormedLanguageTag(tag) {
				return serverHello{}, protocol.ErrProtocol
			}
		}
	}
	// The core declares an object when this known field is present. Non-source
	// clients keep its internals opaque; sourceHello validates them when offered.
	if hello.SourceSupport != nil && !protocol.Object(hello.SourceSupport) {
		return serverHello{}, protocol.ErrProtocol
	}
	hello.SourceSupport = nil // Do not retain an unused capability body.
	return hello, nil
}
