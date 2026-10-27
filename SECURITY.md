# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `sparkplugb-interop`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email **security@otfabric.io**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/sparkplugb-interop/security) tab → **Advisories** → **Report a vulnerability**), with a description, steps to reproduce and the affected image tag or digest.

Vulnerabilities in the bundled implementations themselves (Eclipse Tahu, pysparkplug, SparkplugNet, srad, sparkplug-client, sparkplughost and their dependencies) or in the brokers belong upstream; report them there, and tell us so that the pin can be moved.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No; pull the latest images |

## Security Considerations

### These are test fixtures, not products

The images run **unauthenticated, unencrypted Sparkplug B edge nodes and host applications** against anonymous MQTT brokers. An edge applies any NCMD or DCMD that names a writable metric; a host sends commands to whatever node it is told to. That is their purpose.

- **Never connect an adapter to a production broker**: a host publishes STATE and commands on the real namespace, an edge publishes births and data that real hosts act on
- The self-tests use private Docker networks and publish no port. Keep it that way when you run the images yourself
- There is no TLS and no MQTT authentication; see the gaps in [docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md)

### Image hardening

- Processes run as a non-root user (uid 10001)
- Base images are pinned by digest; upstream libraries by exact version with lock files and hashes ([versions.yaml](versions.yaml))
- The runtime images contain the adapter, its runtime and the fixtures; build tools stay in the build stages
- The images are rebuilt, not patched: a base image or upstream fix arrives with a new release

### Supply chain

- Upstream libraries are fetched during the image build from their package registries (Maven Central, PyPI, NuGet, crates.io, npm, the Go module proxy) at pinned versions, verified by the lock file of each adapter
- Base images and brokers are pinned by digest. CI and the release workflow pull them from `mirror.gcr.io`, a mirror of Docker Hub, because anonymous Docker Hub pulls are rate limited; the digest makes the source irrelevant to the content (`BASE_REGISTRY`, `BROKER_REGISTRY`). The build tooling images (BuildKit, QEMU handlers, shellcheck) come from the same mirror by tag (`TOOLS_REGISTRY`)
- Consumers should pin images and brokers by digest, taken from the `manifest.json` of a release
