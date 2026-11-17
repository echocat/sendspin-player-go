//go:build e2e

package audiotest

import "testing"

func TestE2EScenarioStartupRequiresFreshDecodedAndSignalOutput(t *testing.T) {
	before := scenarioOutputStats{Frames: 48000, SignalWrites: 10}
	for _, test := range []struct {
		name    string
		after   scenarioOutputStats
		decoded int64
		frames  int64
		want    bool
	}{
		{"previous phase", before, 48000, 1, false},
		{"decoded but not rendered", before, 96000, 1, false},
		{"silence only", scenarioOutputStats{Frames: 96000, SignalWrites: 10}, 96000, 1, false},
		{"old decoded tail", scenarioOutputStats{Frames: 96000, SignalWrites: 11}, 48000, 1, false},
		{"fresh signal", scenarioOutputStats{Frames: 96000, SignalWrites: 11}, 96000, 1, true},
		{"rendered just below threshold", scenarioOutputStats{Frames: 95999, SignalWrites: 11}, 96000, 48000, false},
		{"decoded just below threshold", scenarioOutputStats{Frames: 96000, SignalWrites: 11}, 95999, 48000, false},
		{"sustained threshold reached", scenarioOutputStats{Frames: 96000, SignalWrites: 11}, 96000, 48000, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := scenarioPlaybackProgress(before, test.after, 48000, test.decoded, test.frames); got != test.want {
				t.Fatalf("started=%t, want %t", got, test.want)
			}
		})
	}
}
