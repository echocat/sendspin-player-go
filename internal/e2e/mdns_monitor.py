"""Read-only independent mDNS observer in its own isolated network namespace."""

import asyncio
import socket

from aiohttp import web

from mdns import MDNSPeer


async def main():
    interfaces = [(index, name) for index, name in socket.if_nameindex() if name != "lo"]
    if len(interfaces) != 1:
        raise RuntimeError("observer requires exactly one private interface")
    index, _ = interfaces[0]
    peer = MDNSPeer(None, {"mode": "observe_clients", "family": 6, "interface_index": index, "address": "::"}, "unused")
    await peer.start(0)
    app = web.Application()

    async def status(_request):
        return web.json_response(peer.status())

    app.router.add_get("/status", status)
    runner = web.AppRunner(app)
    await runner.setup()
    await web.TCPSite(runner, "0.0.0.0", 8081).start()
    try:
        await asyncio.Event().wait()
    finally:
        await peer.close()
        await runner.cleanup()


if __name__ == "__main__":
    asyncio.run(main())
