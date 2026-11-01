#!/usr/bin/env bash
# What a host makes of each Int8 and Int16 wire vector of tests/int-vectors.tsv.
#
# Usage: int-vectors.sh <host adapter...>
#
# For each adapter it starts a broker and the host and publishes each vector
# as the NBIRTH of its own edge node. It prints one line per vector: adapter,
# place, datatype, int_value, and the value the host reports, or "dropped" when
# it reports no message. Nothing is asserted: this records behaviour that the
# specification does not settle (docs/COMPATIBILITY.md). A host that does not
# declare DataSet and Template gets the metric vectors only, and what the
# sparkplughost adapter reports is the adapter's reading: the library does not
# decode these values.

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib.sh
source "${SCRIPT_DIR}/lib.sh"

[[ $# -ge 1 ]] || { echo "usage: int-vectors.sh <host adapter...>" >&2; exit 2; }
require_tools
trap cleanup EXIT
network_up

for a in "$@"; do
    caps=$(docker run --rm "$(image_of "${a}")" print-capabilities)
    b="broker-${a}-${RUN_ID}"; h="host-${a}-${RUN_ID}"
    start_broker mosquitto "${b}" || { log "${a}: broker"; continue; }
    start_role "${a}" "${h}" host --broker "$(broker_url "${b}")" --host-id vectors
    wait_ready "${h}" || { log "${a}: host not ready"; continue; }

    nodes=()
    while IFS=$'\t' read -r place dt wire hex; do
        [[ -z "${place}" || "${place}" == \#* ]] && continue
        if [[ "${place}" != metric ]] && ! jq -e '.roles.host.datatypes | index("DataSet")' <<<"${caps}" >/dev/null; then continue; fi
        node="v-${place}-${dt}-${wire}"
        nodes+=("${place}"$'\t'"${dt}"$'\t'"${wire}"$'\t'"${node}")
        xxd -r -p <<<"${hex}" | docker exec -i "${b}" mosquitto_pub -h localhost -t "spBv1.0/interop/NBIRTH/${node}" -s
        sleep 0.3
    done < "${ROOT}/tests/int-vectors.tsv"
    sleep 4

    for n in "${nodes[@]}"; do
        IFS=$'\t' read -r place dt wire node <<<"${n}"
        got=$(events "${h}" "select(.event == \"message\" and .type == \"NBIRTH\" and .edgeNode == \"${node}\") | .metrics[] | select(.name == \"V\")
            | if \"${place}\" == \"metric\" then .value
              elif \"${place}\" == \"property\" then .properties.p.value
              elif \"${place}\" == \"dataset\" then .value.rows[0][0]
              elif \"${place}\" == \"parameter\" then .value.parameters[0].value
              else .value.metrics[0].value end" | head -n 1)
        printf '%s\t%s\t%s\t%s\t%s\n' "${a}" "${place}" "${dt}" "${wire}" "${got:-dropped}"
    done
    docker rm -f -v "${b}" "${h}" >/dev/null 2>&1
done
