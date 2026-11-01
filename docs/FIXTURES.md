# Fixtures

A fixture describes one Sparkplug Edge Node: its group and edge node IDs, the
metrics of the node and of each device with their aliases, datatypes and
initial values, which metrics a host may write, and the data updates the edge
publishes when a test asks for them. It says what an edge node *is*; it holds
no assertions and no names from any library.

Schema: [fixtures/schema/sparkplugb-fixture.schema.json](../fixtures/schema/sparkplugb-fixture.schema.json)
(`fixture_schema` in [versions.yaml](../versions.yaml)). Every image bakes the
fixtures of this repository under `/fixtures/<name>/fixture.json` and prints
them with `print-fixture`.

| Fixture | What it exercises |
|---------|-------------------|
| [`baseline`](../fixtures/baseline/fixture.json) | Node metrics, two devices, aliases, writable metrics on the node and on a device, two updates that touch the node and both devices |
| [`scalars`](../fixtures/scalars/fixture.json) | One metric of every scalar datatype, at its initial value and at its limits. Its negative Int8 and Int16 values show the [two encodings](COMPATIBILITY.md#int8-and-int16-are-encoded-two-ways): the fixture gives the value, and each edge encodes it the way its stack does |
| [`composite`](../fixtures/composite/fixture.json) | Integers inside the composite values: DataSet cells, template parameters and members, and metric properties, at the limits of each type. Stacks that have them: Tahu, tahu-js and SparkplugNet as edges; Tahu, SparkplugNet and sparkplughost as hosts. pysparkplug has none of these, srad no DataSet |

## Format

```json
{
  "schemaVersion": "1.0",
  "name": "baseline",
  "description": "...",
  "group": "interop",
  "edgeNode": "edge1",
  "node": {"metrics": [{"name": "Outputs/Setpoint", "alias": 5, "datatype": "Int32", "value": 50, "writable": true}]},
  "devices": [{"id": "pump1", "metrics": [{"name": "Status/Speed", "alias": 11, "datatype": "Float", "value": 1450.5}]}],
  "updates": [{"node": [{"name": "Outputs/Setpoint", "value": 60}], "devices": {"pump1": [{"name": "Status/Speed", "value": 1475.25}]}}]
}
```

| Field | Meaning |
|-------|---------|
| `group`, `edgeNode` | The topic IDs; `edge --group` and `--edge-node` override them |
| `node.metrics` | The node metrics of NBIRTH, in this order, after the edge's own `bdSeq` and `Node Control/Rebirth` |
| `devices[].metrics` | The metrics of each device's DBIRTH, in this order; devices are born in this order |
| `writable` | A host may write the metric with NCMD (node) or DCMD (device); the edge applies it and reports it |
| `updates[N]` | What `call publish-data {"update": N}` changes: one NDATA with the `node` changes (none when empty), then one DDATA per device in `devices`, in fixture order |

## Rules

Checked by `scripts/validate-fixtures.py` in CI, and by every adapter when it
loads a fixture (an invalid fixture is exit 4):

1. Names are unique within the node and within each device. `bdSeq` and
   names under `Node Control/` belong to the edge itself.
2. Aliases are unique across the node and all its devices (Sparkplug B 3.0.0,
   aliases are per edge node).
3. Values fit their datatype (below); updates change only metrics that
   exist, once each, with values of the metric's datatype.
4. Values of the 64-bit types and `DateTime` stay within ±(2^53 − 1), so every
   JSON parser reads them exactly; `Float` values are exactly representable
   in 32 bits, so that every stack reports the same number.

## Datatypes

| Datatype | JSON value | Range in fixtures |
|----------|------------|-------------------|
| `Int8`, `Int16`, `Int32` | number | the type's range |
| `Int64` | number | ±(2^53 − 1) |
| `UInt8`, `UInt16`, `UInt32` | number | the type's range |
| `UInt64` | number | 0 to 2^53 − 1 |
| `Float` | number | exactly representable in 32 bits |
| `Double` | number | |
| `Boolean` | boolean | |
| `String`, `Text` | string | |
| `DateTime` | number, milliseconds since the epoch | 0 to 2^53 − 1 |

## DataSets, templates and properties

```json
{"name": "Table", "alias": 1, "datatype": "DataSet",
 "value": {"columns": ["i8"], "types": ["Int8"], "rows": [[-8], [127]]}}
{"name": "SignedType", "alias": 2, "datatype": "Template",
 "value": {"version": "1.0", "isDefinition": true,
           "parameters": [{"name": "low", "type": "Int8", "value": -8}],
           "metrics": [{"name": "offset", "datatype": "Int8", "value": -8}]}}
{"name": "Instance", "alias": 3, "datatype": "Template",
 "value": {"templateRef": "SignedType", "isDefinition": false,
           "parameters": [{"name": "low", "type": "Int8", "value": -1}],
           "metrics": [{"name": "offset", "datatype": "Int8", "value": -1}]}}
{"name": "Scaled", "alias": 4, "datatype": "Int32", "value": -5,
 "properties": {"low": {"type": "Int8", "value": -8}}}
```

Cells, parameters, members and properties have the scalar datatypes above and
their ranges. `templateRef` is the name of the metric of the definition, which
is in the node's metrics. `properties` go out in births; updates change a value
only, and a DataSet or Template by a whole new value. Keys of a document are
not ordered: consumers compare them as sets.

## Unsupported datatypes

An adapter whose upstream cannot carry a datatype does not substitute another:
it leaves the fixture out of its capabilities (`fixtures`), and `edge` with
that fixture is exit 5.
