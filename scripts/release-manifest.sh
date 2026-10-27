#!/usr/bin/env bash
# Prints the manifest.json of a release: the multi-arch image of every
# adapter by digest, the upstream each was built from, and the brokers the
# self-tests ran on, by digest. Consumers pin exactly these references.
#
# Usage: release-manifest.sh <version> <digests.txt>
#        digests.txt has one "<adapter> <sha256:...>" line per adapter, as
#        "make digests" prints them.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ $# -eq 2 ]] || { echo "usage: release-manifest.sh <version> <digests.txt>" >&2; exit 2; }
version="$1" digests="$2"
REGISTRY="${REGISTRY:-ghcr.io/otfabric}"

# yaml_json <section>: the entries of a versions.yaml section as a JSON object
# of objects of scalars (the only shape versions.yaml uses there).
yaml_json() {
    awk -v sec="$1:" '
        $0 == sec { s = 1; next }
        /^[a-z_]+:/ { s = 0 }
        s && /^  [a-z0-9-]+:$/ { name = $1; sub(/:$/, "", name); next }
        s && name != "" && /^    [a-z_]+: / {
            key = $1; sub(/:$/, "", key)
            v = $0; sub(/^    [a-z_]+: /, "", v); sub(/ +#.*$/, "", v)
            gsub(/^"|"$/, "", v)
            printf "%s\t%s\t%s\n", name, key, v
        }' "${ROOT}/versions.yaml" \
    | jq -Rn '[inputs | split("\t")] | reduce .[] as [$n, $k, $v] ({}; .[$n][$k] = $v)'
}

scalar() { sed -nE "s/^$1: \"([^\"]+)\".*/\\1/p" "${ROOT}/versions.yaml"; }

images=$(while read -r adapter digest; do
    [[ "${digest}" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "release-manifest: no digest for ${adapter}" >&2; exit 1; }
    jq -n --arg a "${adapter}" --arg ref "${REGISTRY}/sparkplugb-interop-${adapter}@${digest}" '{($a): $ref}'
done < "${digests}" | jq -s 'add')

jq -n \
    --arg version "${version}" \
    --arg commit "${GITHUB_SHA:-$(git -C "${ROOT}" rev-parse HEAD)}" \
    --arg contract "$(scalar contract_version)" \
    --arg fixtureSchema "$(scalar fixture_schema)" \
    --argjson images "${images}" \
    --argjson impl "$(yaml_json implementations)" \
    --argjson brokers "$(yaml_json brokers)" \
    '{
      version: $version,
      commit: $commit,
      contract: $contract,
      fixtureSchema: $fixtureSchema,
      protocol: "sparkplug-b-3.0.0",
      platforms: ["linux/amd64", "linux/arm64"],
      images: $images,
      upstream: ($impl | with_entries(select(.key as $k | $images | has($k)))
                 | map_values({name: .upstream, version, license})),
      brokers: ($brokers | map_values({version, image: (if (.image | contains("/")) then "docker.io/" else "docker.io/library/" end + .image), license}))
    }'
