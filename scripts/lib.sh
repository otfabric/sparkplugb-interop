#!/usr/bin/env bash
# Shared helpers for the self-test scripts. Source, do not execute.
#
# Brokers, edges and hosts run as containers on a private Docker network and
# reach each other by container name, so no host port is published and
# parallel runs do not collide.

REGISTRY="${REGISTRY:-ghcr.io/otfabric}"
VERSION="${VERSION:-dev}"
RUN_ID="${RUN_ID:-$$}"
NETWORK="sparkplugb-interop-${RUN_ID}"
READY_FILE="/run/sparkplugb-interop/ready"
WAIT_TIMEOUT="${WAIT_TIMEOUT:-60}"
# JVM and Erlang brokers start slowly on a busy runner.
BROKER_TIMEOUT="${BROKER_TIMEOUT:-180}"
# Registry of the broker images: a mirror of Docker Hub serves the same
# digests (see the Makefile).
BROKER_REGISTRY="${BROKER_REGISTRY:-docker.io}"
BROKER="${BROKER:-mosquitto}"

_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${_LIB_DIR}/.." && pwd)"

PASS=0
FAIL=0
SKIPPED=0

log()  { echo "[$(basename "$0" .sh)] $*" >&2; }
ok()   { PASS=$((PASS + 1)); [[ -n "${VERBOSE:-}" ]] && log "ok: $*"; return 0; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; return 0; }
skip() { SKIPPED=$((SKIPPED + 1)); [[ -n "${VERBOSE:-}" ]] && log "skip: $*"; return 0; }

# check <label> <command...>: ok when the command succeeds.
check() { local label="$1"; shift; if "$@" >/dev/null 2>&1; then ok "${label}"; else fail "${label}"; fi; }

image_of() { echo "${REGISTRY}/sparkplugb-interop-$1:${VERSION}"; }

