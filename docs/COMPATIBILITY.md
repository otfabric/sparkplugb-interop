# Compatibility

Which upstream versions the adapters are built on, what the reference stacks
were verified to do against each other on each broker, and what was found on
the way.

Everything here is about the reference stacks. It makes no statement about
any library that consumes the images.

## Pinned upstreams

The rule is the latest upstream release, also when it has a bug: the bug is
listed in [tests/known-bugs.tsv](../tests/known-bugs.tsv) and described
below, and the steps it explains count as known bugs instead of failures.

| Adapter | Upstream | Pinned | Pin | Engine |
|---------|----------|--------|-----|--------|
| `tahu` | [Eclipse Tahu](https://github.com/eclipse-tahu/tahu) | 1.0.21 | Maven Central `org.eclipse.tahu:tahu-core`, `tahu-edge`, `tahu-host` 1.0.21, SHA-256 of every jar in `adapters/tahu/dependencies.sha256` | independent, Java |
| `pysparkplug` | [pysparkplug](https://github.com/matteosox/pysparkplug) | 0.6.1 | PyPI, hashed in `adapters/pysparkplug/requirements.txt` | independent, Python |
| `sparkplugnet` | [SparkplugNet](https://github.com/SeppPenner/SparkplugNet) | 1.4.1 | NuGet `[1.4.1]`, content hashes in `adapters/sparkplugnet/adapter/packages.lock.json` | independent, C# |
| `srad` | [srad](https://github.com/dpazj/srad) | 0.5.0 | crates.io, checksums in `adapters/srad/Cargo.lock` | independent, Rust |
| `tahu-js` | [Eclipse Tahu sparkplug-client](https://github.com/eclipse-tahu/tahu/tree/master/javascript) | 3.2.4 | npm, integrity hashes in `adapters/tahu-js/package-lock.json` | independent, JavaScript |
| `sparkplughost` | [EvergenEnergy/sparkplughost](https://github.com/EvergenEnergy/sparkplughost) | v0.4.0 | Go module, checksum in `adapters/sparkplughost/go.sum`, vendored | independent, Go |

| Broker | Pinned | Image |
|--------|--------|-------|
| Eclipse Mosquitto | 2.1.2 | `eclipse-mosquitto:2.1.2-alpine`, by digest |
| HiveMQ Community Edition | 2026.5 | `hivemq/hivemq-ce:2026.5`, by digest |
| EMQX | 6.3.2 | `emqx/emqx:6.3.2`, by digest |
| NanoMQ | 0.25.7 | `emqx/nanomq:0.25.7`, by digest |

The base images are pinned by multi-arch index digest. Every pin is in
[versions.yaml](../versions.yaml), and `scripts/check-versions.sh` checks that
it matches the Dockerfiles and the Makefile.

The **Upstream** workflow (`.github/workflows/upstream.yml`, daily and on
demand) resolves the latest release of each adapter's library (Maven
Central, PyPI, NuGet, crates.io, npm, the Go module proxy) and of each
broker (Docker Hub). For each one newer than its pin it builds the adapter on
it, its lock file regenerated in a copy of the checkout, and runs the smoke
and cross-stack self-tests against the pinned other adapters; a newer broker
runs the cross-stack self-test of the pinned adapters. The known bugs of the
pinned version apply to the candidate, so it fails only on what is new and
reports a known bug that is gone as stale. A candidate is tested once per pin
and release. It also checks the digest of every base image, writes a
pin-vs-latest table to its job summary, keeps the issue "Upstream releases:
pins behind the latest" open while a pin is behind, and never changes a
pin. `make candidate-<name> CANDIDATE=<version or image>` runs the same
locally.

## Cross-stack matrix

`make interop-brokers` runs every host against every edge node (including
itself), on both fixtures, on each broker. For each pairing it checks the
births, every update, the writes, rebirth on request and by call, device
death and birth, the host's `nodes` view, NDEATH on a clean stop, births
after a restart, the will after a crash and the primary host's STATE. A step
that needs a capability one side does not declare is skipped; a step that
fails because of a listed bug counts as a known bug.

| Host ↓ / Edge → | tahu | pysparkplug | sparkplugnet | srad | tahu-js |
|-----------------|:----:|:-----------:|:------------:|:----:|:-------:|
| **tahu** | pass | pass ² ³ | pass ⁵ | pass ⁷ | pass ⁶ |
| **pysparkplug** | pass ¹ | pass ² | pass ¹ ⁵ | pass ³ | pass ¹ |
| **sparkplugnet** | pass ⁴ ⁸ | pass ² ⁴ | pass ⁴ ⁵ | pass ³ ⁴ | pass ⁴ ⁸ |
| **srad** | pass ⁸ | pass ² ³ | pass ⁵ | pass | pass ⁶ ⁸ |
| **sparkplughost** | pass | pass ² | pass ⁵ | pass | pass |

"pass" is no failure; the footnotes are the known bugs of the pairing. On
NanoMQ and HiveMQ CE, the pysparkplug, srad and sparkplughost hosts also
have the intermittent
[reordering](#messages-of-one-edge-node-arrive-out-of-order) finding, and
srad edges on NanoMQ the
[late will](#nanomq-publishes-the-will-after-an-mqtt-5-disconnect).

¹ The pysparkplug host decodes [width-sized Int8 and Int16 only](#int8-and-int16-are-encoded-two-ways), a specification ambiguity (scalars).
² The pysparkplug edge [puts bdSeq + 1 in its NDEATH](#pysparkplug-ndeath-carries-the-next-bdseq).
³ [Metrics without a datatype](#metrics-without-a-datatype) in the commands of srad and Tahu and in srad's NDEATH, which pysparkplug and SparkplugNet cannot decode.
⁴ The SparkplugNet host [keeps dead nodes and devices online](#sparkplugnet-host-keeps-dead-nodes-online), decodes [sign-extended Int8 and Int16 only](#int8-and-int16-are-encoded-two-ways), a specification ambiguity, and [sets `seq` in commands](#commands-carry-no-seq).
⁵ The SparkplugNet edge [drops device writes](#sparkplugnet-edge-drops-device-writes).
⁶ tahu-js [cannot decode commands without a datatype](#metrics-without-a-datatype) (from srad and Tahu).
⁷ The Tahu host [rejects a DBIRTH that reuses aliases](#tahu-host-rejects-a-dbirth-that-reuses-aliases).
⁸ Tahu and tahu-js [encode UInt32 in `long_value`](#tahu-encodes-uint32-in-long_value) (scalars).

Verified on linux/arm64 with the pins above on 2026-10-27, six adapters on
each broker (72 pairings per broker, both fixtures):

| Broker | Passed | Failed | Known bugs | Skipped (capability) |
|--------|-------:|-------:|-----------:|---------------------:|
| Mosquitto 2.1.2 | 582 | 0 | 167 | 137 |
| HiveMQ CE 2026.5 | 574 | 0 | 175 | 137 |
| EMQX 6.3.2 | 582 | 0 | 167 | 137 |
| NanoMQ 0.25.7 | 549 | 0 | 200 | 137 |

The difference between brokers is the intermittent findings.

These counts predate the third update of the scalars fixture (v0.2.2), which adds
steps to each scalars pairing.

## Findings

### Int8 and Int16 are encoded two ways

**Classification: specification ambiguity and interoperability
incompatibility, not a demonstrated violation of the specification.** No stack
below is non-compliant for the form it uses. The entries in
`tests/known-bugs.tsv` start with "Specification ambiguity:": they are known
differences between stacks, not bugs.

Specification ambiguity: negative Int8/Int16 encoding. Sparkplug B defines
Metric.int_value as protobuf uint32 but does not explicitly prescribe whether
negative Int8 and Int16 values must be sign-extended to 32 bits or represented
using their native bit width. Eclipse Tahu and SparkplugNet use sign-extension,
whereas pysparkplug and srad use width-sized representations. Both encodings
preserve the original two's-complement bit pattern, but implementations differ
in decoding behavior, resulting in interoperability failures. OT Fabric follows
the Eclipse Tahu convention when encoding and accepts both representations when
decoding.

Sign-extended, -8 is `0xFFFFFFF8`; width-sized, it is `0xF8` for an Int8 and
`0xFFF8` for an Int16. For a value that is not negative the two coincide. Tahu
sign-extension is the de facto reference: the Sparkplug TCK's encoder does the
same.

| Stack | Encodes | Decodes |
|-------|---------|---------|
| Eclipse Tahu 1.0.21 (Java) | sign-extended | both: truncates to the width |
| sparkplug-payload (tahu-js) 3.2.4 | sign-extended | sign-extended only: a width-sized Int8 of -8 reads as 248 |
| SparkplugNet 1.4.1 | sign-extended | sign-extended only: a width-sized value raises `OverflowException` and the whole payload is dropped |
| pysparkplug 0.6.1 | width-sized | width-sized only: a sign-extended value raises `OverflowError` and the whole payload is dropped |
| srad 0.5.0 | width-sized | both: truncates to the width |
| sparkplughost v0.4.0 | not applicable | no decoding: it hands the host the protobuf metric |

The tahu-js and sparkplughost adapters report the low bits of the value
themselves, because a document must carry the type's value, so they read both
forms in the tables above; that is the adapter, not the library. tahu-js has
the edge role only here, and no fixture writes a negative Int8 or Int16, so its
decoding is not run.

The scalars fixture has negative Int8 and Int16 values, down to the minimum of
each. The fixture says the value and each edge encodes it the way its stack
does, so every host meets both forms: from pysparkplug and srad width-sized,
from the others sign-extended. The known differences reproduce only because
the forms differ, so a change in either stack shows as a stale entry.

Decoders that truncate also accept values that are neither form, such as `0x100`
for an Int8. No stack sends these, and a stricter decoder may reject them.

The ambiguity is worth raising with the Eclipse Sparkplug maintainers; nothing
has been filed from here.

### Tahu encodes UInt32 in long_value

The specification gives UInt32 the protobuf type `uint32`, that is
`int_value`. Eclipse Tahu 1.0.21 (`setLongValue`) and sparkplug-payload
(tahu-js) 3.2.4 write it to `long_value`; both read either field. srad 0.5.0
rejects the metric ("Value variant type was invalid"); SparkplugNet 1.4.1
reads `int_value` only and gets 0. pysparkplug and sparkplughost read both.

### Tahu host rejects a DBIRTH that reuses aliases

After a DDEATH, Tahu 1.0.21 keeps the aliases of the dead device. A DBIRTH
that announces the device again with the same aliases, as srad's does, is
rejected ("Not adding duplicated alias") and the host requests a rebirth.
Aliases are unique per edge node; a device that is born again may reuse its
own.

### Tahu publishes DDEATH before NDEATH

On a clean stop, Tahu's `EdgeClient.disconnect(true)` publishes a DDEATH for
every device before the NDEATH. Sparkplug allows it; the self-test expects
those DDEATHs.

### Metrics without a datatype

The specification requires the datatype in NBIRTH and DBIRTH and says it
SHOULD NOT be in NDATA, DDATA, NCMD and DCMD. srad 0.5.0 follows that,
also for the `bdSeq` metric of NDEATH and its will. Stacks that cannot decode
a metric without a datatype:

Tahu 1.0.21 sends its commands without datatypes too.

- pysparkplug 0.6.1, as an edge, fails on such an NCMD and disconnects; as a
  host, drops such an NDEATH, so the node stays online;
- tahu-js 3.2.4 decodes the value as `null` and applies no command;
- SparkplugNet 1.4.1, as a host, drops such an NDEATH.

### Commands carry no seq

`[tck-id-payloads-ncmd-seq]` and `[tck-id-payloads-dcmd-seq]`: NCMD and DCMD
MUST NOT include a sequence number. The SparkplugNet 1.4.1 application sets
one; pysparkplug 0.6.1, as an edge, raises `TypeError` on it.

### Metric timestamps

`[tck-id-payloads-metric-timestamp-in-...]`: every metric of NBIRTH, DBIRTH,
NDATA and DDATA MUST carry a timestamp. sparkplug-client (tahu-js) 3.2.4 adds
the `bdSeq` metric to NBIRTH without one; srad logs the payload as invalid
but keeps the birth.

### pysparkplug NDEATH carries the next bdSeq

pysparkplug 0.6.1 increments `bdSeq` right after publishing NBIRTH, so the
NDEATH of `EdgeNode.disconnect()` carries `bdSeq + 1`. Hosts that check that
the NDEATH matches the birth keep the node online.

### SparkplugNet host keeps dead nodes online

SparkplugNet 1.4.1 updates its node and device states on NDEATH and DDEATH
through a LINQ iterator that is never enumerated, so dead nodes and devices
stay online. It also resolves no aliases (`aliasResolution` false) and
tracks no `seq` (`seqCheck` false).

### SparkplugNet edge drops device writes

SparkplugNet 1.4.1 filters the metrics of a DCMD against the node's known
metrics instead of the device's, and resolves no aliases in commands: a
device write, or a write addressed by alias (srad sends them), is dropped.
Its rebirth publishes NDEATH first and it does not act on
`Node Control/Rebirth` itself (`rebirthCommand` false), and it subscribes to
the Sparkplug 2.2 STATE topic, so `--primary-host` is not supported.

### SparkplugNet sends an empty password

SparkplugNet 1.4.1 defaults the user name and password to `""` and always
passes them to MQTTnet, which then sends a password flag without a user
name. MQTT 3.1.1 forbids that (MQTT-3.1.2-22) and NanoMQ refuses the
connection. The adapter sets both to `null`, so no credentials are sent.

### Messages of one edge node arrive out of order

MQTT orders messages per topic only. A Sparkplug edge node publishes on
several topics (NBIRTH, DBIRTH per device, NDATA, DDATA per device), and
`seq` is what orders them. NanoMQ 0.25.7 and HiveMQ CE 2026.5 deliver them,
in some runs, out of publishing order: a DBIRTH before its NBIRTH, a DDATA
before an earlier NDATA. The specification has the host wait a reorder
timeout for the missing message before it requests a rebirth. The
sparkplughost host resequences by `seq` once a node is born, but not across
its NBIRTH: it clears its buffer when the NBIRTH arrives, so a DBIRTH
delivered before it is lost. The adapter runs srad's host on `AppEventLoop`,
which hands messages over in arrival order (srad's `Resequencer` serves
`generic_app` only), and checks `seq` itself, so it requests a rebirth for a
message from a node it does not know yet. pysparkplug has no host application
of its own; the adapter's host applies messages in arrival order. Each takes the gap
for a lost message and requests a rebirth, or misreads data for which it has
no birth yet. Mosquitto and EMQX kept the order in every
run. The self-test compares messages as a set ordered by `seq`, and lists
the hosts' behaviour as an intermittent known bug on the two brokers.

### NanoMQ publishes the will after an MQTT 5 DISCONNECT

srad's MQTT client (rumqttc) speaks MQTT 5 only. NanoMQ 0.25.7 publishes the
will of an MQTT 5 client after a DISCONNECT with reason code 0 (normal
disconnection), which MQTT 5 forbids, in some runs. The late NDEATH carries
the `bdSeq` of the previous session; a host that checks it ignores it, a
host that does not takes the restarted node offline. HiveMQ CE, EMQX and
Mosquitto publish no will after a normal disconnect.

### Adapter limits

- The Tahu edge assigns its own aliases (from 0, `bdSeq` included), not the
  fixture's. The Tahu host requests a rebirth for a `seq` gap only after a
  5-second reorder timeout, as the specification describes, and reports only
  an NDEATH whose `bdSeq` matches the birth.
- tahu-js has no host: sparkplug-client is an edge node library. Its `bdSeq`
  is always 0.
- sparkplughost has no edge, and connects with its host ID as the MQTT client
  identifier, so `--client-id` must equal `--host-id`.
- srad's edge publishes its devices' births in an order it does not
  guarantee across a rebirth, so it declares no `rebirthCommand`.
- pysparkplug publishes data by name (`aliases` false).

## Platforms

| Platform | Status |
|----------|--------|
| linux/amd64 | Built and self-tested natively by CI and by the release workflow |
| linux/arm64 | Built and self-tested natively by CI and by the release workflow (arm64 runner); also locally |

The adapters have no architecture-specific code.
