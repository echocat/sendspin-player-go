// Package sendspin implements a Sendspin player SDK.
//
// The initial protocol profile is specification 1.0.0-rc1. A Client owns its
// connection and shared playback pipeline, while applications provide persistent
// storage and an audio.Output. Roles are explicitly enabled in Config.
//
// New may persist credentials but starts no network or audio worker. Cancel and
// join Connect, Run, DiscoverAndRun or Serve to stop those operations; a Client is
// reusable and has no process-global Close operation. Serve owns its listener;
// ServeHTTP only owns the upgraded connection, not the enclosing HTTP server.
//
// Events is a bounded, best-effort single channel, not a broadcast or durable
// audit stream, and remains open across sessions. Consumers must also select on
// their own cancellation context. Snapshot and CurrentArtwork provide copies of
// the latest published state when events are missed.
//
// The identity private key and pairing token are secrets; ClientID is the public
// identity. Never log a Credentials value or expose its serialized form to peers.
// Logging uses only the slf4g facade; applications choose the output provider.
package sendspin
