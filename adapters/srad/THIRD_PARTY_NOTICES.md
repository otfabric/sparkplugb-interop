# Third-party notices: sparkplugb-interop-srad

This image contains the sparkplugb-interop adapter (MIT, see
`/licenses/sparkplugb-interop/LICENSE`), compiled into
`/usr/local/bin/sparkplugb-interop`, and the following third-party
software, each under its own licence.

## srad

- Upstream: https://github.com/dpazj/srad
- Version: 0.5.0 (with srad-eon 0.5.0, srad-app, srad-client,
  srad-client-rumqtt, srad-types and srad-macros 0.3.1), from crates.io with
  the checksums of `adapters/srad/Cargo.lock`
- Licence: MIT OR Apache-2.0, `/licenses/srad/` (the crates ship no licence
  files; these are the repository's at tag `srad-v0.5.0`, checked by
  checksum)
- Statically linked, unmodified. It includes the Sparkplug B protobuf
  definitions as srad generates and distributes them.

## Rust crates

The dependencies of srad and of the adapter (rumqttc, tokio, prost,
serde_json and what they need), from crates.io with the checksums of
`adapters/srad/Cargo.lock`, statically linked. Their names, versions and
licence expressions are in `/licenses/BUNDLED-MODULES.txt` and their licence
files in `/licenses/<crate>-<version>/`. rumqttc 0.24.0 (Apache-2.0) ships
none; `/licenses/rumqttc-0.24.0/LICENSE` is its repository's at tag
`rumqttc-0.24.0`.

The Rust standard library (MIT OR Apache-2.0), from the pinned
`rust:1.95-slim-trixie` build image, is linked as well.

## Debian

The base image `debian:trixie-slim` (pinned by digest) carries Debian
packages under their own licences, listed in `/usr/share/doc/*/copyright`.
