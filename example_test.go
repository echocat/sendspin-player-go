package sendspin_test

import (
	"context"
	"errors"
	"fmt"

	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/slf4g/testing/recording"
)

func ExampleNew() {
	// MemoryStore is deliberately ephemeral; use a private FileStore or another
	// durable Store for a product identity. A controller needs no audio backend.
	client, err := sendspin.New(context.Background(), sendspin.Config{Name: "Controller", Store: &sendspin.MemoryStore{}, Roles: []sendspin.Role{sendspin.Controller}, Logger: recording.NewLogger()})
	if err != nil {
		panic(err)
	}
	fmt.Println(client.Snapshot().Available)
	fmt.Println(errors.Is(client.LeaveGroup(context.Background()), sendspin.ErrNotConnected))
	// Output:
	// false
	// true
}
