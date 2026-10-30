//go:build e2e

package audiotest

func (s sourceObservation) ready() bool {
	ready := false
	for _, event := range s.Events {
		if event.Type == "client/state" {
			ready = event.Available && sourceObject(event.Source)
		}
	}
	return ready
}
