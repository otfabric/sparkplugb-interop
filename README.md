# sparkplugb-interop

Docker-based interoperability test infrastructure for Sparkplug B 3.0.0
implementations.

This repository provides independent MQTT brokers, reference Sparkplug
implementations, and shared fixtures for black-box testing of any Sparkplug B
library — including [go-sparkplugb](https://github.com/otfabric/go-sparkplugb).

## Architecture

```
sparkplugb-interop
├── brokers/          MQTT broker configurations
│   ├── mosquitto/    Eclipse Mosquitto (MQTT 3.1.1 + 5.0)
│   ├── hivemq-ce/    HiveMQ Community Edition
│   ├── emqx/         EMQX
│   └── nanomq/       NanoMQ
├── adapters/         Independent Sparkplug implementations
│   └── tahu/         Eclipse Tahu (Java reference)
├── fixtures/         Shared test payloads and scenarios
└── compose.yaml      Docker Compose orchestration
```

## Quick start

```bash
# Start Mosquitto broker
docker compose up -d mosquitto

# Start all brokers
docker compose up -d

# Run reference-to-reference self-test
docker compose --profile test up --abort-on-container-exit
```

## Brokers

| Broker    | MQTT 3.1.1 | MQTT 5.0 | Port  |
|-----------|:----------:|:--------:|-------|
| Mosquitto | ✓          | ✓        | 1883  |
| HiveMQ CE | ✓          | ✓        | 1884  |
| EMQX      | ✓          | ✓        | 1885  |
| NanoMQ    | ✓          | ✓        | 1886  |

All brokers are configured for anonymous access on loopback only. These are
test brokers and must not be exposed to any other network.

## Adapters

Each adapter wraps an independent Sparkplug B implementation in a container
with a standardised interface. Adapters never import `go-sparkplugb`.

| Adapter      | Language | Roles       | Source |
|-------------|----------|-------------|--------|
| Eclipse Tahu | Java     | Edge + Host | [eclipse/tahu](https://github.com/eclipse/tahu) |

## Fixtures

The `fixtures/` directory contains:

- Protobuf-encoded reference payloads for each message type
- JSON scenario definitions for lifecycle tests
- Golden outputs for decode verification

## License

MIT — see [LICENSE](LICENSE).
