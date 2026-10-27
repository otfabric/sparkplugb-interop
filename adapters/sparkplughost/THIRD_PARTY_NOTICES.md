# Third-party notices: sparkplugb-interop-sparkplughost

This image contains the sparkplugb-interop adapter (MIT, see
`/licenses/sparkplugb-interop/LICENSE`) and the following third-party
software, each under its own licence.

## sparkplughost

- Upstream: https://github.com/EvergenEnergy/sparkplughost (Evergen)
- Version: v0.4.0, Go module `github.com/EvergenEnergy/sparkplughost`, from
  the Go module proxy with the hashes in `adapters/sparkplughost/go.sum`
- Licence: Apache-2.0, `/licenses/github.com/EvergenEnergy/sparkplughost/`
- Compiled into `/usr/local/bin/sparkplugb-interop`. No upstream file is
  changed. The build adds one file of this repository,
  `adapters/sparkplughost/overlay/zz_interop.go` (MIT), to the package: it
  exposes, read-only, the messages and the edge nodes the host application
  keeps private, and starts and stops it as `Run` does. sparkplughost
  includes the Sparkplug B protobuf definitions as it generates and
  distributes them.

## Go modules

The dependencies of sparkplughost, compiled into the same binary:
Eclipse Paho MQTT Go client (EPL-2.0 OR EDL-1.0), Go protobuf
(BSD-3-Clause), gorilla/websocket (BSD-2-Clause), golang.org/x/net and
golang.org/x/sync (BSD-3-Clause). Their paths and versions are in
`/licenses/BUNDLED-MODULES.txt` and their licence files in
`/licenses/<module path>/`.

## Go and Debian

The binary is built with Go (BSD-3-Clause) from the image
`golang:1.25.14-trixie`, and runs on `debian:trixie-slim` (both pinned by
digest), whose Debian packages are under their own licences, listed in
`/usr/share/doc/*/copyright`.
