// Package mdns is the SDK-private, source-pinned mDNS transport adaptation.
// It is not a new public discovery API; see README.md and LICENSE.
package mdns

import (
	"net"

	log "github.com/echocat/slf4g"
)

var logger = log.GetLoggerForCurrentPackage()

// Never infer a receiving scope from service data or an unspecified control
// message. Socket setup requires kernel interface metadata before use.
func scopeAllowed(received int, selected []net.Interface) bool {
	if received <= 0 {
		return false
	}
	for _, iface := range selected {
		if iface.Index == received {
			return true
		}
	}
	return false
}
