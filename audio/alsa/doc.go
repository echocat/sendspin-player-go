// Package alsa provides direct Linux ALSA playback without Oto or PortAudio.
// Native support uses only automatic linux/cgo constraints and libasound.
// The SDK core remains independent and pure Go. No input device or mixer is
// opened or configured. Device opening is explicit through Output.Open.
//
// Native MONOTONIC PCM status timestamps and reported queue/delivery delay drive
// scheduling. They are driver evidence, not a physical synchronization certificate.
// Non-HW routes (including typical PipeWire/PulseAudio ALSA plugins) require an
// explicit AllowPluginTiming choice because their downstream clock/flush quality
// cannot be inferred from the ALSA API alone. Hardware loopback/calibration is
// still required to characterize the actual port and downstream stop behavior.
package alsa
