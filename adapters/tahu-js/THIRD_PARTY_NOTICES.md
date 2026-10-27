# Third-party notices: sparkplugb-interop-tahu-js

This image contains the sparkplugb-interop adapter (MIT, see
`/licenses/sparkplugb-interop/LICENSE`) and the following third-party
software, each under its own licence.

## sparkplug-client (Eclipse Tahu)

- Upstream: https://github.com/eclipse-tahu/tahu, `javascript/core/sparkplug-client`
  (Cirrus Link Solutions and the Eclipse Tahu contributors)
- Version: 3.2.4, from npm with the integrity hash in
  `adapters/tahu-js/package-lock.json`
- Licence: EPL-2.0, `/licenses/sparkplug-client@3.2.4/`
- Installed unmodified in `/opt/sparkplugb-interop/node_modules`.

## sparkplug-payload (Eclipse Tahu)

- Upstream: https://github.com/eclipse-tahu/tahu, `javascript/core/sparkplug-payload`
- Version: 1.0.3, from npm with the integrity hash in
  `adapters/tahu-js/package-lock.json`
- Licence: EPL-2.0. The npm package ships no licence file;
  `/licenses/sparkplug-payload@1.0.3/` holds the EPL-2.0 text of
  sparkplug-client, published by the same project, and a `SOURCE.txt` saying so.
- Installed unmodified. It includes the Sparkplug B protobuf definitions.

## npm packages

The dependencies of sparkplug-client (MQTT.js, protobuf.js, Long, pako,
debug and what they need, under MIT, BSD-3-Clause, Apache-2.0, ISC and
similar licences), installed from npm with the integrity hashes of
`adapters/tahu-js/package-lock.json` and without install scripts. Their
names, versions and declared licences are in `/licenses/BUNDLED-MODULES.txt`
and their licence files in `/licenses/<name>@<version>/`.

## Node.js and Debian

The base image `node:22-trixie-slim` (pinned by digest) carries Node.js (MIT, with the licences of its bundled
components in `/usr/local/LICENSE`) and Debian
packages under their own licences, listed in `/usr/share/doc/*/copyright`.
npm and corepack are removed from the runtime image.
