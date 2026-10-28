module github.com/echocat/sendspin-player-go/_examples/audio-probe

go 1.27.3

require (
	github.com/echocat/sendspin-player-go v0.1.1
	github.com/echocat/sendspin-player-go/audio/portaudio v0.1.1
	github.com/echocat/slf4g v1.9.0
	github.com/echocat/slf4g/native v1.9.0
	github.com/gordonklaus/portaudio v0.0.0-20260203164431-765aa7dfa631
)

require (
	github.com/ebitengine/oto/v3 v3.5.1 // indirect
	github.com/ebitengine/purego v0.11.2 // indirect
	github.com/jfreymuth/pulse v0.1.3 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/echocat/sendspin-player-go => ../..

replace github.com/echocat/sendspin-player-go/audio/portaudio => ../../audio/portaudio
