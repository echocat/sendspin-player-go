package playback

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/audio"
)

func TestHostManagedUnityGainIgnoresVolumeMuteAndPriorGain(t *testing.T) {
	for _, volume := range []int{0, 1, 50, 100} {
		for _, muted := range []bool{false, true} {
			for _, prior := range []float32{0, 0.1, 1} {
				t.Run(fmt.Sprintf("%d/%v/%g", volume, muted, prior), func(t *testing.T) {
					target := time.Now()
					device := &testDevice{next: target}
					engine := NewUnityGain(nil, 65535, func(int64) (time.Time, error) { return target, nil })
					engine.State(volume, muted, 0)
					samples := []float32{0.25, -0.5, 0.75, -1, 0, 1}
					gain := prior
					if err := engine.render(context.Background(), device, Chunk{Format: audio.Format{SampleRate: 48000, Channels: 1}, Samples: samples}, &gain); err != nil {
						t.Fatal(err)
					}
					if len(device.writes) != 1 || !slices.Equal(device.writes[0], samples) || gain != 1 {
						t.Fatal("external/fixed control changed sample amplitudes", device.writes, gain)
					}
				})
			}
		}
	}
}
