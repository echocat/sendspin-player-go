package sendspin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"

	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/internal/monotime"
	"github.com/echocat/sendspin-player-go/internal/protocol"
)

var controllerCommands = []string{"play", "pause", "stop", "next", "previous", "volume", "mute", "repeat_off", "repeat_one", "repeat_all", "shuffle", "unshuffle", "switch", "seek", "seek_relative"}

func (s *session) request(r request) error {
	if !s.hello || !s.activated {
		return ErrNotConnected
	}
	if s.awaitingActivation {
		return errors.New("sendspin: re-handshake in progress")
	}
	switch r.kind {
	case "pairing/cancel":
		if s.pendingPSK == nil && s.codePairing == nil {
			return nil
		}
		s.endPairing(true)
		return s.send("pair/abort", map[string]string{"reason": "user_cancelled"})
	case "command":
		command := r.value.(Command)
		state := s.state.Controller
		if !s.has(Controller) || state == nil || !slices.Contains(state.SupportedCommands, command.Command) {
			return ErrInactiveRole
		}
		if !validCommand(command, state.SeekMaxMS) {
			return errors.New("sendspin: invalid controller command arguments")
		}
		return s.send("client/command", map[string]any{"controller": command})
	case "leave":
		return s.send("client/leave", map[string]any{})
	case "available":
		s.willing = r.value.(bool)
		if !s.willing && s.engine != nil {
			s.engine.Reset()
		}
		return s.sendState()
	case "format":
		if !s.has(Player) {
			return ErrInactiveRole
		}
		format := r.value.(codec.Format)
		if !slices.Contains(s.client.cfg.Formats, format) {
			return errors.New("sendspin: preferred format was not advertised")
		}
		s.preferred = &format
		return s.sendState()
	case "volume", "muted", "delay":
		if !s.has(Player) {
			return ErrInactiveRole
		}
		if s.client.playerControl != nil && r.kind != "delay" {
			return s.client.playerControl.enqueue(s, r)
		}
		if err := s.client.update(r.ctx, func(c *Credentials) error {
			switch r.kind {
			case "volume":
				c.Volume = r.value.(int)
			case "muted":
				c.Muted = r.value.(bool)
			case "delay":
				c.OutputDelayMS = r.value.(int)
			}
			return nil
		}); err != nil {
			return err
		}
		s.client.mu.Lock()
		c := s.client.credentials
		s.client.mu.Unlock()
		s.state.Volume, s.state.Muted, s.state.OutputDelayMS = c.Volume, c.Muted, c.OutputDelayMS
		s.syncPlayerControl()
		return s.sendState()
	default:
		return errors.New("sendspin: unknown local operation")
	}
}

func validCommand(c Command, seekMax *int64) bool {
	if !slices.Contains(controllerCommands, c.Command) {
		return false
	}
	if (c.Volume != nil) != (c.Command == "volume") || (c.Mute != nil) != (c.Command == "mute") || (c.PositionMS != nil) != (c.Command == "seek") || (c.OffsetMS != nil) != (c.Command == "seek_relative") {
		return false
	}
	if c.Volume != nil && (*c.Volume < 0 || *c.Volume > 100) {
		return false
	}
	if c.PositionMS != nil && (seekMax == nil || *c.PositionMS < 0 || *c.PositionMS > *seekMax) {
		return false
	}
	return true
}

func (s *session) serverState(data []byte) error {
	var p map[string]json.RawMessage
	if err := protocol.Decode(data, &p); err != nil {
		return err
	}
	if raw, ok := p["metadata"]; ok {
		if !s.has(MetadataRole) {
			return protocol.ErrProtocol
		}
		m, err := decodeMetadataState(raw)
		if err != nil {
			return err
		}
		local, err := s.local(m.Timestamp)
		if err != nil {
			return err
		}
		future := local.After(monotime.Now())
		// Even a converged estimate has uncertainty. Do not accuse the peer of
		// sending its first state in the future solely from our clock estimate;
		// apply the normal current/pending rules and let later samples refine it.
		if future {
			s.pendingMetadata = &m
		} else {
			s.state.Metadata = &m
			s.pendingMetadata = nil
		}
	}
	if raw, ok := p["color"]; ok {
		if !s.has(ColorRole) {
			return protocol.ErrProtocol
		}
		colors, err := decodeColorState(raw)
		if err != nil {
			return err
		}
		local, err := s.local(colors.Timestamp)
		if err != nil {
			return err
		}
		future := local.After(monotime.Now())
		if future {
			s.pendingColors = &colors
		} else {
			s.state.Colors = &colors
			s.pendingColors = nil
		}
	}
	if raw, ok := p["controller"]; ok {
		if !s.has(Controller) {
			return protocol.ErrProtocol
		}
		state, err := decodeControllerState(raw)
		if err != nil {
			return err
		}
		s.state.Controller = &state
	}
	s.publish()
	return nil
}

