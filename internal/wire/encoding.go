package wire

import (
	"encoding/base64"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

// decodeBase64URL rejects padding, ignored CR/LF and nonzero unused bits. Go's
// Strict decoder alone still ignores CR/LF, which RC1's wire encodings do not.
func decodeBase64URL(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(value) != base64.RawURLEncoding.EncodedLen(len(decoded)) {
		return nil, protocol.ErrProtocol
	}
	return decoded, nil
}
