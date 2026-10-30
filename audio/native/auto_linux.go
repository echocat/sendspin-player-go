//go:build linux && (amd64 || arm64)

package native

import (
	"context"
	"errors"
)

// Only an automatic, non-overridden Linux choice may try another backend. A
// failure after Open/Start is never hidden by changing the route mid-stream.
func autoSelect[T any](ctx context.Context, strict bool, pw, alsa func() (T, error)) (T, Backend, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, BackendAuto, err
	}
	d, pwErr := pw()
	if pwErr == nil {
		return d, BackendPipeWire, nil
	}
	if strict || ctx.Err() != nil {
		return zero, BackendPipeWire, errors.Join(pwErr, ctx.Err())
	}
	d, alsaErr := alsa()
	if alsaErr != nil {
		return zero, BackendAuto, errors.Join(pwErr, alsaErr)
	}
	return d, BackendALSA, nil
}
