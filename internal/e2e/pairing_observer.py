"""Opt-in wire observations and explicit hello/activation protocol faults.

The unmodified reference library still owns Noise, pairing and persistence.
Never retain/log PSKs, handshake data, or full pairing message bodies here.
"""

import asyncio
import json

from aiohttp import WSMsgType
from aiosendspin.noise.wire import EncryptedWebSocket


class PairingObserver:
    def __init__(self, events, initial_activation=None, pairing_activation=None, stale_messages=None, hold_followup=False, initial_hello=None, observe_controls=False, observe_core=False, repeat_initial_activation=False):
        self.events = events
        self.initial_activation = initial_activation
        self.repeat_initial_activation = repeat_initial_activation
        self.pairing_activation = pairing_activation
        self.stale_messages = stale_messages
        self.initial_hello = initial_hello
        self.observe_controls = observe_controls
        self.observe_core = observe_core
        self.replaced_activation = False
        self.followup_gate = asyncio.Event()
        if not hold_followup:
            self.followup_gate.set()
        self._receive = EncryptedWebSocket.receive
        self._receive_timed = EncryptedWebSocket.receive_timed
        self._send_str = EncryptedWebSocket.send_str

        async def receive(transport):
            result = await self._receive(transport)
            self.observe(result)
            return result

        async def receive_timed(transport, clock):
            result, at = await self._receive_timed(transport, clock)
            self.observe(result)
            return result, at

        async def send_str(transport, data):
            if self.initial_hello is not None and '"server/hello"' in data:
                message = json.loads(data)
                if message.get("type") == "server/hello":
                    data = json.dumps({"type": "server/hello", "payload": self.initial_hello})
                    self.initial_hello = None
            if '"server/activate"' in data:
                message = json.loads(data)
                if message.get("type") == "server/activate":
                    # Explicit malformed/unsupported-peer fixture, not normal
                    # reference behavior. Replacement is still encrypted by
                    # the real reference transport, never sent in cleartext.
                    replacement = self.initial_activation
                    if replacement is None and self.pairing_activation is not None:
                        if "pairing" in message["payload"]["activities"]:
                            replacement = self.pairing_activation
                            self.pairing_activation = None
                    if replacement is not None:
                        data = json.dumps({"type": "server/activate", "payload": replacement})
                        # A retry in an admission-lock fixture must not become
                        # a valid contender after its one injected refusal.
                        # Other protocol-fault scenarios remain one-shot.
                        if not self.repeat_initial_activation:
                            self.initial_activation = None
                        self.replaced_activation = True
                    elif self.replaced_activation:
                        # Exercise the client's discard window immediately
                        # before the native cancelling activation closes it.
                        # These fault messages use the same serialized writer
                        # and real Noise transport, with no compliance bypass.
                        for stale in self.stale_messages or ():
                            await self._send_str(transport, json.dumps(stale))
                        self.stale_messages = None
                        await self.followup_gate.wait()
            await self._send_str(transport, data)

        EncryptedWebSocket.receive = receive
        EncryptedWebSocket.receive_timed = receive_timed
        EncryptedWebSocket.send_str = send_str

    def observe(self, result):
        if result.type is not WSMsgType.TEXT:
            return
        message = json.loads(result.data)
        if not isinstance(message, dict) or not isinstance(message.get("payload"), dict):
            return
        payload = message["payload"]
        kind = message.get("type")
        if self.observe_core and kind == "client/hello":
            # Identifiers/counters/policy boolean only; never copy secrets.
            methods = payload.get("supported_pair_methods")
            roles = payload.get("supported_roles")
            access = payload.get("unpaired_access")
            enabled = access.get("enabled") if isinstance(access, dict) else None
            known_methods = ("pairing_psk", "static_pairing_code", "dynamic_pairing_code")
            known_roles = ("player@v1", "controller@v1", "metadata@v1", "color@v1", "artwork@v1", "visualizer@v1", "source@v1")
            self.events.append({
                "type": "core_hello",
                "methods": sorted(k if k in known_methods else "unknown" for k in methods) if isinstance(methods, dict) else None,
                "roles": [r if r in known_roles else "unknown" for r in roles] if isinstance(roles, list) else None,
                "psk_descriptor_empty": isinstance(methods, dict) and methods.get("pairing_psk") == {},
                "unpaired_access_enabled": enabled if type(enabled) is bool else None,
            })
        elif self.observe_core and kind == "client/time":
            value = payload.get("client_transmitted")
            self.events.append({
                "type": "clock_request",
                "client_transmitted": str(value) if type(value) is int and -(1 << 63) <= value < (1 << 63) else None,
                "unexpected_keys": len(set(payload) - {"client_transmitted"}) + len(set(message) - {"type", "payload"}),
            })
        elif self.observe_core and kind == "client/leave":
            self.events.append({"type": "leave_request", "unexpected_keys": len(payload) + len(set(message) - {"type", "payload"})})
        elif kind == "client/pair-init":
            index = payload.get("pairing_index")
            if type(index) is not int or not 0 <= index <= 0xFFFFFFFF:
                index = None
            self.events.append({"type": "pair_init", "pairing_index": index})
        elif kind == "client/pair-finalize":
            self.events.append({"type": "pair_finalize"})
        elif kind == "pair/abort":
            reason = payload.get("reason")
            if reason not in ("attempt_timeout", "concurrent_attempt", "method_not_supported", "pairing_code_mismatch", "user_cancelled"):
                reason = "invalid"
            self.events.append({"type": "pair_abort", "reason": reason})
        elif self.observe_controls and kind == "client/command" and isinstance(payload.get("controller"), dict):
            controller = payload["controller"]
            allowed = ("play", "pause", "stop", "next", "previous", "volume", "mute", "repeat_off", "repeat_one", "repeat_all", "shuffle", "unshuffle", "switch", "seek", "seek_relative")
            fields = {}
            if "command" in controller:
                value = controller["command"]
                fields["command"] = value if value in allowed else "invalid"
            for key in ("volume", "position_ms", "offset_ms"):
                if key in controller:
                    value = controller[key]
                    # Decimal metadata, not a changed wire type: preserve exact
                    # signed-64-bit arguments through the JSON control API.
                    fields[key] = str(value) if type(value) is int and -(1 << 63) <= value < (1 << 63) else None
            if "mute" in controller:
                value = controller["mute"]
                fields["mute"] = value if type(value) is bool else None
            unexpected = len(set(controller) - {"command", "volume", "mute", "position_ms", "offset_ms"}) + len(set(payload) - {"controller"})
            self.events.append({"type": "controller_command", "fields": fields, "unexpected_keys": unexpected})
        elif self.observe_controls and kind == "client/state" and isinstance(payload.get("player"), dict):
            player = payload["player"]
            fields = {}
            for key in ("volume", "output_delay_ms", "required_lead_time_ms", "min_buffer_ms"):
                if key in player:
                    value = player[key]
                    fields[key] = value if type(value) is int and -(1 << 63) <= value < (1 << 63) else None
            if "muted" in player:
                value = player["muted"]
                fields["muted"] = value if type(value) is bool else None
            if "supported_commands" in player:
                commands = player["supported_commands"]
                fields["supported_commands"] = commands if isinstance(commands, list) and len(commands) <= 256 and all(value in ("volume", "mute", "set_output_delay") for value in commands) else None
            available = payload.get("available")
            self.events.append({"type": "player_state", "available": available if type(available) is bool else None, "fields": fields})

    def close(self):
        self.followup_gate.set()
        EncryptedWebSocket.receive = self._receive
        EncryptedWebSocket.receive_timed = self._receive_timed
        EncryptedWebSocket.send_str = self._send_str
