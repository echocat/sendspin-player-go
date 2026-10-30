#!/usr/bin/env bash
#MISE description="LIVE PLAY! 3 interactive direct-cable experiment; explicit wiring/capture flags required"
#MISE depends=["prepare"]
#MISE raw=true
set -euo pipefail
exec bash "${MISE_PROJECT_ROOT:?Run this file task through mise}/scripts/audio/play3-low-level-loopback.sh" "$@"
