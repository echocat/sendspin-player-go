package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	sendspin "github.com/echocat/sendspin-player-go"
	"github.com/echocat/sendspin-player-go/codec"
	log "github.com/echocat/slf4g"
	_ "github.com/echocat/slf4g/native" // Applications, never the SDK, choose the output.
)

func main() {
	if err := run(); err != nil {
		log.WithError(err).Error("Player stopped")
		os.Exit(1)
	}
}

func run() error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	name := flag.String("name", "Go player", "Friendly player name")
	endpoint := flag.String("server", "", "Server ws:// endpoint (empty: discover servers)")
	credentials := flag.String("credentials", filepath.Join(configDir, "sendspin-player-go", "device.json"), "Private credential file")
	showToken := flag.Bool("pairing-token", false, "Print the confidential pairing token and exit")
	backend := flag.String("output", "oto", "Audio output: oto or portaudio")
	approximate := flag.Bool("allow-approximate", false, "Allow Oto's estimated timing and software-only flush (not physical RC1 conformance)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	output, err := newOutput(*backend, *approximate)
	if err != nil {
		return err
	}
	client, err := sendspin.New(ctx, sendspin.Config{Name: *name, Store: sendspin.FileStore{Path: *credentials}, Output: output, Roles: []sendspin.Role{sendspin.Player, sendspin.Controller, sendspin.MetadataRole}, Formats: []codec.Format{{Codec: "opus", SampleRate: 48000, Channels: 2, BitDepth: 16}, {Codec: "flac", SampleRate: 48000, Channels: 2, BitDepth: 24}, {Codec: "pcm", SampleRate: 48000, Channels: 2, BitDepth: 16}}, RequiredLeadTime: 250 * time.Millisecond, MinBuffer: 100 * time.Millisecond})
	if err != nil {
		return err
	}
	if *showToken {
		fmt.Println(client.PairingToken())
		return nil
	}
	if *endpoint != "" {
		err = client.Run(ctx, *endpoint)
	} else {
		err = client.DiscoverAndRun(ctx)
	}
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
