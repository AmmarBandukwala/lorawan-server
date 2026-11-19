#!/usr/bin/env bash
# End-to-end smoke test of the two binaries against each other over real UDP:
# the ns network server runs for real, and nsbench plays gateway and device.
#
# Nothing is mocked. The join, the RX-window scheduling, the status endpoint and
# the SIGTERM shutdown are all exercised, which is what makes this worth running
# before touching radio hardware.
#
#   make smoke          # runs this inside the Go container
set -u

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

echo "==> building the binaries"
go build -o /tmp/ns ./cmd/ns || fail "cannot build cmd/ns"
go build -o /tmp/nsbench ./cmd/nsbench || fail "cannot build cmd/nsbench"

UDP_PORT=16858
STATUS_PORT=18090
API_PORT=18091
API_TOKEN=smoke-test-token-0123456789
SEED="0011223344556677:0102030405060708:2b7e151628aed2a6abf7158809cf4f3c"

echo "==> starting ns (in-memory store, one seeded device)"
NS_LISTEN_ADDR=127.0.0.1:${UDP_PORT} \
NS_STATUS_ADDR=127.0.0.1:${STATUS_PORT} \
NS_API_ADDR=127.0.0.1:${API_PORT} \
NS_API_TOKEN=${API_TOKEN} \
NS_REGION=EU868 \
NS_NET_ID=000000 \
NS_LOG_LEVEL=debug \
NS_SEED_DEVICES=${SEED} \
/tmp/ns > /tmp/ns.log 2>&1 &
NSPID=$!

# The probe is the same one the container health check runs.
cleanup() {
  if kill -0 "$NSPID" 2>/dev/null; then
    kill -TERM "$NSPID" 2>/dev/null
    wait "$NSPID" 2>/dev/null
  fi
}
trap 'cleanup; echo "--- ns log ---"; cat /tmp/ns.log' EXIT

fetch() {
  exec 3<>/dev/tcp/127.0.0.1/"$2" || return 1
  printf 'GET %s HTTP/1.0\r\n\r\n' "$1" >&3
  cat <&3
}

# request sends one HTTP request with a bearer token over a raw TCP socket. The
# Go image has no curl and the point of this script is to depend on nothing, so
# the request is written by hand. Content-Length is computed rather than hard
# coded, because a wrong length makes the server wait for a body that never
# comes and the failure looks like a hang rather than a bug.
request() {
  local method=$1 path=$2 port=$3 token=$4 body=$5
  exec 3<>/dev/tcp/127.0.0.1/"$port" || return 1
  if [ -n "$body" ]; then
    printf '%s %s HTTP/1.0\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s' \
      "$method" "$path" "$token" "${#body}" "$body" >&3
  else
    printf '%s %s HTTP/1.0\r\nAuthorization: Bearer %s\r\n\r\n' "$method" "$path" "$token" >&3
  fi
  cat <&3
}

echo "==> waiting for the status endpoint"
for _ in $(seq 1 30); do
  if fetch /healthz ${STATUS_PORT} > /tmp/healthz.out 2>/dev/null; then break; fi
  sleep 0.5
done
grep -q "200 OK" /tmp/healthz.out || fail "/healthz did not answer 200: $(cat /tmp/healthz.out)"
echo "    /healthz answered 200"

echo "==> running nsbench (simulated gateway and class-A device)"
/tmp/nsbench -addr 127.0.0.1:${UDP_PORT} -net-id 000000 || fail "nsbench reported a failure"

echo "==> checking /status"
fetch /status ${STATUS_PORT} > /tmp/status.out || fail "/status did not answer"
grep -q '"gateways_online":1' /tmp/status.out || fail "the gateway is not online in /status: $(cat /tmp/status.out)"
grep -q '"downlink_errors":0' /tmp/status.out || fail "the gateway recorded downlink errors: $(cat /tmp/status.out)"
echo "    /status reports the gateway online with no downlink errors"

echo "==> checking the health probe binary"
NS_STATUS_ADDR=127.0.0.1:${STATUS_PORT} /tmp/ns -healthz || fail "/ns -healthz failed against a live server"

# The device nsbench joined now has a real session, so the application API can be
# exercised against live engine state rather than a fixture.
DEVI=0011223344556677
DL_PATH="/api/v1/devices/${DEVI}/downlink"

echo "==> checking the API refuses an unauthenticated request"
request GET "/api/v1/devices/${DEVI}/session" ${API_PORT} "" "" > /tmp/api_noauth.out \
  || fail "the API is not listening on ${API_PORT}"
grep -q "401" /tmp/api_noauth.out || fail "an unauthenticated request was not refused: $(cat /tmp/api_noauth.out)"
echo "    unauthenticated request got 401"

echo "==> reading the session over the API"
request GET "/api/v1/devices/${DEVI}/session" ${API_PORT} "${API_TOKEN}" "" > /tmp/api_session.out \
  || fail "the session endpoint did not answer"
grep -q "200 OK" /tmp/api_session.out || fail "GET session: $(cat /tmp/api_session.out)"
grep -q '"f_cnt_up"' /tmp/api_session.out || fail "the session view has no counters: $(cat /tmp/api_session.out)"
# A key on this endpoint would be a leak, not a typo, so it is checked explicitly.
grep -q "2b7e151628aed2a6abf7158809cf4f3c" /tmp/api_session.out \
  && fail "the session endpoint exposed the application key"
echo "    session endpoint returned counters and no keys"

echo "==> queueing a downlink over the API"
request POST "$DL_PATH" ${API_PORT} "${API_TOKEN}" '{"f_port":10,"data":"deadbeef"}' > /tmp/api_dl.out \
  || fail "the downlink endpoint did not answer"
grep -q "202" /tmp/api_dl.out || fail "POST downlink: $(cat /tmp/api_dl.out)"
echo "    downlink accepted (202)"

echo "==> a second frame conflicts with the one in the queue"
request POST "$DL_PATH" ${API_PORT} "${API_TOKEN}" '{"f_port":10,"data":"ee"}' > /tmp/api_dl2.out \
  || fail "the downlink endpoint did not answer"
grep -q "409" /tmp/api_dl2.out || fail "a second queued frame was not refused: $(cat /tmp/api_dl2.out)"
echo "    second frame got 409"

echo "==> cancelling the queued frame"
request DELETE "$DL_PATH" ${API_PORT} "${API_TOKEN}" "" > /tmp/api_cancel.out \
  || fail "the cancel endpoint did not answer"
grep -q "200 OK" /tmp/api_cancel.out || fail "DELETE downlink: $(cat /tmp/api_cancel.out)"
echo "    queued frame cancelled"

echo "==> an unknown device is a 404, not a 500"
request POST "/api/v1/devices/9999999999999999/downlink" ${API_PORT} "${API_TOKEN}" \
  '{"f_port":10,"data":"ff"}' > /tmp/api_404.out || fail "the API did not answer"
grep -q "404" /tmp/api_404.out || fail "an unknown device gave: $(cat /tmp/api_404.out)"
echo "    unknown device got 404"

echo "==> sending SIGTERM"
kill -TERM "$NSPID"
wait "$NSPID"
RC=$?
[ "$RC" -eq 0 ] || fail "ns exited with $RC after SIGTERM, want a clean 0"
grep -q "shutting down" /tmp/ns.log || fail "ns did not log a graceful shutdown"

echo
echo "SMOKE PASS: joined a device over UDP, acknowledged a confirmed uplink,"
echo "            served /healthz and /status, queued and cancelled a downlink"
echo "            over the authenticated API, and shut down cleanly on SIGTERM"
