"""Thin scenario driver around the unmodified aiosendspin 10.2.1 server.

Only test control is ours: Noise, pairing, negotiation, chunking, encoding and
clock replies are implemented by the independent library. Secrets arrive through
a private temporary config file, never process arguments or protocol logs.
"""

import argparse
import asyncio
import hashlib
import json
import logging
import struct

from aiohttp import web
from aiosendspin.models.types import ConnectionReason, PairMethod, PairingCodeFormat
from aiosendspin.noise.keys import Identity, b64url_decode, psk_id_for
from aiosendspin.noise.pairing import PairingAttempt, PairingAbortError
from aiosendspin.noise.trust_store import (
    InMemoryServerPairingStore,
    ServerPairingRecord,
    StagedPairingPsk,
)
from aiosendspin.server.audio import AudioFormat
from aiosendspin.server.roles import AudioChunk
from aiosendspin.server.server import (
    ClientCredentialMismatchEvent,
    ClientDisconnectedEvent,
    SendspinServer,
)
from media import MediaControls
from mdns import MDNSPeer
from pairing_observer import PairingObserver
from weak_playback import WeakPlayback
from source_controls import SourceControls


class ControlledStore(InMemoryServerPairingStore):
    """Exercise an in-progress attempt without replacing the pairing protocol."""

    def __init__(self):
        super().__init__()
        self.gate = asyncio.Event()
        self.gate.set()
        self.pending = 0

    async def store_record(self, record):
        self.pending += 1
        try:
            await self.gate.wait()
            await super().store_record(record)
        finally:
            self.pending -= 1


class InjectedMessage:
    """Explicit fault/extension injection; still uses the real encrypted writer."""

    def __init__(self, body):
        self.body = body

    def to_json(self):
        return json.dumps(self.body)

    def merge(self, other):
        return None


