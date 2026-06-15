#!/usr/bin/env sh
set -eu

NAMESPACE="${NAMESPACE:-free5gc-cwm}"
PACKETRUSHER_SELECTOR="${PACKETRUSHER_SELECTOR:-app=packetrusher}"
SERVER_IP="${SERVER_IP:-10.200.6.10}"
UE_IP="${UE_IP:-10.63.0.1}"
VRF="${VRF:-vrf0000000003}"
PORT="${PORT:-5201}"
DURATION="${DURATION:-10}"
PARALLEL="${PARALLEL:-1}"

pod="$(kubectl -n "$NAMESPACE" get pod -l "$PACKETRUSHER_SELECTOR" \
  --field-selector=status.phase=Running \
  -o jsonpath='{.items[0].metadata.name}')"

kubectl -n "$NAMESPACE" exec "$pod" -- sh -c \
  "ip vrf exec '$VRF' iperf3 -c '$SERVER_IP' -B '$UE_IP' -p '$PORT' -t '$DURATION' -P '$PARALLEL'"
