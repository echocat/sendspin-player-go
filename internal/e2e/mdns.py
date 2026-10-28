"""Independent python-zeroconf peer, used only in the isolated multicast suite."""

import asyncio
import socket

from zeroconf import IPVersion, ServiceStateChange
from zeroconf.asyncio import AsyncServiceBrowser, AsyncServiceInfo, AsyncZeroconf


class MDNSPeer:
    def __init__(self, server, config, client_id):
        self.server = server
        self.config = config
        self.client_id = client_id
        self.zeroconf = None
        self.browser = None
        self.advertisement = None
        self.tasks = set()
        self.clients = {}
        self.removed = []
        self.dialed = set()

    async def start(self, port):
        address = self.config["address"]
        ipv6 = self.config.get("family") == 6
        interfaces = [self.config["interface_index"]] if ipv6 else [address]
        self.zeroconf = AsyncZeroconf(interfaces=interfaces, ip_version=IPVersion.V6Only if ipv6 else IPVersion.V4Only)
        self.browser = AsyncServiceBrowser(
            self.zeroconf.zeroconf, "_sendspin._tcp.local.", handlers=[self.on_client]
        )
        if self.config["mode"] == "advertise_server":
            properties = self.config.get("properties", {"path": "/sendspin", "name": "Discovery hint"})
            self.advertisement = AsyncServiceInfo(
                "_sendspin-server._tcp.local.",
                f"{self.server.id}._sendspin-server._tcp.local.",
                server=f"reference-{self.server.id[:12]}.local.",
                addresses=[socket.inet_pton(socket.AF_INET6 if ipv6 else socket.AF_INET, address)], port=port, properties=properties,
                interface_index=self.config.get("interface_index"),
            )
            await self.zeroconf.async_register_service(self.advertisement)

    def on_client(self, zeroconf, service_type, name, state_change):
        instance = name.removesuffix("._sendspin._tcp.local.")
        if state_change is ServiceStateChange.Removed:
            self.clients.pop(instance, None)
            self.removed.append(instance)
            return
        task = asyncio.create_task(self.resolve(service_type, name, instance))
        self.tasks.add(task)
        task.add_done_callback(self.tasks.discard)

    async def resolve(self, service_type, name, instance):
        info = AsyncServiceInfo(service_type, name, interface_index=self.config.get("interface_index"))
        if not await info.async_request(self.zeroconf.zeroconf, 2000):
            return
        properties = {
            key.decode("utf-8"): value.decode("utf-8") if value is not None else ""
            for key, value in info.properties.items()
        }
        addresses = info.parsed_scoped_addresses()
        self.clients[instance] = {"port": info.port, "properties": properties, "addresses": addresses}
        if self.config["mode"] != "discover_clients" or instance != self.client_id:
            return
        path = properties.get("path")
        if path is None or not path.startswith("/") or not addresses:
            return
        host = f"[{addresses[0]}]" if ":" in addresses[0] else addresses[0]
        url = f"ws://{host}:{info.port}{path}"
        if url not in self.dialed:
            self.dialed.add(url)
            self.server.connect_to_client(url)

    def status(self):
        return {"clients": self.clients, "removed": self.removed}

    async def close(self):
        if self.browser is not None:
            await self.browser.async_cancel()
        for task in self.tasks:
            task.cancel()
        await asyncio.gather(*self.tasks, return_exceptions=True)
        if self.advertisement is not None:
            await self.zeroconf.async_unregister_service(self.advertisement)
        if self.zeroconf is not None:
            await self.zeroconf.async_close()
