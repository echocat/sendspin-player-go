package alsa_test

import (
	"fmt"

	"github.com/echocat/sendspin-player-go/audio"
	"github.com/echocat/sendspin-player-go/audio/alsa"
)

func ExampleOutput() {
	// Pass this output to sendspin.Config.Output. Construction opens no devices.
	var output audio.Output = alsa.Output{Device: "hw:CARD=Device,DEV=0"}
	fmt.Printf("%T\n", output)
	// Output: alsa.Output
}
