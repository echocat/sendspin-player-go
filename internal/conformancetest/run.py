"""Local registration overlay; runs unchanged pinned orchestration/verdicts.

No upstream files are modified and no result is published. This is a separate
implementation entry, never an alias for upstream's unrelated sendspin-go.
"""

import argparse
import asyncio
import dataclasses
import importlib.metadata
import json
import os
import secrets
import socket
from pathlib import Path

import aiosendspin
import av
import numpy as np

from conformance import runner
from conformance.implementations import IMPLEMENTATIONS
from conformance.models import ImplementationSpec, RoleSpec

HARNESS_REVISION = "fa6dc45795f0810c5810af9f9b828595b7267ece"
SPEC_REVISION = "671a34d408581fc25ea56b3528a4a3f13e3be901"
IMPLEMENTATION = "sendspin-player-go"


def write_fixture(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    output = av.open(str(path), "w", format="flac")
    stream = output.add_stream("flac", rate=48000)
    stream.layout = "stereo"
    # Generated, non-silent 16-bit stereo. No external copyrighted fixture.
    values = np.array([i % 2001 - 1000 for i in range(48000)], dtype=np.int16)
    interleaved = np.repeat(values, 2).reshape(1, -1)
    frame = av.AudioFrame.from_ndarray(interleaved, format="s16", layout="stereo")
    frame.sample_rate = 48000
    for packet in stream.encode(frame):
        output.mux(packet)
    for packet in stream.encode(None):
        output.mux(packet)
    output.close()


def free_port():
    # The upstream CLI contract takes port numbers, not inherited socket FDs.
    # Select ephemeral ports instead of assuming its fixed default ports are free.
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


async def main(args):
    sdk = Path(__file__).resolve().parents[2]
    result_dir = sdk / "var" / "conformance" / f"run-{secrets.token_hex(6)}"
    result_dir.mkdir(parents=True)
    fixture_repo = result_dir / "generated-fixture"
    write_fixture(fixture_repo / "tests" / "fixtures" / "almost_silent.flac")
    os.environ["CONFORMANCE_REPO_AIOSENDSPIN"] = str(Path(aiosendspin.__file__).resolve().parent.parent)
    os.environ["CONFORMANCE_REPO_SENDSPIN_CLI"] = str(fixture_repo)
    os.environ["SENDSPIN_CONFORMANCE_CLIENT"] = str(Path(args.client).resolve())
    os.environ["PYTHONPATH"] = str(Path(__file__).resolve().parent) + os.pathsep + os.environ.get("PYTHONPATH", "")
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
    scenarios = args.scenarios.split(",") if args.scenarios else [
        "client-initiated-pcm", "server-initiated-pcm",
        "server-initiated-protocol-baseline-v1", "server-initiated-audio-chunk-framing",
        "server-initiated-pcm-24bit", "server-initiated-flac", "server-initiated-opus",
        "client-initiated-state-format-pcm", "client-initiated-state-format-flac",
        "client-initiated-metadata", "server-initiated-metadata",
        "client-initiated-controller", "server-initiated-controller",
        "client-initiated-artwork", "server-initiated-artwork",
    ]
    results = []
    children = []
    collect = runner.collect_process

    async def tracked_process(*args, **kwargs):
        process = await collect(*args, **kwargs)
        children.append(process)
        return process

    runner.collect_process = tracked_process  # Cleanup seam only; verdicts are unchanged.
    try:
        for scenario in scenarios:
            runner.SERVER_PORT_BASE = free_port()
            runner.CLIENT_PORT_BASE = free_port()
            print(f"[conformance] {scenario}", flush=True)
            result = await runner.run_case(
                results_dir=result_dir, scenario_id=scenario,
                server_impl="aiosendspin", client_impl=IMPLEMENTATION,
                timeout_s=30, slot_index=0,
            )
            payload = dataclasses.asdict(result)
            results.append(payload)
            print(f"[conformance] {result.status}: {result.reason}", flush=True)
    finally:
        for process in children:
            if process.returncode is None:
                try:
                    process.kill()
                except ProcessLookupError:
                    pass
        await asyncio.gather(*(process.wait() for process in children), return_exceptions=True)
        for process in children:
            await runner.close_process_log(process)
        runner.collect_process = collect
    manifest = {
        "implementation": IMPLEMENTATION, "harness_revision": HARNESS_REVISION,
        "sdk_specification": "1.0.0-rc1", "audited_spec_revision": SPEC_REVISION,
        "protocol_assertion_revision": "8c9577ea8719ad082d051ec13cc73ef15ed68948",
        "reference_package": importlib.metadata.version("aiosendspin"),
        "scope": "local loopback registry; not mDNS, hardware timing, or full certification",
        "results": results,
    }
    (result_dir / "sdk-run.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    if args.manifest:
        Path(args.manifest).write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"[conformance] artifacts: {result_dir}", flush=True)
    return 0 if all(result["status"] == "passed" for result in results) else 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--client", required=True)
    parser.add_argument("--scenarios", default="")
    parser.add_argument("--manifest", default="")
    raise SystemExit(asyncio.run(main(parser.parse_args())))
