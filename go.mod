module github.com/echocat/sendspin-player-go

go 1.27.3

require (
	filippo.io/edwards25519 v1.2.0
	github.com/coder/websocket v1.8.15
	github.com/ebitengine/purego v0.11.2
	github.com/echocat/slf4g v1.9.0
	github.com/flynn/noise v1.1.0
	github.com/mewkiz/flac v1.0.14
	github.com/miekg/dns v1.1.73
	github.com/thesyncim/gopus v0.2.2
	golang.org/x/crypto v0.58.0
	golang.org/x/mod v0.42.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.49.0
)

require (
	github.com/icza/bitio v1.1.0 // indirect
	github.com/mewkiz/pkg v0.0.0-20260703220044-4fb89b18cc87 // indirect
	github.com/mewpkg/term v0.0.0-20241026122259-37a80af23985 // indirect
)

ignore (
	.github
	LICENSES
	_examples
	docs
	mise-tasks
	var
)
