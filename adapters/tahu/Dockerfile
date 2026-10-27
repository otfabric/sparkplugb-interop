# SPDX-License-Identifier: MIT
# sparkplugb-interop adapter for Eclipse Tahu (Java, EPL-2.0).
#
# Build from the repository root:
#   docker build -f adapters/tahu/Dockerfile -t ghcr.io/otfabric/sparkplugb-interop-tahu:dev .
#
# Pins are mirrored in versions.yaml (scripts/check-versions.sh checks them).

# Where the base images come from. They are pinned by digest, so a mirror of
# Docker Hub serves exactly the same images.
ARG BASE_REGISTRY=docker.io/library
# maven:3.9.16-eclipse-temurin-21-noble, multi-arch index (linux/amd64 and linux/arm64).
ARG MAVEN_DIGEST=sha256:99e61abcff91a9b1333463bd8451fb18495d6eba9250ac66a338b518f8278320
# eclipse-temurin:21.0.12.1_1-jre-noble, multi-arch index (linux/amd64 and linux/arm64).
ARG JRE_DIGEST=sha256:dd4cbbf22ea3efd0b0bf3c72618a1999e6ba7394afdebf2d0780b2fdee8c6694

FROM ${BASE_REGISTRY}/maven:3.9.16-eclipse-temurin-21-noble@${MAVEN_DIGEST} AS build

# The upstream release, from Maven Central; dependencies.sha256 pins every jar.
ARG TAHU_VERSION=1.0.21

WORKDIR /src
COPY adapters/tahu/pom.xml adapters/tahu/dependencies.sha256 /src/
# Maven verifies the repository checksums (--strict-checksums); the resolved
# runtime jars must then match dependencies.sha256 exactly.
RUN set -eu; \
    grep -q "<tahu.version>${TAHU_VERSION}</tahu.version>" pom.xml; \
    mvn -B -q --strict-checksums -Dmaven.repo.local=/m2 dependency:go-offline dependency:copy-dependencies \
        -DincludeScope=runtime -DoutputDirectory=/src/target/lib
COPY adapters/tahu/src /src/src
RUN set -eu; \
    mvn -B -q -o --strict-checksums -Dmaven.repo.local=/m2 package; \
    (cd target/lib && sha256sum *.jar | sort -k2) > /tmp/resolved.sha256; \
    if ! diff -u dependencies.sha256 /tmp/resolved.sha256; then \
        echo "resolved jars differ from adapters/tahu/dependencies.sha256" >&2; exit 1; \
    fi; \
    mkdir -p /out/lib /out/licenses; \
    cp target/lib/*.jar target/sparkplugb-interop-tahu.jar /out/lib/; \
    for jar in target/lib/*.jar; do \
        name=$(basename "${jar}" .jar); \
        mkdir -p "/tmp/x/${name}"; \
        (cd "/tmp/x/${name}" && jar xf "/src/${jar}" META-INF 2>/dev/null || true); \
        found=$(find "/tmp/x/${name}/META-INF" -maxdepth 2 -type f \
            \( -iname 'LICENSE*' -o -iname 'NOTICE*' -o -iname 'LICENCE*' \) 2>/dev/null || true); \
        if [ -n "${found}" ]; then mkdir -p "/out/licenses/${name}"; cp ${found} "/out/licenses/${name}/"; fi; \
        echo "${name}" >> /out/licenses/BUNDLED-MODULES.txt; \
    done

FROM ${BASE_REGISTRY}/eclipse-temurin:21.0.12.1_1-jre-noble@${JRE_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev
ARG TAHU_VERSION=1.0.21

RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/sparkplugb-interop \
    && chown interop /run/sparkplugb-interop

COPY --from=build /out/lib /opt/sparkplugb-interop/lib
COPY --from=build /out/licenses /licenses
COPY LICENSE /licenses/sparkplugb-interop/LICENSE
COPY adapters/tahu/THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY adapters/tahu/logback.xml /opt/sparkplugb-interop/logback.xml
COPY fixtures/ /fixtures/
RUN printf '%s\n' '#!/bin/sh' \
        'exec java -XX:+UseSerialGC -XX:TieredStopAtLevel=1 -Xss512k -XX:-UsePerfData \' \
        '    -Dlogback.configurationFile=/opt/sparkplugb-interop/logback.xml \' \
        '    -cp "/opt/sparkplugb-interop/lib/*" org.otfabric.sparkplugbinterop.tahu.Main "$@"' \
        > /usr/local/bin/sparkplugb-interop \
    && chmod 0755 /usr/local/bin/sparkplugb-interop

ENV SPARKPLUGB_INTEROP_ADAPTER_VERSION=${ADAPTER_VERSION} \
    SPARKPLUGB_INTEROP_UPSTREAM_VERSION=${TAHU_VERSION}

LABEL org.opencontainers.image.source="https://github.com/otfabric/sparkplugb-interop" \
      org.opencontainers.image.title="sparkplugb-interop-tahu" \
      org.opencontainers.image.description="Eclipse Tahu Sparkplug B edge node and host application for sparkplugb-interop" \
      org.opencontainers.image.licenses="MIT AND EPL-2.0" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

HEALTHCHECK --interval=2s --timeout=2s --retries=15 --start-period=20s \
    CMD ["test", "-f", "/run/sparkplugb-interop/ready"]

USER 10001

ENTRYPOINT ["sparkplugb-interop"]
CMD ["edge"]
