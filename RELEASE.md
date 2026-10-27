# sparkplugb-interop Releases

## How a release is made

1. Write the notes of the version below and set `interop_version` in `versions.yaml` to it, in the same change. `make validate` (and CI) fails when RELEASE.md has no notes for `interop_version`.
2. `make ci` on the commit to release.
3. Tag `vX.Y.Z` and push the tag. The release workflow runs one job on an amd64 runner and one on an arm64 runner: each validates (including the notes of the tag), builds every image natively with `ADAPTER_VERSION` set to the tag, runs the smoke and cross-stack self-tests on those images and pushes exactly them to GHCR as `<image>:vX.Y.Z-amd64` and `-arm64`. A last job joins the two into the multi-arch `<image>:vX.Y.Z` and creates the GitHub release with `manifest.json` (upstream versions, the digests of the multi-arch images and the pinned brokers) and `checksums.txt`.
4. Consumers pin the digests from `manifest.json`.

The notes are the body of the GitHub release, followed by the image digests:
everything after the heading `## vX.Y.Z` up to the next line that is exactly
`---` (`scripts/release-notes.sh vX.Y.Z` prints it). This is the format of
every otfabric repository, so: one section per version, newest first, each
ending with a `---` line, and no `---` line inside a section.

Tags are immutable. A fix is a new version.

| What changed | Version |
|--------------|---------|
| Contract or fixture format changed incompatibly | major |
| New operation, event, fixture, adapter or document field | minor |
| Upstream pin, broker pin, base image or bug fix with unchanged contract | patch |

---

## v0.1.0

**Date:** 2026-10-27

## Summary

First release: Sparkplug B 3.0.0 edge nodes and host applications of
independent implementations as multi-arch images with one container
contract, two fixtures, and pinned MQTT brokers to run them on.

## Changes

### Added

- Container contract 0.1 ([docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md)): `edge`, `host`, `call` over a control socket, `print-capabilities`, `print-fixture`, `version`; JSON Lines events, call result documents, fixed exit codes.
- Fixtures `baseline` (node with two devices, writable metrics, two updates) and `scalars` (all 14 scalar datatypes at their limits), with a JSON Schema and a validator for the cross-field rules.
- Adapter images: Eclipse Tahu 1.0.21 (Java, edge and host), pysparkplug 0.6.1 (Python, edge and host), SparkplugNet 1.4.1 (C#, edge and host), srad 0.5.0 (Rust, edge and host), Eclipse Tahu sparkplug-client 3.2.4 (JavaScript, edge) and sparkplughost v0.4.0 (Go, host). See [README.md](README.md).
- Broker pins: Eclipse Mosquitto, HiveMQ CE, EMQX and NanoMQ, by multi-arch digest in `versions.yaml` and `manifest.json`.
- Self-tests: `scripts/smoke.sh` (contract of one image) and `scripts/interop.sh` (every host against every edge, every fixture, any broker), with expectations computed from the fixture, and `tests/known-bugs.tsv` for verified upstream defects.
- Schemas for capability documents, call results and events; `make documents` validates every document the self-tests produce.
- CI natively on amd64 and arm64, the cross-stack self-test on every broker, and a tag-driven release to GHCR.
- Upstream candidates workflow, weekly and on demand: the latest release of every adapter library and broker against its pin, with a self-test of each newer one and a decision table (`make upstream`, `make candidate-<name>`).

### Upstream notes

The self-test passes on all four brokers with 0 failures. Verified upstream defects are listed in [tests/known-bugs.tsv](tests/known-bugs.tsv) and described in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

### Not in this release

- Templates, DataSets, arrays and properties in fixtures.
- A choice of MQTT version (every adapter speaks MQTT 3.1.1 except srad, which speaks MQTT 5 only), TLS and broker authentication.
---
