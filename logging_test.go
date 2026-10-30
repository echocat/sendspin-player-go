package sendspin

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/protocol"
	"github.com/echocat/slf4g/level"
	"github.com/echocat/slf4g/testing/recording"
)

// Concrete logging belongs to the test executable, never the SDK runtime.
func TestMain(m *testing.M) {
	restore := recording.NewProvider().HookGlobally()
	result := runPackageTests(m)
	restore()
	os.Exit(result)
}

func TestLoggingIsInjectableStructuredAndCredentialRedacted(t *testing.T) {
	l := recording.NewLogger()
	l.SetLevel(level.Debug)
	c, err := New(context.Background(), Config{Name: "private-name-SP:SECRET", Store: &MemoryStore{}, Roles: []Role{}, Logger: l})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			c.logger().With("error_class", logErrorClass(errors.New("SP:SECRET ws://private/?psk=SECRET"))).Debug("Connection attempt ended")
		})
	}
	workers.Wait()
	if l.Len() != 9 {
		t.Fatal("logger injection lost events", l.Len())
	}
	for _, event := range l.GetAll() {
		if err := event.ForEach(func(k string, v any) error {
			value, _ := v.(string)
			if strings.Contains(value, "SECRET") || k == "error" {
				t.Fatal("private value in SDK logging", k)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "none"}, {context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline"},
		{protocol.ErrProtocol, "protocol"}, {ErrConnectionLimit, "capacity"},
		{&GoodbyeError{Reason: "pairing_required"}, "pairing_required"},
		{&GoodbyeError{Reason: "SP:SECRET"}, "operation_failed"},
	} {
		if got := logErrorClass(tc.err); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}
