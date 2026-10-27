# Contributing to sparkplugb-interop

## Ground rules

- **Nothing here may know about a library under test.** No imports, no build steps, no expectations, no names from `go-sparkplugb` or any other consumer. See [REQUIREMENTS.md](REQUIREMENTS.md) R1; CI greps for it.
- **The contract is the product.** Adapters differ inside; their commands, events, documents and exit codes are identical. A change to [docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md) or [docs/FIXTURES.md](docs/FIXTURES.md) lands in every published adapter in the same change.
- **Declare only what is tested.** A capability is `true` when the self-tests exercise it for that adapter, and only when the upstream library does it itself.
- **Pin everything.** Upstreams by exact version with a lock file, base images and brokers by digest, all mirrored in [versions.yaml](versions.yaml).

## Setup

Docker (with buildx), `jq`, GNU make and Python 3. Nothing else: the JDK, Maven, .NET, Rust, Node and Go run inside the image builds.

```sh
make ci     # validate, lint, build, smoke, cross-stack self-test, document schemas
```

Run `make` for all targets.

## Changing an adapter

1. Edit under `adapters/<adapter>/`.
2. `make image-<adapter> smoke-<adapter>`.
3. `make interop`, and `make interop-brokers` when the change touches the MQTT session: every pairing must still pass. If two stacks disagree, find out which one is right by the specification before touching the expectation.
4. If behaviour visible to consumers changed, update the contract or fixture documentation in the same change.

## Moving an upstream pin

1. Change the version in the Dockerfile (`ARG <NAME>_VERSION`), regenerate the lock file inside Docker (`make lock-pysparkplug` is the model) and change `versions.yaml`; `scripts/check-versions.sh` must pass.
2. `make ci`.
3. Record the new version, and anything it changed, in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md), [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and [RELEASE.md](RELEASE.md).

The rule is the latest upstream release, also when it has a bug: list the bug in [tests/known-bugs.tsv](tests/known-bugs.tsv) for that exact version (the matching steps then count as known bugs, not failures) and describe it in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md), rather than pinning an older release. When a run reports that a known bug no longer reproduces, remove its entry: a stale entry fails the run.

## Moving a broker pin

Change the `image:` line of the broker in `versions.yaml` (tag and multi-arch index digest), then `make interop-brokers`. `scripts/lib.sh` reads the pins from there; nothing else names them.

## Registries and retries

CI does not pull from Docker Hub: its anonymous rate limit and its token service fail too often on shared runners. The workflows set `BASE_REGISTRY=mirror.gcr.io/library` (base images), `BROKER_REGISTRY=mirror.gcr.io` (brokers) and `TOOLS_REGISTRY=mirror.gcr.io` (BuildKit, QEMU handlers, shellcheck, Python). All are pinned by digest or are tooling, so the mirror cannot change what is built or tested. Locally they default to Docker Hub.

Every command that talks to a registry runs through `scripts/retry.sh`, which repeats it with a growing delay when the output shows a rate limit, a 5xx or a timeout, and returns any other failure at once.

Both architectures are built and self-tested natively, on an amd64 and an arm64 runner; nothing in CI runs under emulation. `make buildx-setup` and `make buildx` remain for checking the other architecture from one machine.

## Adding an operation, an event or a behaviour

1. Specify it first in the contract: the operation, its payload, when it succeeds and its result fields, or the event and its fields.
2. Implement it in every published adapter whose upstream can do it; the others declare the feature `false`.
3. Add a step to `scripts/interop.sh` that computes its expectation from the fixture, and a check to `scripts/smoke.sh` if it has a contract-level edge case.
4. Add the feature to `schemas/capabilities.schema.json`, the capability documents and [docs/CAPABILITIES.md](docs/CAPABILITIES.md).
5. Extend `schemas/event.schema.json` or `schemas/call-result.schema.json` if documents gained fields.

Additive changes bump the contract's minor version; anything a consumer could break on bumps the major version.

## Adding a fixture

See [docs/FIXTURES.md](docs/FIXTURES.md).

## Adding an adapter

1. Evaluate the upstream: does it build from a pinned source on both architectures, which roles does it provide, what is its licence, and is its Sparkplug engine its own? Write the answers into [PLAN.md](PLAN.md) before writing code.
2. Create `adapters/<name>/` with a Dockerfile built from the repository root, following the existing ones: pinned build stage, minimal runtime stage, non-root user (uid 10001), `/fixtures`, `/licenses` with a `THIRD_PARTY_NOTICES.md`, the readiness file, the health check, `ENTRYPOINT ["sparkplugb-interop"]`.
3. Implement the contract with the upstream's own APIs. A role the upstream does not have exits 5 and `roles` says `supported: false`. What the upstream cannot do within a role is a feature declared `false`: never fake a behaviour the library does not have.
4. `scripts/smoke.sh <name>` must pass, then `scripts/interop.sh <all adapters> <name>`.
5. Add it to `ADAPTERS` in the Makefile, set its `status: published` in `versions.yaml` with its base image digests, and add it to the notices and the documentation tables.

Do not build a shared adapter framework. Six adapters in six languages share a contract, not code.

## Style

- Shell: `bash`, `set -uo pipefail`, shellcheck-clean (`make lint`).
- Documentation states what was verified. If something is assumed or untested, say so.

## License

By contributing you agree that your contributions are licensed under the MIT licence of the repository.
