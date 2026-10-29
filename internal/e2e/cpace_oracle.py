"""Deterministic TEST-ONLY differential corpus from the unmodified cpace oracle.

No socket, device, SDK implementation, runtime RNG override or production
credential is used. --write is explicit; the default checks frozen bytes.
"""

import argparse
import copy
import hashlib
import importlib.metadata
import json
from pathlib import Path

import cpace
from cryptography.hazmat.primitives.asymmetric.x25519 import (
    X25519PrivateKey,
    X25519PublicKey,
)

VERSION = "0.1.0"
MODULE_SHA256 = "ee207059c29923d1b0aad50ab3b252a475e44424e130849cef85b86f7755bbd9"
WHEEL_SHA256 = "9fabb60a711a85934225be4081b3f5994e1ca4cd1335c5b9171770c8f1a2fda3"
TARGET = Path(__file__).resolve().parents[1] / "cpace/testdata/python-differential.json"


def fixed(index, label, size):
    """Public fixture derivation, NEVER a production source of key material."""
    seed = b"sendspin-cpace-test-corpus-v1\x00" + index.to_bytes(4, "big") + label.encode("ascii")
    return hashlib.shake_256(seed).digest(size)


def side(prs, sid, ci, ad, peer_ad, scalar):
    return {"prs": prs, "sid": sid, "ci": ci, "ad": ad, "peer_ad": peer_ad, "scalar": scalar}


def evaluate(name, category, a, b, change_a_share=False, change_b_share=False):
    ga = cpace._calculate_generator(a["prs"], a["ci"], a["sid"])
    gb = cpace._calculate_generator(b["prs"], b["ci"], b["sid"])
    ka = X25519PrivateKey.from_private_bytes(a["scalar"])
    kb = X25519PrivateKey.from_private_bytes(b["scalar"])
    ya = ka.exchange(X25519PublicKey.from_public_bytes(ga))
    yb = kb.exchange(X25519PublicKey.from_public_bytes(gb))
    received_a, received_b = ya, yb
    if change_a_share:
        received_a = bytes([ya[0] ^ 1]) + ya[1:]
    if change_b_share:
        received_b = bytes([yb[0] ^ 1]) + yb[1:]
    initiator = cpace.CPace(role=cpace.CPaceRole.INITIATOR, sid=a["sid"], ad=a["ad"], scalar=a["scalar"], public_share=ya)
    responder = cpace.CPace(role=cpace.CPaceRole.RESPONDER, sid=b["sid"], ad=b["ad"], scalar=b["scalar"], public_share=yb)
    initiator.derive(received_b, a["peer_ad"])
    responder.derive(received_a, b["peer_ad"])
    server_tag, client_tag = initiator.tag(), responder.tag()
    return {
        "id": name, "category": category,
        "a": {k: v.hex() for k, v in a.items()},
        "b": {k: v.hex() for k, v in b.items()},
        "generator_a": ga.hex(), "generator_b": gb.hex(),
        "ya": ya.hex(), "yb": yb.hex(),
        "received_ya": received_a.hex(), "received_yb": received_b.hex(),
        "shared_a": ka.exchange(X25519PublicKey.from_public_bytes(received_b)).hex(),
        "shared_b": kb.exchange(X25519PublicKey.from_public_bytes(received_a)).hex(),
        "isk_a": initiator.isk.hex(), "isk_b": responder.isk.hex(),
        "server_tag": server_tag.hex(), "client_tag": client_tag.hex(),
        "server_accepted": responder.verify(server_tag),
        "client_accepted": initiator.verify(client_tag),
    }


