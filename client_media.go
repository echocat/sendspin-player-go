package sendspin

import (
	"bytes"
	"encoding/json"
	"slices"
)

// CurrentPosition returns the current track position in milliseconds, using the
// active connection's clock mapping and applied (never pending) metadata. False
// means there is no synchronized, active metadata role with a known position.
func (c *Client) CurrentPosition() (int64, bool) {
	c.mu.Lock()
	s := c.active
	c.mu.Unlock()
	if s == nil || !s.filter.Ready() {
		return 0, false
	}
	s.snapshotMu.RLock()
	var state Snapshot
	err := json.Unmarshal(s.snapshot, &state)
	s.snapshotMu.RUnlock()
	if err != nil || state.Metadata == nil || !slices.Contains(state.ActiveRoles, MetadataRole) {
		return 0, false
	}
	server, ok := s.filter.CheckedServerTime(s.now())
	if !ok {
		return 0, false
	}
	return state.Metadata.Position(server)
}

// CurrentArtwork returns an application-owned copy of a channel's current image.
// It returns false for invalid channels, cleared images, and disconnected clients.
// Pending images are never returned before their scheduled timestamp. Unlike
// best-effort events, this read model remains usable when the event buffer fills.
func (c *Client) CurrentArtwork(channel int) (Artwork, bool) {
	if channel < 0 || channel >= 4 {
		return Artwork{}, false
	}
	c.mu.Lock()
	s := c.active
	c.mu.Unlock()
	if s == nil {
		return Artwork{}, false
	}
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	image := s.media.images[channel]
	if image == nil {
		return Artwork{}, false
	}
	copy := *image
	copy.Data = bytes.Clone(image.Data)
	return copy, true
}

// applyArtwork runs on the session actor. Complete image data is immutable once
// published; event consumers and getter callers receive their own byte slices.
func (s *session) applyArtwork(image Artwork) {
	s.snapshotMu.Lock()
	if len(image.Data) == 0 {
		s.media.images[image.Channel] = nil
	} else {
		s.media.images[image.Channel] = &image
	}
	s.snapshotMu.Unlock()
	if len(s.client.events) == cap(s.client.events) {
		return
	}
	eventImage := image
	eventImage.Data = bytes.Clone(image.Data)
	s.client.emit(Event{Kind: "artwork", ServerID: s.channel.ServerID, Artwork: &eventImage})
}
