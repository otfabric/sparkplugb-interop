# SPDX-License-Identifier: MIT
# sparkplugb-interop adapter for the Eclipse Tahu JavaScript sparkplug-client
# (npm, EPL-2.0). Edge role only. See THIRD_PARTY_NOTICES.md.
#
# Build from the repository root:
#   docker build -f adapters/tahu-js/Dockerfile -t ghcr.io/otfabric/sparkplugb-interop-tahu-js:dev .
#
# Pins are mirrored in versions.yaml (scripts/check-versions.sh checks them).

# Where the base image comes from. It is pinned by digest, so a mirror of
# Docker Hub serves exactly the same image.
ARG BASE_REGISTRY=docker.io/library
# node:22-trixie-slim, multi-arch index (linux/amd64 and linux/arm64).
ARG NODE_DIGEST=sha256:8eb619e402eaede1748a3c8173bffab391592812e85136452f781741c7242ddf

FROM ${BASE_REGISTRY}/node:22-trixie-slim@${NODE_DIGEST} AS build

# The upstream release, from npm with the integrity hashes of package-lock.json.
ARG SPARKPLUG_CLIENT_VERSION=3.2.4

ENV NPM_CONFIG_UPDATE_NOTIFIER=false NPM_CONFIG_FUND=false NPM_CONFIG_AUDIT=false
WORKDIR /opt/sparkplugb-interop
COPY adapters/tahu-js/package.json adapters/tahu-js/package-lock.json ./

# Exactly the lock, without install scripts: no package here needs one.
RUN set -eu; \
    node -e "const p=require('./package.json'); if (p.dependencies['sparkplug-client'] !== process.argv[1]) process.exit(1)" "${SPARKPLUG_CLIENT_VERSION}"; \
    npm ci --omit=dev --ignore-scripts; \
    node -e "const v=require('sparkplug-client/package.json').version; if (v !== process.argv[1]) { console.error(v); process.exit(1) }" "${SPARKPLUG_CLIENT_VERSION}"

COPY adapters/tahu-js/collect-licenses.js /src/collect-licenses.js
COPY adapters/tahu-js/adapter/sparkplugb-interop.js /opt/sparkplugb-interop/adapter/sparkplugb-interop.js
RUN set -eu; \
    node /src/collect-licenses.js node_modules /out/licenses sparkplug-payload=sparkplug-client; \
    node --check adapter/sparkplugb-interop.js

FROM ${BASE_REGISTRY}/node:22-trixie-slim@${NODE_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev
ARG SPARKPLUG_CLIENT_VERSION=3.2.4

# npm and corepack are not needed at run time.
RUN rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
        /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
    && useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/sparkplugb-interop \
    && chown interop /run/sparkplugb-interop

COPY --from=build /opt/sparkplugb-interop /opt/sparkplugb-interop
COPY --from=build /out/licenses /licenses
COPY LICENSE /licenses/sparkplugb-interop/LICENSE
COPY adapters/tahu-js/THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY fixtures/ /fixtures/
RUN printf '#!/bin/sh\nexec node /opt/sparkplugb-interop/adapter/sparkplugb-interop.js "$@"\n' \
        > /usr/local/bin/sparkplugb-interop \
    && chmod 0755 /usr/local/bin/sparkplugb-interop

ENV SPARKPLUGB_INTEROP_ADAPTER_VERSION=${ADAPTER_VERSION} \
    SPARKPLUGB_INTEROP_UPSTREAM_VERSION=${SPARKPLUG_CLIENT_VERSION} \
    NODE_ENV=production

LABEL org.opencontainers.image.source="https://github.com/otfabric/sparkplugb-interop" \
      org.opencontainers.image.title="sparkplugb-interop-tahu-js" \
      org.opencontainers.image.description="Eclipse Tahu JavaScript sparkplug-client Sparkplug B edge node for sparkplugb-interop" \
      org.opencontainers.image.licenses="MIT AND EPL-2.0" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

HEALTHCHECK --interval=2s --timeout=2s --retries=15 --start-period=20s \
    CMD ["test", "-f", "/run/sparkplugb-interop/ready"]

USER 10001

ENTRYPOINT ["sparkplugb-interop"]
CMD ["edge"]
