#!/usr/bin/env bash
#MISE description="LIVE PLAY! 3 SDK loopback: cable already connected, five-second capture, no prompts, leaves output muted"
#MISE depends=["prepare"]
set -euo pipefail
exec bash "${MISE_PROJECT_ROOT:?Run this file task through mise}/scripts/audio/play3-low-level-loopback.sh" \
    --acknowledge-unverified-wiring --capture-gain-24db --sdk-output --connected-leave-muted
