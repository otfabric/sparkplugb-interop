# SPDX-License-Identifier: MIT
# sparkplugb-interop adapter for pysparkplug (Python, Apache-2.0). See README.md.
#
# Build from the repository root:
#   docker build -f adapters/pysparkplug/Dockerfile -t ghcr.io/otfabric/sparkplugb-interop-pysparkplug:dev .
#
# Pins are mirrored in versions.yaml (scripts/check-versions.sh checks them).

# Where the base image comes from. It is pinned by digest, so a mirror of
# Docker Hub serves exactly the same image.
ARG BASE_REGISTRY=docker.io/library
# python:3.13-slim-trixie, multi-arch index (linux/amd64 and linux/arm64).
ARG PYTHON_DIGEST=sha256:57b29a7c050c4e679e4bea572e14b3965cf4b5839de2da49d55e49ffc4024f39

FROM ${BASE_REGISTRY}/python:3.13-slim-trixie@${PYTHON_DIGEST} AS build

# The upstream release, from PyPI with the hash of requirements.txt.
ARG PYSPARKPLUG_VERSION=0.6.1

ENV PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1 PIP_ROOT_USER_ACTION=ignore
WORKDIR /src
COPY adapters/pysparkplug/requirements.txt /src/requirements.txt

# Every package from the hashed lock. paho-mqtt 1.6.1 is published as an sdist
# only: it is built with the locked setuptools and wheel, installed first, and
# without build isolation, so nothing unhashed is downloaded.
RUN set -eu; \
    grep -q "^pysparkplug==${PYSPARKPLUG_VERSION} " requirements.txt; \
    awk '/^[^ #]/ { keep = ($0 ~ /^(setuptools|wheel|packaging)==/) } keep' requirements.txt > build-requirements.txt; \
    python -m venv /opt/venv; \
    /opt/venv/bin/pip install --require-hashes --only-binary=:all: -r build-requirements.txt; \
    /opt/venv/bin/pip install --require-hashes --no-build-isolation -r requirements.txt; \
    /opt/venv/bin/pip check; \
    /opt/venv/bin/python -c "import pysparkplug; assert pysparkplug.__version__ == '${PYSPARKPLUG_VERSION}', pysparkplug.__version__"

COPY adapters/common/collect_licenses.py /src/collect_licenses.py
COPY adapters/pysparkplug/adapter/sparkplugb_interop.py /opt/sparkplugb-interop/sparkplugb_interop.py
RUN set -eu; \
    /opt/venv/bin/python collect_licenses.py /out/licenses setuptools wheel; \
    /opt/venv/bin/python -m py_compile /opt/sparkplugb-interop/sparkplugb_interop.py

FROM ${BASE_REGISTRY}/python:3.13-slim-trixie@${PYTHON_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev
ARG PYSPARKPLUG_VERSION=0.6.1

RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/sparkplugb-interop \
    && chown interop /run/sparkplugb-interop

COPY --from=build /opt/venv /opt/venv
COPY --from=build /out/licenses /licenses
COPY LICENSE /licenses/sparkplugb-interop/LICENSE
COPY adapters/pysparkplug/THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY --from=build /opt/sparkplugb-interop /opt/sparkplugb-interop
COPY fixtures/ /fixtures/
RUN printf '#!/bin/sh\nexec /opt/venv/bin/python -I /opt/sparkplugb-interop/sparkplugb_interop.py "$@"\n' \
        > /usr/local/bin/sparkplugb-interop \
    && chmod 0755 /usr/local/bin/sparkplugb-interop

ENV SPARKPLUGB_INTEROP_ADAPTER_VERSION=${ADAPTER_VERSION} \
    SPARKPLUGB_INTEROP_UPSTREAM_VERSION=${PYSPARKPLUG_VERSION} \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

LABEL org.opencontainers.image.source="https://github.com/otfabric/sparkplugb-interop" \
      org.opencontainers.image.title="sparkplugb-interop-pysparkplug" \
      org.opencontainers.image.description="pysparkplug Sparkplug B edge node and host application for sparkplugb-interop" \
      org.opencontainers.image.licenses="MIT AND Apache-2.0" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

HEALTHCHECK --interval=2s --timeout=2s --retries=15 --start-period=20s \
    CMD ["test", "-f", "/run/sparkplugb-interop/ready"]

USER 10001

ENTRYPOINT ["sparkplugb-interop"]
CMD ["edge"]
