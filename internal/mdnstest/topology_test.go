package mdnstest

import (
	"bytes"
	"errors"
	"testing"
)

func TestTopologyControlIsBoundedOrderedAndScoped(t *testing.T) {
	var output bytes.Buffer
	var changes []bool
	w := &topologyWriter{destination: &output, change: func(attach bool) error { changes = append(changes, attach); return nil }}
	for _, part := range []string{"ordinary output\n[mdns-topo", "logy] attach\n", "[mdns-topology] detach\n"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if len(changes) != 2 || !changes[0] || changes[1] || w.stage != 0 || w.cycles != 1 {
		t.Fatal(changes)
	}
	if _, err := w.Write([]byte("[mdns-topology] attach\n[mdns-topology] attach\n")); err == nil {
		t.Fatal("accepted an attach before the previous cycle detached")
	}
	w = &topologyWriter{destination: &output, change: func(bool) error { return errors.New("engine failure") }}
	if _, err := w.Write([]byte("[mdns-topology] detach\n")); err == nil {
		t.Fatal("accepted out-of-order detach")
	}
	if _, err := w.Write([]byte("[mdns-topology] attach\n")); err == nil || w.stage != 0 {
		t.Fatal("swallowed engine failure")
	}
}

func TestTopologyControlSupportsCountAndPreservesGoTestFraming(t *testing.T) {
	var output bytes.Buffer
	var changes []bool
	w := &topologyWriter{destination: &output, change: func(attach bool) error { changes = append(changes, attach); return nil }}
	input := "\x16=== RUN   TestE2EMDNSActualInterfaceAttachAndRemoval\n[mdns-topology] attach\n[mdns-topology] detach\n\x16--- PASS: TestE2EMDNSActualInterfaceAttachAndRemoval (1.00s)\n"
	for range 2 {
		for _, b := range []byte(input) {
			if _, err := w.Write([]byte{b}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if output.String() != input+input || w.cycles != 2 || w.stage != 0 || len(changes) != 4 {
		t.Fatal("-count or test2json framing lost", output.String(), changes)
	}
	if _, err := w.Write(bytes.Repeat([]byte{'x'}, (256<<10)+1)); err == nil {
		t.Fatal("unbounded control line accepted")
	}
}
