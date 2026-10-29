// Package bridge owns callback tokens and native-clock arithmetic. Native code
// receives integer tokens, never retained Go pointers. Free removes the token
// only after driver teardown; late callbacks safely find no state.
package bridge

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/echocat/sendspin-player-go/audio/internal/render"
)

var sequence atomic.Uint64
var states sync.Map

func Register(v any) uintptr {
	id := uintptr(sequence.Add(1))
	if id == 0 {
		panic("native callback token space exhausted")
	}
	states.Store(id, v)
	return id
}
func Value(id uintptr) any         { v, _ := states.Load(id); return v }
func New(d *render.Device) uintptr { return Register(d) }
func Free(id uintptr)              { states.Delete(id) }
func Render(id uintptr, out *float32, count int, first int64) {
	d, ok := Value(id).(*render.Device)
	if !ok {
		if out != nil && count > 0 && count <= 384000*8/2 {
			clear(unsafe.Slice(out, count))
		}
		return
	}
	if out == nil || count <= 0 || count > d.MaxSamples() {
		d.Fail(render.ErrDriver)
		return
	}
	d.Process(unsafe.Slice(out, count), first)
}
func Fail(id uintptr) {
	if d, ok := Value(id).(*render.Device); ok {
		d.Fail(render.ErrDriver)
	}
}
