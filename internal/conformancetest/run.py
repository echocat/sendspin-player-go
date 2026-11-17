"""JSON-lines binding to unchanged, pinned official harness calls.

Go owns the fixture, ports, scenario inventory, SDK build, process and cleanup.
This module registers launch shims, budgets adapter readiness separately from
scenario execution, and forwards unchanged native verdicts.
"""

import aiosendspin
import argparse
import asyncio
import contextlib
import dataclasses
import json
import os
import sys
from conformance import runner
from conformance.implementations import IMPLEMENTATIONS
from conformance.models import ImplementationSpec, RoleSpec
from conformance.process import wait_for_file
from pathlib import Path

IMPLEMENTATION = "sendspin-player-go"
ADAPTER_READY_TIMEOUT_SECONDS = 60


async def wait_for_adapter_file(path, timeout_s):
    # The pinned runner hardcodes ten seconds for cold Python adapter startup,
    # independently of run_case(timeout_s). DLL imports/AV scanning on Windows
    # can exceed that before any protocol or server logging has started. Only
    # genuine adapter readiness files get this bounded startup budget; other
    # waits, process execution deadlines and all protocol verdicts are untouched.
    if path.name in ("server-ready.json", "client-ready.json"):
        timeout_s = max(timeout_s, ADAPTER_READY_TIMEOUT_SECONDS)
    await wait_for_file(path, timeout_s=timeout_s)


def register(args):
    runner.wait_for_file = wait_for_adapter_file
    os.environ["CONFORMANCE_REPO_AIOSENDSPIN"] = str(Path(aiosendspin.__file__).resolve().parent.parent)
    os.environ["CONFORMANCE_REPO_SENDSPIN_CLI"] = args.fixture_repo
    os.environ["SENDSPIN_CONFORMANCE_CLIENT"] = args.client
    os.environ["PYTHONPATH"] = str(Path(__file__).resolve().parent) + os.pathsep + os.environ.get("PYTHONPATH", "")
    server = IMPLEMENTATIONS["aiosendspin"]
    IMPLEMENTATIONS["aiosendspin"] = dataclasses.replace(
        server, server=dataclasses.replace(server.server, entrypoint="sdk_server"),
    )
    IMPLEMENTATIONS[IMPLEMENTATION] = ImplementationSpec(
        name=IMPLEMENTATION,
        display_name="sendspin-player-go SDK",
        repo_dirname=IMPLEMENTATION,
        remote_url="https://github.com/echocat/sendspin-player-go.git",
        client=RoleSpec(
            supported=True, adapter_kind="python", entrypoint="sdk_client",
            supports_server_initiated=True, supports_client_initiated=True,
            supports_flac=True, supports_opus=True, supports_discovery=False,
            supported_role_families=("player", "metadata", "controller", "artwork"),
        ),
        server=RoleSpec(
            supported=False, adapter_kind="placeholder",
            entrypoint="conformance.adapters.placeholder", reason="This SDK implements clients only.",
        ),
    )


async def serve(args):
    register(args)
    for line in sys.stdin:
        request = json.loads(line)
        # Public runner configuration, not a modified runner or verdict.
        runner.SERVER_PORT_BASE = request["server_port"]
        runner.CLIENT_PORT_BASE = request["client_port"]
        with contextlib.redirect_stdout(sys.stderr):
            result = await runner.run_case(
                results_dir=Path(request["results_dir"]),
                scenario_id=request["scenario_id"],
                server_impl="aiosendspin", client_impl=IMPLEMENTATION,
                timeout_s=30, slot_index=0,
            )
        print(json.dumps(dataclasses.asdict(result)), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client", required=True)
    parser.add_argument("--fixture-repo", required=True)
    asyncio.run(serve(parser.parse_args()))
