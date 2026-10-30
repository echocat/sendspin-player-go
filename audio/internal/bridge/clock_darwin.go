//go:build darwin

package bridge

import (
	"sync"

	"github.com/echocat/sendspin-player-go/audio/internal/dynlib"
)

var machClock struct {
	once         sync.Once
	absolute     func() uint64
	numer, denom uint32
}

func Now() int64 {
	machClock.once.Do(func() {
		lib, err := dynlib.Open("", "/usr/lib/libSystem.B.dylib")
		if err != nil {
			return
		}
		var info func(*[2]uint32) int32
		if lib.Bind(&info, "mach_timebase_info") != nil || lib.Bind(&machClock.absolute, "mach_absolute_time") != nil {
			return
		}
		var ratio [2]uint32
		if info(&ratio) == 0 {
			machClock.numer, machClock.denom = ratio[0], ratio[1]
		}
	})
	if machClock.absolute == nil || machClock.denom == 0 {
		return 0
	}
	return timestamp(float64(machClock.absolute()) * float64(machClock.numer) / float64(machClock.denom))
}
