package sendspin

import (
	"errors"
	"slices"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/codec"
	"github.com/echocat/sendspin-player-go/diagnostic"
	"github.com/echocat/sendspin-player-go/internal/protocol"
	log "github.com/echocat/slf4g"
)

const Specification = protocol.Specification

type Role string

const (
	Player         Role = "player@v1"
	Controller     Role = "controller@v1"
	MetadataRole   Role = "metadata@v1"
	ArtworkRole    Role = "artwork@v1"
	VisualizerRole Role = "visualizer@v1"
	ColorRole      Role = "color@v1"
	SourceRole     Role = "source@v1"
)

type DeviceInfo struct {
	ProductName     string `json:"product_name,omitempty"`
	Manufacturer    string `json:"manufacturer,omitempty"`
	SoftwareVersion string `json:"software_version,omitempty"`
}

type ArtworkChannel struct {
	Source string `json:"source"`
	Format string `json:"format,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}
type Spectrum struct {
	Bins         int    `json:"n_disp_bins"`
	Scale        string `json:"scale"`
	MinFrequency int    `json:"f_min"`
	MaxFrequency int    `json:"f_max"`
}
type VisualizerConfig struct {
	Types    []string  `json:"types"`
	RateMax  int       `json:"rate_max"`
	Spectrum *Spectrum `json:"spectrum,omitempty"`
}

// Config is copied by New. Output is required only when Player is enabled.
// Buffer limits are local memory bounds, not optional protocol extensions.
type Config struct {
	// Specification explicitly pins a protocol profile, not the SDK's version.
	// Empty selects RC1. Unsupported profiles fail before any storage changes.
	Specification string
	Name          string
	DeviceInfo    *DeviceInfo
	Store         Store
	Output        audio.Output
	// PlayerControl selects host-managed volume/mute and immutable unity gain.
	// Nil retains legacy software gain and persistence. Non-nil, even empty,
	// never falls back to software control; bind a host target after New.
	PlayerControl *PlayerControlConfig
	// Input is used only with explicitly configured SourceRole, and only after
	// an active, available, authorized server requests source start. It is never
	// routed to Output. AllowUnpaired defaults to false, also for sources.
	Input audio.Input
	// SourceFormat selects the capture format. Zero defaults to 48 kHz stereo.
	// Upstream encoding is PCM16; supported capture rates are 8–192 kHz, 1–8 channels.
	SourceFormat audio.Format
	Roles        []Role
	Formats      []codec.Format
	// Decoders adds or replaces codec factories. Defaults contain PCM, FLAC and
	// Opus. Advertise only formats supported by the configured factories.
	Decoders           map[string]codec.Factory
	BufferCapacity     int
	VisualizerCapacity int
	MaxMessageBytes    int
	RecordCapacity     int
	RequiredLeadTime   time.Duration
	MinBuffer          time.Duration
	Artwork            []ArtworkChannel
	Visualizer         VisualizerConfig
	AllowUnpaired      bool
	CipherSuite        string
	HandshakeTimeout   time.Duration
	DisableDiscovery   bool
	// CodePairing enables optional PIN methods. Nil keeps PSK-only behavior.
	CodePairing *CodePairingConfig
	// Logger overrides the package's slf4g facade for this Client. Nil uses the
	// application's provider. The SDK never installs an output implementation.
	// Logging runs synchronously outside Client locks; sinks must return promptly.
	Logger log.Logger
	// Decoded observes samples before gain/drift correction. It runs on the
	// network loop, must return promptly, and must not call Client methods.
	// Copy samples to retain them. Nil disables this diagnostic hook.
	Decoded func(codec.Format, []float32)
	// Observe is an opt-in, credential-redacted transport observer. Receive/send
	// may invoke it concurrently. It must return promptly and not call Client.
	Observe diagnostic.Observer
	// Encoded observes actual audio payloads before decoding. Payload is borrowed
	// read-only; copy to retain. It contains private media. Same actor contract as Decoded.
	Encoded func(diagnostic.AudioChunk)
}

func (c *Config) defaults() error {
	if err := c.copyPlayerControl(); err != nil {
		return err
	}
	if c.CodePairing != nil {
		copy := *c.CodePairing
		c.CodePairing = &copy
		if copy.Static && copy.Display != nil {
			return errors.New("sendspin: RC1 permits at most one PIN method per Client")
		}
		if !copy.Static && copy.Display == nil {
			return errors.New("sendspin: empty code-pairing configuration")
		}
	}
	if c.Specification == "" {
		c.Specification = Specification
	}
	if c.Specification != Specification {
		return errors.New("sendspin: unsupported specification profile")
	}
	if c.Name == "" || c.Store == nil {
		return errors.New("sendspin: name and store are required")
	}
	if c.Roles == nil {
		c.Roles = []Role{Player, Controller, MetadataRole}
	}
	c.Roles = slices.Clone(c.Roles)
	c.Formats = slices.Clone(c.Formats)
	c.Artwork = slices.Clone(c.Artwork)
	factories := map[string]codec.Factory{"pcm": codec.NewBuiltIn, "flac": codec.NewBuiltIn, "opus": codec.NewBuiltIn}
	for name, factory := range c.Decoders {
		if factory == nil {
			return errors.New("sendspin: nil decoder factory")
		}
		factories[name] = factory
	}
	c.Decoders = factories
	c.Visualizer.Types = slices.Clone(c.Visualizer.Types)
	if c.Visualizer.Spectrum != nil {
		s := *c.Visualizer.Spectrum
		c.Visualizer.Spectrum = &s
	}
	if c.DeviceInfo != nil {
		d := *c.DeviceInfo
		c.DeviceInfo = &d
	}
	seen := map[Role]bool{}
	for _, role := range c.Roles {
		if !slices.Contains([]Role{Player, Controller, MetadataRole, ArtworkRole, VisualizerRole, ColorRole, SourceRole}, role) || seen[role] {
			return errors.New("sendspin: unsupported or duplicate role")
		}
		seen[role] = true
	}
	if seen[SourceRole] {
		if c.Input == nil {
			return errors.New("sendspin: source requires an explicit audio input")
		}
		if c.SourceFormat == (audio.Format{}) {
			c.SourceFormat = audio.Format{SampleRate: 48000, Channels: 2}
		}
		if c.SourceFormat.SampleRate < 8000 || c.SourceFormat.SampleRate > 192000 || c.SourceFormat.Channels < 1 || c.SourceFormat.Channels > 8 {
			return errors.New("sendspin: invalid source capture format")
		}
	}
	if c.BufferCapacity == 0 {
		c.BufferCapacity = 4 << 20
	}
	if c.VisualizerCapacity == 0 {
		c.VisualizerCapacity = 128 << 10
	}
	if c.MaxMessageBytes == 0 {
		c.MaxMessageBytes = 8 << 20
	}
	if c.RecordCapacity == 0 {
		c.RecordCapacity = 16
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = 30 * time.Second
	}
	if c.CipherSuite == "" {
		c.CipherSuite = "25519_ChaChaPoly_SHA256"
	}
	if c.CipherSuite != "25519_ChaChaPoly_SHA256" && c.CipherSuite != "25519_AESGCM_SHA256" {
		return errors.New("sendspin: unsupported cipher suite")
	}
	if c.BufferCapacity < 65535 || c.BufferCapacity > 64<<20 || c.MaxMessageBytes < 65519 || c.MaxMessageBytes > 64<<20 || c.VisualizerCapacity <= 0 || c.VisualizerCapacity > 16<<20 || c.RecordCapacity < 5 || c.RecordCapacity > 1024 || c.HandshakeTimeout <= 0 || c.RequiredLeadTime < 0 || c.MinBuffer < 0 {
		return errors.New("sendspin: invalid capacity or timing limits")
	}
	if seen[Player] {
		if c.Output == nil {
			return errors.New("sendspin: player requires an audio output")
		}
		if c.Formats == nil {
			c.Formats = []codec.Format{{Codec: "flac", SampleRate: 48000, Channels: 2, BitDepth: 24}, {Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 24}, {Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 16}}
		}
		if len(c.Formats) == 0 {
			return errors.New("sendspin: player requires audio formats")
		}
		baseline := false
		for _, f := range c.Formats {
			if err := f.Validate(); err != nil {
				return err
			}
			if c.Decoders[f.Codec] == nil {
				return errors.New("sendspin: advertised codec has no decoder")
			}
			if f.Codec == "pcm" || f.Codec == "flac" {
				baseline = true
			}
		}
		if !baseline {
			return errors.New("sendspin: player must advertise PCM or FLAC in addition to Opus")
		}
	}
	if seen[ArtworkRole] {
		if len(c.Artwork) < 1 || len(c.Artwork) > 4 {
			return errors.New("sendspin: artwork requires 1-4 channels")
		}
		for _, ch := range c.Artwork {
			if !validArtwork(ch) {
				return errors.New("sendspin: invalid artwork channel")
			}
		}
	}
	if seen[VisualizerRole] {
		if c.Visualizer.Types == nil {
			c.Visualizer.Types = []string{"loudness", "peak"}
		}
		if c.Visualizer.RateMax == 0 {
			c.Visualizer.RateMax = 30
		}
		if c.Visualizer.RateMax < 1 || c.Visualizer.RateMax > 1000 {
			return errors.New("sendspin: invalid visualizer frame rate")
		}
		vseen := map[string]bool{}
		for _, typ := range c.Visualizer.Types {
			if !slices.Contains([]string{"loudness", "beat", "f_peak", "spectrum", "peak"}, typ) || vseen[typ] {
				return errors.New("sendspin: invalid visualizer type")
			}
			vseen[typ] = true
		}
		if vseen["spectrum"] && c.Visualizer.Spectrum == nil {
			return errors.New("sendspin: spectrum type requires a spectrum configuration")
		}
		// Optional fields still need valid values when provided: this object is
		// serialized into client/state even if spectrum is not requested now.
		if c.Visualizer.Spectrum != nil {
			s := c.Visualizer.Spectrum
			if s.Bins < 1 || s.Bins > 4096 || !slices.Contains([]string{"mel", "log", "lin"}, s.Scale) || s.MinFrequency < 0 || s.MaxFrequency <= s.MinFrequency {
				return errors.New("sendspin: invalid spectrum configuration")
			}
		}
	}
	return nil
}
func validArtwork(c ArtworkChannel) bool {
	if c.Source == "none" {
		return c.Format == "" && c.Width == 0 && c.Height == 0
	}
	return (c.Source == "album" || c.Source == "artist") && (c.Format == "png" || c.Format == "jpeg") && c.Width > 0 && c.Height > 0 && c.Width <= 8192 && c.Height <= 8192
}

type Group struct {
	PlaybackState string `json:"playback_state"`
	ID            string `json:"group_id"`
	Name          string `json:"group_name"`
}
type Progress struct {
	PositionMS int64 `json:"track_progress"`
	DurationMS int64 `json:"track_duration"`
	Speed      int64 `json:"playback_speed"`
}
type Metadata struct {
	Timestamp   int64     `json:"timestamp"`
	Title       string    `json:"title,omitempty"`
	Artist      string    `json:"artist,omitempty"`
	AlbumArtist string    `json:"album_artist,omitempty"`
	Album       string    `json:"album,omitempty"`
	ArtworkURL  string    `json:"artwork_url,omitempty"`
	Year        int       `json:"year,omitempty"`
	Track       int       `json:"track,omitempty"`
	Progress    *Progress `json:"progress,omitempty"`
}

type Colors struct {
	Timestamp       int64 `json:"timestamp"`
	BackgroundDark  []int `json:"background_dark,omitempty"`
	BackgroundLight []int `json:"background_light,omitempty"`
	Primary         []int `json:"primary,omitempty"`
	Accent          []int `json:"accent,omitempty"`
	OnDark          []int `json:"on_dark,omitempty"`
	OnLight         []int `json:"on_light,omitempty"`
}
type ControllerState struct {
	SupportedCommands []string `json:"supported_commands"`
	Volume            int      `json:"volume"`
	Muted             bool     `json:"muted"`
	Repeat            string   `json:"repeat"`
	Shuffle           bool     `json:"shuffle"`
	SeekMaxMS         *int64   `json:"seek_max_ms,omitempty"`
}
type Command struct {
	Command    string `json:"command"`
	Volume     *int   `json:"volume,omitempty"`
	Mute       *bool  `json:"mute,omitempty"`
	PositionMS *int64 `json:"position_ms,omitempty"`
	OffsetMS   *int64 `json:"offset_ms,omitempty"`
}

// Event contains immutable, application-owned data. Delivery is best-effort on
// the bounded Events channel; a slow consumer never stalls the protocol loop.
// StateChanged is a notification to obtain a fresh Snapshot.
type Event struct {
	Kind          string
	ServerID      string
	Err           error
	Artwork       *Artwork
	Visualization *Visualization
}
type Artwork struct {
	Channel   int
	Timestamp int64
	Data      []byte
}
type Visualization struct {
	Type      string
	Timestamp int64
	// Values preserves the received bins/scalars. For f_peak it is [dominant
	// frequency in Hz, amplitude], not an inferred musical fundamental or note.
	Values   []uint16
	Downbeat bool
	Strength uint8
}

type Snapshot struct {
	ServerID, ServerName string
	// ServerLanguages preserves the admitted server's language preference order
	// and spelling. Nil means the optional hint was absent. The caller owns it.
	ServerLanguages []string
	ActiveRoles     []Role
	// ActiveStreams lists sorted, unversioned role families such as "artwork".
	// A role can remain active after its stream ends.
	ActiveStreams []string
	Available     bool
	Group         Group
	Metadata      *Metadata
	Colors        *Colors
	Controller    *ControllerState
	Volume        int
	Muted         bool
	// Known flags distinguish unknown external state from zero/false. Legacy
	// software state is always known. PlayerControl is nil in legacy mode.
	VolumeKnown   bool
	MutedKnown    bool
	PlayerControl *PlayerControlSnapshot
	OutputDelayMS int
}
