"""Media scenarios driven through the pinned reference SDK's role/model APIs.

Encoded image bytes are observed before reference framing, not reconstructed from
the Go client's result. Visualizer vectors use the reference packer and normal
role queue. They test transport/scheduling, not the reference FFT's quality.
"""

import asyncio
import hashlib
import io
import itertools
import json
import random
import struct

from aiohttp import web
from aiosendspin.models.core import ServerActivateMessage, ServerActivatePayload
from aiosendspin.models.types import ArtworkSource, BinaryMessageType, MediaCommand, RepeatMode
from aiosendspin.models.visualizer import BeatTiming
from aiosendspin.noise.wire import EncryptedWebSocket
from aiosendspin.server.roles.base import AudioChunk
from aiosendspin.server.roles.color.state import Color
from aiosendspin.server.roles.metadata.state import Metadata
from aiosendspin.server.roles.visualizer.packing import pack_visualizer_frame


class ArtworkWireProbe:
    """One bounded pause after an actually transmitted first image part.

    Only headers/counts are recorded; native packing, FIFO/cancel handling and
    Noise remain unchanged. A same-direction marker precedes the pause, so a
    test can prove the SDK actor processed that partial image before continuing.
    """

    def __init__(self, marker):
        self.marker = marker
        self.frames = []
        self.transfers = {}
        self.armed = True
        self.paused = asyncio.Event()
        self.release = asyncio.Event()
        self._send_bytes = EncryptedWebSocket.send_bytes
        self._send_str = EncryptedWebSocket.send_str

        async def send_bytes(transport, data):
            await self._send_bytes(transport, data)
            if len(data) < 2 or not 8 <= data[0] <= 11:
                return
            channel, flags = data[0] - 8, data[1]
            record = {"channel": channel, "flags": flags, "bytes": len(data)}
            if flags == 2 and len(data) == 14:
                timestamp, size = struct.unpack(">qI", data[2:])
                record.update(timestamp=timestamp, total_size=size)
                self.transfers[channel] = [timestamp, size, 0]
            transfer = self.transfers.get(channel)
            if transfer is not None:
                record["timestamp"] = transfer[0]
                if flags == 0:
                    transfer[2] += len(data) - 2
                if flags == 1 or transfer[2] == transfer[1]:
                    self.transfers.pop(channel, None)
            self.frames.append(record)
            if flags == 0 and self.armed:
                self.armed = False
                await self._send_str(transport, json.dumps({
                    "type": "group/update", "payload": {
                        "group_id": "artwork-wire-probe", "group_name": self.marker,
                        "playback_state": "stopped",
                    },
                }))
                self.paused.set()
                await self.release.wait()

        async def send_str(transport, data):
            await self._send_str(transport, data)
            if '"stream/end"' in data or '"server/activate"' in data:
                message = json.loads(data)
                if message.get("type") in ("stream/end", "server/activate"):
                    self.frames.append({"type": message["type"]})

        EncryptedWebSocket.send_bytes = send_bytes
        EncryptedWebSocket.send_str = send_str

    def close(self):
        self.release.set()
        EncryptedWebSocket.send_bytes = self._send_bytes
        EncryptedWebSocket.send_str = self._send_str