def corpus():
    if importlib.metadata.version("cpace") != VERSION:
        raise RuntimeError("independent CPace version changed")
    if hashlib.sha256(Path(cpace.__file__).read_bytes()).hexdigest() != MODULE_SHA256:
        raise RuntimeError("independent CPace source fingerprint changed")
    cases = []
    profiles = []
    for index in range(64):
        length = 6 if index % 2 == 0 else 8
        prs = (str(int.from_bytes(fixed(index, "pin", 8), "big") % 10**length).zfill(length)).encode("ascii")
        if index < 2:
            prs = b"0" * length
        if index in (2, 3):
            prs = b"9" * length
        pairing_index = (1, 2, 127, 128, 65535, 0xFFFFFFFF)[index % 6]
        round_index = 1 if length == 8 else (1, 2, 19, 20)[index % 4]
        sid = b"sendspin-pair-pake-v1" + fixed(index, "noise-hash", 32) + pairing_index.to_bytes(4, "big") + round_index.to_bytes(4, "big")
        a = side(prs, sid, b"", b"server", b"client", fixed(index, "scalar-a", 32))
        b = side(prs, sid, b"", b"client", b"server", fixed(index, "scalar-b", 32))
        if index in (4, 5):
            # Equal public shares alone are not reflection when role AD differs.
            b["scalar"] = a["scalar"]
        profiles.append((a, b))
        cases.append(evaluate(f"sendspin-{index:03d}", "sendspin-profile", a, b))
    lengths = (0, 1, 6, 8, 24, 110, 116, 117, 118, 119, 120, 126, 127, 128, 129, 255)
    for i in range(64):
        index = i + 64
        prs = fixed(index, "binary-prs", lengths[i % len(lengths)])
        sid = fixed(index, "binary-sid", (0, 1, 60, 127, 128, 129)[i % 6])
        ci = fixed(index, "binary-ci", (0, 1, 127, 128, 255)[i % 5])
        ada = fixed(index, "associated-a", (0, 1, 127, 128)[i % 4])
        adb = fixed(index, "associated-b", (1, 0, 128, 129)[i % 4])
        scalar_a = fixed(index, "scalar-a", 32)
        scalar_b = fixed(index, "scalar-b", 32)
        if i < 3:
            scalar_a = (bytes(32), bytes([255]) * 32, bytes(range(32)))[i]
        a = side(prs, sid, ci, ada, adb, scalar_a)
        b = side(prs, sid, ci, adb, ada, scalar_b)
        cases.append(evaluate(f"boundary-{i:03d}", "draft-boundary", a, b))
    for index, original in enumerate(profiles[:6]):
        for field in ("prs", "sid", "ci", "ad", "peer_ad"):
            a, b = copy.deepcopy(original)
            value = b[field]
            b[field] = bytes([value[0] ^ 1]) + value[1:] if value else b"changed"
            cases.append(evaluate(f"mismatch-{index}-{field}", "mismatch", a, b))
        for changed in ("a-share", "b-share"):
            a, b = copy.deepcopy(original)
            cases.append(evaluate(f"mismatch-{index}-{changed}", "mismatch", a, b, changed == "a-share", changed == "b-share"))
    for i in range(2):
        value = side(b"123456", fixed(i, "reflection-sid", 60), b"", b"" if i == 0 else b"same", b"" if i == 0 else b"same", fixed(i, "reflection-scalar", 32))
        cases.append(evaluate(f"reflection-{i}", "reflection", value, value))
    for case in cases:
        expected = case["category"] in ("sendspin-profile", "draft-boundary")
        if case["server_accepted"] != expected or case["client_accepted"] != expected:
            raise RuntimeError("oracle case has unexpected authentication result: " + case["id"])
    return {
        "version": 1,
        "scope": "Public deterministic test scalars/PINs only; functional differential evidence, not a security certificate",
        "source": {"package": "cpace", "version": VERSION, "module_sha256": MODULE_SHA256, "wheel_sha256": WHEEL_SHA256, "draft": "draft-irtf-cfrg-cpace-21"},
        "cases": cases,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="Explicitly replace the frozen public TEST corpus")
    args = parser.parse_args()
    data = (json.dumps(corpus(), indent=2) + "\n").encode("ascii")
    if args.write:
        TARGET.write_bytes(data)
    elif TARGET.read_bytes() != data:
        raise RuntimeError("frozen CPace corpus differs from independent oracle; no files modified")
    print(f"Independent CPace oracle: {len(json.loads(data)['cases'])} cases; corpus SHA-256 {hashlib.sha256(data).hexdigest()}; no device/network/protocol activity")


if __name__ == "__main__":
    main()