func (s *session) serverCommand(data []byte) error {
	var p map[string]json.RawMessage
	if err := protocol.Decode(data, &p); err != nil {
		return err
	}
	// Validate both objects before side effects, particularly opening an input.
	// Non-source clients keep unsupported source-specific internals opaque.
	var sourceCommand string
	if raw, ok := p["source"]; ok {
		var err error
		if sourceCommand, err = s.decodeSourceCommand(raw); err != nil {
			return err
		}
	}
	raw, ok := p["player"]
	if !ok {
		return s.applySourceCommand(sourceCommand)
	}
	if !s.has(Player) {
		return protocol.ErrProtocol
	}
	s.syncPlayerControl()
	kind, value, err := decodeOfferedPlayerCommand(raw, playerCommands(s.state))
	if err != nil {
		return err
	}
	if err := s.applySourceCommand(sourceCommand); err != nil {
		return err
	}
	if kind == "" {
		return nil
	}
	err = s.request(request{ctx: s.ctx, kind: kind, value: value})
	if s.client.playerControl != nil && kind != "delay" && err != nil {
		// Backend loss/overload is local, not a malformed server message and
		// must not tear down a healthy stream or its timing exchange.
		s.client.emit(Event{Kind: "player_control_error", ServerID: s.channel.ServerID, Err: err})
		return nil
	}
	return err
}

func (s *session) startStream(data []byte) error {
	var p map[string]json.RawMessage
	if err := protocol.Decode(data, &p, "server_transmitted"); err != nil {
		return err
	}
	var transmitted int64
	if json.Unmarshal(p["server_transmitted"], &transmitted) != nil {
		return protocol.ErrProtocol
	}
	// Availability can race the server; keep lifecycle state even when locally
	// unavailable and discard data, rather than closing solely for its arrival.
	if raw, ok := p["player"]; ok {
		if !s.has(Player) {
			return protocol.ErrProtocol
		}
		var config struct {
			codec.Format
			Header *string `json:"codec_header"`
		}
		if err := protocol.Decode(raw, &config, "codec", "sample_rate", "channels", "bit_depth"); err != nil {
			return err
		}
		if !slices.ContainsFunc(s.client.cfg.Formats, func(f codec.Format) bool { return f.Matches(config.Format) }) {
			return protocol.ErrProtocol
		}
		// Presence is a wire constraint even for a custom decoder: FLAC needs
		// STREAMINFO, whereas PCM and Opus prohibit the field, including "".
		if (config.Header != nil) != (config.Codec == "flac") {
			return protocol.ErrProtocol
		}
		header := ""
		if config.Header != nil {
			header = *config.Header
			// Known wire encoding is checked even for a custom factory. Go's
			// Base64 decoder ignores CR/LF; RFC 4648 codec_header is not MIME.
			decoded, err := base64.StdEncoding.Strict().DecodeString(header)
			if err != nil || base64.StdEncoding.EncodeToString(decoded) != header {
				return protocol.ErrProtocol
			}
		}
		d, err := s.client.cfg.Decoders[config.Codec](config.Format, header)
		if err != nil || d == nil {
			return protocol.ErrProtocol
		}
		s.decoder, s.streamFormat, s.codecHeader = d, config.Format, header
		s.streams["player"] = true
	}
	if raw, ok := p["artwork"]; ok {
		if !s.has(ArtworkRole) {
			return protocol.ErrProtocol
		}
		var a struct {
			Channels []ArtworkChannel `json:"channels"`
		}
		if err := protocol.Decode(raw, &a, "channels"); err != nil {
			return err
		}
		if len(a.Channels) > 4 {
			return protocol.ErrProtocol
		}
		for i := 0; i < 4; i++ {
			got, want := ArtworkChannel{Source: "none"}, ArtworkChannel{Source: "none"}
			if i < len(a.Channels) {
				got = a.Channels[i]
			}
			if i < len(s.client.cfg.Artwork) {
				want = s.client.cfg.Artwork[i]
			}
			if got != want {
				return protocol.ErrProtocol
			}
		}
		s.media.artworkChannels = slices.Clone(a.Channels)
		s.streams["artwork"] = true
	}
	if raw, ok := p["visualizer"]; ok {
		if !s.has(VisualizerRole) {
			return protocol.ErrProtocol
		}
		config, downbeats, err := decodeVisualizerStream(raw, s.client.cfg.Visualizer)
		if err != nil {
			return err
		}
		s.media.visualizer = config
		s.media.downbeats = downbeats
		if !s.streams["visualizer"] {
			s.media.visualLast = nil
		}
		s.streams["visualizer"] = true
	}
	s.publish()
	return nil
}