class MediaControls:
    commands = frozenset(
        {
            "metadata", "metadata_clear", "color", "color_clear", "artwork",
            "artwork_cancel", "visualizer", "visualizer_clear", "visualizer_end",
            "roles", "barrier", "clock", "controller", "player_status",
            "artwork_inflight_start", "artwork_inflight_finish",
            "visualizer_start", "visualizer_frame",
        }
    )

    def __init__(self, server):
        self.server = server
        self.markers = itertools.count(1)
        self.artwork_probe = None
        self.probe_channel = 0

    def close(self):
        if self.artwork_probe is not None:
            self.artwork_probe.close()
            self.artwork_probe = None

    async def barrier(self, client):
        # Drain producer queues before enqueuing a unique group/update. Seeing
        # its name proves the Go actor processed every earlier WebSocket message.
        families = {role.split("@", 1)[0] for role in client.active_role_ids}
        for family in families:
            await asyncio.wait_for(client.wait_role_drained(family), 4)
        marker = f"media-barrier-{next(self.markers)}"
        client.group.set_group_name(marker)
        return marker

    def timestamp(self, command):
        return self.server.clock.now_us() + int(command.get("delay_ms", 0)) * 1000

    async def run(self, client, command):
        if client is None or not client.is_connected:
            raise web.HTTPConflict(text="media scenario requires a connected client")
        kind = command["command"]
        timestamp = self.timestamp(command)
        if kind in ("artwork_inflight_start", "artwork_inflight_finish"):
            return await self.artwork_inflight(client, command)
        if kind == "metadata":
            client.group.group_role("metadata").set_metadata(
                Metadata(**command.get("fields", {})), timestamp_us=timestamp
            )
        elif kind == "metadata_clear":
            client.group.group_role("metadata").clear()
        elif kind == "color":
            fields = {key: tuple(value) for key, value in command.get("fields", {}).items()}
            client.group.group_role("color").set_color(Color(**fields), timestamp_us=timestamp)
        elif kind == "color_clear":
            client.group.group_role("color").clear()
        elif kind == "roles":
            roles = command["roles"]
            if not set(roles).issubset(client.negotiated_role_ids):
                raise web.HTTPBadRequest(text="test cannot activate unadvertised roles")
            old_roles = list(client.active_role_ids)
            client.deactivate_roles(roles)
            for role in set(old_roles) - set(roles):
                await asyncio.wait_for(client.wait_role_drained(role.split("@", 1)[0]), 4)
            # Native teardown hooks queued any required stream/end first. A
            # priority activation then precedes the newly added roles' snapshots.
            client.connection.send_priority_message(
                ServerActivateMessage(ServerActivatePayload(activities=[], active_roles=roles))
            )
            client.set_active_roles(roles)
        elif kind == "controller":
            role = client.group.group_role("controller")
            role.set_seek_max_ms(command.get("seek_max_ms"))
            role.set_supported_commands([MediaCommand(value) for value in command.get("commands", [])])
            if "repeat" in command:
                role.set_repeat(RepeatMode(command["repeat"]))
            if "shuffle" in command:
                role.set_shuffle(command["shuffle"])
        elif kind == "player_status":
            if "player@v1" not in client.active_role_ids:
                raise web.HTTPConflict(text="player role is not active")
            role = client.role("player@v1")
            return {
                "volume": role.volume,
                "muted": role.muted,
                "output_delay_ms": role.output_delay_ms,
                "available": client.available,
            }
        elif kind == "artwork":
            return await self.artwork(client, command, timestamp)
        elif kind == "artwork_cancel":
            await client.group.group_role("artwork").cancel_scheduled(
                ArtworkSource(command.get("source", "album"))
            )
        elif kind == "visualizer":
            return await self.visualizer(client, command, timestamp)
        elif kind == "visualizer_start":
            role = client.role("visualizer@v1")
            role.set_tracks_downbeats(tracks=bool(command.get("tracks_downbeats", False)))
            role.on_stream_start()
        elif kind == "visualizer_frame":
            kind_id = BinaryMessageType(int(command["message_type"]))
            timestamp = int(command.get("timestamp", self.timestamp(command)))
            data = pack_visualizer_frame(kind_id, timestamp, bytes.fromhex(command["data_hex"]))
            client.send_binary(
                data, role_family="visualizer", timestamp_us=timestamp,
                message_type=kind_id.value, buffer_end_time_us=timestamp,
                buffer_byte_count=len(data), duration_us=0,
            )
            return {"timestamp": timestamp, "hex": data.hex(), "marker": await self.barrier(client)}
        elif kind in ("visualizer_clear", "visualizer_end"):
            role = client.role("visualizer@v1")
            if kind == "visualizer_clear":
                role.on_stream_clear()
            else:
                role.on_stream_end()
        elif kind not in ("clock", "barrier"):
            raise web.HTTPBadRequest(text="unknown media command")
        return {
            "timestamp": timestamp,
            "now": self.server.clock.now_us(),
            "marker": await self.barrier(client),
        }

    def probe_image(self, client, channel, seed):
        from PIL import Image

        config = client.role("artwork@v1").get_channel_configs().get(channel)
        if config is None or config.format.value != "png" or config.width * config.height > 1_048_576:
            raise web.HTTPBadRequest(text="probe needs a streamed PNG channel of at most 1M pixels")
        pixels = random.Random(seed).randbytes(config.width * config.height * 3)
        image = Image.frombytes("RGB", (config.width, config.height), pixels)
        output = io.BytesIO()
        image.save(output, format="PNG")
        return output.getvalue()

    async def artwork_inflight(self, client, command):
        role = client.role("artwork@v1")
        if command["command"] == "artwork_inflight_start":
            if self.artwork_probe is not None:
                raise web.HTTPConflict(text="an artwork probe is already running")
            channel = int(command.get("channel", 0))
            data = self.probe_image(client, channel, int(command.get("seed", 31)))
            if len(data) <= 65517:
                raise web.HTTPBadRequest(text="probe image must require multiple parts")
            marker = f"artwork-partial-{next(self.markers)}"
            self.artwork_probe = ArtworkWireProbe(marker)
            self.probe_channel = channel
            timestamp = self.server.clock.now_us() + 15_000_000
            role.send_artwork(channel, data, timestamp)
            return {"gate_marker": marker, "timestamp": timestamp, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}

        probe = self.artwork_probe
        if probe is None or not probe.paused.is_set():
            raise web.HTTPConflict(text="probe has not reached its first transmitted part")
        mode = command["mode"]
        result = {}
        try:
            if mode == "replace":
                data = self.probe_image(client, self.probe_channel, int(command.get("seed", 32)))
                timestamp = self.server.clock.now_us() - 1_000_000  # Lateness never drops artwork.
                role.send_artwork(self.probe_channel, data, timestamp)
                result.update(timestamp=timestamp, bytes=len(data), sha256=hashlib.sha256(data).hexdigest())
            elif mode == "cancel":
                if not role.cancel_scheduled_artwork(self.probe_channel):
                    raise web.HTTPConflict(text="probe requires native multipart cancel support")
            elif mode == "remove":
                roles = [value for value in client.active_role_ids if value != "artwork@v1"]
                client.deactivate_roles(roles)  # Native lifecycle enqueues cancel then stream/end.
            else:
                raise web.HTTPBadRequest(text="unknown artwork probe action")
            probe.release.set()
            await asyncio.wait_for(client.wait_role_drained("artwork"), 4)
            if mode == "remove":
                client.connection.send_priority_message(ServerActivateMessage(ServerActivatePayload(activities=[], active_roles=roles)))
                client.set_active_roles(roles)
            result["marker"] = await self.barrier(client)
            result["frames"] = list(probe.frames)
            return result
        finally:
            probe.close()
            self.artwork_probe = None

    async def artwork(self, client, command, timestamp):
        from PIL import Image

        source = ArtworkSource(command.get("source", "album"))
        role = client.role("artwork@v1")
        expected_channels = {
            channel for channel, config in role.get_channel_configs().items()
            if config.source is source
        }
        if not expected_channels:
            raise web.HTTPConflict(text="no active artwork channel for this source")
        image = None
        if not command.get("clear", False):
            width, height = (512, 256) if command.get("large", False) else (48, 24)
            pixels = random.Random(command.get("seed", 1)).randbytes(width * height * 3)
            image = Image.frombytes("RGB", (width, height), pixels)

        expected = {}
        transfers = {}
        queued = asyncio.Event()
        original_image = role.send_artwork
        original_binary = client.send_binary

        def observe_image(channel, data, image_timestamp):
            if image_timestamp == timestamp and channel in expected_channels:
                expected[channel] = {
                    "sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data),
                    "timestamp": image_timestamp, "parts": 0, "max_frame_bytes": 0,
                }
            original_image(channel, data, image_timestamp)

        def observe_binary(data, **kwargs):
            if kwargs.get("role_family") == "artwork" and len(data) >= 2:
                channel, flags = data[0] - 8, data[1]
                if flags == 2 and len(data) == 14:
                    image_timestamp, size = struct.unpack(">qI", data[2:])
                    transfers[channel] = [image_timestamp, size, 0]
                transfer = transfers.get(channel)
                if transfer is not None and transfer[0] == timestamp and channel in expected:
                    record = expected[channel]
                    record["max_frame_bytes"] = max(record["max_frame_bytes"], len(data))
                    if flags == 0:
                        transfer[2] += len(data) - 2
                        record["parts"] += 1
            original_binary(data, **kwargs)
            if expected_channels.issubset(expected) and all(
                channel in transfers and transfers[channel][0] == timestamp
                and transfers[channel][1] == transfers[channel][2]
                for channel in expected_channels
            ):
                queued.set()

        role.send_artwork = observe_image
        client.send_binary = observe_binary
        try:
            group = client.group.group_role("artwork")
            if source is ArtworkSource.ALBUM:
                await group.set_album_artwork(image, timestamp_us=timestamp)
            else:
                await group.set_artist_artwork(image, timestamp_us=timestamp)
            await asyncio.wait_for(queued.wait(), 4)
            marker = await self.barrier(client)
        finally:
            role.send_artwork = original_image
            client.send_binary = original_binary
        return {"images": expected, "timestamp": timestamp, "marker": marker}

    async def visualizer(self, client, command, timestamp):
        role = client.role("visualizer@v1")
        beat_only = command.get("beat", False)
        if beat_only:
            role.set_tracks_downbeats(tracks=True)
        role.on_stream_start()
        # The first stream/start may initialize native analysis libraries. Take
        # the future timestamp after initialization, not before slow startup.
        timestamp = self.timestamp(command)
        observed = []
        original_binary = client.send_binary

        def observe_binary(data, **kwargs):
            if kwargs.get("role_family") == "visualizer":
                observed.append({"hex": data.hex(), "timestamp": kwargs["timestamp_us"]})
            original_binary(data, **kwargs)

        client.send_binary = observe_binary
        try:
            if beat_only:
                role.append_beats([BeatTiming(timestamp_us=timestamp, is_downbeat=True)])
                # Beat-only roles drain their public schedule on audio hooks;
                # these bytes are analysis input, not an audio player stream.
                role.on_audio_chunk(
                    AudioChunk(data=bytes(4800), timestamp_us=timestamp, duration_us=25000, byte_count=4800)
                )
            else:
                vectors = [
                    (BinaryMessageType.VISUALIZATION_LOUDNESS, struct.pack(">H", 12345)),
                    (BinaryMessageType.VISUALIZATION_F_PEAK, struct.pack(">HH", 997, 54321)),
                    (BinaryMessageType.VISUALIZATION_SPECTRUM, struct.pack(">HHHH", 0, 123, 45678, 65535)),
                    (BinaryMessageType.VISUALIZATION_PEAK, bytes((203,))),
                ]
                for kind, payload in vectors:
                    data = pack_visualizer_frame(kind, timestamp, payload)
                    client.send_binary(
                        data, role_family="visualizer", timestamp_us=timestamp,
                        message_type=kind.value, buffer_end_time_us=timestamp,
                        buffer_byte_count=len(data), duration_us=0,
                    )
            marker = await self.barrier(client)
        finally:
            client.send_binary = original_binary
        return {"frames": observed, "timestamp": timestamp, "marker": marker}
