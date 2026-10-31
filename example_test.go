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

func ExampleClient_RestartPairing() {
	ctx := context.Background()
	client, err := sendspin.New(ctx, sendspin.Config{
		Name: "Automatic controller", Store: &sendspin.MemoryStore{},
		Roles:       []sendspin.Role{sendspin.Controller},
		CodePairing: &sendspin.CodePairingConfig{Automatic: true},
		Logger:      recording.NewLogger(),
	})
	if err != nil {
		panic(err)
	}
	// A product uses a durable Store. Authenticate/authorize the administrator
	// and obtain explicit confirmation BEFORE invoking this operation.
	result, err := client.RestartPairing(ctx)
	if err != nil {
		// result.Committed distinguishes an irreversible reset from no commit.
		panic(err)
	}
	fmt.Println(result.Committed, client.PairingState().Ready)
	_, visible := client.CurrentPairingPIN() // No PIN before server nonce exchange.
	fmt.Println(visible)
	// Output:
	// true true
	// false
}
