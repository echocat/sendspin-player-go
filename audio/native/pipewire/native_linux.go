//go:build linux && cgo

package pipewire

/*
#cgo pkg-config: libpipewire-0.3
#cgo LDFLAGS: -pthread
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
	p     *C.sendspin_pw
	token uintptr
}

func pwError(code C.int) error {
	if code < 0 {
		return fmt.Errorf("pipewire: native operation failed (%d)", int(code))
	}
	return nil
}
func openNative(o Output, d *render.Device) (render.Driver, error) {
	token := bridge.New(d)
	name := C.CString(o.Device)
	defer C.free(unsafe.Pointer(name))
	var code C.int
	p := C.sendspin_pw_open(C.uintptr_t(token), name, C.int(d.Format().SampleRate), C.int(d.Format().Channels), &code)
	if p == nil {
		bridge.Free(token)
		if err := pwError(code); err != nil {
			return nil, err
		}
		return nil, render.ErrDriver
	}
	return &driver{p: p, token: token}, nil
}
func (d *driver) Start() error { return pwError(C.sendspin_pw_start(d.p)) }
func (d *driver) Stop() error  { return pwError(C.sendspin_pw_stop(d.p)) }
func (d *driver) Check() error { return pwError(C.sendspin_pw_check(d.p)) }
func (d *driver) Clock() int64 { return bridge.Now() }
func (d *driver) Close() error { C.sendspin_pw_close(d.p); bridge.Free(d.token); return nil }
