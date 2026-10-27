# Third-party notices: sparkplugb-interop-tahu

This image contains the sparkplugb-interop adapter (MIT, see
`/licenses/sparkplugb-interop/LICENSE`) and the following third-party
software, each under its own licence.

## Eclipse Tahu

- Upstream: https://github.com/eclipse-tahu/tahu (Eclipse Foundation, Cirrus Link Solutions and others)
- Version: 1.0.21, the artifacts `org.eclipse.tahu:tahu-core`, `tahu-edge`
  and `tahu-host` from Maven Central, with the SHA-256 of
  `adapters/tahu/dependencies.sha256`
- Licence: EPL-2.0, https://www.eclipse.org/legal/epl-2.0/
- Used unmodified in `/opt/sparkplugb-interop/lib`. It includes the Sparkplug
  B protobuf definitions as Tahu generates and distributes them. Its source
  is available from the upstream repository at the tag `v1.0.21` and as the
  `-sources.jar` of each artifact on Maven Central.

## Java libraries

The dependencies of Tahu (Eclipse Paho MQTT client under EPL-2.0 OR
EDL-1.0, protobuf-java under BSD-3-Clause, Jackson, Apache Commons, SLF4J and
Logback, victools jsonschema-generator, classmate, all under their own
licences), resolved from Maven Central and pinned by the SHA-256 of
`adapters/tahu/dependencies.sha256`. Their names and versions are in
`/licenses/BUNDLED-MODULES.txt` and the licence files they carry in
`/licenses/<name>/`.

## Java runtime and Ubuntu

The base image `eclipse-temurin:21-jre-noble` (pinned by digest) carries the
Eclipse Temurin OpenJDK runtime (GPL-2.0 with the Classpath Exception, see
`/opt/java/openjdk/legal/`) and Ubuntu packages under their own licences,
listed in `/usr/share/doc/*/copyright`.
