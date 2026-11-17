"""Preload cold audio dependencies, then run the unchanged official adapter.

The pinned adapter anchors its clip only 250 ms in the future, before the
server's first conversion. Importing PyAV/NumPy inside that window can make the
server discard chunks as late on a busy CI runner (especially PCM24 conversion).
Keep import time outside the scenario; do not change timestamps or verdicts.
"""

import runpy

import av
import numpy


if __name__ == "__main__":
    runpy.run_module("conformance.adapters.aiosendspin_server", run_name="__main__")
