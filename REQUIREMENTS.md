# Requirements

What this repository must do and must not do. [PLAN.md](PLAN.md) says in
which order; [docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md) and
[docs/FIXTURES.md](docs/FIXTURES.md) are the specifications that follow from
it.

## Purpose

Independent, reproducible interoperability validation for Sparkplug B 3.0.0
libraries: established third-party edge nodes and host applications, packaged
as containers that all behave the same way, and pinned MQTT brokers they meet
on, so that a library can be tested against real implementation diversity
without owning any of it.

It is not another Sparkplug implementation, not an MQTT broker test suite and
not the Eclipse Sparkplug TCK.

## R1 - Repository isolation

- R1.1 This repository does not import, build, link against or depend on any
  library under test, and contains no test logic or expectation specific to
  one. In particular it knows nothing about `otfabric/go-sparkplugb`.
- R1.2 A consumer depends on published images, pinned broker references and
  the documented contract only. It does not need a checkout of this
  repository to run its tests.
- R1.3 No third-party code from here is copied into a consumer, and the
  contract does not shape a consumer's public API.

| Responsibility | Here | Consumer |
|----------------|:----:|:--------:|
| Third-party implementations, adapters, Dockerfiles | yes | no |
| Broker pins | yes | pins the same references |
| Fixtures, fixture schema and validation | yes | no |
| Container contract, capability declarations | yes | no |
| Image builds and publishing, licence compliance | yes | no |
| Self-tests of the reference stacks | yes | no |
| Scenarios and assertions about a library | no | yes |
| Compatibility claims and release qualification of a library | no | yes |

## R2 - Reference implementations

- R2.1 These are in scope, in this order of priority:

  | Priority | Implementation | Language | Roles | Independent engine |
  |----------|----------------|----------|-------|:------------------:|
  | P0 | Eclipse Tahu (Java) | Java | edge, host | yes |
  | P1 | pysparkplug | Python | edge, host | yes |
  | P1 | SparkplugNet | C# | edge, host | yes |
  | P1 | srad | Rust | edge, host | yes |
  | P1 | Eclipse Tahu sparkplug-client | JavaScript | edge | yes |
  | P2 | sparkplughost | Go | host | yes |

- R2.2 Each is its own image. No image contains two implementations, and no
  image contains a broker.
- R2.3 An adapter that wraps another adapter's Sparkplug engine says so
  (`upstream.independentEngine: false`) and does not count as an additional
  independent implementation.
- R2.4 An adapter is published only when it builds from pinned sources and
  passes the smoke test and the cross-stack self-test for the roles it
  declares. An upstream that cannot be made to do so is documented and left
  out; it does not block the others.

## R3 - Container contract

- R3.1 Every image has the same entry points: `edge`, `host`, `call`,
  `print-capabilities`, `print-fixture`, `version`, with the same flags and
  semantics.
- R3.2 Roles signal readiness when their births or STATE are actually
  published, by a file and an event. Nothing relies on delays.
- R3.3 Machine-readable output is JSON on stdout with stable field names;
  diagnostics go to stderr; exit codes have fixed meanings.
- R3.4 Every operation is bounded by a configurable timeout.
- R3.5 Roles stop cleanly on SIGTERM (an edge with its NDEATH, a host with
  STATE offline) and exit non-zero when they cannot start.
- R3.6 Capabilities are machine-readable and declared only for behaviour the
  self-tests exercise.
- R3.7 Hosts report what arrived in one normalized form: metric names, never
  aliases; Sparkplug datatype names; `bdSeq` apart from the metrics.

## R4 - Fixtures

- R4.1 Fixtures describe the simulated edge node, its devices and their
  metrics independent of any implementation: names, aliases, datatypes,
  values, writability, and the updates it publishes.
- R4.2 Fixtures contain no assertions, no vendor callback names and no names
  from any library under test.
- R4.3 Dynamic behaviour is deterministic and triggered by calls, not by
  timers: an edge publishes data only on `publish-data`.
- R4.4 Fixtures are validated against a schema, in CI and by each adapter at
  start.
- R4.5 A datatype an adapter cannot serve is not silently replaced by
  another; it does not declare the fixture.

## R5 - Reproducibility

- R5.1 Every upstream is pinned to an exact version with a lock file, every
  base image and broker to a digest. `versions.yaml` records all of them and
  is checked against the build files.
- R5.2 Releases are immutable tags; images are published with their digests.
  Nothing a release depends on floats.
- R5.3 Images are published for linux/amd64 and linux/arm64. An adapter that
  cannot support one documents it.

## R6 - Self-tests

- R6.1 The reference stacks are tested against each other, every host
  against every edge, on every fixture they share, on every pinned broker;
  the expectations are computed from the fixture, not from another stack.
- R6.2 These results are about the references and are never presented as
  results for a consumer.
- R6.3 A verified upstream defect is recorded in `tests/known-bugs.tsv` for
  the exact upstream version, reported as known rather than failed, and an
  entry that no longer fails fails the run.
- R6.4 Malformed payloads, broken protobuf and broker fault injection are out
  of scope: reference implementations are not misused as fault injectors.
  That testing belongs in a library's own unit, integration and fuzz tests.

## R7 - Licensing and safety

- R7.1 Licence notices of all bundled software are preserved in the images
  under `/licenses`.
- R7.2 Brokers with a licence that does not allow redistribution (EMQX,
  BSL-1.1) are only run, never bundled.
- R7.3 Test services are reachable on private Docker networks only by
  default; nothing publishes a port.

## R8 - Scope discipline

- R8.1 Adapters are small and explicit. No generic interoperability
  framework, no protocol-independent abstraction without a concrete need.
