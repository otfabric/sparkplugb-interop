# AGENTS.md — sparkplugb-interop

## What this is

Independent Sparkplug B 3.0.0 edge node and host application implementations (Eclipse Tahu, pysparkplug, SparkplugNet, srad, sparkplughost) in containers behind one command-line contract, with deterministic fixtures, tested across MQTT brokers and used as oracles by go-sparkplugb and sparkplugbctl. Nothing here depends on go-sparkplugb. Profile: **interoperability**.

## Organisation standards

This repository follows **otfabric/engineering policy 1.2**, profile **interoperability** (`.otfabric/policy.yaml`, mode `enforce`). Where this file and the organisation standards disagree, this file wins here — say so in your report.

## Where the truth lives

| Question | Source |
|---|---|
| Container commands, documents, exit codes | docs/CONTAINER_CONTRACT.md (`contract_version` in versions.yaml) |
| What each stack can do | docs/CAPABILITIES.md |
| Pins (upstream revision, source hash, base image digests) | versions.yaml, checked by `make validate` |
| Fixtures | fixtures/, docs/FIXTURES.md |
| Expected deviations of the stacks | tests/known-bugs.tsv |
| Results | docs/COMPATIBILITY.md |
| What this repository must and must not do | REQUIREMENTS.md |
| What is done and what is next | PLAN.md |

## Hard rules

1. Nothing floats: upstream sources and base images stay pinned, and versions.yaml mirrors the pins. `make validate` must pass.
2. Skips and known differences are never counted as passes.
3. An adapter answers an unsupported operation with the contract's unsupported error, never with a fake success.
4. Contract changes bump `contract_version` and reach every adapter and docs/CONTAINER_CONTRACT.md in the same change.
5. Licences: every redistributed component in THIRD_PARTY_NOTICES.md; images whose licence forbids redistribution are not published.
6. The release tag equals `interop_version`; bump versions.yaml before tagging.

## Commands

| When you change… | Run |
|---|---|
| anything | `make validate lint` |
| an adapter | `make image-<adapter> smoke-<adapter>` |
| cross-stack behaviour | `make interop` |
| other MQTT brokers | `make interop-brokers` |
| documents or the contract | `make documents` |

## Git and releases

Branches and PRs. Do not push to `main`, push tags or publish images unless the maintainer asks in the session.
