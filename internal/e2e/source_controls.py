"""Source scenarios using unmodified aiosendspin 10.2.1 source APIs/decoder.

The optional transport probe only observes decrypted ordering and metadata; it
does not implement/replace source authorization, Noise, framing or decoding.
Decoded PCM is hashed, never included in logs or control responses.
"""

import asyncio
import hashlib
import json
import struct

from aiohttp import WSMsgType, web
from aiosendspin.server.roles.source.events import SourceStreamStartedEvent, SourceStreamEndedEvent
from pairing_observer import current_scope


class SourceControls:
    commands = {"source_start", "source_stop", "source_status", "source_approve", "source_revoke"}

    def __init__(self, server, client_id):
        self.server = server
        self.client_id = client_id
        self.events = []
        self.streams = []
        self.tasks = []
        client = server.get_or_create_client(client_id)
        self.client = client
        self.unsubscribe = client.add_event_listener(self.on_event)
        self.scope = current_scope.get()
        self.scope.source = self

    def observe(self, message):
        if len(self.events) >= 512:
            return
        if message.type is WSMsgType.TEXT:
            body = json.loads(message.data)
            kind, payload = body.get("type"), body.get("payload", {})
            if kind == "client/hello":
                self.events.append({"type": kind, "support": payload.get("source@v1_support")})
            elif kind in ("client-stream/start", "client-stream/end"):
                self.events.append({"type": kind, "payload": payload})
            elif kind == "client/state":
                self.events.append({"type": kind, "available": payload.get("available"), "source": payload.get("source")})
        elif message.type is WSMsgType.BINARY and message.data and message.data[0] == 12:
            self.events.append({"type": "audio", "timestamp": str(struct.unpack(">q", message.data[1:9])[0]), "bytes": len(message.data)-9})

    def on_event(self, _client, event):
        if isinstance(event, SourceStreamStartedEvent):
            fmt = event.audio_format
            record = {"sample_rate": fmt.sample_rate, "channels": fmt.channels, "bit_depth": fmt.bit_depth, "bytes": 0, "chunks": 0, "ended": False, "sha256": hashlib.sha256(b"").hexdigest(), "first_timestamp": None}
            self.streams.append(record)

            async def consume():
                digest = hashlib.sha256()
                async for pcm, timestamp in event.handle:
                    digest.update(pcm)
                    record["bytes"] += len(pcm)
                    record["chunks"] += 1
                    record["sha256"] = digest.hexdigest()
                    if record["first_timestamp"] is None:
                        record["first_timestamp"] = str(timestamp)
                record["ended"] = True

            self.tasks.append(asyncio.create_task(consume()))
        elif isinstance(event, SourceStreamEndedEvent):
            pass  # Consumer completion, not event timing, marks decoded drain.

    async def run(self, client, command):
        if client is not None and client is not self.client:
            self.unsubscribe()
            self.client = client
            self.unsubscribe = client.add_event_listener(self.on_event)
        kind = command["command"]
        if kind == "source_status":
            return {"events": self.events, "streams": self.streams, "server_now": str(self.server.clock.now_us())}
        if kind == "source_approve":
            await self.server.trust_unpaired(self.client_id, approved_roles={"source"})
            return {"ok": True}
        if kind == "source_revoke":
            await self.server.trust_unpaired(self.client_id, approved_roles=set())
            return {"ok": True}
        role = client.role("source@v1") if client is not None and client.is_connected else None
        if role is None:
            raise web.HTTPConflict(text="source role is not active")
        if kind == "source_start":
            role.request_start()
        else:
            role.request_stop()
        return {"ok": True}

    async def close(self):
        self.unsubscribe()
        for task in self.tasks:
            task.cancel()
        await asyncio.gather(*self.tasks, return_exceptions=True)
        self.scope.source = None
