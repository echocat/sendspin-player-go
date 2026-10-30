// Command loopbackcmd generates reference WAV files and analyzes recordings offline.
package main

import (
	"fmt"
	"os"

	"github.com/echocat/sendspin-player-go/internal/loopback"
)

func main() {
	if err := loopback.Run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
