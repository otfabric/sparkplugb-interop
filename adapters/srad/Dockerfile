# SPDX-License-Identifier: MIT
# sparkplugb-interop adapter for srad (Rust, MIT OR Apache-2.0).
#
# Build from the repository root:
#   docker build -f adapters/srad/Dockerfile -t ghcr.io/otfabric/sparkplugb-interop-srad:dev .
#
# Pins are mirrored in versions.yaml (scripts/check-versions.sh checks them).

# Where the base images come from. They are pinned by digest, so a mirror of
# Docker Hub serves exactly the same images.
ARG BASE_REGISTRY=docker.io/library
# rust:1.95-slim-trixie and debian:trixie-slim, multi-arch indexes
# (linux/amd64 and linux/arm64).
ARG RUST_DIGEST=sha256:e14e87345b4d5964ddcc3491d27ee046a0f23820f340c3c1e24da6880141f7c0
ARG DEBIAN_DIGEST=sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

FROM ${BASE_REGISTRY}/rust:1.95-slim-trixie@${RUST_DIGEST} AS build

# The upstream release, from crates.io with the checksums of Cargo.lock.
ARG SRAD_VERSION=0.5.0

WORKDIR /src
COPY adapters/srad/Cargo.toml adapters/srad/Cargo.lock /src/
RUN set -eu; \
    grep -q "^srad = \"=${SRAD_VERSION}\"" Cargo.toml; \
    grep -A1 '^name = "srad"$' Cargo.lock | grep -q "^version = \"${SRAD_VERSION}\"$"; \
    cargo fetch --locked

COPY adapters/srad/src /src/src
COPY adapters/srad/collect-licenses.sh /src/collect-licenses.sh
RUN set -eu; \
    cargo build --locked --offline --release; \
    sh collect-licenses.sh /out/licenses; \
    install -D -m 0755 target/release/sparkplugb-interop /out/sparkplugb-interop

FROM ${BASE_REGISTRY}/debian:trixie-slim@${DEBIAN_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev
ARG SRAD_VERSION=0.5.0

RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/sparkplugb-interop \
    && chown interop /run/sparkplugb-interop

COPY --from=build /out/licenses /licenses
# srad and rumqttc publish their crates without licence files: the files of
# their repositories at the release tags, verified by checksum.
ADD --chmod=0644 --checksum=sha256:508a77d2e7b51d98adeed32648ad124b7b30241a8e70b2e72c99f92d8e5874d1 \
    https://raw.githubusercontent.com/dpazj/srad/srad-v${SRAD_VERSION}/LICENSE-MIT /licenses/srad/LICENSE-MIT
ADD --chmod=0644 --checksum=sha256:a6cba85bc92e0cff7a450b1d873c0eaa2e9fc96bf472df0247a26bec77bf3ff9 \
    https://raw.githubusercontent.com/dpazj/srad/srad-v${SRAD_VERSION}/LICENSE-APACHE /licenses/srad/LICENSE-APACHE
ADD --chmod=0644 --checksum=sha256:cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30 \
    https://raw.githubusercontent.com/bytebeamio/rumqtt/rumqttc-0.24.0/LICENSE /licenses/rumqttc-0.24.0/LICENSE
COPY LICENSE /licenses/sparkplugb-interop/LICENSE
COPY adapters/srad/THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY --from=build /out/sparkplugb-interop /usr/local/bin/sparkplugb-interop
COPY fixtures/ /fixtures/

ENV SPARKPLUGB_INTEROP_ADAPTER_VERSION=${ADAPTER_VERSION} \
    SPARKPLUGB_INTEROP_UPSTREAM_VERSION=${SRAD_VERSION}

LABEL org.opencontainers.image.source="https://github.com/otfabric/sparkplugb-interop" \
      org.opencontainers.image.title="sparkplugb-interop-srad" \
      org.opencontainers.image.description="srad Sparkplug B edge node and host application for sparkplugb-interop" \
      org.opencontainers.image.licenses="MIT AND (MIT OR Apache-2.0)" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

HEALTHCHECK --interval=2s --timeout=2s --retries=15 --start-period=20s \
    CMD ["test", "-f", "/run/sparkplugb-interop/ready"]

USER 10001

ENTRYPOINT ["sparkplugb-interop"]
CMD ["edge"]
