package sendspin

import (
	"context"
	"errors"

	"github.com/echocat/sendspin-player-go/internal/protocol"
	log "github.com/echocat/slf4g"
)

var logger = log.GetLogger(Client{})

func (c *Client) logger() log.Logger {
	if c.cfg.Logger != nil {
		return c.cfg.Logger
	}
	return logger
}

// Never log arbitrary errors: Store/backend/transport errors may contain paths,
// URL query parameters, credentials, or application/private media values.
func logErrorClass(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, protocol.ErrProtocol) {
		return "protocol"
	}
	if errors.Is(err, ErrConnectionLimit) {
		return "capacity"
	}
	var goodbye *GoodbyeError
	if errors.As(err, &goodbye) && validGoodbyeReason(goodbye.Reason) {
		return goodbye.Reason
	}
	return "operation_failed"
}
