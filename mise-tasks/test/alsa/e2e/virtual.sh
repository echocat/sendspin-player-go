#!/usr/bin/env bash
#MISE description="SOFTWARE ONLY: independent-server scenario with virtual output; no audio devices, mixers or capture"
#MISE depends=["prepare"]
# Explicit software-only independent-server validation, never an audio fallback.
set -euo pipefail
umask 077
root="${MISE_PROJECT_ROOT:?Run this file task through mise}"
mkdir -p "$root/var/platform-validation/alsa-e2e-virtual"
work="$(mktemp -d "$root/var/platform-validation/alsa-e2e-virtual/run-XXXXXXXX")"
trap 'printf "Software-only E2E artifacts: %s\n" "$work"' EXIT
UV_PROJECT_ENVIRONMENT="$root/var/python/e2e" PYTHONPYCACHEPREFIX="$root/var/python/pycache" \
    uv sync --locked --project "$root/internal/e2e" > "$work/python-setup.log" 2>&1
CGO_ENABLED=0 go -C "$root" run ./internal/loopbackcmd generate -out "$work/reference.wav" -seconds 3 -amplitude 0.01 > "$work/generate.log" 2>&1
CGO_ENABLED=0 SENDSPIN_ALSA_E2E_PLAYBACK='' SENDSPIN_ALSA_E2E_VIRTUAL=1 \
    SENDSPIN_ALSA_E2E_ROOT="$root" SENDSPIN_ALSA_E2E_ARTIFACT_DIR="$work" SENDSPIN_ALSA_REFERENCE_FILE="$work/reference.wav" \
    go -C "$root/audio/alsa" test -tags=e2e -count=1 -v -run '^TestReferenceServerPlaybackScenario$' -timeout=90s .
