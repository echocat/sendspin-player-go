package sendspin

import (
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/monotime"
)

func TestUnconvergedClockRetriesAfterQuantizedInvalidBurst(t *testing.T) {
	for _, converged := range []bool{false, true} {
		s := idleActivationSession(t)
		if converged {
			for _, at := range []int64{1, 2, 3} {
				s.filter.Update(0, 1, at)
			}
		}
		s.timeOutstanding, s.arrival, s.burstLeft = 100, 100, 1
		before := monotime.Now()
		if err := s.timeSample([]byte(`{"client_transmitted":100,"server_received":500,"server_transmitted":500}`)); err != nil {
			t.Fatal(err)
		}
		if s.filter.Ready() != converged || s.bestUncertainty != 0 {
			t.Fatal("zero-RTT sample was fabricated into a valid observation")
		}
		if !converged && s.nextBurst.After(before.Add(100*time.Millisecond)) {
			t.Fatal("unconverged filter sleeps a full steady-state interval after an invalid/quantized burst")
		}
		if converged && (s.nextBurst.Before(before.Add(10*time.Second)) || s.nextBurst.After(monotime.Now().Add(10*time.Second))) {
			t.Fatal("steady-state burst interval changed")
		}
	}
}
