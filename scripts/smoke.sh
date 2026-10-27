#!/usr/bin/env bash
# Container-contract smoke test for one adapter image.
#
# Usage: smoke.sh <adapter>          e.g. smoke.sh tahu
#
# Checks the parts of docs/CONTAINER_CONTRACT.md that need no second adapter:
# the command surface, capabilities, baked fixtures, usage errors and exit
# codes, readiness, the event stream, calls and a clean stop on SIGTERM. Each
# role the image declares runs against the pinned broker BROKER (default
# mosquitto).
#
# Environment: REGISTRY, VERSION (image tag, default dev), BROKER, VERBOSE=1,
#              REPORT_DIR (keeps the capability document when set).

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib.sh
source "${SCRIPT_DIR}/lib.sh"

ADAPTER="${1:?usage: smoke.sh <adapter>}"
IMAGE="$(image_of "${ADAPTER}")"
require_tools
trap cleanup EXIT
network_up
log "${IMAGE} on ${BROKER}"

expect_exit() {  # expect_exit <code> <label> <docker run args...>
    local want="$1" label="$2"; shift 2
    docker run --rm --network "${NETWORK}" --label "$(run_label)" "$@" >/dev/null 2>&1
    local got=$?
    if [[ "${got}" == "${want}" ]]; then ok "${label}: exit ${got}"; else fail "${label}: exit ${got}, want ${want}"; fi
}

# ---- capabilities ----
caps=$(docker run --rm "${IMAGE}" print-capabilities 2>/dev/null)
if [[ -n "${REPORT_DIR:-}" ]]; then
    mkdir -p "${REPORT_DIR}"
    printf '%s\n' "${caps}" > "${REPORT_DIR}/capabilities-${ADAPTER}.json"
fi
if jq -e --arg a "${ADAPTER}" '
        .schemaVersion == "1.0" and .adapter == $a and .protocol == "sparkplug-b-3.0.0"
        and (.adapterVersion | type == "string") and (.upstream.name | type == "string")
        and (.upstream.version | type == "string") and (.upstream.license | type == "string")
        and (.upstream.independentEngine | type == "boolean")
        and (.roles.edge.supported | type == "boolean") and (.roles.host.supported | type == "boolean")
        and (.roles.edge.supported or .roles.host.supported)' <<<"${caps}" >/dev/null 2>&1
then ok "print-capabilities"; else fail "print-capabilities is not a valid capability document: ${caps}"; fi
if [[ $(wc -l <<<"${caps}") -eq 1 ]]; then ok "print-capabilities is one line"; else fail "print-capabilities printed more than one line"; fi
EDGE=$(jq -r '.roles.edge.supported' <<<"${caps}")
HOST=$(jq -r '.roles.host.supported' <<<"${caps}")

