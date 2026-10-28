//go:build cgo && (linux || windows || darwin)

package bridge

/*
#include "bridge.h"
*/
import "C"

import (
	"github.com/echocat/sendspin-player-go/audio/native/internal/render"
	"runtime/cgo"
	"unsafe"
)

func New(d *render.Device) uintptr { return uintptr(cgo.NewHandle(d)) }
func Free(token uintptr)           { cgo.Handle(token).Delete() }
func Now() int64                   { return int64(C.sendspin_native_now()) }

//export sendspinNativeRender
func sendspinNativeRender(token C.uintptr_t, out *C.float, count C.int, first C.int64_t) {
	d := cgo.Handle(token).Value().(*render.Device)
	if out == nil || count <= 0 || int(count) > d.MaxSamples() {
		d.Fail(render.ErrDriver)
		return
	}
	d.Process(unsafe.Slice((*float32)(unsafe.Pointer(out)), int(count)), int64(first))
}

//export sendspinNativeFail
func sendspinNativeFail(token C.uintptr_t) {
	cgo.Handle(token).Value().(*render.Device).Fail(render.ErrDriver)
}
