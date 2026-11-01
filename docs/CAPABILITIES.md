# Capabilities

`sparkplugb-interop print-capabilities` prints one JSON line that says what an
image does. It validates against
[schemas/capabilities.schema.json](../schemas/capabilities.schema.json). A
capability is declared only when the self-tests exercise it: a test skips a
pairing that lacks what it needs, and fails one that declares it but does not
do it.

```json
{
  "schemaVersion": "1.0",
  "adapter": "tahu",
  "adapterVersion": "v0.1.0",
  "protocol": "sparkplug-b-3.0.0",
  "upstream": {"name": "Eclipse Tahu", "version": "1.0.21", "license": "EPL-2.0", "language": "Java", "independentEngine": true},
  "roles": {
    "edge": {
      "supported": true,
      "fixtures": ["baseline", "scalars"],
      "calls": ["publish-data", "rebirth", "device-death", "device-birth"],
      "features": {"aliases": true, "rebirthCommand": true, "writes": true, "primaryHost": true, "gracefulDeath": true}
    },
    "host": {
      "supported": true,
      "datatypes": ["Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64", "Float", "Double", "Boolean", "String", "DateTime", "Text"],
      "calls": ["send-command", "request-rebirth", "nodes"],
      "features": {"aliasResolution": true, "seqCheck": true, "gracefulOffline": true}
    }
  }
}
```

## Fields

| Field | Meaning |
|-------|---------|
| `adapterVersion` | The release of this repository the image belongs to (`dev` for a local build) |
| `upstream.version` | The upstream release the adapter is built on |
| `upstream.independentEngine` | `false` when the Sparkplug engine is another adapter's (for example a binding of it) |
| `roles.edge.fixtures` | The baked fixtures `edge` can be an edge node of; any other is exit 5 |
| `roles.host.datatypes` | The datatypes a host decodes and reports; a test skips a fixture with another. A host with `DataSet` and `Template` also reports metric properties |
| `roles.*.calls` | The [`call` operations](CONTAINER_CONTRACT.md#call) of the role |

## Edge features

| Feature | When `true` |
|---------|-------------|
| `aliases` | Data messages carry the alias of each metric, not its name |
| `rebirthCommand` | An NCMD `Node Control/Rebirth` true makes the edge rebirth |
| `writes` | NCMD and DCMD writes of `writable` metrics are applied and reported in NDATA or DDATA |
| `primaryHost` | `--primary-host` is supported: births wait for the host's STATE online |
| `gracefulDeath` | SIGTERM publishes NDEATH before the disconnect |

## Host features

| Feature | When `true` |
|---------|-------------|
| `aliasResolution` | Metrics of data messages that carry only an alias are reported by name |
| `seqCheck` | A `seq` gap or data from an unknown node is a `rebirth-requested` event and an NCMD rebirth |
| `gracefulOffline` | SIGTERM publishes STATE offline before the disconnect |

The capabilities of every image of a release are in its release notes and in
[docs/COMPATIBILITY.md](COMPATIBILITY.md).
