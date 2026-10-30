#!/usr/bin/env bash
#MISE description="Repeat isolated userspace transport/load scenarios without hardware or host network changes"
#MISE depends=["prepare"]
set -euo pipefail
cd "${MISE_PROJECT_ROOT:?Run this file task through mise}"
mkdir -p var/network-load
log=$(mktemp var/network-load/campaign-XXXXXXXX.jsonl)
status=0
go test -json -race -tags=e2e -run '^TestReferenceImpairedTransportRecoveryAndCancellation$' -count=3 -timeout=6m . > "$log" 2>&1 || status=$?
printf 'Transport campaign exit=%s; events=%s\n' "$status" "$log"
exit "$status"
