"""Weak-producer regression tests: no sockets or audio hardware."""

import asyncio
from pathlib import Path
import struct
import tempfile
import unittest
import wave

from weak_playback import WeakPlayback


class FakeStream:
    def __init__(self):
        self.frames = 0
        self.stopped = False

    def prepare_audio(self, pcm, audio_format):
        assert audio_format.sample_rate == 48000 and audio_format.channels == 2
        self.frames += len(pcm) // 4

    async def commit_audio(self):
        pass

    def stop(self):
        self.stopped = True


class FakeClient:
    is_connected = True

    def __init__(self):
        self.group = self
        self.stream = FakeStream()

    def start_stream(self):
        return self.stream

    async def wait_role_drained(self, role):
        assert role == "player"


class WeakPlaybackTest(unittest.IsolatedAsyncioTestCase):
    def reference(self, root, left=320, right=0):
        path = Path(root) / "synthetic-only.wav"
        pcm = bytearray(144000 * 4)
        struct.pack_into("<hh", pcm, 48000 * 4, left, right)
        with wave.open(str(path), "wb") as w:
            w.setnchannels(2)
            w.setsampwidth(2)
            w.setframerate(48000)
            w.writeframes(pcm)
        return path

    async def test_bounded_start_stop_and_shutdown_join_the_producer(self):
        with tempfile.TemporaryDirectory(dir="/tmp/opencode" if Path("/tmp/opencode").is_dir() else None) as root:
            producer = WeakPlayback(self.reference(root))
            client = FakeClient()
            await producer.start(client)
            await asyncio.sleep(.01)
            result = await producer.stop(client)
            self.assertGreaterEqual(result["source_frames"], 4800)
            self.assertIsNone(producer.task)
            self.assertTrue(client.stream.stopped)
            await producer.start(FakeClient())
            await asyncio.sleep(.01)
            await producer.close()
            self.assertIsNone(producer.task)

    async def test_source_rejects_extra_level_or_right_channel_signal(self):
        with tempfile.TemporaryDirectory(dir="/tmp/opencode" if Path("/tmp/opencode").is_dir() else None) as root:
            for left, right in [(329, 0), (-329, 0), (320, 1)]:
                with self.subTest(left=left, right=right):
                    producer = WeakPlayback(self.reference(root, left, right))
                    with self.assertRaises(ValueError):
                        producer.source()
                    self.assertIsNone(producer.task)


if __name__ == "__main__":
    unittest.main(verbosity=2)