func (s *session) stopStream(kind string, data []byte) error {
	var p struct {
		Roles *[]string `json:"roles"`
	}
	if err := protocol.Decode(data, &p); err != nil {
		return err
	}
	if kind == "stream/clear" {
		// This field is defined only for clear. On end it is an unknown key,
		// whose body must not be validated through a shared typed Go model.
		var timing struct {
			Transmitted int64 `json:"server_transmitted"`
		}
		if err := protocol.Decode(data, &timing, "server_transmitted"); err != nil {
			return err
		}
	}
	var roles []string
	if p.Roles != nil {
		roles = *p.Roles
		if len(roles) == 0 {
			return protocol.ErrProtocol
		}
	} else {
		for _, family := range []string{"player", "artwork", "visualizer"} {
			if s.streams[family] && (kind == "stream/end" || family != "artwork") {
				roles = append(roles, family)
			}
		}
	}
	if len(roles) == 0 {
		return protocol.ErrProtocol
	}
	seen := map[string]bool{}
	for _, family := range roles {
		if family == "source" || seen[family] || !s.streams[family] || (kind == "stream/clear" && family == "artwork") {
			return protocol.ErrProtocol
		}
		// Validate every target before stopping any other role. An uncancelled
		// artwork transfer must not produce a target-order-dependent partial end.
		if family == "artwork" && s.media.transfer != nil {
			return protocol.ErrProtocol
		}
		seen[family] = true
	}
	for _, family := range roles {
		if family == "player" && s.engine != nil {
			s.engine.Reset()
		}
		if family == "player" && kind == "stream/clear" {
			d, err := s.client.cfg.Decoders[s.streamFormat.Codec](s.streamFormat, s.codecHeader)
			if err != nil || d == nil {
				return protocol.ErrProtocol
			}
			s.decoder = d
		}
		if family == "visualizer" {
			s.media.visualQueue = nil
			s.media.visualBytes = 0
			s.media.visualLast = nil
		}
		if kind == "stream/end" {
			delete(s.streams, family)
			if family == "artwork" {
				s.clearRole("artwork")
			}
			if family == "player" {
				s.decoder = nil
			}
		}
	}
	s.publish()
	return nil
}

func (s *session) clearRole(family string) {
	switch family {
	case "player":
		if s.engine != nil {
			s.engine.Reset()
		}
		s.decoder = nil
	case "metadata":
		s.state.Metadata = nil
		s.pendingMetadata = nil
	case "color":
		s.state.Colors = nil
		s.pendingColors = nil
	case "controller":
		s.state.Controller = nil
	case "visualizer":
		s.media.visualQueue = nil
		s.media.visualBytes = 0
		s.media.visualLast = nil
	case "artwork":
		s.media.transfer = nil
		for i := range s.media.images {
			s.media.pendingImages[i] = nil
			s.applyArtwork(Artwork{Channel: i})
		}
	}
}
