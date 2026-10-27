# Plan

In which order [REQUIREMENTS.md](REQUIREMENTS.md) is met. Each phase ends
with everything green in CI on both architectures.

## Phase 1 - Foundation (done)

- Container contract, fixture format and schema, capability schema.
- `baseline` and `scalars` fixtures.
- Smoke and cross-stack self-tests that compute expectations from the
  fixture; known-bug list with stale-entry detection.
- Brokers pinned by digest; the test network publishes no port.

## Phase 2 - First adapter (done)

- pysparkplug (edge and host): the contract proven end to end, and the first
  upstream defect found (NDEATH `bdSeq` after `disconnect()`).

## Phase 3 - Reference adapters (done)

- Eclipse Tahu (Java), SparkplugNet (C#), srad (Rust), Eclipse Tahu
  sparkplug-client (JavaScript, edge), sparkplughost (Go, host).
- Every host against every edge on every fixture they share, on Mosquitto,
  HiveMQ CE, EMQX and NanoMQ.

## Phase 4 - First release (v0.1.0)

- Multi-arch images on GHCR with `manifest.json`; consumers pin by digest.

## Phase 5 - Expanded coverage (open)

- Fixtures with templates, DataSets, arrays and metric properties.
- A role for applications that are not the primary host.
- Reordering by the host (the reorder timeout) without timing dependence.
- MQTT 5 and TLS once a consumer needs them.
- An upstream-candidate workflow, as in the sibling interop repositories.

## Evaluated and left out

- Eclipse Tahu C (`tahu/c`): a protobuf and payload library without an MQTT
  session, so it has neither role on its own.
- Node-RED Sparkplug nodes and other products: they wrap the libraries above.