# ---- the baked fixtures are the repository's ----
for f in "${ROOT}"/fixtures/*/fixture.json; do
    name=$(basename "$(dirname "${f}")")
    if diff -q <(docker run --rm "${IMAGE}" print-fixture "${name}" 2>/dev/null) "${f}" >/dev/null
    then ok "print-fixture ${name}"; else fail "print-fixture ${name} differs from fixtures/${name}/fixture.json"; fi
done
if diff -q <(docker run --rm "${IMAGE}" print-fixture 2>/dev/null) "${ROOT}/fixtures/baseline/fixture.json" >/dev/null
then ok "print-fixture defaults to baseline"; else fail "print-fixture without a name is not baseline"; fi
expect_exit 1 "print-fixture of an unknown name" "${IMAGE}" print-fixture no-such-fixture
if docker run --rm "${IMAGE}" version 2>/dev/null | grep -q .; then ok "version"; else fail "version printed nothing"; fi

# ---- usage errors never touch the network ----
expect_exit 2 "unknown command" "${IMAGE}" frobnicate
expect_exit 2 "call of an unknown operation" "${IMAGE}" call frobnicate
expect_exit 2 "call with a payload that is not an object" "${IMAGE}" call rebirth --payload '[1]'
expect_exit 1 "call without a running role" "${IMAGE}" call rebirth
if [[ "${EDGE}" == true ]]; then
    expect_exit 2 "edge without --broker" "${IMAGE}" edge
    expect_exit 2 "edge with an unknown flag" "${IMAGE}" edge --broker tcp://nowhere:1883 --frobnicate 1
    expect_exit 2 "edge with a flag given twice" "${IMAGE}" edge --broker tcp://nowhere:1883 --broker tcp://nowhere:1883
    expect_exit 2 "edge with a broker that is not tcp://" "${IMAGE}" edge --broker http://nowhere
    expect_exit 2 "edge with an invalid group" "${IMAGE}" edge --broker tcp://nowhere:1883 --group a/b
    expect_exit 2 "edge with an invalid timeout" "${IMAGE}" edge --broker tcp://nowhere:1883 --connect-timeout-ms soon
    expect_exit 4 "edge with a fixture that does not exist" "${IMAGE}" edge --broker tcp://nowhere:1883 --fixture /no/such/fixture.json
    bad=$(mktemp -d)
    trap 'rm -rf "${bad}"; cleanup' EXIT
    chmod 755 "${bad}"
    jq '.node.metrics[1].alias = .node.metrics[0].alias' "${ROOT}/fixtures/baseline/fixture.json" > "${bad}/dup-alias.json"
    jq '.updates[0].node[0].name = "No/Such/Metric"' "${ROOT}/fixtures/baseline/fixture.json" > "${bad}/bad-update.json"
    printf '{"schemaVersion":' > "${bad}/truncated.json"
    chmod 644 "${bad}"/*.json
    for f in dup-alias bad-update truncated; do
        expect_exit 4 "edge with an invalid fixture (${f})" -v "${bad}:/bad:ro" "${IMAGE}" edge --broker tcp://nowhere:1883 --fixture "/bad/${f}.json"
    done
    expect_exit 3 "edge with a broker that cannot be reached" "${IMAGE}" edge --broker "tcp://no-such-host-${RUN_ID}:1883" --connect-timeout-ms 2000
else
    expect_exit 5 "edge on a host-only image" "${IMAGE}" edge --broker tcp://nowhere:1883
fi
if [[ "${HOST}" == true ]]; then
    expect_exit 2 "host without --host-id" "${IMAGE}" host --broker tcp://nowhere:1883
    expect_exit 2 "host with an invalid --host-id" "${IMAGE}" host --broker tcp://nowhere:1883 --host-id 'a#b'
    expect_exit 2 "host with an unknown flag" "${IMAGE}" host --broker tcp://nowhere:1883 --host-id h --fixture baseline
    expect_exit 3 "host with a broker that cannot be reached" "${IMAGE}" host --broker "tcp://no-such-host-${RUN_ID}:1883" --host-id h --connect-timeout-ms 2000
else
    expect_exit 5 "host on an edge-only image" "${IMAGE}" host --broker tcp://nowhere:1883 --host-id h
fi

# ---- the roles: readiness, events, calls, clean stop ----
broker="broker-${RUN_ID}"
if ! start_broker "${BROKER}" "${broker}"; then fail "broker ${BROKER} did not start"; finish; exit 1; fi
url=$(broker_url "${broker}")

stdout_is_json() {  # stdout_is_json <container> <label>
    if docker logs "$1" 2>/dev/null | grep -v '^{' | grep -q .; then fail "$2: stdout has lines that are not JSON"; else ok "$2: stdout is JSON Lines only"; fi
}

stops_cleanly() {  # stops_cleanly <container> <label>
    docker stop -t 10 "$1" >/dev/null
    local code last
    code=$(docker inspect --format '{{.State.ExitCode}}' "$1")
    last=$(docker logs "$1" 2>/dev/null | tail -1)
    if [[ "${code}" == 0 ]]; then ok "$2: SIGTERM exit 0"; else fail "$2: SIGTERM exit ${code}"; fi
    if jq -e '.event == "stopped" and (.time | type == "string")' <<<"${last}" >/dev/null 2>&1
    then ok "$2: stopped event"; else fail "$2: last stdout line is not the stopped event: ${last}"; fi
}

if [[ "${HOST}" == true ]]; then
    h="host-${ADAPTER}-${RUN_ID}"
    start_role "${ADAPTER}" "${h}" host --broker "${url}" --host-id smoke-host
    if wait_ready "${h}"; then ok "host becomes ready"; else fail "host did not become ready"; fi
    first=$(docker logs "${h}" 2>/dev/null | head -1)
    if jq -e --arg a "${ADAPTER}" '.event == "ready" and .adapter == $a and .hostId == "smoke-host" and (.time | type == "string")' <<<"${first}" >/dev/null 2>&1
    then ok "host ready event"; else fail "first host stdout line is not the ready event: ${first}"; fi
    doc=$(call "${h}" nodes); code=$?
    if [[ "${code}" == 0 ]] && jq -e --arg a "${ADAPTER}" '.schemaVersion == "1.0" and .adapter == $a and .operation == "nodes"
            and .ok and .error == null and (.result.nodes | type == "array") and (.elapsedMs | type == "number")' <<<"${doc}" >/dev/null 2>&1
    then ok "host call nodes"; else
        if jq -e '.roles.host.calls | index("nodes")' <<<"${caps}" >/dev/null; then fail "host call nodes: exit ${code}, ${doc}"; else skip "host call nodes"; fi
    fi
    doc=$(call "${h}" publish-data '{"update": 0}'); code=$?
    if [[ "${code}" == 5 ]] && jq -e '.ok == false and .error.code == "unsupported"' <<<"${doc}" >/dev/null 2>&1
    then ok "host call of an edge operation is unsupported"; else fail "host call publish-data: exit ${code}, ${doc}"; fi
    doc=$(call "${h}" send-command '{"group": "g"}'); code=$?
    if [[ "${code}" == 1 ]] && jq -e '.ok == false and .error.code == "invalid-request"' <<<"${doc}" >/dev/null 2>&1
    then ok "host call with an invalid payload"; else fail "host call send-command without edgeNode: exit ${code}, ${doc}"; fi
fi

if [[ "${EDGE}" == true ]]; then
    e="edge-${ADAPTER}-${RUN_ID}"
    start_role "${ADAPTER}" "${e}" edge --broker "${url}" --edge-node "smoke-${ADAPTER}"
    if wait_ready "${e}"; then ok "edge becomes ready"; else fail "edge did not become ready"; fi
    ready=$(events "${e}" 'select(.event == "ready")' | head -1)
    if jq -e --arg a "${ADAPTER}" --arg n "smoke-${ADAPTER}" '.adapter == $a and .fixture == "baseline" and .group == "interop"
            and .edgeNode == $n and (.bdSeq | type == "number")' <<<"${ready}" >/dev/null 2>&1
    then ok "edge ready event"; else fail "edge ready event: ${ready}"; fi
    pub=$(events "${e}" 'select(.event == "published") | "\(.type)/\(.device // "-")/\(.seq)"' | tr -d '"' | paste -sd' ' -)
    if [[ "${pub}" == "NBIRTH/-/0 DBIRTH/pump1/1 DBIRTH/meter1/2" ]]; then ok "edge births"; else fail "edge births: ${pub}"; fi
    doc=$(call "${e}" publish-data '{"update": 0}'); code=$?
    if [[ "${code}" == 0 ]] && jq -e --arg a "${ADAPTER}" '.schemaVersion == "1.0" and .adapter == $a and .operation == "publish-data"
            and .ok and .result.published == ["NDATA", "DDATA/pump1", "DDATA/meter1"]' <<<"${doc}" >/dev/null 2>&1
    then ok "edge call publish-data"; else fail "edge call publish-data: exit ${code}, ${doc}"; fi
    doc=$(call "${e}" publish-data '{"update": 99}'); code=$?
    if [[ "${code}" == 1 ]] && jq -e '.ok == false and .error.code == "invalid-request"' <<<"${doc}" >/dev/null 2>&1
    then ok "edge call with an unknown update"; else fail "edge call publish-data 99: exit ${code}, ${doc}"; fi
    doc=$(call "${e}" request-rebirth '{"group": "interop", "edgeNode": "x"}'); code=$?
    if [[ "${code}" == 5 ]] && jq -e '.ok == false and .error.code == "unsupported"' <<<"${doc}" >/dev/null 2>&1
    then ok "edge call of a host operation is unsupported"; else fail "edge call request-rebirth: exit ${code}, ${doc}"; fi
    stdout_is_json "${e}" edge
    stops_cleanly "${e}" edge
    last_pub=$(events "${e}" 'select(.event == "published") | .type' | tail -1)
    if jq -e '.roles.edge.features.gracefulDeath' <<<"${caps}" >/dev/null; then
        if [[ "${last_pub}" == '"NDEATH"' ]]; then ok "edge publishes NDEATH on SIGTERM"; else fail "edge did not publish NDEATH on SIGTERM (last: ${last_pub})"; fi
    else skip "gracefulDeath"; fi
fi

if [[ "${HOST}" == true ]]; then
    if [[ "${EDGE}" == true ]]; then
        n=$(events "${h}" 'select(.event == "message" and .edgeNode == "smoke-'"${ADAPTER}"'")' | wc -l)
        if (( n >= 6 )); then ok "host received the edge's messages (${n})"; else fail "host received ${n} messages of its own edge, want at least 6"; fi
    fi
    stdout_is_json "${h}" host
    stops_cleanly "${h}" host
fi

finish
