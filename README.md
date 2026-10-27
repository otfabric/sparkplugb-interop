# sparkplugb-interop

[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)](https://go.dev) [![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE) [![CI](https://github.com/otfabric/sparkplugb-interop/actions/workflows/ci.yaml/badge.svg)](https://github.com/otfabric/sparkplugb-interop/actions/workflows/ci.yaml)

Docker-based interoperability and chaos test infrastructure for
[Sparkplug B 3.0.0](https://www.eclipse.org/tahu/spec/sparkplug_spec.pdf)
implementations. Tests any Sparkplug B library against a matrix of four MQTT
brokers with end-to-end lifecycle tests and resilience scenarios. Built on
[go-sparkplugb](https://github.com/otfabric/go-sparkplugb).

---

## Quick start

```bash
# Start the full broker matrix
docker compose up -d

# Wait for all brokers to accept connections
go run ./cmd/wait-brokers

# Run E2E and chaos tests against every broker
go test -tags e2e ./tests/... -v -timeout=120s

# Or use the Makefile (starts brokers, waits, tests, tears down)
make e2e
```

## Broker matrix

| Broker         | Version   | MQTT 3.1.1 | MQTT 5.0 | Port | Image                                      |
|----------------|-----------|:----------:|:--------:|------|---------------------------------------------|
| Mosquitto      | 2.0.21    | ✓          | ✓        | 1883 | `eclipse-mosquitto:2.0.21`                  |
| HiveMQ CE      | 2024.12   | ✓          | ✓        | 1884 | `hivemq/hivemq-ce:2024.12`                  |
| EMQX           | 5.8.6     | ✓          | ✓        | 1885 | `emqx/emqx:5.8.6`                          |
| NanoMQ         | 0.21      | ✓          | ✓        | 1886 | `emqx/nanomq:0.21`                         |

All brokers are configured for anonymous access on loopback only. These are
test brokers and must not be exposed to any other network.

## Architecture

```
sparkplugb-interop
├── brokers/              MQTT broker configurations
│   ├── mosquitto/        Eclipse Mosquitto 2.0 (port 1883)
│   ├── hivemq-ce/        HiveMQ Community Edition (port 1884)
│   ├── emqx/             EMQX 5.8 (port 1885)
│   └── nanomq/           NanoMQ 0.21 (port 1886)
├── harness/              Go test harness (broker config, wait helpers)
├── tests/                Test suites
│   ├── e2e_test.go       End-to-end lifecycle tests
│   └── chaos_test.go     Chaos and resilience tests
├── cmd/wait-brokers/     Broker readiness CLI
├── fixtures/             Reference payloads and scenario definitions
├── adapters/             Independent Sparkplug implementations
│   └── tahu/             Eclipse Tahu (Java reference)
├── compose.yaml          Docker Compose orchestration
├── versions.yaml         Pinned broker and adapter versions
└── Makefile              Build and test targets
```

## Test suites

Every test runs against all four brokers via `t.Run(broker.Name, ...)`.

### End-to-end (`tests/e2e_test.go`)

| Test               | Description                                                  |
|--------------------|--------------------------------------------------------------|
| EdgeLifecycle      | Connect, NBIRTH, DBIRTH, NDATA, DDATA, graceful disconnect   |
| HostDiscovery      | Host sees NBIRTH/DBIRTH, tracks node/device state            |
| AliasRoundTrip     | Metric aliases in NBIRTH resolve correctly in NDATA          |
| RebirthCycle       | Host detects seq gap, sends rebirth command, node re-births  |

### Chaos (`tests/chaos_test.go`)

| Test                 | Description                                                |
|----------------------|------------------------------------------------------------|
| RapidReconnect       | 50 connect/disconnect cycles in quick succession           |
| MalformedPayload     | Garbage bytes on Sparkplug topics — host must not crash    |
| ConcurrentEdgeNodes  | 10 edge nodes publishing simultaneously                    |
| ConnectionDrop       | Abrupt TCP close mid-session, verify clean recovery        |

## Running tests

### Prerequisites

- **Docker** and **Docker Compose** (v2)
- **Go 1.27+**

### Full workflow (recommended)

```bash
make e2e
```

This starts all brokers, waits for readiness, runs the E2E and chaos suites,
and tears down the containers.

### Manual steps

```bash
# 1. Start brokers
docker compose up -d

# 2. Wait for all four brokers to accept TCP connections
go run ./cmd/wait-brokers

# 3. Run E2E tests
go test -tags e2e ./tests/... -v -timeout=120s

# 4. Run unit tests (harness only, no brokers needed)
go test ./harness/... -v

# 5. Tear down
docker compose down
```

## Adapters

Each adapter wraps an independent Sparkplug B implementation in a container
with a standardised interface. Adapters never import `go-sparkplugb`.

| Adapter      | Language | Roles       | Source                                              |
|-------------|----------|-------------|------------------------------------------------------|
| Eclipse Tahu | Java     | Edge + Host | [eclipse/tahu](https://github.com/eclipse/tahu)     |

## Fixtures

The `fixtures/` directory contains:

- **Protobuf-encoded reference payloads** for each Sparkplug B message type
  (NBIRTH, NDEATH, DBIRTH, DDEATH, NDATA, DDATA)
- **JSON scenario definitions** for lifecycle tests
  (`edge-lifecycle.json`, `host-lifecycle.json`, `alias-roundtrip.json`,
  `rebirth-cycle.json`)
- **Golden outputs** for decode verification

## Makefile targets

| Target         | Description                                          |
|----------------|------------------------------------------------------|
| `make test`    | Run harness unit tests (no Docker needed)            |
| `make e2e`     | Full workflow: start brokers, wait, test, tear down  |
| `make lint`    | Run `go vet` on all packages                         |
| `make clean`   | Tear down brokers and remove coverage files          |

## License

MIT — see [LICENSE](LICENSE).
