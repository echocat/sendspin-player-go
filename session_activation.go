package sendspin

import "github.com/echocat/sendspin-player-go/internal/protocol"

// activationRefusal applies RC1's ordered admissibility responses after payload
// shape and role validation, but before admission or any persistent mutation.
func (s *session) activationRefusal(activities map[string]bool, roles []Role, pairingSupported bool) string {
	paired := s.channel.Category == "lt"
	allowedPurposes := !paired || !activities["pairing"]
	needsUnpairedAccess := activities["playback"] || len(roles) > 0
	// Enabling unpaired access must make the *whole* activation admissible,
	// including the selected method/format, not merely its activity set.
	if !paired && !s.client.cfg.AllowUnpaired && needsUnpairedAccess && allowedPurposes && pairingSupported {
		return "pairing_required"
	}
	playbackCapable := allowedPurposes && (paired || s.client.cfg.AllowUnpaired)
	if !allowedPurposes || (!playbackCapable && needsUnpairedAccess) {
		return "unauthorized"
	}
	if !pairingSupported {
		return "method_not_supported"
	}
	return ""
}

// rejectPairingActivation ends the received activation's sequencing barrier,
// without admitting its unsupported attempt or adopting its explicit role set.
func (s *session) rejectPairingActivation(playbackCapable bool) error {
	if !playbackCapable {
		// Re-handshaking off a long-term key can make persisted roles implicitly
		// inactive. A method refusal must never preserve those old permissions.
		for family := range s.active {
			if s.streams[family] {
				return protocol.ErrProtocol // Required stream/end did not precede removal.
			}
		}
		for family := range s.active {
			s.clearRole(family)
		}
		s.active = map[string]Role{}
		s.state.ActiveRoles = []Role{}
		s.state.Available = false
		s.publish()
	}
	s.endPairing(true)
	// Receipt, not acceptance, ends the first/re-handshake activation gate.
	s.activated, s.awaitingActivation = true, false
	return nil
}
