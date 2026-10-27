# SPDX-License-Identifier: MIT
# sparkplugb-interop adapter for SparkplugNet (C#, MIT). See THIRD_PARTY_NOTICES.md.
#
# Build from the repository root:
#   docker build -f adapters/sparkplugnet/Dockerfile -t ghcr.io/otfabric/sparkplugb-interop-sparkplugnet:dev .
#
# Pins are mirrored in versions.yaml (scripts/check-versions.sh checks them).

# mcr.microsoft.com is not on Docker Hub, so BASE_REGISTRY does not apply to
# these images; they are pinned by digest all the same.
ARG DOTNET_REGISTRY=mcr.microsoft.com/dotnet
# sdk:10.0 (10.0.403) and runtime:10.0 (10.0.14), Ubuntu 24.04, multi-arch
# indexes (linux/amd64 and linux/arm64).
ARG SDK_DIGEST=sha256:de4fe90a98845d75910b9769662465c84de1a934665c431279d1382b2bddc02b
ARG RUNTIME_DIGEST=sha256:a3f9f81954b9c810af2ed18849716c8dc74a4707bd0907fdbd69be81554c0f30

FROM ${DOTNET_REGISTRY}/sdk:10.0@${SDK_DIGEST} AS build

# The upstream release, from NuGet with the content hashes of packages.lock.json.
ARG SPARKPLUGNET_VERSION=1.4.1

ENV DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 NUGET_PACKAGES=/nuget
WORKDIR /src
COPY adapters/sparkplugnet/adapter/SparkplugbInterop.csproj adapters/sparkplugnet/adapter/packages.lock.json /src/
RUN set -eu; \
    grep -q "Include=\"SparkplugNet\" Version=\"\[${SPARKPLUGNET_VERSION}\]\"" SparkplugbInterop.csproj; \
    dotnet restore --locked-mode

COPY adapters/sparkplugnet/adapter/Program.cs /src/
RUN dotnet publish -c Release --no-restore --self-contained false -p:UseAppHost=false -o /opt/sparkplugb-interop

# Each package's licence: the files it ships and the SPDX expression of its nuspec.
RUN set -eu; \
    mkdir -p /out/licenses; \
    for dir in /nuget/*/*/; do \
        name=$(basename "$(dirname "${dir}")"); version=$(basename "${dir}"); \
        mkdir -p "/out/licenses/${name}"; \
        echo "${name} ${version}" >> /out/licenses/BUNDLED-MODULES.txt; \
        find "${dir}" -maxdepth 1 -type f -iregex '.*/\(licen[cs]e\|notice\|copying\)[^/]*' -exec cp {} "/out/licenses/${name}/" \; ; \
        sed -n 's:.*<license type="expression">\(.*\)</license>.*:\1:p' "${dir}"/*.nuspec > "/out/licenses/${name}/LICENSE-EXPRESSION"; \
        sed -n 's:.*<projectUrl>\(.*\)</projectUrl>.*:\1:p' "${dir}"/*.nuspec >> "/out/licenses/${name}/LICENSE-EXPRESSION"; \
        if [ -z "$(ls -A "/out/licenses/${name}" | grep -v LICENSE-EXPRESSION)" ] && [ ! -s "/out/licenses/${name}/LICENSE-EXPRESSION" ]; then \
            echo "no licence for ${name}" >&2; exit 1; \
        fi; \
    done; \
    sort -o /out/licenses/BUNDLED-MODULES.txt /out/licenses/BUNDLED-MODULES.txt

FROM ${DOTNET_REGISTRY}/runtime:10.0@${RUNTIME_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev
ARG SPARKPLUGNET_VERSION=1.4.1

RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/sparkplugb-interop \
    && chown interop /run/sparkplugb-interop

COPY --from=build /opt/sparkplugb-interop /opt/sparkplugb-interop
COPY --from=build /out/licenses /licenses
COPY LICENSE /licenses/sparkplugb-interop/LICENSE
COPY adapters/sparkplugnet/THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY fixtures/ /fixtures/
RUN printf '#!/bin/sh\nexec /usr/bin/dotnet /opt/sparkplugb-interop/sparkplugb-interop.dll "$@"\n' \
        > /usr/local/bin/sparkplugb-interop \
    && chmod 0755 /usr/local/bin/sparkplugb-interop

ENV SPARKPLUGB_INTEROP_ADAPTER_VERSION=${ADAPTER_VERSION} \
    SPARKPLUGB_INTEROP_UPSTREAM_VERSION=${SPARKPLUGNET_VERSION} \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    DOTNET_gcServer=0 \
    DOTNET_TieredPGO=0

LABEL org.opencontainers.image.source="https://github.com/otfabric/sparkplugb-interop" \
      org.opencontainers.image.title="sparkplugb-interop-sparkplugnet" \
      org.opencontainers.image.description="SparkplugNet Sparkplug B edge node and host application for sparkplugb-interop" \
      org.opencontainers.image.licenses="MIT AND Apache-2.0" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

HEALTHCHECK --interval=2s --timeout=2s --retries=15 --start-period=20s \
    CMD ["test", "-f", "/run/sparkplugb-interop/ready"]

USER 10001

ENTRYPOINT ["sparkplugb-interop"]
CMD ["edge"]
