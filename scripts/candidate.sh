#!/usr/bin/env bash
# Self-test of an upstream release other than the pin: an adapter built on
# another version of its library, or the pinned adapters on another release
# of a broker. The release pins are never changed.
#
# Usage: candidate.sh <adapter> <version>    e.g. candidate.sh srad 0.6.0
#        candidate.sh <broker> <image>       e.g. candidate.sh nanomq emqx/nanomq:0.25.8
#
# An adapter candidate is built from a copy of the repository whose build
# files (pom.xml, requirements.txt, packages.lock.json, Cargo.lock,
# package-lock.json, go.mod) are locked again on that version, with the
# toolchain image the Dockerfile builds with. It is tagged <image>:candidate,
# smoke tested and cross-stack tested on BROKER against the images of
# VERSION of the other adapters (build them first: make images). The entries
# of tests/known-bugs.tsv for the pinned version apply to the candidate: it
# fails on what is new, and an entry it does not reproduce is reported stale.
#
# A broker candidate runs the cross-stack self-test of the images of VERSION
# on that broker image (Docker Hub reference).
#
# Environment: REGISTRY, VERSION (the pinned images, default dev), BROKER,
#              BASE_REGISTRY, BROKER_REGISTRY, ADAPTERS (default: all).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
RETRY="${SCRIPT_DIR}/retry.sh"
REGISTRY="${REGISTRY:-ghcr.io/otfabric}"
VERSION="${VERSION:-dev}"
BASE_REGISTRY="${BASE_REGISTRY:-docker.io/library}"
read -r -a ALL <<<"${ADAPTERS:-$(sed -nE 's/^ADAPTERS \?= (.*)$/\1/p' "${ROOT}/Makefile")}"

log() { echo "[candidate] $*" >&2; }
[[ $# -eq 2 ]] || { echo "usage: candidate.sh <adapter> <version> | <broker> <image>" >&2; exit 2; }
NAME="$1" CANDIDATE="$2"
image() { echo "${REGISTRY}/sparkplugb-interop-$1:$2"; }

for a in "${ALL[@]}"; do
    docker image inspect "$(image "$a" "${VERSION}")" >/dev/null 2>&1 \
        || { log "$(image "$a" "${VERSION}") is missing: build the pinned images first (make images)"; exit 2; }
done

# ---- broker ----
if grep -qE "^  ${NAME}:$" <(sed -n '/^brokers:/,/^[a-z]/p' "${ROOT}/versions.yaml"); then
    log "cross-stack self-test of ${ALL[*]} (${VERSION}) on ${NAME} ${CANDIDATE}"
    BROKER="${NAME}" BROKER_IMAGE="${CANDIDATE}" REGISTRY="${REGISTRY}" VERSION="${VERSION}" \
        exec "${SCRIPT_DIR}/interop.sh" "${ALL[@]}"
fi

# ---- adapter ----
DOCKERFILE="${ROOT}/adapters/${NAME}/Dockerfile"
[[ -f "${DOCKERFILE}" ]] || { log "${NAME} is neither an adapter nor a broker"; exit 2; }
# arg <name>: the default of ARG <name> in the adapter's Dockerfile.
arg() { sed -nE "s/^ARG $1=(.+)$/\1/p" "${DOCKERFILE}" | head -1; }
VAR=$(grep -oE '^ARG [A-Z0-9_]+_VERSION=' "${DOCKERFILE}" | grep -v ADAPTER_VERSION | head -1 | sed -E 's/^ARG |=$//g')
PIN=$(arg "${VAR}")
log "${NAME}: building on ${CANDIDATE} (pin ${PIN}, ${VAR})"

CTX=$(mktemp -d "${TMPDIR:-/tmp}/sparkplugb-candidate.XXXXXX")
trap 'rm -rf "${CTX}"' EXIT
tar -C "${ROOT}" --exclude=./.git --exclude=./reports -cf - . | tar -C "${CTX}" -xf -
DIR="${CTX}/adapters/${NAME}"

# tool <image> <command>: runs command in the adapter directory of the copy,
# as the calling user, in a toolchain image of the Dockerfile.
tool() {
    local img="$1"; shift
    "${RETRY}" docker pull -q "${img}" >/dev/null || return 1
    docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "${DIR}:/work" -w /work "${img}" sh -euc "$*"
}
base() { echo "${BASE_REGISTRY}/$1@$(arg "$2")"; }

case "${NAME}" in
tahu)
    sed -i.bak "s|<tahu.version>${PIN}</tahu.version>|<tahu.version>${CANDIDATE}</tahu.version>|" "${DIR}/pom.xml"
    tool "$(base maven:3.9.16-eclipse-temurin-21-noble MAVEN_DIGEST)" \
        "mvn -B -q -Dmaven.repo.local=/tmp/m2 dependency:copy-dependencies -DincludeScope=runtime -DoutputDirectory=/tmp/lib
         (cd /tmp/lib && sha256sum *.jar | sort -k2) > dependencies.sha256" ;;
pysparkplug)
    sed -i.bak "s|^pysparkplug==${PIN}\$|pysparkplug==${CANDIDATE}|" "${DIR}/requirements.in"
    tool "$(base python:3.13-slim-trixie PYTHON_DIGEST)" \
        "python -m venv /tmp/v && /tmp/v/bin/pip install -q pip==25.2 pip-tools==7.5.1
         /tmp/v/bin/pip-compile -q --generate-hashes --allow-unsafe --strip-extras -o requirements.txt requirements.in" ;;
