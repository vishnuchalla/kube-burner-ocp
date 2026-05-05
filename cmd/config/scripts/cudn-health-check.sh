#!/bin/bash
set -u

STABLE_WAIT="3m"

# Move pprof data if it exists
if [ -d "pprof-data" ]; then
    mv pprof-data "pprof-data-$(date +%Y%m%d-%H%M%S)"
else
    log "WARN: pprof-data directory not found."
fi

# Cluster Stability Check
log "Waiting for cluster stability (Minimum: $STABLE_WAIT)..."
oc adm wait-for-stable-cluster --minimum-stable-period="$STABLE_WAIT"

if [ $? -ne 0 ]; then
    log "CRITICAL: Cluster failed to stabilize. Stopping test."
    break
fi
