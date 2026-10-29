module github.com/echocat/sendspin-player-go/_examples/audio-probe

go 1.27.3

require (
	github.com/echocat/sendspin-player-go v0.1.1
	github.com/echocat/sendspin-player-go/audio/native v0.0.0
	github.com/echocat/slf4g v1.9.0
	github.com/echocat/slf4g/native v1.9.0
)

require (
	github.com/ebitengine/purego v0.11.2 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/echocat/sendspin-player-go => ../..

replace github.com/echocat/sendspin-player-go/audio/native => ../../audio/native
