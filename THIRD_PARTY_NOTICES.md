# Third-party notices

The images published from this repository contain third-party software. Each
adapter has its own notices file, `adapters/<adapter>/THIRD_PARTY_NOTICES.md`.
It is copied into its image as `/licenses/THIRD_PARTY_NOTICES.md`, next to the
licence texts of everything the image bundles (`/licenses/<name>/`) and the
list of bundled modules (`/licenses/BUNDLED-MODULES.txt`).

## What is licensed how

| Part | Licence |
|------|---------|
| Repository tooling, fixtures, schemas, scripts, documentation | MIT ([LICENSE](LICENSE)) |
| Adapter sources under `adapters/` | MIT |
| Each published image | The adapter under MIT, and the upstream stack and its dependencies under their own licences (below) |

The adapters use the upstream stacks unmodified, as libraries. None of these
licences reaches a consumer: a library that tests against the images talks to
them over MQTT and reads their output.

## Upstream stacks

| Image | Stack | Version | Licence | Source | Notices |
|-------|-------|---------|---------|--------|---------|
| `sparkplugb-interop-tahu` | Eclipse Tahu (Java) | 1.0.21 | EPL-2.0 | https://github.com/eclipse-tahu/tahu | [adapters/tahu](adapters/tahu/THIRD_PARTY_NOTICES.md) |
| `sparkplugb-interop-pysparkplug` | pysparkplug | 0.6.1 | Apache-2.0 | https://github.com/matteosox/pysparkplug | [adapters/pysparkplug](adapters/pysparkplug/THIRD_PARTY_NOTICES.md) |
| `sparkplugb-interop-sparkplugnet` | SparkplugNet | 1.4.1 | MIT | https://github.com/SeppPenner/SparkplugNet | [adapters/sparkplugnet](adapters/sparkplugnet/THIRD_PARTY_NOTICES.md) |
| `sparkplugb-interop-srad` | srad | 0.5.0 | MIT OR Apache-2.0 | https://github.com/dpazj/srad | [adapters/srad](adapters/srad/THIRD_PARTY_NOTICES.md) |
| `sparkplugb-interop-tahu-js` | Eclipse Tahu JavaScript (sparkplug-client) | 3.2.4 | EPL-2.0 | https://github.com/eclipse-tahu/tahu | [adapters/tahu-js](adapters/tahu-js/THIRD_PARTY_NOTICES.md) |
| `sparkplugb-interop-sparkplughost` | EvergenEnergy/sparkplughost | v0.4.0 | Apache-2.0 | https://github.com/EvergenEnergy/sparkplughost | [adapters/sparkplughost](adapters/sparkplughost/THIRD_PARTY_NOTICES.md) |

Eclipse Tahu is distributed under EPL-2.0. The tahu and tahu-js images contain
it unmodified; its source is available from the upstream repository and, for
the Java artifacts, as the `-sources.jar` on Maven Central.

## Brokers

The tests run these brokers; no image of this repository contains them.

| Broker | Version | Licence |
|--------|---------|---------|
| Eclipse Mosquitto | 2.1.2 | EPL-2.0 OR BSD-3-Clause |
| HiveMQ Community Edition | 2026.5 | Apache-2.0 |
| EMQX | 6.3.2 | BSL-1.1 |
| NanoMQ | 0.25.7 | MIT |
