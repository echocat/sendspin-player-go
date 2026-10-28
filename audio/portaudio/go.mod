module github.com/echocat/sendspin-player-go/audio/portaudio

go 1.27.3

require (
	github.com/echocat/sendspin-player-go v0.1.1
	github.com/gordonklaus/portaudio v0.0.0-20260203164431-765aa7dfa631
)

require golang.org/x/sys v0.49.0 // indirect

replace github.com/echocat/sendspin-player-go => ../..
