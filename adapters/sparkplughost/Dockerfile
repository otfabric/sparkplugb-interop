# SPDX-License-Identifier: MIT
# sparkplugb-interop adapter for sparkplughost (Go, Apache-2.0): host role only.
#
# Build from the repository root:
#   docker build -f adapters/sparkplughost/Dockerfile -t ghcr.io/otfabric/sparkplugb-interop-sparkplughost:dev .
#
# Pins are mirrored in versions.yaml (scripts/check-versions.sh checks them).

# Where the base images come from. They are pinned by digest, so a mirror of
# Docker Hub serves exactly the same images.
ARG BASE_REGISTRY=docker.io/library
# golang:1.25.14-trixie, multi-arch index (linux/amd64 and linux/arm64).
ARG GOLANG_DIGEST=sha256:2c4c60ef415fbfa5e90300722293bef36c5e63fae17570ce18f580af933dbd73
# debian:trixie-slim, multi-arch index; it has the test(1) of the health check.
ARG DEBIAN_DIGEST=sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

FROM ${BASE_REGISTRY}/golang:1.25.14-trixie@${GOLANG_DIGEST} AS build

# The upstream release, from the Go module proxy with the hashes of go.sum.
ARG SPARKPLUGHOST_VERSION=v0.4.0

ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=vendor
WORKDIR /src
COPY adapters/sparkplughost/go.mod adapters/sparkplughost/go.sum /src/
RUN set -eu; \
    grep -q "github.com/EvergenEnergy/sparkplughost ${SPARKPLUGHOST_VERSION}\$" go.mod; \
    GOFLAGS=-mod=mod go mod download; \
    GOFLAGS=-mod=mod go mod verify

# The modules are vendored from the verified module cache, then
# overlay/zz_interop.go is added to the sparkplughost package: it exposes the
# messages and nodes the host keeps private. No upstream file is changed.
COPY adapters/sparkplughost/main.go /src/
COPY adapters/sparkplughost/overlay/ /src/overlay/
RUN set -eu; \
    GOFLAGS=-mod=mod go mod vendor; \
    cp overlay/zz_interop.go vendor/github.com/EvergenEnergy/sparkplughost/; \
    go vet ./; \
    go build -trimpath -ldflags="-s -w" -o /out/sparkplugb-interop .; \
    /out/sparkplugb-interop version | grep -q "(sparkplughost ${SPARKPLUGHOST_VERSION})"

# The licence files of every module in the binary, from the module cache.
RUN set -eu; \
    mkdir -p /out/licenses; \
    go version -m /out/sparkplugb-interop | awk '$1 == "dep" { print $2, $3 }' > /out/licenses/BUNDLED-MODULES.txt; \
    while read -r mod ver; do \
        dir=$(GOFLAGS=-mod=mod go list -m -f '{{.Dir}}' "${mod}"); \
        mkdir -p "/out/licenses/${mod}"; \
        found=0; \
        for f in "${dir}"/*; do \
            case "$(basename "${f}" | tr '[:lower:]' '[:upper:]')" in \
                LICENSE*|LICENCE*|COPYING*|NOTICE*|PATENTS*|EDL-V10*|EPL-V20*) cp "${f}" "/out/licenses/${mod}/"; found=1 ;; \
            esac; \
        done; \
        [ "${found}" = 1 ] || { echo "no licence file in ${mod} ${ver}" >&2; exit 1; }; \
    done < /out/licenses/BUNDLED-MODULES.txt

FROM ${BASE_REGISTRY}/debian:trixie-slim@${DEBIAN_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev
ARG SPARKPLUGHOST_VERSION=v0.4.0

RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/sparkplugb-interop \
    && chown interop /run/sparkplugb-interop

COPY --from=build /out/licenses /licenses
COPY LICENSE /licenses/sparkplugb-interop/LICENSE
COPY adapters/sparkplughost/THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY --from=build /out/sparkplugb-interop /usr/local/bin/sparkplugb-interop
COPY fixtures/ /fixtures/

ENV SPARKPLUGB_INTEROP_ADAPTER_VERSION=${ADAPTER_VERSION} \
    SPARKPLUGB_INTEROP_UPSTREAM_VERSION=${SPARKPLUGHOST_VERSION}

LABEL org.opencontainers.image.source="https://github.com/otfabric/sparkplugb-interop" \
      org.opencontainers.image.title="sparkplugb-interop-sparkplughost" \
      org.opencontainers.image.description="Evergen sparkplughost Sparkplug B host application for sparkplugb-interop" \
      org.opencontainers.image.licenses="MIT AND Apache-2.0 AND (EPL-2.0 OR BSD-3-Clause) AND BSD-3-Clause AND BSD-2-Clause" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

HEALTHCHECK --interval=2s --timeout=2s --retries=15 --start-period=20s \
    CMD ["test", "-f", "/run/sparkplugb-interop/ready"]

USER 10001

ENTRYPOINT ["sparkplugb-interop"]
CMD ["host"]
