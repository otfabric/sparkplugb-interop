#!/usr/bin/env bash
# Fails when versions.yaml disagrees with what the builds actually pin:
#   - every base image digest a Dockerfile pins (ARG *_DIGEST=sha256:...) is
#     listed in versions.yaml;
#   - the upstream version of every implementation with an adapter directory
#     is the one its Dockerfile builds (ARG <NAME>_VERSION=<version>);
#   - an implementation is "published" exactly when it has an adapter
#     directory, and the Makefile builds exactly the published ones;
#   - every implementation names the commit of its version (revision) and a
#     lock file of the repository that pins that version (lock);
#   - every base image digest of an own/<stack>/Dockerfile is in the
#     own_implementations entry with "build: own/<stack>", and every digest of
#     that entry is in the Dockerfile;
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
            v = substr($0, length(key) + 1); sub(/ +#.*$/, "", v); gsub(/^ +| +$|"/, "", v); print v; exit
        }' versions.yaml
}
names() { awk -v sec="$1:" '$0 == sec { s = 1; next } /^[a-z_]+:/ { s = 0 } s && /^  [a-z0-9-]+:$/ { sub(/:$/, ""); print $1 }' versions.yaml; }
# entry <section> <name>: the lines of one entry of a section.
entry() {
    awk -v sec="$1:" -v name="  $2:" '
        $0 == sec { in_sec = 1; next }
        /^[a-z_]+:/ { in_sec = 0 }
        in_sec && /^  [a-z0-9-]+:$/ { in_name = ($0 == name); next }
        in_sec && in_name { print }' versions.yaml
}

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
    revision=$(field implementations "${name}" revision)
    if [[ "${revision}" =~ ^[0-9a-f]{40}$ ]]; then ok "${name}: revision ${revision:0:12}"
    else bad "${name}: revision '${revision}' is not a commit (40 hexadecimal digits)"; fi
    lock=$(field implementations "${name}" lock)
    if [[ -z "${lock}" || ! -f "${lock}" ]]; then
        bad "${name}: lock '${lock}' is not a file of this repository"
    elif grep -qwF -- "${version}" "${lock}"; then
        ok "${name}: ${lock} pins ${version}"
    else
        bad "${name}: ${lock} does not pin version ${version}"
    fi
done
if [[ " ${make_adapters} " == " ${published[*]} " ]]; then ok "Makefile ADAPTERS: ${make_adapters}"
else bad "Makefile ADAPTERS is '${make_adapters}', versions.yaml publishes '${published[*]}' (same order)"; fi

for dockerfile in own/*/Dockerfile; do
    [[ -f "${dockerfile}" ]] || continue
    dir=${dockerfile%/Dockerfile}
    own=""
    for name in $(names own_implementations); do
        [[ "$(field own_implementations "${name}" build)" == "${dir}" ]] && own=${name}
    done
    if [[ -z "${own}" ]]; then bad "${dockerfile}: no own_implementations entry has build: ${dir}"; continue; fi
    pinned=$(entry own_implementations "${own}")
    built=$(sed -nE 's/^ARG [A-Z0-9_]+_DIGEST=(sha256:[0-9a-f]{64})$/\1/p' "${dockerfile}")
    while read -r digest; do
        if grep -qF -- "${digest}" <<< "${pinned}"; then ok "${dockerfile}: ${digest:0:19}… in own_implementations.${own}"
        else bad "${dockerfile}: base image ${digest} is not in own_implementations.${own}"; fi
    done < <(grep -oE 'sha256:[0-9a-f]{64}' <<< "${built}")
    while read -r digest; do
        grep -qxF -- "${digest}" <<< "${built}" || bad "own_implementations.${own}: ${digest} is not a base image of ${dockerfile}"
    done < <(grep -oE 'sha256:[0-9a-f]{64}' <<< "${pinned}")
done

for broker in $(names brokers); do
    image=$(field brokers "${broker}" image)
    if [[ "${image}" =~ @sha256:[0-9a-f]{64}$ ]]; then ok "broker ${broker}: pinned by digest"
    else bad "broker ${broker}: ${image} is not pinned by digest"; fi
done

exit "${status}"
