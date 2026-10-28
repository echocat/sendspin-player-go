//go:build cgo

// Package portaudio provides an optional native output using PortAudio's DAC
// timestamps. Importing it requires CGO and a native PortAudio development
// package; no custom build tag is used. It is an independent Go module so the
// SDK core can be tested with the race detector without PortAudio headers.
//
// Physical synchronization depends on the driver's timestamp quality. The
// backend has not been hardware-certified; use loopback measurements before
// claiming RC1's physical +/-1ms synchronization guarantee.
package portaudio
