//go:build darwin && cgo

package coreaudio

/*
#cgo LDFLAGS: -framework AudioUnit -framework CoreAudio -framework CoreFoundation
#include <stdlib.h>
#include "native.h"
*/
import "C"

import (
	"fmt"
	"github.com/echocat/sendspin-player-go/audio/native/internal/bridge"
	"github.com/echocat/sendspin-player-go/audio/native/internal/render"
	"unsafe"
)

type driver struct {
	p     *C.sendspin_ca
	token uintptr
}

func caError(code C.int32_t) error {
	if code != 0 {
		return fmt.Errorf("coreaudio: native operation failed (OSStatus %d)", int32(code))
	}
	return nil
}
func openNative(o Output, d *render.Device) (render.Driver, error) {
	token := bridge.New(d)
	name := C.CString(o.Device)
	defer C.free(unsafe.Pointer(name))
	var code C.int32_t
	p := C.sendspin_ca_open(C.uintptr_t(token), name, C.int(d.Format().SampleRate), C.int(d.Format().Channels), &code)
	if p == nil {
		bridge.Free(token)
		if err := caError(code); err != nil {
			return nil, err
		}
		return nil, render.ErrDriver
	}
	return &driver{p: p, token: token}, nil
}
func (d *driver) Start() error { return caError(C.sendspin_ca_start(d.p)) }
func (d *driver) Stop() error  { return caError(C.sendspin_ca_stop(d.p)) }
func (d *driver) Check() error { return caError(C.sendspin_ca_check(d.p)) }
func (d *driver) Clock() int64 { return bridge.Now() }
func (d *driver) Close() error {
	err := caError(C.sendspin_ca_close(d.p))
	bridge.Free(d.token)
	return err
}
