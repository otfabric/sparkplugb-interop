# SPDX-License-Identifier: MIT
#
# sparkplugb-interop-otfabric: OT Fabric's own Sparkplug B stack behind the container contract.
# Not an independent implementation: see README.md.
#
# Build from the repository root:
#   make -f own/go-sparkplugb/Makefile image
#
# The build takes the go-sparkplugb release as UPSTREAM_VERSION (the released git tag,
# which is the Go module version) and its commit as UPSTREAM_SHA. There is no pin
# in this repository: the workflow passes the latest release. A build never uses
# main. go.mod and go.sum here are only a starting point for local work.

ARG BASE_REGISTRY=docker.io/library
# golang:1.27.2-trixie and debian:trixie-slim, multi-arch indexes
# (linux/amd64 and linux/arm64).
ARG GOLANG_DIGEST=sha256:47667934874913a449b96faa7ef4777fa285d9ec29126ee80ed61c45b7cccf0e
ARG DEBIAN_DIGEST=sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

FROM ${BASE_REGISTRY}/golang:1.27.2-trixie@${GOLANG_DIGEST} AS build

ARG UPSTREAM_VERSION
ARG UPSTREAM_SHA
ARG ADAPTER_VERSION=dev
ARG GOPROXY=https://proxy.golang.org

ENV CGO_ENABLED=0 GOPROXY=${GOPROXY} GOSUMDB=sum.golang.org GOTOOLCHAIN=local
WORKDIR /src
COPY own/go-sparkplugb/go.mod own/go-sparkplugb/go.sum own/go-sparkplugb/*.go ./
# The tests read the fixtures where the image keeps them.
COPY fixtures /fixtures

RUN set -eu; \
    test -n "${UPSTREAM_VERSION}" || { echo "UPSTREAM_VERSION is required" >&2; exit 1; }; \
    echo "${UPSTREAM_SHA}" | grep -Eq '^[0-9a-f]{40}$'; \
    go get "github.com/otfabric/go-sparkplugb@${UPSTREAM_VERSION}"; \
    go mod tidy; \
    built="$(go list -m -f '{{.Version}}' github.com/otfabric/go-sparkplugb)"; \
    test "${built}" = "${UPSTREAM_VERSION}" || { echo "go.mod has go-sparkplugb ${built}, want ${UPSTREAM_VERSION}" >&2; exit 1; }; \
    go mod download && go mod verify; \
    origin="$(go mod download -json "github.com/otfabric/go-sparkplugb@${UPSTREAM_VERSION}" \
        | sed -nE 's/^[[:space:]]*"Hash": "([0-9a-f]{40})".*/\1/p')"; \
    if [ -n "${origin}" ] && [ "${origin}" != "${UPSTREAM_SHA}" ]; then \
        echo "go-sparkplugb ${UPSTREAM_VERSION} is commit ${origin}, not ${UPSTREAM_SHA}" >&2; exit 1; \
    fi

RUN set -eu; \
    test -z "$(gofmt -l .)"; \
    go vet ./...; \
    go test ./...; \
    go build -trimpath -ldflags "-s -w \
        -X main.adapterVersion=${ADAPTER_VERSION} \
        -X main.upstreamVersion=${UPSTREAM_VERSION} \
        -X main.upstreamRevision=${UPSTREAM_SHA}" \
        -o /out/sparkplugb-interop .; \
    mkdir -p /out/licenses/go; \
    cp "$(go env GOROOT)/LICENSE" /out/licenses/go/LICENSE; \
    go version -m /out/sparkplugb-interop | awk '$1 == "dep" { print $2, $3 }' > /out/licenses/BUNDLED-MODULES.txt; \
    test -s /out/licenses/BUNDLED-MODULES.txt; \
    while read -r module modversion; do \
        dir="$(go list -m -f '{{.Dir}}' "${module}")"; \
        mkdir -p "/out/licenses/${module}"; \
        for file in "${dir}"/LICENSE* "${dir}"/LICENCE* "${dir}"/COPYING* "${dir}"/NOTICE*; do \
            if [ -f "${file}" ]; then cp "${file}" "/out/licenses/${module}/"; fi; \
        done; \
        test -n "$(ls "/out/licenses/${module}")" || { echo "no licence file in ${module} ${modversion}" >&2; exit 1; }; \
    done < /out/licenses/BUNDLED-MODULES.txt

FROM ${BASE_REGISTRY}/debian:trixie-slim@${DEBIAN_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev
ARG UPSTREAM_VERSION

RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/sparkplugb-interop \
    && chown interop /run/sparkplugb-interop

COPY --from=build /out/sparkplugb-interop /usr/local/bin/sparkplugb-interop
COPY --from=build /out/licenses /licenses
COPY LICENSE /licenses/sparkplugb-interop/LICENSE
COPY fixtures /fixtures
COPY THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md

LABEL org.opencontainers.image.title="sparkplugb-interop-otfabric" \
      org.opencontainers.image.description="OT Fabric go-sparkplugb behind the sparkplugb-interop container contract, for testing. Not an independent implementation." \
      org.opencontainers.image.source="https://github.com/otfabric/sparkplugb-interop" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${UPSTREAM_VERSION}"

USER interop
HEALTHCHECK --interval=2s --timeout=2s --start-period=10s --retries=15 \
  CMD test -f /run/sparkplugb-interop/ready

ENTRYPOINT ["/usr/local/bin/sparkplugb-interop"]
CMD ["edge"]
