#!/usr/bin/env bash
#MISE description="Validate real MISE file-task discovery and safe shell orchestration with fake hardware"
#MISE depends=["prepare"]
set -euo pipefail
cd "${MISE_PROJECT_ROOT:?Run this file task through mise}"
export PYTHONPYCACHEPREFIX="$MISE_PROJECT_ROOT/var/python/pycache"
python3 -m unittest discover -s scripts/tests -p 'test_*.py' -v
