#!/usr/bin/env bash
#MISE description="Sustained virtual-output resource soak; three minutes per cipher suite by default"
#MISE depends=["prepare"]
set -euo pipefail
cd "${MISE_PROJECT_ROOT:?Run this file task through mise}"
mkdir -p var/soak
log=$(mktemp var/soak/events-XXXXXXXX.jsonl)
status=0
SENDSPIN_SOAK_DURATION="${SENDSPIN_SOAK_DURATION:-3m}" go test -json -race -tags=e2e -run '^TestReferenceSustainedPlaybackResourceSoak$' -count=1 -timeout=25m . > "$log" 2>&1 || status=$?
printf 'Resource soak exit=%s; events=%s; per-suite receipts=var/soak/resource-*.json\n' "$status" "$log"
exit "$status"