async def main(config_path):
    with open(config_path, encoding="utf-8") as source:
        config = json.load(source)
    store = ControlledStore()
    client_id = config["client_id"]
    psk = b64url_decode(config["psk"])
    device_psk = b64url_decode(config.get("device_pairing_psk", config["psk"]))
    pairing = config.get("pairing", False)
    if pairing:
        await store.stage_pairing_psk(
            client_id, StagedPairingPsk(psk_id=psk_id_for(device_psk), psk=device_psk)
        )
    elif not config.get("no_record", False):
        await store.store_record(
            ServerPairingRecord(
                client_id=client_id,
                psk_id=psk_id_for(psk),
                psk=psk,
                pair_methods=[PairMethod.PAIRING_PSK],
            )
        )
    server = SendspinServer(
        asyncio.get_running_loop(),
        Identity.generate(),
        "RC1 independent E2E",
        pairing_store=store,
        allow_noncompliant_clients=False,
    )
    if config.get("hold_pairing", False):
        store.gate.clear()
    events = []
    pairing_observer = (
        PairingObserver(
            events,
            initial_activation=config.get("initial_activation"),
            pairing_activation=config.get("pairing_activation"),
            stale_messages=config.get("pairing_stale_messages"),
            hold_followup=config.get("hold_followup_activation", False),
            initial_hello=config.get("initial_hello"),
            observe_controls=config.get("observe_controls", False),
            observe_core=config.get("observe_core", False),
            repeat_initial_activation=config.get("repeat_initial_activation", False),
        )
        if config.get("observe_pairing", False) or config.get("observe_controls", False) or config.get("observe_core", False) else None
    )

    def observe_event(_server, event):
        if isinstance(event, ClientDisconnectedEvent):
            events.append(
                {
                    "type": "disconnected",
                    "reason": event.goodbye_reason.value if event.goodbye_reason else None,
                }
            )
        elif isinstance(event, ClientCredentialMismatchEvent):
            events.append({"type": "credential_mismatch"})

    server.add_event_listener(observe_event)
    media = MediaControls(server)
    sources = SourceControls(server, client_id) if config.get("observe_source", False) else None
    weak = WeakPlayback(config["weak_reference_path"]) if "weak_reference_path" in config else None
    stream = None
    pump_task = None
    pump_stop = None
    pump_samples = 0
    stop = asyncio.Event()
    operator_codes = asyncio.Queue()
    mdns = MDNSPeer(server, config["mdns"], client_id) if "mdns" in config else None

    async def control(request):
        nonlocal stream, pump_task, pump_stop, pump_samples
        command = await request.json()
        client = server.get_client(client_id)
        kind = command["command"]
        if sources is not None and kind in sources.commands:
            return web.json_response(await sources.run(client, command))
        if kind in ("weak_start", "weak_stop", "weak_disconnect", "weak_status"):
            if weak is None:
                raise web.HTTPConflict(text="weak reference scenario is not configured")
            if kind == "weak_start":
                return web.json_response(await weak.start(client))
            if kind == "weak_stop":
                return web.json_response(await weak.stop(client))
            if kind == "weak_status":
                player = client.role("player@v1") if client is not None and client.is_connected else None
                selected = player.preferred_codec if player is not None else None
                return web.json_response({"codec": selected.value if selected is not None else None, "source_frames": weak.frames})
            if client is None or client.connection is None or weak.task is not None:
                raise web.HTTPConflict(text="disconnect needs a connected client and stopped producer")
            await client.connection.disconnect()
            return web.json_response({"ok": True})
        if kind == "pump_start":
            if client is None or not client.is_connected or pump_task is not None:
                raise web.HTTPConflict(text="pump needs one connected client and no existing pump")
            stream = client.group.start_stream()
            pump_stop = asyncio.Event()
            pcm = b"".join(struct.pack("<h", i % 2001 - 1000) for i in range(4800))

            async def pump():
                nonlocal pump_samples
                next_tick = asyncio.get_running_loop().time()
                while not pump_stop.is_set():
                    stream.prepare_audio(pcm, AudioFormat(sample_rate=48000, channels=1, bit_depth=16))
                    await stream.commit_audio()
                    pump_samples += 4800
                    next_tick += 0.1
                    await asyncio.sleep(max(0, next_tick - asyncio.get_running_loop().time()))

            pump_task = asyncio.create_task(pump())
            return web.json_response({"source_samples": pump_samples})
        elif kind == "pump_stop":
            if pump_task is None:
                raise web.HTTPConflict(text="no running pump")
            pump_stop.set()
            await asyncio.wait_for(pump_task, 4)
            pump_task = None
            await asyncio.wait_for(client.wait_role_drained("player"), 4)
            stream.stop()
            return web.json_response({"source_samples": pump_samples})
        if kind in media.commands:
            return web.json_response(await media.run(client, command))
        elif kind == "connect":
            attempt = (
                PairingAttempt(
                    method=PairMethod.PAIRING_PSK, pairing_psk=device_psk, client_id=client_id
                )
                if pairing and command.get("pairing", True)
                else None
            )
            server.connect_to_client(
                command["url"],
                pairing_attempt=attempt,
                connection_reason=(
                    ConnectionReason.PLAYBACK if command.get("playback", False)
                    else ConnectionReason.DISCOVERY
                ),
            )
        elif kind in ("pair", "pair_at"):
            if kind == "pair_at":
                server.get_or_create_client(client_id)
                server.register_client_url(client_id, command["url"])
            try:
                await server.initiate_pairing(
                    client_id,
                    PairingAttempt(
                        method=PairMethod.PAIRING_PSK,
                        pairing_psk=device_psk,
                        client_id=client_id,
                    ),
                )
            except PairingAbortError as error:
                return web.json_response({"pair_abort": error.reason.value})
        elif kind == "submit_code":
            # Private loopback operator input; never put the code into events.
            operator_codes.put_nowait(command["code"])
        elif kind == "pair_code":
            method = PairMethod(command["method"])
            async def provide_code():
                return await operator_codes.get()
            try:
                await server.initiate_pairing(client_id, PairingAttempt(
                    method=method, pairing_code_provider=provide_code,
                    pairing_format=PairingCodeFormat.DIGITS if method is PairMethod.DYNAMIC_PAIRING_CODE else None,
                    on_pair_pending=lambda _message: events.append({"type": "code_pending"}),
                ))
            except PairingAbortError as error:
                return web.json_response({"pair_abort": error.reason.value})
        elif kind == "release_pairing":
            store.gate.set()
        elif kind == "release_activation":
            if pairing_observer is None:
                raise web.HTTPConflict(text="pairing observer is not enabled")
            pairing_observer.followup_gate.set()
        elif kind == "disconnect":
            server.disconnect_from_client(command["url"])
        elif kind == "forget":
            # Simulate storage loss, not an intentional server/unpair operation.
            await store.remove_record(client_id)
        elif kind == "trust_unpaired":
            await server.trust_unpaired(client_id)
        elif kind == "send":
            if client is None or client.connection is None:
                raise web.HTTPConflict(text="client is not connected")
            client.send_message(InjectedMessage(command["message"]))
        elif kind == "binary":
            if client is None or client.connection is None:
                raise web.HTTPConflict(text="client is not connected")
            client.connection.send_priority_message(b64url_decode(command["data"]))
        elif kind == "status":
            return web.json_response(
                {
                    "connected": client is not None and client.is_connected,
                    "available": client.available if client else False,
                    "active_roles": list(client.active_role_ids) if client else [],
                    "pairing_record": await store.record_by_client_id(client_id) is not None,
                    "pair_finalize_pending": store.pending > 0,
                    "events": events,
                    "mdns": mdns.status() if mdns is not None else None,
                    "client_name": client.info.name if client is not None and client.is_connected else None,
                }
            )
        elif kind in ("stream", "more"):
            if client is None or not client.is_connected:
                raise web.HTTPConflict(text="client is not connected")
            player = client.role("player@v1")
            original_on_chunk = player.on_audio_chunk
            duration_us = 0

            def observe_chunk(chunk):
                nonlocal duration_us
                duration_us += chunk.duration_us
                original_on_chunk(chunk)

            player.on_audio_chunk = observe_chunk
            if kind == "stream":
                stream = client.group.start_stream()
            elif stream is None:
                raise web.HTTPConflict(text="no active stream")
            # Exactly representable, non-silent ramp, 300ms at 48kHz mono.
            sample_count = command.get("frames", 14400)
            pcm = b"".join(struct.pack("<h", i % 2001 - 1000) for i in range(sample_count))
            stream.prepare_audio(
                pcm, AudioFormat(sample_rate=48000, channels=1, bit_depth=16)
            )
            await stream.commit_audio()
            # PushStream does not expose an EOF operation. Drain the reference
            # encoder through its public transformer/role hooks, so a final
            # partial FLAC frame is verified rather than silently ignored.
            transformer = player.get_audio_requirements().transformer
            timestamp = transformer.pending_timestamp_us
            for payload, duration in transformer.flush():
                assert timestamp is not None
                player.on_audio_chunk(
                    AudioChunk(
                        data=payload,
                        timestamp_us=timestamp,
                        duration_us=duration,
                        byte_count=len(payload),
                    )
                )
                timestamp += duration
            await asyncio.sleep(0.8)
            player.on_audio_chunk = original_on_chunk
            # The reference encoder may pad its final frame with silence. Count
            # that explicitly, while hashing every original source sample.
            # Use durations of actual role chunks, not endpoint subtraction:
            # Opus timestamps also incorporate the encoder's lookahead offset.
            decoded_samples = round(duration_us * 48000 / 1_000_000)
            return web.json_response(
                {
                    "pcm_sha256": hashlib.sha256(pcm).hexdigest(),
                    "source_samples": sample_count,
                    "decoded_samples": decoded_samples,
                }
            )
        elif kind == "volume":
            player = client.role("player@v1")
            player.set_mute(True)
            player.set_volume(37)
        elif kind == "clear":
            if stream is not None:
                stream.clear()
        elif kind == "stop":
            if stream is not None:
                stream.stop()
        elif kind == "unpair":
            await server.unpair(client_id)
        elif kind == "shutdown":
            stop.set()
        else:
            raise web.HTTPBadRequest(text="unknown test command")
        return web.json_response({"ok": True})

    app = web.Application()
    app.router.add_get("/sendspin", server.on_client_connect)
    app.router.add_post("/control", control)
    runner = web.AppRunner(app, shutdown_timeout=2)
    await runner.setup()
    site = web.TCPSite(runner, "0.0.0.0" if mdns is not None else "127.0.0.1", 0)
    await site.start()
    # aiohttp exposes bound sockets on AppRunner's addresses without reserving
    # and reopening a port (which would introduce a race).
    port = runner.addresses[0][1]
    if mdns is not None and config["mdns"].get("family") == 6:
        await web.TCPSite(runner, "::", port).start()
    if mdns is not None:
        await mdns.start(port)
    print(json.dumps({"url": f"http://127.0.0.1:{port}", "server_id": server.id}), flush=True)
    try:
        await stop.wait()
    finally:
        if weak is not None:
            await weak.close()
        if pump_task is not None:
            pump_task.cancel()
            await asyncio.gather(pump_task, return_exceptions=True)
        media.close()
        if mdns is not None:
            await mdns.close()
        await server.close()
        if sources is not None:
            await sources.close()
        await runner.cleanup()
        if pairing_observer is not None:
            pairing_observer.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    args = parser.parse_args()
    logging.basicConfig(level=logging.WARNING)
    asyncio.run(main(args.config))
