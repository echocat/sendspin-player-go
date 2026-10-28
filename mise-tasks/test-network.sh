#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p var/network-load
log=$(mktemp var/network-load/campaign-XXXXXXXX.jsonl)
status=0
go test -json -race -tags=e2e -run '^TestReferenceImpairedTransportRecoveryAndCancellation$' -count=3 -timeout=6m . > "$log" 2>&1 || status=$?
printf 'Transport campaign exit=%s; events=%s\n' "$status" "$log"
exit "$status"
