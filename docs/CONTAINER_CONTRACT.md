# Container contract

Version: **0.1 (draft)**. `contract_version` in [versions.yaml](../versions.yaml).

Every adapter image exposes the same commands, flags, JSON documents and exit
codes. Consumers depend on this document, not on any adapter's source. Until
1.0 it may change without a major version bump; after 1.0 a breaking change
bumps the major version and an addition the minor version. Documents carry
`schemaVersion` `"1.0"`.

## Contents

- [Image layout](#image-layout)
- [How Sparkplug B shapes this contract](#how-sparkplug-b-shapes-this-contract)
- [Commands](#commands)
- [`edge`](#edge)
- [`host`](#host)
- [`call`](#call)
- [Events](#events)
- [Metrics in documents](#metrics-in-documents)
- [Call result document](#call-result-document)
- [`print-capabilities`](#print-capabilities)
- [`print-fixture`](#print-fixture)
- [Exit codes](#exit-codes)
- [Output rules](#output-rules)
- [Gaps before 1.0](#gaps-before-10)

## Image layout

| Item | Value |
|------|-------|
| Image | `ghcr.io/otfabric/sparkplugb-interop-<adapter>` |
| Entrypoint | `sparkplugb-interop` (`/usr/local/bin/sparkplugb-interop`) |
| Default command | the adapter's first role: `edge`, or `host` for a host-only adapter |
| Protocol | Sparkplug B 3.0.0 over MQTT 3.1.1, plain TCP |
| User | non-root, uid 10001 |
| Fixtures | `/fixtures/<name>/fixture.json`, baked in; `--fixture` takes a name or a path |
| Readiness file | `/run/sparkplugb-interop/ready` |
| Control socket | `/run/sparkplugb-interop/control.sock` (see [`call`](#call)) |
| Health check | built in: `test -f /run/sparkplugb-interop/ready` |
| Licences | `/licenses/`, with `THIRD_PARTY_NOTICES.md` |

The broker is not part of any adapter image. The broker images the
self-tests use are pinned by digest in [versions.yaml](../versions.yaml) and
listed in each release's `manifest.json`.

## How Sparkplug B shapes this contract

1. **Nothing talks point to point.** Edge nodes and host applications meet
   at an MQTT broker. A test starts a broker, then containers in the roles
   it needs, all on one network.
2. **Two roles, both long-running.** `edge` is a Sparkplug Edge Node with
   the devices of a fixture. `host` is a Sparkplug Host Application that is
   the primary host of its `--host-id`. Neither exits on its own.
3. **Behaviour is triggered, never timed.** An edge publishes its births when
   it connects (or when its primary host comes online) and data only when a
   test asks it to, with [`call publish-data`](#call). Nothing depends on a
   delay, so every pairing produces the same messages.
4. **What a host receives is the result.** A host writes every Sparkplug
   message it receives as a `message` event, with aliases resolved to names
   and each metric's datatype taken from the birth. Two stacks agree when
   their events agree.
5. **Death is part of the protocol.** SIGTERM is a clean stop (an edge
   publishes NDEATH first); `docker kill` is a crash, after which the broker
   delivers the will (NDEATH, or STATE offline for a host).

## Commands

```text
sparkplugb-interop edge --broker URL [--fixture NAME|PATH] [--group G] [--edge-node N] [--primary-host ID] [flags]
sparkplugb-interop host --broker URL --host-id ID [flags]
sparkplugb-interop call <operation> [--payload JSON] [--timeout-ms N] [--control-socket PATH]
sparkplugb-interop print-capabilities
sparkplugb-interop print-fixture [NAME]
sparkplugb-interop version
```

Flags have the form `--name value`. An unknown command or flag, a flag given
twice, a missing required flag, or an invalid value is a usage error (exit
2), detected before anything touches the network. Flags may appear in any
order.

| Flag | Commands | Description |
|------|----------|-------------|
| `--broker URL` | `edge`, `host` | `tcp://host[:port]`, port 1883 by default; required |
| `--fixture NAME\|PATH` | `edge` | A baked fixture name or a path to a fixture file (default `baseline`) |
| `--group G` | `edge` | Group ID; overrides the fixture's `group` |
| `--edge-node N` | `edge` | Edge Node ID; overrides the fixture's `edgeNode` |
| `--primary-host ID` | `edge` | Wait for `spBv1.0/STATE/ID` to be online before publishing births, and rebirth when it comes back online |
| `--host-id ID` | `host` | The Host Application ID of its STATE topic; required |
| `--client-id ID` | `edge`, `host` | MQTT client identifier (default: one derived from the adapter, the role and the IDs) |
| `--connect-timeout-ms N` | `edge`, `host` | Timeout of the MQTT connection (default 10000) |
| `--ready-file PATH` | `edge`, `host` | File to create when ready (default `/run/sparkplugb-interop/ready`) |
| `--control-socket PATH` | `edge`, `host`, `call` | Unix socket of [`call`](#call) (default `/run/sparkplugb-interop/control.sock`) |
| `--payload JSON` | `call` | Operation input, a JSON object (default `{}`) |
| `--timeout-ms N` | `call` | Timeout of the operation (default 10000) |

IDs (`--group`, `--edge-node`, `--host-id`) must be valid Sparkplug topic
elements: non-empty, without `/`, `+` or `#`.

## `edge`

A Sparkplug Edge Node with the node metrics and the devices of the fixture.
It:

1. loads and validates the fixture (exit 4 when it is invalid or missing);
2. connects with a clean session and the will `spBv1.0/G/NDEATH/N` carrying
   its `bdSeq` (exit 3 when the broker cannot be reached within
   `--connect-timeout-ms`);
3. subscribes to `spBv1.0/G/NCMD/N` and `spBv1.0/G/DCMD/N/+`;
4. with `--primary-host`, subscribes to its STATE and waits until it is
   `{"online": true, ...}`;
5. publishes NBIRTH (`seq` 0) with `bdSeq`, the metric
   `Node Control/Rebirth` (Boolean, false) and every node metric of the
   fixture with its alias, datatype and value, then one DBIRTH per fixture
   device, in fixture order;
6. creates the readiness file and writes the `ready` event.

While running, it:

- answers an NCMD that writes `Node Control/Rebirth` true with a rebirth:
  NBIRTH (`seq` 0, same `bdSeq`) and every DBIRTH again, as the current
  values;
- applies an NCMD or DCMD that writes a metric the fixture marks
  `writable`, and reports the new value in an NDATA or DDATA with that one
  metric. A write to any other metric is ignored. Each command is a
  `command` event;
- with `--primary-host`, publishes NDEATH when the primary host goes
  offline, and connects anew (new `bdSeq`) and rebirths when it comes back
  online;
- publishes in data messages the alias of a metric only, when the adapter
  declares the feature `aliases`, and the name otherwise.

On SIGTERM it publishes NDEATH with its `bdSeq`, disconnects, writes
`stopped` and exits 0 within 10 seconds. When the connection is lost
otherwise, it writes `connection-lost` and exits 1.

## `host`

A Sparkplug Host Application, the primary host of `--host-id`. It:

1. connects with a clean session and the will `spBv1.0/STATE/ID`,
   `{"online": false, "timestamp": T}`, QoS 1, retained;
2. subscribes to `spBv1.0/#` and `spBv1.0/STATE/ID`;
3. publishes `{"online": true, "timestamp": T}` (the same `T`) to
   `spBv1.0/STATE/ID`, QoS 1, retained;
4. creates the readiness file and writes the `ready` event.

While running, it writes a `message` event for every NBIRTH, NDATA, NDEATH,
DBIRTH, DDATA and DDEATH it receives, in the order it receives them. It
tracks every edge node: births define the aliases and datatypes of its
metrics, NDEATH (with the `bdSeq` of the birth) and DDEATH take nodes and
devices offline. With the feature `seqCheck`, a data message whose `seq` is
not the one expected, or that comes from an edge node it has no birth for,
is a `rebirth-requested` event, and it sends that node an NCMD with
`Node Control/Rebirth` true.

On SIGTERM it publishes `{"online": false, "timestamp": T}` to its STATE
topic, disconnects, writes `stopped` and exits 0 within 10 seconds. When the
connection is lost otherwise, it writes `connection-lost` and exits 1.

## `call`

```bash
docker exec <container> sparkplugb-interop call publish-data --payload '{"update": 0}'
```

Sends an operation to the running `edge` or `host` of the container through
its control socket and prints the [call result document](#call-result-document).
A control socket that does not exist or does not answer is `not-connected`
(exit 1). An operation the running role does not offer is `unsupported`
(exit 5); the capabilities list the operations of each role.

On the socket, `call` writes one JSON line, `{"operation": "...",
"payload": {...}}`, and reads one JSON line back, the result document.

### Edge operations

| Operation | `--payload` | What it does | `ok` when |
|-----------|-------------|--------------|-----------|
| `publish-data` | `{"update": N}` | applies `updates[N]` of the fixture and publishes its node metrics as one NDATA, then the metrics of each of its devices as one DDATA, in fixture order | every message was published; `result` is `{"published": ["NDATA", "DDATA/pump1", ...]}` |
| `rebirth` | `{}` | publishes NBIRTH (`seq` 0, same `bdSeq`) and every DBIRTH of an online device | published |
| `device-death` | `{"device": "ID"}` | publishes DDEATH for the device and takes it offline | published |
| `device-birth` | `{"device": "ID"}` | publishes DBIRTH for an offline device with its current values | published |

### Host operations

| Operation | `--payload` | What it does | `ok` when |
|-----------|-------------|--------------|-----------|
| `send-command` | `{"group": G, "edgeNode": N, "device": D (optional), "metrics": [{"name", "datatype", "value"}]}` | publishes an NCMD, or a DCMD with `device` | published |
| `request-rebirth` | `{"group": G, "edgeNode": N}` | publishes an NCMD with `Node Control/Rebirth` true | published |
| `nodes` | `{}` | returns what the host tracks | always; `result` is `{"nodes": [{"group", "edgeNode", "online", "bdSeq", "devices": [{"device", "online"}]}]}`, sorted by group, edge node and device |

## Events

`edge` and `host` write JSON Lines to stdout. Every line is a JSON object
with at least `event` and `time` (RFC 3339, UTC).

### Edge events

| Event | Fields | When |
|-------|--------|------|
| `ready` | `adapter`, `fixture`, `group`, `edgeNode`, `bdSeq` | Births published, readiness file created |
| `published` | `type`, `device` (or null), `seq` (null for NDEATH), `bdSeq` (NBIRTH and NDEATH; null otherwise) | A Sparkplug message was published |
| `command` | `device` (or null), `metrics`, `applied` | An NCMD or DCMD arrived; `applied` is whether it changed a writable metric or caused a rebirth |
| `primary-host` | `hostId`, `online` | The STATE of the primary host changed |
| `connection-lost` | `detail` | The connection to the broker was lost |
| `stopped` | | Clean shutdown |

### Host events

| Event | Fields | When |
|-------|--------|------|
| `ready` | `adapter`, `hostId` | STATE online published, readiness file created |
| `message` | `type`, `group`, `edgeNode`, `device` (or null), `seq` (null for NDEATH), `bdSeq` (NBIRTH and NDEATH; null otherwise), `metrics` | A Sparkplug message arrived |
| `rebirth-requested` | `group`, `edgeNode`, `reason` (`seq-gap` or `unknown-node`) | The host requested a rebirth on its own |
| `connection-lost` | `detail` | The connection to the broker was lost |
| `stopped` | | Clean shutdown |

## Metrics in documents

Every metric in an event, a payload or a result is
`{"name": "...", "datatype": "...", "value": ...}`:

- `name` is the metric name, never an alias: a host resolves aliases with
  the birth of the node.
- `datatype` is the Sparkplug datatype name, as the
  [fixtures](FIXTURES.md#datatypes) spell them: `Int8`, `Int16`, `Int32`,
  `Int64`, `UInt8`, `UInt16`, `UInt32`, `UInt64`, `Float`, `Double`,
  `Boolean`, `String`, `DateTime`, `Text`.
- `value` is a JSON number for the integer types, `Float`, `Double` and
  `DateTime` (milliseconds since the epoch), a boolean for `Boolean`, a string
  for `String` and `Text`, and `null` for a metric whose `is_null` is set.

In a `message` event, metrics are in the order of the payload. The `bdSeq`
metric is reported in the event's `bdSeq` field, not among `metrics`. Other
metrics an upstream adds on its own (such as `Node Control/Reboot` or
`Properties/...`) are reported as they arrive; a consumer comparing stacks
ignores names that are not in the fixture or `Node Control/Rebirth`.

## Call result document

```json
{
  "schemaVersion": "1.0",
  "adapter": "tahu",
  "operation": "publish-data",
  "ok": true,
  "error": null,
  "result": {"published": ["NDATA", "DDATA/pump1", "DDATA/meter1"]},
  "elapsedMs": 4
}
```

| `error.code` | Exit | Meaning |
|--------------|------|---------|
| `invalid-request` | 1 | The payload names an unknown update, device or metric, or is not valid for the operation |
| `not-connected` | 1 | No running role answers on the control socket, or it is not connected to the broker |
| `timeout` | 1 | `--timeout-ms` elapsed |
| `unsupported` | 5 | The running role does not offer the operation |

## `print-capabilities`

Prints one JSON line, the capability document, which validates against
[schemas/capabilities.schema.json](../schemas/capabilities.schema.json). See
[docs/CAPABILITIES.md](CAPABILITIES.md).

## `print-fixture`

Prints the named baked fixture (default `baseline`) byte for byte as in this
repository. An unknown name is exit 1.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success, or a clean stop on SIGTERM |
| 1 | Operation failed, connection lost, or an unknown fixture name |
| 2 | Usage error, before any network access |
| 3 | The broker could not be reached |
| 4 | Invalid or missing fixture |
| 5 | Unsupported command, operation or fixture |

## Output rules

1. **stdout is structured.** `edge` and `host` write JSON Lines; `call` and
   `print-capabilities` one JSON line; `print-fixture` one JSON document;
   `version` one line.
2. **stderr is for humans.** Diagnostics, upstream logs and errors.
3. **No mixing.** A line on stdout that is not JSON is a contract violation,
   which the smoke test checks.

## Gaps before 1.0

- [ ] Templates, DataSets, arrays and properties are not in any fixture.
- [ ] MQTT 5, TLS and authentication to the broker are not specified.
- [ ] A host's handling of out-of-order messages (the reorder timeout) has
      no test that does not depend on timing.
- [ ] Applications that are not the primary host (no STATE) are not a role.
