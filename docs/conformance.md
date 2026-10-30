# Protocol support

The SDK targets [Sendspin 1.0.0-rc1](https://github.com/Sendspin/spec/tree/671a34d408581fc25ea56b3528a4a3f13e3be901).
SDK release numbers are independent of the specification and role versions.

Implemented roles are Player, Controller, Metadata, Artwork, Visualizer, Color,
and opt-in Source. Playback supports PCM, FLAC, and Opus; Source sends PCM16.
Connections use Noise encryption over plain WebSockets. PSK/token pairing is the
default; dynamic or static PIN pairing is optional.

## Limitations

- Full RC1 conformance is not established. Some requirement interpretations and
  product-specific pairing obligations remain open.
- Interoperability tests do not guarantee physical synchronization, absolute
  latency, capture-clock accuracy, or click-free stop/flush behavior on a device.
- Native capture and Windows/macOS audio hardware need separate hardware validation.
- Cryptographic interoperability checks are not an independent security audit.

For implementation review, the [internal audit tooling](../internal/specaudit)
keeps the requirement catalog and test mappings. These records are not a
certification or an application integration guide.
