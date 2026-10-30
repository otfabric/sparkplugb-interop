#!/usr/bin/env bash
# Cross-stack self-test: every host adapter against every edge adapter, for
# every fixture both handle, on the pinned broker BROKER.
#
# Usage: interop.sh <adapter...>      e.g. interop.sh tahu pysparkplug
#
# For each pairing it starts a fresh broker, the host and the edge, drives the
# edge and the host through their calls, and compares what the host reports
# (its "message" events, normalized) with what the fixture says the edge
# publishes. The expectations are computed from the fixture, so every pairing
# must produce the same documents. Steps a pairing lacks a capability for are
# skipped; steps that fail on a bug listed in tests/known-bugs.tsv are counted
# as known, and an entry that no longer fails is an error.
#
# Environment: REGISTRY, VERSION, BROKER, BROKER_IMAGE, KNOWN_BUGS_AS,
#              VERBOSE=1, REPORT_DIR (keeps the normalized host messages of
#              every pairing, and writes results.json, interop-result-v1: only
#              a pass counts as passing), EVENT_TIMEOUT, WAIT_TIMEOUT,
#              CONNECT_TIMEOUT_MS (MQTT connection of the roles, 45000).

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib.sh
source "${SCRIPT_DIR}/lib.sh"

[[ $# -ge 1 ]] || { echo "usage: interop.sh <adapter...>" >&2; exit 2; }
ADAPTERS=("$@")
require_tools
WORK=$(mktemp -d)
STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
RESULT_LOG="${WORK}/results.log"
REPRODUCE="REPORT_DIR=reports VERSION=${VERSION} BROKER=${BROKER} scripts/interop.sh ${ADAPTERS[*]}"
trap 'abort_results "${WORK}" "${STARTED}" "${REPRODUCE}"; rm -rf "${WORK}"; cleanup' EXIT
network_up
HOST_ID="interop-host"

declare -A CAPS VERSIONS
for a in "${ADAPTERS[@]}"; do
    CAPS[$a]=$(docker run --rm "$(image_of "$a")" print-capabilities 2>/dev/null) \
        || { log "cannot read the capabilities of $a"; exit 1; }
    VERSIONS[$a]=$(jq -r '.upstream.version' <<<"${CAPS[$a]}")
    printf '%s\t%s\t%s\n' "$a" "${VERSIONS[$a]}" "$(image_of "$a")" >> "${WORK}/implementations.tsv"
done
printf '%s\t-\t%s\n' "${BROKER}" "$(broker_image "${BROKER}")" >> "${WORK}/implementations.tsv"

has() { jq -e "$2" <<<"${CAPS[$1]}" >/dev/null 2>&1; }  # has <adapter> <jq predicate>

# ---- known upstream bugs ----
# tests/known-bugs.tsv: implementation, version, role (edge or host), peer
# (regex on the adapter of the other role), broker (regex), fixture (regex),
# step (regex), description. An entry applies when the adapter of that role
# in the pairing is that implementation at that version and the other
# columns match.
#
# KNOWN_BUGS_AS="<implementation>=<version> ..." applies the entries of that
# version to the implementation at any version: a candidate of a newer
# upstream then fails only on what is new, and an entry it no longer
# reproduces is reported stale (the bug is fixed).
declare -A KNOWN_USED KNOWN_AS
for kv in ${KNOWN_BUGS_AS:-}; do KNOWN_AS[${kv%%=*}]="${kv#*=}"; done
KNOWN_LINES=()
while IFS=$'\t' read -r k_impl k_version k_role k_peer k_broker k_fixture k_step k_what k_evidence; do
    [[ -z "${k_impl}" || "${k_impl}" == \#* || "${k_impl}" == implementation ]] && continue
    case "${k_role}" in edge|host) ;; *) log "tests/known-bugs.tsv: role '${k_role}' is not edge or host"; exit 1 ;; esac
    [[ -n "${k_evidence}" ]] || { log "tests/known-bugs.tsv: entry for ${k_impl} has fewer than 9 columns"; exit 1; }
    KNOWN_LINES+=("${k_impl}"$'\t'"${k_version}"$'\t'"${k_role}"$'\t'"${k_peer}"$'\t'"${k_broker}"$'\t'"${k_fixture}"$'\t'"${k_step}"$'\t'"${k_what}")
done < "${ROOT}/tests/known-bugs.tsv"

# known_bug <step>: prints the entry that matches the step of the current
# pairing (HA, EA, BROKER, FIXTURE), if any.
known_bug() {
    local step="$1" line impl v role peer br fx s w x y
    for line in ${KNOWN_LINES[@]+"${KNOWN_LINES[@]}"}; do
        IFS=$'\t' read -r impl v role peer br fx s w <<<"${line}"
        if [[ "${role}" == host ]]; then x="${HA}" y="${EA}"; else x="${EA}" y="${HA}"; fi
        if [[ "${impl}" == "${x}" ]] && [[ "${v}" == "${VERSIONS[$x]:-}" || "${v}" == "${KNOWN_AS[$x]:-}" ]] && [[ "${y}" =~ ^(${peer})$ ]] \
            && [[ "${BROKER}" =~ ^(${br})$ ]] && [[ "${FIXTURE}" =~ ^(${fx})$ ]] && [[ "${step}" =~ ^(${s})$ ]]; then
            printf '%s' "${line}"; return 0
        fi
    done
    return 1
}

# entry_key <entry>: what names it in results.json: the columns before the description.
entry_key() { local key="${1%$'\t'*}"; printf '%s\n' "${key//$'\t'/ }"; }

# outcome <passed 0|1> <label> <step>
outcome() {
    local passed="$1" label="$2" step="$3" entry
    entry=$(known_bug "${step}")
    if [[ "${passed}" == 0 ]]; then
        [[ -n "${entry}" ]] && KNOWN_USED["${entry}"]+=pass
        ok "${label}"
    elif [[ -n "${entry}" ]]; then
        KNOWN_USED["${entry}"]+=fail
        KNOWN=$((KNOWN + 1)); record known-difference "${entry##*$'\t'}" "$(entry_key "${entry}")"
        log "known bug: ${label} (${entry##*$'\t'})"
    else
        fail "${label}"
    fi
}

# ---- expectations, computed from the fixture state ----
# STATE is the fixture with the current values; OFFLINE the devices that are dead.
# NORMALIZE keeps what two stacks must agree on: the metrics named by the
# fixture (and Node Control/Rebirth), sorted by name.
NORMALIZE='
  ([$fx.node.metrics[].name, $fx.devices[].metrics[].name, "Node Control/Rebirth"]) as $names
  | {type, device, seq, metrics: ([.metrics[] | select(.name as $n | $names | index($n)) | {name, datatype, value}] | sort_by(.name))}
  | walk(if type == "number" then . + 0 else . end)'

births() {  # births <seq of NBIRTH>: NBIRTH and the DBIRTHs of online devices
    jq -c --argjson seq "$1" --argjson off "${OFFLINE}" '
        def m: [.[] | {name, datatype, value}] | sort_by(.name);
        [{type: "NBIRTH", device: null, seq: $seq,
          metrics: ([{name: "Node Control/Rebirth", datatype: "Boolean", value: false}] + .node.metrics | m)}]
        + [ [.devices[] | select(.id as $d | $off | index($d) | not)] | to_entries[]
            | {type: "DBIRTH", device: .value.id, seq: (($seq + 1 + .key) % 256), metrics: (.value.metrics | m)} ]
        | .[]' <<<"${STATE}"
}

# apply_update <i>: changes STATE and sets UPDATE_OUT to the data messages of
# updates[i]. Not to be run in a subshell: STATE must change here.
apply_update() {
    UPDATE_OUT=$(jq -c --argjson i "$1" --argjson seq "${SEQ}" '
        . as $fx | .updates[$i] as $u
        | def dt($scope; $n): ($scope | map(select(.name == $n)) | .[0].datatype);
          ([if ($u.node | length) > 0 then {type: "NDATA", device: null,
              metrics: ([$u.node[] | {name, datatype: dt($fx.node.metrics; .name), value}] | sort_by(.name))} else empty end]
           + [$fx.devices[] | .id as $d | select($u.devices[$d]) |
              {type: "DDATA", device: $d,
               metrics: ([$u.devices[$d][] | {name, datatype: dt(($fx.devices[] | select(.id == $d) | .metrics); .name), value}] | sort_by(.name))}])
        | to_entries | map(.value + {seq: (($seq + .key) % 256)}) | .[] | {type, device, seq, metrics}' <<<"${STATE}")
    STATE=$(jq -c --argjson i "$1" '
        .updates[$i] as $u
        | def set($changes): .name as $n | ($changes | map(select(.name == $n))) as $c
              | if ($c | length) > 0 then .value = $c[0].value else . end;
          .node.metrics |= map(set($u.node))
        | .devices |= map(.id as $d | .metrics |= map(set($u.devices[$d] // [])))' <<<"${STATE}")
}

# ---- one pairing ----
EXPECTED=()
expect() { local l; while IFS= read -r l; do [[ -n "$l" ]] && EXPECTED+=("$l"); done <<<"$1"; }

# at <stable id>: the scenario, within the fixture, of the current pairing.
at() { scenario "${FIXTURE}.$1" "host=${HA}" "edge=${EA}" "broker=${BROKER}"; }

# compare <label> <step> [id]: waits until the host has as many messages from the
# edge as expected, then compares them all, normalized. The comparison is of
# the messages as a set: each carries its seq, which is what orders Sparkplug
# messages, and MQTT orders messages per topic only (NanoMQ does deliver
# messages of different topics out of publishing order).
compare() {
    local label="$1" step="$2" got want n=${#EXPECTED[@]}
    at "${3:-$2}"
    got=$(wait_events "${H}" ".event == \"message\" and .edgeNode == \"${EDGE_NODE}\"" "${n}" \
        | jq -c --slurpfile fx "${FIXTURE_FILE}" "\$fx[0] as \$fx | ${NORMALIZE}" | head -n "${n}" | LC_ALL=C sort)
    want=$(printf '%s\n' "${EXPECTED[@]}" | jq -c 'walk(if type == "number" then . + 0 else . end)' | LC_ALL=C sort)
    if [[ "${got}" == "${want}" ]]; then
        outcome 0 "${PAIR}: ${label}" "${step}"
    else
        outcome 1 "${PAIR}: ${label}" "${step}"
        if [[ -n "${VERBOSE:-}" || -z "$(known_bug "${step}")" ]]; then
            diff <(printf '%s\n' "${want}") <(printf '%s\n' "${got}") | head -20 >&2
        fi
        # Continue from what the host actually saw, so one failure is reported once.
        mapfile -t EXPECTED < <(printf '%s\n' "${got}")
    fi
}

host_nodes() { call "${H}" nodes | jq -c '.result.nodes[]? | select(.edgeNode == "'"${EDGE_NODE}"'")'; }

bdseq_of() {  # bdseq_of <type> [nth]: the bdSeq of the nth such message the host saw from the edge
    events "${H}" "select(.event == \"message\" and .edgeNode == \"${EDGE_NODE}\" and .type == \"$1\") | .bdSeq" | sed -n "${2:-1}p"
}

new_value() {  # new_value <datatype> <json value>: a different value of the type
    jq -cn --arg dt "$1" --argjson v "$2" '
        if $dt == "Boolean" then ($v | not)
        elif $dt == "String" or $dt == "Text" then ($v + "!")
        elif $dt == "Float" or $dt == "Double" then ($v + 0.5)
        elif $dt == "DateTime" then ($v + 1000)
        elif ($dt == "Int8" and $v >= 127) or ($dt == "UInt8" and $v >= 255) then ($v - 1)
        else ($v + 1) end'
}

pairing() {  # pairing <host adapter> <edge adapter> <fixture>
    HA="$1" EA="$2" FIXTURE="$3"
    PAIR="${BROKER} host=${HA} edge=${EA} fixture=${FIXTURE}"
    FIXTURE_FILE="${ROOT}/fixtures/${FIXTURE}/fixture.json"
    local idx=$((++PAIRS)) b h e url
    EDGE_NODE="e${idx}-${EA}"
    b="broker-${idx}-${RUN_ID}"; h="host-${idx}-${RUN_ID}"; e="edge-${idx}-${RUN_ID}"
    H="${h}"; E="${e}"
    STATE=$(jq -c . "${FIXTURE_FILE}"); OFFLINE='[]'; EXPECTED=()
    log "${PAIR}"

    at broker
    if ! start_broker "${BROKER}" "${b}"; then infra "${PAIR}: broker"; return; fi
    url=$(broker_url "${b}")
    local edge_args=(edge --broker "${url}" --fixture "${FIXTURE}" --edge-node "${EDGE_NODE}")
    local primary=false
    if has "${EA}" '.roles.edge.features.primaryHost'; then primary=true; edge_args+=(--primary-host "${HOST_ID}"); fi

    # With --primary-host the edge starts first: the host only sees the births
    # if the edge waited for its STATE.
    if [[ "${primary}" == true ]]; then start_role "${EA}" "${e}" "${edge_args[@]}"; fi
    start_role "${HA}" "${h}" host --broker "${url}" --host-id "${HOST_ID}"
    at host-ready
    if ! wait_ready "${h}"; then fail "${PAIR}: host ready"; docker rm -f -v "${b}" "${h}" "${e}" >/dev/null 2>&1; return; fi
    if [[ "${primary}" == false ]]; then start_role "${EA}" "${e}" "${edge_args[@]}"; fi
    at edge-ready
    if ! wait_ready "${e}"; then fail "${PAIR}: edge ready"; docker rm -f -v "${b}" "${h}" "${e}" >/dev/null 2>&1; return; fi

    # 1. Births.
    expect "$(births 0)"
    SEQ=${#EXPECTED[@]}
    compare "births" births
    at births-bdseq
    local bdseq; bdseq=$(bdseq_of NBIRTH)
    if [[ "${bdseq}" =~ ^[0-9]+$ ]]; then ok "${PAIR}: NBIRTH bdSeq"; else outcome 1 "${PAIR}: NBIRTH bdSeq is ${bdseq:-missing}" births; fi

    local alias_ok=true
    if has "${EA}" '.roles.edge.features.aliases' && ! has "${HA}" '.roles.host.features.aliasResolution'; then alias_ok=false; fi

    # 2. Data updates.
    at update
    if has "${EA}" '.roles.edge.calls | index("publish-data")' && [[ "${alias_ok}" == true ]]; then
        local i n out doc
        n=$(jq '.updates | length' "${FIXTURE_FILE}")
        for ((i = 0; i < n; i++)); do
            at "update-${i}-call"
            doc=$(call "${E}" publish-data "{\"update\": ${i}}")
            jq -e '.ok' <<<"${doc}" >/dev/null 2>&1 || fail "${PAIR}: call publish-data ${i}: ${doc}"
            apply_update "${i}"; out="${UPDATE_OUT}"
            expect "${out}"
            SEQ=$(( (SEQ + $(grep -c . <<<"${out}")) % 256 ))
            compare "update ${i}" "update-${i}"
        done
    else skip "${PAIR}: updates"; fi

    # 3. Writes from the host, applied by the edge.
    at write
    if has "${EA}" '.roles.edge.features.writes' && has "${HA}" '.roles.host.calls | index("send-command")' && [[ "${alias_ok}" == true ]]; then
        local w scope dev name dt val nv payload metric applied=0
        while IFS=$'\t' read -r scope dev name dt val; do
            [[ -z "${name}" ]] && continue
            metric="${scope}-${dev}-$(tr -cs 'A-Za-z0-9' '-' <<<"${name}" | tr 'A-Z' 'a-z')"; metric="${metric%-}"
            nv=$(new_value "${dt}" "${val}")
            if [[ "${scope}" == node ]]; then
                payload=$(jq -cn --arg n "${EDGE_NODE}" --arg m "${name}" --arg dt "${dt}" --argjson v "${nv}" \
                    '{group: "interop", edgeNode: $n, metrics: [{name: $m, datatype: $dt, value: $v}]}')
                expect "$(jq -cn --arg m "${name}" --arg dt "${dt}" --argjson v "${nv}" --argjson s "${SEQ}" \
                    '{type: "NDATA", device: null, seq: $s, metrics: [{name: $m, datatype: $dt, value: $v}]}')"
                STATE=$(jq -c --arg m "${name}" --argjson v "${nv}" '.node.metrics |= map(if .name == $m then .value = $v else . end)' <<<"${STATE}")
            else
                payload=$(jq -cn --arg n "${EDGE_NODE}" --arg d "${dev}" --arg m "${name}" --arg dt "${dt}" --argjson v "${nv}" \
                    '{group: "interop", edgeNode: $n, device: $d, metrics: [{name: $m, datatype: $dt, value: $v}]}')
                expect "$(jq -cn --arg d "${dev}" --arg m "${name}" --arg dt "${dt}" --argjson v "${nv}" --argjson s "${SEQ}" \
                    '{type: "DDATA", device: $d, seq: $s, metrics: [{name: $m, datatype: $dt, value: $v}]}')"
                STATE=$(jq -c --arg d "${dev}" --arg m "${name}" --argjson v "${nv}" \
                    '.devices |= map(if .id == $d then .metrics |= map(if .name == $m then .value = $v else . end) else . end)' <<<"${STATE}")
            fi
            at "write-${metric}-call"
            w=$(call "${H}" send-command "${payload}")
            jq -e '.ok' <<<"${w}" >/dev/null 2>&1 || fail "${PAIR}: call send-command ${name}: ${w}"
            SEQ=$(( (SEQ + 1) % 256 )); applied=$((applied + 1))
            compare "write ${name}" write "write-${metric}"
        done < <(jq -r '(.node.metrics[] | select(.writable) | ["node", "-", .name, .datatype, (.value | tojson)]),
                        (.devices[] | .id as $d | .metrics[] | select(.writable) | ["device", $d, .name, .datatype, (.value | tojson)]) | @tsv' "${FIXTURE_FILE}")
        if (( applied > 0 )); then
            at write-applied
            if wait_events "${E}" '.event == "command" and .applied == true' "${applied}" >/dev/null; then ok "${PAIR}: edge command events"
            else outcome 1 "${PAIR}: edge reports ${applied} applied command events" write; fi
        fi
    else skip "${PAIR}: writes"; fi

    # 4. Rebirth requested by the host.
    at rebirth-command
    if has "${EA}" '.roles.edge.features.rebirthCommand' && has "${HA}" '.roles.host.calls | index("request-rebirth")'; then
        call "${H}" request-rebirth "{\"group\": \"interop\", \"edgeNode\": \"${EDGE_NODE}\"}" >/dev/null
        local out; out=$(births 0); expect "${out}"; SEQ=$(grep -c . <<<"${out}")
        compare "rebirth on request" rebirth-command
        at rebirth-command-bdseq
        if [[ "$(bdseq_of NBIRTH 2)" == "${bdseq}" ]]; then ok "${PAIR}: rebirth keeps bdSeq"
        else outcome 1 "${PAIR}: rebirth keeps bdSeq ${bdseq} (got $(bdseq_of NBIRTH 2))" rebirth-command; fi
    else skip "${PAIR}: rebirth on request"; fi

    # 5. Device death and birth.
    at device-lifecycle
    local first_dev; first_dev=$(jq -r '.devices[0].id // empty' "${FIXTURE_FILE}")
    if [[ -n "${first_dev}" ]] && has "${EA}" '.roles.edge.calls | index("device-death") and index("device-birth")'; then
        call "${E}" device-death "{\"device\": \"${first_dev}\"}" >/dev/null
        expect "$(jq -cn --arg d "${first_dev}" --argjson s "${SEQ}" '{type: "DDEATH", device: $d, seq: $s, metrics: []}')"
        SEQ=$(( (SEQ + 1) % 256 ))
        compare "device death" device-death
        if has "${HA}" '.roles.host.calls | index("nodes")'; then
            at device-death-tracked
            if host_nodes | jq -e --arg d "${first_dev}" '.online and (.devices[] | select(.device == $d) | .online == false)' >/dev/null 2>&1
            then ok "${PAIR}: host tracks the dead device"; else outcome 1 "${PAIR}: host tracks the dead device: $(host_nodes)" device-death; fi
        fi
        call "${E}" device-birth "{\"device\": \"${first_dev}\"}" >/dev/null
        expect "$(jq -c --arg d "${first_dev}" --argjson s "${SEQ}" '.devices[] | select(.id == $d) |
            {type: "DBIRTH", device: $d, seq: $s, metrics: ([.metrics[] | {name, datatype, value}] | sort_by(.name))}' <<<"${STATE}")"
        SEQ=$(( (SEQ + 1) % 256 ))
        compare "device birth" device-birth
    else skip "${PAIR}: device death and birth"; fi

    # 6. Rebirth on the edge's own initiative.
    at rebirth-call
    if has "${EA}" '.roles.edge.calls | index("rebirth")'; then
        call "${E}" rebirth >/dev/null
        local out; out=$(births 0); expect "${out}"; SEQ=$(grep -c . <<<"${out}")
        compare "rebirth" rebirth-call
    else skip "${PAIR}: rebirth call"; fi

    # 7. What the host tracks.
    at nodes-online
    if has "${HA}" '.roles.host.calls | index("nodes")'; then
        if host_nodes | jq -e --argjson b "${bdseq:-null}" '.online == true and .bdSeq == $b and ([.devices[].online] | all)' >/dev/null 2>&1
        then ok "${PAIR}: host tracks the node online"; else outcome 1 "${PAIR}: host tracks the node online: $(host_nodes)" nodes; fi
    fi

    # 8. Stop: NDEATH with the bdSeq of the birth, published by the edge on a
    #    clean stop, or by the broker (the will) when the edge cannot do that.
    local how=crash step=will
    if has "${EA}" '.roles.edge.features.gracefulDeath'; then how=clean; step=graceful-death; docker stop -t 10 "${E}" >/dev/null
    else docker kill "${E}" >/dev/null; fi
    expect '{"type":"NDEATH","device":null,"seq":null,"metrics":[]}'
    if [[ "${how}" == clean ]]; then
        # An edge node may publish a DDEATH for each device before its NDEATH
        # (Tahu does); Sparkplug allows it, so they are expected too.
        local mine=".event == \"message\" and .edgeNode == \"${EDGE_NODE}\""
        wait_events "${H}" "${mine} and .type == \"NDEATH\"" 1 >/dev/null || true
        expect "$(events "${H}" "select(${mine})" | tail -n "+${#EXPECTED[@]}" \
            | jq -c --slurpfile fx "${FIXTURE_FILE}" "\$fx[0] as \$fx | ${NORMALIZE} | select(.type == \"DDEATH\")")"
    fi
    compare "NDEATH on a ${how} stop" "${step}"
    at "${step}-bdseq"
    if [[ "$(bdseq_of NDEATH)" == "${bdseq}" ]]; then ok "${PAIR}: NDEATH bdSeq matches NBIRTH"
    else outcome 1 "${PAIR}: NDEATH bdSeq $(bdseq_of NDEATH), NBIRTH ${bdseq}" "${step}"; fi
    if has "${HA}" '.roles.host.calls | index("nodes")'; then
        at nodes-offline
        if host_nodes | jq -e '.online == false' >/dev/null 2>&1; then ok "${PAIR}: host tracks the node offline"
        else outcome 1 "${PAIR}: host tracks the node offline: $(host_nodes)" "${step}"; fi
    fi
    docker rm -f -v "${E}" >/dev/null 2>&1

    # 9. Crash: the broker delivers the will, an NDEATH with the new bdSeq.
    STATE=$(jq -c . "${FIXTURE_FILE}"); OFFLINE='[]'
    local births_before; births_before=$(events "${H}" "select(.event == \"message\" and .edgeNode == \"${EDGE_NODE}\" and .type == \"NBIRTH\")" | wc -l)
    start_role "${EA}" "${e}" "${edge_args[@]}"
    at edge-restart
    if wait_ready "${e}"; then
        expect "$(births 0)"
        compare "births after a restart" births births-restart
        docker kill "${E}" >/dev/null
        expect '{"type":"NDEATH","device":null,"seq":null,"metrics":[]}'
        compare "NDEATH will after a crash" will will-crash
        at will-crash-bdseq
        local b2 d2; b2=$(bdseq_of NBIRTH $((births_before + 1))); d2=$(bdseq_of NDEATH "$(events "${H}" "select(.event == \"message\" and .edgeNode == \"${EDGE_NODE}\" and .type == \"NDEATH\")" | wc -l)")
        if [[ -n "${b2}" && "${b2}" == "${d2}" ]]; then ok "${PAIR}: will bdSeq matches NBIRTH"
        else outcome 1 "${PAIR}: will bdSeq ${d2:-missing}, NBIRTH ${b2:-missing}" will; fi
    else at edge-restart; fail "${PAIR}: edge restart"; fi
    docker rm -f -v "${E}" >/dev/null 2>&1

    # 10. The primary host goes offline: the edge sees it and dies.
    at primary-host-offline
    if [[ "${primary}" == true ]] && has "${HA}" '.roles.host.features.gracefulOffline'; then
        start_role "${EA}" "${e}" "${edge_args[@]}"
        if wait_ready "${e}"; then
            docker stop -t 10 "${H}" >/dev/null
            if wait_events "${E}" '.event == "primary-host" and .online == false' 1 >/dev/null \
                && wait_events "${E}" '.event == "published" and .type == "NDEATH"' 1 >/dev/null
            then ok "${PAIR}: edge dies when the primary host goes offline"
            else outcome 1 "${PAIR}: edge dies when the primary host goes offline" primary-host-offline; fi
        else fail "${PAIR}: edge start for the STATE test"; fi
    else skip "${PAIR}: primary host offline"; fi

    if [[ -n "${REPORT_DIR:-}" ]]; then
        mkdir -p "${REPORT_DIR}"
        events "${H}" "select(.event == \"message\" and .edgeNode == \"${EDGE_NODE}\")" \
            | jq -c --slurpfile fx "${FIXTURE_FILE}" "\$fx[0] as \$fx | ${NORMALIZE}" \
            > "${REPORT_DIR}/interop-${BROKER}-${HA}-${EA}-${FIXTURE}.jsonl"
        docker logs "${H}" > "${REPORT_DIR}/interop-${BROKER}-${HA}-${EA}-${FIXTURE}.host.jsonl" 2>/dev/null
    fi
    docker rm -f -v "${b}" "${h}" "${e}" >/dev/null 2>&1
}

PAIRS=0
for HA in "${ADAPTERS[@]}"; do
    has "${HA}" '.roles.host.supported' || continue
    for EA in "${ADAPTERS[@]}"; do
        has "${EA}" '.roles.edge.supported' || continue
        for FIXTURE in $(jq -r '.roles.edge.fixtures[]' <<<"${CAPS[$EA]}"); do
            at pairing
            if [[ ! -f "${ROOT}/fixtures/${FIXTURE}/fixture.json" ]]; then fail "${EA} declares an unknown fixture ${FIXTURE}"; continue; fi
            missing=$(jq -r --argjson caps "${CAPS[$HA]}" '[.node.metrics[], .devices[].metrics[] | .datatype] | unique
                - $caps.roles.host.datatypes | join(",")' "${ROOT}/fixtures/${FIXTURE}/fixture.json")
            if [[ -n "${missing}" ]]; then skip "host=${HA} edge=${EA} fixture=${FIXTURE}: host lacks ${missing}"; continue; fi
            pairing "${HA}" "${EA}" "${FIXTURE}"
        done
    done
done
SCENARIO_ID=""
(( PAIRS > 0 )) || { log "no pairing: the adapters need at least one host and one edge"; FAIL=$((FAIL + 1)); }

# A known bug that did not fail in a pairing that ran it is stale, unless it
# is intermittent (its description starts with "Intermittent:").
# ${WORK}/active: the entries this run can judge; ${WORK}/reproduced: those that
# showed. An intermittent entry that did not show says nothing either way.
for entry in "${!KNOWN_USED[@]}"; do
    if [[ "${KNOWN_USED[$entry]}" == *fail* ]]; then
        entry_key "${entry}" | tee -a "${WORK}/active" >> "${WORK}/reproduced"
    elif [[ "${entry##*$'\t'}" != Intermittent:* ]]; then
        entry_key "${entry}" >> "${WORK}/active"
    fi
    if [[ "${KNOWN_USED[$entry]}" != *fail* && "${entry##*$'\t'}" != Intermittent:* ]]; then
        fail "stale entry in tests/known-bugs.tsv (every matching step passed): ${entry}"
    fi
done

write_results "${WORK}" "${STARTED}" "${REPRODUCE}"
finish
