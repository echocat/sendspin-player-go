module github.com/echocat/sendspin-player-go/_examples/player

go 1.27.3

require (
	github.com/echocat/sendspin-player-go v0.1.1
	github.com/echocat/sendspin-player-go/audio/portaudio v0.1.1
	github.com/echocat/slf4g v1.9.0
	github.com/echocat/slf4g/native v1.9.0
)

require (
	github.com/coder/websocket v1.8.15 // indirect
	github.com/ebitengine/oto/v3 v3.5.1 // indirect
	github.com/ebitengine/purego v0.11.2 // indirect
	github.com/flynn/noise v1.1.0 // indirect
	github.com/gordonklaus/portaudio v0.0.0-20260203164431-765aa7dfa631 // indirect
	github.com/icza/bitio v1.1.0 // indirect
	github.com/jfreymuth/pulse v0.1.3 // indirect
	github.com/mewkiz/flac v1.0.14 // indirect
	github.com/mewkiz/pkg v0.0.0-20260703220044-4fb89b18cc87 // indirect
	github.com/mewpkg/term v0.0.0-20241026122259-37a80af23985 // indirect
	github.com/miekg/dns v1.1.73 // indirect
	github.com/thesyncim/gopus v0.2.2 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/echocat/sendspin-player-go => ../..

replace github.com/echocat/sendspin-player-go/audio/portaudio => ../../audio/portaudio
