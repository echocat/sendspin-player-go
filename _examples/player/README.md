# Native player

A Sendspin player with native audio output, server discovery and persistent
pairing credentials. It also receives metadata and supports controller commands.

Run from the repository root:

```shell
mise example player --help
# Show options without opening audio or connecting to a server.

mise example player --pairing-token
# Print the confidential pairing token, then exit.

mise example player --server ws://192.0.2.1:8927/sendspin
# Connect to your server and play incoming audio. Stop with Ctrl+C.
```

Replace the endpoint with your server's address. Without `--server`, the player
uses mDNS discovery. Register its pairing token with your server first; keep the
token and credential file private.

`--output` selects a backend and `--device` its device identifier. The default
`native` output selects the platform backend. `--credentials` overrides the
credential file under the operating system's user-config directory.

See [audio setup](../../docs/audio.md) for backends and device selection, or
[SDK usage](../../docs/usage.md) for embedding a player in your own application.
