#!/usr/bin/env bash
# Fails when versions.yaml disagrees with what the builds actually pin:
#   - every base image digest a Dockerfile pins (ARG *_DIGEST=sha256:...) is
#     listed in versions.yaml;
#   - the upstream version of every implementation with an adapter directory
#     is the one its Dockerfile builds (ARG <NAME>_VERSION=<version>);
#   - an implementation is "published" exactly when it has an adapter
#     directory, and the Makefile builds exactly the published ones;
#   - every broker is pinned by digest.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
status=0
ok()   { echo "ok   $*"; }
bad()  { echo "FAIL $*"; status=1; }

# field <section> <name> <key>: a scalar of versions.yaml, without quotes.
field() {
    awk -v sec="$1:" -v name="  $2:" -v key="    $3:" '
        $0 == sec { in_sec = 1; next }
        /^[a-z_]+:/ { in_sec = 0 }
        in_sec && $0 == name { in_name = 1; next }
        in_sec && /^  [a-z0-9-]+:/ { in_name = 0 }
        in_sec && in_name && index($0, key) == 1 {
            v = substr($0, length(key) + 1); gsub(/^ +| +$|"/, "", v); print v; exit
        }' versions.yaml
}
names() { awk -v sec="$1:" '$0 == sec { s = 1; next } /^[a-z_]+:/ { s = 0 } s && /^  [a-z0-9-]+:$/ { sub(/:$/, ""); print $1 }' versions.yaml; }

for dockerfile in adapters/*/Dockerfile; do
    while read -r digest; do
        if grep -qF -- "${digest}" versions.yaml; then ok "${dockerfile}: ${digest:0:19}… in versions.yaml"
        else bad "${dockerfile}: base image ${digest} is not in versions.yaml"; fi
    done < <(sed -nE 's/^ARG [A-Z0-9_]+_DIGEST=(sha256:[0-9a-f]{64})$/\1/p' "${dockerfile}")
done

make_adapters=$(sed -nE 's/^ADAPTERS \?= (.*)$/\1/p' Makefile)
published=()
for name in $(names implementations); do
    status_field=$(field implementations "${name}" status)
    version=$(field implementations "${name}" version)
    if [[ -f "adapters/${name}/Dockerfile" ]]; then
        [[ "${status_field}" == published ]] || bad "${name}: has an adapter but status is ${status_field}"
        published+=("${name}")
        if grep -qE "^ARG [A-Z0-9_]+_VERSION=${version//./\\.}\$" "adapters/${name}/Dockerfile"; then
            ok "${name}: Dockerfile builds ${version}"
        else
            bad "${name}: Dockerfile does not build version ${version} (ARG <NAME>_VERSION=${version})"
        fi
    else
        [[ "${status_field}" != published ]] || bad "${name}: published but adapters/${name}/Dockerfile is missing"
    fi
done
if [[ " ${make_adapters} " == " ${published[*]} " ]]; then ok "Makefile ADAPTERS: ${make_adapters}"
else bad "Makefile ADAPTERS is '${make_adapters}', versions.yaml publishes '${published[*]}' (same order)"; fi

for broker in $(names brokers); do
    image=$(field brokers "${broker}" image)
    if [[ "${image}" =~ @sha256:[0-9a-f]{64}$ ]]; then ok "broker ${broker}: pinned by digest"
    else bad "broker ${broker}: ${image} is not pinned by digest"; fi
done

exit "${status}"
