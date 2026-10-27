# Third-party notices: sparkplugb-interop-pysparkplug

This image contains the sparkplugb-interop adapter (MIT, see
`/licenses/sparkplugb-interop/LICENSE`) and the following third-party
software, each under its own licence.

## pysparkplug

- Upstream: https://github.com/matteosox/pysparkplug (Matt Fay)
- Version: 0.6.1, from PyPI with the hash in
  `adapters/pysparkplug/requirements.txt`
- Licence: Apache-2.0, `/licenses/pysparkplug/`
- Installed unmodified in `/opt/venv`. It includes the Sparkplug B protobuf
  definitions as pysparkplug generates and distributes them.

## Python packages

The dependencies of pysparkplug (paho-mqtt under EPL-2.0 OR EDL-1.0, protobuf
under BSD-3-Clause, and what they need), installed from PyPI with the hashes
of `adapters/pysparkplug/requirements.txt`. Their names and versions are in
`/licenses/BUNDLED-MODULES.txt` and their licence files in `/licenses/<name>/`.

## Python and Debian

The base image `python:3.13-slim-trixie` (pinned by digest) carries CPython
(PSF licence) and Debian packages under their own licences, listed in
`/usr/share/doc/*/copyright`.
