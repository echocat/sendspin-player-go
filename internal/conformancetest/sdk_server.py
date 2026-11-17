"""Preload cold audio dependencies, then run the unchanged official adapter.

The pinned adapter anchors its clip only 250 ms in the future, before the
server's first conversion. Importing PyAV/NumPy inside that window can make the
server discard chunks as late on a busy CI runner (especially PCM24 conversion).
Keep import time outside the scenario; do not change timestamps or verdicts.
The launch binding in run.py separately budgets adapter readiness, including
cold imports, without extending scenario execution or fabricating a ready file.

For port zero, publish the actual bound port before the upstream adapter writes
its registry/readiness URL. Never probe/release a port before starting a listener.
"""

import argparse
import asyncio
import runpy

import av
import numpy


def run_dynamic_port():
    from aiosendspin.server.server import SendspinServer
    from conformance.adapters import aiosendspin_server as adapter

    args = adapter.build_parser().parse_args()
    start_server = SendspinServer.start_server

    async def bind(server, *positional, **keywords):
        if keywords.get("port") != 0 or keywords.get("host") != "127.0.0.1":
            raise RuntimeError("dynamic port launch requires an explicit loopback bind")
        await start_server(server, *positional, **keywords)
        # The pinned adapter formats args.port immediately after start_server.
        # Its aiohttp runner exposes the actual bound addresses; this private
        # server member is a version-pinned launch seam, not protocol handling.
        addresses = server._app_runner.addresses
        if len(addresses) != 1 or addresses[0][0] != "127.0.0.1" or not 0 < addresses[0][1] <= 65535:
            raise RuntimeError("expected one bound loopback server address")
        args.port = addresses[0][1]

    SendspinServer.start_server = bind
    try:
        # Exactly the coroutine invoked by upstream main(), with its original
        # parser, scenarios, summaries, cleanup and error propagation.
        return asyncio.run(adapter._run(args))
    finally:
        SendspinServer.start_server = start_server


if __name__ == "__main__":
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--port", type=int)
    launch, _ = parser.parse_known_args()
    if launch.port == 0:
        raise SystemExit(run_dynamic_port())
    runpy.run_module("conformance.adapters.aiosendspin_server", run_name="__main__")