sparkplugnet)
    sed -i.bak "s|Include=\"SparkplugNet\" Version=\"\[${PIN}\]\"|Include=\"SparkplugNet\" Version=\"[${CANDIDATE}]\"|" \
        "${DIR}/adapter/SparkplugbInterop.csproj"
    DIR="${DIR}/adapter" tool "$(arg DOTNET_REGISTRY)/sdk:10.0@$(arg SDK_DIGEST)" \
        "DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 NUGET_PACKAGES=/tmp/nuget dotnet restore --force-evaluate" ;;
srad)
    sed -i.bak "s|^srad = \"=${PIN}\"|srad = \"=${CANDIDATE}\"|" "${DIR}/Cargo.toml"
    tool "$(base rust:1.95-slim-trixie RUST_DIGEST)" "CARGO_HOME=/tmp/cargo cargo update -p srad" ;;
tahu-js)
    sed -i.bak "s|\"sparkplug-client\": \"${PIN}\"|\"sparkplug-client\": \"${CANDIDATE}\"|" "${DIR}/package.json"
    tool "$(base node:22-trixie-slim NODE_DIGEST)" \
        "npm install --package-lock-only --ignore-scripts --no-audit --no-fund" ;;
sparkplughost)
    tool "$(base golang:1.25.14-trixie GOLANG_DIGEST)" \
        "export GOTOOLCHAIN=local GOFLAGS=-mod=mod GOPATH=/tmp/go GOCACHE=/tmp/gocache
         go get github.com/EvergenEnergy/sparkplughost@${CANDIDATE} && go mod tidy" ;;
*)
    log "no candidate recipe for ${NAME}"; exit 2 ;;
esac
rm -f "${DIR}"/*.bak "${DIR}"/adapter/*.bak
(cd "${CTX}" && git diff --no-index --stat "${ROOT}/adapters/${NAME}" "adapters/${NAME}" >&2) || true

build() {
    "${RETRY}" docker build --build-arg BASE_REGISTRY="$1" --build-arg ADAPTER_VERSION=candidate \
        --build-arg "${VAR}=${CANDIDATE}" --tag "$(image "${NAME}" candidate)" \
        --file "${CTX}/adapters/${NAME}/Dockerfile" "${CTX}"
}
build "${BASE_REGISTRY}" || { [[ "${BASE_REGISTRY}" != docker.io/library ]] \
    && log "building from Docker Hub: ${BASE_REGISTRY} failed" && build docker.io/library; }

for a in "${ALL[@]}"; do
    [[ "$a" == "${NAME}" ]] || docker tag "$(image "$a" "${VERSION}")" "$(image "$a" candidate)"
done
REGISTRY="${REGISTRY}" VERSION=candidate "${SCRIPT_DIR}/smoke.sh" "${NAME}"
REGISTRY="${REGISTRY}" VERSION=candidate KNOWN_BUGS_AS="${NAME}=${PIN}" "${SCRIPT_DIR}/interop.sh" "${ALL[@]}"