# broker_image <name>: the pinned reference of versions.yaml, through
# BROKER_REGISTRY. BROKER_IMAGE (a Docker Hub reference) replaces the pin of
# BROKER: scripts/candidate.sh tests a newer broker release with it.
broker_image() {
    local ref
    if [[ -n "${BROKER_IMAGE:-}" && "$1" == "${BROKER}" ]]; then ref="${BROKER_IMAGE}"
    else ref=$(awk -v b="  $1:" '$0 == b { found = 1; next } found && /^  [a-z]/ { exit }
        found && /^    image:/ { gsub(/"/, "", $2); print $2; exit }' "${ROOT}/versions.yaml"); fi
    [[ -n "${ref}" ]] || { log "no broker $1 in versions.yaml"; return 1; }
    case "${ref}" in
        */*) echo "${BROKER_REGISTRY}/${ref}" ;;
        *)   echo "${BROKER_REGISTRY}/library/${ref}" ;;
    esac
}

brokers() { awk '/^brokers:/ { f = 1; next } f && /^[a-z]/ { exit } f && /^  [a-z0-9-]+:$/ { sub(/:$/, "", $1); print $1 }' "${ROOT}/versions.yaml"; }

require_tools() {
    local t
    for t in docker jq; do
        command -v "$t" >/dev/null || { log "missing required tool: $t"; exit 2; }
    done
}

network_up() { docker network create "${NETWORK}" >/dev/null; }

cleanup() {
    docker ps -aq --filter "label=sparkplugb-interop.run=${RUN_ID}" | xargs -r docker rm -f -v >/dev/null 2>&1
    docker network rm "${NETWORK}" >/dev/null 2>&1
    return 0
}

run_label() { echo "sparkplugb-interop.run=${RUN_ID}"; }

# pullable <ref>: prints ref, or the same image on Docker Hub when ref is on
# BROKER_REGISTRY and that mirror does not serve it (a mirror does not always
# have every manifest). Pulls it unless it is present.
pullable() {
    local ref="$1" hub
    if docker image inspect "${ref}" >/dev/null 2>&1 \
        || "${_LIB_DIR}/retry.sh" docker pull -q "${ref}" >/dev/null 2>&1; then echo "${ref}"; return 0; fi
    [[ "${BROKER_REGISTRY}" != docker.io && "${ref}" == "${BROKER_REGISTRY}/"* ]] || { log "cannot pull ${ref}"; return 1; }
    hub="docker.io/${ref#"${BROKER_REGISTRY}"/}"
    log "pulling ${hub}: ${BROKER_REGISTRY} does not serve it"
    docker image inspect "${hub}" >/dev/null 2>&1 \
        || "${_LIB_DIR}/retry.sh" docker pull -q "${hub}" >/dev/null || return 1
    echo "${hub}"
}

# start_broker <name> <container>: starts the pinned broker and waits until it
# accepts an MQTT CONNECT and PUBLISH (the pinned mosquitto_pub is the probe).
start_broker() {
    local broker="$1" name="$2" img probe args=() start=${SECONDS}
    img=$(broker_image "${broker}") && img=$(pullable "${img}") || return 1
    probe=$(broker_image mosquitto) && probe=$(pullable "${probe}") || return 1
    [[ "${broker}" == mosquitto ]] && args=(mosquitto -c /mosquitto-no-auth.conf)
    docker rm -f -v "${name}" >/dev/null 2>&1
    docker run -d --name "${name}" --network "${NETWORK}" --label "$(run_label)" \
        "${img}" ${args[@]+"${args[@]}"} >/dev/null || return 1
    until docker run --rm --network "${NETWORK}" --label "$(run_label)" --entrypoint mosquitto_pub \
            "${probe}" -h "${name}" -t interop/probe -m ready -q 1 >/dev/null 2>&1; do
        if (( SECONDS - start >= BROKER_TIMEOUT )); then
            log "broker ${name} (${broker}) not ready after ${BROKER_TIMEOUT}s"; docker logs --tail 20 "${name}" >&2
            return 1
        fi
        sleep 1
    done
}

broker_url() { echo "tcp://$1:1883"; }

# start_role <adapter> <container> <edge|host> [args...]
start_role() {
    local adapter="$1" name="$2"; shift 2
    docker rm -f -v "${name}" >/dev/null 2>&1
    docker run -d --name "${name}" --network "${NETWORK}" --label "$(run_label)" \
        ${DOCKER_ARGS[@]+"${DOCKER_ARGS[@]}"} "$(image_of "${adapter}")" "$@" >/dev/null
}

# wait_ready <container>: the ready file exists only once the role is fully up.
wait_ready() {
    local name="$1" waited=0 state
    while true; do
        state=$(docker inspect --format '{{.State.Status}}' "${name}" 2>/dev/null || echo absent)
        if [[ "${state}" != "running" ]]; then
            log "${name} is ${state}"; docker logs --tail 30 "${name}" >&2 2>/dev/null; return 1
        fi
        docker exec "${name}" test -f "${READY_FILE}" 2>/dev/null && return 0
        if (( waited >= WAIT_TIMEOUT * 5 )); then
            log "${name} not ready after ${WAIT_TIMEOUT}s"; docker logs --tail 30 "${name}" >&2; return 1
        fi
        sleep 0.2; waited=$((waited + 1))
    done
}

# call <container> <operation> [payload]: prints the result document; returns
# the exit code of "call". Capture both: doc=$(call ...); CALL_EXIT=$?
call() {
    local name="$1" op="$2" payload="${3:-}"
    [[ -n "${payload}" ]] || payload='{}'
    local doc rc=0
    doc=$(docker exec "${name}" /usr/local/bin/sparkplugb-interop call "${op}" --payload "${payload}" 2>/dev/null) || rc=$?
    printf '%s\n' "${doc}"
    if [[ -n "${REPORT_DIR:-}" && -n "${doc}" ]]; then
        mkdir -p "${REPORT_DIR}"
        # call runs in a command substitution: BASHPID and RANDOM keep names apart.
        printf '%s\n' "${doc}" > "${REPORT_DIR}/call-${RUN_ID}-${BASHPID}-${RANDOM}-${op}.json"
    fi
    return "${rc}"
}

# events <container> [jq filter]: the JSON events of a role so far.
events() { docker logs "$1" 2>/dev/null | jq -c "select(type == \"object\") | ${2:-.}" 2>/dev/null; }

# wait_events <container> <jq filter> <count>: waits until at least count
# events match the filter; prints them. No fixed delays: it polls.
wait_events() {
    local name="$1" filter="$2" want="$3" waited=0 got
    while true; do
        got=$(events "${name}" "select(${filter})")
        if [[ -n "${got}" ]] && (( $(wc -l <<<"${got}") >= want )); then printf '%s\n' "${got}"; return 0; fi
        if (( waited >= ${EVENT_TIMEOUT:-15} * 5 )); then printf '%s\n' "${got}"; return 1; fi
        sleep 0.2; waited=$((waited + 1))
    done
}

KNOWN=0

finish() {
    echo "" >&2
    local extra=""
    [[ "${KNOWN}" -gt 0 ]] && extra="${extra}, ${KNOWN} known upstream bugs"
    [[ "${SKIPPED}" -gt 0 ]] && extra="${extra}, ${SKIPPED} skipped (capability not declared)"
    log "${PASS} passed, ${FAIL} failed${extra}"
    [[ "${FAIL}" -eq 0 ]]
}
