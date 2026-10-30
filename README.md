# sparkplugb-interop — Sparkplug B 3.0.0 Interoperability Testing with Docker

A Docker-based interoperability testing framework for
[Sparkplug B 3.0.0](https://sparkplug.eclipse.org/specification/), providing
independent edge node and host application implementations, deterministic
fixtures, and automated cross-stack testing on four MQTT brokers.
Test Sparkplug B libraries against established implementations including
Eclipse Tahu (Java and JavaScript), pysparkplug, SparkplugNet, srad and
sparkplughost. Reference implementations are independently containerized, so
third-party stacks never have to be integrated into the library under test.

| Adapter | Implementation | Language | Edge | Host | Image |
|---------|----------------|----------|:----:|:----:|-------|
| `tahu` | [Eclipse Tahu](https://github.com/eclipse-tahu/tahu) 1.0.21 | Java | yes | yes | `ghcr.io/otfabric/sparkplugb-interop-tahu` |
| `pysparkplug` | [pysparkplug](https://github.com/matteosox/pysparkplug) 0.6.1 | Python | yes | yes | `ghcr.io/otfabric/sparkplugb-interop-pysparkplug` |
| `sparkplugnet` | [SparkplugNet](https://github.com/SeppPenner/SparkplugNet) 1.4.1 | C# | yes | yes | `ghcr.io/otfabric/sparkplugb-interop-sparkplugnet` |
| `srad` | [srad](https://github.com/dpazj/srad) 0.5.0 | Rust | yes | yes | `ghcr.io/otfabric/sparkplugb-interop-srad` |
| `tahu-js` | [Eclipse Tahu sparkplug-client](https://github.com/eclipse-tahu/tahu/tree/master/javascript) 3.2.4 | JavaScript | yes | no | `ghcr.io/otfabric/sparkplugb-interop-tahu-js` |
| `sparkplughost` | [EvergenEnergy/sparkplughost](https://github.com/EvergenEnergy/sparkplughost) v0.4.0 | Go | no | yes | `ghcr.io/otfabric/sparkplugb-interop-sparkplughost` |

| Broker | Version | Image (pinned by digest in [versions.yaml](versions.yaml)) |
|--------|---------|-------|
| Eclipse Mosquitto | 2.1.2 | `eclipse-mosquitto:2.1.2-alpine` |
| HiveMQ Community Edition | 2026.5 | `hivemq/hivemq-ce:2026.5` |
| EMQX | 6.3.2 | `emqx/emqx:6.3.2` |
| NanoMQ | 0.25.7 | `emqx/nanomq:0.25.7` |

The stacks do not all do the same: only Tahu (Java) waits for a primary host,
Tahu and tahu-js rebirth on `Node Control/Rebirth`, pysparkplug and
SparkplugNet edges publish data by name rather than alias, and SparkplugNet's
host neither resolves aliases nor checks `seq`. Each image says what it
supports (`print-capabilities`); see [docs/CAPABILITIES.md](docs/CAPABILITIES.md).

Evaluated and left out: Tahu C and Node-RED. See [PLAN.md](PLAN.md).

**Docs:** [docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md) (commands, events, call results, exit codes) · [docs/FIXTURES.md](docs/FIXTURES.md) (the simulated edge nodes) · [docs/CAPABILITIES.md](docs/CAPABILITIES.md) · [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) (pins, cross-stack matrix, findings) · [REQUIREMENTS.md](REQUIREMENTS.md) · [PLAN.md](PLAN.md) · [RELEASE.md](RELEASE.md)

## Table of contents

- [sparkplugb-interop — Sparkplug B 3.0.0 Interoperability Testing with Docker](#sparkplugb-interop--sparkplug-b-300-interoperability-testing-with-docker)
  - [Table of contents](#table-of-contents)
  - [What this repository is, and is not](#what-this-repository-is-and-is-not)
  - [Quick start](#quick-start)
  - [Using the images from a library's tests](#using-the-images-from-a-librarys-tests)
  - [Repository layout](#repository-layout)
  - [Development](#development)
  - [Security](#security)
  - [License](#license)

## What this repository is, and is not

It owns the reference implementations, their adapters, the fixtures, the
container contract, the images and the self-tests that show the references
agree with each other on every broker.

It does not contain, import, build or know about any library under test.
[otfabric/go-sparkplugb](https://github.com/otfabric/go-sparkplugb) is a
consumer: it pulls the images and owns its own scenarios, assertions and
compatibility claims. A passing self-test here says the *references*
interoperate; it says nothing about a consumer.

The fixtures describe what an edge node is; the adapters make each
implementation behave that way; the consumer decides what to assert.

## Quick start

Needs Docker and `jq`.

```sh
make images      # build the adapter images for this machine
make smoke       # container contract of each image
make interop     # every host against every edge, every fixture, on Mosquitto
make interop BROKER=nanomq
```

An edge node and a host application, on one broker:

```sh
docker network create sp
docker run -d --name broker --network sp eclipse-mosquitto:2.1.2-alpine mosquitto -c /mosquitto-no-auth.conf
docker run -d --name host --network sp ghcr.io/otfabric/sparkplugb-interop-srad:dev host --broker tcp://broker --host-id h1
docker run -d --name edge --network sp ghcr.io/otfabric/sparkplugb-interop-tahu:dev edge --broker tcp://broker
docker exec edge sparkplugb-interop call publish-data --payload '{"update": 0}'
docker exec host sparkplugb-interop call nodes | jq .
docker logs host | jq -c 'select(.event == "message") | {type, edgeNode, device, seq}'
```

Every image has the same commands:

```text
edge                 a Sparkplug edge node with the metrics and devices of a fixture
host                 a primary Sparkplug host application
call <operation>     drive a running edge or host: publish-data, rebirth, device-death,
                     device-birth, send-command, request-rebirth, nodes
print-capabilities   what this adapter supports, as JSON
print-fixture        the fixture baked into the image
```

`edge` and `host` write what they publish and receive to stdout as JSON
Lines; `call` returns a result document. Both are specified by JSON Schemas
in [schemas/](schemas/).

`make documents` also writes `reports/results.json`, the organisation's
[interop-result-v1](schemas/interop-result-v1.schema.json). Only `pass` counts as passing: a run with
no pass, or with a known bug that no longer reproduces, fails.

## Using the images from a library's tests

A consumer needs Docker, the image references and its own test code. It does
not clone this repository.

**Library as host, reference as edge.** Start a broker and the library's host,
then the reference edge, and wait for its readiness file (never for a delay):

```sh
docker run -d --name edge --network sp ghcr.io/otfabric/sparkplugb-interop-tahu:<tag> edge --broker tcp://broker --fixture scalars
until docker exec edge test -f /run/sparkplugb-interop/ready; do sleep 0.1; done
docker exec edge sparkplugb-interop call publish-data --payload '{"update": 0}'
```

The edge's stdout says what it published (`published`) and which commands it
applied (`command`): assert that the library's view of the node matches.

**Library as edge, reference as host.** Start the reference host, connect the
library's edge node, and read what the host saw: its `message` events, and
`call nodes` for its view of every node, device and metric. `call
send-command` and `call request-rebirth` make the host command the library.

**Expected values** come from the image: `print-fixture scalars` prints the
fixture it was built with, so expectations cannot drift from the image.

**What a stack supports** comes from the image too: `print-capabilities`.
Skip, do not fail, a scenario that needs a feature or a datatype an image
does not declare.

**Pin by digest** in release qualification
(`ghcr.io/otfabric/sparkplugb-interop-tahu@sha256:...`); the digests are in
each release's `manifest.json` and its notes.

## Repository layout

```text
sparkplugb-interop/
├── adapters/
│   ├── tahu/            Java adapter: Dockerfile, pom.xml, src/
│   ├── pysparkplug/     Python adapter: Dockerfile, requirements (hashed), adapter/
│   ├── sparkplugnet/    C# adapter: Dockerfile, adapter/
│   ├── srad/            Rust adapter: Dockerfile, Cargo.toml, src/
│   ├── tahu-js/         JavaScript adapter: Dockerfile, package.json, adapter/
│   └── sparkplughost/   Go adapter: Dockerfile, go.mod, main.go, overlay/
├── fixtures/
│   ├── schema/          JSON Schema of the fixture format
│   ├── baseline/        an edge node with two devices and writable metrics
│   └── scalars/         an edge node with every scalar datatype
├── schemas/             JSON Schemas of capabilities, events and call results
├── tests/known-bugs.tsv bugs of upstream versions, scoped by role, peer, broker, fixture and step
├── scripts/             validate, smoke, cross-stack self-test, upstream candidates, release manifest
├── docs/                contract, fixtures, capabilities, compatibility
├── versions.yaml        every upstream pin, broker and base image digest
└── Makefile
```

## Development

```sh
make help        # all targets
make ci          # validate, lint, build, smoke, cross-stack self-test, document schemas
make validate    # fixtures, versions.yaml and RELEASE.md
make interop-brokers   # the cross-stack self-test on every broker
make buildx      # check the other architecture locally, under emulation (CI builds both natively)
make upstream    # pin vs latest release of every adapter library and broker
make candidate-srad CANDIDATE=0.6.0   # self-test an adapter on another upstream version (pins untouched)
make candidate-nanomq CANDIDATE=emqx/nanomq:0.25.8   # the pinned adapters on another broker release
```

The **Upstream** workflow (Actions → Upstream → Run workflow; also daily)
resolves the latest release of Eclipse Tahu, pysparkplug, SparkplugNet, srad,
the Tahu sparkplug-client, sparkplughost and the four brokers, and the digest
of every base image. It self-tests every release that is newer than its pin
(once per pin and release), reports pin versus latest in its job summary and
keeps the issue "Upstream releases: pins behind the latest" open while a pin
is behind. Its `force` input self-tests even when the pin is the latest;
`only` limits it to some names (and leaves the issue alone).

`make smoke ADAPTERS=srad` and `make interop ADAPTERS="tahu srad"` narrow what
is run. See [CONTRIBUTING.md](CONTRIBUTING.md) for adding an adapter, a
fixture or a call operation.

## Security

The brokers accept anonymous clients and the edge nodes apply writes from
anyone who can publish to them. They are test fixtures: the self-tests put
them on a private Docker network, and they must never be attached to a plant
network. See [SECURITY.md](SECURITY.md).

## License

The repository's code and documentation, the adapters included, are
MIT-licensed. The images also contain the upstream stacks and their
dependencies, each under its own licence (EPL-2.0, Apache-2.0, MIT and
others). See [LICENSE](LICENSE) and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
