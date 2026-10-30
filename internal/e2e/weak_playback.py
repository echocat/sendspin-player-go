"""Bounded weak-reference producer using the unmodified server's public API."""

import asyncio
import struct
import wave
from aiohttp import web
from aiosendspin.server.audio import AudioFormat
from pathlib import Path


class WeakPlayback:
    def __init__(self, path):
        self.path = path
        self.task = None
        self.stop_event = None
        self.stream = None
        self.frames = 0

    def source(self):
        path = Path(self.path)
        if not path.is_file() or path.stat().st_size != 576044:
            raise ValueError("weak reference must be the three-second generated PCM16 file")
        with wave.open(str(path), "rb") as source:
            if (source.getnchannels(), source.getsampwidth(), source.getframerate(), source.getnframes()) != (2, 2, 48000, 144000):
                raise ValueError("invalid weak reference format")
            pcm = source.readframes(144000)
        if len(pcm) != 576000:
            raise ValueError("truncated weak reference")
        for left, right in struct.iter_unpack("<hh", pcm):
            if abs(left) > 328 or right != 0:
                raise ValueError("weak reference exceeds approved level/channel profile")
        return pcm

    async def start(self, client):
        if client is None or not client.is_connected or self.task is not None:
            raise web.HTTPConflict(text="weak producer requires a connected client and no active producer")
        pcm = self.source()
        self.stream = client.group.start_stream()
        self.stop_event = asyncio.Event()

        async def produce():
            at = 0
            tick = asyncio.get_running_loop().time()
            while not self.stop_event.is_set():
                chunk = pcm[at:at + 4800 * 4]
                self.stream.prepare_audio(chunk, AudioFormat(sample_rate=48000, channels=2, bit_depth=16))
                await self.stream.commit_audio()
                self.frames += 4800
                at = (at + len(chunk)) % len(pcm)
                tick += .1
                await asyncio.sleep(max(0, tick - asyncio.get_running_loop().time()))

        self.task = asyncio.create_task(produce())
        return {"source_frames": self.frames}

    async def stop(self, client):
        if self.task is None:
            raise web.HTTPConflict(text="weak producer is not running")
        self.stop_event.set()
        try:
            await asyncio.wait_for(self.task, 4)
            await asyncio.wait_for(client.wait_role_drained("player"), 4)
        finally:
            self.task = None
            self.stream.stop()
        return {"source_frames": self.frames}

    async def close(self):
        if self.task is not None:
            self.task.cancel()
            await asyncio.gather(self.task, return_exceptions=True)
            self.task = None
        if self.stream is not None:
            self.stream.stop()
