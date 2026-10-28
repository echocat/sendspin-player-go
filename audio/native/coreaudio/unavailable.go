//go:build !darwin || !cgo

package coreaudio

import "github.com/echocat/sendspin-player-go/audio/native/internal/render"

func openNative(_ Output, _ *render.Device) (render.Driver, error) { return nil, render.ErrUnavailable }
