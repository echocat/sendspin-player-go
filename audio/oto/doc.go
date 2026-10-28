// Package oto provides an optional CGO-free output using Oto v3.
//
// Upstream Oto does not expose a hardware playback clock or a downstream flush.
// Output therefore requires explicit AllowApproximate opt-in: Next estimates
// timing from software queues and a configured latency; Flush and Close stop
// source playback but cannot retract audio already submitted downstream. This
// mode does not meet the strict hardware-feedback guarantees of audio.Device
// and must not be used to claim physical RC1 synchronization conformance.
//
// One Oto context is retained for the process lifetime, with a fixed sample
// rate, channel count and requested device buffer. Only one SDK device may use
// it at a time. No device is opened merely by importing the package.
package oto
